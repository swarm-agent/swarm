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
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// blockingCapacityTestRunner implements provideriface.Runner and
// provideriface.ExecutionEpochLifecycleRunner. It blocks execution until
// unblocked, allowing deterministic capacity slot assertions.
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
		enteredCh: make(chan struct{}, 200),
	}
}

func (r *blockingCapacityTestRunner) ID() string {
	if r.id != "" {
		return r.id
	}
	return "codex"
}

func (r *blockingCapacityTestRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode:       provideriface.ExecutionEpochContextResponsesChain,
		TransportReusable: true,
	}
}

func (r *blockingCapacityTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *blockingCapacityTestRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	select {
	case r.enteredCh <- struct{}{}:
	default:
	}
	select {
	case <-r.unblockCh:
		return provideriface.Response{
			ID:    "resp-" + req.Model,
			Model: req.Model,
			Text:  "capacity test done",
		}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}

func (r *blockingCapacityTestRunner) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.unblockCh:
	default:
		close(r.unblockCh)
	}
}

func recordTestPendingRunIntent(t *testing.T, server *Server, sessionID, runID, accountID, userID string) {
	t.Helper()
	now := time.Now().UnixMilli()
	epoch, found, err := server.sessions.GetActiveExecutionEpoch(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		boundary, err := server.sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{SessionID: sessionID, UserID: userID, AccountScopeID: accountID, ClientRequestID: "epoch-" + sessionID, PayloadHash: "epoch-" + sessionID, Reason: "session_created", SkipRunIntent: true})
		if err != nil {
			t.Fatal(err)
		}
		epoch = boundary.Epoch
	}
	msg := pebblestore.MessageSnapshot{ID: "msg-" + runID, SessionID: sessionID, UserID: userID, AccountScopeID: accountID, Role: "user", Content: "capacity test"}
	if _, err := server.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: userID, AccountScopeID: accountID, ClientRequestID: "msg-" + runID, IdempotencyKey: "msg-" + runID, PayloadHash: "msg-" + runID, RequestHash: "msg-" + runID, EpochID: epoch.EpochID, Kind: sessionruntime.SessionMutationAppendMessage, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	pending := pebblestore.V3SessionRunIntent{
		EpochID:         epoch.EpochID,
		SourceMessageID: msg.ID,
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		RunID:           runID,
		Status:          sessionruntime.RunIntentPendingExecutor,
		UpdatedAt:       now,
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

func createTestSession(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, accountID string, metadata map[string]any) pebblestore.SessionSnapshot {
	t.Helper()
	pref := pebblestore.ModelPreference{Provider: "codex", Model: "gpt-6-astra", Thinking: "high"}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["agent_profile"] = pebblestore.AgentProfile{Name: "swarm", Mode: "primary", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking}
	s, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  sessionID,
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pref,
		Metadata:       metadata,
	})
	if err != nil {
		t.Fatalf("create session %s: %v", sessionID, err)
	}
	return s
}

// TestSessionsV3Executor_CapacityCapContention
// Purpose:
// - Invariant: Account-scoped ActiveExecutionLimit restricts total concurrent active runs.
// - Threat/regression: Uncontrolled concurrent executions exhaust host resources or overshoot limits.
// - Production boundary: sessionV3Executor.run, permission.Service, executioncapacity.Manager.
// - Narrowest test layer: sessionV3Executor integration with Pebble store and capacity manager using blocking provider runner.
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

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	jobs := make([]sessionV3ExecutorJob, 4)
	for i := 0; i < 4; i++ {
		sessID := fmt.Sprintf("session-cap-%c", 'a'+i)
		runID := fmt.Sprintf("run-cap-%c", 'a'+i)
		createTestSession(t, sessionSvc, sessID, accountID, nil)
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
			intents, err := sessionSvc.ListSessionRunIntents(jobs[0].SessionID, 0, 10)
			t.Fatalf("timed out waiting for job to enter provider runner: %+v err=%v", intents, err)
		}
	}

	// Verify exactly 2 active and 2 pending in the capacity manager
	snap := permSvc.ExecutionCapacitySnapshot(accountID)
	if snap.TotalActive != 2 || snap.Pending != 2 {
		t.Fatalf("capacity contention mismatch: got TotalActive=%d Pending=%d, want 2 and 2", snap.TotalActive, snap.Pending)
	}

	// Cancel the two admitted runs, independent of goroutine scheduling order.
	var admitted []sessionV3ExecutorJob
	for _, job := range jobs {
		intent, ok, err := sessionSvc.GetSessionRunIntent(job.SessionID, job.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && intent.Status == sessionruntime.RunIntentRunning {
			admitted = append(admitted, job)
		}
	}
	for _, job := range admitted {
		if _, _, err := exec.CancelRun(job, "done"); err != nil {
			t.Fatal(err)
		}
	}

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
}

