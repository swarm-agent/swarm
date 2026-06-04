package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionV3ExecutorQueueSize                = 128
	sessionV3ExecutorDefaultStartDelay        = 10 * time.Millisecond
	sessionV3ExecutorRecoveryLimit            = 500
	sessionV3ExecutorDefaultRunningStaleAfter = 5 * time.Minute
	sessionV3AssistantDeltaFlushMaxBytes      = 512
	sessionV3AssistantDeltaFlushMaxDelay      = 100 * time.Millisecond
)

type sessionV3ExecutorJob struct {
	Principal identity.Principal
	SessionID string
	RunID     string
}

type sessionV3Executor struct {
	server *Server
	queue  chan sessionV3ExecutorJob

	startDelay         time.Duration
	modelDelay         time.Duration
	runningStaleAfter  time.Duration
	deltaFlushMaxBytes int
	deltaFlushMaxDelay time.Duration

	mu              sync.Mutex
	inFlightRuns    map[string]bool
	activeBySession map[string]string
}

func newSessionV3Executor(server *Server) *sessionV3Executor {
	exec := &sessionV3Executor{
		server:             server,
		queue:              make(chan sessionV3ExecutorJob, sessionV3ExecutorQueueSize),
		startDelay:         sessionV3ExecutorDefaultStartDelay,
		runningStaleAfter:  sessionV3ExecutorDefaultRunningStaleAfter,
		deltaFlushMaxBytes: sessionV3AssistantDeltaFlushMaxBytes,
		deltaFlushMaxDelay: sessionV3AssistantDeltaFlushMaxDelay,
		inFlightRuns:       make(map[string]bool),
		activeBySession:    make(map[string]string),
	}
	ctx := context.Background()
	if server != nil && server.runCtx != nil {
		ctx = server.runCtx
	}
	go exec.loop(ctx)
	exec.recoverDurableRuns(ctx)
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

func (e *sessionV3Executor) recoverDurableRuns(ctx context.Context) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	staleBefore := int64(0)
	if e.runningStaleAfter > 0 {
		staleBefore = time.Now().Add(-e.runningStaleAfter).UnixMilli()
	}
	intents, err := e.server.sessions.ListRecoverableSessionRunIntents(staleBefore, sessionV3ExecutorRecoveryLimit)
	if err != nil {
		log.Printf("warning: v3 session executor recovery scan failed: %v", err)
		return
	}
	for _, intent := range intents {
		if ctx.Err() != nil {
			return
		}
		if strings.TrimSpace(intent.RunID) == "" || strings.TrimSpace(intent.SessionID) == "" {
			continue
		}
		job := sessionV3ExecutorJob{
			Principal: identity.Principal{
				Type:           identity.PrincipalTypeUser,
				UserID:         intent.UserID,
				AccountScopeID: intent.AccountScopeID,
			},
			SessionID: intent.SessionID,
			RunID:     intent.RunID,
		}
		if strings.TrimSpace(job.Principal.UserID) == "" || strings.TrimSpace(job.Principal.AccountScopeID) == "" {
			if session, ok, err := e.server.sessions.GetSession(intent.SessionID); err != nil {
				log.Printf("warning: v3 session executor recovery could not hydrate session %q for run %q: %v", intent.SessionID, intent.RunID, err)
				continue
			} else if ok {
				if job.Principal.UserID == "" {
					job.Principal.UserID = session.UserID
				}
				if job.Principal.AccountScopeID == "" {
					job.Principal.AccountScopeID = session.AccountScopeID
				}
			}
		}
		if !job.Principal.Valid() {
			log.Printf("warning: v3 session executor recovery skipped run %q for session %q: missing principal", intent.RunID, intent.SessionID)
			continue
		}
		if intent.Status == sessionruntime.RunIntentRunning {
			if err := e.resetRunningRunForRecovery(job); err != nil {
				log.Printf("warning: v3 session executor recovery could not reset run %q for session %q: %v", job.RunID, job.SessionID, err)
				continue
			}
		}
		e.EnqueueRun(job)
	}
}

