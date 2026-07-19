package runtime

import (
	"swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
)

// aiTaskQueueAdapter is the durable lifecycle writer for the in-memory
// dispatcher. It never reads or scans for work and never decides what runs next.
type aiTaskQueueAdapter struct{ service *todo.Service }

func (a aiTaskQueueAdapter) AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record pebblestore.AITaskAuditRecord) error {
	return a.service.AppendAITaskAudit(accountScopeID, workspacePath, taskID, record)
}

func (a aiTaskQueueAdapter) LoadAITaskV2RecoveryQueue(limit int) ([]pebblestore.AITaskV2QueueRecord, error) {
	return a.service.LoadAITaskV2RecoveryQueue(limit)
}

func (a aiTaskQueueAdapter) DeleteAITaskV2QueueRecord(key string) error {
	return a.service.DeleteAITaskV2QueueRecord(key)
}

func (a aiTaskQueueAdapter) TransitionAITaskV2(input run.AITaskV2Transition) (pebblestore.WorkspaceTodoItem, error) {
	return a.service.TransitionAITaskAuthority(todo.AITaskTransitionInput{
		AccountScopeID: input.Item.AccountScopeID, WorkspacePath: input.Item.WorkspacePath, ID: input.Item.ID,
		ExpectedState: input.ExpectedState, State: input.State, Mode: input.Mode, Worktree: input.Worktree, WorktreeName: input.WorktreeName,
		ManagedSessionID: input.ManagedSessionID, DisplayTitle: input.DisplayTitle, FinalRunID: input.FinalRunID, Result: input.Result,
		PreparationSessionID: input.PreparationSessionID, PreparationRunID: input.PreparationRunID,
		PreparationAttemptID: input.PreparationAttemptID, Error: input.Error,
		ExpectedVersion: input.Item.AIStateVersion, RetryCount: input.RetryCount, NextAttemptAt: input.NextAttemptAt, Disposition: input.Disposition,
	})
}
