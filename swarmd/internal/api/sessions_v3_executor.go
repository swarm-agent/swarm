package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"regexp"
	"strconv"
	"strings"
	agentruntime "swarm/packages/swarmd/internal/agent"
	modelruntime "swarm/packages/swarmd/internal/model"

	"swarm/packages/swarmd/internal/privacy"
	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"

	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	sessionV3ExecutorDefaultStartDelay        = 10 * time.Millisecond
	sessionV3ExecutorRecoveryLimit            = 500
	sessionV3ExecutorDefaultRunningStaleAfter = 5 * time.Minute
	sessionV3ProviderIdenticalToolCallLimit   = 5
	sessionV3AssistantDeltaFlushMaxBytes      = 2048
	sessionV3AssistantDeltaFlushMaxDelay      = 250 * time.Millisecond
	sessionV3ReasoningDeltaFlushMaxBytes      = 4096
	sessionV3ReasoningDeltaFlushMaxDelay      = 500 * time.Millisecond
	sessionV3ReasoningEventType               = "v3_provider_reasoning"
	sessionV3RunStopDefaultReason             = "run stopped by user"
	sessionV3TitleDefault                     = "New Session"
	sessionV3TitleConversationLimit           = 24
	sessionV3TitlePromptPreviewRunes          = 2000
	sessionV3TitleGenerationTimeout           = 20 * time.Second
	sessionV3TitleFinalWordsMin               = 0
	sessionV3TitleFinalWordsMax               = 5
	sessionV3HandoffDefaultTailMessages       = 24
	sessionV3HandoffDefaultToolOutputChars    = 1200
	sessionV3HandoffDefaultTotalChars         = 60000
)

var sessionV3TitleWordPattern = regexp.MustCompile(`\b[\p{L}\p{N}][\p{L}\p{N}'-]*\b`)

type sessionV3ExecutorJob struct {
	Principal       identity.Principal
	SessionID       string
	RunID           string
	EpochID         string
	PlanID          string
	CheckpointID    string
	AttemptID       string
	RunSessionID    string
	ParentSessionID string
	enqueuedAt      time.Time
}

type sessionV3ExecutorRunState struct {
	cancel   context.CancelFunc
	canceled bool
	reason   string
}

func sessionV3RunIntentForJob(job sessionV3ExecutorJob, status string, now int64) pebblestore.V3SessionRunIntent {
	return pebblestore.V3SessionRunIntent{
		SessionID:       strings.TrimSpace(job.SessionID),
		UserID:          strings.TrimSpace(job.Principal.UserID),
		AccountScopeID:  strings.TrimSpace(job.Principal.AccountScopeID),
		RunID:           strings.TrimSpace(job.RunID),
		Status:          strings.TrimSpace(status),
		EpochID:         strings.TrimSpace(job.EpochID),
		PlanID:          strings.TrimSpace(job.PlanID),
		CheckpointID:    strings.TrimSpace(job.CheckpointID),
		AttemptID:       strings.TrimSpace(job.AttemptID),
		RunSessionID:    strings.TrimSpace(firstNonEmptyString(job.RunSessionID, job.SessionID)),
		ParentSessionID: strings.TrimSpace(job.ParentSessionID),
		UpdatedAt:       now,
	}
}

type sessionV3Executor struct {
	server *Server
	ctx    context.Context

	startDelay                   time.Duration
	modelDelay                   time.Duration
	runningStaleAfter            time.Duration
	deltaFlushMaxBytes           int
	deltaFlushMaxDelay           time.Duration
	reasoningDeltaFlushMaxBytes  int
	reasoningDeltaFlushMaxDelay  time.Duration
	durableProgressWriterForTest sessionV3DurableProgressWriter

	mu              sync.Mutex
	inFlightRuns    map[string]bool
	activeBySession map[string]string
	runStates       map[string]*sessionV3ExecutorRunState
}

func newSessionV3Executor(server *Server) *sessionV3Executor {
	ctx := context.Background()
	if server != nil && server.runCtx != nil {
		ctx = server.runCtx
	}
	exec := &sessionV3Executor{
		server:                      server,
		ctx:                         ctx,
		startDelay:                  sessionV3ExecutorDefaultStartDelay,
		runningStaleAfter:           sessionV3ExecutorDefaultRunningStaleAfter,
		deltaFlushMaxBytes:          sessionV3AssistantDeltaFlushMaxBytes,
		deltaFlushMaxDelay:          sessionV3AssistantDeltaFlushMaxDelay,
		reasoningDeltaFlushMaxBytes: sessionV3ReasoningDeltaFlushMaxBytes,
		reasoningDeltaFlushMaxDelay: sessionV3ReasoningDeltaFlushMaxDelay,
		inFlightRuns:                make(map[string]bool),
		activeBySession:             make(map[string]string),
		runStates:                   make(map[string]*sessionV3ExecutorRunState),
	}
	exec.recoverDurableRuns(ctx)
	return exec
}

func (e *sessionV3Executor) EnqueueRun(job sessionV3ExecutorJob) bool {
	if e == nil || e.server == nil {
		return false
	}
	job.SessionID = strings.TrimSpace(job.SessionID)
	job.RunID = strings.TrimSpace(job.RunID)
	job.EpochID = strings.TrimSpace(job.EpochID)
	job.PlanID = strings.TrimSpace(job.PlanID)
	job.CheckpointID = strings.TrimSpace(job.CheckpointID)
	job.AttemptID = strings.TrimSpace(job.AttemptID)
	job.RunSessionID = strings.TrimSpace(job.RunSessionID)
	job.ParentSessionID = strings.TrimSpace(job.ParentSessionID)
	if job.enqueuedAt.IsZero() {
		job.enqueuedAt = time.Now()
	}
	if job.SessionID == "" || job.RunID == "" {
		return false
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
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

	go e.run(ctx, job)
	return true
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
		job = hydrateSessionV3ExecutorJobFromIntent(job, intent)
		switch intent.Status {
		case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
			result, err := e.recordCancelledRunAndReconcilePlan(job, reason)
			return result, true, err
		case sessionruntime.RunIntentCancelled:
			if strings.TrimSpace(intent.BlockedReason) == reason {
				result := sessionruntime.SessionMutationResult{SessionID: job.SessionID, RunIntent: &intent}
				if err := e.reconcileCancelledPlanRun(job, reason, intent.UpdatedAt); err != nil {
					return result, true, err
				}
				return result, true, nil
			}
		}
	}
	if tracked {
		result, err := e.recordCancelledRunAndReconcilePlan(job, reason)
		return result, true, err
	}
	return sessionruntime.SessionMutationResult{}, false, fmt.Errorf("v3 run %q is not active", job.RunID)
}

func hydrateSessionV3ExecutorJobFromIntent(job sessionV3ExecutorJob, intent pebblestore.V3SessionRunIntent) sessionV3ExecutorJob {
	if job.PlanID == "" {
		job.PlanID = strings.TrimSpace(intent.PlanID)
	}
	if job.CheckpointID == "" {
		job.CheckpointID = strings.TrimSpace(intent.CheckpointID)
	}
	if job.AttemptID == "" {
		job.AttemptID = strings.TrimSpace(intent.AttemptID)
	}
	if job.RunSessionID == "" {
		job.RunSessionID = strings.TrimSpace(intent.RunSessionID)
	}
	if job.ParentSessionID == "" {
		job.ParentSessionID = strings.TrimSpace(intent.ParentSessionID)
	}
	return job
}

func (e *sessionV3Executor) recordCancelledRunAndReconcilePlan(job sessionV3ExecutorJob, reason string) (sessionruntime.SessionMutationResult, error) {
	result, err := e.recordRunStatus(job, sessionruntime.RunIntentCancelled, reason, "session.run.cancelled")
	if err != nil {
		return result, err
	}
	cancelledAt := time.Now().UnixMilli()
	if result.RunIntent != nil && result.RunIntent.UpdatedAt > 0 {
		cancelledAt = result.RunIntent.UpdatedAt
	}
	if err := e.reconcileCancelledPlanRun(job, reason, cancelledAt); err != nil {
		return result, err
	}
	return result, nil
}

func (e *sessionV3Executor) reconcileCancelledPlanRun(job sessionV3ExecutorJob, reason string, cancelledAt int64) error {
	if strings.TrimSpace(job.PlanID) == "" || strings.TrimSpace(job.CheckpointID) == "" || strings.TrimSpace(job.AttemptID) == "" {
		return nil
	}
	if e == nil || e.server == nil || e.server.planLifecycle == nil {
		return errors.New("v3 plan cancellation reconciliation is not configured")
	}
	result, changed, err := e.server.planLifecycle.ReconcileCancelledRun(sessionruntime.PlanLifecycleExecutionInput{
		SessionID:       job.SessionID,
		PlanID:          job.PlanID,
		CheckpointID:    job.CheckpointID,
		AttemptID:       job.AttemptID,
		RunID:           job.RunID,
		RunSessionID:    firstNonEmptyString(job.RunSessionID, job.SessionID),
		ParentSessionID: job.ParentSessionID,
		Notes:           strings.TrimSpace(reason),
		ReviewedAt:      cancelledAt,
	})
	if err != nil || !changed {
		return err
	}
	return e.server.publishPlanLifecycleResult(result)
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
			SessionID:       intent.SessionID,
			RunID:           intent.RunID,
			EpochID:         intent.EpochID,
			PlanID:          intent.PlanID,
			CheckpointID:    intent.CheckpointID,
			AttemptID:       intent.AttemptID,
			RunSessionID:    intent.RunSessionID,
			ParentSessionID: intent.ParentSessionID,
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
	_, err := e.recordRunStatus(job, sessionruntime.RunIntentInterrupted, "executor interrupted during daemon restart", "session.run.interrupted")
	return err
}

func (e *sessionV3Executor) run(ctx context.Context, job sessionV3ExecutorJob) {
	pebblestore.ObserveExecutionEpochQueueWait(job.enqueuedAt)
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
			if sessionV3IsContextOverflowDiagnostic(err.Error()) {
				e.recordSessionV3ContextOverflowDecision(job, "assistant_response_error", err)
				response, job, err = e.contextOverflowCompactedAssistantResponse(runCtx, job, err)
			}
			if err != nil {
				classification := sessionV3TerminalClassifier.Classify(TerminalClassifierInput{Err: err})
				if classification.Status == sessionruntime.RunIntentFailed && classification.EventType == "session.run.failed" {
					_, _ = e.recordRunFailureSystemMessage(job, classification.Reason)
				}
				_, _ = e.recordRunStatus(job, classification.Status, classification.Reason, classification.EventType)
			}
		}
		if err != nil {
			return
		}
	}
	if e.isRunCanceled(job) || runCtx.Err() != nil {
		return
	}
	if response.LifecycleOnly {
		if _, err := e.recordRunStatus(job, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); err != nil {
			return
		}
		if response.StartNextCheckpoint {
			if err := e.startNextCheckpointRun(job); err != nil {
				log.Printf("warning: v3 session executor could not start next checkpoint after run %q for session %q: %v", job.RunID, job.SessionID, err)
				_, _ = e.recordRunFailureSystemMessage(job, "automatic checkpoint advance failed: "+err.Error())
			}
		}
		return
	}
	// Commit the fixed-size lineage authority before exposing the assistant
	// message. This closes the visibility race where a following turn could see
	// the message before its epoch lifecycle state.
	if err := e.persistSessionV3ProviderLifecycle(job, response, sessionruntime.SessionMutationResult{}); err != nil {
		_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		return
	}
	result, err := e.completeRun(job, response)
	if err != nil {
		if !e.isRunCanceled(job) {
			_, _ = e.recordRunFailureSystemMessage(job, err.Error())
			_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		}
		return
	}
	if err := e.persistSessionV3ProviderLifecycle(job, response, result); err != nil {
		_, _ = e.recordRunStatus(job, sessionruntime.RunIntentFailed, err.Error(), "session.run.failed")
		return
	}
}

func (e *sessionV3Executor) startNextCheckpointRun(job sessionV3ExecutorJob) error {
	if e == nil || e.server == nil || e.server.sessions == nil || e.server.planLifecycle == nil {
		return errors.New("v3 checkpoint auto-advance is not configured")
	}
	active, ok, err := e.server.sessions.GetActivePlan(job.SessionID)
	if err != nil {
		return err
	}
	if !ok || active.Document == nil {
		return errors.New("v3 checkpoint auto-advance has no active plan")
	}
	summary := sessionruntime.SummarizePlanExecution(active.Document)
	checkpointID := strings.TrimSpace(summary.NextCheckpointID)
	if checkpointID == "" || !summary.AutoAdvanceAllowed || summary.ReviewRequired || summary.Blocked || summary.Failed || summary.PlanComplete {
		return nil
	}
	input, err := e.server.sessionsV3PlanModeRunInput(job.SessionID, active.ID, checkpointID)
	if err != nil {
		return err
	}
	result, err := e.server.planLifecycle.StartCheckpoint(input)
	if err != nil {
		return err
	}
	runStart, _, err := e.server.startSessionsV3PlanModeRun(job.Principal, job.SessionID, "automatic_checkpoint_advance", result, false)
	if err != nil {
		return err
	}
	if runStart == nil || runStart.RunIntent == nil {
		return errors.New("automatic checkpoint advance did not create a run intent")
	}
	e.finish(job)
	if !e.EnqueueRun(sessionV3ExecutorJob{Principal: job.Principal, SessionID: job.SessionID, RunID: runStart.RunIntent.RunID, EpochID: runStart.RunIntent.EpochID, PlanID: active.ID, CheckpointID: checkpointID, AttemptID: runStart.AttemptID, ParentSessionID: job.SessionID}) {
		return errors.New("automatic checkpoint advance could not enqueue the next run")
	}
	return nil
}

func (e *sessionV3Executor) recordRunFailureSystemMessage(job sessionV3ExecutorJob, reason string) (sessionruntime.SessionMutationResult, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("v3 executor is not configured")
	}
	content := sessionV3RunFailureSystemMessage(reason)
	if strings.TrimSpace(content) == "" {
		return sessionruntime.SessionMutationResult{}, nil
	}
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{
		ID:        sessionV3RunFailureMessageID(job.SessionID, job.RunID),
		Role:      "system",
		Content:   content,
		CreatedAt: now,
		Metadata: map[string]any{
			"source":       "backend.executor",
			"message_kind": "run_failure",
			"run_id":       strings.TrimSpace(job.RunID),
			"synthetic":    true,
			"visible":      true,
		},
	}
	sanitizedReason := strings.TrimSpace(privacy.SanitizeText(reason))
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentFailed, sanitizedReason, "session.message.appended", content)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := sessionV3ExecutorClientRequestID("session.run.failure_message", job.RunID)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		NowUnixMs:       now,
	})
}

func sessionV3RunFailureSystemMessage(reason string) string {
	reason = strings.TrimSpace(privacy.SanitizeText(reason))
	if reason == "" {
		return "[run-failed] The assistant run failed before it could return a response."
	}
	return "[run-failed] The assistant run failed before it could return a response.\n\n" + reason
}

func (e *sessionV3Executor) recordRunStatus(job sessionV3ExecutorJob, status, reason, eventType string) (sessionruntime.SessionMutationResult, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("v3 executor is not configured")
	}
	now := time.Now().UnixMilli()
	sanitizedReason := strings.TrimSpace(privacy.SanitizeText(reason))
	intent := sessionV3RunIntentForJob(job, status, now)
	intent.BlockedReason = sanitizedReason
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, status, sanitizedReason, eventType, "")
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

func (e *sessionV3Executor) recordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("v3 executor is not configured")
	}
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	phaseText := strings.TrimSpace(string(phase))
	if phaseText == "" {
		return sessionruntime.SessionMutationResult{}, errors.New("run phase is required")
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return sessionruntime.SessionMutationResult{}, errors.New("run phase event type is required")
	}
	now := time.Now().UnixMilli()
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, now)
	payload := struct {
		SessionID string                         `json:"session_id"`
		RunID     string                         `json:"run_id"`
		Status    string                         `json:"status"`
		Phase     string                         `json:"phase"`
		RunIntent pebblestore.V3SessionRunIntent `json:"run_intent"`
	}{SessionID: job.SessionID, RunID: job.RunID, Status: sessionruntime.RunIntentRunning, Phase: phaseText, RunIntent: intent}
	raw, err := json.Marshal(payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, phaseText, eventType, string(raw))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := sessionV3ExecutorClientRequestID(eventType, job.RunID)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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
}

