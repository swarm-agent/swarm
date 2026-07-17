package runtime

import (
	"swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
)

type aiTaskQueueAdapter struct{ service *todo.Service }

func (a aiTaskQueueAdapter) ListAITaskAccounts(limit int) ([]string, error) {
	return a.service.ListAITaskAccounts(limit)
}
func (a aiTaskQueueAdapter) ListActiveAITasks(accountScopeID string, limit int) ([]pebblestore.WorkspaceTodoItem, error) {
	return a.service.ListActiveAITasks(accountScopeID, limit)
}
func (a aiTaskQueueAdapter) GetAITask(accountScopeID, workspacePath, taskID string) (pebblestore.WorkspaceTodoItem, bool, error) {
	return a.service.GetAITask(accountScopeID, workspacePath, taskID)
}
func (a aiTaskQueueAdapter) AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record pebblestore.AITaskAuditRecord) error {
	return a.service.AppendAITaskAudit(accountScopeID, workspacePath, taskID, record)
}
func (a aiTaskQueueAdapter) TransitionAITask(input run.AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error) {
	return a.service.TransitionAITaskAuthority(todo.AITaskTransitionInput{
		AccountScopeID: input.Item.AccountScopeID, WorkspacePath: input.Item.WorkspacePath, ID: input.Item.ID,
		ExpectedState: input.ExpectedState, State: input.State, Mode: input.Mode, Worktree: input.Worktree,
		ManagedSessionID: input.ManagedSessionID, FinalRunID: input.FinalRunID,
		PreparationSessionID: input.PreparationSessionID, PreparationRunID: input.PreparationRunID,
		PreparationAttemptID: input.PreparationAttemptID, Error: input.Error,
		ExpectedVersion: input.Item.AIStateVersion, Disposition: input.Disposition,
	})
}
