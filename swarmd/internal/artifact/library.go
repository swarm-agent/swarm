package artifact

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveLibraryRoot returns the configured absolute artifact library, or the
// visible user-local default when no override is configured.
func ResolveLibraryRoot(configured, userHome string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("artifact library directory must be absolute")
		}
		return filepath.Clean(configured), nil
	}
	userHome = strings.TrimSpace(userHome)
	if userHome == "" || !filepath.IsAbs(userHome) {
		return "", errors.New("artifact library directory is not configured and no usable user home exists")
	}
	return filepath.Join(filepath.Clean(userHome), "Swarm", "Artifacts"), nil
}

// EnsureLibraryRoot creates an absolute library directory without traversing
// symlinks. Existing path components must be real directories.
func EnsureLibraryRoot(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("artifact library root must be absolute")
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
				return fmt.Errorf("create artifact library directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact library path contains a symlink or non-directory")
		}
	}
	return nil
}
