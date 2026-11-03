package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// companionApprovalRunEndFixture is a Companion whose test-owned run waits
// on real permission requests: the run is claimed in the daemon's executor
// before it is recorded pending (so the executor's refill never becomes a
// second writer), each request is created by the permission service and
// recorded by the executor's provider tool event writer.
type companionApprovalRunEndFixture struct {
	*queuedNoteFixture
	executor   *sessionV3Executor
	job        sessionV3ExecutorJob
	modelCalls *atomic.Int64
}

func newCompanionApprovalRunEndFixture(t *testing.T, runID string) *companionApprovalRunEndFixture {
	t.Helper()
	calls := &atomic.Int64{}
	f := newCompanionRunFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		calls.Add(1)
		return provideriface.Response{Text: "Noted.", StopReason: "stop"}
	})
	f.server.ConfigureCompanionMaintenance(f.sessions.Store().Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	f.say(f.sessionID, "q-1", "Help me pick a backup drive for my laptop photos")
	companionStateNow(t, f.confirmationFixture, f.sessionID)

	out := &companionApprovalRunEndFixture{queuedNoteFixture: f, executor: &sessionV3Executor{server: f.server}, job: sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: f.sessionID, RunID: runID}, modelCalls: calls}
	release := claimSessionV3ExecutorRunForTest(t, f.server.v3SessionExecutor, out.job)
	t.Cleanup(release)
	if _, err := out.executor.recordRunStatus(out.job, sessionruntime.RunIntentPendingExecutor, "", "session.run_intent.recorded"); err != nil {
		t.Fatalf("run pending: %v", err)
	}
	f.server.v3SessionExecutor.drainRefillPasses(context.Background())
	if _, err := out.executor.recordRunStatus(out.job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
		t.Fatalf("run running: %v", err)
	}
	return out
}