// TestSessionsV3Executor_AccountIsolationAndMixedLimit
// Purpose:
//   - Invariant: Capacity limits are strictly isolated per account; Account A's jobs do not count
//     towards Account B's capacity limit or starve Account B's execution.
//   - Threat/regression: Cross-account capacity interference or global starvation.
//   - Production boundary: executioncapacity.Manager per-account state, sessionV3Executor.
//   - Narrowest test layer: Multi-account concurrent admission through sessionV3Executor.
func TestSessionsV3Executor_AccountIsolationAndMixedLimit(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountA := "account-isolated-a"
	accountB := "account-isolated-b"

	// Account A limit = 2, Account B limit = 1
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountA, 2); err != nil {
		t.Fatalf("set limit A: %v", err)
	}
	if _, err := permSvc.UpdateActiveExecutionLimitForAccount(accountB, 1); err != nil {
		t.Fatalf("set limit B: %v", err)
	}

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// Account A: 3 sessions
	var jobsA []sessionV3ExecutorJob
	for i := 0; i < 3; i++ {
		sID := fmt.Sprintf("sess-a-%d", i)
		rID := fmt.Sprintf("run-a-%d", i)
		createTestSession(t, sessionSvc, sID, accountA, nil)
		recordTestPendingRunIntent(t, server, sID, rID, accountA, testPrincipal().UserID)
		p := testPrincipal()
		p.AccountScopeID = accountA
		jobsA = append(jobsA, sessionV3ExecutorJob{Principal: p, SessionID: sID, RunID: rID})
	}

	// Account B: 2 sessions
	var jobsB []sessionV3ExecutorJob
	for i := 0; i < 2; i++ {
		sID := fmt.Sprintf("sess-b-%d", i)
		rID := fmt.Sprintf("run-b-%d", i)
		createTestSession(t, sessionSvc, sID, accountB, nil)
		recordTestPendingRunIntent(t, server, sID, rID, accountB, testPrincipal().UserID)
		p := testPrincipal()
		p.AccountScopeID = accountB
		jobsB = append(jobsB, sessionV3ExecutorJob{Principal: p, SessionID: sID, RunID: rID})
	}

	for _, j := range jobsA {
		exec.EnqueueRun(j)
	}
	for _, j := range jobsB {
		exec.EnqueueRun(j)
	}

	// 2 from Account A + 1 from Account B = 3 jobs enter runner
	for i := 0; i < 3; i++ {
		select {
		case <-blockingRunner.enteredCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for initial 3 jobs across accounts")
		}
	}

	snapA := permSvc.ExecutionCapacitySnapshot(accountA)
	if snapA.TotalActive != 2 || snapA.Pending != 1 {
		t.Fatalf("account A snapshot mismatch: TotalActive=%d Pending=%d, want 2 and 1", snapA.TotalActive, snapA.Pending)
	}

	snapB := permSvc.ExecutionCapacitySnapshot(accountB)
	if snapB.TotalActive != 1 || snapB.Pending != 1 {
		t.Fatalf("account B snapshot mismatch: TotalActive=%d Pending=%d, want 1 and 1", snapB.TotalActive, snapB.Pending)
	}

	// Cancel one from Account A to release slot
	for _, job := range jobsA {
		intent, ok, err := sessionSvc.GetSessionRunIntent(job.SessionID, job.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && intent.Status == sessionruntime.RunIntentRunning {
			if _, _, err := exec.CancelRun(job, "done"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for account A pending run to be admitted")
	}

	// Account B is unaffected
	snapB2 := permSvc.ExecutionCapacitySnapshot(accountB)
	if snapB2.TotalActive != 1 || snapB2.Pending != 1 {
		t.Fatalf("account B mutated unexpectedly: %+v", snapB2)
	}

	for _, j := range append(jobsA, jobsB...) {
		_, _, _ = exec.CancelRun(j, "cleanup")
	}
}

// TestSessionsV3Executor_CombinedOrdinaryAndDeployedAccounting
// Purpose:
//   - Invariant: Deployed sessions are distinguished from ordinary sessions via canonical deployment metadata,
//     and both count toward TotalActive while only deployed sessions count toward DeployedActive.
//   - Threat/regression: Deployed sessions misclassified as ordinary, or delegated subagents mistakenly classified as deployed.
//   - Production boundary: sessionruntime.IsDeployedSession, sessionV3Executor.run, executioncapacity.Manager.
//   - Narrowest test layer: sessionV3Executor integration with deployed, ordinary, and delegated subagent sessions.
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

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// 1. Ordinary session
	sOrd := createTestSession(t, sessionSvc, "sess-ordinary", accountID, nil)
	recordTestPendingRunIntent(t, server, sOrd.ID, "run-ordinary", accountID, testPrincipal().UserID)

	// 2. Deployed session (lineage_kind: session_deploy)
	sDep := createTestSession(t, sessionSvc, "sess-deployed", accountID, map[string]any{
		"lineage_kind":      "session_deploy",
		"parent_session_id": sOrd.ID,
	})
	recordTestPendingRunIntent(t, server, sDep.ID, "run-deployed", accountID, testPrincipal().UserID)

	// 3. Delegated subagent session (must NOT count as deployed)
	sSub := createTestSession(t, sessionSvc, "sess-subagent", accountID, map[string]any{
		"lineage_kind":      "delegated_subagent",
		"parent_session_id": sOrd.ID,
	})
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

	_, _, _ = exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sOrd.ID, RunID: "run-ordinary"}, "cleanup")
	_, _, _ = exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sDep.ID, RunID: "run-deployed"}, "cleanup")
	_, _, _ = exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sSub.ID, RunID: "run-subagent"}, "cleanup")
}

