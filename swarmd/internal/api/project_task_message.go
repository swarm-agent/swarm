package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Messaging and stopping a delegated task. A message to a running task is
// queued for its session through the same channel as the person's own
// message during a run (acceptSessionsV3QueuedNote): it reaches the running
// model as the person's turn with its next tool result. A message to a task
// with no active run reopens it (ReopenProjectTask) with the message as the
// follow-up. Stop is the person's stop of the task session's active run
// (stopActiveSessionRun); the task stays resumable with a message.
//
//	POST /v3/projects/{id}/tasks/{taskId}/message  {message, client_request_id?, expected_revision?}
//	POST /v3/projects/{id}/tasks/{taskId}/stop
//
// The native manage_projects message_task/stop_task actions call the same
// service; with machine they call these routes on the linked machine.

const (
	projectTaskMessageMaxBytes = 32000
	projectTaskStopReason      = pebblestore.ProjectTaskStopReason
	// projectTaskStoppedAction is a stopped task's next step.
	projectTaskStoppedAction = pebblestore.ProjectTaskStoppedAction
)

// MessageProjectTask delivers message to a task: queued in its running run,
// or as the follow-up that reopens it.
func (s *Server) MessageProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, in tool.ProjectTaskMessageInput) (tool.ProjectTaskMessageResult, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.UserID == "" {
		return tool.ProjectTaskMessageResult{}, &projectTaskFollowupError{http.StatusForbidden, errors.New("authenticated user required")}
	}
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > projectTaskMessageMaxBytes {
		return tool.ProjectTaskMessageResult{}, &projectTaskFollowupError{http.StatusBadRequest, errors.New("message (1-32000 bytes) is required")}
	}
	key := strings.TrimSpace(in.ClientRequestID)
	if len(key) > 128 {
		return tool.ProjectTaskMessageResult{}, &projectTaskFollowupError{http.StatusBadRequest, errors.New("client_request_id must be at most 128 bytes")}
	}
	if key == "" {
		var raw [12]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return tool.ProjectTaskMessageResult{}, err
		}
		key = "task-message-" + hex.EncodeToString(raw[:])
	}
	if in.Revision < 0 {
		return tool.ProjectTaskMessageResult{}, &projectTaskFollowupError{http.StatusBadRequest, errors.New("expected_revision must be positive")}
	}
	task, sessionID, err := s.projectTaskControlTarget(p, projectID, taskID)
	if err != nil {
		return tool.ProjectTaskMessageResult{}, err
	}
	// A retry with the key of an earlier message never delivers it twice,
	// whatever the run state is now: a reopen it made is replayed while its
	// attempt is the active one and refused once superseded; a note it queued
	// in one of the task's sessions is replayed even after that run ended.
	if active := task.ActiveAttempt(); active != nil && active.ClientRequestID == key {
		reopened, err := s.ReopenProjectTask(ctx, p, projectID, taskID, tool.ProjectTaskFollowupInput{Feedback: in.Message, ClientRequestID: key, Revision: active.RequestRevision})
		if err != nil {
			return tool.ProjectTaskMessageResult{}, err
		}
		return tool.ProjectTaskMessageResult{TaskID: reopened.ID, DeliveredAs: tool.ProjectTaskMessageReopened, RunID: reopened.ExecutionRunID()}, nil
	}
	// The key of an earlier reopen that is no longer the active attempt was
	// already delivered; it is refused, as reopen refuses a historical
	// follow-up, never queued or reopened again.
	for _, attempt := range task.Attempts {
		if attempt.ClientRequestID == key {
			return tool.ProjectTaskMessageResult{}, &projectTaskFollowupError{http.StatusConflict, errors.New("client_request_id belongs to an earlier follow-up that was already delivered; retrieve history instead of resending")}
		}
	}
	if noteSession, found, err := s.projectTaskMessageReplaySession(p, task, key); err != nil {
		return tool.ProjectTaskMessageResult{}, err
	} else if found {
		runID, err := s.replayProjectTaskMessage(p, noteSession, key, in.Message)
		if err != nil {
			return tool.ProjectTaskMessageResult{}, err
		}
		return tool.ProjectTaskMessageResult{TaskID: task.ID, DeliveredAs: tool.ProjectTaskMessageQueued, RunID: runID}, nil
	}
	if sessionID != "" {
		runID, queued, err := s.queueProjectTaskMessage(p, sessionID, key, in.Message)
		if err != nil {
			return tool.ProjectTaskMessageResult{}, err
		}
		if queued {
			return tool.ProjectTaskMessageResult{TaskID: task.ID, DeliveredAs: tool.ProjectTaskMessageQueued, RunID: runID}, nil
		}
	}
	revision := in.Revision
	if revision == 0 {
		revision = task.Revision
	}
	reopened, err := s.ReopenProjectTask(ctx, p, projectID, taskID, tool.ProjectTaskFollowupInput{Feedback: in.Message, ClientRequestID: key, Revision: revision})
	if err != nil {
		return tool.ProjectTaskMessageResult{}, err
	}
	return tool.ProjectTaskMessageResult{TaskID: reopened.ID, DeliveredAs: tool.ProjectTaskMessageReopened, RunID: reopened.ExecutionRunID()}, nil
}

