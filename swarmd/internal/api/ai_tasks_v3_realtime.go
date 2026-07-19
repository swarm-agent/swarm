package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	v3AITaskLifecycleEventType = "task.lifecycle.updated"
	v3AITaskResourceSessionID  = "__desktop_ai_tasks__"
)

type sessionsV3AITaskLifecyclePayload struct {
	TaskID               string `json:"task_id"`
	AccountScopeID       string `json:"account_scope_id"`
	UserID               string `json:"user_id"`
	WorkspaceID          string `json:"workspace_id"`
	WorkspacePath        string `json:"workspace_path"`
	RequestTitle         string `json:"request_title"`
	DisplayTitle         string `json:"display_title,omitempty"`
	State                string `json:"state"`
	Version              uint64 `json:"version"`
	ManagedSessionID     string `json:"managed_session_id,omitempty"`
	ManagedRunID         string `json:"managed_run_id,omitempty"`
	PreparationSessionID string `json:"preparation_session_id,omitempty"`
	PreparationRunID     string `json:"preparation_run_id,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
	CompletedAt          int64  `json:"completed_at,omitempty"`
	Result               string `json:"result,omitempty"`
	Error                string `json:"error,omitempty"`
}

func newSessionsV3AITaskLifecyclePayload(item pebblestore.WorkspaceTodoItem) sessionsV3AITaskLifecyclePayload {
	return sessionsV3AITaskLifecyclePayload{
		TaskID: item.ID, AccountScopeID: item.AccountScopeID, UserID: item.UserID,
		WorkspaceID: item.WorkspaceID, WorkspacePath: item.WorkspacePath,
		RequestTitle: item.Text, DisplayTitle: item.AIDisplayTitle,
		State: item.AIState, Version: item.AIStateVersion,
		ManagedSessionID: item.ManagedSessionID, ManagedRunID: item.FinalRunID,
		PreparationSessionID: item.PreparationSessionID, PreparationRunID: item.PreparationRunID,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, CompletedAt: item.CompletedAt,
		Result: item.AIResult, Error: item.AIError,
	}
}

// publishAITaskLifecycle commits the task resource to the same durable outbox
// consumed by /v3/realtime/stream. The version-derived key makes retries and
// repeated terminal observation idempotent.
func (s *Server) publishAITaskLifecycle(item pebblestore.WorkspaceTodoItem) error {
	if s == nil || s.sessions == nil || strings.TrimSpace(item.ID) == "" || item.AIStateVersion == 0 {
		return nil
	}
	payload := newSessionsV3AITaskLifecyclePayload(item)
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	key := fmt.Sprintf("ai-task-lifecycle:%s:%d", item.ID, item.AIStateVersion)
	now := item.UpdatedAt
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: v3AITaskResourceSessionID, UserID: item.UserID, AccountScopeID: item.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key,
		PayloadHash: fmt.Sprintf("sha256:%x", hash), RequestHash: fmt.Sprintf("sha256:%x", hash),
		Kind: "task.lifecycle.update", EventType: v3AITaskLifecycleEventType,
		EventPayload: raw, CorrelationID: item.ID, NowUnixMs: now,
	})
	return err
}

func sessionsV3AITaskMatchesSelector(item pebblestore.WorkspaceTodoItem, selector sessionsV3SyncSelector) bool {
	if selector.Global || strings.TrimSpace(selector.Kind) == "global" {
		return true
	}
	for _, sessionID := range selector.SessionIDs {
		if strings.TrimSpace(sessionID) == strings.TrimSpace(item.OriginSessionID) || strings.TrimSpace(sessionID) == strings.TrimSpace(item.ManagedSessionID) {
			return true
		}
	}
	if strings.TrimSpace(selector.Kind) == "recent" && len(selector.WorkspacePaths) == 0 && strings.TrimSpace(selector.WorkspacePath) == "" {
		return true
	}
	paths := append([]string(nil), selector.WorkspacePaths...)
	if selector.WorkspacePath != "" {
		paths = append(paths, selector.WorkspacePath)
	}
	for _, path := range paths {
		normalized, ok := normalizeV3RealtimeWorkspaceCandidate(path)
		if ok && normalized == item.WorkspacePath {
			return true
		}
	}
	return false
}

func sessionsV3AITaskLifecyclePayloadFromRecord(record sessionruntime.RealtimeOutboxRecord) (sessionsV3AITaskLifecyclePayload, bool) {
	if strings.TrimSpace(record.Event.EventType) != v3AITaskLifecycleEventType || len(record.Event.Payload) == 0 {
		return sessionsV3AITaskLifecyclePayload{}, false
	}
	var payload sessionsV3AITaskLifecyclePayload
	if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
		return sessionsV3AITaskLifecyclePayload{}, false
	}
	return payload, payload.TaskID != "" && payload.AccountScopeID != "" && payload.WorkspacePath != ""
}

// reconcileAITaskRunLifecycle is the in-process bridge from the managed V3 run
// terminal mutation to the durable task read model. It performs only the single
// explicit task point read required to verify linkage; there is no polling.
func (s *Server) reconcileAITaskRunLifecycle(job sessionV3ExecutorJob, status, reason, resultText string) error {
	if s == nil || s.todos == nil || s.sessions == nil {
		return nil
	}
	session, ok, err := s.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return err
	}
	taskID := sessionsV3MetadataString(session.Metadata, "ai_task_id")
	workspacePath := sessionsV3MetadataString(session.Metadata, "ai_task_workspace_path")
	if taskID == "" || workspacePath == "" {
		return nil
	}
	task, ok, err := s.todos.GetAITask(job.Principal.AccountScopeID, workspacePath, taskID)
	if err != nil || !ok {
		return err
	}
	if task.AIState != pebblestore.WorkspaceTodoAIStateInProgress || task.ManagedSessionID != job.SessionID {
		return nil
	}
	if task.FinalRunID != "" && task.FinalRunID != job.RunID {
		return nil
	}
	next := ""
	switch status {
	case sessionruntime.RunIntentCompleted:
		next = pebblestore.WorkspaceTodoAIStateCompleted
	case sessionruntime.RunIntentCancelled:
		next = pebblestore.WorkspaceTodoAIStateCancelled
	case sessionruntime.RunIntentFailed, sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked:
		next = pebblestore.WorkspaceTodoAIStateFailed
	default:
		return nil
	}
	_, err = s.todos.BindAITaskLifecycle(task.AccountScopeID, task.WorkspacePath, task.ID, task.AIState, next, task.AIMode, task.AIWorktree, task.ManagedSessionID, firstNonEmptyString(task.AIDisplayTitle, session.Title), task.FinalRunID, strings.TrimSpace(resultText), strings.TrimSpace(reason))
	return err
}
