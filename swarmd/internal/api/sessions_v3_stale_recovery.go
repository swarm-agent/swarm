package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"swarm/packages/swarmd/internal/privacy"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var errSessionV3StaleProviderAttempt = errors.New("stale high-context provider attempt")

const (
	sessionV3RecoveryDetected   = "detected"
	sessionV3RecoveryCancelling = "cancelling"
	sessionV3RecoveryCompacting = "compacting"
	sessionV3RecoveryRetrying   = "retrying"
)

type sessionV3RunActivity struct {
	lastProviderActivity atomic.Int64
	toolActive           atomic.Bool
	forceRecovery        chan struct{}
}

func newSessionV3RunActivity() *sessionV3RunActivity {
	activity := &sessionV3RunActivity{forceRecovery: make(chan struct{}, 1)}
	activity.touch()
	return activity
}

func (a *sessionV3RunActivity) touch() {
	if a != nil {
		a.lastProviderActivity.Store(time.Now().UnixMilli())
	}
}

func (a *sessionV3RunActivity) inactivity(now time.Time) time.Duration {
	if a == nil || a.lastProviderActivity.Load() <= 0 {
		return 0
	}
	return now.Sub(time.UnixMilli(a.lastProviderActivity.Load()))
}

func sessionV3TrustedContextUtilization(summary pebblestore.SessionUsageSummary) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(summary.Source)) {
	case "codex_api_usage", "google_api_usage", "anthropic_api_usage", "fireworks_api_usage", "openrouter_api_usage":
	default:
		return 0, false
	}
	if summary.ContextWindow <= 0 || summary.TotalTokens <= 0 {
		return 0, false
	}
	used := summary.TotalTokens
	if used < 0 {
		return 0, false
	}
	return float64(used) * 100 / float64(summary.ContextWindow), true
}

type sessionV3StaleRecoveryDecision struct {
	Eligible bool
	Reason   string
}

func (e *sessionV3Executor) staleRecoveryDecision(job sessionV3ExecutorJob, now time.Time) (sessionV3StaleRecoveryDecision, error) {
	if e == nil || e.server == nil || e.server.sessions == nil || job.activity == nil {
		return sessionV3StaleRecoveryDecision{Reason: "executor_not_configured"}, nil
	}
	if job.activity.inactivity(now) < sessionV3StaleRecoveryMinInactivity {
		return sessionV3StaleRecoveryDecision{Reason: "provider_active"}, nil
	}
	if job.activity.toolActive.Load() {
		return sessionV3StaleRecoveryDecision{Reason: "tool_active"}, nil
	}
	if e.server.perm != nil {
		pending, err := e.server.perm.PendingCount(job.SessionID)
		if err != nil {
			return sessionV3StaleRecoveryDecision{}, err
		}
		if pending > 0 {
			return sessionV3StaleRecoveryDecision{Reason: "permission_pending"}, nil
		}
	}
	summary, ok, err := e.server.sessions.GetUsageSummary(job.SessionID)
	if err != nil {
		return sessionV3StaleRecoveryDecision{}, err
	}
	if !ok {
		return sessionV3StaleRecoveryDecision{Reason: "usage_unavailable"}, nil
	}
	utilization, trusted := sessionV3TrustedContextUtilization(summary)
	if !trusted {
		return sessionV3StaleRecoveryDecision{Reason: "usage_ambiguous"}, nil
	}
	if utilization < sessionV3StaleRecoveryMinUtilization {
		return sessionV3StaleRecoveryDecision{Reason: "context_below_threshold"}, nil
	}
	if utilization > sessionV3StaleRecoveryMaxUtilization {
		return sessionV3StaleRecoveryDecision{Reason: "context_above_safe_range"}, nil
	}
	intent, ok, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil {
		return sessionV3StaleRecoveryDecision{}, err
	}
	if !ok || !strings.EqualFold(strings.TrimSpace(intent.Status), "running") {
		return sessionV3StaleRecoveryDecision{Reason: "run_not_active"}, nil
	}
	if strings.TrimSpace(intent.EpochID) != strings.TrimSpace(job.EpochID) {
		return sessionV3StaleRecoveryDecision{Reason: "epoch_superseded"}, nil
	}
	if recovery, exists, err := e.server.sessions.GetExecutionEpochRecovery(job.SessionID, job.EpochID); err != nil {
		return sessionV3StaleRecoveryDecision{}, err
	} else if exists {
		return sessionV3StaleRecoveryDecision{Reason: "epoch_recovery_already_" + recovery.Status}, nil
	}
	return sessionV3StaleRecoveryDecision{Eligible: true}, nil
}

