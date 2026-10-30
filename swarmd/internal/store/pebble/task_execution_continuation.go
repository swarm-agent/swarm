package pebblestore

import "errors"

// TaskContinuationStateRefusal is the task-state part of the continuation
// rule: a message never continues an archived, integrated, completed,
// rejected or not yet admitted task (those go through approval or reopen).
// prepareTaskExecutionContinuation applies it when a later message's run is
// admitted; callers may apply it earlier to avoid preparing work (such as
// recreating a removed checkout) for a message that will be refused.
func TaskContinuationStateRefusal(task *ProjectTaskRecord) error {
	if task == nil {
		return errors.New("task continuation requires its current owning session")
	}
	if task.Archived || task.IsIntegrated || task.Status == "completed" || task.Status == "rejected" || task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
		return errors.New("task continuation requires an admitted task; use the canonical task approval or reopen lifecycle")
	}
	return nil
}

// Called under the project and session locks by ApplyV3SessionMutation. The
// retry's message, run intent and task receipt must commit together, including
// when a fast failure occurs before any client observes the running state.
func (s *SessionStore) prepareTaskExecutionContinuation(input V3SessionMutationInput, current SessionSnapshot, intent V3SessionRunIntent, now int64) (*projectRealtimeMutation, error) {
	projectID, _ := current.Metadata["project_id"].(string)
	taskID, _ := current.Metadata["task_id"].(string)
	if taskID == "" {
		taskID, _ = current.Metadata["project_task_id"].(string)
	}
	if taskID == "" {
		return nil, nil
	}
	task, found, err := s.GetProjectTask(input.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil || task.AccountID != input.AccountScopeID || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID || task.SessionID != current.ID {
		return nil, errors.New("task continuation requires its current owning session")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	attemptID, _ := current.Metadata["task_attempt_id"].(string)
	if attempt == nil || attempt.SessionID != current.ID || (attempt.UserID != "" && attempt.UserID != input.UserID) || (attemptID != "" && attemptID != attempt.ID) {
		return nil, errors.New("task continuation attempt mismatch")
	}
	if task.ExecutionRunID() == intent.RunID {
		return nil, nil // Initial admission keeps its reserved lifecycle.
	}
	if task.WorkerRunID != "" && task.WorkerRunID == intent.RunID && task.Status == "queued" {
		// A direct worker job's first message runs as the job's own run while
		// its task is still queued; the job marks the task in_progress itself.
		return nil, nil
	}
	if err := TaskContinuationStateRefusal(task); err != nil {
		return nil, err
	}
	previous, found, err := s.GetV3SessionRunState(current.ID)
	if err != nil {
		return nil, err
	}
	if !found || previous.AccountScopeID != input.AccountScopeID || previous.Active {
		return nil, errors.New("task continuation requires a settled owning run")
	}
	attempt.RunID = intent.RunID
	attempt.Summary, attempt.SummaryRunID = "", ""
	task.Status, task.LastError, task.ActionNeeded = "in_progress", "", ""
	// A continuation ends a provider-limit wait (the wait's own continuation,
	// or the person's message) in the same commit as its run intent.
	task.LimitRetryAt, task.LimitReason = 0, ""
	task.Revision++
	task.UpdatedAt = now
	task.CaptureActiveAttempt()
	mutation := &projectRealtimeMutation{accountScopeID: input.AccountScopeID, projectID: projectID}
	if err := mutation.put(KeyProjectTask(input.AccountScopeID, projectID, taskID), task); err != nil {
		return nil, err
	}
	return mutation, nil
}
