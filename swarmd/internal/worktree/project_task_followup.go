package worktree

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/gitenv"
	"swarm/packages/swarmd/internal/identity"
)

// TaskCheckpointTrailer marks the commit that preserves a stopped task's
// uncommitted work; its value is the task ID.
const TaskCheckpointTrailer = "Swarm-Task-Checkpoint"

// TaskCheckpoint reports a retained task worktree's checkpoint commit.
type TaskCheckpoint struct {
	// Commit is the checkpoint commit at HEAD, or "" when HEAD is not one.
	Commit string
	// Created is true when this call made the commit.
	Created bool
	// State is the worktree after the call (clean when Commit is set).
	State TaskWorkspaceState
}

// CheckpointTaskWorkspace preserves every change in a stopped task's retained
// worktree, untracked files included (ignored files stay out), as one commit
// on the task's own branch. Git runs as it does for the task's agent
// (git_commit): the repository's configured identity and signing, with
// identity environment overrides removed, routed through the sandbox. Hooks
// are skipped: they gate an agent's finished work, not the preservation of
// work a stopped run left behind. The commit message ends with the
// TaskCheckpointTrailer naming taskID.
//
// It is idempotent: a clean worktree is never committed, and reports HEAD as
// the checkpoint when HEAD is this task's checkpoint commit, so a retried
// reopen finds the commit the first one made. It refuses a worktree on another
// branch (including a detached or rebasing HEAD) and one with a merge,
// cherry-pick or revert in progress. Callers must first prove the worktree is
// the task's own and that no run is writing to it.
func (s *Service) CheckpointTaskWorkspace(workspacePath, branch, taskID, message string) (TaskCheckpoint, error) {
	workspacePath, branch, taskID = strings.TrimSpace(workspacePath), strings.TrimSpace(branch), strings.TrimSpace(taskID)
	if workspacePath == "" || branch == "" || taskID == "" || strings.ContainsAny(taskID, "\r\n") || strings.TrimSpace(message) == "" {
		return TaskCheckpoint{}, errors.New("checkpoint requires the worktree, its task branch, the task and a message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.InspectTaskWorkspace(workspacePath)
	if err != nil {
		return TaskCheckpoint{}, err
	}
	if state.BranchName != branch {
		return TaskCheckpoint{}, fmt.Errorf("retained task worktree is on %q, not the task branch %q", state.BranchName, branch)
	}
	trailer := TaskCheckpointTrailer + ": " + taskID
	if state.Clean {
		body, err := runGit(workspacePath, "log", "-1", "--format=%B", "HEAD")
		if err != nil {
			return TaskCheckpoint{}, err
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == trailer {
				return TaskCheckpoint{Commit: state.HeadCommit, State: state}, nil
			}
		}
		return TaskCheckpoint{State: state}, nil
	}
	for _, pseudo := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := runGit(workspacePath, "rev-parse", "-q", "--verify", pseudo); err == nil {
			return TaskCheckpoint{}, fmt.Errorf("retained task worktree has a %s in progress; resolve it before reopening", strings.ToLower(strings.TrimSuffix(pseudo, "_HEAD")))
		}
	}
	env := gitenv.FilterIdentityOverrides(os.Environ())
	if _, err := runGitWithEnv(workspacePath, env, "add", "--all", "--", "."); err != nil {
		return TaskCheckpoint{}, fmt.Errorf("stage interrupted task work: %w", err)
	}
	full := strings.TrimRight(message, "\n") + "\n\n" + trailer + "\n"
	if _, err := runGitWithEnv(workspacePath, env, "commit", "--no-verify", "-q", "-m", full); err != nil {
		return TaskCheckpoint{}, fmt.Errorf("commit interrupted task work: %w", err)
	}
	after, err := s.InspectTaskWorkspace(workspacePath)
	if err != nil {
		return TaskCheckpoint{}, err
	}
	parent, err := runGit(workspacePath, "rev-parse", "--verify", "HEAD^")
	if err != nil || !after.Clean || after.BranchName != branch || strings.TrimSpace(parent) != state.HeadCommit {
		return TaskCheckpoint{}, errors.New("checkpoint commit did not leave a clean worktree one commit ahead on the task branch")
	}
	return TaskCheckpoint{Commit: after.HeadCommit, Created: true, State: after}, nil
}

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
		// A finished task's merged, clean checkout is removed while its
		// branch is kept (RemoveLandedWorktree). Its follow-up recreates the
		// checkout from that branch, only while it is still at the exact
		// commit the follow-up continues from.
		tip, err := runGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
		if err != nil || strings.TrimSpace(tip) != head {
			return Allocation{}, errors.New("follow-up branch already exists without owned allocation")
		}
		if _, err := runGitWorktreeAddArgs(repo, []string{"worktree", "add", path, branch}); err != nil {
			cleanupErr := cleanupFailedWorktreeAllocation(repo, path)
			return Allocation{}, fmt.Errorf("recreate follow-up worktree from its branch: %w", allocationFailureWithCleanup(err, cleanupErr))
		}
		if err := os.Chmod(path, appstorage.PrivateDirPerm); err != nil {
			return Allocation{}, fmt.Errorf("secure follow-up worktree: %w", err)
		}
		return Allocation{WorkspacePath: path, RepoRoot: repo, BaseBranch: target, BaseCommit: head, BranchName: branch, WorkspaceID: id}, nil
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
