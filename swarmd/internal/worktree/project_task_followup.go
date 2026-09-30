package worktree

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
)

// AllocateProjectTaskFollowup uses only a backend-authenticated exact commit and
// captured target. Existing allocations are recovered only at their owner/name
// derived path with matching branch and unchanged clean committed head.
func (s *Service) AllocateProjectTaskFollowup(p identity.Principal, source, owner, branch, head, target string) (Allocation, error) {
	if err := requirePrincipal(p); err != nil {
		return Allocation{}, err
	}
	root, err := s.resolveWorkspaceConfigPathForPrincipal(p, source)
	if err != nil {
		return Allocation{}, err
	}
	if root != source || strings.TrimSpace(owner) == "" || head == "" || target == "" {
		return Allocation{}, errors.New("follow-up requires exact catalog source, commit and target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := resolveRepositoryRoot(root)
	if err != nil {
		return Allocation{}, err
	}
	if owner != "" && !strings.HasPrefix(branch, "agent/") {
		return Allocation{}, errors.New("follow-up branch must be an isolated agent branch")
	}
	id, err := workspaceIdentityForRequestedBranch(branch)
	if err != nil {
		return Allocation{}, err
	}
	path, err := deterministicSessionWorktreePath(repo, id)
	if err != nil {
		return Allocation{}, err
	}
	if _, err := os.Stat(path); err == nil {
		if err := s.ValidateSessionRepositoryLane(source, path, owner, branch); err != nil {
			return Allocation{}, err
		}
		state, err := s.InspectTaskWorkspace(path)
		if err != nil || !state.Clean || state.HeadCommit != head {
			return Allocation{}, errors.New("retained follow-up allocation changed; explicit reconciliation required")
		}
		return Allocation{WorkspacePath: path, RepoRoot: repo, BaseBranch: target, BaseCommit: head, BranchName: branch, WorkspaceID: id}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Allocation{}, err
	}
	resolved, err := runGit(repo, "rev-parse", "--verify", head+"^{commit}")
	if err != nil || strings.TrimSpace(resolved) != head {
		return Allocation{}, errors.New("exact follow-up commit unavailable")
	}
	if err := ensureWorktreeParent(repo); err != nil {
		return Allocation{}, err
	}
	exists, err := localBranchExists(repo, branch)
	if err != nil {
		return Allocation{}, err
	}
	if exists {
		return Allocation{}, errors.New("follow-up branch already exists without owned allocation")
	}
	if _, err := runGitWorktreeAdd(repo, path, branch, head, false); err != nil {
		cleanupErr := cleanupFailedWorktreeAllocation(repo, path)
		cleanupErr = errors.Join(cleanupErr, cleanupPartialBranch(repo, branch, head))
		return Allocation{}, fmt.Errorf("allocate follow-up: %w", allocationFailureWithCleanup(err, cleanupErr))
	}
	if err := os.Chmod(path, appstorage.PrivateDirPerm); err != nil {
		cleanupErr := cleanupAllocatedWorktree(repo, path, branch)
		return Allocation{}, fmt.Errorf("secure follow-up worktree: %w", allocationFailureWithCleanup(err, cleanupErr))
	}
	return Allocation{WorkspacePath: path, RepoRoot: repo, BaseBranch: target, BaseCommit: head, BranchName: branch, WorkspaceID: id}, nil
}
