package pebblestore

import (
	"errors"
	"strings"
)

// V3RunStoppedByUser is the durable classification used by the chat stop path.
// Other cancellations (quota, shutdown, deletion) do not authorize continuation.
const V3RunStoppedByUser = "run stopped by user"

func IsPausedTaskPlanningRun(status, reason string) bool {
	return status == V3RunIntentCancelled && reason == V3RunStoppedByUser
}

// prepareTaskPlanningContinuation runs under projectsMu and the session mutation
// lock. Its task write joins the message/run batch: neither can survive alone.
func (s *SessionStore) prepareTaskPlanningContinuation(input V3SessionMutationInput, intent V3SessionRunIntent, now int64) (*projectRealtimeMutation, error) {
	if input.Kind != V3SessionMutationAppendMessage || input.Message == nil || !strings.EqualFold(input.Message.Role, "user") || intent.Status != V3RunIntentPendingExecutor {
		return nil, nil
	}
	current, found, err := s.GetSession(input.SessionID)
	if err != nil || !found {
		return nil, err
	}
	if ProjectConversationID(current) != "" {
		return nil, nil // A project conversation is not a project task attempt.
	}
	if current.Mode != "plan" && current.Mode != "plan+bypass_permissions" {
		return nil, nil
	}
	projectID, _ := current.Metadata["project_id"].(string)
	taskID, _ := current.Metadata["task_id"].(string)
	if projectID == "" && taskID == "" {
		return nil, nil
	}
	task, found, err := s.GetProjectTask(input.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil || task.AccountID != input.AccountScopeID || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID || task.SessionID != input.SessionID || task.Archived || task.Status != "planning" {
		return nil, errors.New("task planning continuation requires the active planning task; use canonical task reopen for terminal attempts")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	attemptID, _ := current.Metadata["task_attempt_id"].(string)
	if attempt == nil || attempt.SessionID != current.ID || (attempt.UserID != "" && attempt.UserID != input.UserID) || (attemptID != "" && attemptID != attempt.ID) {
		return nil, errors.New("task planning continuation attempt mismatch")
	}
	if task.ExecutionRunID() == intent.RunID {
		return nil, nil // Initial admission retains its reserved owner.
	}
	previous, found, err := s.GetV3SessionRunState(current.ID)
	if err != nil {
		return nil, err
	}
	if !found || previous.AccountScopeID != input.AccountScopeID || previous.RunID != task.ExecutionRunID() || previous.Active || !IsPausedTaskPlanningRun(previous.Status, previous.BlockedReason) {
		return nil, errors.New("task planning continuation requires its durably paused owning run")
	}
	attempt.RunID = intent.RunID
	task.Revision++
	task.UpdatedAt = now
	task.CaptureActiveAttempt()
	task.PlanDocument = nil
	mutation := &projectRealtimeMutation{accountScopeID: input.AccountScopeID, projectID: projectID}
	if err := mutation.put(KeyProjectTask(input.AccountScopeID, projectID, taskID), task); err != nil {
		return nil, err
	}
	return mutation, nil
}
