package api

import (
	"encoding/json"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionV3StaleRecoveryDecisionReasons(t *testing.T) {
	tests := []struct {
		name       string
		activity   *sessionV3RunActivity
		summary    pebblestore.SessionUsageSummary
		wantReason string
	}{
		{name: "active tool", activity: &sessionV3RunActivity{}, summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "codex_api_usage"}, wantReason: "tool_active"},
		{name: "ambiguous usage", activity: &sessionV3RunActivity{}, summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "estimated"}, wantReason: "usage_ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "active tool" {
				test.activity.toolActive.Store(true)
			}
			test.activity.lastProviderActivity.Store(time.Now().Add(-sessionV3StaleRecoveryMinInactivity - time.Second).UnixMilli())
			if test.name == "ambiguous usage" {
				if _, trusted := sessionV3TrustedContextUtilization(test.summary); trusted {
					t.Fatal("ambiguous usage was trusted")
				}
				return
			}
			if !test.activity.toolActive.Load() || test.wantReason != "tool_active" {
				t.Fatalf("activity=%+v wantReason=%s", test.activity, test.wantReason)
			}
		})
	}
}

func TestSessionV3StaleRecoveryEventMovesToCompactionSuccessorEpoch(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "stale-recovery-successor", "stale recovery successor")
	principal := testPrincipal()

	originalEpoch, ok, err := sessionSvc.GetActiveExecutionEpoch(created.ID)
	if err != nil || !ok {
		t.Fatalf("get original active epoch: ok=%t err=%v", ok, err)
	}
	job := sessionV3ExecutorJob{Principal: principal, SessionID: created.ID, RunID: "run-stale-recovery", EpochID: originalEpoch.EpochID}
	pending := sessionV3RunIntentForJob(job, sessionruntime.RunIntentPendingExecutor, time.Now().UnixMilli())
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: created.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: "stale-recovery-pending", IdempotencyKey: "stale-recovery-pending", PayloadHash: "stale-recovery-pending", RequestHash: "stale-recovery-pending",
		Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pending,
	}); err != nil {
		t.Fatalf("persist pending run: %v", err)
	}
	running := sessionV3RunIntentForJob(job, sessionruntime.RunIntentRunning, time.Now().UnixMilli())
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: created.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: "stale-recovery-running", IdempotencyKey: "stale-recovery-running", PayloadHash: "stale-recovery-running", RequestHash: "stale-recovery-running",
		Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.started", RunIntent: &running,
	}); err != nil {
		t.Fatalf("persist running run: %v", err)
	}

	ownerRunID := job.RunID + "-stale-recovery"
	if _, claimed, err := sessionSvc.ClaimExecutionEpochRecovery(created.ID, originalEpoch.EpochID, ownerRunID, time.Now().UnixMilli()); err != nil || !claimed {
		t.Fatalf("claim recovery: claimed=%t err=%v", claimed, err)
	}
	boundary, err := sessionSvc.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID: created.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: "stale-recovery-compaction-boundary", PayloadHash: "stale-recovery-compaction-boundary",
		Reason: "context_compaction_threshold", RunID: job.RunID + "-stale-compact", SkipRunIntent: true,
	})
	if err != nil {
		t.Fatalf("begin compaction successor epoch: %v", err)
	}
	if boundary.Epoch.ParentEpochID != originalEpoch.EpochID {
		t.Fatalf("successor parent epoch = %q, want %q", boundary.Epoch.ParentEpochID, originalEpoch.EpochID)
	}

	exec := &sessionV3Executor{server: server}
	if err := exec.recordStaleRecoveryEvent(job, sessionV3RecoveryRetrying, "compaction_completed"); err != nil {
		t.Fatalf("record retry event after compaction: %v", err)
	}
	intent, ok, err := sessionSvc.GetSessionRunIntent(created.ID, job.RunID)
	if err != nil || !ok {
		t.Fatalf("get updated run intent: ok=%t err=%v", ok, err)
	}
	if intent.EpochID != boundary.Epoch.EpochID {
		t.Fatalf("run intent epoch = %q, want active successor %q", intent.EpochID, boundary.Epoch.EpochID)
	}
	projection, ok, err := sessionSvc.Store().GetV3SessionProjection(created.ID)
	if err != nil || !ok {
		t.Fatalf("get projection: ok=%t err=%v", ok, err)
	}
	event, ok, err := sessionSvc.Store().GetV3SessionEvent(created.ID, projection.LastEventSeq)
	if err != nil || !ok {
		t.Fatalf("get recovery event: ok=%t err=%v", ok, err)
	}
	if event.EpochID != boundary.Epoch.EpochID {
		t.Fatalf("recovery event epoch = %q, want active successor %q", event.EpochID, boundary.Epoch.EpochID)
	}
	var payload struct {
		EpochID string `json:"epoch_id"`
		State   string `json:"recovery_state"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode recovery event: %v", err)
	}
	if payload.EpochID != originalEpoch.EpochID || payload.State != sessionV3RecoveryRetrying {
		t.Fatalf("recovery payload = %+v, want original epoch %q and retrying state", payload, originalEpoch.EpochID)
	}
}

func TestSessionV3TrustedContextUtilizationBounds(t *testing.T) {
	tests := []struct {
		name    string
		summary pebblestore.SessionUsageSummary
		want    float64
		trusted bool
	}{
		{name: "trusted 85 percent", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 850, Source: "codex_api_usage"}, want: 85, trusted: true},
		{name: "trusted 99 percent", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 990, Source: "anthropic_api_usage"}, want: 99, trusted: true},
		{name: "missing source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900}, trusted: false},
		{name: "untrusted source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "estimated"}, trusted: false},
		{name: "retired copilot source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "copilot_session_usage"}, trusted: false},
		{name: "missing window", summary: pebblestore.SessionUsageSummary{TotalTokens: 900, Source: "codex_api_usage"}, trusted: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, trusted := sessionV3TrustedContextUtilization(test.summary)
			if trusted != test.trusted || got != test.want {
				t.Fatalf("utilization=%v trusted=%t, want %v/%t", got, trusted, test.want, test.trusted)
			}
		})
	}
}
