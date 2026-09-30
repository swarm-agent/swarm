package tool

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// rejectTaskSessionContinuation prevents session messaging from becoming a second
// task execution authority. Even the current terminal attempt must use reopen_task.
// Non-triggering messages do not call this guard and cannot reopen a task.
func (r *Runtime) rejectTaskSessionContinuation(scope WorkspaceScope, session pebblestore.SessionSnapshot) error {
	projectID := asString(session.Metadata["project_id"])
	taskID := firstNonEmptyString(asString(session.Metadata["task_id"]), asString(session.Metadata["project_task_id"]))
	if taskID == "" {
		return nil // Project-associated standalone sessions are not task attempts.
	}
	if projectID == "" || r.projects == nil {
		return errors.New("task session binding unavailable; use manage_projects get_task and reopen_task, not send_message with trigger_run=true")
	}
	task, found, err := r.projects.GetProjectTask(scope.Principal.AccountScopeID, projectID, taskID)
	if err != nil {
		return err
	}
	if !found || task == nil || (task.AccountID != "" && task.AccountID != scope.Principal.AccountScopeID) {
		return errors.New("owned task not found; task session cannot be started")
	}
	return fmt.Errorf("task-linked session cannot be started via send_message: use manage_projects reopen_task with project_id=%s task_id=%s expected_revision=%d feedback and a stable client_request_id; current_session_id=%s active_attempt_id=%s task_status=%s archived=%t (retained work is unchanged)", projectID, taskID, task.Revision, task.SessionID, task.ActiveAttemptID, task.Status, task.Archived)
}
