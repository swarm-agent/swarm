package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"

	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	sessionV3ExecutorQueueSize                = 128
	sessionV3ExecutorDefaultStartDelay        = 10 * time.Millisecond
	sessionV3ExecutorRecoveryLimit            = 500
	sessionV3ExecutorDefaultRunningStaleAfter = 5 * time.Minute
	sessionV3ProviderToolLoopMaxSteps         = 8
	sessionV3AssistantDeltaFlushMaxBytes      = 512
	sessionV3AssistantDeltaFlushMaxDelay      = 100 * time.Millisecond
	sessionV3RunStopDefaultReason             = "run stopped by user"
	sessionV3TitleDefault                     = "New Session"
	sessionV3TitleConversationLimit           = 24
	sessionV3TitlePromptPreviewRunes          = 2000
	sessionV3TitleGenerationTimeout           = 20 * time.Second
	sessionV3TitleFinalWordsMin               = 5
	sessionV3TitleFinalWordsMax               = 6
)

var sessionV3TitleWordPattern = regexp.MustCompile(`\b[\p{L}\p{N}][\p{L}\p{N}'-]*\b`)

type sessionV3ExecutorJob struct {
	Principal identity.Principal
	SessionID string
	RunID     string
}

type sessionV3ExecutorRunState struct {
	cancel   context.CancelFunc
	canceled bool
	reason   string
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
	runStates       map[string]*sessionV3ExecutorRunState
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
		runStates:          make(map[string]*sessionV3ExecutorRunState),
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
	if e.runStates == nil {
		e.runStates = make(map[string]*sessionV3ExecutorRunState)
	}
	if e.runStates[runKey] == nil {
		e.runStates[runKey] = &sessionV3ExecutorRunState{}
	}
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
	delete(e.runStates, runKey)
	if e.activeBySession[job.SessionID] == job.RunID {
		delete(e.activeBySession, job.SessionID)
	}
	e.mu.Unlock()
}

func (e *sessionV3Executor) attachCancel(job sessionV3ExecutorJob, cancel context.CancelFunc) {
	if e == nil || cancel == nil {
		return
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	shouldCancel := false
	e.mu.Lock()
	if e.runStates == nil {
		e.runStates = make(map[string]*sessionV3ExecutorRunState)
	}
	state := e.runStates[runKey]
	if state == nil {
		state = &sessionV3ExecutorRunState{}
		e.runStates[runKey] = state
	}
	state.cancel = cancel
	shouldCancel = state.canceled
	e.mu.Unlock()
	if shouldCancel {
		cancel()
	}
}

func (e *sessionV3Executor) isRunCanceled(job sessionV3ExecutorJob) bool {
	if e == nil {
		return false
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.runStates[runKey]
	return state != nil && state.canceled
}

func (e *sessionV3Executor) cancellationReason(job sessionV3ExecutorJob) string {
	if e == nil {
		return sessionV3RunStopDefaultReason
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if state := e.runStates[runKey]; state != nil {
		if reason := strings.TrimSpace(state.reason); reason != "" {
			return reason
		}
	}
	return sessionV3RunStopDefaultReason
}

func (e *sessionV3Executor) CancelRun(job sessionV3ExecutorJob, reason string) (sessionruntime.SessionMutationResult, bool, error) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return sessionruntime.SessionMutationResult{}, false, errors.New("v3 executor is not configured")
	}
	job.SessionID = strings.TrimSpace(job.SessionID)
	job.RunID = strings.TrimSpace(job.RunID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = sessionV3RunStopDefaultReason
	}
	if job.SessionID == "" || job.RunID == "" {
		return sessionruntime.SessionMutationResult{}, false, errors.New("session id and run id are required")
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	var cancel context.CancelFunc
	tracked := false
	e.mu.Lock()
	if e.runStates == nil {
		e.runStates = make(map[string]*sessionV3ExecutorRunState)
	}
	state := e.runStates[runKey]
	if state != nil {
		tracked = true
		state.canceled = true
		state.reason = reason
		cancel = state.cancel
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	intent, ok, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, tracked, err
	}
	if ok {
		switch intent.Status {
		case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
			result, err := e.recordRunStatus(job, sessionruntime.RunIntentFailed, reason, "session.run.failed")
			return result, true, err
		case sessionruntime.RunIntentFailed:
			if strings.TrimSpace(intent.BlockedReason) == reason {
				return sessionruntime.SessionMutationResult{SessionID: job.SessionID, RunIntent: &intent}, true, nil
			}
		}
	}
	if tracked {
		result, err := e.recordRunStatus(job, sessionruntime.RunIntentFailed, reason, "session.run.failed")
		return result, true, err
	}
	return sessionruntime.SessionMutationResult{}, false, fmt.Errorf("v3 run %q is not active", job.RunID)
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
			if err := e.failStaleRunningRunForRecovery(job); err != nil {
				log.Printf("warning: v3 session executor recovery could not fail interrupted run %q for session %q: %v", job.RunID, job.SessionID, err)
			}
			continue
		}
		e.EnqueueRun(job)
	}
}

func (e *sessionV3Executor) failStaleRunningRunForRecovery(job sessionV3ExecutorJob) error {
	_, err := e.recordRunStatus(job, sessionruntime.RunIntentFailed, "executor interrupted during daemon restart", "session.run.failed")
	return err
}

