package gitwatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Backend is the small filesystem-watching contract used by Git realtime.
type Backend interface {
	Events() <-chan Event
	Diagnostics() Diagnostics
	Close() error
}

// Scope identifies which part of a repository was invalidated.
type Scope string

const (
	ScopeWorktree Scope = "worktree"
	ScopeMetadata Scope = "metadata"
)

// Event is a conservative invalidation. RebuildRequired means the consumer
// must not claim realtime consistency until it has rebuilt the watcher.
type Event struct {
	Path            string
	Scope           Scope
	RebuildRequired bool
	Err             error
}

// Diagnostics contains cumulative counters. It is intentionally read as one
// snapshot so callers can expose it without logging every raw event.
type Diagnostics struct {
	BackendKind      string
	WatchedDirs      int
	RawEvents        uint64
	Overflows        uint64
	RebuildRequired  uint64
	WatchAddFailures uint64
}

// Config describes one worktree and its worktree-specific and shared Git
// metadata. Git metadata is excluded from the worktree crawl and watched via
// dedicated roots instead.
type Config struct {
	WorktreeRoot string
	GitDir       string
	CommonDir    string
}

type nativeWatcher interface {
	Add(string) error
	Close() error
	EventsChan() <-chan fsnotify.Event
	ErrorsChan() <-chan error
}

type fsnotifyAdapter struct{ *fsnotify.Watcher }

func (w fsnotifyAdapter) EventsChan() <-chan fsnotify.Event { return w.Events }
func (w fsnotifyAdapter) ErrorsChan() <-chan error          { return w.Errors }

type watcher struct {
	native  nativeWatcher
	events  chan Event
	closing chan struct{}
	done    chan struct{}

	mu              sync.RWMutex
	watched         map[string]Scope
	worktreeRoot    string
	excluded        []string
	metadataRoots   []string
	metadataAnchors []string
	diagnostics     Diagnostics
	closeOnce       sync.Once
}

// NewFSNotify performs one setup crawl, then returns a backend which blocks on
// native filesystem events. Any incomplete setup fails rather than silently
// degrading consistency.
func NewFSNotify(config Config) (Backend, error) {
	native, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}
	backend, err := newWithNative(config, fsnotifyAdapter{native})
	if err != nil {
		_ = native.Close()
		return nil, err
	}
	return backend, nil
}

func newWithNative(config Config, native nativeWatcher) (*watcher, error) {
	worktreeRoot, err := requiredDirectory(config.WorktreeRoot, "worktree root")
	if err != nil {
		return nil, err
	}
	gitDir := cleanOptional(config.GitDir)
	commonDir := cleanOptional(config.CommonDir)

	w := &watcher{
		native:       native,
		events:       make(chan Event, 64),
		closing:      make(chan struct{}),
		done:         make(chan struct{}),
		watched:      make(map[string]Scope),
		worktreeRoot: worktreeRoot,
		diagnostics:  Diagnostics{BackendKind: "fsnotify"},
	}
	w.excluded = uniquePaths(gitDir, commonDir)
	w.metadataAnchors = uniquePaths(gitDir, commonDir)
	w.metadataRoots = metadataRecursiveRoots(gitDir, commonDir)

	if err := w.addRecursive(worktreeRoot, ScopeWorktree, w.isWorktreeExcluded); err != nil {
		return nil, fmt.Errorf("watch worktree: %w", err)
	}
	if err := w.addDirectory(filepath.Dir(worktreeRoot), ScopeWorktree); err != nil {
		return nil, fmt.Errorf("watch worktree parent %q: %w", filepath.Dir(worktreeRoot), err)
	}
	for _, anchor := range w.metadataAnchors {
		if err := w.addDirectory(anchor, ScopeMetadata); err != nil {
			return nil, fmt.Errorf("watch Git metadata root %q: %w", anchor, err)
		}
		if err := w.addDirectory(filepath.Dir(anchor), ScopeMetadata); err != nil {
			return nil, fmt.Errorf("watch Git metadata parent %q: %w", filepath.Dir(anchor), err)
		}
	}
	for _, root := range w.metadataRoots {
		if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
			if err := w.addRecursive(root, ScopeMetadata, nil); err != nil {
				return nil, fmt.Errorf("watch Git metadata tree %q: %w", root, err)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect Git metadata tree %q: %w", root, statErr)
		}
	}
	go w.run()
	return w, nil
}

func (w *watcher) Events() <-chan Event { return w.events }

func (w *watcher) Diagnostics() Diagnostics {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.diagnostics
}

func (w *watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.closing)
		err = w.native.Close()
		<-w.done
	})
	return err
}

