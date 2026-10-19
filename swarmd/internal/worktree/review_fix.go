package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
)

// ReviewFixBranchPrefix names the writable branch of a reviewer an
// automation step allowed to commit security fixes: agent/review-<run>.
const ReviewFixBranchPrefix = "agent/review-"

// AllocateReviewFixWorkspace gives a reviewer that may commit security fixes
// a writable branch checkout started at the reviewed head: a managed clone of
// the folder (origin is the folder, like a coder's job clone), seeded from
// the reviewed job's own clone, with branch agent/review-<run> at head. The
// reviewed job's clone and the folder are never written. Its head descends
// from the reviewed head, so the delivery's ancestry check and importJobHead
// read it like a coder job's head. Every other reviewer keeps
// AllocateReviewWorkspace (detached, disposable).
func (s *Service) AllocateReviewFixWorkspace(p identity.Principal, source, owner, reviewedClone, head, branch string) (Allocation, error) {
	if err := requirePrincipal(p); err != nil {
		return Allocation{}, err
	}
	source, err := s.resolveWorkspaceConfigPathForPrincipal(p, source)
	if err != nil {
		return Allocation{}, err
	}
	return s.allocateReviewFixWorkspace(source, owner, reviewedClone, head, branch)
}

func (s *Service) allocateReviewFixWorkspace(source, owner, reviewedClone, head, branch string) (Allocation, error) {
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
	// Branch from the reviewed job's own repository: its managed clone of
	// this folder (or the folder itself when the head has landed there).
	cloneSource := root
	if strings.TrimSpace(reviewedClone) != "" && !sameCleanPath(reviewedClone, root) {
		if !managedCloneIdentity(root, reviewedClone) {
			return Allocation{}, errors.New("the reviewed job's checkout is not a managed clone of the folder")
		}
		cloneSource = reviewedClone
	}
	if resolved, err := runGit(cloneSource, "rev-parse", "--verify", "--end-of-options", head+"^{commit}"); err != nil || resolved != head {
		return Allocation{}, errors.New("the reviewed head is not in the reviewed job's checkout")
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
	cleanup := func(cause error) (Allocation, error) {
		if errors.Is(cause, ErrAllocationWriterUnconfirmed) {
			return Allocation{}, cause
		}
		return Allocation{}, errors.Join(cause, os.RemoveAll(path))
	}
	// --no-local copies objects through upload-pack: the clone shares no
	// object store, alternates or config with the reviewed job's clone.
	if _, err := runGitAllocation(root, nil, boundedCloneArgs(cloneSource, path)...); err != nil {
		return cleanup(err)
	}
	for _, key := range []string{"user.name", "user.email"} {
		ctx := sandbox.WithGitCommandTimeout(context.Background(), gitCommandTimeout)
		value, err := sandbox.TimedGitOutput(ctx, root, nil, "config", "--local", "--get", key)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			continue
		}
		if err != nil {
			return cleanup(fmt.Errorf("read folder Git %s: %w", key, err))
		}
		if _, err := runGitAllocation(path, nil, "config", "--local", key, strings.TrimSuffix(string(value), "\n")); err != nil {
			return cleanup(err)
		}
	}
	for _, args := range [][]string{
		{"fetch", "--no-tags", "--", cloneSource, head},
		// origin is the folder, as for a coder's job clone: the checkout is a
		// managed clone of the folder, not of the reviewed job's clone.
		{"remote", "set-url", "origin", root},
		{"checkout", "-B", branch, head},
	} {
		if _, err := runGitAllocation(path, nil, args...); err != nil {
			return cleanup(err)
		}
	}
	if err := appstorage.SetPrivateDirPerm(path); err != nil {
		return cleanup(err)
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
// reviewer allowed to commit security fixes: a managed clone of the folder on
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