func (e *sessionV3Executor) recordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	now := time.Now().UnixMilli()
	if progress.RecordedAt == 0 {
		progress.RecordedAt = now
	}
	metadata := map[string]any{
		"run_id":         job.RunID,
		"epoch_id":       job.EpochID,
		"stream_id":      progress.StreamID,
		"operation":      "append",
		"step":           progress.Step,
		"step_id":        progress.StepID,
		"delta_index":    deltaIndex,
		"offset_start":   progress.OffsetStart,
		"offset_end":     progress.OffsetEnd,
		"delta":          progress.Text,
		"recorded_at":    progress.RecordedAt,
		"live_seq_start": progress.LiveSeqStart,
		"live_seq_end":   progress.LiveSeqEnd,
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, now)
	hashMaterial := fmt.Sprintf("%d:%s:%d:%d:%s", deltaIndex, progress.StreamID, progress.OffsetStart, progress.OffsetEnd, progress.Text)
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, "", "session.assistant.delta", hashMaterial)
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

func sessionV3ReasoningEventID(step int, reasoningKey string) string {
	if step <= 0 {
		step = 1
	}
	reasoningKey = sessionV3NormalizeReasoningKey(reasoningKey)
	sum := sha256.Sum256([]byte(strconv.Itoa(step) + "\x00" + reasoningKey + "\x00reasoning"))
	return "reasoning_" + hex.EncodeToString(sum[:8])
}

func (e *sessionV3Executor) recordReasoningEvent(job sessionV3ExecutorJob, eventType string, step, eventIndex int, reasoningKey, delta, deltaMode, summary string) (sessionruntime.SessionMutationResult, error) {
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return sessionruntime.SessionMutationResult{}, errors.New("v3 reasoning event type is required")
	}
	if step <= 0 {
		step = 1
	}
	reasoningKey = sessionV3NormalizeReasoningKey(reasoningKey)
	now := time.Now().UnixMilli()
	payload := map[string]any{
		"path_id":       "run.v3.provider-reasoning.v1",
		"type":          sessionV3ReasoningEventType,
		"run_id":        strings.TrimSpace(job.RunID),
		"epoch_id":      strings.TrimSpace(job.EpochID),
		"step":          step,
		"step_id":       sessionV3ProviderToolStepID(step),
		"reasoning_id":  sessionV3ReasoningEventID(step, reasoningKey),
		"reasoning_key": reasoningKey,
		"recorded_at":   now,
	}
	if delta != "" {
		payload["delta"] = delta
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if eventIndex > 0 && eventType == "session.reasoning.delta" {
		payload["delta_index"] = eventIndex
		payload["delta_version"] = 2
		if deltaMode == "append" || deltaMode == "replace" {
			payload["delta_mode"] = deltaMode
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, now)
	contentForHash := string(raw)
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, "", eventType, contentForHash)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := sessionV3ReasoningEventClientRequestID(eventType, job.RunID, step, reasoningKey, eventIndex)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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
}

func sessionV3MergeReasoningSnapshotOrChunk(previous, incoming string) string {
	previous = strings.TrimSpace(previous)
	incoming = strings.TrimSpace(incoming)
	if incoming == "" {
		return previous
	}
	if previous == "" || strings.HasPrefix(incoming, previous) {
		return incoming
	}
	if strings.HasPrefix(previous, incoming) {
		return previous
	}
	return incoming
}

type sessionV3ResolvedRuntime struct {
	Session       pebblestore.SessionSnapshot
	AgentProfile  pebblestore.AgentProfile
	Preference    pebblestore.ModelPreference
	ContextWindow int
	ModelCatalog  any
	Scope         tool.WorkspaceScope
	Instructions  string
	Tools         []provideriface.ToolDefinition
	ToolChoice    string
}

type sessionV3AssistantResponse struct {
	Content                       string
	LifecycleOnly                 bool
	StartNextCheckpoint           bool
	AgentName                     string
	ResolvedAgentName             string
	ExecutorKind                  string
	ProviderID                    string
	Model                         string
	ProviderLineageID             string
	ProviderConfigurationHash     string
	ContextBranchID               string
	ProviderCacheKey              string
	SessionAffinityKey            string
	TransportAffinityKey          string
	EpochID                       string
	BoundaryReason                string
	PreviousProviderLineageID     string
	PreviousProviderID            string
	PreviousModel                 string
	NewProviderID                 string
	NewModel                      string
	HandoffSummaryMessageID       string
	HandoffSummaryGlobalSeq       uint64
	ProviderLineageStartMessageID string
	ProviderLineageStartRunID     string
	ProviderLineageStartGlobalSeq uint64
	NativeContinuationAllowed     bool
	ForceFreshProviderContext     bool
	ProviderResponseID            string
	StopReason                    string
	Usage                         provideriface.TokenUsage
	ProviderOutputItems           []any
	StreamID                      string
	StreamStep                    int
	StreamOffsetEnd               uint64
}

func (r sessionV3AssistantResponse) metadata(runID string) map[string]any {
	metadata := map[string]any{
		"run_id":        strings.TrimSpace(runID),
		"executor_kind": strings.TrimSpace(r.ExecutorKind),
	}
	if agentName := strings.TrimSpace(r.AgentName); agentName != "" {
		metadata["agent_name"] = agentName
	}
	if resolvedAgentName := strings.TrimSpace(r.ResolvedAgentName); resolvedAgentName != "" {
		metadata["resolved_agent_name"] = resolvedAgentName
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
	if lineageID := strings.TrimSpace(r.ProviderLineageID); lineageID != "" {
		metadata["provider_lineage_id"] = lineageID
	}
	if branchID := strings.TrimSpace(r.ContextBranchID); branchID != "" {
		metadata["context_branch_id"] = branchID
	}
	if cacheKey := strings.TrimSpace(r.ProviderCacheKey); cacheKey != "" {
		metadata["provider_cache_key"] = cacheKey
	}
	if affinityKey := strings.TrimSpace(r.SessionAffinityKey); affinityKey != "" {
		metadata["session_affinity_key"] = affinityKey
	}
	if transportKey := strings.TrimSpace(r.TransportAffinityKey); transportKey != "" {
		metadata["transport_affinity_key"] = transportKey
	}
	if epochID := strings.TrimSpace(r.EpochID); epochID != "" {
		metadata["epoch_id"] = epochID
	}
	if boundaryReason := strings.TrimSpace(r.BoundaryReason); boundaryReason != "" {
		metadata["boundary_reason"] = boundaryReason
	}
	if previousLineageID := strings.TrimSpace(r.PreviousProviderLineageID); previousLineageID != "" {
		metadata["previous_provider_lineage_id"] = previousLineageID
	}
	if previousProvider := strings.TrimSpace(r.PreviousProviderID); previousProvider != "" {
		metadata["previous_provider"] = previousProvider
	}
	if previousModel := strings.TrimSpace(r.PreviousModel); previousModel != "" {
		metadata["previous_model"] = previousModel
	}
	if newProvider := strings.TrimSpace(r.NewProviderID); newProvider != "" {
		metadata["new_provider"] = newProvider
	}
	if newModel := strings.TrimSpace(r.NewModel); newModel != "" {
		metadata["new_model"] = newModel
	}
	if summaryID := strings.TrimSpace(r.HandoffSummaryMessageID); summaryID != "" {
		metadata["handoff_summary_message_id"] = summaryID
	}
	if r.HandoffSummaryGlobalSeq != 0 {
		metadata["handoff_summary_global_seq"] = r.HandoffSummaryGlobalSeq
	}
	if startMessageID := strings.TrimSpace(r.ProviderLineageStartMessageID); startMessageID != "" {
		metadata["provider_lineage_start_message_id"] = startMessageID
	}
	if startRunID := strings.TrimSpace(r.ProviderLineageStartRunID); startRunID != "" {
		metadata["provider_lineage_start_run_id"] = startRunID
	}
	if r.ProviderLineageStartGlobalSeq != 0 {
		metadata["provider_lineage_start_global_seq"] = r.ProviderLineageStartGlobalSeq
	}
	metadata["native_continuation_allowed"] = r.NativeContinuationAllowed
	metadata["force_fresh_provider_context"] = r.ForceFreshProviderContext
	if responseID := strings.TrimSpace(r.ProviderResponseID); responseID != "" {
		metadata["provider_response_id"] = responseID
	}
	if stopReason := strings.TrimSpace(r.StopReason); stopReason != "" {
		metadata["stop_reason"] = stopReason
	}
	if r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 || r.Usage.ThinkingTokens != 0 || r.Usage.TotalTokens != 0 || r.Usage.CacheReadTokens != 0 || r.Usage.CacheWriteTokens != 0 || r.Usage.Source != "" || r.Usage.Transport != "" {
		metadata["usage"] = r.Usage
	}
	if len(r.ProviderOutputItems) > 0 {
		metadata["provider_output_format"] = "responses_api"
		metadata["provider_output_items"] = cloneSessionsV3ProviderItems(r.ProviderOutputItems)
	}
	if streamID := strings.TrimSpace(r.StreamID); streamID != "" {
		metadata["stream_id"] = streamID
		metadata["stream_step"] = r.StreamStep
		metadata["stream_offset_end"] = r.StreamOffsetEnd
	}
	return metadata
}

func (e *sessionV3Executor) persistSessionV3ProviderLifecycle(job sessionV3ExecutorJob, response sessionV3AssistantResponse, result sessionruntime.SessionMutationResult) error {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return errors.New("v3 executor is not configured")
	}
	epochID := strings.TrimSpace(firstNonEmptyString(response.EpochID, job.EpochID))
	if epochID == "" {
		return errors.New("completed provider response has no execution epoch")
	}
	messageID := ""
	globalSeq := result.PrimarySeq
	if result.Message != nil {
		messageID = strings.TrimSpace(result.Message.ID)
		globalSeq = result.Message.GlobalSeq
	}
	state := pebblestore.ExecutionProviderLifecycleState{
		SessionID:                     job.SessionID,
		EpochID:                       epochID,
		Provider:                      response.ProviderID,
		Model:                         response.Model,
		ConfigurationHash:             response.ProviderConfigurationHash,
		ProviderLineageID:             response.ProviderLineageID,
		ContextBranchID:               response.ContextBranchID,
		ProviderCacheKey:              response.ProviderCacheKey,
		SessionAffinityKey:            response.SessionAffinityKey,
		TransportAffinityKey:          response.TransportAffinityKey,
		PreviousProviderLineageID:     response.PreviousProviderLineageID,
		PreviousProvider:              response.PreviousProviderID,
		PreviousModel:                 response.PreviousModel,
		BoundaryReason:                response.BoundaryReason,
		HandoffSummaryMessageID:       response.HandoffSummaryMessageID,
		HandoffSummaryGlobalSeq:       response.HandoffSummaryGlobalSeq,
		ProviderLineageStartMessageID: firstNonEmptyString(response.ProviderLineageStartMessageID, messageID),
		ProviderLineageStartRunID:     firstNonEmptyString(response.ProviderLineageStartRunID, job.RunID),
		ProviderLineageStartGlobalSeq: firstNonZeroUint64(response.ProviderLineageStartGlobalSeq, globalSeq),
		UpdatedAt:                     time.Now().UnixMilli(),
	}
	return e.server.sessions.PutExecutionProviderLifecycleState(state)
}

func (e *sessionV3Executor) completeRun(job sessionV3ExecutorJob, response sessionV3AssistantResponse) (sessionruntime.SessionMutationResult, error) {
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	content := response.Content
	classification := sessionV3TerminalClassifier.Classify(TerminalClassifierInput{
		ProviderID:      response.ProviderID,
		StopReason:      response.StopReason,
		HasFinalContent: strings.TrimSpace(content) != "",
	})
	if classification.Status != sessionruntime.RunIntentCompleted {
		return e.recordRunStatus(job, classification.Status, classification.Reason, classification.EventType)
	}
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{
		ID:        sessionV3AssistantMessageID(job.SessionID, job.RunID),
		Role:      "assistant",
		Content:   content,
		CreatedAt: now,
		Metadata:  response.metadata(job.RunID),
	}
	e.recordSessionV3Diagnostic(job, "session.diagnostic.backend.message", "backend.executor", "assistant-message-before-store", sessionV3MessageDiagnostic(message, response))
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentCompleted, now)
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

func (e *sessionV3Executor) recordPreToolAssistantSegment(job sessionV3ExecutorJob, response sessionV3AssistantResponse, step int) (sessionruntime.SessionMutationResult, error) {
	if e.isRunCanceled(job) {
		return sessionruntime.SessionMutationResult{}, context.Canceled
	}
	content := response.Content
	if strings.TrimSpace(content) == "" {
		return sessionruntime.SessionMutationResult{}, nil
	}
	now := time.Now().UnixMilli()
	metadata := response.metadata(job.RunID)
	metadata["segment_kind"] = "pre_tool"
	metadata["step"] = step
	metadata["step_id"] = sessionV3ProviderToolStepID(step)
	message := pebblestore.MessageSnapshot{
		ID:        sessionV3AssistantSegmentMessageID(job.SessionID, job.RunID, step),
		Role:      "assistant",
		Content:   content,
		CreatedAt: now,
		Metadata:  metadata,
	}
	e.recordSessionV3Diagnostic(job, "session.diagnostic.backend.message", "backend.executor", fmt.Sprintf("assistant-pre-tool-message-step-%d-before-store", step), sessionV3MessageDiagnostic(message, response))
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, now)
	eventType := "session.message.appended"
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, sessionruntime.RunIntentRunning, "", eventType, fmt.Sprintf("%d:%s", step, content))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	clientRequestID := sessionV3ExecutorStepClientRequestID("session.assistant.pre_tool", job.RunID, step)
	return e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       eventType,
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
	title, err := e.generateSessionV3CompactTitle(session, conversation, job.Principal)
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
	currentMessages, err := e.server.sessions.ListSessionMessages(job.SessionID, 0, sessionV3TitleConversationLimit)
	if err != nil || !shouldGenerateSessionV3TitleWithMessages(current, currentMessages) {
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

func (e *sessionV3Executor) generateSessionV3CompactTitle(session pebblestore.SessionSnapshot, promptContext string, principal identity.Principal) (string, error) {
	if e == nil || e.server == nil || e.server.providers == nil || e.server.model == nil || e.server.agents == nil {
		return "", errors.New("Compact provider, model, and agent services are not configured")
	}
	providerID := strings.ToLower(strings.TrimSpace(session.Preference.Provider))
	compactModel := ""
	compactThinking := ""
	if e.server.uiSettings != nil {
		if settings, settingsErr := e.server.uiSettings.GetForAccount(principal.AccountScopeID); settingsErr == nil {
			if configured := strings.ToLower(strings.TrimSpace(settings.Agents.Compact.Provider)); configured != "" {
				providerID = configured
			}
			compactModel = strings.TrimSpace(settings.Agents.Compact.Model)
			compactThinking = strings.TrimSpace(settings.Agents.Compact.Thinking)
		}
	}
	_, _, utility, ok, err := e.server.model.RecommendedCatalogDefaults(providerID)
	if err != nil {
		return "", fmt.Errorf("resolve Compact utility recommendation: %w", err)
	}
	if !ok || strings.TrimSpace(utility.Model) == "" {
		return "", fmt.Errorf("Compact utility recommendation for provider %q is unavailable", providerID)
	}
	if compactModel == "" {
		compactModel = strings.TrimSpace(utility.Model)
	}
	if compactThinking == "" {
		for _, recommendation := range utility.Recommendations {
			if strings.EqualFold(strings.TrimSpace(recommendation.Role), "utility") {
				compactThinking = strings.TrimSpace(recommendation.Thinking)
				break
			}
		}
	}
	compactProfile, err := e.server.agents.ResolveSystemAgent(agentruntime.CompactAgentID, pebblestore.AgentProfile{Provider: providerID, Model: compactModel, Thinking: compactThinking})
	if err != nil {
		return "", err
	}
	preference, contextWindow, err := e.resolveSessionV3ProviderPreference(applySessionV3AgentPreferenceOverridesForMode(session.Preference, compactProfile, session.Mode))
	if err != nil {
		return "", err
	}
	catalogRecord, err := e.sessionV3ModelCatalogRecord(preference)
	if err != nil {
		return "", err
	}
	providerID = strings.ToLower(strings.TrimSpace(preference.Provider))
	if providerID == "" {
		return "", errors.New("resolved Compact title provider is empty")
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok {
		return "", fmt.Errorf("Compact title provider %q is not runnable", providerID)
	}
	modelName := strings.TrimSpace(preference.Model)
	if modelName == "" {
		return "", errors.New("resolved Compact title model is empty")
	}
	instructions := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(compactProfile.Prompt),
		"Title-only case: generate a deterministic session title. Do not summarize or compact the conversation.",
		fmt.Sprintf("Return only the title text with at most %d words.", sessionV3TitleFinalWordsMax),
		"No markdown, no quotes, no explanations, no trailing punctuation.",
		"Stage: final.",
	}, "\n"))
	providerLineageID := provideriface.ShortProviderLineageKey("session_title", session.ID, providerID, modelName, instructions)
	req := provideriface.Request{
		SessionID:                 session.ID,
		ProviderLineageID:         providerLineageID,
		ContextBranchID:           provideriface.ShortProviderLineageKey("session", session.ID, session.Mode),
		ProviderCacheKey:          sessionV3ProviderScopedKey("cache", providerLineageID),
		SessionAffinityKey:        sessionV3ProviderScopedKey("affinity", providerLineageID),
		BoundaryReason:            "session_title",
		NativeContinuationAllowed: false,
		ForceFreshProviderContext: true,
		Model:                     modelName,
		Thinking:                  normalizeSessionV3ThinkingWithProvider(providerID, preference.Thinking),
		Instructions:              instructions,
		Input:                     []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "Conversation summary:\n" + truncateSessionV3TitleRunes(promptContext, sessionV3TitlePromptPreviewRunes)}}}},
		ToolChoice:                "none",
		ServiceTier:               strings.TrimSpace(preference.ServiceTier),
		ContextMode:               strings.TrimSpace(preference.ContextMode),
		ContextWindow:             contextWindow,
		ModelCatalog:              catalogRecord,
		WorkspacePath:             strings.TrimSpace(session.WorkspacePath),
	}
	bgCtx := context.Background()
	if principal.Valid() {
		bgCtx = identity.ContextWithPrincipal(bgCtx, principal)
	}
	ctx, cancel := context.WithTimeout(bgCtx, sessionV3TitleGenerationTimeout)
	defer cancel()
	e.recordSessionV3Diagnostic(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: providerLineageID}, "session.diagnostic.title.request", "backend.title", "compact-title-request", map[string]any{
		"provider": providerID,
		"model":    modelName,
		"request":  sessionV3ProviderRequestDiagnostic(req),
	})
	var streamed strings.Builder
	var reasoning strings.Builder
	response, err := runner.CreateResponseStreaming(ctx, req, func(event provideriface.StreamEvent) {
		switch event.Type {
		case provideriface.StreamEventOutputTextDelta:
			streamed.WriteString(event.Delta)
		case provideriface.StreamEventReasoningSummaryDelta:
			reasoning.WriteString(event.Delta)
		}
	})
	if err != nil {
		e.recordSessionV3Diagnostic(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: providerLineageID}, "session.diagnostic.title.error", "backend.title", "compact-title-error", map[string]any{
			"provider": providerID,
			"model":    modelName,
			"error":    err.Error(),
		})
		return "", err
	}
	rawTitle := firstNonEmpty(strings.TrimSpace(response.Text), strings.TrimSpace(streamed.String()), strings.TrimSpace(response.ReasoningSummary), strings.TrimSpace(reasoning.String()))
	words := sessionV3TitleWordPattern.FindAllString(strings.TrimSpace(rawTitle), -1)
	title := sanitizeSessionV3GeneratedTitle(rawTitle, sessionV3TitleFinalWordsMin, sessionV3TitleFinalWordsMax)
	rejectReason := ""
	if strings.TrimSpace(rawTitle) == "" {
		rejectReason = "empty_raw_title"
	} else if sessionV3TitleFinalWordsMin > 0 && len(words) < sessionV3TitleFinalWordsMin {
		rejectReason = "too_few_words"
	} else if title == "" {
		rejectReason = "sanitizer_rejected"
	}
	e.recordSessionV3Diagnostic(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: providerLineageID}, "session.diagnostic.title.response", "backend.title", "compact-title-response", map[string]any{
		"provider":                   providerID,
		"model":                      modelName,
		"response_model":             strings.TrimSpace(response.Model),
		"stop_reason":                strings.TrimSpace(response.StopReason),
		"text_present":               strings.TrimSpace(response.Text) != "",
		"streamed_text_present":      strings.TrimSpace(streamed.String()) != "",
		"reasoning_present":          strings.TrimSpace(response.ReasoningSummary) != "",
		"streamed_reasoning_present": strings.TrimSpace(reasoning.String()) != "",
		"raw_title_preview":          truncateSessionV3TitleRunes(rawTitle, 160),
		"raw_word_count":             len(words),
		"sanitized_title":            title,
		"reject_reason":              rejectReason,
	})
	if title == "" {
		return "", fmt.Errorf("Compact returned an empty/invalid title: %s", firstNonEmpty(rejectReason, "unknown"))
	}
	return title, nil
}

