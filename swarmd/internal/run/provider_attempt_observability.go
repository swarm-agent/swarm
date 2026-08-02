package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type durableProviderAttemptObserver struct {
	sessionID string
	runID     string
	principal identity.Principal
	apply     func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
}

func (o *durableProviderAttemptObserver) AttemptTimedOut(attempt int) {
	o.record("watchdog_detected", attempt, "provider_activity_timeout")
}

func (o *durableProviderAttemptObserver) AttemptCancelled(attempt int) {
	o.record("watchdog_cancelled", attempt, "expired_generation_fenced")
}

func (o *durableProviderAttemptObserver) AttemptRetrying(attempt int) {
	o.record("watchdog_retrying", attempt, "durable_boundary_replay")
}

func (o *durableProviderAttemptObserver) AttemptFailed(attempt int, _ error) {
	o.record("watchdog_terminal_failure", attempt, "retry_limit_exhausted")
}

func (o *durableProviderAttemptObserver) record(state string, attempt int, reason string) {
	if o == nil || o.apply == nil || strings.TrimSpace(o.sessionID) == "" || strings.TrimSpace(o.runID) == "" {
		return
	}
	now := time.Now().UnixMilli()
	payload := struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		State     string `json:"recovery_state"`
		Reason    string `json:"reason"`
		Attempt   int    `json:"attempt"`
		Recorded  int64  `json:"recorded_at"`
	}{strings.TrimSpace(o.sessionID), strings.TrimSpace(o.runID), state, reason, attempt, now}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	eventType := "session.run.recovery." + state
	requestID := fmt.Sprintf("run-recovery-%s-%s-%d", strings.ReplaceAll(state, "_", "-"), strings.TrimSpace(o.runID), attempt)
	_, _ = o.apply(sessionruntime.SessionMutationInput{
		SessionID:       o.sessionID,
		UserID:          o.principal.UserID,
		AccountScopeID:  o.principal.AccountScopeID,
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     hash,
		RequestHash:     hash,
		Kind:            sessionruntime.SessionMutationRecordDiagnostic,
		EventType:       eventType,
		EventPayload:    raw,
		NowUnixMs:       now,
	})
}