// StopProjectTask stops the active run of the task's session, the same stop
// the person has. A task with no active run is refused, never reported as
// stopped.
func (s *Server) StopProjectTask(_ context.Context, p identity.Principal, projectID, taskID string) (tool.ProjectTaskStopResult, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.UserID == "" {
		return tool.ProjectTaskStopResult{}, &projectTaskFollowupError{http.StatusForbidden, errors.New("authenticated user required")}
	}
	task, sessionID, err := s.projectTaskControlTarget(p, projectID, taskID)
	if err != nil {
		return tool.ProjectTaskStopResult{}, err
	}
	// A task waiting for its provider's limit has no active run; the stop
	// cancels the wait instead, so nothing continues it.
	if task.Status == pebblestore.ProjectTaskStatusLimitWaiting {
		cancelled, err := s.cancelProjectTaskLimitWait(p.AccountScopeID, task.ProjectID, task.ID)
		if err != nil {
			return tool.ProjectTaskStopResult{}, &projectTaskFollowupError{http.StatusInternalServerError, err}
		}
		if cancelled {
			return tool.ProjectTaskStopResult{TaskID: task.ID, RunID: task.LimitRunID, Status: "stopped", Next: "message_task resumes it"}, nil
		}
		// The wait ended meanwhile (its continuation started): stop that run.
	}
	if sessionID == "" {
		return tool.ProjectTaskStopResult{}, &projectTaskFollowupError{http.StatusConflict, errors.New("task has no session to stop")}
	}
	runID, err := s.stopActiveSessionRun(p, sessionID, projectTaskStopReason)
	if err != nil {
		return tool.ProjectTaskStopResult{}, &projectTaskFollowupError{http.StatusInternalServerError, err}
	}
	if runID == "" {
		return tool.ProjectTaskStopResult{}, &projectTaskFollowupError{http.StatusConflict, errors.New("task has no active run")}
	}
	return tool.ProjectTaskStopResult{TaskID: task.ID, RunID: runID, Status: "stopped", Next: "message_task resumes it"}, nil
}

// projectTaskControlTarget reads the account's task and the session of its
// current attempt, which must be the principal's.
func (s *Server) projectTaskControlTarget(p identity.Principal, projectID, taskID string) (*pebblestore.ProjectTaskRecord, string, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return nil, "", &projectTaskFollowupError{http.StatusBadRequest, errors.New("project_id and task_id are required")}
	}
	if s.sessions == nil {
		return nil, "", &projectTaskFollowupError{http.StatusServiceUnavailable, errors.New("session service unavailable")}
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, "", err
	}
	if !found || task == nil {
		return nil, "", &projectTaskFollowupError{http.StatusNotFound, errors.New("task not found")}
	}
	if task.Archived {
		return nil, "", &projectTaskFollowupError{http.StatusConflict, errors.New("archived task cannot be messaged or stopped")}
	}
	task.EnsureTaskAttempts()
	sessionID := strings.TrimSpace(task.SessionID)
	if attempt := task.ActiveAttempt(); attempt != nil && strings.TrimSpace(attempt.SessionID) != "" {
		sessionID = strings.TrimSpace(attempt.SessionID)
	}
	if sessionID == "" {
		return task, "", nil
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, "", err
	}
	if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
		return nil, "", &projectTaskFollowupError{http.StatusForbidden, errors.New("task session ownership mismatch")}
	}
	return task, sessionID, nil
}