func (e *sessionV3Executor) run(ctx context.Context, job sessionV3ExecutorJob) {
	defer e.finish(job)
	if e.server == nil || e.server.sessions == nil {
		return
	}
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	e.attachCancel(job, runCancel)
	e.server.beginActiveRun()
	defer e.server.endActiveRun()
	if e.startDelay > 0 {
		select {
		case <-runCtx.Done():
			return
		case <-time.After(e.startDelay):
		}
	}
	if e.isRunCanceled(job) || runCtx.Err() != nil {
		return
	}
	intent, ok, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentPendingExecutor {
		return
	}
	if _, err := e.recordRunStatus(job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
		return
	}
	select {
	case <-runCtx.Done():
		if !e.isRunCanceled(job) {
			_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, "executor stopped", "session.run.failed")
		}
		return
	default:
	}
	if e.modelDelay > 0 {
		select {
		case <-runCtx.Done():
			if !e.isRunCanceled(job) {
				_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, "executor stopped", "session.run.failed")
			}
			return
		case <-time.After(e.modelDelay):
		}
	}
	response, err := e.assistantResponse(runCtx, job)
	if err != nil {
		if !e.isRunCanceled(job) {
			_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		}
		return
	}
	if e.isRunCanceled(job) || runCtx.Err() != nil {
		return
	}
	result, err := e.completeRun(job, response)
	if err != nil {
		if !e.isRunCanceled(job) {
			_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		}
		return
	}
	e.maybeStartSessionV3TitleFlow(job, result)
}

func (e *sessionV3Executor) recordRunStatus(job sessionV3ExecutorJob, status, reason, eventType string) (sessionruntime.SessionMutationResult, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("v3 executor is not configured")
	}
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
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
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

type sessionV3ResolvedRuntime struct {
	Session       pebblestore.SessionSnapshot
	AgentProfile  pebblestore.AgentProfile
	Preference    pebblestore.ModelPreference
	ContextWindow int
	Scope         tool.WorkspaceScope
	Instructions  string
	Tools         []provideriface.ToolDefinition
	ToolChoice    string
}

type sessionV3AssistantResponse struct {
	Content            string
	ExecutorKind       string
	ProviderID         string
	Model              string
	ProviderResponseID string
	StopReason         string
	Usage              provideriface.TokenUsage
}

func (r sessionV3AssistantResponse) metadata(runID string) map[string]any {
	metadata := map[string]any{
		"run_id":        strings.TrimSpace(runID),
		"executor_kind": strings.TrimSpace(r.ExecutorKind),
	}
	if metadata["executor_kind"] == "" {
		metadata["executor_kind"] = "v3_provider"
	}
	if providerID := strings.TrimSpace(r.ProviderID); providerID != "" {
		metadata["provider"] = providerID
	}
	if model := strings.TrimSpace(r.Model); model != "" {
		metadata["model"] = model
	}
	if responseID := strings.TrimSpace(r.ProviderResponseID); responseID != "" {
		metadata["provider_response_id"] = responseID
	}
	if stopReason := strings.TrimSpace(r.StopReason); stopReason != "" {
		metadata["stop_reason"] = stopReason
	}
	if r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 || r.Usage.ThinkingTokens != 0 || r.Usage.TotalTokens != 0 || r.Usage.CacheReadTokens != 0 || r.Usage.CacheWriteTokens != 0 || r.Usage.Source != "" || r.Usage.Transport != "" {
		metadata["usage"] = r.Usage
	}
	return metadata
}

func (e *sessionV3Executor) completeRun(job sessionV3ExecutorJob, response sessionV3AssistantResponse) (sessionruntime.SessionMutationResult, error) {
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	content := response.Content
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{
		ID:        sessionV3AssistantMessageID(job.SessionID, job.RunID),
		Role:      "assistant",
		Content:   content,
		CreatedAt: now,
		Metadata:  response.metadata(job.RunID),
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

func (e *sessionV3Executor) maybeStartSessionV3TitleFlow(job sessionV3ExecutorJob, result sessionruntime.SessionMutationResult) {
	if e == nil || e.server == nil || e.server.sessions == nil || result.Replayed || result.Message == nil {
		return
	}
	session, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !ok || !shouldGenerateSessionV3Title(session) {
		return
	}
	messages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, sessionV3TitleConversationLimit)
	if err != nil || !shouldGenerateSessionV3TitleWithMessages(session, messages) {
		return
	}
	go e.generateAndApplySessionV3Title(job)
}

func (e *sessionV3Executor) generateAndApplySessionV3Title(job sessionV3ExecutorJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("warning: v3 session title background panic for session %q: %v", job.SessionID, recovered)
		}
	}()
	if e == nil || e.server == nil || e.server.sessions == nil {
		return
	}
	e.server.beginActiveRun()
	defer e.server.endActiveRun()
	session, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !ok || !shouldGenerateSessionV3Title(session) {
		return
	}
	messages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, sessionV3TitleConversationLimit)
	if err != nil || !shouldGenerateSessionV3TitleWithMessages(session, messages) {
		return
	}
	conversation := buildSessionV3TitleConversation(messages)
	if conversation == "" {
		return
	}
	title, err := e.generateSessionV3MemoryTitle(session, conversation, job.Principal)
	if err != nil {
		log.Printf("warning: v3 session title generation failed for session %q: %v", job.SessionID, err)
		return
	}
	if title == "" {
		return
	}
	current, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !ok || !shouldGenerateSessionV3Title(current) {
		return
	}
	now := time.Now().UnixMilli()
	current.Title = title
	current.UpdatedAt = now
	current.Metadata = cloneSessionsV3Metadata(current.Metadata)
	payload, err := json.Marshal(map[string]any{
		"session_id": job.SessionID,
		"title":      title,
		"stage":      "final",
		"updated_at": now,
		"session":    current,
	})
	if err != nil {
		log.Printf("warning: marshal v3 session title payload for session %q: %v", job.SessionID, err)
		return
	}
	payloadHash, err := sessionV3TitlePayloadHash(job.SessionID, job.RunID, title)
	if err != nil {
		log.Printf("warning: hash v3 session title payload for session %q: %v", job.SessionID, err)
		return
	}
	clientRequestID := sessionV3ExecutorClientRequestID("session.title.updated", job.RunID)
	if _, err := e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateTitle,
		EventType:       "session.title.updated",
		EventPayload:    payload,
		Session:         &current,
		NowUnixMs:       now,
	}); err != nil {
		log.Printf("warning: apply v3 session title update for session %q: %v", job.SessionID, err)
	}
}

