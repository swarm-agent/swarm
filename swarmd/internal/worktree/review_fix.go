package worktree

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
)

// ReviewFixBranchPrefix names the writable branch of a reviewer an
// automation step allowed to commit security fixes: agent/review-<run>.
const ReviewFixBranchPrefix = "agent/review-"

// AllocateReviewFixWorkspace gives a reviewer that may commit security fixes
// a writable branch checkout started at the reviewed head: a worktree of the
// folder on branch agent/review-<run> at head, like a coder job's. Its head
// descends from the reviewed head and its commits are in the folder's
// repository as they are made. Every other reviewer keeps
// AllocateReviewWorkspace (detached, disposable).
func (s *Service) AllocateReviewFixWorkspace(p identity.Principal, source, owner, head, branch string) (Allocation, error) {
	if err := requirePrincipal(p); err != nil {
		return Allocation{}, err
	}
	source, err := s.resolveWorkspaceConfigPathForPrincipal(p, source)
	if err != nil {
		return Allocation{}, err
	}
	return s.allocateReviewFixWorkspace(source, owner, head, branch)
}

func (s *Service) allocateReviewFixWorkspace(source, owner, head, branch string) (Allocation, error) {
	if strings.TrimSpace(owner) == "" {
		return Allocation{}, errors.New("review fix checkout owner required")
	}
	if !validCommitID(head) {
		return Allocation{}, errors.New("review fix checkout requires the exact reviewed head")
	}
	if !strings.HasPrefix(branch, ReviewFixBranchPrefix) || len(branch) == len(ReviewFixBranchPrefix) {
		return Allocation{}, fmt.Errorf("review fix branch must be %s<run>", ReviewFixBranchPrefix)
	}
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return Allocation{}, err
	}
	if _, err := runGit(root, "check-ref-format", "--branch", branch); err != nil {
		return Allocation{}, fmt.Errorf("review fix branch: %w", err)
	}
	if resolved, err := runGit(root, "rev-parse", "--verify", "--end-of-options", head+"^{commit}"); err != nil || resolved != head {
		return Allocation{}, errors.New("the reviewed head is not in the folder's repository")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := sessionWorkspaceID(owner)
	path, err := deterministicSessionWorktreePath(root, id)
	if err != nil {
		return Allocation{}, err
	}
	if err := ensureWorktreeParent(root); err != nil {
		return Allocation{}, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return Allocation{}, errors.New("review fix checkout already exists")
	}
	existed, err := localBranchExists(root, branch)
	if err != nil {
		return Allocation{}, err
	}
	if existed {
		return Allocation{}, fmt.Errorf("review fix branch %s already exists", branch)
	}
	if _, err := runGitWorktreeAdd(root, path, branch, head, false); err != nil {
		cleanupErr := errors.Join(cleanupFailedWorktreeAllocation(root, path), cleanupPartialBranch(root, branch, head))
		return Allocation{}, allocationFailureWithCleanup(err, cleanupErr)
	}
	if err := appstorage.SetPrivateDirPerm(path); err != nil {
		return Allocation{}, allocationFailureWithCleanup(err, errors.Join(cleanupFailedWorktreeAllocation(root, path), cleanupPartialBranch(root, branch, head)))
	}
	return Allocation{WorkspacePath: path, RepoRoot: root, BaseBranch: head, BaseCommit: head, BranchName: branch, WorkspaceID: id}, nil
}

// ReviewFixHead reads the head of a fixing reviewer's checkout: lane must be
// on branch (agent/review-<run>) and its HEAD must descend from the reviewed
// head. It returns HEAD, which equals reviewed when the reviewer committed
// nothing.
func ReviewFixHead(lane, branch, reviewed string) (string, error) {
	if !strings.HasPrefix(branch, ReviewFixBranchPrefix) || !validCommitID(reviewed) || strings.TrimSpace(lane) == "" {
		return "", errors.New("review fix checkout identity is incomplete")
	}
	current, err := runGit(lane, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || current != branch {
		return "", fmt.Errorf("the reviewer's checkout is not on %s", branch)
	}
	head, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil || !validCommitID(head) {
		return "", errors.New("the reviewer's checkout head could not be read")
	}
	if _, err := runGit(lane, "merge-base", "--is-ancestor", reviewed, head); err != nil {
		return "", errors.New("the reviewer's head does not descend from the reviewed head")
	}
	return head, nil
}

// ValidateReviewFixExecutionIdentity admits the writable branch checkout of a
// reviewer allowed to commit security fixes: a worktree of the folder on
// its agent/review-<run> branch whose HEAD descends from the reviewed head.
// It replaces the read-only reviewer's "detached HEAD equal to the reviewed
// head" check for that reviewer only.
func ValidateReviewFixExecutionIdentity(source, lane, branch, head string) error {
	if !strings.HasPrefix(branch, ReviewFixBranchPrefix) {
		return errors.New("review fix checkout must be on its agent/review-<run> branch")
	}
	if !validCommitID(head) {
		return errors.New("review fix checkout requires the exact reviewed head")
	}
	if err := ValidateOwnedIdentity(source, lane, branch, head); err != nil {
		return fmt.Errorf("review fix checkout: %w", err)
	}
	return nil
}