func (e *sessionV3Executor) staleRecoveryEligible(job sessionV3ExecutorJob, now time.Time) (bool, error) {
	decision, err := e.staleRecoveryDecision(job, now)
	return decision.Eligible, err
}

func (e *sessionV3Executor) recordStaleRecoveryEvent(job sessionV3ExecutorJob, state, reason string) error {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return errors.New("v3 stale recovery event recorder is not configured")
	}
	state = strings.TrimSpace(state)
	reason = strings.TrimSpace(reason)
	if state == "" {
		return errors.New("v3 stale recovery state is required")
	}

	// The recovery projection remains owned by the epoch where the stale attempt
	// was detected. Compaction seals that epoch and atomically creates a successor,
	// so subsequent recovery observability must be written in the active epoch
	// while retaining the original recovery epoch in its payload and idempotency
	// identity. This preserves normal epoch fencing instead of bypassing it.
	recoveryEpochID := strings.TrimSpace(job.EpochID)
	eventJob := job
	if recoveryEpoch, ok, err := e.server.sessions.GetExecutionEpoch(job.SessionID, recoveryEpochID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("stale recovery execution epoch %q not found", recoveryEpochID)
	} else if recoveryEpoch.Status != pebblestore.ExecutionEpochStatusActive {
		activeEpoch, active, activeErr := e.server.sessions.GetActiveExecutionEpoch(job.SessionID)
		if activeErr != nil {
			return activeErr
		}
		if !active {
			return fmt.Errorf("stale recovery execution epoch %q is sealed without an active continuation epoch", recoveryEpochID)
		}
		eventJob.EpochID = activeEpoch.EpochID
	}

	now := time.Now().UnixMilli()
	intent := sessionV3RunIntentForJob(eventJob, pebblestore.V3RunIntentRunning, now)
	payload := struct {
		SessionID string                         `json:"session_id"`
		RunID     string                         `json:"run_id"`
		EpochID   string                         `json:"epoch_id"`
		State     string                         `json:"recovery_state"`
		Reason    string                         `json:"reason,omitempty"`
		RunIntent pebblestore.V3SessionRunIntent `json:"run_intent"`
		Recorded  int64                          `json:"recorded_at"`
	}{job.SessionID, job.RunID, recoveryEpochID, state, reason, intent, now}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventType := "session.run.recovery." + state
	payloadHash, err := sessionV3ExecutorPayloadHash(job.SessionID, job.RunID, pebblestore.V3RunIntentRunning, reason, eventType, string(raw))
	if err != nil {
		return err
	}
	clientRequestID := sessionV3ExecutorClientRequestID(eventType+"."+reason+"."+recoveryEpochID, job.RunID)
	_, err = e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: job.SessionID, UserID: job.Principal.UserID, AccountScopeID: job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: eventType, EventPayload: raw, EpochID: eventJob.EpochID, RunIntent: &intent, NowUnixMs: now,
	})
	return err
}

func (e *sessionV3Executor) updateStaleRecoveryPhase(job sessionV3ExecutorJob, ownerRunID, phase, reason string) error {
	if _, err := e.server.sessions.UpdateExecutionEpochRecoveryPhase(job.SessionID, job.EpochID, ownerRunID, phase, reason, time.Now().UnixMilli()); err != nil {
		return err
	}
	return e.recordStaleRecoveryEvent(job, phase, reason)
}