// TestSessionsV3Executor_MandatoryAccountMatchingValidation
// Purpose:
// - Invariant: A run submitted with a mismatched Principal account is rejected before capacity admission.
// - Threat/regression: Cross-account spoofing or admission bypass under foreign credentials.
// - Production boundary: sessionV3Executor.run principal ownership check.
// - Narrowest test layer: sessionV3Executor execution with mismatched principal account.
func TestSessionsV3Executor_MandatoryAccountMatchingValidation(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountA := "account-owner"
	accountB := "account-attacker"

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	s := createTestSession(t, sessionSvc, "sess-mismatch", accountA, nil)
	recordTestPendingRunIntent(t, server, s.ID, "run-mismatch", accountA, testPrincipal().UserID)

	// Submit job with Principal belonging to accountB
	foreignPrincipal := testPrincipal()
	foreignPrincipal.AccountScopeID = accountB

	job := sessionV3ExecutorJob{
		Principal: foreignPrincipal,
		SessionID: s.ID,
		RunID:     "run-mismatch",
	}
	before, _, err := sessionSvc.GetSessionRunIntent(s.ID, job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if exec.EnqueueRun(job) {
		t.Fatal("foreign enqueue accepted")
	}
	after, ok, err := sessionSvc.GetSessionRunIntent(s.ID, job.RunID)
	if err != nil || !ok || before != after {
		t.Fatalf("rejected principal mutated owner's intent: before=%+v after=%+v err=%v", before, after, err)
	}
	select {
	case <-blockingRunner.enteredCh:
		t.Fatal("foreign job entered provider")
	default:
	}

	// Verify no slots leaked on either account
	if snapA := permSvc.ExecutionCapacitySnapshot(accountA); snapA.TotalActive != 0 || snapA.Pending != 0 {
		t.Fatalf("account A slots leaked: %+v", snapA)
	}
	if snapB := permSvc.ExecutionCapacitySnapshot(accountB); snapB.TotalActive != 0 || snapB.Pending != 0 {
		t.Fatalf("account B slots leaked: %+v", snapB)
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

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	s1 := createTestSession(t, sessionSvc, "sess-cancel-1", accountID, nil)
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

	s2 := createTestSession(t, sessionSvc, "sess-cancel-2", accountID, nil)
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

	// Verify all slots drained cleanly
	drainDeadline := time.After(3 * time.Second)
	for {
		snap := permSvc.ExecutionCapacitySnapshot(accountID)
		if snap.TotalActive == 0 && snap.Pending == 0 {
			break
		}
		select {
		case <-drainDeadline:
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

	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// Set daily limit to $0.01 and spend $0.02
	_, err := sessionSvc.SetUsageLimit(accountID, 0.01, 0, true)
	if err != nil {
		t.Fatalf("set usage limit: %v", err)
	}

	s := createTestSession(t, sessionSvc, "sess-usage-refusal", accountID, nil)
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
//   - Invariant: On restart, running intents are reconciled to interrupted preserving plan attempt metadata,
//     recovery does not kill live executor runs, and pending intents are refilled and executed without poll.
//   - Threat/regression: Restart leaves orphan running states or loses plan execution lineage.
//   - Production boundary: sessionV3Executor.recoverDurableRuns, refillPendingBacklog.
//   - Narrowest test layer: executor recovery scan on startup.
func TestSessionsV3Executor_RestartAndBacklogRecovery(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	accountID := testPrincipal().AccountScopeID

	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// Seed a pending intent first: running is a transition, not an initial state.
	sRunning := createTestSession(t, sessionSvc, "sess-restart-running", accountID, nil)
	recordTestPendingRunIntent(t, server, sRunning.ID, "run-fresh-interrupted", accountID, testPrincipal().UserID)
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
	_, seedErr := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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

	if seedErr != nil {
		t.Fatal(seedErr)
	}

	// Seed 2 pending runs
	for i := 1; i <= 2; i++ {
		sessID := fmt.Sprintf("sess-restart-pending-%d", i)
		createTestSession(t, sessionSvc, sessID, accountID, nil)
		recordTestPendingRunIntent(t, server, sessID, fmt.Sprintf("run-pending-%d", i), accountID, testPrincipal().UserID)
	}

	// Create new executor (simulating daemon startup)
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec

	// Check running run was reconciled to interrupted
	intent, ok, err := sessionSvc.GetSessionRunIntent(sRunning.ID, "run-fresh-interrupted")
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentInterrupted {
		t.Fatalf("expected fresh running intent to be reconciled to interrupted, got ok=%v status=%s err=%v", ok, intent.Status, err)
	}
}

// TestSessionsV3Executor_BacklogPagingAcrossLargePendingQueue
// Purpose:
//   - Invariant: Event-driven refill uses cursor paging across >500 pending intents and same-session runs
//     so runs for other sessions are not stranded.
//   - Threat/regression: First page of 500 contains same-session runs, causing subsequent sessions to starve.
//   - Production boundary: sessionV3Executor.refillPendingBacklog, ListSessionRunIntentsByStatusPaged.
//   - Narrowest test layer: executor cursor paging through pending intents.
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

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// The canonical writer rejects multiple active intents per session. Seed 501
	// distinct pending sessions and reserve their executor identities to isolate
	// paging beyond a completely in-flight first page without 501 goroutines.
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("sess-page-a-%04d", i)
		createTestSession(t, sessionSvc, id, accountID, nil)
		recordTestPendingRunIntent(t, server, id, "run-page", accountID, testPrincipal().UserID)
		exec.mu.Lock()
		exec.activeBySession[id] = "occupied-run"
		exec.mu.Unlock()
	}

	// Session B: seeded after Session A pending runs
	sB := createTestSession(t, sessionSvc, "sess-page-b", accountID, nil)
	recordTestPendingRunIntent(t, server, sB.ID, "run-b-pending-1", accountID, testPrincipal().UserID)

	// Trigger refill: session A has an active run, so its pending runs are skipped,
	// but session B MUST NOT be stranded!
	exec.refillPendingBacklog()

	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for session B run to be admitted despite session A pending queue")
	}

	_, _, _ = exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sB.ID, RunID: "run-b-pending-1"}, "cleanup")
}