func (w *watcher) run() {
	defer close(w.done)
	defer close(w.events)
	for {
		select {
		case <-w.closing:
			return
		case event, ok := <-w.native.EventsChan():
			if !ok {
				w.emitInconsistency(errors.New("fsnotify event channel closed"))
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.native.ErrorsChan():
			if !ok {
				w.emitInconsistency(errors.New("fsnotify error channel closed"))
				return
			}
			w.mu.Lock()
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.diagnostics.Overflows++
			}
			w.mu.Unlock()
			w.emitInconsistency(fmt.Errorf("fsnotify error: %w", err))
			return
		}
	}
}

func (w *watcher) handleEvent(event fsnotify.Event) {
	path := filepath.Clean(event.Name)
	w.mu.Lock()
	w.diagnostics.RawEvents++
	scope, watchedPath := w.watched[path]
	w.mu.Unlock()
	if scope == "" {
		scope = w.scopeFor(path)
	}

	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if recursiveScope, ok := w.recursiveScopeFor(path); ok {
				if err := w.addRecursive(path, recursiveScope, w.exclusionFor(recursiveScope)); err != nil {
					w.emitInconsistency(fmt.Errorf("watch new directory %q: %w", path, err))
					return
				}
			}
		}
	}
	if (event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename)) &&
		((watchedPath && w.directoryMissing(path)) || w.isRootOrAnchor(path)) {
		w.emitInconsistency(fmt.Errorf("watched directory or root was removed or renamed: %s", path))
		return
	}
	w.emit(Event{Path: path, Scope: scope})
}

func (w *watcher) emit(event Event) {
	select {
	case w.events <- event:
	case <-w.closing:
	}
}

func (w *watcher) emitInconsistency(err error) {
	w.mu.Lock()
	w.diagnostics.RebuildRequired++
	w.mu.Unlock()
	w.emit(Event{RebuildRequired: true, Err: err})
}

func (w *watcher) addRecursive(root string, scope Scope, exclude func(string) bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		path = filepath.Clean(path)
		if exclude != nil && exclude(path) {
			return filepath.SkipDir
		}
		return w.addDirectory(path, scope)
	})
}

func (w *watcher) addDirectory(path string, scope Scope) error {
	path = filepath.Clean(path)
	w.mu.RLock()
	_, exists := w.watched[path]
	w.mu.RUnlock()
	if exists {
		return nil
	}
	if err := w.native.Add(path); err != nil {
		w.mu.Lock()
		w.diagnostics.WatchAddFailures++
		w.mu.Unlock()
		return err
	}
	w.mu.Lock()
	w.watched[path] = scope
	w.diagnostics.WatchedDirs = len(w.watched)
	w.mu.Unlock()
	return nil
}

func (w *watcher) isWorktreeExcluded(path string) bool {
	for _, excluded := range w.excluded {
		if sameOrBelow(path, excluded) {
			return true
		}
	}
	return false
}

func (w *watcher) exclusionFor(scope Scope) func(string) bool {
	if scope == ScopeWorktree {
		return w.isWorktreeExcluded
	}
	return nil
}

func (w *watcher) recursiveScopeFor(path string) (Scope, bool) {
	if sameOrBelow(path, w.worktreeRoot) && !w.isWorktreeExcluded(path) {
		return ScopeWorktree, true
	}
	for _, root := range w.metadataRoots {
		if sameOrBelow(path, root) {
			return ScopeMetadata, true
		}
	}
	return "", false
}

func (w *watcher) scopeFor(path string) Scope {
	for _, anchor := range w.metadataAnchors {
		if sameOrBelow(path, anchor) || filepath.Dir(anchor) == filepath.Dir(path) {
			return ScopeMetadata
		}
	}
	return ScopeWorktree
}

func (w *watcher) directoryMissing(path string) bool {
	info, err := os.Stat(path)
	return err != nil || !info.IsDir()
}

func (w *watcher) isRootOrAnchor(path string) bool {
	if path == w.worktreeRoot {
		return true
	}
	for _, anchor := range w.metadataAnchors {
		if path == anchor {
			return true
		}
	}
	return false
}

func requiredDirectory(path, label string) (string, error) {
	clean := cleanOptional(path)
	if clean == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", label)
	}
	return clean, nil
}

func cleanOptional(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func uniquePaths(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = cleanOptional(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func metadataRecursiveRoots(gitDir, commonDir string) []string {
	var roots []string
	for _, base := range uniquePaths(gitDir, commonDir) {
		for _, relative := range []string{"refs", "logs", "rebase-apply", "rebase-merge", "sequencer"} {
			roots = append(roots, filepath.Join(base, relative))
		}
	}
	return uniquePaths(roots...)
}

func sameOrBelow(path, root string) bool {
	if path == root {
		return true
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
