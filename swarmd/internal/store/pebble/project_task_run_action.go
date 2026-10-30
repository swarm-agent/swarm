package pebblestore

import "strings"

// ProjectTaskFailedRunAction reports a task's ActionNeeded that describes how
// an earlier run ended (blocked, failed, stopped or cancelled). It describes
// only that run: once a newer run of the task succeeds, it is replaced.
func ProjectTaskFailedRunAction(action string) bool {
	if action == ProjectTaskStoppedAction {
		return true
	}
	for _, prefix := range []string{
		"Action Needed: Supply required input and resume.",
		"Action Needed: Worker run failed",
		"Action Needed: Run failed",
		"Action Needed: Run was stopped",
		"Action Needed: Run was cancelled",
	} {
		if strings.HasPrefix(action, prefix) {
			return true
		}
	}
	return false
}

// ClearProjectTaskRunOutcome removes an earlier run's outcome from a task when
// a new run of it starts: its error and its failed/blocked action describe a
// run that is over. Integration guidance and an integration error stay (they
// describe the task's branch, not a run). It reports whether it changed task.
func ClearProjectTaskRunOutcome(task *ProjectTaskRecord) bool {
	if task == nil {
		return false
	}
	changed := false
	if task.LastError != "" && (task.Integration == nil || task.Integration.Error == "") {
		task.LastError = ""
		changed = true
	}
	if ProjectTaskFailedRunAction(task.ActionNeeded) {
		task.ActionNeeded = ""
		changed = true
	}
	return changed
}

// ClearProjectTaskRunOutcomeForSession applies ClearProjectTaskRunOutcome to
// the stored task whose current session is sessionID. A task that moved to
// another session or was archived is left unchanged.
func (s *SessionStore) ClearProjectTaskRunOutcomeForSession(accountScopeID, projectID, taskID, sessionID string) error {
	_, err := s.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *ProjectTaskRecord) error {
		if t.SessionID != sessionID || t.Archived {
			return nil
		}
		ClearProjectTaskRunOutcome(t)
		return nil
	})
	return err
}
