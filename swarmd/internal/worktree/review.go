package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	"time"
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
	return s.allocateReviewWorkspaceTimeout(source, owner, head, worktreeAllocationTimeout)
}

func (s *Service) allocateReviewWorkspaceTimeout(source, owner, head string, timeout time.Duration) (Allocation, error) {
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
	guard := path + ".review-writer"
	guardFile, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Allocation{}, fmt.Errorf("allocation writer guard: %w", err)
	}
	if err := guardFile.Close(); err != nil {
		return Allocation{}, err
	}
	_, allocationErr := runGitAllocationTimeout(root, nil, timeout, "worktree", "add", "--detach", path, head)
	if errors.Is(allocationErr, ErrAllocationWriterUnconfirmed) {
		return Allocation{}, allocationErr
	}
	if err := os.Remove(guard); err != nil {
		return Allocation{}, errors.Join(allocationErr, err)
	}
	if err := allocationErr; err != nil {
		return Allocation{}, errors.Join(fmt.Errorf("review checkout admission: %w", err), s.CleanupReviewAdmission(root, owner))
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
		common, err := runGit(source, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		adminRoot := filepath.Join(strings.TrimSpace(common), "worktrees")
		entries, err := os.ReadDir(adminRoot)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			admin := filepath.Join(adminRoot, entry.Name())
			pointer := filepath.Join(admin, "gitdir")
			info, err := os.Lstat(pointer)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("invalid worktree admin pointer")
			}
			data, err := os.ReadFile(pointer)
			if err != nil {
				return err
			}
			if filepath.Clean(strings.TrimSpace(string(data))) != filepath.Join(filepath.Clean(path), ".git") {
				continue
			}
			// Delete only the positively identified target registration. Never
			// prune unrelated jobs whose checkouts may also be unavailable.
			if err := os.RemoveAll(admin); err != nil {
				return err
			}
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

// CleanupReviewAdmission is daemon-only. The caller must establish review ownership
// from a durable run receipt and exclude live execution before calling it. Unlike
// normal removal it tolerates a checkout interrupted before HEAD was written.
func (s *Service) CleanupReviewAdmission(source, owner string) error {
	if strings.TrimSpace(owner) == "" {
		return errors.New("review owner required")
	}
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return err
	}
	path, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path + ".review-writer"); !errors.Is(err, os.ErrNotExist) {
		return ErrAllocationWriterUnconfirmed
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(path) {
		return errors.New("review parent is a symlink")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	common, err := runGit(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	registrations := filepath.Join(common, "worktrees")
	if info, err := os.Lstat(registrations); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("worktree registrations directory is a symlink")
	}
	entries, err := os.ReadDir(registrations)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Identify only this checkout's registration; a global prune would also
	// remove unrelated missing user worktrees.
	var owned []string
	pointer, _ := os.ReadFile(filepath.Join(path, ".git"))
	linked := strings.TrimSpace(strings.TrimPrefix(string(pointer), "gitdir: "))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		registration := filepath.Join(registrations, entry.Name())
		data, err := os.ReadFile(filepath.Join(registration, "gitdir"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if strings.TrimSpace(string(data)) == filepath.Join(path, ".git") || (errors.Is(err, os.ErrNotExist) && linked == registration && entry.Name() == filepath.Base(path)) {
			head, err := os.ReadFile(filepath.Join(registration, "HEAD"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if strings.HasPrefix(strings.TrimSpace(string(head)), "ref:") {
				return errors.New("refusing to remove a branch-backed checkout as a review")
			}
			owned = append(owned, registration)
		}
	}
	for _, registration := range owned {
		if err := os.RemoveAll(registration); err != nil {
			return fmt.Errorf("prune review registration: %w", err)
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove partial review checkout: %w", err)
	}
	return nil
}
