package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
)

// AllocateReviewWorkspace creates no branch or writable publication lineage.
func (s *Service) AllocateReviewWorkspace(p identity.Principal, source, owner, head string) (Allocation, error) {
	if err := requirePrincipal(p); err != nil {
		return Allocation{}, err
	}
	source, err := s.resolveWorkspaceConfigPathForPrincipal(p, source)
	if err != nil {
		return Allocation{}, err
	}
	return s.allocateReviewWorkspace(source, owner, head)
}

func (s *Service) allocateReviewWorkspace(source, owner, head string) (Allocation, error) {
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return Allocation{}, err
	}
	resolved, err := runGit(root, "rev-parse", "--verify", head+"^{commit}")
	if err != nil || resolved != head {
		return Allocation{}, errors.New("review requires an exact commit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return Allocation{}, err
	}
	if err := ensureWorktreeParent(root); err != nil {
		return Allocation{}, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return Allocation{}, errors.New("review checkout already exists")
	}
	if _, err := runGit(root, "worktree", "add", "--detach", path, head); err != nil {
		return Allocation{}, err
	}
	return Allocation{WorkspacePath: path, RepoRoot: root, BaseCommit: head, BaseBranch: head, BranchName: "HEAD", WorkspaceID: sessionWorkspaceID(owner)}, nil
}

// RemoveReviewWorkspace discards build outputs only in the verified managed
// detached lane. No branch or commit is promoted or delivered.
func (s *Service) RemoveReviewWorkspace(source, path, owner, head string) error {
	if err := s.ValidateReviewWorkspaceRemoval(source, path, owner, head); err != nil {
		return err
	}
	return s.RemoveValidatedReviewWorkspace(source, path)
}

// ValidateReviewWorkspaceRemoval runs before terminal sandbox shutdown.
func (s *Service) ValidateReviewWorkspaceRemoval(source, path, owner, head string) error {
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return err
	}
	expected, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return err
	}
	if filepath.Clean(path) != expected {
		return errors.New("review checkout owner mismatch")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.ValidateSessionRepositoryLaneForRead(source, path, owner, "HEAD"); err != nil {
		return err
	}
	actual, err := runGit(path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(actual) != head {
		return errors.New("review head changed")
	}
	return nil
}

// RemoveValidatedReviewWorkspace is daemon-only; no Git runs in the closing
// checkout. Caller must validate before shutdown and retain restrictive scope.
func (s *Service) RemoveValidatedReviewWorkspace(source, path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		// A torn-down checkout can retain files but no Git identity. Pruning
		// must succeed and registration absence must be confirmed before the
		// caller releases the restrictive reviewer identity.
		if _, err := runGit(source, "worktree", "prune", "--expire", "now"); err != nil {
			return err
		}
		listing, err := runGit(source, "worktree", "list", "--porcelain")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(listing, "\n") {
			if strings.TrimPrefix(line, "worktree ") == path {
				return errors.New("review checkout remains registered")
			}
		}
		return nil
	} else if err != nil {
		return err
	}
	_, err := runGit(source, "worktree", "remove", "--force", path)
	return err
}

// ValidateReviewExecutionIdentity admits only the daemon-registered, owner-bound
// detached review checkout at its immutable reviewed commit. It grants no branch
// publication or mutation authority and never substitutes for writable validation.
func ValidateReviewExecutionIdentity(source, lane, owner, head string) error {
	if sandbox.ReviewSessionCheckout(owner) != lane {
		return errors.New("review session checkout is not registered")
	}
	if err := (&Service{}).ValidateSessionRepositoryLaneForRead(source, lane, owner, "HEAD"); err != nil {
		return err
	}
	resolved, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", head+"^{commit}")
	if err != nil || resolved != head || head == "" {
		return errors.New("review requires an exact recorded head")
	}
	actual, err := resolveRepositoryHeadCommit(lane)
	if err != nil {
		return err
	}
	if actual != head {
		return errors.New("review head changed")
	}
	return nil
}
