package tool

import (
	"path/filepath"
	"strings"
)

// Coder source-read authority does not expose another worktree's Git admin
// directory. Only its explicitly granted per-worktree admin root is readable.
func workspaceGitAdminAllowed(scope WorkspaceScope, candidate string) bool {
	if !scope.RejectScopeExpansion {
		return true
	}
	normalized := filepath.ToSlash(filepath.Clean(candidate))
	if !strings.Contains(normalized, "/.git/") && !strings.HasSuffix(normalized, "/.git") {
		return true
	}
	for _, root := range scope.ReadOnlyRoots {
		clean := filepath.ToSlash(filepath.Clean(root))
		if strings.Contains(clean, "/.git/worktrees/") && pathWithinAllowedRoots([]string{root}, candidate) {
			return true
		}
	}
	return pathWithinAllowedRoots([]string{scope.PrimaryPath}, candidate)
}
