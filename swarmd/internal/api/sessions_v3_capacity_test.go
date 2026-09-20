package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TestSessionsV3Executor_CapacityCapContention
// Purpose:
// - Invariant: Account-scoped ActiveExecutionLimit restricts total concurrent active runs.
// - Threat/regression: Uncontrolled concurrent executions exhaust host resources or overshoot limits.
// - Production boundary: sessionV3Executor.run, permission.Service, executioncapacity.Manager.
// - Narrowest test layer: sessionV3Executor integration with Pebble store and capacity manager.
func TestSessionsV3Executor_CapacityCapContention(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 200 * time.Millisecond
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	// Set execution limit to 2
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 2); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	sessions := make([]pebblestore.SessionSnapshot, 4)
	jobs := make([]sessionV3ExecutorJob, 4)
	for i := 0; i < 4; i++ {
		sessID := "session-cap-" + string(rune('a'+i))
		runID := "run-cap-" + string(rune('a'+i))
		s, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      sessID,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			WorkspacePath:  t.TempDir(),
			WorkspaceName:  sessID,
			Mode:           sessionruntime.ModeAuto,
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		sessions[i] = s
		intent := pebblestore.V3SessionRunIntent{
			SessionID:      sessID,
			RunID:          runID,
			Status:         sessionruntime.RunIntentPendingExecutor,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			UpdatedAt:      time.Now().UnixMilli(),
		}
		if _, err := sessionSvc.SaveRunIntent(intent); err != nil {
			t.Fatalf("save intent %d: %v", i, err)
		}
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

	// Wait briefly for first 2 to admit
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.TotalActive == 2 && snap.Pending == 2 {
			break
		}
		select {
		case <-deadline:
			snap := permSvc.ExecutionCapacitySnapshot(accountID)
			t.Fatalf("timed out waiting for capacity contention: got TotalActive=%d Pending=%d, want 2 and 2", snap.TotalActive, snap.Pending)
		case <-tick.C:
		}
	}

	// Cancel/finish the first 2 runs
	_, _, _ = exec.CancelRun(jobs[0], "done")
	_, _, _ = exec.CancelRun(jobs[1], "done")

	// Verify next pending runs are admitted
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.Pending < 2 {
			break
		}
		select {
		case <-deadline:
			snap := permSvc.ExecutionCapacitySnapshot(accountID)
			t.Fatalf("timed out waiting for pending to be admitted after release: %+v", snap)
		case <-tick.C:
		}
	}
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
	exec := newSessionV3Executor(server)
	exec.startDelay = 200 * time.Millisecond
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
	})
	if err != nil {
		t.Fatalf("create ordinary session: %v", err)
	}
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID:      sOrd.ID,
		RunID:          "run-ordinary",
		Status:         sessionruntime.RunIntentPendingExecutor,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		UpdatedAt:      time.Now().UnixMilli(),
	})

	// 2. Deployed session (has lineage_kind: session_deploy)
	sDep, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "sess-deployed",
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "deployed",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"lineage_kind": "session_deploy",
			"parent_session_id": sOrd.ID,
		},
	})
	if err != nil {
		t.Fatalf("create deployed session: %v", err)
	}
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID:      sDep.ID,
		RunID:          "run-deployed",
		Status:         sessionruntime.RunIntentPendingExecutor,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		UpdatedAt:      time.Now().UnixMilli(),
	})

	// 3. Delegated subagent session (must NOT be counted as deployed!)
	sSub, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "sess-subagent",
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "subagent",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"lineage_kind": "delegated_subagent",
			"parent_session_id": sOrd.ID,
		},
	})
	if err != nil {
		t.Fatalf("create subagent session: %v", err)
	}
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID:      sSub.ID,
		RunID:          "run-subagent",
		Status:         sessionruntime.RunIntentPendingExecutor,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		UpdatedAt:      time.Now().UnixMilli(),
	})

	// Verify IsDeployedSession
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

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.TotalActive == 3 && snap.DeployedActive == 1 {
			break
		}
		select {
		case <-deadline:
			snap := permSvc.ExecutionCapacitySnapshot(accountID)
			t.Fatalf("expected TotalActive=3 and DeployedActive=1, got: %+v", snap)
		case <-tick.C:
		}
	}
}