// TestSessionsV3Executor_CapacityQueueFullRetainsPending
// Purpose:
//   - Invariant: When capacity admission returns ErrQueueFull, the run intent remains durable pending
//     for subsequent refill retry rather than failing accepted work.
//   - Threat/regression: ErrQueueFull causes permanent failure of queued user/subagent requests.
//   - Production boundary: sessionV3Executor.run, executioncapacity.ErrQueueFull handling.
//   - Narrowest test layer: executor admission against bounded capacity manager queue.
func TestSessionsV3Executor_CapacityQueueFullRetainsPending(t *testing.T) {
	server, sessionSvc, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	blockingRunner := newBlockingCapacityTestRunner("codex")
	providers := registry.New()
	providers.RegisterRunner(blockingRunner)
	server.providers = providers

	// Configure capacity manager with Limit=1 and MaxWaiters=1
	mgr := executioncapacity.NewManager(executioncapacity.ManagerConfig{
		DefaultLimit:    1,
		MaxQueueWaiters: 1,
	})
	permSvc.SetExecutionCapacity(mgr)

	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	accountID := testPrincipal().AccountScopeID

	defer func() {
		blockingRunner.close()
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// 1. Session 1: occupies slot 1
	s1 := createTestSession(t, sessionSvc, "sess-qf-1", accountID, nil)
	recordTestPendingRunIntent(t, server, s1.ID, "run-qf-1", accountID, testPrincipal().UserID)
	j1 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s1.ID, RunID: "run-qf-1"}
	exec.EnqueueRun(j1)

	select {
	case <-blockingRunner.enteredCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for session 1 to become active")
	}

	// 2. Session 2: enqueued, fills the 1 waiter slot
	s2 := createTestSession(t, sessionSvc, "sess-qf-2", accountID, nil)
	recordTestPendingRunIntent(t, server, s2.ID, "run-qf-2", accountID, testPrincipal().UserID)
	j2 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s2.ID, RunID: "run-qf-2"}
	exec.EnqueueRun(j2)

	// Wait for session 2 to become pending waiter
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
			t.Fatalf("timed out waiting for session 2 to become pending waiter")
		case <-tick.C:
		}
	}

	// 3. Session 3: enqueued, waiter queue full (MaxWaiters=1), AdmitExecution returns ErrQueueFull
	s3 := createTestSession(t, sessionSvc, "sess-qf-3", accountID, nil)
	recordTestPendingRunIntent(t, server, s3.ID, "run-qf-3", accountID, testPrincipal().UserID)
	j3 := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: s3.ID, RunID: "run-qf-3"}
	exec.EnqueueRun(j3)

	// Verify session 3 is NOT marked failed; it remains pending!
	time.Sleep(100 * time.Millisecond)
	intent3, ok3, err3 := sessionSvc.GetSessionRunIntent(s3.ID, "run-qf-3")
	if err3 != nil || !ok3 {
		t.Fatalf("get intent 3 failed: ok=%v err=%v", ok3, err3)
	}
	if intent3.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("expected intent 3 to remain pending on queue full, got status %q", intent3.Status)
	}

	_, _, _ = exec.CancelRun(j1, "cleanup")
	_, _, _ = exec.CancelRun(j2, "cleanup")
	_, _, _ = exec.CancelRun(j3, "cleanup")
}

