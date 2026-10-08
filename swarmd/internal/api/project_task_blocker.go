package api

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// projectTaskDeclaredBlocker projects only an explicit outcome of this execution.
// Provider completion is transport completion, not proof that the task succeeded.
func projectTaskDeclaredBlocker(task *pebblestore.ProjectTaskRecord, sess pebblestore.SessionSnapshot, state pebblestore.V3SessionRunState) bool {
	if task.SessionID != sess.ID || task.AccountID != sess.AccountScopeID || state.AccountScopeID != sess.AccountScopeID ||
		state.RunID == "" || state.Status != pebblestore.V3RunIntentCompleted || state.Active ||
		(task.ExecutionRunID() != "" && task.ExecutionRunID() != state.RunID) ||
		sess.Metadata["lifecycle_signal"] != "blocked" || sess.Metadata["lifecycle_signal_run_id"] != state.RunID {
		return false
	}
	reason, _ := sess.Metadata["blocker_reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return false
	}
	task.Status = "blocked"
	task.LastError = reason
	task.ActionNeeded = "Action Needed: Supply required input and resume. " + reason
	return true
}