func (e *sessionV3Executor) generateSessionV3MemoryTitle(session pebblestore.SessionSnapshot, promptContext string, principal identity.Principal) (string, error) {
	if e == nil || e.server == nil || e.server.providers == nil {
		return "", errors.New("provider registry is not configured")
	}
	memoryProfile := pebblestore.AgentProfile{Name: "memory", Prompt: "You are Memory, the durable artifacts agent.", Enabled: true}
	if e.server.agents != nil {
		resolved, err := e.server.agents.ResolveSubagentForAccount(session.AccountScopeID, "memory")
		if err != nil {
			return "", err
		}
		memoryProfile = resolved
	}
	preference, _, err := e.resolveSessionV3ProviderPreference(applySessionV3AgentPreferenceOverrides(session.Preference, memoryProfile))
	if err != nil {
		return "", err
	}
	providerID := strings.ToLower(strings.TrimSpace(preference.Provider))
	if providerID == "" {
		return "", errors.New("resolved memory title provider is empty")
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok {
		return "", fmt.Errorf("memory title provider %q is not runnable", providerID)
	}
	modelName := strings.TrimSpace(preference.Model)
	if modelName == "" {
		return "", errors.New("resolved memory title model is empty")
	}
	instructions := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(memoryProfile.Prompt),
		"You generate deterministic session titles.",
		fmt.Sprintf("Return only the title text with %d to %d words.", sessionV3TitleFinalWordsMin, sessionV3TitleFinalWordsMax),
		"No markdown, no quotes, no explanations, no trailing punctuation.",
		"Stage: final.",
	}, "\n"))
	req := provideriface.Request{
		SessionID:     session.ID,
		Model:         modelName,
		Thinking:      normalizeSessionV3ThinkingWithProvider(providerID, preference.Thinking),
		Instructions:  instructions,
		Input:         []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "Conversation summary:\n" + truncateSessionV3TitleRunes(promptContext, sessionV3TitlePromptPreviewRunes)}}}},
		ToolChoice:    "none",
		ServiceTier:   strings.TrimSpace(preference.ServiceTier),
		ContextMode:   strings.TrimSpace(preference.ContextMode),
		WorkspacePath: strings.TrimSpace(session.WorkspacePath),
	}
	bgCtx := context.Background()
	if principal.Valid() {
		bgCtx = identity.ContextWithPrincipal(bgCtx, principal)
	}
	ctx, cancel := context.WithTimeout(bgCtx, sessionV3TitleGenerationTimeout)
	defer cancel()
	response, err := runner.CreateResponse(ctx, req)
	if err != nil {
		return "", err
	}
	title := sanitizeSessionV3GeneratedTitle(firstNonEmpty(strings.TrimSpace(response.Text), strings.TrimSpace(response.ReasoningSummary)), sessionV3TitleFinalWordsMin, sessionV3TitleFinalWordsMax)
	if title == "" {
		return "", errors.New("memory agent returned an empty/invalid title")
	}
	return title, nil
}

func (e *sessionV3Executor) assistantResponse(ctx context.Context, job sessionV3ExecutorJob) (sessionV3AssistantResponse, error) {
	resolved, err := e.resolveSessionV3Runtime(job)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	return e.providerAssistantResponse(ctx, job, resolved)
}

func (e *sessionV3Executor) providerAssistantResponse(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime) (sessionV3AssistantResponse, error) {
	if e == nil || e.server == nil || e.server.providers == nil {
		return sessionV3AssistantResponse{}, errors.New("provider registry is not configured")
	}
	pref := resolved.Preference
	providerID := strings.ToLower(strings.TrimSpace(pref.Provider))
	modelName := strings.TrimSpace(pref.Model)
	if providerID == "" || modelName == "" {
		return sessionV3AssistantResponse{}, errors.New("resolved v3 provider/model is empty")
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok {
		return sessionV3AssistantResponse{}, fmt.Errorf("provider %q is configured but not runnable yet", providerID)
	}
	messages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, 500)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	input := sessionsV3ProviderInput(messages)
	if len(input) == 0 {
		return sessionV3AssistantResponse{}, errors.New("v3 provider input is empty")
	}
	baseReq := provideriface.Request{
		SessionID:         job.SessionID,
		Model:             modelName,
		Thinking:          strings.TrimSpace(pref.Thinking),
		Instructions:      resolved.Instructions,
		Input:             input,
		Tools:             resolved.Tools,
		ToolChoice:        resolved.ToolChoice,
		ServiceTier:       strings.TrimSpace(pref.ServiceTier),
		ContextMode:       strings.TrimSpace(pref.ContextMode),
		ContextWindow:     resolved.ContextWindow,
		ParallelToolCalls: true,
		WorkspacePath:     strings.TrimSpace(resolved.Scope.PrimaryPath),
	}
	if baseReq.Thinking == "" {
		baseReq.Thinking = "medium"
	}
	if baseReq.Instructions == "" {
		return sessionV3AssistantResponse{}, errors.New("resolved v3 instructions are empty")
	}
	if baseReq.ToolChoice == "" {
		baseReq.ToolChoice = "none"
	}
	ctx = identity.ContextWithPrincipal(ctx, job.Principal)
	response, streamed, flushCount, err := e.runProviderToolLoop(ctx, job, resolved, runner, baseReq)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	content := strings.TrimSpace(response.Text)
	if content == "" {
		content = strings.TrimSpace(streamed)
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
		return sessionV3AssistantResponse{}, errors.New("provider returned empty assistant response")
	}
	if flushCount == 0 {
		if _, err := e.recordRunProgress(job, 1, content); err != nil {
			return sessionV3AssistantResponse{}, err
		}
	}
	model := strings.TrimSpace(response.Model)
	if model == "" {
		model = modelName
	}
	providerRunnerID := strings.TrimSpace(runner.ID())
	if providerRunnerID == "" {
		providerRunnerID = providerID
	}
	return sessionV3AssistantResponse{
		Content:            content,
		ExecutorKind:       "v3_provider",
		ProviderID:         providerRunnerID,
		Model:              model,
		ProviderResponseID: strings.TrimSpace(response.ID),
		StopReason:         strings.TrimSpace(response.StopReason),
		Usage:              response.Usage,
	}, nil
}

