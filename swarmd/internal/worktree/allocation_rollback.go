package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RollbackAllocation removes an allocation that this service previously
// returned when its caller fails before committing durable session authority.
// It refuses paths or branches that no longer match the recorded allocation.
func (s *Service) RollbackAllocation(allocation Allocation) error {
	if s == nil {
		return errors.New("worktree service is not configured")
	}
	repoRoot := filepath.Clean(strings.TrimSpace(allocation.RepoRoot))
	worktreePath := filepath.Clean(strings.TrimSpace(allocation.WorkspacePath))
	branchName := strings.TrimSpace(allocation.BranchName)
	baseBranch := strings.TrimSpace(allocation.BaseBranch)
	workspaceID := strings.TrimSpace(allocation.WorkspaceID)
	if repoRoot == "." || worktreePath == "." || branchName == "" || baseBranch == "" || workspaceID == "" {
		return errors.New("complete worktree allocation facts are required for rollback")
	}
	resolvedRepoRoot, err := resolveRepositoryRoot(repoRoot)
	if err != nil {
		return fmt.Errorf("validate rollback repository: %w", err)
	}
	if !sameCleanPath(resolvedRepoRoot, repoRoot) {
		return fmt.Errorf("refuse worktree rollback because recorded repository root changed: got %q want %q", repoRoot, resolvedRepoRoot)
	}
	expectedPath, err := deterministicSessionWorktreePath(repoRoot, workspaceID)
	if err != nil {
		return fmt.Errorf("validate rollback worktree path: %w", err)
	}
	if !sameCleanPath(expectedPath, worktreePath) {
		return fmt.Errorf("refuse worktree rollback outside recorded managed path: got %q want %q", worktreePath, expectedPath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	output, err := runGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect worktree allocation for rollback: %w", err)
	}
	matched := false
	for _, entry := range parseWorktreeList(output) {
		if !sameCleanPath(entry.Path, worktreePath) {
			continue
		}
		if entry.Detached || normalizeGitWorktreeBranch(entry.Branch) != branchName {
			return fmt.Errorf("refuse worktree rollback after allocation ownership changed at %q", worktreePath)
		}
		matched = true
		break
	}
	if !matched {
		return fmt.Errorf("refuse worktree rollback because recorded allocation %q is not registered", worktreePath)
	}
	branchHead, headErr := runGit(repoRoot, "rev-parse", "--verify", "refs/heads/"+branchName+"^{commit}")
	baseHead, baseErr := runGit(repoRoot, "rev-parse", "--verify", baseBranch+"^{commit}")
	if headErr != nil || baseErr != nil || branchHead != baseHead {
		return fmt.Errorf("refuse worktree rollback because recorded branch %q changed after allocation", branchName)
	}
	if _, err := runGit(repoRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		return fmt.Errorf("rollback worktree allocation: remove worktree: %w", err)
	}
	var cleanupErrs []error
	if err := os.RemoveAll(worktreePath); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove worktree path: %w", err))
	}
	if _, err := runGit(repoRoot, "worktree", "prune"); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("prune worktree metadata: %w", err))
	}
	if _, err := runGit(repoRoot, "branch", "-D", branchName); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove branch: %w", err))
	}
	if err := errors.Join(cleanupErrs...); err != nil {
		return fmt.Errorf("rollback worktree allocation: %w", err)
	}
	return nil
}
