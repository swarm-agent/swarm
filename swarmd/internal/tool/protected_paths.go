package tool

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// ErrProtectedPath reports a path inside Swarm's own storage or the daemon
// user's credential directories. File tools refuse it outright, whatever the
// permission decision or workspace grant, symlinks included.
var ErrProtectedPath = errors.New("path is inside Swarm's private storage or credentials; tools never access it")

type protectedPathSet struct {
	storage     []string // daemon storage roots: never inside, never containing
	credentials []string // credential directories: never inside
}

var protectedPaths atomic.Pointer[protectedPathSet]

// SetProtectedPaths installs the daemon's storage roots and the credential
// directories file tools must never reach. The daemon calls it once at
// startup with the same set the agent sandbox refuses to mount.
func SetProtectedPaths(storage, credentials []string) {
	set := &protectedPathSet{storage: cleanProtected(storage), credentials: cleanProtected(credentials)}
	protectedPaths.Store(set)
}

func cleanProtected(paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		clean := filepath.Clean(p)
		out = append(out, clean)
		if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
			out = append(out, resolved)
		}
	}
	return out
}

// refuseProtectedPath fails when any of paths (pass both the requested and
// the symlink-resolved form) is a protected path or inside one.
func refuseProtectedPath(paths ...string) error {
	set := protectedPaths.Load()
	if set == nil {
		return nil
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		path = filepath.Clean(path)
		for _, root := range append(append([]string(nil), set.storage...), set.credentials...) {
			if pathWithinProtected(root, path) {
				return fmt.Errorf("%w: %s", ErrProtectedPath, path)
			}
		}
	}
	return nil
}

// refuseRootContainingStorage fails when root contains a daemon storage root,
// so a broad workspace grant (for example "/" or "/var") cannot be used to
// list, search or open files below it through an in-root symlink swap.
func refuseRootContainingStorage(root string) error {
	set := protectedPaths.Load()
	if set == nil || strings.TrimSpace(root) == "" {
		return nil
	}
	root = filepath.Clean(root)
	for _, storage := range set.storage {
		if pathWithinProtected(root, storage) {
			return fmt.Errorf("%w: %s contains %s", ErrProtectedPath, root, storage)
		}
	}
	return nil
}

func pathWithinProtected(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
