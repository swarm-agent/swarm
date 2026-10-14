package sandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Git-specific hardening of a project's sandbox.
//
// Hooks and configuration are the two places a repository can name programs
// to run. A project root is mounted read-write so agents can commit, but its
// .git/hooks directory and .git/config file are bind-mounted read-only on top
// of it: an agent can neither plant a hook nor set core.hooksPath,
// core.fsmonitor, filters or a credential helper that would run later, for
// example when the owner's own Git opens the repository on the host.
// Objects, refs, the index and linked worktrees stay writable, so commits and
// `git worktree add` keep working. A guard that appears later (a repository
// initialised after the sandbox was created) changes the spec hash, so the
// sandbox is recreated with it.

// gitGuardMounts lists root's .git/hooks directory and .git/config file when
// they exist, as read-only mounts at their own paths.
func gitGuardMounts(root string) []FileMount {
	root = filepath.Clean(root)
	var out []FileMount
	hooks := filepath.Join(root, ".git", "hooks")
	if info, err := os.Lstat(hooks); err == nil && info.IsDir() {
		out = append(out, FileMount{HostPath: hooks, Dest: hooks})
	}
	config := filepath.Join(root, ".git", "config")
	if info, err := os.Lstat(config); err == nil && info.Mode().IsRegular() {
		out = append(out, FileMount{HostPath: config, Dest: config})
	}
	return out
}

func gitGuardSpec(guards []FileMount) []string {
	parts := make([]string, 0, len(guards))
	for _, g := range guards {
		parts = append(parts, "ro:"+g.HostPath)
	}
	return parts
}

// Folders the owner made visible through a host change belong to another
// user, and Git refuses to work in a repository it does not own ("dubious
// ownership"). The daemon, and agents in the sandbox (which runs as the
// daemon's uid), need safe.directory for exactly those repositories. They are
// passed as GIT_CONFIG_* environment, never written to any config file, for
// every registered project root and worktree bucket whose owner is not the
// daemon's uid.

// safeDirectoryTTL bounds how long the owner check is reused.
const safeDirectoryTTL = projectCacheTTL

// safeDirectories returns the registered project roots and worktree buckets
// owned by another uid, sorted, cached briefly.
func (m *Manager) safeDirectories() []string {
	if m == nil {
		return nil
	}
	m.safeMu.Lock()
	if m.safeDirs != nil && time.Since(m.safeDirsAt) < safeDirectoryTTL {
		out := append([]string(nil), m.safeDirs...)
		m.safeMu.Unlock()
		return out
	}
	m.safeMu.Unlock()

	roots, layout, err := m.projectRoots()
	if err != nil {
		return nil
	}
	owner := m.owner
	if owner == nil {
		owner = fileOwnerUID
	}
	seen := map[string]struct{}{}
	dirs := make([]string, 0)
	consider := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		if uid, ok := owner(path); ok && uid != m.cfg.UID {
			dirs = append(dirs, path)
		}
	}
	for _, root := range roots {
		consider(root)
		if layout.Bucket != nil && layout.WorktreesRoot != "" {
			if bucket, err := layout.Bucket(root); err == nil && bucket != "" {
				consider(filepath.Join(layout.WorktreesRoot, bucket))
			}
		}
	}
	sort.Strings(dirs)
	m.safeMu.Lock()
	m.safeDirs = dirs
	m.safeDirsAt = time.Now()
	m.safeMu.Unlock()
	return append([]string(nil), dirs...)
}

// AppendSafeDirectoryEnv adds safe.directory entries for dirs to env in Git's
// GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n form, continuing a
// count the environment already carries.
func AppendSafeDirectoryEnv(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	count := 0
	out := make([]string, 0, len(env)+2*len(dirs)+1)
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if key == "GIT_CONFIG_COUNT" {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				count = n
			}
			continue
		}
		out = append(out, kv)
	}
	for i, dir := range dirs {
		n := strconv.Itoa(count + i)
		out = append(out, "GIT_CONFIG_KEY_"+n+"=safe.directory", "GIT_CONFIG_VALUE_"+n+"="+dir)
	}
	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count+len(dirs)))
	return out
}

// safeDirectoryEnvMap is AppendSafeDirectoryEnv for a map environment; it
// returns a new map and leaves env untouched.
func safeDirectoryEnvMap(env map[string]string, dirs []string) map[string]string {
	if len(dirs) == 0 {
		return env
	}
	out := make(map[string]string, len(env)+2*len(dirs)+1)
	for k, v := range env {
		out[k] = v
	}
	count := 0
	if n, err := strconv.Atoi(strings.TrimSpace(out["GIT_CONFIG_COUNT"])); err == nil && n > 0 {
		count = n
	}
	for i, dir := range dirs {
		n := strconv.Itoa(count + i)
		out["GIT_CONFIG_KEY_"+n] = "safe.directory"
		out["GIT_CONFIG_VALUE_"+n] = dir
	}
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(count + len(dirs))
	return out
}
