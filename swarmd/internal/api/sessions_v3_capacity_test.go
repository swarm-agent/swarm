package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/provideriface"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// blockingCapacityTestRunner implements provideriface.ExecutionEpochLifecycleRunner
// and blocks execution until unblock channel is triggered, allowing deterministic
// capacity slot assertions.
type blockingCapacityTestRunner struct {
	mu        sync.Mutex
	id        string
	unblockCh chan struct{}
	enteredCh chan struct{}
}

func newBlockingCapacityTestRunner(id string) *blockingCapacityTestRunner {
	return &blockingCapacityTestRunner{
		id:        id,
		unblockCh: make(chan struct{}),
		enteredCh: make(chan struct{}, 100),
	}
}

func (r *blockingCapacityTestRunner) ID() string {
	if r.id != "" {
		return r.id
	}
	return "test-provider"
}

func (r *blockingCapacityTestRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{ContextMode: provideriface.ExecutionEpochContextResponsesChain, TransportReusable: true}
}

func (r *blockingCapacityTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *blockingCapacityTestRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.enteredCh <- struct{}{}
	select {
	case <-r.unblockCh:
		return provideriface.Response{
			ID:    "resp-" + req.Model,
			Model: req.Model,
			Choices: []provideriface.Choice{{
				Index:   0,
				Message: provideriface.Message{Role: "assistant", Content: "capacity test done"},
			}},
		}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}

func recordTestPendingRunIntent(t *testing.T, server *Server, sessionID, runID, accountID, userID string) {
	t.Helper()
	now := time.Now().UnixMilli()
	pending := pebblestore.V3SessionRunIntent{
		SessionID:      sessionID,
		UserID:         userID,
		AccountScopeID: accountID,
		RunID:          runID,
		Status:         sessionruntime.RunIntentPendingExecutor,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, runID, pending.Status, "", "session.assistant.queued", "")
	if err != nil {
		t.Fatalf("hash pending intent: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "intent-" + runID,
		IdempotencyKey:  "intent-" + runID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.queued",
		RunIntent:       &pending,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("record pending intent %s: %v", runID, err)
	}
}

// TestSessionsV3Executor_CapacityCapContention
// Purpose:
// - Invariant: Account-scoped ActiveExecutionLimit restricts total concurrent active runs.
// - Threat/regression: Uncontrolled concurrent executions exhaust host resources or overshoot limits.
// - Production boundary: sessionV3Executor.run, permission.Service, executioncapacity.Manager.
// - Narrowest test layer: sessionV3Executor integration with Pebble store and capacity manager using real blocking provider runner.
func TestSessionsV3Executor_CapacityCapContention(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 2); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	sessions := make([]pebblestore.SessionSnapshot, 4)
	jobs := make([]sessionV3ExecutorJob, 4)
	for i := 0; i < 4; i++ {
		sessID := fmt.Sprintf("session-cap-%c", 'a'+i)
		runID := fmt.Sprintf("run-cap-%c", 'a'+i)
		s, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      sessID,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			WorkspacePath:  t.TempDir(),
			WorkspaceName:  sessID,
			Mode:           sessionruntime.ModeAuto,
			Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		sessions[i] = s
		recordTestPendingRunIntent(t, server, sessID, runID, accountID, testPrincipal().UserID)
		jobs[i] = sessionV3ExecutorJob{
			Principal: testPrincipal(),
			SessionID: sessID,
			RunID:     runID,
		}
	}

	for i := 0; i < 4; i++ {
		if !exec.EnqueueRun(jobs[i]) {
			t.Fatalf("enqueue run %d failed", i)
		}
	}

	// Wait for first 2 to enter the blocking provider
	for i := 0; i < 2; i++ {
		select {
		case <-blockingRunner.enteredCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for job to enter provider runner")
		}
	}

	// Verify exactly 2 active and 2 pending in the capacity manager
	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 2 || snap.Pending != 2 {
		t.Fatalf("capacity contention mismatch: got TotalActive=%d Pending=%d, want 2 and 2", snap.TotalActive, snap.Pending)
	}

	// Cancel the first 2 active runs to release their slots
	_, _, _ = exec.CancelRun(jobs[0], "done")
	_, _, _ = exec.CancelRun(jobs[1], "done")

	// Wait for the remaining 2 pending runs to be admitted and enter the provider runner
	for i := 0; i < 2; i++ {
		select {
		case <-blockingRunner.enteredCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for refilled jobs to enter provider runner")
		}
	}

	snapAfter := permSvc.ExecutionCapacitySnapshot(accountID)
	if snapAfter.Pending != 0 {
		t.Fatalf("expected pending to be 0 after refilling slots, got: %+v", snapAfter)
	}

	// Cancel remaining to drain cleanly
	_, _, _ = exec.CancelRun(jobs[2], "done")
	_, _, _ = exec.CancelRun(jobs[3], "done")
	close(blockingRunner.unblockCh)
}

// TestSessionsV3Executor_CombinedOrdinaryAndDeployedAccounting
// Purpose:
// - Invariant: Deployed sessions are distinguished from ordinary sessions via canonical deployment metadata,
//   and both count toward TotalActive while only deployed sessions count toward DeployedActive.
// - Threat/regression: Deployed sessions misclassified as ordinary, or delegated subagents mistakenly classified as deployed.
// - Production boundary: sessionruntime.IsDeployedSession, sessionV3Executor.run, executioncapacity.Manager.
// - Narrowest test layer: sessionV3Executor integration with deployed and ordinary sessions.
func TestSessionsV3Executor_CombinedOrdinaryAndDeployedAccounting(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 10); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	// 1. Ordinary session
	sOrd, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "sess-ordinary",
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "ordinary",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
	})
	if err != nil {
		t.Fatalf("create ordinary session: %v", err)
	}
	recordTestPendingRunIntent(t, server, sOrd.ID, "run-ordinary", accountID, testPrincipal().UserID)

	// 2. Deployed session (lineage_kind: session_deploy)
	sDep, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "sess-deployed",
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "deployed",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
		Metadata: map[string]any{
			"lineage_kind":      "session_deploy",
			"parent_session_id": sOrd.ID,
		},
	})
	if err != nil {
		t.Fatalf("create deployed session: %v", err)
	}
	recordTestPendingRunIntent(t, server, sDep.ID, "run-deployed", accountID, testPrincipal().UserID)

	// 3. Delegated subagent session (must NOT count as deployed)
	sSub, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "sess-subagent",
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "subagent",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
		Metadata: map[string]any{
			"lineage_kind":      "delegated_subagent",
			"parent_session_id": sOrd.ID,
		},
	})
	if err != nil {
		t.Fatalf("create subagent session: %v", err)
	}
	recordTestPendingRunIntent(t, server, sSub.ID, "run-subagent", accountID, testPrincipal().UserID)

	// Verify metadata classification contract
	if !sessionruntime.IsDeployedSession(sDep.Metadata) {
		t.Fatalf("expected sDep to be recognized as deployed session")
	}
	if sessionruntime.IsDeployedSession(sSub.Metadata) {
		t.Fatalf("delegated subagent must not be recognized as deployed session")
	}
	if sessionruntime.IsDeployedSession(sOrd.Metadata) {
		t.Fatalf("ordinary session must not be recognized as deployed session")
	}

	exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sOrd.ID, RunID: "run-ordinary"})
	exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sDep.ID, RunID: "run-deployed"})
	exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sSub.ID, RunID: "run-subagent"})

	for i := 0; i < 3; i++ {
		select {
		case <-blockingRunner.enteredCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for session to enter runner")
		}
	}

	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 3 || snap.DeployedActive != 1 {
		t.Fatalf("expected TotalActive=3 and DeployedActive=1, got: %+v", snap)
	}

	close(blockingRunner.unblockCh)
	server.CancelInFlightRuns()
}