func (e *sessionV3Executor) runProviderToolLoop(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, runner provideriface.Runner, baseReq provideriface.Request) (provideriface.Response, string, int, error) {
	if runner == nil {
		return provideriface.Response{}, "", 0, errors.New("provider runner is not configured")
	}
	toolsEnabled := len(baseReq.Tools) > 0 && !strings.EqualFold(strings.TrimSpace(baseReq.ToolChoice), "none")
	input := append([]map[string]any(nil), baseReq.Input...)
	var lastResponse provideriface.Response
	var finalStreamed strings.Builder
	var totalFlushCount int
	for step := 1; step <= sessionV3ProviderToolLoopMaxSteps; step++ {
		var toolInvoker provideriface.ToolInvoker
		if toolsEnabled {
			var invokerErr error
			toolInvoker, invokerErr = e.newSessionV3ProviderToolInvoker(resolved, job, step)
			if invokerErr != nil {
				return provideriface.Response{}, "", totalFlushCount, invokerErr
			}
		}
		req := baseReq
		req.Input = append([]map[string]any(nil), input...)
		req.ToolInvoker = toolInvoker
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
			return provideriface.Response{}, "", totalFlushCount, err
		}
		if progressErr != nil {
			return provideriface.Response{}, "", totalFlushCount, progressErr
		}
		lastResponse = response
		totalFlushCount += coalescer.FlushCount()
		if len(response.FunctionCalls) == 0 && !response.RestartTurn {
			finalStreamed.WriteString(streamed.String())
			return response, finalStreamed.String(), totalFlushCount, nil
		}
		if len(response.FunctionCalls) == 0 {
			if response.RestartTurn {
				input = e.sessionV3ProviderContinuationInput(job)
				if len(input) == 0 {
					return provideriface.Response{}, "", totalFlushCount, errors.New("v3 provider restart requested but continuation input is empty")
				}
				continue
			}
			return provideriface.Response{}, "", totalFlushCount, errors.New("v3 provider requested a tool-loop restart without tool calls")
		}
		if !toolsEnabled {
			return provideriface.Response{}, "", totalFlushCount, errors.New("v3 provider returned tool calls; tool-loop execution is not supported without resolved tools")
		}
		if toolInvoker == nil {
			return provideriface.Response{}, "", totalFlushCount, errors.New("v3 provider tool invoker is not configured")
		}
		toolResults := make([]provideriface.ToolExecutionResult, 0, len(response.FunctionCalls))
		for _, call := range response.FunctionCalls {
			result, err := toolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{
				CallID:    strings.TrimSpace(call.CallID),
				Name:      strings.TrimSpace(call.Name),
				Arguments: strings.TrimSpace(call.Arguments),
				Metadata:  cloneSessionsV3Metadata(call.Metadata),
			})
			if err != nil {
				return provideriface.Response{}, "", totalFlushCount, err
			}
			toolResults = append(toolResults, result)
		}
		input = append(input, sessionsV3ProviderToolResultInputItems(response.FunctionCalls, toolResults)...)
		if len(input) == 0 {
			return provideriface.Response{}, "", totalFlushCount, errors.New("v3 provider continuation input is empty after tool execution")
		}
	}
	if len(lastResponse.FunctionCalls) > 0 || lastResponse.RestartTurn {
		return provideriface.Response{}, "", totalFlushCount, fmt.Errorf("v3 provider tool loop exceeded %d steps", sessionV3ProviderToolLoopMaxSteps)
	}
	return lastResponse, finalStreamed.String(), totalFlushCount, nil
}

func (e *sessionV3Executor) newSessionV3ProviderToolInvoker(resolved sessionV3ResolvedRuntime, job sessionV3ExecutorJob, step int) (provideriface.ToolInvoker, error) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil, errors.New("v3 executor is not configured")
	}
	if e.server.runner == nil {
		return nil, errors.New("run service is not configured")
	}
	builder, ok := e.server.runner.(interface {
		NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig) provideriface.ToolInvoker
	})
	if !ok || builder == nil {
		return nil, errors.New("run service does not support provider-managed tool execution")
	}
	policy, err := e.sessionV3CompiledToolPolicy(resolved)
	if err != nil {
		return nil, err
	}
	workspacePath := strings.TrimSpace(resolved.Scope.PrimaryPath)
	roots := append([]string(nil), resolved.Scope.Roots...)
	if len(roots) == 0 && workspacePath != "" {
		roots = []string{workspacePath}
	}
	if step <= 0 {
		step = 1
	}
	invoker := builder.NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{
		SessionID:            job.SessionID,
		PermissionSessionID:  job.SessionID,
		RunID:                job.RunID,
		Step:                 step,
		SessionMode:          resolved.Session.Mode,
		WorkspacePath:        workspacePath,
		WorkspaceRoots:       roots,
		WorkspaceOriginPath:  workspacePath,
		WorkspaceOriginRoots: roots,
		WorkspaceName:        resolved.Session.WorkspaceName,
		Principal:            job.Principal,
		Policy:               policy,
		Emit:                 e.emitSessionV3ProviderToolEvent(job),
		ApplySessionMutation: e.server.applySessionV3PrimaryMutation,
		AgentProfile:         resolved.AgentProfile,
	})
	if invoker == nil {
		return nil, errors.New("provider-managed tool invoker is not configured")
	}
	return invoker, nil
}

