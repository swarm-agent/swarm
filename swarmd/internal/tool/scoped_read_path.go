package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveExistingReadPath applies the read/list tools' path containment to a
// path that another reader (for example a read-only shell command) will open.
// requested is resolved against scope.PrimaryPath with every symlink followed;
// it must exist, be a regular file or directory, avoid protected storage and
// kernel pseudo-filesystems, and lie inside the scope's read roots (primary,
// additional and read-only roots). It returns the real path.
func ResolveExistingReadPath(scope WorkspaceScope, requested string) (string, error) {
	primary := strings.TrimSpace(scope.PrimaryPath)
	requested = strings.TrimSpace(requested)
	if primary == "" || requested == "" {
		return "", errors.New("workspace path and requested path are required")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(primary, candidate)
	}
	candidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("path %q does not resolve: %w", requested, err)
	}
	for _, pseudo := range []string{"/proc", "/sys", "/dev"} {
		if pathWithinProtected(pseudo, resolved) {
			return "", fmt.Errorf("path %q resolves into %s", requested, pseudo)
		}
	}
	if err := refuseProtectedPath(candidate, resolved); err != nil {
		return "", err
	}
	if err := refuseRootContainingStorage(resolved); err != nil {
		return "", err
	}
	if !workspaceGitAdminAllowed(scope, resolved) || !pathWithinAllowedRoots(resolveAllowedRoots(scope), resolved) {
		return "", fmt.Errorf("path %q escapes workspace scope", requested)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return "", fmt.Errorf("path %q is not a regular file or directory", requested)
	}
	return resolved, nil
}