func (e *sessionV3Executor) assistantResponse(ctx context.Context, job sessionV3ExecutorJob) (sessionV3AssistantResponse, error) {
	resolved, err := e.resolveSessionV3Runtime(job)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	return e.providerAssistantResponse(ctx, job, resolved, "", false)
}

func (e *sessionV3Executor) contextOverflowCompactedAssistantResponse(ctx context.Context, job sessionV3ExecutorJob, cause error) (sessionV3AssistantResponse, sessionV3ExecutorJob, error) {
	if e == nil || e.server == nil || e.server.runner == nil || e.server.sessions == nil {
		return sessionV3AssistantResponse{}, job, cause
	}
	compactRunID := job.RunID + "-overflow-compact"
	result, err := e.server.runner.RunTurn(ctx, job.SessionID, runruntime.RunRequest{Prompt: "context overflow compact request", Compact: true, CompactOrigin: "overflow"}, runruntime.RunStartMeta{RunID: compactRunID, Principal: job.Principal, ApplySessionMutation: e.server.applySessionV3PrimaryMutation})
	if err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 context overflow compact failed: %w", err)
	}
	if strings.TrimSpace(result.AssistantMessage.Content) == "" {
		return sessionV3AssistantResponse{}, job, errors.New("v3 context overflow compact returned empty checkpoint acknowledgement")
	}
	activeEpoch, ok, err := e.server.sessions.GetActiveExecutionEpoch(job.SessionID)
	if err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 context overflow compact continuation epoch resolve failed: %w", err)
	}
	if !ok || strings.TrimSpace(activeEpoch.EpochID) == "" {
		return sessionV3AssistantResponse{}, job, errors.New("v3 context overflow compact did not create an active continuation epoch")
	}
	if activeEpoch.ParentEpochID != strings.TrimSpace(job.EpochID) || activeEpoch.Boundary.RunID != compactRunID || activeEpoch.Boundary.Reason != "context_compaction_overflow" {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 context overflow compact created unexpected continuation epoch %q", activeEpoch.EpochID)
	}
	job.EpochID = activeEpoch.EpochID
	resolved, err := e.resolveSessionV3Runtime(job)
	if err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 context overflow compact continuation runtime resolve failed: %w", err)
	}
	response, err := e.providerAssistantResponse(ctx, job, resolved, "overflow-continuation", true)
	return response, job, err
}

func sessionV3IsContextOverflowDiagnostic(detail string) bool {
	normalized := strings.ToLower(strings.TrimSpace(detail))
	return strings.Contains(normalized, "context_length_exceeded") || strings.Contains(normalized, "context window") || strings.Contains(normalized, "context length") || strings.Contains(normalized, "maximum context")
}

func (e *sessionV3Executor) providerAssistantResponse(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, requestPhaseSuffix string, forceCommittedContext bool) (sessionV3AssistantResponse, error) {
	if e == nil || e.server == nil || e.server.providers == nil {
		return sessionV3AssistantResponse{}, errors.New("provider registry is not configured")
	}
	pref := resolved.Preference
	providerID := strings.ToLower(strings.TrimSpace(pref.Provider))
	modelName := strings.TrimSpace(pref.Model)
	if providerID == "" || modelName == "" {
		return sessionV3AssistantResponse{}, errors.New("resolved v3 provider/model is empty")
	}
	runner, err := e.sessionV3ProviderRunner(resolved)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	var messages []pebblestore.MessageSnapshot
	var input []map[string]any
	checkpointRestartInput := false
	if !forceCommittedContext && (strings.TrimSpace(job.CheckpointID) != "" || strings.TrimSpace(job.PlanID) != "") {
		checkpointInput, ok, checkpointErr := e.sessionV3ProviderCheckpointRestartInput(ctx, job, resolved, "")
		if checkpointErr != nil {
			return sessionV3AssistantResponse{}, checkpointErr
		}
		if ok {
			input = checkpointInput
			checkpointRestartInput = true
		}
	}
	if !checkpointRestartInput {
		var err error
		messages, err = e.sessionV3ProviderContextMessages(job)
		if err != nil {
			return sessionV3AssistantResponse{}, err
		}
		input = sessionsV3ProviderInput(messages)
	}
	if len(input) == 0 {
		return sessionV3AssistantResponse{}, errors.New("v3 provider input is empty")
	}
	baseReq, err := e.sessionV3ProviderBaseRequest(job, resolved, input)
	if err != nil {
		return sessionV3AssistantResponse{}, err
	}
	if forceCommittedContext {
		// Overflow compaction is a hard provider boundary. The compacted epoch is
		// the complete continuation context, so neither response lineage nor the
		// websocket that carried the overflowing chain may be reused.
		baseReq.StartNewChain = true
		baseReq.AllowContinuation = false
		baseReq.ReuseTransport = false
		baseReq.ResetTransport = true
		baseReq.NativeContinuationAllowed = false
		baseReq.ForceFreshProviderContext = true
	} else if checkpointRestartInput {
		baseReq.BoundaryReason = sessionV3ProviderBoundaryReasonWithOverride(baseReq.BoundaryReason, "checkpoint_fresh_context")
		baseReq.StartNewChain = true
		baseReq.AllowContinuation = false
		baseReq.ReuseTransport = true
		baseReq.NativeContinuationAllowed = false
		baseReq.ForceFreshProviderContext = true
	} else if sessionV3ProviderRequiresBoundedHandoff(baseReq) {
		handoffInput, handoffErr := e.sessionV3ProviderHandoffInput(job, resolved, messages, baseReq)
		if handoffErr != nil {
			return sessionV3AssistantResponse{}, handoffErr
		}
		if len(handoffInput) == 0 {
			return sessionV3AssistantResponse{}, errors.New("v3 provider handoff input is empty")
		}
		baseReq.Input = handoffInput
	} else {
		input = sessionsV3ProviderInputForLineage(messages, baseReq.ProviderLineageID, baseReq.NativeContinuationAllowed && !baseReq.ForceFreshProviderContext)
		if len(input) == 0 {
			return sessionV3AssistantResponse{}, errors.New("v3 provider input is empty")
		}
		baseReq.Input = append([]map[string]any(nil), input...)
	}
	requestEventType := "session.provider.request_started"
	if suffix := strings.TrimSpace(requestPhaseSuffix); suffix != "" {
		requestEventType += "." + suffix
	}
	if _, err := e.recordRunPhase(job, RunPhaseProviderRequestStarted, requestEventType); err != nil {
		return sessionV3AssistantResponse{}, err
	}
	ctx = identity.ContextWithPrincipal(ctx, job.Principal)
	ctx = providerdiagnostics.ContextWithRecorder(ctx, func(_ context.Context, event providerdiagnostics.Event) {
		e.recordSessionV3ProviderAPIDiagnostic(job, event)
	})
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	var sink *sessionV3DurableProgressSink
	if e.durableProgressWriterForTest != nil {
		sink = newSessionV3DurableProgressSinkWithWriter(e, job, cancelStream, e.durableProgressWriterForTest)
	} else {
		sink = newSessionV3DurableProgressSink(e, job, cancelStream)
	}
	loopResult, err := e.runProviderToolLoop(streamCtx, job, resolved, runner, baseReq, sink)
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	closeErr := sink.CloseAndFlush(flushCtx)
	if err != nil {
		e.recordSessionV3Diagnostic(job, "session.diagnostic.provider.error", "backend.provider", "provider-error", map[string]any{
			"provider": providerID,
			"model":    modelName,
			"error":    err.Error(),
		})
		return sessionV3AssistantResponse{}, err
	}
	if closeErr != nil {
		e.recordSessionV3Diagnostic(job, "session.diagnostic.provider.error", "backend.provider", "provider-error", map[string]any{
			"provider": providerID,
			"model":    modelName,
			"error":    closeErr.Error(),
		})
		return sessionV3AssistantResponse{}, closeErr
	}
	response := loopResult.Response
	e.recordSessionV3Diagnostic(job, "session.diagnostic.provider.response", "backend.provider", "provider-response", map[string]any{
		"provider": providerID,
		"model":    modelName,
		"result":   sessionV3ProviderResponseDiagnostic(response, loopResult.FinalContent, loopResult.DurableFlushCount),
	})
	if loopResult.TerminalPlanHandled {
		return sessionV3AssistantResponse{LifecycleOnly: true, StartNextCheckpoint: loopResult.StartNextCheckpoint}, nil
	}
	content := loopResult.FinalContent
	if strings.TrimSpace(content) == "" {
		return sessionV3AssistantResponse{}, errors.New("provider returned empty assistant response")
	}
	model := strings.TrimSpace(response.Model)
	if model == "" {
		model = modelName
	}
	providerRunnerID := strings.TrimSpace(runner.ID())
	if providerRunnerID == "" {
		providerRunnerID = providerID
	}
	agentName := strings.TrimSpace(resolved.AgentProfile.Name)
	assistant := sessionV3AssistantResponse{
		Content:                       content,
		AgentName:                     agentName,
		ResolvedAgentName:             agentName,
		ExecutorKind:                  "v3_provider",
		ProviderID:                    providerRunnerID,
		Model:                         model,
		ProviderResponseID:            strings.TrimSpace(response.ID),
		StopReason:                    strings.TrimSpace(response.StopReason),
		Usage:                         response.Usage,
		ProviderLineageID:             loopResult.FinalRequest.ProviderLineageID,
		ProviderConfigurationHash:     loopResult.FinalRequest.ProviderConfigurationHash,
		ContextBranchID:               loopResult.FinalRequest.ContextBranchID,
		ProviderCacheKey:              loopResult.FinalRequest.ProviderCacheKey,
		SessionAffinityKey:            loopResult.FinalRequest.SessionAffinityKey,
		TransportAffinityKey:          loopResult.FinalRequest.TransportAffinityKey,
		EpochID:                       loopResult.FinalRequest.ExecutionEpochID,
		BoundaryReason:                loopResult.FinalRequest.BoundaryReason,
		PreviousProviderLineageID:     loopResult.FinalRequest.PreviousProviderLineageID,
		PreviousProviderID:            loopResult.FinalRequest.PreviousProviderID,
		PreviousModel:                 loopResult.FinalRequest.PreviousModel,
		NewProviderID:                 loopResult.FinalRequest.NewProviderID,
		NewModel:                      loopResult.FinalRequest.NewModel,
		HandoffSummaryMessageID:       loopResult.FinalRequest.HandoffSummaryMessageID,
		HandoffSummaryGlobalSeq:       loopResult.FinalRequest.HandoffSummaryGlobalSeq,
		ProviderLineageStartMessageID: loopResult.FinalRequest.ProviderLineageStartMessageID,
		ProviderLineageStartRunID:     loopResult.FinalRequest.ProviderLineageStartRunID,
		ProviderLineageStartGlobalSeq: loopResult.FinalRequest.ProviderLineageStartGlobalSeq,
		NativeContinuationAllowed:     loopResult.FinalRequest.NativeContinuationAllowed,
		ForceFreshProviderContext:     loopResult.FinalRequest.ForceFreshProviderContext,
		ProviderOutputItems:           sessionV3ProviderNativeOutputItems(providerRunnerID, response.Raw),
		StreamID:                      loopResult.FinalStreamID,
		StreamStep:                    loopResult.FinalStep,
		StreamOffsetEnd:               loopResult.FinalOffsetEnd,
	}
	if err := validateSessionV3AssistantStreamCompletion(content, assistant.StreamOffsetEnd); err != nil {
		return sessionV3AssistantResponse{}, err
	}
	e.recordSessionV3Diagnostic(job, "session.diagnostic.backend.final", "backend.executor", "assistant-final", map[string]any{
		"content":            content,
		"provider_response":  response,
		"assistant_response": assistant,
	})
	return assistant, nil
}

func (e *sessionV3Executor) sessionV3ProviderRunner(resolved sessionV3ResolvedRuntime) (provideriface.Runner, error) {
	if e == nil || e.server == nil || e.server.providers == nil {
		return nil, errors.New("provider registry is not configured")
	}
	providerID := strings.ToLower(strings.TrimSpace(resolved.Preference.Provider))
	if providerID == "" {
		return nil, errors.New("resolved v3 provider/model is empty")
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q is configured but not runnable yet", providerID)
	}
	return runner, nil
}

func (e *sessionV3Executor) sessionV3ProviderBaseRequest(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, input []map[string]any) (provideriface.Request, error) {
	return e.sessionV3ProviderBaseRequestWithCheckpointScope(job, resolved, input, sessionV3ProviderJobCheckpointScope(job))
}

