package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// companionStateData drops the read's own time, so two reads of one state
// compare equal.
func companionStateData(t *testing.T, raw []byte) string {
	t.Helper()
	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("state view: %v (%s)", err, raw)
	}
	delete(view, "read_at")
	// Whether a background pass happens to be running at the instant of a
	// read is a live process flag, like read_at, not part of the state the
	// surfaces must agree on (a commit nudge can start a pass between two
	// reads); freshness and the last failure are still compared.
	if maintenance, ok := view["maintenance"].(map[string]any); ok {
		delete(maintenance, "running")
	}
	out, _ := json.Marshal(view)
	return string(out)
}

// Purpose: an approval response reaches the Companion's state through its
// real writers, and the state read is one service on the route and natively.
// Requirement: a permission request recorded by the V3 executor's provider
// tool event writer opens an approval commitment citing that
// permission.requested event; the person's answer through the real resolve
// route (POST /v3/sessions/{id}/permissions/{pid}/resolve) resolves it with
// the decision, citing the permission.updated event; background maintenance
// (the commit nudge, as wired on the daemon) gets there without any run
// start. GET /v3/companion/state and the Companion's native manage_companion
// state answer the same state; a native call from another conversation is
// refused, another user
// has no Companion state to read, the route is read only, and explicit
// account memory is untouched. Threat: approvals the state never sees (a
// second, synthetic ingress), surfaces that disagree, one user reading
// another's state, or the state leaking into account memory. Boundary:
// sessionV3Executor.recordProviderToolEvent, the resolve route and
// publishSessionV3PermissionUpdatedFromRecord, the session commit observer,
// MaintainCompanionState, companionStateRead, ManageCompanionContext,
// Runtime.executeManageCompanion. Narrowest layer: the daemon's HTTP handlers,
// executor writers, permission service and tool runtime over a real Pebble
// store with a scripted provider (no live model).
func TestCompanionStateFollowsApprovalsAndSurfacesAgree(t *testing.T) {
	f := newCompanionRunFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "Noted.", StopReason: "stop"}
	})
	store := f.sessions.Store()
	f.server.ConfigureCompanionMaintenance(store.Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	principal := testPrincipal()
	memoryBefore, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	f.say(f.sessionID, "q-1", "Help me pick a backup drive for my laptop photos")
	companionStateNow(t, f.confirmationFixture, f.sessionID)

	// A tool call waits on the person's approval in a running turn. This test
	// is the executor for approval-run, so it claims the run in the daemon's
	// executor before recording it pending, as the executor's own start does.
	// Unclaimed, a pending run is the executor's to start: the refill that a
	// finishing run triggers (finish -> refillPendingBacklog) runs it with the
	// scripted model and completes it, a second writer the person's answer
	// then cannot reach (the route skips permission.updated for a run that is
	// no longer running), so the approval stayed open.
	executor := &sessionV3Executor{server: f.server}
	job := sessionV3ExecutorJob{Principal: principal, SessionID: f.sessionID, RunID: "approval-run"}
	release := claimSessionV3ExecutorRunForTest(t, f.server.v3SessionExecutor, job)
	defer release()
	if _, err := executor.recordRunStatus(job, sessionruntime.RunIntentPendingExecutor, "", "session.run_intent.recorded"); err != nil {
		t.Fatalf("run %s: %v", sessionruntime.RunIntentPendingExecutor, err)
	}
	// The refill pass while the run is pending, deterministically: the
	// interleaving that used to start a second writer.
	f.server.v3SessionExecutor.drainRefillPasses(context.Background())
	if sessionV3ExecutorStartedRunForTest(f.server.v3SessionExecutor, job) {
		t.Fatal("the daemon's executor started the run this test owns")
	}
	if _, err := executor.recordRunStatus(job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
		t.Fatalf("run %s: %v", sessionruntime.RunIntentRunning, err)
	}
	record, err := f.permissions.CreatePending(permission.CreateInput{SessionID: f.sessionID, RunID: job.RunID, Step: 1, CallID: "call-approve", ToolName: "bash", ToolArguments: `{"command":"ls"}`, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.recordProviderToolEvent(job, runruntime.StreamEvent{Type: runruntime.StreamEventPermissionReq, Step: 1, ToolName: "bash", CallID: "call-approve", Permission: &record}, "permission.requested", 0); err != nil {
		t.Fatal(err)
	}
	waiting := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	approval, ok := companionCommitmentNamed(waiting, "approval:"+record.ID)
	if !ok || approval.State != pebblestore.CompanionCommitmentOpen || approval.Opened.EventType != "permission.requested" || approval.Title != "bash" {
		t.Fatalf("approval while waiting = %+v %v", approval, ok)
	}

	// The person answers through the real route.
	if code, body := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+record.ID+"/resolve", `{"action":"deny","reason":"not now"}`); code != http.StatusOK {
		t.Fatalf("resolve: %d %s", code, body)
	}
	answered := companionStateNow(t, f.confirmationFixture, f.sessionID).State
	approval, _ = companionCommitmentNamed(answered, "approval:"+record.ID)
	if approval.State != pebblestore.CompanionCommitmentResolved || approval.Resolution != pebblestore.PermissionStatusDenied || approval.Resolved == nil || approval.Resolved.EventType != "permission.updated" || approval.Resolved.EventSeq <= approval.Opened.EventSeq {
		t.Fatalf("approval after the answer = %+v", approval)
	}
	if _, err := executor.recordRunStatus(job, sessionruntime.RunIntentCompleted, "", "session.run.completed"); err != nil {
		t.Fatal(err)
	}
	companionStateNow(t, f.confirmationFixture, f.sessionID)

	// One state on three surfaces.
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
	call := func(scope tool.WorkspaceScope) (string, error) {
		ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), agentruntime.SwarmOrchestratorAgentID), 20*time.Second)
		defer cancel()
		return rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: tool.ManageCompanionToolName, Arguments: `{"action":"state"}`})
	}
	route := companionStateData(t, routed.State)
	// (Swarm Control's MCP call is compared in TestCompanionStateSurfacesAgree.)
	for name, scope := range map[string]tool.WorkspaceScope{"native": {SessionID: f.sessionID, Principal: principal}} {
		out, err := call(scope)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, err := tool.EnvelopeData(out)
		if err != nil {
			t.Fatalf("%s envelope: %v (%s)", name, err, out)
		}
		var answer struct {
			State json.RawMessage `json:"companion_state"`
		}
		if err := json.Unmarshal(data, &answer); err != nil {
			t.Fatal(err)
		}
		if got := companionStateData(t, answer.State); got != route {
			t.Fatalf("%s disagrees with the route:\n%s\n%s", name, got, route)
		}
	}

	// Refusals.
	other := f.createSession("not-the-companion")
	if _, err := call(tool.WorkspaceScope{SessionID: other, Principal: principal}); err == nil || !strings.Contains(err.Error(), "not your Companion") {
		t.Fatalf("another conversation read the state: %v", err)
	}
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, CompanionStatePath, nil), "someone-else", principal.AccountScopeID)
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), f.sessionID) {
		t.Fatalf("another user's read = %d %s, want 404 without this Companion", rec.Code, rec.Body.String())
	}
	if code, _ := f.do(http.MethodPost, CompanionStatePath, `{}`); code != http.StatusMethodNotAllowed {
		t.Fatalf("state route accepted a write: %d", code)
	}
	memoryAfter, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memoryBefore, memoryAfter) {
		t.Fatal("Companion state maintenance changed explicit account memory")
	}
}

