package tool

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type promotionTaskReceipt struct {
	task *pebblestore.ProjectTaskRecord
	receipt *pebblestore.ProjectTaskIntegration
}

func (r *Runtime) promotionTask(scope WorkspaceScope, session pebblestore.SessionSnapshot, branch, head, target, targetBranch, targetHead string) (*promotionTaskReceipt, error) {
	projectID := asString(session.Metadata["project_id"])
	taskID := firstNonEmptyString(asString(session.Metadata["task_id"]), asString(session.Metadata["project_task_id"]))
	if projectID == "" && taskID == "" {
		return nil, nil
	}
	if r.projects == nil || projectID == "" || taskID == "" {
		return nil, errors.New("promotion task binding or project store unavailable")
	}
	task, found, err := r.projects.GetProjectTask(scope.Principal.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil || task.SessionID != session.ID || (task.AccountID != "" && task.AccountID != scope.Principal.AccountScopeID) || task.Archived || (task.SourceWorkspace.Path != "" && task.SourceWorkspace.Path != target) || (task.WorktreeBranch != "" && task.WorktreeBranch != branch) || (task.BaseBranch != "" && task.BaseBranch != targetBranch) {
		return nil, errors.New("promotion source does not match the current owned task attempt and captured target")
	}
	return &promotionTaskReceipt{task: task, receipt: &pebblestore.ProjectTaskIntegration{SessionID: session.ID, SourceBranch: branch, SourceHead: head, TargetWorkspacePath: target, TargetBranch: targetBranch, PreviousTargetHead: targetHead}}, nil
}

func (r *Runtime) finishPromotionTasks(scope WorkspaceScope, receipts []*promotionTaskReceipt, state, targetHead string, cause error) error {
	var failures []error
	for _, entry := range receipts {
		entry.receipt.State = state
		entry.receipt.ResultingTargetHead = targetHead
		if cause != nil {
			entry.receipt.Error = cause.Error()
		}
		if _, err := pebblestore.FinishProjectTaskIntegration(r.projects, scope.Principal.AccountScopeID, entry.task, entry.receipt); err != nil {
			failures = append(failures, fmt.Errorf("persist promotion receipt for task %s: %w", entry.task.ID, err))
		}
	}
	return errors.Join(failures...)
}