func (e *sessionV3Executor) emitSessionV3ProviderToolEvent(job sessionV3ExecutorJob) runruntime.StreamHandler {
	var mu sync.Mutex
	deltaIndex := 0
	return func(event runruntime.StreamEvent) {
		eventType := ""
		eventDeltaIndex := 0
		switch strings.TrimSpace(event.Type) {
		case runruntime.StreamEventToolStarted:
			eventType = "session.tool.started"
		case runruntime.StreamEventToolDelta:
			eventType = "session.tool.delta"
			mu.Lock()
			deltaIndex++
			eventDeltaIndex = deltaIndex
			mu.Unlock()
		default:
			return
		}
		if err := e.recordProviderToolEvent(job, event, eventType, eventDeltaIndex); err != nil {
			log.Printf("warning: failed to record v3 provider tool event session=%q run=%q type=%q call=%q: %v", job.SessionID, job.RunID, eventType, event.CallID, err)
			return
		}
	}
}

func (e *sessionV3Executor) recordProviderToolEvent(job sessionV3ExecutorJob, event runruntime.StreamEvent, eventType string, deltaIndex int) error {
	if e.isRunCanceled(job) {
		return context.Canceled
	}
	if e == nil || e.server == nil {
		return errors.New("v3 executor is not configured")
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return errors.New("v3 provider tool event type is required")
	}
	step := event.Step
	if step <= 0 {
		step = 1
	}
	toolName := strings.TrimSpace(event.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	callID := strings.TrimSpace(event.CallID)
	if callID == "" {
		callID = "tool_call"
	}
	stepID := sessionV3ProviderToolStepID(step)
	toolInstanceID := sessionV3ProviderToolInstanceID(step, callID)
	payload := map[string]any{
		"run_id":           strings.TrimSpace(job.RunID),
		"step":             step,
		"step_id":          stepID,
		"tool_name":        toolName,
		"call_id":          callID,
		"tool_instance_id": toolInstanceID,
	}
	if args := strings.TrimSpace(event.Arguments); args != "" {
		payload["arguments"] = args
	}
	if output := event.Output; output != "" {
		payload["output"] = output
	}
	if rawOutput := event.RawOutput; rawOutput != "" {
		payload["raw_output"] = rawOutput
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		payload["error"] = errText
	}
	if event.DurationMS != 0 {
		payload["duration_ms"] = event.DurationMS
	}
	if deltaIndex > 0 {
		payload["delta_index"] = deltaIndex
	}
	if len(event.Metadata) > 0 {
		payload["metadata"] = cloneSessionsV3Metadata(event.Metadata)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	contentForHash := string(raw)
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, "", eventType, contentForHash)
	if err != nil {
		return err
	}
	clientRequestID := sessionV3ProviderToolEventClientRequestID(eventType, job.RunID, step, callID, deltaIndex)
	now := time.Now().UnixMilli()
	intent := pebblestore.V3SessionRunIntent{RunID: job.RunID, Status: sessionruntime.RunIntentRunning, UpdatedAt: now}
	_, err = e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       eventType,
		EventPayload:    raw,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
	return err
}

func (e *sessionV3Executor) sessionV3CompiledToolPolicy(resolved sessionV3ResolvedRuntime) (*permission.Policy, error) {
	if e == nil || e.server == nil || e.server.runner == nil {
		return nil, nil
	}
	_, policy, _, err := e.server.runner.ResolveAgentToolContractForAccount(resolved.Session.AccountScopeID, resolved.AgentProfile)
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func (e *sessionV3Executor) sessionV3ProviderContinuationInput(job sessionV3ExecutorJob) []map[string]any {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil
	}
	messages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, 500)
	if err != nil || len(messages) == 0 {
		return nil
	}
	return sessionsV3ProviderInput(messages)
}

func (e *sessionV3Executor) resolveSessionV3Runtime(job sessionV3ExecutorJob) (sessionV3ResolvedRuntime, error) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return sessionV3ResolvedRuntime{}, errors.New("v3 executor is not configured")
	}
	session, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if !ok {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("session %q not found", job.SessionID)
	}
	if strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(job.Principal.AccountScopeID) {
		return sessionV3ResolvedRuntime{}, errors.New("session principal account mismatch")
	}
	agentName := strings.TrimSpace(firstNonEmpty(sessionV3MetadataString(session.Metadata, "resolved_agent_name"), sessionV3MetadataString(session.Metadata, "agent_name")))
	if agentName == "" {
		return sessionV3ResolvedRuntime{}, errors.New("v3 session is missing durable agent identity")
	}
	if e.server.agents == nil {
		return sessionV3ResolvedRuntime{}, errors.New("agent service is not configured")
	}
	agentProfile, err := e.server.agents.ResolvePrimaryForAccount(session.AccountScopeID, agentName)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if !agentProfile.Enabled {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("agent %q is disabled", strings.TrimSpace(agentProfile.Name))
	}
	if strings.TrimSpace(agentProfile.Mode) != "primary" {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("agent %q is not primary", strings.TrimSpace(agentProfile.Name))
	}
	pref, contextWindow, err := e.resolveSessionV3ProviderPreference(applySessionV3AgentPreferenceOverrides(session.Preference, agentProfile))
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if strings.TrimSpace(pref.Provider) == "" || strings.TrimSpace(pref.Model) == "" {
		return sessionV3ResolvedRuntime{}, errors.New("resolved v3 provider/model is empty")
	}
	scope, err := e.resolveSessionV3WorkspaceScope(session, job.Principal)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		return sessionV3ResolvedRuntime{}, errors.New("session workspace path is empty")
	}
	instructions := strings.TrimSpace(e.composeSessionV3Instructions(scope, session.Mode, agentProfile))
	if instructions == "" {
		return sessionV3ResolvedRuntime{}, errors.New("resolved v3 instructions are empty")
	}
	tools, err := e.resolveSessionV3ProviderTools(session.AccountScopeID, agentProfile)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	toolChoice := "none"
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	return sessionV3ResolvedRuntime{Session: session, AgentProfile: agentProfile, Preference: pref, ContextWindow: contextWindow, Scope: scope, Instructions: instructions, Tools: tools, ToolChoice: toolChoice}, nil
}