func (e *sessionV3Executor) sessionV3ProviderBaseRequestWithCheckpointScope(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, input []map[string]any, checkpointScope sessionV3ProviderCheckpointScope) (provideriface.Request, error) {
	pref := resolved.Preference
	providerID := strings.ToLower(strings.TrimSpace(pref.Provider))
	model := strings.TrimSpace(pref.Model)
	epochID := strings.TrimSpace(job.EpochID)
	if epochID == "" && e != nil && e.server != nil && e.server.sessions != nil {
		if intent, ok, err := e.server.sessions.GetV3SessionRunIntent(job.SessionID, job.RunID); err != nil {
			return provideriface.Request{}, err
		} else if ok {
			epochID = strings.TrimSpace(intent.EpochID)
		}
		if epochID == "" {
			if active, ok, err := e.server.sessions.GetActiveExecutionEpoch(job.SessionID); err != nil {
				return provideriface.Request{}, err
			} else if ok {
				epochID = strings.TrimSpace(active.EpochID)
			}
		}
	}
	if epochID == "" {
		return provideriface.Request{}, errors.New("v3 provider request has no execution epoch")
	}
	if e == nil || e.server == nil || e.server.providers == nil {
		return provideriface.Request{}, errors.New("provider registry is not configured")
	}
	runner, ok := e.server.providers.GetRunner(providerID)
	if !ok || runner == nil {
		return provideriface.Request{}, fmt.Errorf("provider %q is configured but not runnable yet", providerID)
	}
	declared, declaredOK := runner.(provideriface.ExecutionEpochLifecycleRunner)
	if !declaredOK {
		return provideriface.Request{}, fmt.Errorf("provider %q has no declared execution epoch lifecycle capability", providerID)
	}
	lifecycle := declared.ExecutionEpochLifecycle()
	if !lifecycle.Valid() {
		return provideriface.Request{}, fmt.Errorf("provider %q declared an invalid execution epoch lifecycle capability", providerID)
	}
	contextBranchID := provideriface.ShortProviderLineageKey("epoch", strings.TrimSpace(job.SessionID), epochID)
	configurationHash := provideriface.ShortProviderLineageKey(
		providerID,
		model,
		resolved.Instructions,
		sessionV3ProviderToolsLineageHash(resolved.Tools),
		strings.TrimSpace(resolved.Session.Mode),
		strings.TrimSpace(resolved.AgentProfile.Name),
		strings.TrimSpace(resolved.AgentProfile.Mode),
		strings.TrimSpace(resolved.AgentProfile.RuntimeMode),
		strings.TrimSpace(resolved.AgentProfile.ExecutionSetting),
		strings.TrimSpace(pref.Thinking),
		strings.TrimSpace(pref.ServiceTier),
		strings.TrimSpace(pref.ContextMode),
	)
	previousState, previousOK, err := e.server.sessions.GetExecutionProviderLifecycleState(job.SessionID, epochID)
	if err != nil {
		return provideriface.Request{}, err
	}
	previousLineageID := strings.TrimSpace(previousState.ProviderLineageID)
	lineageID := provideriface.ShortProviderLineageKey(job.SessionID, epochID, configurationHash)
	nativeContinuationAllowed := previousOK && previousState.ConfigurationHash == configurationHash && previousLineageID == lineageID
	providerCacheKey := sessionV3ProviderScopedKey("cache", epochID+"-"+lineageID)
	sessionAffinityKey := sessionV3ProviderScopedKey("affinity", epochID+"-"+lineageID)
	boundaryReason := "session_turn"
	if !previousOK {
		boundaryReason = "epoch_fresh_context"
		if sessionV3ProviderCheckpointFreshContext(job, checkpointScope) {
			boundaryReason = "checkpoint_fresh_context"
		}
	} else if !nativeContinuationAllowed {
		boundaryReason = "provider_model_runtime_handoff"
	}
	lineageStart := sessionV3ProviderLineageSnapshot{
		ProviderLineageID:       previousState.ProviderLineageID,
		ContextBranchID:         previousState.ContextBranchID,
		BoundaryReason:          previousState.BoundaryReason,
		ProviderID:              previousState.Provider,
		Model:                   previousState.Model,
		StartMessageID:          previousState.ProviderLineageStartMessageID,
		StartRunID:              previousState.ProviderLineageStartRunID,
		StartGlobalSeq:          previousState.ProviderLineageStartGlobalSeq,
		HandoffSummaryMessageID: previousState.HandoffSummaryMessageID,
		HandoffSummaryGlobalSeq: previousState.HandoffSummaryGlobalSeq,
	}
	providerContinuationAllowed := lifecycle.ContextMode == provideriface.ExecutionEpochContextResponsesChain && nativeContinuationAllowed
	baseReq := provideriface.Request{
		SessionID:                     job.SessionID,
		ProviderLineageID:             lineageID,
		ExecutionEpochID:              epochID,
		ProviderConfigurationHash:     configurationHash,
		ContextBranchID:               contextBranchID,
		ProviderCacheKey:              providerCacheKey,
		SessionAffinityKey:            sessionAffinityKey,
		TransportAffinityKey:          sessionV3ProviderTransportAffinityKey(job, providerID, model),
		BoundaryReason:                boundaryReason,
		PreviousProviderLineageID:     previousLineageID,
		PreviousProviderID:            previousState.Provider,
		PreviousModel:                 previousState.Model,
		NewProviderID:                 providerID,
		NewModel:                      model,
		HandoffSummaryMessageID:       lineageStart.HandoffSummaryMessageID,
		HandoffSummaryGlobalSeq:       lineageStart.HandoffSummaryGlobalSeq,
		ProviderLineageStartMessageID: lineageStart.StartMessageID,
		ProviderLineageStartRunID:     lineageStart.StartRunID,
		ProviderLineageStartGlobalSeq: lineageStart.StartGlobalSeq,
		StartNewChain:                 lifecycle.ContextMode == provideriface.ExecutionEpochContextResponsesChain && !providerContinuationAllowed,
		AllowContinuation:             providerContinuationAllowed,
		ReuseTransport:                lifecycle.TransportReusable,
		NativeContinuationAllowed:     providerContinuationAllowed,
		ForceFreshProviderContext:     !providerContinuationAllowed,
		Model:                         model,
		Thinking:                      strings.TrimSpace(pref.Thinking),
		Instructions:                  resolved.Instructions,
		Input:                         append([]map[string]any(nil), input...),
		Tools:                         resolved.Tools,
		ToolChoice:                    resolved.ToolChoice,
		ServiceTier:                   strings.TrimSpace(pref.ServiceTier),
		ContextMode:                   strings.TrimSpace(pref.ContextMode),
		ContextWindow:                 resolved.ContextWindow,
		ModelCatalog:                  resolved.ModelCatalog,
		ParallelToolCalls:             true,
		WorkspacePath:                 strings.TrimSpace(resolved.Scope.PrimaryPath),
	}
	if baseReq.Model == "" {
		return provideriface.Request{}, errors.New("resolved v3 provider/model is empty")
	}
	if baseReq.Thinking == "" {
		baseReq.Thinking = "medium"
	}
	if strings.TrimSpace(baseReq.Instructions) == "" {
		return provideriface.Request{}, errors.New("resolved v3 instructions are empty")
	}
	if strings.TrimSpace(baseReq.ToolChoice) == "" {
		baseReq.ToolChoice = "none"
	}
	return baseReq, nil
}

type sessionV3ProviderHandoffCaps struct {
	TailMessages    int
	ToolOutputChars int
	TotalChars      int
}

func sessionV3ProviderRequiresBoundedHandoff(req provideriface.Request) bool {
	previousLineageID := strings.TrimSpace(req.PreviousProviderLineageID)
	lineageID := strings.TrimSpace(req.ProviderLineageID)
	if previousLineageID == "" || lineageID == "" || previousLineageID == lineageID {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(req.BoundaryReason), "provider_model_runtime_handoff") {
		return true
	}
	return req.ForceFreshProviderContext && !req.NativeContinuationAllowed
}

func sessionV3ProviderHandoffCapsFromEnv() sessionV3ProviderHandoffCaps {
	return sessionV3ProviderHandoffCaps{
		TailMessages:    sessionV3EnvInt("SWARM_V3_HANDOFF_TAIL_MESSAGES", sessionV3HandoffDefaultTailMessages),
		ToolOutputChars: sessionV3EnvInt("SWARM_V3_HANDOFF_TOOL_OUTPUT_CHARS", sessionV3HandoffDefaultToolOutputChars),
		TotalChars:      sessionV3EnvInt("SWARM_V3_HANDOFF_TOTAL_CHARS", sessionV3HandoffDefaultTotalChars),
	}
}

func sessionV3EnvInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func (e *sessionV3Executor) sessionV3ProviderHandoffInput(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, messages []pebblestore.MessageSnapshot, req provideriface.Request) ([]map[string]any, error) {
	caps := sessionV3ProviderHandoffCapsFromEnv()
	packet, err := e.sessionV3ProviderHandoffPacket(job, resolved, messages, req, caps)
	if err != nil {
		return nil, err
	}
	return []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": packet}}}}, nil
}

func (e *sessionV3Executor) sessionV3ProviderHandoffPacket(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, messages []pebblestore.MessageSnapshot, req provideriface.Request, caps sessionV3ProviderHandoffCaps) (string, error) {
	if caps.TailMessages <= 0 {
		caps.TailMessages = sessionV3HandoffDefaultTailMessages
	}
	if caps.ToolOutputChars <= 0 {
		caps.ToolOutputChars = sessionV3HandoffDefaultToolOutputChars
	}
	if caps.TotalChars <= 0 {
		caps.TotalChars = sessionV3HandoffDefaultTotalChars
	}
	var b strings.Builder
	appendLine := func(line string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(line, " \t"))
	}
	appendLine("[provider-handoff] Bounded context packet for a provider/model/runtime boundary.")
	appendLine("Do not assume provider-native continuation or cache state from earlier lineage. Current system/developer instructions and tool definitions are supplied separately in this provider request.")
	appendLine("")
	appendLine("Boundary:")
	appendLine("- reason: " + strings.TrimSpace(req.BoundaryReason))
	appendLine("- previous_provider_lineage_id: " + strings.TrimSpace(req.PreviousProviderLineageID))
	appendLine("- provider_lineage_id: " + strings.TrimSpace(req.ProviderLineageID))
	appendLine("- context_branch_id: " + strings.TrimSpace(req.ContextBranchID))
	appendLine("- target_provider: " + strings.TrimSpace(resolved.Preference.Provider))
	appendLine("- target_model: " + strings.TrimSpace(resolved.Preference.Model))
	appendLine("- previous_provider: " + strings.TrimSpace(req.PreviousProviderID))
	appendLine("- previous_model: " + strings.TrimSpace(req.PreviousModel))
	appendLine("- new_provider: " + strings.TrimSpace(req.NewProviderID))
	appendLine("- new_model: " + strings.TrimSpace(req.NewModel))
	appendLine("")
	appendLine("Handoff summary:")
	if summary := sessionV3LatestHandoffSummary(messages); summary != "" {
		appendLine(summary)
	} else {
		appendLine(fmt.Sprintf("No compacted summary is available. Durable session contains %d fetched messages; this packet intentionally includes only a bounded visible tail and summarized tool calls.", len(messages)))
	}
	appendLine("")
	e.appendSessionV3ProviderHandoffPlanState(&b, job)
	handoffTailMessages := messages
	appendLine("")
	appendLine(fmt.Sprintf("Recent tool call summaries (latest %d tool messages; outputs capped at %d chars each):", caps.TailMessages, caps.ToolOutputChars))
	toolLines := sessionV3HandoffToolSummaries(handoffTailMessages, caps.TailMessages, caps.ToolOutputChars)
	if len(toolLines) == 0 {
		appendLine("- none")
	} else {
		for _, line := range toolLines {
			appendLine(line)
		}
	}
	appendLine("")
	appendLine(fmt.Sprintf("Visible conversation tail as Desktop sees it (latest %d user/assistant messages):", caps.TailMessages))
	visible := sessionV3HandoffVisibleTail(handoffTailMessages, caps.TailMessages)
	if len(visible) == 0 {
		appendLine("- none")
	} else {
		for _, message := range visible {
			role := strings.ToLower(strings.TrimSpace(message.Role))
			appendLine("--- " + role + " ---")
			appendLine(sessionV3TruncateForHandoff(message.Content, caps.ToolOutputChars))
		}
	}
	packet := strings.TrimSpace(b.String())
	if len([]rune(packet)) > caps.TotalChars {
		return "", fmt.Errorf("bounded provider handoff packet exceeds safety cap: %d chars > %d; compact the session before provider/model handoff", len([]rune(packet)), caps.TotalChars)
	}
	return packet, nil
}

func (e *sessionV3Executor) appendSessionV3ProviderHandoffPlanState(b *strings.Builder, job sessionV3ExecutorJob) {
	if b == nil {
		return
	}
	write := func(line string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(line, " \t"))
	}
	write("Active plan/checkpoint state:")
	if e == nil || e.server == nil || e.server.sessions == nil {
		write("- unavailable")
		return
	}
	plan, ok, err := e.server.sessions.GetActivePlan(job.SessionID)
	if err != nil || !ok || plan.Document == nil {
		write("- none")
		return
	}
	doc := plan.Document
	if title := strings.TrimSpace(firstNonEmptyString(doc.Title, plan.Title)); title != "" {
		write("- plan: " + title)
	}
	if goal := strings.TrimSpace(doc.Info.Goal); goal != "" {
		write("- goal: " + goal)
	}
	if len(doc.Info.RelevantFiles) > 0 {
		write("- relevant_files: " + strings.Join(trimStrings(doc.Info.RelevantFiles), ", "))
	}
	checkpointID := strings.TrimSpace(firstNonEmptyString(job.CheckpointID, doc.ActiveCheckpointID))
	for _, checkpoint := range doc.Checkpoints {
		if checkpointID != "" && strings.TrimSpace(checkpoint.ID) != checkpointID {
			continue
		}
		write("- checkpoint_id: " + strings.TrimSpace(checkpoint.ID))
		if title := strings.TrimSpace(checkpoint.Title); title != "" {
			write("- checkpoint_title: " + title)
		}
		if status := strings.TrimSpace(checkpoint.Status); status != "" {
			write("- checkpoint_status: " + status)
		}
		if len(checkpoint.ChangedFiles) > 0 {
			write("- changed_files: " + strings.Join(trimStrings(checkpoint.ChangedFiles), ", "))
		}
		if len(checkpoint.Validation) > 0 {
			write("- validation: " + strings.Join(trimStrings(checkpoint.Validation), "; "))
		}
		break
	}
}

func sessionV3LatestHandoffSummary(messages []pebblestore.MessageSnapshot) string {
	// The caller already supplied an explicitly named epoch range. Compaction
	// authority is encoded in that epoch boundary, never rediscovered from text.
	return ""
}