func (e *sessionV3Executor) runStaleSupervisedProviderAttempt(ctx context.Context, job sessionV3ExecutorJob, runner provideriface.Runner, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if job.activity == nil {
		return runner.CreateResponseStreaming(ctx, req, onEvent)
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	attemptCtx = provideriface.WithAttemptActivityReporter(attemptCtx, func(provideriface.AttemptActivityPhase) { job.activity.touch() })
	job.activity.touch()
	type result struct {
		response provideriface.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := runner.CreateResponseStreaming(attemptCtx, req, func(event provideriface.StreamEvent) {
			job.activity.touch()
			if attemptCtx.Err() == nil && onEvent != nil {
				onEvent(event)
			}
		})
		resultCh <- result{response: response, err: err}
	}()

	ticker := time.NewTicker(sessionV3StaleRecoveryConfirmInterval)
	defer ticker.Stop()
	confirmedAt := time.Time{}
	for {
		select {
		case result := <-resultCh:
			return result.response, result.err
		case <-ctx.Done():
			cancel()
			select {
			case <-resultCh:
			case <-time.After(30 * time.Second):
			}
			return provideriface.Response{}, ctx.Err()
		case <-job.activity.forceRecovery:
			confirmedAt = time.Now().Add(-sessionV3StaleRecoveryConfirmInterval)
		case now := <-ticker.C:
			eligible, err := e.staleRecoveryEligible(job, now)
			if err != nil || !eligible {
				confirmedAt = time.Time{}
				continue
			}
			if confirmedAt.IsZero() {
				confirmedAt = now
				continue
			}
			if now.Sub(confirmedAt) < sessionV3StaleRecoveryConfirmInterval {
				continue
			}
			ownerRunID := strings.TrimSpace(job.RunID) + "-stale-recovery"
			_, claimed, claimErr := e.server.sessions.ClaimExecutionEpochRecovery(job.SessionID, job.EpochID, ownerRunID, now.UnixMilli())
			if claimErr != nil || !claimed {
				confirmedAt = time.Time{}
				continue
			}
			if eventErr := e.recordStaleRecoveryEvent(job, sessionV3RecoveryDetected, "provider_inactive_high_context"); eventErr != nil {
				_, _ = e.server.sessions.FinishExecutionEpochRecovery(job.SessionID, job.EpochID, ownerRunID, pebblestore.ExecutionEpochRecoveryStatusFailed, "recovery detection event failed", time.Now().UnixMilli())
				return provideriface.Response{}, fmt.Errorf("persist stale recovery detection: %w", eventErr)
			}
			if phaseErr := e.updateStaleRecoveryPhase(job, ownerRunID, sessionV3RecoveryCancelling, "provider_inactive_high_context"); phaseErr != nil {
				_, _ = e.server.sessions.FinishExecutionEpochRecovery(job.SessionID, job.EpochID, ownerRunID, pebblestore.ExecutionEpochRecoveryStatusFailed, "recovery cancellation event failed", time.Now().UnixMilli())
				return provideriface.Response{}, fmt.Errorf("persist stale recovery cancellation: %w", phaseErr)
			}
			cancel()
			select {
			case <-resultCh:
				return provideriface.Response{}, fmt.Errorf("%w: epoch %s owner %s", errSessionV3StaleProviderAttempt, job.EpochID, ownerRunID)
			case <-time.After(30 * time.Second):
				_, _ = e.server.sessions.FinishExecutionEpochRecovery(job.SessionID, job.EpochID, ownerRunID, pebblestore.ExecutionEpochRecoveryStatusFailed, "expired provider attempt did not terminate", time.Now().UnixMilli())
				return provideriface.Response{}, errors.New("stale provider attempt did not terminate before recovery")
			}
		}
	}
}

func (e *sessionV3Executor) startStaleRecoveryBackstop(ctx context.Context) {
	if e == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(sessionV3StaleRecoveryScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				e.scanStaleRecoveryCandidates(now)
			}
		}
	}()
}