func (e *sessionV3Executor) resolveSessionV3WorkspaceScope(session pebblestore.SessionSnapshot, principal identity.Principal) (tool.WorkspaceScope, error) {
	if hydrator, ok := e.server.runner.(interface {
		ResolveRuntimeWorkspaceScope(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error)
	}); ok && hydrator != nil {
		return hydrator.ResolveRuntimeWorkspaceScope(session, principal)
	}
	hydrator := runruntime.NewService(e.server.sessions, e.server.model, e.server.providers, nil, nil, e.server.agents, e.server.discovery, e.server.events)
	if e.server.workspace != nil {
		hydrator.SetWorkspaceService(e.server.workspace)
	}
	return hydrator.ResolveRuntimeWorkspaceScope(session, principal)
}

func (e *sessionV3Executor) composeSessionV3Instructions(scope tool.WorkspaceScope, mode string, agentProfile pebblestore.AgentProfile) string {
	if hydrator, ok := e.server.runner.(interface {
		ComposeRuntimeInstructions(tool.WorkspaceScope, string, bool, pebblestore.AgentProfile, string) string
	}); ok && hydrator != nil {
		return hydrator.ComposeRuntimeInstructions(scope, mode, e.server.BypassPermissions(), agentProfile, "")
	}
	hydrator := runruntime.NewService(e.server.sessions, e.server.model, e.server.providers, nil, nil, e.server.agents, e.server.discovery, e.server.events)
	return hydrator.ComposeRuntimeInstructions(scope, mode, e.server.BypassPermissions(), agentProfile, "")
}

func (e *sessionV3Executor) composeSessionV3InstructionsLegacy(scope tool.WorkspaceScope, mode string, agentProfile pebblestore.AgentProfile) string {
	agentName := strings.TrimSpace(agentProfile.Name)
	if agentName == "" {
		agentName = "swarm"
	}
	agentMode := strings.TrimSpace(agentProfile.Mode)
	if agentMode == "" {
		agentMode = "primary"
	}
	runtimeMode := pebblestore.AgentProfileRuntimeMode(agentProfile)
	if runtimeMode == "" {
		runtimeMode = "unset"
	}
	prompt := strings.TrimSpace(agentProfile.Prompt)
	if prompt == "" {
		return ""
	}
	lines := []string{
		"Active agent profile:",
		"- name: " + agentName,
		"- mode: " + agentMode,
		"- runtime_contract: " + runtimeMode,
		fmt.Sprintf("- exit_plan_mode_enabled: %t", pebblestore.AgentExitPlanModeEnabled(agentProfile)),
		"",
		prompt,
		"",
		"Current session mode: " + sessionruntime.NormalizeMode(mode) + ".",
		"Use the committed V3 session history and workspace scope to answer the user's latest message.",
		"Workspace scope primary path: " + strings.TrimSpace(scope.PrimaryPath),
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (e *sessionV3Executor) resolveSessionV3ProviderTools(accountScopeID string, agentProfile pebblestore.AgentProfile) ([]provideriface.ToolDefinition, error) {
	if e == nil || e.server == nil || e.server.runner == nil {
		return nil, nil
	}
	contract, _, disabled, err := e.server.runner.ResolveAgentToolContractForAccount(accountScopeID, agentProfile)
	if err != nil {
		return nil, err
	}
	definitions := sessionsV3ProviderToolDefinitions(e.server.runner.ListAgentToolDefinitionsForAccount(accountScopeID))
	if len(definitions) == 0 {
		return nil, nil
	}
	allowed := make(map[string]bool, len(contract.Tools))
	for name, state := range contract.Tools {
		name = strings.TrimSpace(name)
		if name != "" && state.Enabled {
			allowed[name] = true
		}
	}
	filtered := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" || disabled[name] || !allowed[name] {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered, nil
}

func sessionsV3ProviderToolDefinitions(definitions []tool.Definition) []provideriface.ToolDefinition {
	out := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, provideriface.ToolDefinition{
			Type:        definition.Type,
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  sessionsV3ProviderToolParameters(definition.Parameters),
		})
	}
	return out
}

func sessionsV3ProviderToolParameters(parameters map[string]any) map[string]any {
	if len(parameters) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := make(map[string]any, len(parameters))
	for key, value := range parameters {
		if value != nil {
			out[key] = value
		}
	}
	if strings.TrimSpace(fmt.Sprint(out["type"])) == "" {
		out["type"] = "object"
	}
	if strings.EqualFold(strings.TrimSpace(fmt.Sprint(out["type"])), "object") {
		if _, ok := out["properties"].(map[string]any); !ok {
			out["properties"] = map[string]any{}
		}
	}
	return out
}