func sessionV3HandoffVisibleTail(messages []pebblestore.MessageSnapshot, limit int) []pebblestore.MessageSnapshot {
	if limit <= 0 {
		limit = sessionV3HandoffDefaultTailMessages
	}
	out := make([]pebblestore.MessageSnapshot, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		role := strings.ToLower(strings.TrimSpace(messages[i].Role))
		if role != "user" && role != "assistant" {
			continue
		}
		if strings.TrimSpace(messages[i].Content) == "" {
			continue
		}
		out = append(out, messages[i])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func sessionV3HandoffToolSummaries(messages []pebblestore.MessageSnapshot, limit int, outputLimit int) []string {
	if limit <= 0 {
		limit = sessionV3HandoffDefaultTailMessages
	}
	if outputLimit <= 0 {
		outputLimit = sessionV3HandoffDefaultToolOutputChars
	}
	out := make([]string, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		message := messages[i]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if record, ok := sessionsV3DecodeProviderToolResultRecord(content); ok {
			status := "ok"
			if strings.TrimSpace(record.Error) != "" {
				status = "error"
			}
			output := strings.TrimSpace(firstNonEmpty(record.CompletedOutput, record.Output, record.Error))
			line := fmt.Sprintf("- %s call_id=%s status=%s args=%s output=%s", strings.TrimSpace(record.ToolName), strings.TrimSpace(record.CallID), status, strings.TrimSpace(record.Arguments), sessionV3TruncateForHandoff(output, outputLimit))
			out = append(out, line)
			continue
		}
		out = append(out, "- unstructured_tool_message output="+sessionV3TruncateForHandoff(content, outputLimit))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func sessionV3TruncateForHandoff(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + fmt.Sprintf("… [truncated %d chars]", len(runes)-limit)
}

type sessionV3ProviderCheckpointScope struct {
	PlanID          string
	CheckpointID    string
	AttemptID       string
	ParentSessionID string
	FreshContext    bool
}

func sessionV3ProviderJobCheckpointScope(job sessionV3ExecutorJob) sessionV3ProviderCheckpointScope {
	checkpointID := strings.TrimSpace(job.CheckpointID)
	return sessionV3ProviderCheckpointScope{
		PlanID:          strings.TrimSpace(job.PlanID),
		CheckpointID:    checkpointID,
		AttemptID:       strings.TrimSpace(job.AttemptID),
		ParentSessionID: strings.TrimSpace(job.ParentSessionID),
		FreshContext:    checkpointID != "",
	}
}

func sessionV3ProviderCheckpointScopeFromPayload(scope sessionV3ProviderCheckpointScope, payload map[string]any) sessionV3ProviderCheckpointScope {
	if payload == nil {
		return scope
	}
	freshContext := strings.EqualFold(strings.TrimSpace(sessionsV3MapString(payload, "next_action")), "run_checkpoint_with_fresh_context")
	if freshContext {
		scope.FreshContext = true
	}
	payloadPlanID := strings.TrimSpace(sessionsV3MapString(payload, "plan_id"))
	payloadCheckpointID := strings.TrimSpace(firstNonEmptyString(sessionsV3MapString(payload, "checkpoint_id"), sessionsV3MapString(payload, "next_checkpoint_id")))
	payloadAttemptID := ""
	payloadParentSessionID := ""
	if runRequest, ok := payload["run_request"].(map[string]any); ok {
		if checkpointContext, ok := runRequest["plan_checkpoint_context"].(map[string]any); ok {
			payloadPlanID = strings.TrimSpace(firstNonEmptyString(sessionsV3MapString(checkpointContext, "plan_id"), payloadPlanID))
			payloadCheckpointID = strings.TrimSpace(firstNonEmptyString(sessionsV3MapString(checkpointContext, "checkpoint_id"), payloadCheckpointID))
			payloadAttemptID = strings.TrimSpace(sessionsV3MapString(checkpointContext, "attempt_id"))
			payloadParentSessionID = strings.TrimSpace(sessionsV3MapString(checkpointContext, "parent_session_id"))
		}
	}
	if freshContext {
		if payloadPlanID != "" {
			scope.PlanID = payloadPlanID
		}
		if payloadCheckpointID != "" {
			scope.CheckpointID = payloadCheckpointID
		}
		if payloadAttemptID != "" {
			scope.AttemptID = payloadAttemptID
		}
		if payloadParentSessionID != "" {
			scope.ParentSessionID = payloadParentSessionID
		}
		return scope
	}
	if scope.PlanID == "" {
		scope.PlanID = payloadPlanID
	}
	if scope.CheckpointID == "" {
		scope.CheckpointID = payloadCheckpointID
	}
	if scope.AttemptID == "" {
		scope.AttemptID = payloadAttemptID
	}
	if scope.ParentSessionID == "" {
		scope.ParentSessionID = payloadParentSessionID
	}
	return scope
}

func (e *sessionV3Executor) sessionV3ProviderCheckpointScope(job sessionV3ExecutorJob) sessionV3ProviderCheckpointScope {
	scope := sessionV3ProviderJobCheckpointScope(job)
	scope = sessionV3ProviderCheckpointScopeFromPayload(scope, e.sessionV3LatestCheckpointRunToolPayload(job))
	if e == nil || e.server == nil || e.server.sessions == nil {
		return scope
	}
	if active, ok, err := e.server.sessions.GetActivePlan(job.SessionID); err == nil && ok && active.Document != nil {
		if scope.PlanID == "" {
			scope.PlanID = strings.TrimSpace(active.ID)
		}
		if active.Document.ExecutionState != nil {
			state := active.Document.ExecutionState
			if scope.CheckpointID == "" && sessionV3PlanFreshContextBoundaryStatus(state.Status) {
				scope.CheckpointID = strings.TrimSpace(firstNonEmptyString(state.LastCheckpointID, active.Document.ActiveCheckpointID))
			}
			if scope.AttemptID == "" {
				scope.AttemptID = strings.TrimSpace(firstNonEmptyString(state.ActiveAttemptID, state.LastAttemptID))
			}
			if scope.ParentSessionID == "" {
				scope.ParentSessionID = strings.TrimSpace(state.ParentSessionID)
			}
		}
		if scope.CheckpointID != "" {
			for _, checkpoint := range active.Document.Checkpoints {
				if strings.TrimSpace(checkpoint.ID) != scope.CheckpointID {
					continue
				}
				if scope.AttemptID == "" {
					scope.AttemptID = strings.TrimSpace(checkpoint.AttemptID)
				}
				break
			}
		}
	}
	if scope.ParentSessionID == "" {
		scope.ParentSessionID = strings.TrimSpace(job.SessionID)
	}
	return scope
}

func sessionV3ProviderContextBranchID(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, checkpointScope sessionV3ProviderCheckpointScope) string {
	if checkpointID := strings.TrimSpace(firstNonEmptyString(checkpointScope.CheckpointID, job.CheckpointID)); checkpointID != "" {
		return provideriface.ShortProviderLineageKey("checkpoint", strings.TrimSpace(firstNonEmptyString(checkpointScope.PlanID, job.PlanID)), checkpointID, strings.TrimSpace(firstNonEmptyString(checkpointScope.AttemptID, job.AttemptID)), strings.TrimSpace(firstNonEmptyString(checkpointScope.ParentSessionID, job.ParentSessionID)), strings.TrimSpace(job.SessionID))
	}
	return provideriface.ShortProviderLineageKey("session", strings.TrimSpace(job.SessionID), strings.TrimSpace(resolved.Session.Mode))
}

type sessionV3ProviderLineageSnapshot struct {
	ProviderLineageID       string
	ContextBranchID         string
	BoundaryReason          string
	ProviderID              string
	Model                   string
	MessageID               string
	RunID                   string
	GlobalSeq               uint64
	StartMessageID          string
	StartRunID              string
	StartGlobalSeq          uint64
	HandoffSummaryMessageID string
	HandoffSummaryGlobalSeq uint64
}

func sessionV3ProviderBoundaryReason(job sessionV3ExecutorJob, previousLineageID, lineageID string, checkpointScope sessionV3ProviderCheckpointScope) string {
	if sessionV3ProviderCheckpointFreshContext(job, checkpointScope) {
		return "checkpoint_fresh_context"
	}
	if previousLineageID != "" && lineageID != "" && previousLineageID != lineageID {
		return "provider_model_runtime_handoff"
	}
	return "session_turn"
}

func sessionV3ProviderNativeContinuationAllowed(previousLineageID, lineageID string) bool {
	previousLineageID = strings.TrimSpace(previousLineageID)
	lineageID = strings.TrimSpace(lineageID)
	return previousLineageID != "" && lineageID != "" && previousLineageID == lineageID
}

func sessionV3ProviderForceFreshContext(job sessionV3ExecutorJob, previousLineageID, lineageID string, checkpointScope sessionV3ProviderCheckpointScope) bool {
	if sessionV3ProviderCheckpointFreshContext(job, checkpointScope) {
		return true
	}
	return !sessionV3ProviderNativeContinuationAllowed(previousLineageID, lineageID)
}

func sessionV3ProviderCheckpointFreshContext(job sessionV3ExecutorJob, checkpointScope sessionV3ProviderCheckpointScope) bool {
	return strings.TrimSpace(job.CheckpointID) != "" || checkpointScope.FreshContext
}

func sessionV3ProviderBoundaryReasonWithOverride(current, override string) string {
	current = strings.TrimSpace(current)
	override = strings.TrimSpace(override)
	if override == "" {
		return current
	}
	if current == "" || current == "session_turn" {
		return override
	}
	if strings.Contains(current, override) {
		return current
	}
	return current + "+" + override
}

func sessionV3ProviderTransportAffinityKey(job sessionV3ExecutorJob, providerID, model string) string {
	// Transport compatibility is deliberately independent of the execution
	// epoch and provider chain. Epoch boundaries rotate continuation/affinity
	// state while a healthy socket for the same root session/provider/model can
	// remain open.
	return sessionV3ProviderScopedKey("transport", provideriface.ShortProviderLineageKey(
		strings.TrimSpace(job.SessionID),
		strings.ToLower(strings.TrimSpace(providerID)),
		strings.TrimSpace(model),
	))
}

func sessionV3ProviderScopedKey(prefix, lineageID string) string {
	lineageID = strings.TrimSpace(lineageID)
	if lineageID == "" {
		return ""
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return lineageID
	}
	return prefix + "-" + lineageID
}

func firstNonZeroUint64(values ...uint64) uint64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func sessionV3ProviderToolsLineageHash(tools []provideriface.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	projection := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		projection = append(projection, map[string]any{
			"type":        strings.TrimSpace(tool.Type),
			"name":        strings.TrimSpace(tool.Name),
			"description": strings.TrimSpace(tool.Description),
			"parameters":  tool.Parameters,
		})
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return provideriface.ShortProviderLineageKey(fmt.Sprint(projection))
	}
	return provideriface.ShortProviderLineageKey(string(raw))
}

type sessionV3ProviderLoopResult struct {
	Response            provideriface.Response
	FinalContent        string
	FinalRequest        provideriface.Request
	DurableFlushCount   int
	FinalStreamID       string
	FinalStep           int
	FinalOffsetEnd      uint64
	TerminalPlanHandled bool
	StartNextCheckpoint bool
}

func (e *sessionV3Executor) runProviderToolLoop(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, runner provideriface.Runner, baseReq provideriface.Request, sink *sessionV3DurableProgressSink) (sessionV3ProviderLoopResult, error) {
	if runner == nil {
		return sessionV3ProviderLoopResult{}, errors.New("provider runner is not configured")
	}
	if sink == nil {
		return sessionV3ProviderLoopResult{}, errors.New("v3 durable progress sink is not configured")
	}
	input := append([]map[string]any(nil), baseReq.Input...)
	identicalCalls := sessionV3ProviderIdenticalToolCallTracker{}
	finalizingPlanTerminal := false
	runtimeContextAt := time.Now()
	for step := 1; ; step++ {
		toolsEnabled := len(baseReq.Tools) > 0 && !strings.EqualFold(strings.TrimSpace(baseReq.ToolChoice), "none")
		var toolInvoker provideriface.ToolInvoker
		if toolsEnabled {
			var invokerErr error
			toolInvoker, invokerErr = e.newSessionV3ProviderToolInvoker(resolved, job, step)
			if invokerErr != nil {
				return sessionV3ProviderLoopResult{}, invokerErr
			}
		}
		req := baseReq
		req.Input = append([]map[string]any(nil), input...)
		req.ToolInvoker = toolInvoker
		// Runtime context is part of the provider request properties and therefore
		// must remain byte-stable while this epoch's response chain is active.
		req = req.WithRuntimeContext(resolved.Preference.Provider, runtimeContextAt)
		e.recordSessionV3Diagnostic(job, "session.diagnostic.provider.request", "backend.provider", "request", map[string]any{
			"provider": strings.ToLower(strings.TrimSpace(resolved.Preference.Provider)),
			"model":    strings.TrimSpace(req.Model),
			"step":     step,
			"request":  sessionV3ProviderRequestDiagnostic(req),
		})
		streamState := newSessionV3ProviderStreamState(e, job, sink, step)
		providerStart := time.Now()
		var firstEventOnce sync.Once
		response, providerErr := runner.CreateResponseStreaming(ctx, req, func(event provideriface.StreamEvent) {
			firstEventOnce.Do(func() { pebblestore.ObserveExecutionEpochFirstEvent(providerStart) })
			streamState.Handle(event)
		})
		pebblestore.ObserveExecutionEpochProviderSend(providerStart)
		finishErr := streamState.FinishStep()
		stepText := sessionV3ProviderStepAssistantText(response, streamState.StreamedText())
		var ensureErr error
		if providerErr == nil && finishErr == nil && streamState.OffsetEnd() == 0 && strings.TrimSpace(stepText) != "" {
			ensureErr = streamState.EnsureResponseText(stepText)
		}
		barrierErr := sink.FlushBarrier(ctx)
		stepErr := firstNonNilErr(finishErr, ensureErr, sink.Err(), providerErr, barrierErr)
		e.recordSessionV3Diagnostic(job, "session.diagnostic.backend.flush", "backend.coalescer", fmt.Sprintf("step-%d-flush-summary", step), map[string]any{
			"step":             step,
			"streamed":         streamState.StreamedText(),
			"step_flush_count": sink.AssistantFlushCount(),
			"progress_error":   sessionV3DiagnosticErrorString(stepErr),
		})
		if stepErr != nil {
			return sessionV3ProviderLoopResult{}, stepErr
		}
		usageProviderID := strings.TrimSpace(runner.ID())
		if usageProviderID == "" {
			usageProviderID = strings.TrimSpace(resolved.Preference.Provider)
		}
		usageModel := strings.TrimSpace(response.Model)
		if usageModel == "" {
			usageModel = strings.TrimSpace(baseReq.Model)
		}
		if _, recorded, usageErr := e.recordProviderUsage(job, resolved, usageProviderID, usageModel, step, response.Usage, time.Now().UnixMilli()); usageErr != nil {
			return sessionV3ProviderLoopResult{}, usageErr
		} else if recorded {
			e.recordSessionV3Diagnostic(job, "session.diagnostic.provider.usage", "backend.provider", fmt.Sprintf("step-%d-usage-recorded", step), map[string]any{
				"step":     step,
				"provider": usageProviderID,
				"model":    usageModel,
				"usage":    response.Usage,
			})
		}
		if len(response.FunctionCalls) == 0 && !response.RestartTurn {
			if strings.TrimSpace(response.StopReason) == "" && strings.TrimSpace(stepText) != "" {
				response.StopReason = "stop"
			}
			return sessionV3ProviderLoopResult{Response: response, FinalContent: stepText, FinalRequest: baseReq, DurableFlushCount: sink.AssistantFlushCount(), FinalStreamID: streamState.StreamID(), FinalStep: streamState.Step(), FinalOffsetEnd: streamState.OffsetEnd()}, nil
		}
		if finalizingPlanTerminal && len(response.FunctionCalls) > 0 {
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider returned tool calls after terminal plan finalization started")
		}
		if finalizingPlanTerminal && response.RestartTurn {
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider requested restart after terminal plan finalization")
		}
		classification := sessionV3TerminalClassifier.Classify(TerminalClassifierInput{
			ProviderID:       runner.ID(),
			StopReason:       response.StopReason,
			HasFinalContent:  strings.TrimSpace(stepText) != "",
			HasFunctionCalls: len(response.FunctionCalls) > 0,
			RestartTurn:      response.RestartTurn,
		})
		if classification.Status != sessionruntime.RunIntentRunning {
			return sessionV3ProviderLoopResult{}, errors.New(classification.Reason)
		}
		if len(response.FunctionCalls) == 0 {
			if response.RestartTurn {
				refreshed, err := e.resolveSessionV3Runtime(job)
				if err != nil {
					return sessionV3ProviderLoopResult{}, err
				}
				resolved = refreshed
				refreshedRunner, err := e.sessionV3ProviderRunner(resolved)
				if err != nil {
					return sessionV3ProviderLoopResult{}, err
				}
				runner = refreshedRunner
				if _, ok := e.sessionV3LatestTerminalPlanToolPayload(job); ok {
					return sessionV3ProviderLoopResult{Response: provideriface.Response{StopReason: "stop"}, FinalRequest: baseReq, DurableFlushCount: sink.AssistantFlushCount(), FinalStreamID: streamState.StreamID(), FinalStep: streamState.Step(), FinalOffsetEnd: streamState.OffsetEnd(), TerminalPlanHandled: true}, nil
				}
				if e.sessionV3LatestCheckpointRunToolPayload(job) != nil {
					return sessionV3ProviderLoopResult{Response: provideriface.Response{StopReason: "stop"}, FinalRequest: baseReq, DurableFlushCount: sink.AssistantFlushCount(), FinalStreamID: streamState.StreamID(), FinalStep: streamState.Step(), FinalOffsetEnd: streamState.OffsetEnd(), TerminalPlanHandled: true, StartNextCheckpoint: true}, nil
				}
				input, err = e.sessionV3ProviderRestartInput(ctx, job, resolved, "")
				if err != nil {
					return sessionV3ProviderLoopResult{}, err
				}
				if len(input) == 0 {
					return sessionV3ProviderLoopResult{}, errors.New("v3 provider restart requested but continuation input is empty")
				}
				baseReq, err = e.sessionV3ProviderBaseRequest(job, resolved, input)
				if err != nil {
					return sessionV3ProviderLoopResult{}, err
				}
				if !strings.Contains(strings.TrimSpace(baseReq.BoundaryReason), "checkpoint_fresh_context") {
					baseReq.BoundaryReason = sessionV3ProviderBoundaryReasonWithOverride(baseReq.BoundaryReason, "restart_after_tool")
				}
				baseReq.StartNewChain = true
				baseReq.AllowContinuation = false
				baseReq.ResetTransport = true
				baseReq.NativeContinuationAllowed = false
				baseReq.ForceFreshProviderContext = true
				continue
			}
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider requested a tool-loop restart without tool calls")
		}
		if !toolsEnabled {
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider returned tool calls; tool-loop execution is not supported without resolved tools")
		}
		if toolInvoker == nil {
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider tool invoker is not configured")
		}
		if strings.TrimSpace(stepText) != "" {
			agentName := strings.TrimSpace(resolved.AgentProfile.Name)
			segment := sessionV3AssistantResponse{
				Content:                       stepText,
				AgentName:                     agentName,
				ResolvedAgentName:             agentName,
				ExecutorKind:                  "v3_provider",
				ProviderID:                    strings.TrimSpace(runner.ID()),
				Model:                         strings.TrimSpace(firstNonEmpty(response.Model, baseReq.Model)),
				ProviderLineageID:             baseReq.ProviderLineageID,
				ContextBranchID:               baseReq.ContextBranchID,
				ProviderCacheKey:              baseReq.ProviderCacheKey,
				SessionAffinityKey:            baseReq.SessionAffinityKey,
				TransportAffinityKey:          baseReq.TransportAffinityKey,
				EpochID:                       job.EpochID,
				BoundaryReason:                baseReq.BoundaryReason,
				PreviousProviderLineageID:     baseReq.PreviousProviderLineageID,
				PreviousProviderID:            baseReq.PreviousProviderID,
				PreviousModel:                 baseReq.PreviousModel,
				NewProviderID:                 baseReq.NewProviderID,
				NewModel:                      baseReq.NewModel,
				HandoffSummaryMessageID:       baseReq.HandoffSummaryMessageID,
				HandoffSummaryGlobalSeq:       baseReq.HandoffSummaryGlobalSeq,
				ProviderLineageStartMessageID: baseReq.ProviderLineageStartMessageID,
				ProviderLineageStartRunID:     baseReq.ProviderLineageStartRunID,
				ProviderLineageStartGlobalSeq: baseReq.ProviderLineageStartGlobalSeq,
				NativeContinuationAllowed:     baseReq.NativeContinuationAllowed,
				ForceFreshProviderContext:     baseReq.ForceFreshProviderContext,
				ProviderResponseID:            strings.TrimSpace(response.ID),
				StopReason:                    strings.TrimSpace(response.StopReason),
				Usage:                         response.Usage,
				StreamID:                      streamState.StreamID(),
				StreamStep:                    streamState.Step(),
				StreamOffsetEnd:               streamState.OffsetEnd(),
			}
			if segment.ProviderID == "" {
				segment.ProviderID = strings.TrimSpace(resolved.Preference.Provider)
			}
			if _, err := e.recordPreToolAssistantSegment(job, segment, step); err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			input = append(input, sessionsV3ProviderAssistantInputItem(stepText))
		}
		toolResults := make([]provideriface.ToolExecutionResult, 0, len(response.FunctionCalls))
		restartAfterTools := false
		for _, call := range response.FunctionCalls {
			if identicalCount, key := identicalCalls.Observe(call); identicalCount >= sessionV3ProviderIdenticalToolCallLimit {
				return sessionV3ProviderLoopResult{}, fmt.Errorf("v3 provider repeated identical tool call %d times: %s", sessionV3ProviderIdenticalToolCallLimit, key)
			}
			result, err := toolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: strings.TrimSpace(call.CallID), Name: strings.TrimSpace(call.Name), Arguments: strings.TrimSpace(call.Arguments), Metadata: cloneSessionsV3Metadata(call.Metadata)})
			if err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			if result.RestartTurn {
				restartAfterTools = true
			}
			toolResults = append(toolResults, result)
		}
		input = append(input, sessionsV3ProviderToolResultInputItems(response.FunctionCalls, toolResults)...)
		if len(input) == 0 {
			return sessionV3ProviderLoopResult{}, errors.New("v3 provider continuation input is empty after tool execution")
		}
		if _, ok := sessionsV3ProviderTerminalPlanToolResult(toolResults); ok {
			return sessionV3ProviderLoopResult{Response: provideriface.Response{StopReason: "stop"}, FinalRequest: baseReq, DurableFlushCount: sink.AssistantFlushCount(), FinalStreamID: streamState.StreamID(), FinalStep: streamState.Step(), FinalOffsetEnd: streamState.OffsetEnd(), TerminalPlanHandled: true}, nil
		}
		if sessionsV3ProviderCheckpointRunToolResult(toolResults) {
			return sessionV3ProviderLoopResult{Response: provideriface.Response{StopReason: "stop"}, FinalRequest: baseReq, DurableFlushCount: sink.AssistantFlushCount(), FinalStreamID: streamState.StreamID(), FinalStep: streamState.Step(), FinalOffsetEnd: streamState.OffsetEnd(), TerminalPlanHandled: true, StartNextCheckpoint: true}, nil
		}
		if restartAfterTools {
			restartCheckpointScope := sessionV3ProviderCheckpointScopeFromPayload(sessionV3ProviderJobCheckpointScope(job), sessionsV3DecodeToolPayload(strings.TrimSpace(firstNonEmpty(toolResults[len(toolResults)-1].Output, sessionsV3LatestFunctionCallOutput(input), toolResults[len(toolResults)-1].TextForModel))))
			refreshed, err := e.resolveSessionV3Runtime(job)
			if err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			resolved = refreshed
			refreshedRunner, err := e.sessionV3ProviderRunner(resolved)
			if err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			runner = refreshedRunner
			input, err = e.sessionV3ProviderRestartInput(ctx, job, resolved, strings.TrimSpace(firstNonEmpty(toolResults[len(toolResults)-1].Output, sessionsV3LatestFunctionCallOutput(input), toolResults[len(toolResults)-1].TextForModel)))
			if err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			if len(input) == 0 {
				return sessionV3ProviderLoopResult{}, errors.New("v3 provider restart requested but continuation input is empty after tool execution")
			}
			baseReq, err = e.sessionV3ProviderBaseRequestWithCheckpointScope(job, resolved, input, restartCheckpointScope)
			if err != nil {
				return sessionV3ProviderLoopResult{}, err
			}
			if !strings.Contains(strings.TrimSpace(baseReq.BoundaryReason), "checkpoint_fresh_context") {
				baseReq.BoundaryReason = sessionV3ProviderBoundaryReasonWithOverride(baseReq.BoundaryReason, "restart_after_tool")
			}
			baseReq.StartNewChain = true
			baseReq.AllowContinuation = false
			baseReq.ResetTransport = true
			baseReq.NativeContinuationAllowed = false
			baseReq.ForceFreshProviderContext = true
		} else {
			baseReq = sessionV3ProviderContinuationRequest(baseReq, runner)
		}
	}
}

func sessionV3ProviderContinuationRequest(req provideriface.Request, runner provideriface.Runner) provideriface.Request {
	if strings.TrimSpace(req.ExecutionEpochID) == "" || strings.TrimSpace(req.ProviderLineageID) == "" || runner == nil {
		return req
	}
	declared, ok := runner.(provideriface.ExecutionEpochLifecycleRunner)
	if !ok {
		return req
	}
	lifecycle := declared.ExecutionEpochLifecycle()
	if lifecycle.ContextMode != provideriface.ExecutionEpochContextResponsesChain {
		return req
	}
	// The first request in an epoch is deliberately fresh. Once it succeeds,
	// subsequent tool-loop requests continue that same provider chain. A later
	// restart or epoch transition rebuilds the base request and makes it fresh
	// again before this promotion can happen.
	req.StartNewChain = false
	req.AllowContinuation = true
	req.ResetTransport = false
	req.NativeContinuationAllowed = true
	req.ForceFreshProviderContext = false
	return req
}

type sessionV3ProviderTerminalPlanResult struct {
	Action           string
	NextAction       string
	CheckpointID     string
	NextCheckpointID string
	PlanID           string
	PlanTitle        string
	Summary          string
}

func sessionsV3ProviderCheckpointRunToolResult(results []provideriface.ToolExecutionResult) bool {
	for i := len(results) - 1; i >= 0; i-- {
		result := results[i]
		toolName := strings.TrimSpace(result.Name)
		if !strings.EqualFold(toolName, "plan_manage") && !strings.EqualFold(toolName, "exit_plan_mode") {
			continue
		}
		payload := sessionsV3DecodeToolPayload(strings.TrimSpace(firstNonEmpty(result.Output, result.TextForModel, result.Error)))
		if strings.EqualFold(strings.TrimSpace(sessionsV3MapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
			return true
		}
	}
	return false
}

func sessionsV3ProviderTerminalPlanToolResult(results []provideriface.ToolExecutionResult) (sessionV3ProviderTerminalPlanResult, bool) {
	for i := len(results) - 1; i >= 0; i-- {
		result := results[i]
		if !strings.EqualFold(strings.TrimSpace(result.Name), "plan_manage") {
			continue
		}
		payload := sessionsV3DecodeToolPayload(strings.TrimSpace(firstNonEmpty(result.Output, result.TextForModel, result.Error)))
		if terminal, ok := sessionsV3ProviderTerminalPlanPayload(payload); ok {
			return terminal, true
		}
	}
	return sessionV3ProviderTerminalPlanResult{}, false
}

func sessionsV3ProviderTerminalPlanPayload(payload map[string]any) (sessionV3ProviderTerminalPlanResult, bool) {
	if payload == nil {
		return sessionV3ProviderTerminalPlanResult{}, false
	}
	nextAction := strings.TrimSpace(sessionsV3MapString(payload, "next_action"))
	if !sessionsV3ProviderTerminalPlanNextAction(nextAction) {
		return sessionV3ProviderTerminalPlanResult{}, false
	}
	terminal := sessionV3ProviderTerminalPlanResult{
		Action:           strings.TrimSpace(sessionsV3MapString(payload, "action")),
		NextAction:       nextAction,
		CheckpointID:     strings.TrimSpace(sessionsV3MapString(payload, "checkpoint_id")),
		NextCheckpointID: strings.TrimSpace(sessionsV3MapString(payload, "next_checkpoint_id")),
		Summary:          strings.TrimSpace(sessionsV3MapString(payload, "summary")),
	}
	if plan, ok := payload["plan"].(map[string]any); ok {
		terminal.PlanID = strings.TrimSpace(sessionsV3MapString(plan, "id"))
		terminal.PlanTitle = strings.TrimSpace(sessionsV3MapString(plan, "title"))
	}
	if terminal.CheckpointID == "" {
		terminal.CheckpointID = terminal.NextCheckpointID
	}
	return terminal, true
}

func sessionsV3ProviderTerminalPlanNextAction(nextAction string) bool {
	switch strings.ToLower(strings.TrimSpace(nextAction)) {
	case "await_review", "plan_complete", "stopped":
		return true
	default:
		return false
	}
}

type sessionV3ProviderIdenticalToolCallTracker struct {
	lastKey string
	count   int
}

func (t *sessionV3ProviderIdenticalToolCallTracker) Observe(call provideriface.FunctionCall) (int, string) {
	key := sessionV3ProviderCanonicalToolCallKey(call)
	if key == t.lastKey {
		t.count++
	} else {
		t.lastKey = key
		t.count = 1
	}
	return t.count, key
}

func sessionV3ProviderCanonicalToolCallKey(call provideriface.FunctionCall) string {
	name := strings.TrimSpace(call.Name)
	args := strings.TrimSpace(call.Arguments)
	canonicalArgs := args
	if args != "" {
		var decoded any
		if err := json.Unmarshal([]byte(args), &decoded); err == nil {
			if encoded, err := json.Marshal(decoded); err == nil {
				canonicalArgs = string(encoded)
			}
		}
	}
	return name + ":" + canonicalArgs
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
		Emit:                 e.emitSessionV3ProviderToolEvent(job),
		ApplySessionMutation: e.applySessionV3ProviderToolMutation(job),
		ProviderManagedV3:    true,
		AgentProfile:         resolved.AgentProfile,
	})
	if invoker == nil {
		return nil, errors.New("provider-managed tool invoker is not configured")
	}
	return invoker, nil
}

