package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const gitWatcherDebounce = 150 * time.Millisecond

func newRepoGitWatcher(path string) (*repoGitWatcher, error) {
	target := normalizePath(path)
	if target == "" {
		return nil, errors.New("path is required")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	repoRoot, gitDir, commonDir, err := resolveGitWatchPaths(target)
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}
	w := &repoGitWatcher{
		path:      target,
		repoRoot:  repoRoot,
		gitDir:    gitDir,
		commonDir: commonDir,
		watched:   make(map[string]struct{}, 64),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
		debounce:  make(chan struct{}, 1),
		watcher:   watcher,
	}
	if err := w.addRecursiveWorktreeWatches(); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	for _, root := range watchRootsForGitPaths(gitDir, commonDir) {
		for _, candidate := range candidateGitWatchPaths(root) {
			if err := w.addWatchIfExists(candidate); err != nil {
				_ = watcher.Close()
				return nil, err
			}
		}
	}
	return w, nil
}

func (w *repoGitWatcher) run(refresh func()) {
	if w == nil {
		return
	}
	defer close(w.stopped)
	defer func() {
		if w.watcher != nil {
			_ = w.watcher.Close()
		}
	}()
	if refresh == nil {
		refresh = func() {}
	}
	refresh()
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	for {
		select {
		case <-w.stop:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			return
		case <-w.debounce:
			if timer == nil {
				timer = time.NewTimer(gitWatcherDebounce)
				timerC = timer.C
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(gitWatcherDebounce)
			timerC = timer.C
		case <-timerC:
			refresh()
			timerC = nil
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.signalRefresh()
		}
	}
}

func (w *repoGitWatcher) signalRefresh() {
	if w == nil {
		return
	}
	select {
	case w.debounce <- struct{}{}:
	default:
	}
}

func (w *repoGitWatcher) stopWatching() {
	if w == nil {
		return
	}
	select {
	case <-w.stopped:
		return
	default:
	}
	close(w.stop)
	<-w.stopped
}

func (w *repoGitWatcher) handleEvent(event fsnotify.Event) {
	if w == nil {
		return
	}
	name := normalizePath(event.Name)
	if name == "" {
		w.signalRefresh()
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
		_ = w.addRecursiveFromPath(name)
	}
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		w.removeWatchPath(name)
	}
	w.signalRefresh()
}

func (w *repoGitWatcher) addRecursiveWorktreeWatches() error {
	if w == nil {
		return nil
	}
	return filepath.WalkDir(w.repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		clean := normalizePath(path)
		if clean == "" {
			return nil
		}
		if w.shouldSkipDir(clean) {
			return filepath.SkipDir
		}
		return w.addWatchPath(clean)
	})
}

func (w *repoGitWatcher) addRecursiveFromPath(path string) error {
	if w == nil {
		return nil
	}
	clean := normalizePath(path)
	if clean == "" {
		return nil
	}
	info, err := os.Stat(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if w.shouldSkipDir(clean) {
		return nil
	}
	return filepath.WalkDir(clean, func(child string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		normalized := normalizePath(child)
		if normalized == "" {
			return nil
		}
		if w.shouldSkipDir(normalized) {
			return filepath.SkipDir
		}
		return w.addWatchPath(normalized)
	})
}

func (w *repoGitWatcher) addWatchIfExists(path string) error {
	if w == nil {
		return nil
	}
	clean := normalizePath(path)
	if clean == "" {
		return nil
	}
	info, err := os.Stat(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return w.addWatchPath(clean)
}

func (w *repoGitWatcher) addWatchPath(path string) error {
	if w == nil || w.watcher == nil {
		return nil
	}
	clean := normalizePath(path)
	if clean == "" {
		return nil
	}
	if _, ok := w.watched[clean]; ok {
		return nil
	}
	if err := w.watcher.Add(clean); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	w.watched[clean] = struct{}{}
	return nil
}

func (w *repoGitWatcher) removeWatchPath(path string) {
	if w == nil || w.watcher == nil {
		return
	}
	clean := normalizePath(path)
	if clean == "" {
		return
	}
	for watched := range w.watched {
		if watched == clean || strings.HasPrefix(watched, clean+string(filepath.Separator)) {
			_ = w.watcher.Remove(watched)
			delete(w.watched, watched)
		}
	}
}

func (w *repoGitWatcher) shouldSkipDir(path string) bool {
	if w == nil {
		return false
	}
	clean := normalizePath(path)
	if clean == "" {
		return true
	}
	for _, root := range watchRootsForGitPaths(w.gitDir, w.commonDir) {
		if root != "" && (clean == root || strings.HasPrefix(clean, root+string(filepath.Separator))) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	switch base {
	case ".git":
		return true
	case ".swarm", "node_modules":
		return true
	}
	return false
}

func resolveGitWatchPaths(path string) (string, string, string, error) {
	repoRootRaw, err := runGitPathQuery(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", err
	}
	repoRoot, err := resolveReportedGitPath(path, repoRootRaw)
	if err != nil {
		return "", "", "", err
	}
	gitDirRaw, err := runGitPathQuery(path, "rev-parse", "--git-dir")
	if err != nil {
		return "", "", "", err
	}
	gitDir, err := resolveReportedGitPath(path, gitDirRaw)
	if err != nil {
		return "", "", "", err
	}
	commonDirRaw, err := runGitPathQuery(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return repoRoot, gitDir, "", nil
	}
	commonDir, err := resolveReportedGitPath(path, commonDirRaw)
	if err != nil {
		return repoRoot, gitDir, "", nil
	}
	return repoRoot, gitDir, commonDir, nil
}

func resolveReportedGitPath(basePath, reportedPath string) (string, error) {
	basePath = strings.TrimSpace(basePath)
	reportedPath = strings.TrimSpace(reportedPath)
	if reportedPath == "" {
		return "", errors.New("git path is empty")
	}
	if !filepath.IsAbs(reportedPath) {
		if basePath == "" {
			return "", errors.New("base path is required for relative git path")
		}
		reportedPath = filepath.Join(basePath, reportedPath)
	}
	resolvedPath, err := filepath.Abs(reportedPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolvedPath), nil
}

func runGitPathQuery(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs, "--no-optional-locks", "-C", path)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func watchRootsForGitPaths(gitDir, commonDir string) []string {
	roots := []string{normalizePath(gitDir)}
	common := normalizePath(commonDir)
	if common != "" && common != roots[0] {
		roots = append(roots, common)
	}
	return roots
}

func candidateGitWatchPaths(gitDir string) []string {
	candidates := []string{
		gitDir,
		filepath.Join(gitDir, "refs"),
		filepath.Join(gitDir, "refs", "heads"),
		filepath.Join(gitDir, "refs", "remotes"),
		filepath.Join(gitDir, "refs", "tags"),
		filepath.Join(gitDir, "logs"),
		filepath.Join(gitDir, "logs", "refs"),
		filepath.Join(gitDir, "logs", "refs", "heads"),
		filepath.Join(gitDir, "logs", "refs", "remotes"),
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		clean := filepath.Clean(strings.TrimSpace(candidate))
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}