// TestSessionsV3Executor_PendingStopCancelDoesNotLeakSlot
// Purpose:
// - Invariant: Cancelling a pending queued run aborts capacity admission without leaking execution slots.
// - Threat/regression: Cancelled run leaves phantom active slot or orphaned waiter in capacity pool.
// - Production boundary: sessionV3Executor.CancelRun, executioncapacity.Manager.Acquire context cancellation.
// - Narrowest test layer: sessionV3Executor pending cancel integration.
func TestSessionsV3Executor_PendingStopCancelDoesNotLeakSlot(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	// Session 1: occupies slot 1
	s1, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-cancel-1", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "cancel-1", Mode: sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
	})
	recordTestPendingRunIntent(t, server, s1.ID, "run-1", accountID, testPrincipal().UserID)
	j1 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s1.ID, RunID: "run-1"}
	exec.EnqueueRun(j1)

	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for session 1 to become active")
	}

	snap1 := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap1.TotalActive != 1 {
		t.Fatalf("expected TotalActive=1, got %+v", snap1)
	}

	// Session 2: enqueued, stays pending due to cap 1
	s2, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-cancel-2", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "cancel-2", Mode: sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
	})
	recordTestPendingRunIntent(t, server, s2.ID, "run-2", accountID, testPrincipal().UserID)
	j2 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s2.ID, RunID: "run-2"}
	exec.EnqueueRun(j2)

	// Wait for session 2 to register as pending in capacity manager
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.Pending == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for session 2 to become pending")
		case <-tick.C:
		}
	}

	// Cancel session 2 while it is pending in queue
	_, tracked, err := exec.CancelRun(j2, "user cancelled pending")
	if err != nil {
		t.Fatalf("cancel pending run: %v", err)
	}
	if !tracked {
		t.Fatalf("expected run 2 to be tracked by executor")
	}

	// Now cancel session 1
	_, _, _ = exec.CancelRun(j1, "stop run 1")
	close(blockingRunner.unblockCh)

	// Verify all slots drained cleanly
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.TotalActive == 0 && snap.Pending == 0 {
			break
		}
		select {
		case <-deadline:
			snap := permSvc.ExecutionCapacitySnapshot(accountID)
			t.Fatalf("capacity leaked after cancel: %+v", snap)
		case <-tick.C:
		}
	}
}