func (e *sessionV3Executor) resolveSessionV3ProviderPreference(pref pebblestore.ModelPreference) (pebblestore.ModelPreference, int, error) {
	pref = normalizeSessionsV3ModelPreference(pref)
	if e == nil || e.server == nil || e.server.model == nil {
		if pref.Provider == "codex" {
			pref.ServiceTier = codexruntime.NormalizeServiceTier(pref.ServiceTier)
			pref.ContextMode = codexruntime.NormalizeContextMode(pref.ContextMode)
		} else {
			pref.ServiceTier = ""
			pref.ContextMode = ""
		}
		return pref, 0, nil
	}
	resolved, err := e.server.model.ResolvePreference(pref)
	if err != nil {
		return pebblestore.ModelPreference{}, 0, fmt.Errorf("resolve v3 provider preference: %w", err)
	}
	resolvedPref := normalizeSessionsV3ModelPreference(resolved.Preference)
	if resolvedPref.Provider == "" && pref.Provider != "" {
		resolvedPref.Provider = pref.Provider
	}
	if resolvedPref.Model == "" && pref.Model != "" {
		resolvedPref.Model = pref.Model
	}
	return resolvedPref, resolved.ContextWindow, nil
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
		case "reasoning":
			continue
		case "tool":
			if toolItems, ok := sessionsV3ProviderToolMessageInput(content, message.Metadata); ok {
				input = append(input, toolItems...)
			}
		default:
			input = append(input, map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": content}}})
		}
	}
	return input
}

func sessionsV3ProviderToolMessageInput(content string, metadata map[string]any) ([]map[string]any, bool) {
	record, ok := sessionsV3DecodeProviderToolResultRecord(content)
	if !ok {
		return nil, false
	}
	if len(record.Metadata) == 0 {
		record.Metadata = cloneSessionsV3Metadata(metadata)
	}
	return sessionsV3ProviderToolRecordInputItems(record), true
}

func sessionsV3ProviderToolRecordInputItems(record sessionV3ProviderToolResultRecord) []map[string]any {
	callID := strings.TrimSpace(record.CallID)
	if callID == "" {
		return nil
	}
	name := strings.TrimSpace(record.ToolName)
	if name == "" {
		name = "tool"
	}
	arguments := strings.TrimSpace(record.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	callInput := map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
	if metadata := cloneSessionsV3Metadata(record.Metadata); len(metadata) > 0 {
		callInput["metadata"] = metadata
	}
	output := strings.TrimSpace(firstNonEmpty(record.Output, record.CompletedOutput, record.Error))
	return []map[string]any{
		callInput,
		{"type": "function_call_output", "call_id": callID, "output": output},
	}
}

func sessionsV3ProviderToolResultInputItems(calls []provideriface.FunctionCall, results []provideriface.ToolExecutionResult) []map[string]any {
	count := len(calls)
	if len(results) < count {
		count = len(results)
	}
	out := make([]map[string]any, 0, count*2)
	for i := 0; i < count; i++ {
		call := calls[i]
		result := results[i]
		callID := strings.TrimSpace(firstNonEmpty(result.CallID, call.CallID))
		if callID == "" {
			continue
		}
		name := strings.TrimSpace(firstNonEmpty(call.Name, result.Name))
		if name == "" {
			name = "tool"
		}
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		metadata := cloneSessionsV3Metadata(call.Metadata)
		callInput := map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": arguments}
		if len(metadata) > 0 {
			callInput["metadata"] = metadata
		}
		output := strings.TrimSpace(result.TextForModel)
		if output == "" {
			output = strings.TrimSpace(firstNonEmpty(result.Output, result.Error))
		}
		out = append(out, callInput, map[string]any{"type": "function_call_output", "call_id": callID, "output": output})
	}
	return out
}

type sessionV3ProviderToolResultRecord struct {
	PathID          string         `json:"path_id"`
	Type            string         `json:"type"`
	RunID           string         `json:"run_id,omitempty"`
	Step            int            `json:"step,omitempty"`
	StepID          string         `json:"step_id,omitempty"`
	ToolName        string         `json:"tool_name"`
	CallID          string         `json:"call_id"`
	ToolInstanceID  string         `json:"tool_instance_id,omitempty"`
	Arguments       string         `json:"arguments,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Output          string         `json:"output,omitempty"`
	CompletedOutput string         `json:"completed_output,omitempty"`
	Error           string         `json:"error,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
}

func sessionsV3DecodeProviderToolResultRecord(raw string) (sessionV3ProviderToolResultRecord, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sessionV3ProviderToolResultRecord{}, false
	}
	var record sessionV3ProviderToolResultRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return sessionV3ProviderToolResultRecord{}, false
	}
	record.PathID = strings.TrimSpace(record.PathID)
	record.Type = strings.TrimSpace(record.Type)
	record.RunID = strings.TrimSpace(record.RunID)
	record.StepID = strings.TrimSpace(record.StepID)
	record.ToolName = strings.TrimSpace(record.ToolName)
	record.CallID = strings.TrimSpace(record.CallID)
	record.ToolInstanceID = strings.TrimSpace(record.ToolInstanceID)
	record.Arguments = strings.TrimSpace(record.Arguments)
	record.Output = strings.TrimSpace(record.Output)
	record.CompletedOutput = strings.TrimSpace(record.CompletedOutput)
	record.Error = strings.TrimSpace(record.Error)
	record.Metadata = cloneSessionsV3Metadata(record.Metadata)
	if !strings.EqualFold(record.PathID, "run.v3.provider-tool-result.v1") || record.ToolName == "" || record.CallID == "" {
		return sessionV3ProviderToolResultRecord{}, false
	}
	if record.Arguments == "" {
		record.Arguments = "{}"
	}
	if record.CompletedOutput == "" {
		record.CompletedOutput = record.Output
	}
	return record, true
}

