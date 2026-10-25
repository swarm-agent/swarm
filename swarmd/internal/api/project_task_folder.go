package api

import (
	"context"
	"fmt"
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