// request records one real permission request of the running run and checks
// the Companion opened its approval commitment from that event.
func (f *companionApprovalRunEndFixture) request(t *testing.T, step int, callID string) pebblestore.PermissionRecord {
	t.Helper()
	record, err := f.permissions.CreatePending(permission.CreateInput{SessionID: f.sessionID, RunID: f.job.RunID, Step: step, CallID: callID, ToolName: "bash", ToolArguments: `{"command":"ls"}`, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.executor.recordProviderToolEvent(f.job, runruntime.StreamEvent{Type: runruntime.StreamEventPermissionReq, Step: step, ToolName: "bash", CallID: callID, Permission: &record}, "permission.requested", 0); err != nil {
		t.Fatal(err)
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	if c, ok := companionCommitmentNamed(state, "approval:"+record.ID); !ok || c.State != pebblestore.CompanionCommitmentOpen || c.Opened.EventType != "permission.requested" {
		t.Fatalf("approval %s while waiting = %+v %v", record.ID, c, ok)
	}
	return record
}

// permissionEvents returns the session's permission.updated events for one
// permission, and every session.tool.* event of the call it gated.
func (f *companionApprovalRunEndFixture) permissionEvents(t *testing.T, record pebblestore.PermissionRecord) (updated []pebblestore.V3SessionEvent, toolEvents int) {
	t.Helper()
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		var payload struct {
			CallID     string `json:"call_id"`
			Permission *struct {
				ID string `json:"id"`
			} `json:"permission"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		switch {
		case event.EventType == "permission.updated" && payload.Permission != nil && payload.Permission.ID == record.ID:
			updated = append(updated, event)
		case strings.HasPrefix(event.EventType, "session.tool.") && payload.CallID == record.CallID:
			toolEvents++
		}
	}
	return updated, toolEvents
}

// requireSettledOnce checks that exactly one permission.updated event closes
// the permission, carrying the exact original request (run and call) and the
// authoritative stored decision, and that the Companion resolved its approval
// from that event with that decision.
func (f *companionApprovalRunEndFixture) requireSettledOnce(t *testing.T, record pebblestore.PermissionRecord, wantStatus string) pebblestore.CompanionCommitment {
	t.Helper()
	stored, ok, err := f.permissions.GetPermission(f.sessionID, record.ID)
	if err != nil || !ok || stored.Status != wantStatus {
		t.Fatalf("stored permission %s = %+v %v %v, want %s", record.ID, stored.Status, ok, err, wantStatus)
	}
	updated, toolEvents := f.permissionEvents(t, record)
	if len(updated) != 1 {
		t.Fatalf("permission %s has %d permission.updated events, want exactly 1", record.ID, len(updated))
	}
	if toolEvents != 0 {
		t.Fatalf("call %s of the ended run produced %d tool events", record.CallID, toolEvents)
	}
	var payload struct {
		RunID      string                       `json:"run_id"`
		CallID     string                       `json:"call_id"`
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := json.Unmarshal(updated[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RunID != f.job.RunID || payload.CallID != record.CallID || payload.Permission.ID != record.ID || payload.Permission.Status != wantStatus || payload.Permission.RunID != f.job.RunID {
		t.Fatalf("settlement event provenance = run %q call %q permission %q/%q", payload.RunID, payload.CallID, payload.Permission.ID, payload.Permission.Status)
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	c, _ := companionCommitmentNamed(state, "approval:"+record.ID)
	if c.State != pebblestore.CompanionCommitmentResolved || c.Resolution != wantStatus || c.Resolved == nil || c.Resolved.EventType != "permission.updated" || c.Resolved.EventSeq != updated[0].Seq || c.Resolved.EventSeq <= c.Opened.EventSeq {
		t.Fatalf("approval %s = %+v, want resolved %s from event %d", record.ID, c, wantStatus, updated[0].Seq)
	}
	if state.Counters.UnmatchedResolution != 0 {
		t.Fatalf("unmatched resolutions = %d", state.Counters.UnmatchedResolution)
	}
	return c
}

func (f *companionApprovalRunEndFixture) requireRunEnded(t *testing.T, wantStatus string, modelCallsBefore int64) {
	t.Helper()
	intent, ok, err := f.sessions.GetSessionRunIntent(f.sessionID, f.job.RunID)
	if err != nil || !ok || intent.Status != wantStatus {
		t.Fatalf("run %s = %q %v %v, want %s", f.job.RunID, intent.Status, ok, err, wantStatus)
	}
	if sessionV3ExecutorStartedRunForTest(f.server.v3SessionExecutor, f.job) {
		t.Fatal("the daemon's executor restarted the ended run")
	}
	if got := f.modelCalls.Load(); got != modelCallsBefore {
		t.Fatalf("model calls = %d, want %d: an answer resumed a model run", got, modelCallsBefore)
	}
}

func (f *companionApprovalRunEndFixture) resolve(t *testing.T, record pebblestore.PermissionRecord, body string) {
	t.Helper()
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+record.ID+"/resolve", body); code != http.StatusOK {
		t.Fatalf("resolve %s: %d %s", record.ID, code, out)
	}
}

// Purpose: a pending approval cannot stay "waiting on the person" in the
// Companion after the person stops its run.
// Requirement: stopping the run through the real stop route
// (POST /v3/sessions/{id}/run/stop) records the run cancelled and cancels its
// pending permissions (permission.Service.CancelRunPending); the cancellation
// reaches the session's event log as exactly one permission.updated event that
// carries the original request's run and call and the stored "cancelled"
// decision, so the Companion resolves the approval as cancelled. A later answer
// to the cancelled request changes nothing: no second event, no reopened or
// re-resolved commitment, the run stays cancelled, no tool runs and no model
// run starts. Threat: the stop writes the cancellation only to the permission
// store (the run-owned writer refuses a run that is no longer running), so
// the derived needs-you stays open forever; or a fix that revives the run,
// replays the tool or duplicates the resolution. Boundary:
// handleSessionV3PrimaryRunStop, sessionV3Executor.CancelRun,
// cancelSessionV3RunPendingPermissions, settleSessionV3PermissionAfterRun,
// the resolve route, MaintainCompanionState. Narrowest layer: the daemon's
// HTTP handlers, executor writers and permission service over a real Pebble
// store with a scripted provider.
func TestCompanionApprovalClosesWhenItsRunIsStopped(t *testing.T) {
	f := newCompanionApprovalRunEndFixture(t, "approval-stop-run")
	record := f.request(t, 1, "call-stop")
	modelCalls := f.modelCalls.Load()

	if code, body := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/run/stop", `{"run_id":"approval-stop-run","target_swarm_id":"host-swarm-id","reason":"the person stopped it"}`); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, body)
	}
	f.requireRunEnded(t, sessionruntime.RunIntentCancelled, modelCalls)
	closed := f.requireSettledOnce(t, record, pebblestore.PermissionStatusCancelled)

	// A late answer to the cancelled request is a no-op everywhere.
	f.resolve(t, record, `{"action":"allow"}`)
	f.resolve(t, record, `{"action":"deny","reason":"too late"}`)
	again := f.requireSettledOnce(t, record, pebblestore.PermissionStatusCancelled)
	if again.Resolved.EventSeq != closed.Resolved.EventSeq {
		t.Fatalf("a late answer re-resolved the approval: %d then %d", closed.Resolved.EventSeq, again.Resolved.EventSeq)
	}
	f.requireRunEnded(t, sessionruntime.RunIntentCancelled, modelCalls)
}

// Purpose: the person's answer to an approval whose run already ended reaches
// the Companion with its real decision, while a request nobody answered stays
// open and another account cannot answer.
// Requirement: a run that ends with permission requests still pending leaves
// them pending (the permission store is the authority: the Companion keeps
// them open). The owner's later deny and allow through the real resolve route
// each write exactly one permission.updated event with the original request
// and the stored decision, and the Companion resolves each approval as denied
// or approved; repeated answers and a retried publication (as a restart or
// replay would) add nothing; the unanswered request stays pending and open; a
// resolve from another account is refused and changes nothing. No answer
// restarts the run, runs the gated tool or starts a model run, and the route
// and the Companion's native manage_companion state agree. Threat: the
// resolve route skipping its event for a run that is no longer running (the
// approval stays open forever), duplicate resolutions, an "approved" that runs
// the stale call, or another account closing someone's request. Boundary:
// handleSessionV3PrimaryPermissionResolve, publishSessionV3PermissionDecision,
// settleSessionV3PermissionAfterRun, MaintainCompanionState,
// companionStateRead, Runtime.executeManageCompanion. Narrowest layer: the
// daemon's HTTP handlers, executor writers, permission service and tool
// runtime over a real Pebble store with a scripted provider.
func TestCompanionApprovalAnsweredAfterItsRunEnded(t *testing.T) {
	f := newCompanionApprovalRunEndFixture(t, "approval-ended-run")
	denied := f.request(t, 1, "call-deny")
	allowed := f.request(t, 2, "call-allow")
	open := f.request(t, 3, "call-open")
	modelCalls := f.modelCalls.Load()

	// The run ends while all three wait.
	if _, err := f.executor.recordRunStatus(f.job, sessionruntime.RunIntentCompleted, "", "session.run.completed"); err != nil {
		t.Fatal(err)
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	for _, record := range []pebblestore.PermissionRecord{denied, allowed, open} {
		if c, _ := companionCommitmentNamed(state, "approval:"+record.ID); c.State != pebblestore.CompanionCommitmentOpen {
			t.Fatalf("pending approval %s after the run ended = %+v, want open", record.ID, c)
		}
	}

	// Another account cannot answer.
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+denied.ID+"/resolve", strings.NewReader(`{"action":"allow"}`)), "someone-else", "another-account")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("another account resolved the request: %d %s", rec.Code, rec.Body.String())
	}
	if stored, _, _ := f.permissions.GetPermission(f.sessionID, denied.ID); stored.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("a refused resolve changed the request to %s", stored.Status)
	}
	if updated, _ := f.permissionEvents(t, denied); len(updated) != 0 {
		t.Fatalf("a refused resolve wrote %d events", len(updated))
	}

	// The owner answers late.
	f.resolve(t, denied, `{"action":"deny","reason":"not now"}`)
	f.resolve(t, allowed, `{"action":"allow"}`)
	f.requireSettledOnce(t, denied, pebblestore.PermissionStatusDenied)
	f.requireSettledOnce(t, allowed, pebblestore.PermissionStatusApproved)

	// Repeated answers and a retried publication add nothing.
	f.resolve(t, denied, `{"action":"allow"}`)
	f.resolve(t, allowed, `{"action":"deny"}`)
	for _, record := range []pebblestore.PermissionRecord{denied, allowed} {
		stored, _, _ := f.permissions.GetPermission(f.sessionID, record.ID)
		if _, published, err := f.server.publishSessionV3PermissionDecision(testPrincipal(), f.sessionID, stored, true); err != nil || published {
			t.Fatalf("retried publication of %s: published=%v err=%v", record.ID, published, err)
		}
	}
	f.requireSettledOnce(t, denied, pebblestore.PermissionStatusDenied)
	f.requireSettledOnce(t, allowed, pebblestore.PermissionStatusApproved)

	// The unanswered request is still genuinely pending.
	state = companionStateNow(t, f.confirmationFixture, f.sessionID).State
	if c, _ := companionCommitmentNamed(state, "approval:"+open.ID); c.State != pebblestore.CompanionCommitmentOpen {
		t.Fatalf("unanswered approval = %+v, want open", c)
	}
	if stored, _, _ := f.permissions.GetPermission(f.sessionID, open.ID); stored.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("unanswered request = %s, want pending", stored.Status)
	}
	if updated, _ := f.permissionEvents(t, open); len(updated) != 0 {
		t.Fatalf("unanswered request has %d permission.updated events", len(updated))
	}
	f.requireRunEnded(t, sessionruntime.RunIntentCompleted, modelCalls)

	// The route and the native state answer the same state.
	code, routeBody := f.do(http.MethodGet, CompanionStatePath, "")
	if code != http.StatusOK {
		t.Fatalf("route: %d %s", code, routeBody)
	}
	var routed struct {
		State json.RawMessage `json:"companion_state"`
	}
	if err := json.Unmarshal([]byte(routeBody), &routed); err != nil {
		t.Fatal(err)
	}
	rt := tool.NewRuntime(1)
	rt.SetCompanionContextService(f.server)
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), agentruntime.SwarmOrchestratorAgentID), 20*time.Second)
	defer cancel()
	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: f.sessionID, Principal: testPrincipal()}, tool.Call{Name: tool.ManageCompanionToolName, Arguments: `{"action":"state"}`})
	if err != nil {
		t.Fatal(err)
	}
	data, err := tool.EnvelopeData(out)
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		State json.RawMessage `json:"companion_state"`
	}
	if err := json.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if got, want := companionStateData(t, native.State), companionStateData(t, routed.State); got != want {
		t.Fatalf("native disagrees with the route:\n%s\n%s", got, want)
	}
}