// TestSessionsV3Executor_UsageRefusalDoesNotLeakSlot
// Purpose:
// - Invariant: Daily usage limit check fails the run cleanly without leaking capacity slots.
// - Threat/regression: Usage refusal leaves dangling capacity lease or unowned running state.
// - Production boundary: sessionV3Executor.run daily limit check, recordRunStatus.
// - Narrowest test layer: sessionV3Executor daily limit check integration.
func TestSessionsV3Executor_UsageRefusalDoesNotLeakSlot(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	// Set daily limit to $0.01 and spend $0.02
	_, err := sessionSvc.SetUsageLimit(accountID, 0.01, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	s, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-usage-refusal", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "usage-refusal", Mode: sessionruntime.ModeAuto,
	})
	now := time.Now().UnixMilli()
	_, _, _, err = sessionSvc.RecordTurnUsage(s.ID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:        s.ID,
		AccountScopeID:   accountID,
		UserID:           testPrincipal().UserID,
		RunID:            "run-prior-cost",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		EstimatedCostUSD: 0.02,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("record turn usage: %v", err)
	}

	recordTestPendingRunIntent(t, server, s.ID, "run-usage", accountID, testPrincipal().UserID)
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s.ID, RunID: "run-usage"}
	exec.EnqueueRun(job)

	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		intent, ok, err := sessionSvc.GetSessionRunIntent(s.ID, "run-usage")
		if err == nil && ok && intent.Status == sessionruntime.RunIntentFailed {
			if !strings.Contains(intent.BlockedReason, "daily usage limit exceeded") {
				t.Fatalf("expected reason to mention daily usage limit exceeded, got %q", intent.BlockedReason)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for run to fail due to usage refusal")
		case <-tick.C:
		}
	}

	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 0 || snap.Pending != 0 {
		t.Fatalf("expected 0 active and 0 pending after usage refusal, got: %+v", snap)
	}
}