func (e *sessionV3Executor) scanStaleRecoveryCandidates(now time.Time) {
	e.mu.Lock()
	jobs := make([]sessionV3ExecutorJob, 0, len(e.runStates))
	for _, state := range e.runStates {
		if state != nil && state.job.activity != nil {
			jobs = append(jobs, state.job)
			if len(jobs) >= sessionV3StaleRecoveryScanLimit {
				break
			}
		}
	}
	e.mu.Unlock()
	for _, job := range jobs {
		decision, err := e.staleRecoveryDecision(job, now)
		key := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
		e.mu.Lock()
		if cooldownUntil := e.recoveryCooldown[job.SessionID]; cooldownUntil > now.UnixMilli() {
			decision = sessionV3StaleRecoveryDecision{Reason: "session_recovery_cooldown"}
		} else if cooldownUntil != 0 {
			delete(e.recoveryCooldown, job.SessionID)
		}
		if err != nil || !decision.Eligible {
			delete(e.recoveryConfirm, key)
			e.mu.Unlock()
			if err == nil && job.activity.inactivity(now) >= sessionV3StaleRecoveryMinInactivity && decision.Reason != "provider_active" {
				_ = e.recordStaleRecoveryEvent(job, "skipped", decision.Reason)
			}
			continue
		}
		first := e.recoveryConfirm[key]
		if first == 0 {
			e.recoveryConfirm[key] = now.UnixMilli()
			e.mu.Unlock()
			continue
		}
		delete(e.recoveryConfirm, key)
		e.recoveryCooldown[job.SessionID] = now.Add(sessionV3StaleRecoveryCooldown).UnixMilli()
		e.mu.Unlock()
		if now.Sub(time.UnixMilli(first)) >= sessionV3StaleRecoveryConfirmInterval {
			select {
			case job.activity.forceRecovery <- struct{}{}:
			default:
			}
		}
	}
}

func (e *sessionV3Executor) staleCompactedAssistantResponse(ctx context.Context, job sessionV3ExecutorJob, cause error) (response sessionV3AssistantResponse, nextJob sessionV3ExecutorJob, err error) {
	ownerRunID := strings.TrimSpace(job.RunID) + "-stale-recovery"
	originalEpochID := strings.TrimSpace(job.EpochID)
	defer func() {
		status := pebblestore.ExecutionEpochRecoveryStatusCompleted
		outcome := "compacted continuation completed"
		state := "completed"
		if err != nil {
			status = pebblestore.ExecutionEpochRecoveryStatusFailed
			outcome = strings.TrimSpace(privacy.SanitizeText(err.Error()))
			state = "terminal_failure"
		}
		_, finishErr := e.server.sessions.FinishExecutionEpochRecovery(job.SessionID, originalEpochID, ownerRunID, status, outcome, time.Now().UnixMilli())
		terminalJob := job
		terminalJob.EpochID = originalEpochID
		if eventErr := e.recordStaleRecoveryEvent(terminalJob, state, outcome); err == nil && eventErr != nil {
			err = eventErr
		}
		if err == nil && finishErr != nil {
			err = finishErr
		}
	}()
	if err := e.updateStaleRecoveryPhase(job, ownerRunID, sessionV3RecoveryCompacting, "stale_high_context"); err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("persist stale recovery compaction: %w", err)
	}
	compactRunID := strings.TrimSpace(job.RunID) + "-stale-compact"
	result, err := e.server.runner.RunTurn(ctx, job.SessionID, runruntime.RunRequest{Prompt: "stale high-context compact request", Compact: true, CompactOrigin: "threshold"}, runruntime.RunStartMeta{RunID: compactRunID, Principal: job.Principal, ApplySessionMutation: e.server.applySessionV3PrimaryMutation})
	if err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 stale recovery compact failed: %w", err)
	}
	if strings.TrimSpace(result.AssistantMessage.Content) == "" {
		return sessionV3AssistantResponse{}, job, errors.New("v3 stale recovery compact returned empty checkpoint acknowledgement")
	}
	activeEpoch, ok, err := e.server.sessions.GetActiveExecutionEpoch(job.SessionID)
	if err != nil || !ok {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 stale recovery continuation epoch resolve failed: %w", err)
	}
	if activeEpoch.ParentEpochID != strings.TrimSpace(job.EpochID) || activeEpoch.Boundary.RunID != compactRunID || activeEpoch.Boundary.Reason != "context_compaction_threshold" {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("v3 stale recovery compact created unexpected continuation epoch %q", activeEpoch.EpochID)
	}
	job.EpochID = activeEpoch.EpochID
	job.ResumeContext = true
	phaseJob := job
	phaseJob.EpochID = originalEpochID
	if err := e.updateStaleRecoveryPhase(phaseJob, ownerRunID, sessionV3RecoveryRetrying, "compaction_completed"); err != nil {
		return sessionV3AssistantResponse{}, job, fmt.Errorf("persist stale recovery retry: %w", err)
	}
	resolved, err := e.resolveSessionV3Runtime(job)
	if err != nil {
		return sessionV3AssistantResponse{}, job, err
	}
	response, err = e.providerAssistantResponse(ctx, job, resolved, "stale-compacted-continuation", true)
	return response, job, err
}