// queueProjectTaskMessage queues message for the session's active run as a
// person's queued note. queued is false when the session has no active run;
// a run that is active but does not take notes yet is refused, so a message
// never starts a second run beside it.
func (s *Server) queueProjectTaskMessage(p identity.Principal, sessionID, key, message string) (string, bool, error) {
	if _, active, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return "", false, err
	} else if !active {
		return "", false, nil
	}
	unlock := runruntime.LockClientToolConfirmations(sessionID)
	defer unlock()
	session, found, err := s.requireSessionV3Access(p, sessionID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, &projectTaskFollowupError{http.StatusNotFound, errors.New("task session not found")}
	}
	if sessionEnded(session) {
		return "", false, &projectTaskFollowupError{http.StatusConflict, errSessionV3Ended}
	}
	req := sessionsV3MessageRequest{queueWhileRunning: true, queuedNoteSource: pebblestore.ProjectTaskMessageSourceTask, ClientRequestID: key, IdempotencyKey: key, Role: "user", Content: message}
	result, queued, err := s.acceptSessionsV3QueuedNote(p, session, req, pebblestore.MessageSnapshot{Role: "user", Content: message})
	if err != nil {
		return "", false, err
	}
	if queued {
		fields := sessionsV3QueuedMessageResponseFields(result.Message)
		runID, _ := fields["queued_for_run_id"].(string)
		return runID, true, nil
	}
	if _, active, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return "", false, err
	} else if active {
		return "", false, &projectTaskFollowupError{http.StatusConflict, errors.New("the task's run is starting and cannot take a message yet; try again shortly")}
	}
	return "", false, nil
}

// projectTaskMessageReplaySession finds the task session, current or of an
// earlier attempt, where a message with key was already recorded.
func (s *Server) projectTaskMessageReplaySession(p identity.Principal, task *pebblestore.ProjectTaskRecord, key string) (string, bool, error) {
	seen := map[string]bool{}
	candidates := []string{task.SessionID}
	for _, attempt := range task.Attempts {
		candidates = append(candidates, attempt.SessionID)
	}
	for _, sessionID := range candidates {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		_, found, err := s.sessions.Store().GetV3SessionOperationIdempotencyRecord(p.AccountScopeID, sessionID, sessionruntime.SessionMutationAppendMessage, key)
		if err != nil {
			return "", false, err
		}
		if found {
			return sessionID, true, nil
		}
	}
	return "", false, nil
}

// replayProjectTaskMessage replays the queued note recorded with key in
// sessionID. The key reused for a different message is refused.
func (s *Server) replayProjectTaskMessage(p identity.Principal, sessionID, key, message string) (string, error) {
	unlock := runruntime.LockClientToolConfirmations(sessionID)
	defer unlock()
	session, found, err := s.requireSessionV3Access(p, sessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", &projectTaskFollowupError{http.StatusNotFound, errors.New("task session not found")}
	}
	req := sessionsV3MessageRequest{queueWhileRunning: true, queuedNoteSource: pebblestore.ProjectTaskMessageSourceTask, ClientRequestID: key, IdempotencyKey: key, Role: "user", Content: message}
	result, queued, err := s.acceptSessionsV3QueuedNote(p, session, req, pebblestore.MessageSnapshot{Role: "user", Content: message})
	if err != nil {
		return "", err
	}
	if !queued || result.Message == nil || !runruntime.IsQueuedNote(*result.Message) {
		return "", &projectTaskFollowupError{http.StatusConflict, errors.New("client_request_id was already used for a different message")}
	}
	runID, _ := sessionsV3QueuedMessageResponseFields(result.Message)["queued_for_run_id"].(string)
	return runID, nil
}

// handleProjectTaskControl serves POST .../tasks/{taskId}/message and /stop.
func (s *Server) handleProjectTaskControl(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID, taskID, op string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
		return
	}
	writeFailure := func(err error) {
		var failure *projectTaskFollowupError
		if errors.As(err, &failure) {
			writeError(w, failure.status, failure.err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
	}
	if op == "stop" {
		result, err := s.StopProjectTask(r.Context(), p, projectID, taskID)
		if err != nil {
			writeFailure(err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var req struct {
		Message          string `json:"message"`
		ClientRequestID  string `json:"client_request_id"`
		ExpectedRevision int    `json:"expected_revision"`
	}
	if !readProjectTaskJSON(w, r, &req) {
		return
	}
	if !utf8.ValidString(req.Message) {
		writeError(w, http.StatusBadRequest, errors.New("message must be UTF-8 text"))
		return
	}
	result, err := s.MessageProjectTask(r.Context(), p, projectID, taskID, tool.ProjectTaskMessageInput{Message: req.Message, ClientRequestID: req.ClientRequestID, Revision: req.ExpectedRevision})
	if err != nil {
		writeFailure(err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