// Purpose: the Companion state read is one capability on three surfaces.
// Requirement: once the harness Companion's events are maintained, the
// Companion's native manage_companion state, Swarm Control's
// manage_companion state (the real MCP tools/call) and GET
// /v3/companion/state answer the identical state (the read's own time
// aside), with nothing pending; a Swarm Control key without a sessions scope
// is refused, and manage_companion state naming a machine is refused with
// the one shared reason. Threat: surfaces drifting apart, or a key reading
// the Companion's state beyond its scopes. Boundary: companionStateRead,
// ManageCompanionContext, Runtime.executeManageCompanion, controlMCPCall,
// handleCompanionState. Narrowest layer: the catalog parity harness, the same
// in-process handlers the daemon serves.
func TestCompanionStateSurfacesAgree(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	session, ok, err := store.GetSession(h.companionSession)
	if err != nil || !ok {
		t.Fatalf("harness companion: %v %v", ok, err)
	}
	if _, err := runruntime.MaintainCompanionState(store, session, runruntime.CompanionMaintainBackgroundEvents, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	native, err := h.companionNative(t, h.companionSession, h.p, parityArgs("action", "state"))
	if err != nil {
		t.Fatal(err)
	}
	control, controlErr := h.controlTool(t, catalogParityFullKey, tool.ManageCompanionToolName, parityArgs("action", "state"))
	if controlErr != "" {
		t.Fatal(controlErr)
	}
	code, route := h.companionRoute(t, CompanionStatePath)
	if code != http.StatusOK {
		t.Fatalf("route: %d %v", code, route)
	}
	routeState, _ := json.Marshal(route["companion_state"])
	want := companionStateData(t, routeState)
	for name, raw := range map[string]string{"native": native, "control": control} {
		data := envelopeDataOf(t, raw)
		state, _ := json.Marshal(data["companion_state"])
		if got := companionStateData(t, state); got != want {
			t.Fatalf("%s disagrees with the route:\n%s\n%s", name, got, want)
		}
	}
	var view CompanionStateView
	if err := json.Unmarshal(routeState, &view); err != nil || view.State == nil || view.Pending != 0 || view.SessionID != h.companionSession || view.State.Cursor.EventSeq == 0 {
		t.Fatalf("route state = %+v (%v)", view, err)
	}
	if _, refused := h.controlTool(t, []string{"swarm:read"}, tool.ManageCompanionToolName, parityArgs("action", "state")); refused == "" {
		t.Fatal("a key without a sessions scope read the Companion state")
	}
	if _, err := h.companionNative(t, h.companionSession, h.p, parityArgs("action", "state", "machine", "laptop")); err == nil || err.Error() != tool.ManageCompanionMachineRefusal {
		t.Fatalf("machine= was not refused with the shared reason: %v", err)
	}
}