func (e *sessionV3Executor) applySessionV3ProviderToolMutation(job sessionV3ExecutorJob) func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
	return func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if sessionV3ProviderToolTerminalEvent(input.EventType) && e.isRunCanceled(job) {
			return sessionruntime.SessionMutationResult{}, context.Canceled
		}
		return e.server.applySessionV3PrimaryMutation(input)
	}
}

func sessionV3ProviderToolTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "session.tool.completed", "session.tool.failed", "session.tool.cancelled", "session.tool.canceled":
		return true
	default:
		return false
	}
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
		case runruntime.StreamEventPermissionReq:
			eventType = "permission.requested"
		case runruntime.StreamEventPermissionUpdate:
			eventType = "permission.updated"
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
	now := time.Now().UnixMilli()
	payload := map[string]any{}
	if eventType == "permission.updated" {
		payload = sessionV3PermissionUpdatedPayload(strings.TrimSpace(firstNonEmpty(event.SessionID, job.SessionID)), strings.TrimSpace(job.RunID), step, toolName, callID, strings.TrimSpace(event.Arguments), event.Permission)
	} else {
		payload = map[string]any{
			"run_id":           strings.TrimSpace(job.RunID),
			"step":             step,
			"step_id":          stepID,
			"tool_name":        toolName,
			"call_id":          callID,
			"tool_instance_id": toolInstanceID,
			"recorded_at":      now,
		}
		if eventType == "permission.requested" {
			payload["type"] = eventType
			payload["session_id"] = strings.TrimSpace(firstNonEmpty(event.SessionID, job.SessionID))
			if event.Permission != nil {
				payload["permission"] = event.Permission
			}
		}
		if args := strings.TrimSpace(event.Arguments); args != "" {
			payload["arguments"] = args
		}
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
	if status := sessionV3ProviderToolEventStatus(eventType); status != "" {
		payload["status"] = status
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
	intent := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, now)
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

func (e *sessionV3Executor) sessionV3ProviderContinuationInput(job sessionV3ExecutorJob, nativeReplayAllowed bool) []map[string]any {
	messages, err := e.sessionV3ProviderContextMessages(job)
	if err != nil || len(messages) == 0 {
		return nil
	}
	return sessionsV3ProviderInputWithOptions(messages, sessionsV3ProviderInputOptions{SuppressNativeReplay: !nativeReplayAllowed})
}

func sessionsV3DecodeToolPayload(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func (e *sessionV3Executor) sessionV3LatestCheckpointRunToolPayload(job sessionV3ExecutorJob) map[string]any {
	payload := e.sessionV3LatestPlanManageToolPayload(job)
	if payload == nil || !strings.EqualFold(strings.TrimSpace(sessionsV3MapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
		return nil
	}
	return payload
}

func (e *sessionV3Executor) sessionV3LatestTerminalPlanToolPayload(job sessionV3ExecutorJob) (sessionV3ProviderTerminalPlanResult, bool) {
	payload := e.sessionV3LatestPlanManageToolPayload(job)
	return sessionsV3ProviderTerminalPlanPayload(payload)
}

func (e *sessionV3Executor) sessionV3LatestPlanManageToolPayload(job sessionV3ExecutorJob) map[string]any {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil
	}
	messages, err := e.server.sessions.ListSessionMessageTail(job.SessionID, 64)
	if err != nil || len(messages) == 0 {
		return nil
	}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if strings.TrimSpace(message.Role) != "tool" {
			continue
		}
		payload := sessionsV3DecodeToolPayload(message.Content)
		if record, ok := sessionsV3DecodeProviderToolResultRecord(message.Content); ok {
			toolName := strings.TrimSpace(record.ToolName)
			if !strings.EqualFold(toolName, "plan_manage") && !strings.EqualFold(toolName, "exit_plan_mode") {
				continue
			}
			payload = sessionsV3DecodeToolPayload(strings.TrimSpace(record.CompletedOutput))
			if payload == nil {
				payload = sessionsV3DecodeToolPayload(strings.TrimSpace(record.Output))
			}
		}
		if payload == nil {
			continue
		}
		return payload
	}
	return nil
}

func sessionsV3MapString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprint(value)
}

func (e *sessionV3Executor) sessionV3ProviderRestartInput(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, toolOutput string) ([]map[string]any, error) {
	if input, ok, err := e.sessionV3ProviderCheckpointRestartInput(ctx, job, resolved, toolOutput); err != nil || ok {
		return input, err
	}
	input := e.sessionV3ProviderContinuationInput(job, false)
	if len(input) == 0 {
		return nil, errors.New("v3 provider restart requested but continuation input is empty")
	}
	return input, nil
}

