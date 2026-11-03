package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// liveApprovalFixture is a checkout-free Companion whose real foreground run,
// started by the person's message from their PC app, waits inside the run
// loop on the real machine_run device call (permission.Service.CreatePending
// through the client tool gate, recorded by the run's own writer). Nothing in
// the test claims or drives the run: the daemon's executor owns its goroutine.
type liveApprovalFixture struct {
	*queuedNoteFixture
	script *pcRoundTripScript
	runID  string
	call   pebblestore.PermissionRecord
}

func newLiveApprovalFixture(t *testing.T, path string) *liveApprovalFixture {
	t.Helper()
	f := checkoutFreeCompanionFixture(t, path)
	script := &pcRoundTripScript{}
	f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return script.model(req), nil
	}
	f.server.ConfigureCompanionMaintenance(f.sessions.Store().Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	view, _ := checkoutFreeEnsure(t, f)
	f.sessionID = view.SessionID
	connectSendingDeviceTestClient(t, f.server, f.permissions, testPrincipal())

	out := &liveApprovalFixture{queuedNoteFixture: f, script: script}
	out.runID = deviceSay(t, f, "disk-1", "Can you check my disk space on this PC")
	out.call = waitDeviceCall(t, f, "pc-disk")
	if out.call.RunID != out.runID || out.call.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("device call = %+v, want pending in run %s", out.call, out.runID)
	}
	if !sessionV3ExecutorRunLive(f.server.v3SessionExecutor, f.sessionID, out.runID) {
		t.Fatal("the run waiting on the device call is not the executor's live run")
	}
	// The permission store has the request before the run's writer records
	// its permission.requested event: wait for that event.
	for deadline := time.Now().Add(10 * time.Second); !sessionHasPermissionEvent(t, f, "permission.requested", out.call.ID); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the run never recorded permission.requested for %s", out.call.ID)
		}
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	if c, ok := companionCommitmentNamed(state, "approval:"+out.call.ID); !ok || c.State != pebblestore.CompanionCommitmentOpen || c.Opened.EventType != "permission.requested" {
		t.Fatalf("approval while the run waits = %+v %v", c, ok)
	}
	return out
}

func sessionHasPermissionEvent(t *testing.T, f *queuedNoteFixture, eventType, permissionID string) bool {
	t.Helper()
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		var payload struct {
			Permission *struct {
				ID string `json:"id"`
			} `json:"permission"`
		}
		if event.EventType == eventType && json.Unmarshal(event.Payload, &payload) == nil && payload.Permission != nil && payload.Permission.ID == permissionID {
			return true
		}
	}
	return false
}

// sessionV3ExecutorRunLive reports whether the executor still has the run in
// flight (its goroutine, deferred cancellation included, has not finished).
func sessionV3ExecutorRunLive(e *sessionV3Executor, sessionID, runID string) bool {
	key := sessionV3ExecutorRunKey(sessionID, runID)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inFlightRuns[key] || e.runStates[key] != nil
}

// waitRunGoroutineDone is the barrier on the executor's own bookkeeping: the
// run's goroutine, and with it run.Service.runTurn's deferred
// CancelRunPending, has returned.
func (f *liveApprovalFixture) waitRunGoroutineDone(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); sessionV3ExecutorRunLive(f.server.v3SessionExecutor, f.sessionID, f.runID); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("run %s never finished", f.runID)
		}
	}
	f.server.waitCompanionMaintenance()
}