func shouldGenerateSessionV3Title(session pebblestore.SessionSnapshot) bool {
	if session.MessageCount > 2 {
		return false
	}
	if sessionV3TitleGenerationLocked(session.Metadata) {
		return false
	}
	title := strings.TrimSpace(session.Title)
	return title == "" || strings.EqualFold(title, sessionV3TitleDefault)
}

func shouldGenerateSessionV3TitleWithMessages(session pebblestore.SessionSnapshot, messages []pebblestore.MessageSnapshot) bool {
	if !shouldGenerateSessionV3Title(session) {
		return false
	}
	var userCount, assistantCount int
	for _, message := range messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "system":
			continue
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		default:
			return false
		}
	}
	return userCount == 1 && assistantCount >= 1
}

func sessionV3TitleGenerationLocked(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	if sessionV3MetadataBool(metadata, "title_locked") || sessionV3MetadataBool(metadata, "background") {
		return true
	}
	for _, pair := range []struct{ key, value string }{
		{"title_source", "flow_task"},
		{"lineage_kind", "delegated_subagent"},
		{"lineage_kind", "flow"},
		{"launch_source", "task"},
		{"launch_source", "targeted_subagent"},
		{"launch_mode", "background"},
		{"source", "flow"},
		{"owner_transport", "flow_scheduler"},
		{"subagent", "commit"},
		{"requested_subagent", "commit"},
	} {
		if strings.EqualFold(sessionV3MetadataString(metadata, pair.key), pair.value) {
			return true
		}
	}
	return sessionV3MetadataString(metadata, "flow_id") != ""
}

func sessionV3MetadataBool(metadata map[string]any, key string) bool {
	switch typed := metadata[key].(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func sessionV3MetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func buildSessionV3TitleConversation(messages []pebblestore.MessageSnapshot) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		lines = append(lines, role+": "+truncateSessionV3TitleRunes(content, 240))
	}
	return strings.Join(lines, "\n")
}

func applySessionV3AgentPreferenceOverrides(base pebblestore.ModelPreference, agentProfile pebblestore.AgentProfile) pebblestore.ModelPreference {
	providerOverride := strings.ToLower(strings.TrimSpace(agentProfile.Provider))
	modelOverride := strings.TrimSpace(agentProfile.Model)
	thinkingOverride := strings.TrimSpace(agentProfile.Thinking)
	if providerOverride != "" && modelOverride != "" {
		base.Provider = providerOverride
		base.Model = modelOverride
	} else if providerOverride == "" && modelOverride != "" {
		base.Model = modelOverride
	}
	if thinkingOverride != "" {
		base.Thinking = thinkingOverride
	}
	base.Thinking = normalizeSessionV3ThinkingWithProvider(base.Provider, base.Thinking)
	if !strings.EqualFold(strings.TrimSpace(base.Provider), "codex") || !strings.EqualFold(strings.TrimSpace(base.Model), "gpt-5.4") {
		base.ServiceTier = ""
		base.ContextMode = ""
	}
	return base
}

func normalizeSessionV3ThinkingWithProvider(providerID, thinking string) string {
	normalized := strings.ToLower(strings.TrimSpace(thinking))
	switch normalized {
	case "off", "low", "medium", "high", "xhigh":
		if (strings.EqualFold(providerID, "copilot") || strings.EqualFold(providerID, "fireworks") || strings.EqualFold(providerID, "openrouter")) && normalized == "xhigh" {
			return "high"
		}
		return normalized
	}
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "google":
		return "xhigh"
	case "copilot", "fireworks", "openrouter":
		return "high"
	default:
		return pebblestore.DefaultThinkingLevel
	}
}

func sanitizeSessionV3GeneratedTitle(raw string, minWords, maxWords int) string {
	words := sessionV3TitleWordPattern.FindAllString(strings.TrimSpace(raw), -1)
	if len(words) == 0 {
		return ""
	}
	if maxWords > 0 && len(words) > maxWords {
		words = words[:maxWords]
	}
	if len(words) < minWords {
		return ""
	}
	return strings.Join(words, " ")
}

func truncateSessionV3TitleRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func sessionV3TitlePayloadHash(sessionID, runID, title string) (string, error) {
	canonical := struct {
		Operation string `json:"operation"`
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Title     string `json:"title"`
	}{
		Operation: sessionruntime.SessionMutationUpdateTitle,
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Title:     strings.TrimSpace(title),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal v3 title payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sessionV3ExecutorRunKey(sessionID, runID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID)
}

func sessionV3ExecutorClientRequestID(eventType, runID string) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	return "v3-executor-" + label + "-" + strings.TrimSpace(runID)
}

func sessionV3ProviderToolStepID(step int) string {
	if step <= 0 {
		step = 1
	}
	return fmt.Sprintf("step-%d", step)
}

func sessionV3ProviderToolInstanceID(step int, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool_call"
	}
	return sessionV3ProviderToolStepID(step) + ":" + callID
}

func sessionV3ProviderToolEventClientRequestID(eventType, runID string, step int, callID string, deltaIndex int) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	callID = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(sessionV3ProviderToolInstanceID(step, callID))
	if deltaIndex > 0 {
		return fmt.Sprintf("v3-executor-%s-%s-%04d-%s-%04d", label, strings.TrimSpace(runID), step, callID, deltaIndex)
	}
	return fmt.Sprintf("v3-executor-%s-%s-%04d-%s", label, strings.TrimSpace(runID), step, callID)
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