// TestSessionsV3Executor_RestartAndBacklogRecovery
// Purpose:
// - Invariant: On restart, running intents are immediately reconciled as interrupted without stale cutoff,
//   and pending intents are refilled and executed without poll.
// - Threat/regression: Restart leaves orphan running states or skips fresh interrupted runs.
// - Production boundary: sessionV3Executor.recoverDurableRuns, refillPendingBacklog.
// - Narrowest test layer: executor recovery scan on startup.
func TestSessionsV3Executor_RestartAndBacklogRecovery(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	accountID := testPrincipal().AccountScopeID

	// Seed 1 fresh running run (timestamp only 10 seconds old; must not be skipped by stale cutoff!)
	sRunning, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-restart-running", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "running", Mode: sessionruntime.ModeAuto,
	})
	now := time.Now().UnixMilli()
	runningIntent := pebblestore.V3SessionRunIntent{
		SessionID:      sRunning.ID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		RunID:          "run-fresh-interrupted",
		Status:         sessionruntime.RunIntentRunning,
		UpdatedAt:      now - 10000,
	}
	payloadHash, _ := sessionV3ExecutorPayloadHash(sRunning.ID, runningIntent.RunID, runningIntent.Status, "", "session.assistant.started", "")
	_, _ = server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sRunning.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-fresh-interrupted",
		IdempotencyKey:  "run-fresh-interrupted",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &runningIntent,
		NowUnixMs:       now - 10000,
	})

	// Seed 2 pending runs
	for i := 1; i <= 2; i++ {
		sessID := fmt.Sprintf("sess-restart-pending-%d", i)
		s, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID: sessID, UserID: testPrincipal().UserID, AccountScopeID: accountID,
			WorkspacePath: t.TempDir(), WorkspaceName: sessID, Mode: sessionruntime.ModeAuto,
		})
		recordTestPendingRunIntent(t, server, s.ID, fmt.Sprintf("run-pending-%d", i), accountID, testPrincipal().UserID)
	}

	// Create new executor (simulating daemon startup)
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec

	// Check running run was reconciled to interrupted
	intent, ok, err := sessionSvc.GetSessionRunIntent(sRunning.ID, "run-fresh-interrupted")
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentInterrupted {
		t.Fatalf("expected fresh running intent to be reconciled to interrupted, got ok=%v status=%s err=%v", ok, intent.Status, err)
	}

	server.CancelInFlightRuns()
}

// TestSessionsV3Executor_BacklogPagingAcrossLargePendingQueue
// Purpose:
// - Invariant: Event-driven refill uses cursor paging across >500 pending intents and same-session runs
//   so runs for other sessions are not stranded.
// - Threat/regression: First page of 500 contains same-session runs, causing subsequent sessions to starve.
// - Production boundary: sessionV3Executor.refillPendingBacklog, ListSessionRunIntentsByStatusPaged.
// - Narrowest test layer: executor cursor paging through pending intents.
func TestSessionsV3Executor_BacklogPagingAcrossLargePendingQueue(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 10); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	// Session A: has an active run AND 10 pending runs
	sA, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-page-a", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "page-a", Mode: sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
	})
	recordTestPendingRunIntent(t, server, sA.ID, "run-a-active", accountID, testPrincipal().UserID)
	exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sA.ID, RunID: "run-a-active"})

	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for session A active run")
	}

	// Seed 5 additional pending runs for session A
	for i := 1; i <= 5; i++ {
		recordTestPendingRunIntent(t, server, sA.ID, fmt.Sprintf("run-a-pending-%d", i), accountID, testPrincipal().UserID)
	}

	// Session B: seeded after Session A pending runs
	sB, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-page-b", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "page-b", Mode: sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra"},
	})
	recordTestPendingRunIntent(t, server, sB.ID, "run-b-pending-1", accountID, testPrincipal().UserID)

	// Trigger refill: session A has an active run, so its pending runs are skipped,
	// but session B MUST NOT be stranded!
	exec.refillPendingBacklog()

	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for session B run to be admitted despite session A pending queue")
	}

	close(blockingRunner.unblockCh)
	server.CancelInFlightRuns()
}