// requireLiveApprovalClosedOnce checks the stored decision, exactly one
// permission.updated event that closes the request (a non-pending status)
// with the original run and call, the Companion approval resolved from that
// event, the run not running, and that the gated call never reached the
// model. It returns the closing event's seq.
func requireLiveApprovalClosedOnce(t *testing.T, f *queuedNoteFixture, script *pcRoundTripScript, runID string, record pebblestore.PermissionRecord, wantStatus string) uint64 {
	t.Helper()
	stored, ok, err := f.permissions.GetPermission(f.sessionID, record.ID)
	if err != nil || !ok || stored.Status != wantStatus {
		t.Fatalf("stored permission = %q %v %v, want %s", stored.Status, ok, err, wantStatus)
	}
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var closing []pebblestore.V3SessionEvent
	for _, event := range events {
		if event.EventType != "permission.updated" {
			continue
		}
		var payload struct {
			RunID      string                        `json:"run_id"`
			CallID     string                        `json:"call_id"`
			Permission *pebblestore.PermissionRecord `json:"permission"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Permission == nil || payload.Permission.ID != record.ID || payload.Permission.Status == pebblestore.PermissionStatusPending {
			continue
		}
		if payload.RunID != runID || payload.CallID != record.CallID || payload.Permission.RunID != runID || payload.Permission.Status != wantStatus {
			t.Fatalf("closing event provenance = run %q call %q permission run %q status %q", payload.RunID, payload.CallID, payload.Permission.RunID, payload.Permission.Status)
		}
		closing = append(closing, event)
	}
	if len(closing) != 1 {
		t.Fatalf("permission %s has %d closing permission.updated events, want exactly 1", record.ID, len(closing))
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	c, _ := companionCommitmentNamed(state, "approval:"+record.ID)
	if c.State != pebblestore.CompanionCommitmentResolved || c.Resolution != wantStatus || c.Resolved == nil || c.Resolved.EventSeq != closing[0].Seq {
		t.Fatalf("approval %s = %+v, want resolved %s from event %d", record.ID, c, wantStatus, closing[0].Seq)
	}
	if state.Counters.UnmatchedResolution != 0 {
		t.Fatalf("unmatched resolutions = %d", state.Counters.UnmatchedResolution)
	}
	if intent, ok, err := f.sessions.GetSessionRunIntent(f.sessionID, runID); err != nil || !ok || intent.Status == sessionruntime.RunIntentRunning {
		t.Fatalf("run %s = %q %v %v, want ended", runID, intent.Status, ok, err)
	}
	if script != nil {
		if seen, ok := script.toolOutputSeen(record.CallID); ok {
			t.Fatalf("the cancelled call reached the model: %q", seen)
		}
	}
	return closing[0].Seq
}

// lateDeviceAnswer is the person's app answering the already closed call; it
// must change nothing.
func (f *liveApprovalFixture) lateDeviceAnswer(t *testing.T, closedSeq uint64, wantStatus string) {
	t.Helper()
	steps := f.script.count()
	raw, _ := json.Marshal(map[string]any{"action": "allow_once", "approved_arguments": map[string]any{"result": map[string]any{"exit_code": 0, "stdout": "too late"}}})
	code, out := deviceDo(f.queuedNoteFixture, http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+f.call.ID+"/resolve", string(raw), sendingDeviceTestClient)
	t.Logf("late device answer: %d %.200s", code, out)
	f.server.waitCompanionMaintenance()
	if again := requireLiveApprovalClosedOnce(t, f.queuedNoteFixture, f.script, f.runID, f.call, wantStatus); again != closedSeq {
		t.Fatalf("a late answer re-closed the approval: %d then %d", closedSeq, again)
	}
	if f.script.count() != steps || sessionV3ExecutorRunLive(f.server.v3SessionExecutor, f.sessionID, f.runID) {
		t.Fatalf("a late answer resumed the run: %d model steps (was %d)", f.script.count(), steps)
	}
}

// Purpose: stopping a live run whose real tool call waits on the person closes
// the Companion approval exactly once.
// Requirement: POST /v3/sessions/{id}/run/stop on the executor's live run
// cancels the run's context first (sessionV3Executor.CancelRun), records it
// cancelled, then cancels its pending request (permission.Service.
// CancelRunPending). Whichever caller moves the request off pending,
// permission.Service tells its RunPendingCancelledObserver, which publishes
// exactly one permission.updated event with the original run and call and the
// stored "cancelled" decision; the Companion resolves the approval as
// cancelled from it. A late answer from the device changes nothing, the model
// never sees the call's output and the run is not resumed. Threat: the
// cancellation written only to the permission store (the approval stays open
// forever), or a second publication (a duplicate resolution). Boundary:
// handleSessionV3PrimaryRunStop, sessionV3Executor.CancelRun,
// permission.Service.CancelRunPending, publishSessionV3RunPendingCancelled,
// MaintainCompanionState. Narrowest layer: the daemon's HTTP routes,
// executor, run loop and permission service over a real Pebble store with a
// scripted provider and the device's client session; the run goroutine is the
// executor's own.
func TestCompanionApprovalClosesWhenItsLiveRunIsStopped(t *testing.T) {
	f := newLiveApprovalFixture(t, filepath.Join(t.TempDir(), "live-stop.pebble"))
	steps := f.script.count()
	body, _ := json.Marshal(map[string]any{"run_id": f.runID, "target_swarm_id": "host-swarm-id", "reason": "the person stopped it"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/run/stop", string(body)); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, out)
	}
	f.waitRunGoroutineDone(t)
	if f.script.count() != steps {
		t.Fatalf("the stopped run called the model: %d steps (was %d)", f.script.count(), steps)
	}
	closed := requireLiveApprovalClosedOnce(t, f.queuedNoteFixture, f.script, f.runID, f.call, pebblestore.PermissionStatusCancelled)
	f.lateDeviceAnswer(t, closed, pebblestore.PermissionStatusCancelled)
}

// Purpose: a live executor run that ends without a stop while its tool call
// waits (here the executor cancels it, as the daily usage limit does) leaves
// a request the Companion honestly keeps open, and the person's later answer
// closes it exactly once.
// Requirement: the V3 executor's run loop (runProviderToolLoop) does not
// cancel the run's pending requests when the run ends, so the request stays
// pending in the permission store and the approval stays open with no closing
// event. The owner's later deny through the real resolve route writes exactly
// one permission.updated event with the original run and call and "denied"
// (the settlement, the run no longer running), and the Companion resolves the
// approval as denied; the device's late answer and the restart reconciliation
// add nothing; the model never sees the call and the run is not resumed.
// Threat: an approval shown resolved while its request is still pending, or
// one that stays open forever after the answer. Boundary:
// sessionV3Executor.CancelRunsForSession, handleSessionV3PrimaryPermissionResolve,
// publishSessionV3PermissionDecision, settleSessionV3PermissionAfterRun,
// permission.Service.ReconcilePendingRuns, MaintainCompanionState. Narrowest
// layer: as the stop test, with no stop handler involved.
func TestCompanionApprovalOfALiveRunThatEndsWithoutAStop(t *testing.T) {
	f := newLiveApprovalFixture(t, filepath.Join(t.TempDir(), "live-end.pebble"))
	steps := f.script.count()
	if n := f.server.v3SessionExecutor.CancelRunsForSession(f.sessionID, "daily usage limit exceeded"); n != 1 {
		t.Fatalf("executor cancelled %d runs, want the waiting one", n)
	}
	f.waitRunGoroutineDone(t)
	if f.script.count() != steps {
		t.Fatalf("the ended run called the model: %d steps (was %d)", f.script.count(), steps)
	}
	if intent, ok, err := f.sessions.GetSessionRunIntent(f.sessionID, f.runID); err != nil || !ok || intent.Status == sessionruntime.RunIntentRunning {
		t.Fatalf("run %s = %q %v %v, want ended", f.runID, intent.Status, ok, err)
	}
	if stored, _, _ := f.permissions.GetPermission(f.sessionID, f.call.ID); stored.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("request after the run ended = %s, want still pending", stored.Status)
	}
	if c, _ := companionCommitmentNamed(companionStateNow(t, f.confirmationFixture, f.sessionID).State, "approval:"+f.call.ID); c.State != pebblestore.CompanionCommitmentOpen {
		t.Fatalf("approval of the still pending request = %+v, want open", c)
	}

	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+f.call.ID+"/resolve", `{"action":"deny","reason":"not now"}`); code != http.StatusOK {
		t.Fatalf("late deny: %d %s", code, out)
	}
	closed := requireLiveApprovalClosedOnce(t, f.queuedNoteFixture, f.script, f.runID, f.call, pebblestore.PermissionStatusDenied)
	f.lateDeviceAnswer(t, closed, pebblestore.PermissionStatusDenied)
	if err := f.permissions.ReconcilePendingRuns("daemon restarted"); err != nil {
		t.Fatal(err)
	}
	f.server.waitCompanionMaintenance()
	if again := requireLiveApprovalClosedOnce(t, f.queuedNoteFixture, f.script, f.runID, f.call, pebblestore.PermissionStatusDenied); again != closed {
		t.Fatalf("the restart reconciliation re-closed the approval: %d then %d", closed, again)
	}
}

// Purpose: a run.Service.runTurn run (the legacy run route, the run stream,
// compaction and task context rotation runs) that ends while its real tool
// call waits closes the request in the session.
// Requirement: runTurn's deferred CancelRunPending ("run terminated before
// permission resolution") cancels the pending request when the run's context
// is cancelled; permission.Service tells its observer, which writes exactly
// one permission.updated event with the original run and call and
// "cancelled"; a later answer changes nothing. Threat: the run's own deferred
// cancellation writing only to the permission store, so the request is no
// longer pending, a later answer is not "decided", and the session's request
// stays waiting forever. Boundary: run.Service.runTurn,
// permission.Service.CancelRunPending, publishSessionV3RunPendingCancelled,
// settleSessionV3PermissionAfterRun. Narrowest layer: the run service's
// RunTurn with the daemon's permission service and V3 mutation boundary over
// a real Pebble store and a scripted provider asking a real ask-user question.
func TestRunTurnEndingWhileAToolWaitsClosesTheRequestInTheSession(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "run-turn.pebble"), nil)
	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	createBody, _ := json.Marshal(map[string]any{
		"client_request_id": "create-run-turn", "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
		"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
		"title": "run-turn", "mode": sessionruntime.ModeAuto, "agent_name": "frontdesk",
		"preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "low"},
	})
	code, createdBody := f.do(http.MethodPost, "/v3/sessions", string(createBody))
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(createdBody), &created) != nil || created.Session.ID == "" {
		t.Fatalf("create session: %d %s", code, createdBody)
	}
	sessionID := created.Session.ID
	// The scripted model asks the person a question (ask-user), which waits
	// on the person, and answers once any tool output is back.
	f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "done", StopReason: "stop"}, nil
		}
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "ask1", Name: "ask-user", Arguments: `{"question":"Which database should I use?","options":["PostgreSQL","SQLite"]}`}}}, nil
	}
	q := &queuedNoteFixture{confirmationFixture: f}
	q.sessionID = sessionID
	const runID = "run-turn-waiting"
	ctx, cancel := context.WithCancel(identity.ContextWithPrincipal(context.Background(), testPrincipal()))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.server.runner.RunTurn(ctx, sessionID, runruntime.RunRequest{Prompt: "lookup AB12CD"}, runruntime.RunStartMeta{RunID: runID, Principal: testPrincipal(), ApplySessionMutation: f.server.applySessionV3PrimaryMutation})
		done <- err
	}()
	var record pebblestore.PermissionRecord
	for deadline := time.Now().Add(10 * time.Second); record.ID == ""; time.Sleep(5 * time.Millisecond) {
		select {
		case err := <-done:
			msgs, _ := f.sessions.ListSessionMessages(sessionID, 0, 50)
			for _, m := range msgs {
				t.Logf("message %s: %.300s", m.Role, m.Content)
			}
			t.Fatalf("the run ended before its tool call waited: %v", err)
		default:
		}
		if pending, err := f.permissions.ListPending(sessionID, 10); err == nil && len(pending) > 0 {
			record = pending[0]
		} else if time.Now().After(deadline) {
			t.Fatal("no pending client tool call")
		}
	}
	if record.RunID != runID || !strings.Contains(record.ToolName, "ask") {
		t.Fatalf("waiting call = %+v, want the question in %s", record, runID)
	}
	calls := f.callCount()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the cancelled run reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled run never returned")
	}
	closed := requireRunTurnRequestClosedOnce(t, q, runID, record)
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/permissions/"+record.ID+"/resolve", `{"action":"allow_once"}`); code != http.StatusOK {
		t.Fatalf("late answer: %d %s", code, out)
	}
	if again := requireRunTurnRequestClosedOnce(t, q, runID, record); again != closed {
		t.Fatalf("a late answer re-closed the request: %d then %d", closed, again)
	}
	if f.callCount() != calls {
		t.Fatalf("the ended run called the model again: %d (was %d)", f.callCount(), calls)
	}
}

func requireRunTurnRequestClosedOnce(t *testing.T, f *queuedNoteFixture, runID string, record pebblestore.PermissionRecord) uint64 {
	t.Helper()
	stored, ok, err := f.permissions.GetPermission(f.sessionID, record.ID)
	if err != nil || !ok || stored.Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("stored permission = %q %v %v, want cancelled", stored.Status, ok, err)
	}
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var closing []uint64
	for _, event := range events {
		var payload struct {
			RunID      string                        `json:"run_id"`
			CallID     string                        `json:"call_id"`
			Permission *pebblestore.PermissionRecord `json:"permission"`
		}
		if event.EventType != "permission.updated" || json.Unmarshal(event.Payload, &payload) != nil || payload.Permission == nil || payload.Permission.ID != record.ID || payload.Permission.Status == pebblestore.PermissionStatusPending {
			continue
		}
		if payload.RunID != runID || payload.CallID != record.CallID || payload.Permission.Status != pebblestore.PermissionStatusCancelled {
			t.Fatalf("closing event provenance = run %q call %q status %q", payload.RunID, payload.CallID, payload.Permission.Status)
		}
		closing = append(closing, event.Seq)
	}
	if len(closing) != 1 {
		t.Fatalf("permission %s has %d closing permission.updated events, want exactly 1", record.ID, len(closing))
	}
	return closing[0]
}

// Purpose: a daemon restart while a tool call waits closes the Companion
// approval, both when the daemon shuts down its live run and when it crashed
// with the run's request still pending.
// Requirement: (1) shutdown: Server.CancelInFlightRuns ends the executor's
// live run, whose request stays pending in the store; the next daemon over the
// same store reconciles it at start (permission.Service.ReconcilePendingRuns,
// "daemon restarted"), and the observer publishes exactly one closing event,
// so the Companion shows the approval resolved cancelled. (2) crash: a run
// left running with its request pending and no goroutine (the state after a
// crash) is reconciled at start by
// permission.Service.ReconcilePendingRuns ("daemon restarted"), whose
// cancellation reaches the observer (delivered when installed if the
// reconciliation ran first, see
// TestRunPendingCancelledObserverSeesEveryCanceller) and closes the approval
// with exactly one event; reconciling again adds nothing. Threat: the
// restart reconciliation cancelling requests only in the permission store,
// leaving the derived needs-you open forever with no way to close it.
// Boundary: Server.CancelInFlightRuns, run.Service.runTurn,
// permission.Service.ReconcilePendingRuns and CancelRunPending,
// publishSessionV3RunPendingCancelled, MaintainCompanionState. Narrowest
// layer: daemons opened in turn over one real Pebble store.
func TestCompanionApprovalClosesAcrossARestart(t *testing.T) {
	t.Run("shutdown_with_live_run", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "restart-live.pebble")
		f := newLiveApprovalFixture(t, path)
		f.close()

		g := checkoutFreeCompanionFixture(t, path)
		g.server.ConfigureCompanionMaintenance(g.sessions.Store().Underlying())
		t.Cleanup(g.server.waitCompanionMaintenance)
		view, created := checkoutFreeEnsure(t, g)
		if created || view.SessionID != f.sessionID {
			t.Fatalf("restart opened another Companion: %+v created=%v", view, created)
		}
		g.sessionID = view.SessionID
		if err := g.permissions.ReconcilePendingRuns("daemon restarted"); err != nil {
			t.Fatal(err)
		}
		g.server.waitCompanionMaintenance()
		requireLiveApprovalClosedOnce(t, g, nil, f.runID, f.call, pebblestore.PermissionStatusCancelled)
	})

	t.Run("crash_with_pending_request", func(t *testing.T) {
		f := newCompanionApprovalRunEndFixture(t, "approval-crash-run")
		record := f.request(t, 1, "call-crash")
		path := f.path
		f.close()

		g := &queuedNoteFixture{confirmationFixture: newConfirmationFixture(t, path, nil)}
		g.sessionID = f.sessionID
		g.server.ConfigureCompanionMaintenance(g.sessions.Store().Underlying())
		t.Cleanup(g.server.waitCompanionMaintenance)
		if stored, _, _ := g.permissions.GetPermission(g.sessionID, record.ID); stored.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("request before the restart reconciliation = %s, want pending", stored.Status)
		}
		modelCalls := g.callCount()
		if err := g.permissions.ReconcilePendingRuns("daemon restarted"); err != nil {
			t.Fatal(err)
		}
		g.server.waitCompanionMaintenance()
		closed := requireCrashApprovalClosedOnce(t, g, f.job.RunID, record)
		// The reconciliation runs once per daemon; a second one has nothing
		// left to cancel and adds nothing.
		if _, err := g.permissions.CancelRunPending(g.sessionID, f.job.RunID, "daemon restarted"); err != nil {
			t.Fatal(err)
		}
		g.server.waitCompanionMaintenance()
		if again := requireCrashApprovalClosedOnce(t, g, f.job.RunID, record); again != closed {
			t.Fatalf("a second reconciliation re-closed the approval: %d then %d", closed, again)
		}
		if g.callCount() != modelCalls {
			t.Fatalf("the restart ran the model: %d calls (was %d)", g.callCount(), modelCalls)
		}
	})
}

func requireCrashApprovalClosedOnce(t *testing.T, f *queuedNoteFixture, runID string, record pebblestore.PermissionRecord) uint64 {
	t.Helper()
	stored, ok, err := f.permissions.GetPermission(f.sessionID, record.ID)
	if err != nil || !ok || stored.Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("stored permission = %q %v %v, want cancelled", stored.Status, ok, err)
	}
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var closing []pebblestore.V3SessionEvent
	for _, event := range events {
		var payload struct {
			RunID      string                        `json:"run_id"`
			CallID     string                        `json:"call_id"`
			Permission *pebblestore.PermissionRecord `json:"permission"`
		}
		if event.EventType != "permission.updated" || json.Unmarshal(event.Payload, &payload) != nil || payload.Permission == nil || payload.Permission.ID != record.ID || payload.Permission.Status == pebblestore.PermissionStatusPending {
			continue
		}
		if payload.RunID != runID || payload.CallID != record.CallID || payload.Permission.Status != pebblestore.PermissionStatusCancelled {
			t.Fatalf("closing event provenance = run %q call %q status %q", payload.RunID, payload.CallID, payload.Permission.Status)
		}
		closing = append(closing, event)
	}
	if len(closing) != 1 {
		t.Fatalf("permission %s has %d closing permission.updated events, want exactly 1", record.ID, len(closing))
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	c, _ := companionCommitmentNamed(state, "approval:"+record.ID)
	if c.State != pebblestore.CompanionCommitmentResolved || c.Resolution != pebblestore.PermissionStatusCancelled || c.Resolved == nil || c.Resolved.EventSeq != closing[0].Seq {
		t.Fatalf("approval %s = %+v, want resolved cancelled from event %d", record.ID, c, closing[0].Seq)
	}
	return closing[0].Seq
}