// TestServerCapabilities_GetAndPutActiveExecutionLimit
// Purpose:
//   - Invariant: GET /v1/permissions/capabilities exposes active_execution_limit.
//     PUT /v1/permissions/capabilities atomically validates all fields; rejecting invalid fields
//     without mutating any state.
//   - Threat/regression: Accidental reset of execution limit or non-atomic capability updates.
//   - Production boundary: Server.handlePermissions /v1/permissions/capabilities.
//   - Narrowest test layer: HTTP handler test against Server.
func TestServerCapabilities_GetAndPutActiveExecutionLimit(t *testing.T) {
	server, _, permSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	accountID := testPrincipal().AccountScopeID

	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()

	// 1. GET initial
	req := httptest.NewRequest(http.MethodGet, "/v1/permissions/capabilities", nil)
	w := httptest.NewRecorder()
	server.handlePermissions(w, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
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
	server.handlePermissions(w, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
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
	server.handlePermissions(w, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
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
	server.handlePermissions(w, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT atomic bad body code = %d, want 400", w.Code)
	}
	if snap := permSvc.ExecutionCapacitySnapshot(accountID); snap.EffectiveLimit != 42 {
		t.Fatalf("non-atomic update mutated limit to %d on error, want preserved 42", snap.EffectiveLimit)
	}

	// 5. PUT with omitted active_execution_limit: preserves 42
	omitBody := `{"session_deploy": {"mode": "always_allow", "over_limit_action":"ask"}}`
	req = httptest.NewRequest(http.MethodPut, "/v1/permissions/capabilities", bytes.NewBufferString(omitBody))
	w = httptest.NewRecorder()
	server.handlePermissions(w, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
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

// Purpose: shutdown of the admission authority cannot turn an accepted pending
// intent into failure or spin the refill pump. Exercise executor.run with a real
// closed manager and cancellation, then verify the durable intent is unchanged.
func TestSessionsV3Executor_ClosedCapacityRetainsPending(t *testing.T) {
	server, sessions, perms, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	s := createTestSession(t, sessions, "closed-capacity", testPrincipal().AccountScopeID, nil)
	recordTestPendingRunIntent(t, server, s.ID, "closed-run", s.AccountScopeID, s.UserID)
	before, _, err := sessions.GetSessionRunIntent(s.ID, "closed-run")
	if err != nil {
		t.Fatal(err)
	}
	perms.ExecutionCapacity().Close()
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec
	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatal("shutdown did not join capacity wait")
	}
	after, ok, err := sessions.GetSessionRunIntent(s.ID, "closed-run")
	if err != nil || !ok || after.Status != before.Status || after.BlockedReason != "" {
		t.Fatalf("shutdown changed pending work: %+v err=%v", after, err)
	}
}
