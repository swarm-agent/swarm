package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func folderTask(task *store.ProjectTaskRecord) bool {
	return task != nil && task.WorktreeBranch == "" && task.WorkspacePath != "" && task.WorkspacePath == task.SourceWorkspace.Path && task.Agent != "reviewer"
}

func folderGit(ctx context.Context, path string, args ...string) (string, error) {
	out, err := sandbox.Command(ctx, append([]string{"-C", path}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// swarmTaskForksBranch reports whether a Swarm task in path gets its own
// worktree and branch, as a coder task does: path is the top of a Git
// repository whose HEAD is a branch with a commit. Anything else (not a
// repository, a subfolder, detached or unborn) keeps the folder behavior.
func swarmTaskForksBranch(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	top, err := folderGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || !sameFolderPath(top, path) {
		return false
	}
	if _, err := folderGit(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil {
		return false
	}
	_, err = folderGit(ctx, path, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func sameFolderPath(a, b string) bool {
	resolve := func(p string) string {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		return filepath.Clean(p)
	}
	return a != "" && b != "" && resolve(a) == resolve(b)
}

// swarmTaskOwnBranchContext is the seed line for a Swarm task that runs in its
// own worktree: its edits land only there, and only commits are delivered.
func swarmTaskOwnBranchContext(task *store.ProjectTaskRecord, targetAgent, mode string) string {
	if !strings.EqualFold(targetAgent, "swarm") || mode == "plan" || task.WorktreeBranch == "" || task.WorkspacePath == "" || task.WorkspacePath == task.SourceWorkspace.Path {
		return ""
	}
	return fmt.Sprintf("\n\n## Your branch\nYou work on branch %s in your own worktree (%s). Commit your changes there with git_add and git_commit before you finish; uncommitted changes are not delivered. Do not touch the source folder (%s).", task.WorktreeBranch, task.WorkspacePath, task.SourceWorkspace.Path)
}

// swarmFolderTaskNote explains a Swarm task that worked in the folder itself:
// the folder's repository had no branch with a commit to fork from (a detached
// HEAD or an unborn branch), so it had no branch of its own. Empty for every
// other task.
func swarmFolderTaskNote(agent, worktreeBranch, baseCommit, workspacePath, sourcePath string) string {
	if (agent != "" && !strings.EqualFold(agent, "swarm")) || worktreeBranch != "" || baseCommit != "" || workspacePath == "" || workspacePath != sourcePath {
		return ""
	}
	return "The work was done in the folder itself, not on a branch of its own, because the folder's repository had no branch with a commit to fork from (a detached HEAD or an unborn branch); its changes are already in the folder."
}

func captureFolderTaskBase(task *store.ProjectTaskRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Non-repository folders have no Git delivery.
	if _, err := folderGit(ctx, task.WorkspacePath, "rev-parse", "--show-toplevel"); err != nil {
		return nil
	}
	branch, err := folderGit(ctx, task.WorkspacePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// Detached checkouts remain usable, without branch delivery.
		return nil
	}
	head, err := folderGit(ctx, task.WorkspacePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		// Unborn branches remain usable, without a commit range.
		return nil
	}
	task.BaseBranch, task.BaseCommit = branch, head
	return nil
}

// captureFolderTaskDelivery runs at the owning run's terminal mutation, never
// during hydration: later folder commits must not become this task's output.
func captureFolderTaskDelivery(task *store.ProjectTaskRecord) error {
	if !folderTask(task) || task.BaseCommit == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	branch, err := folderGit(ctx, task.WorkspacePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != task.BaseBranch {
		return fmt.Errorf("folder task branch changed before completion")
	}
	head, err := folderGit(ctx, task.WorkspacePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	if _, err := folderGit(ctx, task.WorkspacePath, "merge-base", "--is-ancestor", task.BaseCommit, head); err != nil {
		return fmt.Errorf("folder task head no longer descends from launch base: %w", err)
	}
	task.Integration = &store.ProjectTaskIntegration{State: "in_folder", SessionID: task.SessionID, AttemptID: task.ActiveAttemptID, SourceHead: head, SourceBranch: branch, TargetBranch: branch, PreviousTargetHead: task.BaseCommit, ResultingTargetHead: head, TargetWorkspacePath: task.WorkspacePath}
	return nil
}
