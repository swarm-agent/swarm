package artifact

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveLibraryRoot returns the configured absolute working-copy directory, or
// an XDG/platform cache-backed default when no override is configured.
func ResolveLibraryRoot(configured, userCacheRoot string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("artifact working-copy directory must be absolute")
		}
		return filepath.Clean(configured), nil
	}
	userCacheRoot = strings.TrimSpace(userCacheRoot)
	if userCacheRoot == "" || !filepath.IsAbs(userCacheRoot) {
		return "", errors.New("artifact working-copy directory is not configured and no usable user cache exists")
	}
	return filepath.Join(filepath.Clean(userCacheRoot), "swarm", "artifacts"), nil
}

// EnsureLibraryRoot creates an absolute working-copy directory without traversing
// symlinks. Existing path components must be real directories.
func EnsureLibraryRoot(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("artifact working-copy root must be absolute")
	}
	volume := filepath.VolumeName(root)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(root, current)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create artifact working-copy directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact working-copy path contains a symlink or non-directory")
		}
	}
	return nil
}