func (e *sessionV3Executor) sessionV3ProviderCheckpointRestartInput(ctx context.Context, job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, toolOutput string) ([]map[string]any, bool, error) {
	payload := sessionsV3DecodeToolPayload(strings.TrimSpace(toolOutput))
	if payload == nil || !strings.EqualFold(strings.TrimSpace(sessionsV3MapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
		payload = e.sessionV3LatestCheckpointRunToolPayload(job)
	}
	if payload == nil || !strings.EqualFold(strings.TrimSpace(sessionsV3MapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
		if strings.TrimSpace(job.CheckpointID) == "" && strings.TrimSpace(job.PlanID) == "" {
			return nil, false, nil
		}
	}
	scope := sessionV3ProviderCheckpointScopeFromPayload(sessionV3ProviderJobCheckpointScope(job), payload)
	checkpointID := strings.TrimSpace(scope.CheckpointID)
	planID := strings.TrimSpace(scope.PlanID)
	attemptID := strings.TrimSpace(scope.AttemptID)
	parentSessionID := strings.TrimSpace(scope.ParentSessionID)
	if checkpointID == "" {
		return nil, true, errors.New("checkpoint restart requested without checkpoint_id")
	}
	if planID == "" {
		planID = "active"
	}
	if e == nil || e.server == nil || e.server.runner == nil {
		return nil, true, errors.New("v3 checkpoint restart requires run service")
	}
	builder, ok := e.server.runner.(interface {
		BuildPlanCheckpointRunInput(string, string, runruntime.RunRequest, runruntime.RunStartMeta) ([]map[string]any, bool, error)
	})
	if !ok || builder == nil {
		return nil, true, errors.New("v3 checkpoint restart requires checkpoint input builder")
	}
	if parentSessionID == "" {
		parentSessionID = job.SessionID
	}
	checkpointInput, ok, err := builder.BuildPlanCheckpointRunInput(job.SessionID, job.RunID, runruntime.RunRequest{PlanCheckpointContext: &runruntime.RunPlanCheckpointContext{PlanID: planID, CheckpointID: checkpointID, AttemptID: attemptID, ParentSessionID: parentSessionID}}, runruntime.RunStartMeta{RunID: job.RunID, Principal: job.Principal, ApplySessionMutation: e.server.applySessionV3PrimaryMutation})
	if err != nil {
		return nil, true, err
	}
	if !ok || len(checkpointInput) == 0 {
		return nil, true, errors.New("checkpoint restart requested but checkpoint input is empty")
	}
	return checkpointInput, true, nil
}

func (e *sessionV3Executor) sessionV3ProviderContextMessages(job sessionV3ExecutorJob) ([]pebblestore.MessageSnapshot, error) {
	recoveryStart := time.Now()
	defer pebblestore.ObserveExecutionEpochRecovery(recoveryStart)
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil, errors.New("v3 executor is not configured")
	}
	epochID := strings.TrimSpace(job.EpochID)
	if epochID == "" {
		if intent, ok, err := e.server.sessions.GetV3SessionRunIntent(job.SessionID, job.RunID); err != nil {
			return nil, err
		} else if ok {
			epochID = strings.TrimSpace(intent.EpochID)
		}
	}
	if epochID == "" {
		return nil, errors.New("v3 executor run has no execution epoch")
	}
	epoch, messages, err := e.server.sessions.ListExecutionEpochMessages(job.SessionID, epochID, 0)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(epoch.Boundary.Reason), "post_checkpoint_followup") {
		if activePlan, planOK, planErr := e.server.sessions.GetActivePlan(job.SessionID); planErr != nil {
			return nil, planErr
		} else if planOK {
			summary := sessionV3PlanFreshContextBoundarySummary(activePlan, epoch.Boundary.CheckpointID, "")
			if summary != "" {
				content, metadata := runruntime.BuildProviderContextBoundaryMessage(summary, runruntime.ContextCompactionOriginPlanFreshContext, 1, &activePlan)
				if metadata == nil {
					metadata = map[string]any{}
				}
				metadata["source"] = "execution_epoch_boundary"
				metadata["epoch_id"] = epoch.EpochID
				metadata["checkpoint_id"] = epoch.Boundary.CheckpointID
				metadata["synthetic"] = true
				messages = append([]pebblestore.MessageSnapshot{{SessionID: job.SessionID, Role: "system", Content: content, Metadata: metadata}}, messages...)
			}
		}
	}
	return runruntime.CompactMessagesForProviderContext(messages, 500), nil
}

func (e *sessionV3Executor) sessionV3ProviderPlanFreshContextBoundary(job sessionV3ExecutorJob, messages []pebblestore.MessageSnapshot) (*pebblestore.MessageSnapshot, int, error) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil, 0, errors.New("v3 executor is not configured")
	}
	activePlan, ok, err := e.server.sessions.GetActivePlan(job.SessionID)
	if err != nil {
		return nil, 0, err
	}
	if !ok || activePlan.Document == nil || activePlan.Document.ExecutionState == nil {
		return nil, 0, nil
	}
	doc := activePlan.Document
	if !strings.EqualFold(strings.TrimSpace(doc.ExecutionPolicy.Mode), sessionruntime.PlanExecutionPolicyModeAutomatic) {
		return nil, 0, nil
	}
	state := doc.ExecutionState
	if !sessionV3PlanFreshContextBoundaryStatus(state.Status) {
		return nil, 0, nil
	}
	lastRunID := strings.TrimSpace(state.CurrentRunID)
	if lastRunID == "" {
		return nil, 0, nil
	}
	lastCheckpointID := strings.TrimSpace(state.LastCheckpointID)
	if lastCheckpointID == "" {
		lastCheckpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	}
	lastRunMessage := -1
	for i := range messages {
		if strings.TrimSpace(sessionV3MetadataString(messages[i].Metadata, "run_id")) == lastRunID {
			lastRunMessage = i
		}
	}
	if lastRunMessage < 0 || lastRunMessage >= len(messages)-1 {
		return nil, 0, nil
	}
	summary := sessionV3PlanFreshContextBoundarySummary(activePlan, lastCheckpointID, lastRunID)
	if summary == "" {
		return nil, 0, nil
	}
	content, metadata := runruntime.BuildProviderContextBoundaryMessage(summary, runruntime.ContextCompactionOriginPlanFreshContext, sessionV3NextSyntheticCompactionIndex(messages), &activePlan)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["source"] = "plan_fresh_context_boundary"
	metadata["run_id"] = lastRunID
	metadata["checkpoint_id"] = lastCheckpointID
	metadata["synthetic"] = true
	return &pebblestore.MessageSnapshot{SessionID: job.SessionID, Role: "system", Content: content, Metadata: metadata}, lastRunMessage + 1, nil
}

func sessionV3PlanFreshContextBoundaryStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case sessionruntime.PlanExecutionStateCompleted, sessionruntime.PlanExecutionStateWaitingReview, sessionruntime.PlanExecutionStateBlocked, sessionruntime.PlanExecutionStateFailed:
		return true
	default:
		return false
	}
}

func sessionV3PlanFreshContextBoundarySummary(plan pebblestore.SessionPlanSnapshot, checkpointID, runID string) string {
	if plan.Document == nil {
		return ""
	}
	doc := plan.Document
	lines := []string{"Automatic checkpoint fresh-context boundary."}
	if title := strings.TrimSpace(firstNonEmptyString(doc.Title, plan.Title)); title != "" {
		lines = append(lines, "Plan: "+title)
	}
	if checkpointID != "" {
		lines = append(lines, "Checkpoint: "+checkpointID)
	}
	if runID != "" {
		lines = append(lines, "Run: "+runID)
	}
	if doc.ExecutionState != nil && strings.TrimSpace(doc.ExecutionState.Status) != "" {
		lines = append(lines, "Execution status: "+strings.TrimSpace(doc.ExecutionState.Status))
	}
	for _, checkpoint := range doc.Checkpoints {
		if checkpointID != "" && strings.TrimSpace(checkpoint.ID) != checkpointID {
			continue
		}
		if title := strings.TrimSpace(checkpoint.Title); title != "" {
			lines = append(lines, "Checkpoint title: "+title)
		}
		if report := strings.TrimSpace(checkpoint.Report); report != "" {
			lines = append(lines, "Report: "+report)
		}
		if result := strings.TrimSpace(checkpoint.Result); result != "" {
			lines = append(lines, "Result: "+result)
		}
		if len(checkpoint.ChangedFiles) > 0 {
			lines = append(lines, "Changed files: "+strings.Join(trimStrings(checkpoint.ChangedFiles), ", "))
		}
		if len(checkpoint.Validation) > 0 {
			lines = append(lines, "Validation: "+strings.Join(trimStrings(checkpoint.Validation), "; "))
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func sessionV3NextSyntheticCompactionIndex(messages []pebblestore.MessageSnapshot) int {
	latest := 1
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "system") || !strings.HasPrefix(strings.TrimSpace(message.Content), "[context-compact]") {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(message.Content, "\n", 2)[0])
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "index=") {
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimPrefix(field, "index="))
			if err == nil && parsed > latest {
				latest = parsed
			}
		}
	}
	return latest + 1
}

func trimStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
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
	agentProfile, err := sessionV3AgentProfileFromMetadata(session.Metadata)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	agentProfile, err = e.resolveSessionV3CurrentAgentToolContract(session.AccountScopeID, session.Metadata, agentProfile)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if !agentProfile.Enabled {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("agent %q is disabled", strings.TrimSpace(agentProfile.Name))
	}
	if strings.TrimSpace(agentProfile.Name) == "" {
		return sessionV3ResolvedRuntime{}, errors.New("stored v3 agent profile is missing name")
	}
	if strings.TrimSpace(agentProfile.Mode) == "" {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("stored v3 agent profile %q is missing mode", strings.TrimSpace(agentProfile.Name))
	}
	if strings.TrimSpace(agentProfile.RuntimeMode) == "" {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("stored v3 agent profile %q is missing runtime_mode", strings.TrimSpace(agentProfile.Name))
	}
	if agentProfile.ExitPlanModeEnabled == nil {
		return sessionV3ResolvedRuntime{}, fmt.Errorf("stored v3 agent profile %q is missing exit_plan_mode_enabled", strings.TrimSpace(agentProfile.Name))
	}
	compiler, ok := e.server.runner.(sessionsV3StoredAgentToolContractCompiler)
	if !ok || compiler == nil {
		return sessionV3ResolvedRuntime{}, errors.New("v3 tool contract compiler is not configured")
	}
	if _, _, err := compiler.CompileStoredV3AgentToolContract(session.AccountScopeID, agentProfile); err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	pref, contextWindow, err := e.resolveSessionV3ProviderPreference(applySessionV3AgentPreferenceOverridesForMode(session.Preference, agentProfile, session.Mode))
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
	}
	if strings.TrimSpace(pref.Provider) == "" || strings.TrimSpace(pref.Model) == "" {
		return sessionV3ResolvedRuntime{}, errors.New("resolved v3 provider/model is empty")
	}
	catalogRecord, err := e.sessionV3ModelCatalogRecord(pref)
	if err != nil {
		return sessionV3ResolvedRuntime{}, err
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
	return sessionV3ResolvedRuntime{Session: session, AgentProfile: agentProfile, Preference: pref, ContextWindow: contextWindow, ModelCatalog: catalogRecord, Scope: scope, Instructions: instructions, Tools: tools, ToolChoice: toolChoice}, nil
}

func (e *sessionV3Executor) resolveSessionV3CurrentAgentToolContract(accountScopeID string, metadata map[string]any, snapshot pebblestore.AgentProfile) (pebblestore.AgentProfile, error) {
	if e == nil || e.server == nil || e.server.agents == nil {
		return pebblestore.AgentProfile{}, errors.New("agent service is not configured")
	}
	name := strings.TrimSpace(snapshot.Name)
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("stored v3 agent profile is missing name")
	}

	kind := strings.ToLower(strings.TrimSpace(sessionsV3MetadataString(metadata, "system_sidechat_kind")))
	systemSidechat, _ := metadata["system_sidechat"].(bool)
	lineageSystemSidechat := strings.EqualFold(strings.TrimSpace(sessionsV3MetadataString(metadata, "lineage_kind")), "system_sidechat")
	hasSystemMetadata := systemSidechat || lineageSystemSidechat || kind != "" || strings.TrimSpace(sessionsV3MetadataString(metadata, "system_agent_id")) != ""
	if hasSystemMetadata {
		systemSession, _ := metadata["system_session"].(bool)
		if !systemSidechat || !systemSession || !lineageSystemSidechat || kind == "" || strings.TrimSpace(sessionsV3MetadataString(metadata, "parent_session_id")) == "" {
			return pebblestore.AgentProfile{}, errors.New("invalid system sidechat metadata")
		}
		registry, err := e.server.agents.SystemAgentRegistry()
		if err != nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("system agent registry: %w", err)
		}
		definition, ok := registry.DefinitionBySidechatKind(kind)
		if !ok {
			return pebblestore.AgentProfile{}, fmt.Errorf("unknown system sidechat kind %q", kind)
		}
		if !definition.RequiresSidechatMetadata {
			return pebblestore.AgentProfile{}, fmt.Errorf("system agent %q is not authorized for system sidechat metadata", definition.ID)
		}
		for key, value := range map[string]string{
			"agent profile":       name,
			"agent_name":          sessionsV3MetadataString(metadata, "agent_name"),
			"resolved_agent_name": sessionsV3MetadataString(metadata, "resolved_agent_name"),
			"system_agent_id":     sessionsV3MetadataString(metadata, "system_agent_id"),
		} {
			value = strings.TrimSpace(value)
			if value != "" && !strings.EqualFold(value, definition.ID) {
				return pebblestore.AgentProfile{}, fmt.Errorf("system sidechat metadata mismatch: kind %q requires agent %q, but %s is %q", kind, definition.ID, key, value)
			}
		}
		return e.server.agents.ReconcileSystemAgentSnapshot(definition.ID, snapshot)
	}
	if agentruntime.IsReservedSidechatAgentName(name) {
		return pebblestore.AgentProfile{}, fmt.Errorf("reserved system agent %q requires authenticated system sidechat metadata", name)
	}
	if systemID, ok := agentruntime.CanonicalSystemAgentID(name); ok {
		return e.server.agents.ReconcileSystemAgentSnapshot(systemID, snapshot)
	}
	if strings.HasPrefix(strings.ToLower(name), "system-") {
		return pebblestore.AgentProfile{}, fmt.Errorf("unknown reserved system agent %q", name)
	}

	accountScopeID = strings.TrimSpace(accountScopeID)
	// Built-in tool additions are backfilled per account. Existing accounts are
	// not covered by the daemon's legacy unscoped startup reconciliation, so do
	// the account-scoped reconciliation before resolving the mutable contract.
	if err := e.server.agents.EnsureDefaultsForAccount(accountScopeID); err != nil {
		return pebblestore.AgentProfile{}, fmt.Errorf("reconcile agent defaults for account: %w", err)
	}
	current, ok, err := e.server.agents.GetProfileForAccount(accountScopeID, name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q not found", name)
	}
	if current.ToolContract == nil {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q tool_contract is not configured", name)
	}
	// Session metadata remains authoritative for the selected agent and its
	// prompt/model/runtime snapshot. Tool access is user-owned mutable settings,
	// so each V3 run overlays only the current saved contract onto that snapshot.
	snapshot.ToolContract = pebblestore.CloneAgentToolContract(current.ToolContract)
	return snapshot, nil
}

func (e *sessionV3Executor) sessionV3ModelCatalogRecord(pref pebblestore.ModelPreference) (any, error) {
	if e == nil || e.server == nil || e.server.model == nil {
		return nil, nil
	}
	lookup, err := e.server.model.GetCatalog(pref.Provider, pref.Model)
	if err != nil {
		if strings.Contains(err.Error(), "model catalog is not configured") {
			return nil, nil
		}
		return nil, err
	}
	if !lookup.Found {
		return nil, nil
	}
	return lookup.Record, nil
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
		return hydrator.ComposeRuntimeInstructions(scope, mode, e.server.permissionBypassForAccount(scope.Principal.AccountScopeID), agentProfile, "")
	}
	hydrator := runruntime.NewService(e.server.sessions, e.server.model, e.server.providers, nil, nil, e.server.agents, e.server.discovery, e.server.events)
	return hydrator.ComposeRuntimeInstructions(scope, mode, e.server.permissionBypassForAccount(scope.Principal.AccountScopeID), agentProfile, "")
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
	compiler, ok := e.server.runner.(sessionsV3StoredAgentToolContractCompiler)
	if !ok || compiler == nil {
		return nil, errors.New("v3 tool contract compiler is not configured")
	}
	contract, disabled, err := compiler.CompileStoredV3AgentToolContract(accountScopeID, agentProfile)
	if err != nil {
		return nil, err
	}
	definitions := sessionsV3ProviderToolDefinitions(e.server.runner.ListAgentToolDefinitionsForAccount(accountScopeID))
	if len(definitions) == 0 {
		return nil, nil
	}
	allowed := make(map[string]bool, len(contract.Tools))
	for name, state := range contract.Tools {
		name = agentToolCanonicalName(name)
		if name != "" && state.Enabled {
			allowed[name] = true
		}
	}
	disabledCanonical := make(map[string]bool, len(disabled))
	for name, isDisabled := range disabled {
		name = agentToolCanonicalName(name)
		if name != "" && isDisabled {
			disabledCanonical[name] = true
		}
	}
	filtered := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		name := agentToolCanonicalName(definition.Name)
		if name == "" || disabledCanonical[name] || !allowed[name] {
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
		pref.ServiceTier = modelruntime.NormalizeServiceTierForProvider(pref.Provider, pref.ServiceTier)
		if pref.Provider == "codex" {
			pref.ContextMode = codexruntime.NormalizeContextMode(pref.ContextMode)
		} else {
			pref.ContextMode = ""
		}
		return pref, 0, nil
	}
	resolved, err := e.server.model.ResolvePreference(pref)
	if err != nil {
		pref.ServiceTier = modelruntime.NormalizeServiceTierForProvider(pref.Provider, pref.ServiceTier)
		if pref.Provider == "codex" {
			pref.ContextMode = codexruntime.NormalizeContextMode(pref.ContextMode)
		} else {
			pref.ContextMode = ""
		}
		return pref, 0, nil
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
	return sessionsV3ProviderInputWithOptions(messages, sessionsV3ProviderInputOptions{})
}

