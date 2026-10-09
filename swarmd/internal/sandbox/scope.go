package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Projects lists the canonical root of every project agents may write to
// (the workspace catalog across accounts).
type Projects func() ([]string, error)

// WorktreeBucket maps a project root to its worktree bucket directory name.
type WorktreeBucket func(root string) (string, error)

// Layout tells the manager where agent-writable directories live.
type Layout struct {
	Projects Projects
	// WorktreesRoot holds one bucket directory per project; each project's
	// sandbox mounts its own bucket.
	WorktreesRoot string
	Bucket        WorktreeBucket
}

type layoutCache struct {
	mu      sync.Mutex
	layout  Layout
	roots   []string
	fetched time.Time
}

const projectCacheTTL = 5 * time.Second

var errUnknownWorktree = errors.New("worktree does not belong to a known project")

// SetLayout installs the project layout used to resolve paths to sandboxes.
func (m *Manager) SetLayout(layout Layout) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.layout = &layoutCache{layout: layout}
}

func (m *Manager) projectRoots() ([]string, Layout, error) {
	m.mu.Lock()
	cache := m.layout
	used := make([]string, 0, len(m.used))
	for r := range m.used {
		used = append(used, r)
	}
	m.mu.Unlock()
	if cache == nil {
		return used, Layout{}, nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.layout.Projects != nil && (cache.roots == nil || time.Since(cache.fetched) > projectCacheTTL) {
		roots, err := cache.layout.Projects()
		if err != nil {
			return nil, cache.layout, fmt.Errorf("list projects for sandbox: %w", err)
		}
		cache.roots = make([]string, 0, len(roots))
		for _, r := range roots {
			if c := resolveForCompare(r); c != "" {
				cache.roots = append(cache.roots, c)
			}
		}
		cache.fetched = time.Now()
	}
	return append(append([]string(nil), cache.roots...), used...), cache.layout, nil
}

// resolveExisting resolves symlinks on the longest existing prefix of path.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	suffix := ""
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(path, suffix)
		}
		suffix = filepath.Join(filepath.Base(path), suffix)
		path = parent
	}
}

// ScopeFor resolves an agent-writable path to its project's sandbox scope.
// ok is false when the path belongs to no project or worktree bucket, i.e.
// agents cannot write there.
func (m *Manager) ScopeFor(path string) (Scope, bool, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return Scope{}, false, fmt.Errorf("sandbox scope requires an absolute path, got %q", path)
	}
	target := resolveExisting(path)
	roots, layout, err := m.projectRoots()
	if err != nil {
		return Scope{}, false, err
	}
	worktrees := resolveForCompare(layout.WorktreesRoot)
	if worktrees != "" && within(worktrees, target) && target != worktrees {
		rel, _ := filepath.Rel(worktrees, target)
		bucket := strings.Split(rel, string(filepath.Separator))[0]
		for _, root := range roots {
			if b, err := layout.Bucket(root); err == nil && b == bucket {
				return m.scopeForRoot(root, layout)
			}
		}
		return Scope{}, false, fmt.Errorf("%w: %s", errUnknownWorktree, target)
	}
	best := ""
	for _, root := range roots {
		if within(root, target) && len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return Scope{}, false, nil
	}
	return m.scopeForRoot(best, layout)
}

// CommandScope is ScopeFor for agent commands: a working directory outside
// every known project becomes its own project root, so the command still runs
// sandboxed (and the directory is remembered as sandboxed).
func (m *Manager) CommandScope(path string) (Scope, error) {
	scope, ok, err := m.ScopeFor(path)
	if err != nil {
		return Scope{}, err
	}
	if ok {
		return scope, nil
	}
	_, layout, err := m.projectRoots()
	if err != nil {
		return Scope{}, err
	}
	scope, _, err = m.scopeForRoot(resolveExisting(path), layout)
	return scope, err
}

func (m *Manager) scopeForRoot(root string, layout Layout) (Scope, bool, error) {
	scope := Scope{Root: root, Mounts: []string{root}}
	if layout.Bucket != nil && layout.WorktreesRoot != "" {
		bucket, err := layout.Bucket(root)
		if err != nil {
			return Scope{}, false, fmt.Errorf("worktree bucket for %s: %w", root, err)
		}
		// Created up front so the sandbox's mounts do not change (forcing a
		// recreate) when the first worktree is added.
		dir := filepath.Join(layout.WorktreesRoot, bucket)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Scope{}, false, fmt.Errorf("create worktree bucket: %w", err)
		}
		scope.Mounts = append(scope.Mounts, dir)
	}
	return scope, true, nil
}