// TestSessionsV3Executor_PendingStopCancelDoesNotLeakSlot
// Purpose:
// - Invariant: Cancelling a pending queued run aborts capacity admission without leaking execution slots.
// - Threat/regression: Cancelled run leaves phantom active slot or orphaned waiter in capacity pool.
// - Production boundary: sessionV3Executor.CancelRun, executioncapacity.Manager.Acquire context cancellation.
// - Narrowest test layer: sessionV3Executor pending cancel integration.
func TestSessionsV3Executor_PendingStopCancelDoesNotLeakSlot(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountID, 1); err != nil {
		t.Fatalf("set execution limit: %v", err)
	}

	// Session 1: active run
	s1, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-cancel-1", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "cancel-1", Mode: sessionruntime.ModeAuto,
	})
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID: s1.ID, RunID: "run-1", Status: sessionruntime.RunIntentPendingExecutor,
		UserID: testPrincipal().UserID, AccountScopeID: accountID, UpdatedAt: time.Now().UnixMilli(),
	})
	j1 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s1.ID, RunID: "run-1"}
	exec.EnqueueRun(j1)

	// Wait for s1 to be active
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.TotalActive == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for session 1 to become active")
		case <-tick.C:
		}
	}

	// Session 2: enqueued, must queue as pending
	s2, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-cancel-2", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "cancel-2", Mode: sessionruntime.ModeAuto,
	})
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID: s2.ID, RunID: "run-2", Status: sessionruntime.RunIntentPendingExecutor,
		UserID: testPrincipal().UserID, AccountScopeID: accountID, UpdatedAt: time.Now().UnixMilli(),
	})
	j2 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s2.ID, RunID: "run-2"}
	exec.EnqueueRun(j2)

	// Wait for s2 to be pending
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

	// Cancel session 2 while it's pending in queue
	_, tracked, err := exec.CancelRun(j2, "user cancelled pending")
	if err != nil {
		t.Fatalf("cancel pending run: %v", err)
	}
	if !tracked {
		t.Fatalf("expected run 2 to be tracked by executor")
	}

	// Cancel session 1
	_, _, _ = exec.CancelRun(j1, "stop run 1")

	// Wait for all to drain
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
// - Invariant: Daily usage limit check before admission fails the run cleanly without acquiring or leaking capacity slots.
// - Threat/regression: Usage refusal leaves dangling capacity lease or unowned running state.
// - Production boundary: sessionV3Executor.run daily limit check, recordRunStatus.
// - Narrowest test layer: sessionV3Executor daily limit check integration.
func TestSessionsV3Executor_UsageRefusalDoesNotLeakSlot(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 10 * time.Millisecond
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID
	// Set daily limit to $0.01 and spend $0.02
	_ = sessionSvc.SetDailyLimit(accountID, 0.01)
	_ = sessionSvc.RecordUsageCost(accountID, 0.02)

	s, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-usage-refusal", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "usage-refusal", Mode: sessionruntime.ModeAuto,
	})
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID: s.ID, RunID: "run-usage", Status: sessionruntime.RunIntentPendingExecutor,
		UserID: testPrincipal().UserID, AccountScopeID: accountID, UpdatedAt: time.Now().UnixMilli(),
	})
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s.ID, RunID: "run-usage"}
	exec.EnqueueRun(job)

	deadline := time.After(2 * time.Second)
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
// - Invariant: On restart, stale running intents are failed as interrupted, and release events
//   refill backlog page-by-page so no pending runs are stranded.
// - Threat/regression: Restart leaves unowned running states or strands pending intents beyond the first page.
// - Production boundary: sessionV3Executor.recoverDurableRuns, refillPendingBacklog.
// - Narrowest test layer: executor recovery scan and release backlog refill.
func TestSessionsV3Executor_RestartAndBacklogRecovery(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	accountID := testPrincipal().AccountScopeID

	// Seed 1 interrupted running run
	sRunning, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "sess-restart-running", UserID: testPrincipal().UserID, AccountScopeID: accountID,
		WorkspacePath: t.TempDir(), WorkspaceName: "running", Mode: sessionruntime.ModeAuto,
	})
	_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
		SessionID: sRunning.ID, RunID: "run-interrupted", Status: sessionruntime.RunIntentRunning,
		UserID: testPrincipal().UserID, AccountScopeID: accountID, UpdatedAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
	})

	// Seed 3 pending runs
	for i := 1; i <= 3; i++ {
		sessID := "sess-restart-pending-" + string(rune('0'+i))
		s, _, _ := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID: sessID, UserID: testPrincipal().UserID, AccountScopeID: accountID,
			WorkspacePath: t.TempDir(), WorkspaceName: sessID, Mode: sessionruntime.ModeAuto,
		})
		_, _ = sessionSvc.SaveRunIntent(pebblestore.V3SessionRunIntent{
			SessionID: s.ID, RunID: "run-pending-" + string(rune('0'+i)), Status: sessionruntime.RunIntentPendingExecutor,
			UserID: testPrincipal().UserID, AccountScopeID: accountID, UpdatedAt: time.Now().UnixMilli(),
		})
	}

	// Create new executor (simulates daemon start)
	exec := newSessionV3Executor(server)
	exec.startDelay = 50 * time.Millisecond
	server.v3SessionExecutor = exec

	// Check running run was transitioned to interrupted
	intent, ok, err := sessionSvc.GetSessionRunIntent(sRunning.ID, "run-interrupted")
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentInterrupted {
		t.Fatalf("expected stale running intent to be interrupted, got ok=%v status=%s err=%v", ok, intent.Status, err)
	}

	// Check capacity has not leaked
	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive > 3 {
		t.Fatalf("unexpected active count after restart: %+v", snap)
	}

	server.CancelInFlightRuns()
}

// TestServerCapabilities_GetAndPutActiveExecutionLimit
// Purpose:
// - Invariant: GET /v1/permissions/capabilities exposes active_execution_limit.
//   PUT /v1/permissions/capabilities updates active_execution_limit when valid, rejects invalid limits with 400
//   without resetting, and preserves existing limit when omitted.
// - Threat/regression: Accidental reset of execution limit or lack of validation on capability update.
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
		t.Fatalf("updated limit = %d, want 42", putResp.ActiveExecutionLimit, )
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

	// 4. PUT with omitted active_execution_limit: preserves 42
	omitBody := `{"session_deploy": {"policy": "always-allow"}}`
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