type sessionsV3ProviderInputOptions struct {
	LineageID            string
	Bounded              bool
	SuppressNativeReplay bool
}

func sessionsV3ProviderInputForLineage(messages []pebblestore.MessageSnapshot, lineageID string, nativeReplayAllowed bool) []map[string]any {
	return sessionsV3ProviderInputWithOptions(messages, sessionsV3ProviderInputOptions{LineageID: lineageID, Bounded: true, SuppressNativeReplay: !nativeReplayAllowed})
}

func sessionsV3ProviderInputWithOptions(messages []pebblestore.MessageSnapshot, options sessionsV3ProviderInputOptions) []map[string]any {
	messages = sessionsV3MessagesForProviderLineage(messages, options.LineageID, options.Bounded)
	input := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			if !options.SuppressNativeReplay {
				if nativeItems := sessionsV3ProviderNativeInputItems(message.Metadata); len(nativeItems) > 0 {
					input = append(input, nativeItems...)
					continue
				}
			}
			input = append(input, sessionsV3ProviderAssistantInputItem(content))
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

func sessionsV3MessagesForProviderLineage(messages []pebblestore.MessageSnapshot, _ string, _ bool) []pebblestore.MessageSnapshot {
	// Messages are already bounded by the durable execution epoch. Do not infer
	// a second boundary from transcript text or message metadata.
	return messages
}

func sessionsV3ProviderAssistantInputItem(content string) map[string]any {
	return map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": content}}}
}

func sessionV3ProviderNativeOutputItems(providerID string, raw map[string]any) []any {
	if !sessionV3ProviderSupportsNativeOutputReplay(providerID) || len(raw) == 0 {
		return nil
	}
	if response, ok := raw["response"].(map[string]any); ok {
		if output := cloneSessionsV3ProviderItemSlice(response["output"]); len(output) > 0 {
			return output
		}
	}
	return cloneSessionsV3ProviderItemSlice(raw["output"])
}

func sessionV3ProviderSupportsNativeOutputReplay(providerID string) bool {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	return providerID == "codex" || strings.HasPrefix(providerID, "codex_")
}

func sessionsV3ProviderNativeInputItems(metadata map[string]any) []map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	if !sessionV3ProviderSupportsNativeOutputReplay(fmt.Sprint(metadata["provider"])) {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(fmt.Sprint(metadata["provider_output_format"])), "responses_api") {
		return nil
	}
	raw := cloneSessionsV3ProviderItemSlice(metadata["provider_output_items"])
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		itemMap, ok := item.(map[string]any)
		if !ok || len(itemMap) == 0 {
			return nil
		}
		sanitized, ok := sessionsV3ProviderNativeRequestInputItem(itemMap)
		if !ok {
			return nil
		}
		out = append(out, sanitized)
	}
	return out
}

func sessionsV3ProviderNativeRequestInputItem(item map[string]any) (map[string]any, bool) {
	itemType := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["type"])))
	if itemType == "" {
		return nil, false
	}
	switch itemType {
	case "message":
		out := map[string]any{"type": "message"}
		copySessionsV3ProviderNativeStringField(out, item, "id")
		copySessionsV3ProviderNativeStringField(out, item, "status")
		copySessionsV3ProviderNativeStringField(out, item, "role")
		content := sessionsV3ProviderNativeMessageContent(item["content"])
		if len(content) == 0 {
			return nil, false
		}
		out["content"] = content
		return out, true
	case "function_call":
		out := map[string]any{"type": "function_call"}
		copySessionsV3ProviderNativeStringField(out, item, "id")
		copySessionsV3ProviderNativeStringField(out, item, "status")
		copySessionsV3ProviderNativeStringField(out, item, "call_id")
		copySessionsV3ProviderNativeStringField(out, item, "name")
		copySessionsV3ProviderNativeStringField(out, item, "arguments")
		return out, true
	case "function_call_output":
		out := map[string]any{"type": "function_call_output"}
		copySessionsV3ProviderNativeStringField(out, item, "id")
		copySessionsV3ProviderNativeStringField(out, item, "status")
		copySessionsV3ProviderNativeStringField(out, item, "call_id")
		copySessionsV3ProviderNativeStringField(out, item, "output")
		return out, true
	case "reasoning":
		out := map[string]any{"type": "reasoning"}
		copySessionsV3ProviderNativeStringField(out, item, "id")
		copySessionsV3ProviderNativeStringField(out, item, "status")
		out["summary"] = sessionsV3ProviderNativeReasoningSummary(item["summary"])
		if content, ok := sessionsV3ProviderNativeReasoningContent(item["content"]); ok {
			out["content"] = content
		} else {
			out["content"] = nil
		}
		copySessionsV3ProviderNativeStringField(out, item, "encrypted_content")
		if len(out) <= 1 {
			return nil, false
		}
		return out, true
	default:
		sanitized, ok := sessionsV3ProviderNativeStripResponseFields(item).(map[string]any)
		return sanitized, ok && len(sanitized) > 0
	}
}

func sessionsV3ProviderNativeReasoningSummary(value any) []any {
	raw := cloneSessionsV3ProviderItemSlice(value)
	if len(raw) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		itemMap, ok := item.(map[string]any)
		if !ok || len(itemMap) == 0 {
			continue
		}
		if contentType := strings.TrimSpace(fmt.Sprint(itemMap["type"])); contentType != "" && contentType != "summary_text" {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(itemMap["text"]))
		if text == "" {
			continue
		}
		out = append(out, map[string]any{"type": "summary_text", "text": text})
	}
	return out
}

func sessionsV3ProviderNativeReasoningContent(value any) (any, bool) {
	raw := cloneSessionsV3ProviderItemSlice(value)
	if len(raw) == 0 {
		return nil, false
	}
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		itemMap, ok := item.(map[string]any)
		if !ok || len(itemMap) == 0 {
			continue
		}
		contentType := strings.TrimSpace(fmt.Sprint(itemMap["type"]))
		switch contentType {
		case "reasoning_text", "text":
		default:
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(itemMap["text"]))
		if text == "" {
			continue
		}
		out = append(out, map[string]any{"type": contentType, "text": text})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func sessionsV3ProviderNativeMessageContent(value any) []map[string]any {
	raw := cloneSessionsV3ProviderItemSlice(value)
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		itemMap, ok := item.(map[string]any)
		if !ok || len(itemMap) == 0 {
			return nil
		}
		contentType := strings.TrimSpace(fmt.Sprint(itemMap["type"]))
		if contentType == "" {
			return nil
		}
		content := map[string]any{"type": contentType}
		copySessionsV3ProviderNativeStringField(content, itemMap, "text")
		copySessionsV3ProviderNativeStringField(content, itemMap, "refusal")
		if len(content) <= 1 {
			return nil
		}
		out = append(out, content)
	}
	return out
}

func copySessionsV3ProviderNativeStringField(dst, src map[string]any, key string) {
	value, ok := src[key]
	if !ok {
		return
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return
	}
	dst[key] = text
}

func copySessionsV3ProviderNativeAnyField(dst, src map[string]any, key string) {
	value, ok := src[key]
	if !ok || value == nil {
		return
	}
	dst[key] = sessionsV3ProviderNativeStripResponseFields(value)
}

func copySessionsV3ProviderNativeNonEmptyField(dst, src map[string]any, key string) {
	value, ok := src[key]
	if !ok || value == nil {
		return
	}
	sanitized := sessionsV3ProviderNativeStripResponseFields(value)
	if sessionsV3ProviderNativeIsEmptyValue(sanitized) {
		return
	}
	dst[key] = sanitized
}

func sessionsV3ProviderNativeIsEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case []map[string]any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func sessionsV3ProviderNativeStripResponseFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "", "output_index", "phase", "logprobs", "annotations":
				continue
			default:
				out[key] = sessionsV3ProviderNativeStripResponseFields(child)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sessionsV3ProviderNativeStripResponseFields(child))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sessionsV3ProviderNativeStripResponseFields(child))
		}
		return out
	default:
		return typed
	}
}

func cloneSessionsV3ProviderItems(items []any) []any {
	if len(items) == 0 {
		return nil
	}
	return cloneSessionsV3ProviderItemSlice(items)
}

func cloneSessionsV3ProviderItemSlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSessionsV3ProviderItemValue(item))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSessionsV3ProviderItemValue(item))
		}
		return out
	default:
		return nil
	}
}

func cloneSessionsV3ProviderItemValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = cloneSessionsV3ProviderItemValue(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, cloneSessionsV3ProviderItemValue(child))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, cloneSessionsV3ProviderItemValue(child))
		}
		return out
	default:
		return typed
	}
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
	call := tool.Call{CallID: callID, Name: name, Arguments: arguments}
	result := tool.Result{
		CallID:     callID,
		Name:       name,
		Output:     strings.TrimSpace(firstNonEmpty(record.Output, record.CompletedOutput)),
		Error:      strings.TrimSpace(record.Error),
		DurationMS: record.DurationMS,
	}
	return []map[string]any{
		callInput,
		{"type": "function_call_output", "call_id": callID, "output": runruntime.PrepareToolOutputForModel(call, result)},
	}
}

func sessionsV3LatestFunctionCallOutput(input []map[string]any) string {
	for i := len(input) - 1; i >= 0; i-- {
		item := input[i]
		if !strings.EqualFold(strings.TrimSpace(sessionsV3MapString(item, "type")), "function_call_output") {
			continue
		}
		return strings.TrimSpace(sessionsV3MapString(item, "output"))
	}
	return ""
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
			output = runruntime.PrepareToolOutputForModel(tool.Call{CallID: callID, Name: name, Arguments: arguments}, tool.Result{CallID: callID, Name: name, Output: strings.TrimSpace(result.Output), Error: strings.TrimSpace(result.Error), DurationMS: result.DurationMS})
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
	var userCount int
	for _, message := range messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "system", "reasoning", "tool", "function", "assistant":
			continue
		case "user":
			userCount++
			if userCount > 1 {
				return false
			}
		default:
			return false
		}
	}
	return userCount == 1
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
	return applySessionV3AgentPreferenceOverridesForMode(base, agentProfile, sessionruntime.ModeAuto)
}

func applySessionV3AgentPreferenceOverridesForMode(base pebblestore.ModelPreference, agentProfile pebblestore.AgentProfile, mode string) pebblestore.ModelPreference {
	providerOverride := strings.ToLower(strings.TrimSpace(agentProfile.Provider))
	modelOverride := strings.TrimSpace(agentProfile.Model)
	thinkingOverride := strings.TrimSpace(agentProfile.Thinking)
	if pebblestore.AgentModelMode(agentProfile) == "split" && pebblestore.AgentSupportsSplitModel(agentProfile) {
		mode = sessionruntime.NormalizeMode(mode)
		if mode == sessionruntime.ModePlan {
			providerOverride = strings.ToLower(strings.TrimSpace(agentProfile.PlanProvider))
			modelOverride = strings.TrimSpace(agentProfile.PlanModel)
			thinkingOverride = strings.TrimSpace(agentProfile.PlanThinking)
			if serviceTierOverride := strings.TrimSpace(agentProfile.PlanServiceTier); serviceTierOverride != "" {
				base.ServiceTier = serviceTierOverride
			}
		} else if mode == sessionruntime.ModeAuto {
			providerOverride = strings.ToLower(strings.TrimSpace(agentProfile.AutoProvider))
			modelOverride = strings.TrimSpace(agentProfile.AutoModel)
			thinkingOverride = strings.TrimSpace(agentProfile.AutoThinking)
			if serviceTierOverride := strings.TrimSpace(agentProfile.AutoServiceTier); serviceTierOverride != "" {
				base.ServiceTier = serviceTierOverride
			}
		}
	}
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
	base.ServiceTier = modelruntime.NormalizeServiceTierForProvider(base.Provider, base.ServiceTier)
	if !strings.EqualFold(strings.TrimSpace(base.Provider), "codex") || !strings.EqualFold(strings.TrimSpace(base.Model), "gpt-5.4") {
		base.ContextMode = ""
	}
	return base
}

func normalizeSessionV3ThinkingWithProvider(providerID, thinking string) string {
	normalized := strings.ToLower(strings.TrimSpace(thinking))
	switch normalized {
	case "off", "low", "medium", "high", "xhigh", "max", "ultra":
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
	if minWords > 0 && len(words) < minWords {
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

func sessionV3ExecutorStepClientRequestID(eventType, runID string, step int) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	if step <= 0 {
		step = 1
	}
	return fmt.Sprintf("v3-executor-%s-%s-%04d", label, strings.TrimSpace(runID), step)
}

func sessionV3ProviderToolInstanceID(step int, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool_call"
	}
	return sessionV3ProviderToolStepID(step) + ":" + callID
}

func sessionV3PermissionUpdatedPayload(sessionID, runID string, step int, toolName, callID, arguments string, permission any) map[string]any {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if step <= 0 {
		step = 1
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool_call"
	}
	payload := map[string]any{
		"run_id":           runID,
		"step":             step,
		"step_id":          sessionV3ProviderToolStepID(step),
		"tool_name":        toolName,
		"call_id":          callID,
		"tool_instance_id": sessionV3ProviderToolInstanceID(step, callID),
		"type":             "permission.updated",
		"session_id":       sessionID,
	}
	if permission != nil {
		payload["permission"] = permission
	}
	if arguments = strings.TrimSpace(arguments); arguments != "" {
		payload["arguments"] = arguments
	}
	return payload
}

func sessionV3ProviderToolEventStatus(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "session.tool.started":
		return "started"
	case "session.tool.completed":
		return "completed"
	case "session.tool.failed":
		return "failed"
	case "session.tool.cancelled", "session.tool.canceled":
		return "cancelled"
	default:
		return ""
	}
}

func sessionV3ProviderToolEventClientRequestID(eventType, runID string, step int, callID string, deltaIndex int) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	callID = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(sessionV3ProviderToolInstanceID(step, callID))
	if deltaIndex > 0 {
		return fmt.Sprintf("v3-executor-%s-%s-%04d-%s-%04d", label, strings.TrimSpace(runID), step, callID, deltaIndex)
	}
	return fmt.Sprintf("v3-executor-%s-%s-%04d-%s", label, strings.TrimSpace(runID), step, callID)
}

func sessionV3ReasoningEventClientRequestID(eventType, runID string, step int, reasoningKey string, deltaIndex int) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_").Replace(strings.TrimSpace(eventType))
	reasoningID := strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(sessionV3ReasoningEventID(step, reasoningKey))
	if deltaIndex > 0 {
		return fmt.Sprintf("v3-executor-%s-%s-%04d-%s-%04d", label, strings.TrimSpace(runID), step, reasoningID, deltaIndex)
	}
	return fmt.Sprintf("v3-executor-%s-%s-%04d-%s", label, strings.TrimSpace(runID), step, reasoningID)
}

func sessionV3AssistantMessageID(sessionID, runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00assistant"))
	return "v3msg_assistant_" + hex.EncodeToString(sum[:16])
}

func sessionV3RunFailureMessageID(sessionID, runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00run_failure"))
	return "v3msg_system_failure_" + hex.EncodeToString(sum[:16])
}

func sessionV3AssistantSegmentMessageID(sessionID, runID string, step int) string {
	if step <= 0 {
		step = 1
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00assistant\x00pre_tool\x00" + strconv.Itoa(step)))
	return "v3msg_assistant_" + hex.EncodeToString(sum[:16])
}

func sessionV3NormalizeReasoningKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "default"
	}
	return key
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