func (e *sessionV3Executor) resetRunningRunForRecovery(job sessionV3ExecutorJob) error {
	now := time.Now().UnixMilli()
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentPendingExecutor, "startup recovery", "session.run.recovered", "")
	if err != nil {
		return err
	}
	intent := pebblestore.V3SessionRunIntent{RunID: job.RunID, Status: sessionruntime.RunIntentPendingExecutor, BlockedReason: "startup recovery", UpdatedAt: now}
	_, err = e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: sessionV3ExecutorClientRequestID("session.run.recovered", job.RunID),
		IdempotencyKey:  sessionV3ExecutorClientRequestID("session.run.recovered", job.RunID),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run.recovered",
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
	return err
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
	content, err := e.assistantResponse(ctx, job)
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
	var eventPayload json.RawMessage
	if eventType == "session.run.failed" {
		raw, err := json.Marshal(map[string]any{
			"run_id": job.RunID,
			"status": status,
			"error":  strings.TrimSpace(reason),
		})
		if err != nil {
			return sessionruntime.SessionMutationResult{}, err
		}
		eventPayload = raw
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
		EventPayload:    eventPayload,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
}

func (e *sessionV3Executor) recordRunProgress(job sessionV3ExecutorJob, deltaIndex int, delta string) (sessionruntime.SessionMutationResult, error) {
	now := time.Now().UnixMilli()
	metadata := map[string]any{
		"run_id":      job.RunID,
		"delta_index": deltaIndex,
		"delta":       delta,
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	intent := pebblestore.V3SessionRunIntent{RunID: job.RunID, Status: sessionruntime.RunIntentRunning, UpdatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, "", "session.assistant.delta", fmt.Sprintf("%d:%s", deltaIndex, delta))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := fmt.Sprintf("%s-%04d", sessionV3ExecutorClientRequestID("session.assistant.delta", job.RunID), deltaIndex)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.delta",
		EventPayload:    raw,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
}

type sessionV3AssistantDeltaCoalescer struct {
	exec            *sessionV3Executor
	job             sessionV3ExecutorJob
	buf             strings.Builder
	bufferStartedAt time.Time
	nextDeltaIndex  int
	flushCount      int
}

func newSessionV3AssistantDeltaCoalescer(exec *sessionV3Executor, job sessionV3ExecutorJob) *sessionV3AssistantDeltaCoalescer {
	return &sessionV3AssistantDeltaCoalescer{exec: exec, job: job}
}

func (c *sessionV3AssistantDeltaCoalescer) Add(delta string) error {
	if c == nil || delta == "" {
		return nil
	}
	if c.bufferStartedAt.IsZero() {
		c.bufferStartedAt = time.Now()
	}
	c.buf.WriteString(delta)
	if c.shouldFlush(delta) {
		return c.Flush()
	}
	return nil
}

func (c *sessionV3AssistantDeltaCoalescer) Flush() error {
	if c == nil || c.buf.Len() == 0 {
		return nil
	}
	if c.exec == nil {
		return errors.New("v3 assistant delta coalescer missing executor")
	}
	delta := c.buf.String()
	c.buf.Reset()
	c.bufferStartedAt = time.Time{}
	c.nextDeltaIndex++
	if _, err := c.exec.recordRunProgress(c.job, c.nextDeltaIndex, delta); err != nil {
		return err
	}
	c.flushCount++
	return nil
}

func (c *sessionV3AssistantDeltaCoalescer) FlushCount() int {
	if c == nil {
		return 0
	}
	return c.flushCount
}

func (c *sessionV3AssistantDeltaCoalescer) shouldFlush(delta string) bool {
	if c == nil {
		return false
	}
	if c.exec == nil {
		return true
	}
	maxBytes := c.exec.deltaFlushMaxBytes
	if maxBytes <= 0 {
		maxBytes = sessionV3AssistantDeltaFlushMaxBytes
	}
	if c.buf.Len() >= maxBytes {
		return true
	}
	if strings.Contains(delta, "\n") {
		return true
	}
	maxDelay := c.exec.deltaFlushMaxDelay
	if maxDelay > 0 && !c.bufferStartedAt.IsZero() && time.Since(c.bufferStartedAt) >= maxDelay {
		return true
	}
	return false
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

func (e *sessionV3Executor) assistantResponse(ctx context.Context, job sessionV3ExecutorJob) (string, error) {
	if e != nil && e.server != nil && e.server.providers != nil {
		content, usedProvider, err := e.providerAssistantResponse(ctx, job)
		if usedProvider || err != nil {
			return content, err
		}
	}
	content, err := e.fakeAssistantResponse(job.SessionID)
	if err != nil {
		return "", err
	}
	coalescer := newSessionV3AssistantDeltaCoalescer(e, job)
	if err := coalescer.Add(content); err != nil {
		return "", err
	}
	if err := coalescer.Flush(); err != nil {
		return "", err
	}
	return content, nil
}

func (e *sessionV3Executor) providerAssistantResponse(ctx context.Context, job sessionV3ExecutorJob) (string, bool, error) {
	session, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil {
		return "", true, err
	}
	if !ok {
		return "", true, fmt.Errorf("session %q not found", job.SessionID)
	}
	pref := normalizeSessionsV3ModelPreference(session.Preference)
	providerID := strings.ToLower(strings.TrimSpace(pref.Provider))
	modelName := strings.TrimSpace(pref.Model)
	if providerID == "" || modelName == "" {
		return "", false, nil
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok {
		return "", true, fmt.Errorf("provider %q is configured but not runnable yet", providerID)
	}
	messages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, 500)
	if err != nil {
		return "", true, err
	}
	input := sessionsV3ProviderInput(messages)
	if len(input) == 0 {
		return "", true, errors.New("v3 provider input is empty")
	}
	thinking := strings.TrimSpace(pref.Thinking)
	if thinking == "" {
		thinking = "medium"
	}
	serviceTier := ""
	if providerID == "codex" {
		serviceTier = codexruntime.NormalizeServiceTier(pref.ServiceTier)
	}
	req := provideriface.Request{
		SessionID:     job.SessionID,
		Model:         modelName,
		Thinking:      thinking,
		Instructions:  "You are Swarm, a concise coding assistant. Answer the user's latest message using the committed V3 session history.",
		Input:         input,
		ToolChoice:    "none",
		ServiceTier:   serviceTier,
		ContextMode:   strings.TrimSpace(pref.ContextMode),
		WorkspacePath: strings.TrimSpace(session.WorkspacePath),
	}
	var streamed strings.Builder
	coalescer := newSessionV3AssistantDeltaCoalescer(e, job)
	var progressErr error
	response, err := runner.CreateResponseStreaming(ctx, req, func(event provideriface.StreamEvent) {
		if event.Type != provideriface.StreamEventOutputTextDelta {
			return
		}
		streamed.WriteString(event.Delta)
		if progressErr == nil {
			progressErr = coalescer.Add(event.Delta)
		}
	})
	if flushErr := coalescer.Flush(); flushErr != nil && progressErr == nil {
		progressErr = flushErr
	}
	if err != nil {
		return "", true, err
	}
	if progressErr != nil {
		return "", true, progressErr
	}
	content := strings.TrimSpace(response.Text)
	if content == "" {
		content = strings.TrimSpace(streamed.String())
	}
	if content == "" {
		for _, message := range response.AssistantMessages {
			if message.Phase != "" && message.Phase != provideriface.AssistantPhaseFinalAnswer {
				continue
			}
			if text := strings.TrimSpace(message.Text); text != "" {
				if content != "" {
					content += "\n\n"
				}
				content += text
			}
		}
	}
	if content == "" {
		return "", true, errors.New("provider returned empty assistant response")
	}
	if coalescer.FlushCount() == 0 {
		if err := coalescer.Add(content); err != nil {
			return "", true, err
		}
		if err := coalescer.Flush(); err != nil {
			return "", true, err
		}
	}
	return content, true, nil
}

func sessionsV3ProviderInput(messages []pebblestore.MessageSnapshot) []map[string]any {
	input := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			input = append(input, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": content}}})
		case "system":
			input = append(input, map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "[system] " + content}}})
		case "reasoning", "tool":
			continue
		default:
			input = append(input, map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": content}}})
		}
	}
	return input
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