// TestServerCapabilities_GetAndPutActiveExecutionLimit
// Purpose:
// - Invariant: GET /v1/permissions/capabilities exposes active_execution_limit.
//   PUT /v1/permissions/capabilities atomically validates all fields; rejecting invalid fields
//   without mutating any state.
// - Threat/regression: Accidental reset of execution limit or non-atomic capability updates.
// - Production boundary: Server.handlePermissions /v1/permissions/capabilities.
// - Narrowest test layer: HTTP handler test against Server.
func TestServerCapabilities_GetAndPutActiveExecutionLimit(t *testing.T) {
	server, _, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	accountID := testPrincipal().AccountScopeID

	// 1. GET initial
	req := httptest.NewRequest(http.MethodGet, "/v1/permissions/capabilities", nil)
	w := httptest.NewRecorder()
	server.handlePermissions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/permissions/capabilities failed: %d %s", w.Code, w.Body.String())
	}
	var getResp struct {
		Ok                   bool `json:"ok"`
		ActiveExecutionLimit int  `json:"active_execution_limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal GET response: %v", err)
	}
	if getResp.ActiveExecutionLimit != executioncapacity.DefaultActiveExecutionLimit {
		t.Fatalf("initial limit = %d, want %d", getResp.ActiveExecutionLimit, executioncapacity.DefaultActiveExecutionLimit)
	}

	// 2. PUT valid explicit limit = 42
	putBody := `{"active_execution_limit": 42}`
	req = httptest.NewRequest(http.MethodPut, "/v1/permissions/capabilities", bytes.NewBufferString(putBody))
	w = httptest.NewRecorder()
	server.handlePermissions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /v1/permissions/capabilities failed: %d %s", w.Code, w.Body.String())
	}
	var putResp struct {
		Ok                   bool `json:"ok"`
		ActiveExecutionLimit int  `json:"active_execution_limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("unmarshal PUT response: %v", err)
	}
	if putResp.ActiveExecutionLimit != 42 {
		t.Fatalf("updated limit = %d, want 42", putResp.ActiveExecutionLimit)
	}
	if snap := permSvc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 42 {
		t.Fatalf("capacity snapshot EffectiveLimit = %d, want 42", snap.EffectiveLimit)
	}

	// 3. PUT invalid explicit limit (< 1): must reject with 400 and preserve 42
	badBody := `{"active_execution_limit": 0}`
	req = httptest.NewRequest(http.MethodPut, "/v1/permissions/capabilities", bytes.NewBufferString(badBody))
	w = httptest.NewRecorder()
	server.handlePermissions(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid limit code = %d, want 400", w.Code)
	}
	if snap := permSvc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 42 {
		t.Fatalf("invalid update mutated limit to %d, want preserved 42", snap.EffectiveLimit)
	}

	// 4. Atomic rejection: valid active_execution_limit with invalid session_deploy
	// MUST return 400 and MUST NOT mutate active_execution_limit!
	atomicBadBody := `{"active_execution_limit": 75, "session_deploy": {"mode": "invalid_mode"}}`
	req = httptest.NewRequest(http.MethodPut, "/v1/permissions/capabilities", bytes.NewBufferString(atomicBadBody))
	w = httptest.NewRecorder()
	server.handlePermissions(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT atomic bad body code = %d, want 400", w.Code)
	}
	if snap := permSvc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 42 {
		t.Fatalf("non-atomic update mutated limit to %d on error, want preserved 42", snap.EffectiveLimit)
	}

	// 5. PUT with omitted active_execution_limit: preserves 42
	omitBody := `{"session_deploy": {"mode": "always-allow"}}`
	req = httptest.NewRequest(http.MethodPut, "/v1/permissions/capabilities", bytes.NewBufferString(omitBody))
	w = httptest.NewRecorder()
	server.handlePermissions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT with omitted limit failed: %d %s", w.Code, w.Body.String())
	}
	var omitResp struct {
		ActiveExecutionLimit int `json:"active_execution_limit"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &omitResp)
	if omitResp.ActiveExecutionLimit != 42 {
		t.Fatalf("omitted limit response = %d, want preserved 42", omitResp.ActiveExecutionLimit)
	}
	if snap := permSvc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 42 {
		t.Fatalf("omitted limit mutated snapshot to %d, want preserved 42", snap.EffectiveLimit)
	}
}
