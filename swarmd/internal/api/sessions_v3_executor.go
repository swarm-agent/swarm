package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionV3ExecutorQueueSize         = 128
	sessionV3ExecutorDefaultStartDelay = 10 * time.Millisecond
)

type sessionV3ExecutorJob struct {
	Principal identity.Principal
	SessionID string
	RunID     string
}

type sessionV3Executor struct {
	server *Server
	queue  chan sessionV3ExecutorJob

	startDelay time.Duration
	modelDelay time.Duration

	mu              sync.Mutex
	inFlightRuns    map[string]bool
	activeBySession map[string]string
}

func newSessionV3Executor(server *Server) *sessionV3Executor {
	exec := &sessionV3Executor{
		server:          server,
		queue:           make(chan sessionV3ExecutorJob, sessionV3ExecutorQueueSize),
		startDelay:      sessionV3ExecutorDefaultStartDelay,
		inFlightRuns:    make(map[string]bool),
		activeBySession: make(map[string]string),
	}
	ctx := context.Background()
	if server != nil && server.runCtx != nil {
		ctx = server.runCtx
	}
	go exec.loop(ctx)
	return exec
}

func (e *sessionV3Executor) EnqueueRun(job sessionV3ExecutorJob) bool {
	if e == nil || e.server == nil {
		return false
	}
	job.SessionID = strings.TrimSpace(job.SessionID)
	job.RunID = strings.TrimSpace(job.RunID)
	if job.SessionID == "" || job.RunID == "" {
		return false
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	e.mu.Lock()
	if e.inFlightRuns[runKey] {
		e.mu.Unlock()
		return false
	}
	if activeRunID := e.activeBySession[job.SessionID]; activeRunID != "" && activeRunID != job.RunID {
		e.mu.Unlock()
		return false
	}
	e.inFlightRuns[runKey] = true
	e.activeBySession[job.SessionID] = job.RunID
	e.mu.Unlock()

	select {
	case e.queue <- job:
		return true
	default:
		e.finish(job)
		return false
	}
}

func (e *sessionV3Executor) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-e.queue:
			if ctx.Err() != nil {
				e.finish(job)
				continue
			}
			e.run(ctx, job)
		}
	}
}

func (e *sessionV3Executor) finish(job sessionV3ExecutorJob) {
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	e.mu.Lock()
	delete(e.inFlightRuns, runKey)
	if e.activeBySession[job.SessionID] == job.RunID {
		delete(e.activeBySession, job.SessionID)
	}
	e.mu.Unlock()
}

func (e *sessionV3Executor) run(ctx context.Context, job sessionV3ExecutorJob) {
	defer e.finish(job)
	if e.server == nil || e.server.sessions == nil {
		return
	}
	e.server.beginActiveRun()
	defer e.server.endActiveRun()
	if e.startDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.startDelay):
		}
	}
	intent, ok, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentPendingExecutor {
		return
	}
	if _, err := e.recordRunStatus(job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
		return
	}
	select {
	case <-ctx.Done():
		_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, "executor stopped", "session.run.failed")
		return
	default:
	}
	if e.modelDelay > 0 {
		select {
		case <-ctx.Done():
			_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, "executor stopped", "session.run.failed")
			return
		case <-time.After(e.modelDelay):
		}
	}
	content, err := e.fakeAssistantResponse(job.SessionID)
	if err != nil {
		_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		return
	}
	if _, err := e.completeRun(job, content); err != nil {
		_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
	}
}

func (e *sessionV3Executor) recordRunStatus(job sessionV3ExecutorJob, status, reason, eventType string) (sessionruntime.SessionMutationResult, error) {
	now := time.Now().UnixMilli()
	intent := pebblestore.V3SessionRunIntent{
		RunID:         job.RunID,
		Status:        status,
		BlockedReason: strings.TrimSpace(reason),
		UpdatedAt:     now,
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, status, reason, eventType, "")
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: sessionV3ExecutorClientRequestID(eventType, job.RunID),
		IdempotencyKey:  sessionV3ExecutorClientRequestID(eventType, job.RunID),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       eventType,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
}

func (e *sessionV3Executor) completeRun(job sessionV3ExecutorJob, content string) (sessionruntime.SessionMutationResult, error) {
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{
		ID:        sessionV3AssistantMessageID(job.SessionID, job.RunID),
		Role:      "assistant",
		Content:   content,
		CreatedAt: now,
		Metadata: map[string]any{
			"run_id":        job.RunID,
			"executor_kind": "v3_fake_model",
		},
	}
	intent := pebblestore.V3SessionRunIntent{
		RunID:     job.RunID,
		Status:    sessionruntime.RunIntentCompleted,
		UpdatedAt: now,
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentCompleted, "", "session.assistant.completed", content)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := sessionV3ExecutorClientRequestID("session.assistant.completed", job.RunID)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.assistant.completed",
		Message:         &message,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
}

func (e *sessionV3Executor) fakeAssistantResponse(sessionID string) (string, error) {
	messages, err := e.server.sessions.ListSessionMessages(sessionID, 0, 500)
	if err != nil {
		return "", err
	}
	lastUser := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) == "user" {
			lastUser = messages[i].Content
			break
		}
	}
	if strings.TrimSpace(lastUser) == "" {
		return "V3 fake assistant response.", nil
	}
	return "V3 fake assistant response: " + lastUser, nil
}

func sessionV3ExecutorRunKey(sessionID, runID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID)
}

func sessionV3ExecutorClientRequestID(eventType, runID string) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	return "v3-executor-" + label + "-" + strings.TrimSpace(runID)
}

func sessionV3AssistantMessageID(sessionID, runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00assistant"))
	return "v3msg_assistant_" + hex.EncodeToString(sum[:16])
}

func sessionV3ExecutorPayloadHash(sessionID, runID, status, reason, eventType, content string) (string, error) {
	canonical := struct {
		Operation string `json:"operation"`
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason,omitempty"`
		EventType string `json:"event_type"`
		Content   string `json:"content,omitempty"`
	}{
		Operation: "v3.executor.run",
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Status:    strings.TrimSpace(status),
		Reason:    strings.TrimSpace(reason),
		EventType: strings.TrimSpace(eventType),
		Content:   content,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal v3 executor payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
