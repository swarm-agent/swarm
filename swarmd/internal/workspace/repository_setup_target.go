package workspace

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// WorkspaceGitIdentityName and WorkspaceGitIdentityEmail are the non-personal
// repository-local committer identity Swarm writes into a repository it
// initialises when no identity is configured, matching the headless install's
// "Swarm agent" identity on a reserved .invalid domain.
const (
	WorkspaceGitIdentityName  = "Swarm agent"
	WorkspaceGitIdentityEmail = "swarm@localhost.invalid"
)

// systemRepositorySetupRoots are directories repository setup never creates or
// initialises a workspace in, including the Linux daemon storage defaults.
var systemRepositorySetupRoots = []string{
	"/etc", "/private/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64",
	"/proc", "/sys", "/dev", "/boot", "/System",
	"/var/lib/swarmd", "/var/cache/swarmd", "/var/log/swarmd", "/run/swarmd",
}

// SetProtectedRoots registers the daemon's configured storage roots (data,
// cache) so repository setup refuses to place a workspace inside them.
func (s *Service) SetProtectedRoots(roots ...string) {
	if s == nil {
		return
	}
	protected := make([]string, 0, len(roots)*2)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		protected = append(protected, filepath.Clean(root))
		if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != filepath.Clean(root) {
			protected = append(protected, resolved)
		}
	}
	s.protectedRoots = protected
}

// validateRepositorySetupPath runs before setup resolves, creates or
// initialises anything. The owner must name an absolute path that is not a
// system or Swarm storage directory. It is applied to the requested path and
// again to the canonical (symlink-resolved) path. Registering an existing
// folder (RegisterExistingFolderForPrincipal) applies the same checks.
func (s *Service) validateRepositorySetupPath(input string, paths ...string) error {
	if !filepath.IsAbs(expandHomePath(strings.TrimSpace(input))) {
		return errors.New("Swarm requires an absolute path chosen by the owner")
	}
	roots := append(append([]string(nil), systemRepositorySetupRoots...), s.protectedRoots...)
	for _, path := range paths {
		for _, root := range roots {
			if pathWithinRoot(root, path) {
				return fmt.Errorf("Swarm refuses %q: it is inside the system or Swarm storage directory %q; choose a folder in your home or project directory", path, root)
			}
		}
	}
	return nil
}

// validateRepositorySetupNesting refuses a canonical setup path nested inside
// another workspace registered to the same account.
func (s *Service) validateRepositorySetupNesting(principal identity.Principal, resolved string) error {
	return s.validateRepositorySetupNestingExcept(principal, resolved, "")
}

// validateRepositorySetupNestingExcept is validateRepositorySetupNesting for
// a workspace being moved: its own current entry (exceptWorkspaceID) does not
// count, since it leaves that path.
func (s *Service) validateRepositorySetupNestingExcept(principal identity.Principal, resolved, exceptWorkspaceID string) error {
	entries, err := s.store.ListForAccount(principal.AccountScopeID, math.MaxInt32)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if exceptWorkspaceID != "" && entry.WorkspaceID == exceptWorkspaceID {
			continue
		}
		if entry.Path != resolved && pathWithinRoot(entry.Path, resolved) {
			return fmt.Errorf("Swarm refuses %q: it is inside the registered workspace %q; choose a folder outside it", resolved, entry.Path)
		}
	}
	return nil
}

// ensureRepositoryCommitIdentity gives a repository Swarm just initialised a
// repository-local committer identity when none resolves from system/global
// configuration, so agent commits work on a fresh machine. An identity the
// owner already configured is left in force; global config is never written.
func ensureRepositoryCommitIdentity(path string) error {
	for _, setting := range [][2]string{{"user.name", WorkspaceGitIdentityName}, {"user.email", WorkspaceGitIdentityEmail}} {
		current, err := runRepositoryGit(path, "config", "--default", "", "--get", setting[0])
		if err != nil {
			return fmt.Errorf("read Git %s: %w", setting[0], err)
		}
		if strings.TrimSpace(current) != "" {
			continue
		}
		if _, err := runRepositoryGit(path, "config", "--local", setting[0], setting[1]); err != nil {
			return fmt.Errorf("set repository-local Git %s: %w", setting[0], err)
		}
	}
	return nil
}
