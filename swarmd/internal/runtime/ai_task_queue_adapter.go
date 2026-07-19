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

func (a aiTaskQueueAdapter) TransitionAITask(input run.AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error) {
	return a.service.TransitionAITaskAuthority(todo.AITaskTransitionInput{
		AccountScopeID: input.Item.AccountScopeID, WorkspacePath: input.Item.WorkspacePath, ID: input.Item.ID,
		ExpectedState: input.ExpectedState, State: input.State, Mode: input.Mode, Worktree: input.Worktree,
		ManagedSessionID: input.ManagedSessionID, DisplayTitle: input.DisplayTitle, FinalRunID: input.FinalRunID, Result: input.Result,
		PreparationSessionID: input.PreparationSessionID, PreparationRunID: input.PreparationRunID,
		PreparationAttemptID: input.PreparationAttemptID, Error: input.Error,
		ExpectedVersion: input.Item.AIStateVersion, Disposition: input.Disposition,
	})
}
