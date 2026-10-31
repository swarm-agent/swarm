package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: GET /v3/projects/{p}/tasks/{t}/flow and the project roll-up
// GET /v3/projects/{p}/flow are computed from the task's durable records in
// the real store: the run records and tool events written through the V3
// mutation boundary (ApplyV3SessionMutation) and the run's usage record. A
// task's report reads only sessions of the caller's account, another
// account cannot read the task's flow, and an unknown period is refused.
// Threat: a flow answer that leaks another account's session activity, or
// reports nothing because it reads a second, non-durable source.
// Owners: Server.ProjectTaskFlow / ProjectFlowRollup and the projects route
// (project_task_flow.go); the computation itself is covered in
// jobautomation/flow_test.go. The route over a temp store is the narrowest
// layer that proves the records are read as stored.
func TestProjectTaskFlowReadsDurableRecords(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := server.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "flow-project", AccountID: p.AccountScopeID, Name: "Flow"}); err != nil {
		t.Fatal(err)
	}
	apply := func(account, user string, in pebblestore.V3SessionMutationInput) {
		t.Helper()
		in.AccountScopeID, in.UserID, in.PayloadHash = account, user, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	session := func(account, user, id string) {
		t.Helper()
		apply(account, user, pebblestore.V3SessionMutationInput{SessionID: id, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-" + id,
			Session: &pebblestore.SessionSnapshot{ID: id, Mode: "auto", WorkspacePath: t.TempDir()}})
	}
	const t0 = int64(1_800_000_000_000)
	record := func(account, user, sessionID, key, eventType string, intent pebblestore.V3SessionRunIntent, payload map[string]any, at int64) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		intent.SessionID = sessionID
		apply(account, user, pebblestore.V3SessionMutationInput{SessionID: sessionID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: key,
			EventType: eventType, EventPayload: raw, RunIntent: &intent, NowUnixMs: at})
	}
	// The task's own session: one run that ran go test for 4 minutes.
	session(p.AccountScopeID, p.UserID, "flow-own")
	record(p.AccountScopeID, p.UserID, "flow-own", "pending", "session.run_intent.recorded", pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor, CreatedAt: t0}, map[string]any{"run_id": "run-1"}, t0)
	running := pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning, CreatedAt: t0, StartedAt: t0}
	record(p.AccountScopeID, p.UserID, "flow-own", "start", "session.run.started", running, map[string]any{"run_id": "run-1"}, t0)
	record(p.AccountScopeID, p.UserID, "flow-own", "tool-start", "session.tool.started", running,
		map[string]any{"run_id": "run-1", "tool_name": "bash", "arguments": `{"command":"go test ./..."}`, "call_id": "c1", "tool_instance_id": "i1"}, t0+60_000)
	record(p.AccountScopeID, p.UserID, "flow-own", "tool-done", "session.tool.completed", running,
		map[string]any{"run_id": "run-1", "tool_name": "bash", "call_id": "c1", "tool_instance_id": "i1", "output": "ok  pkg 1.2s"}, t0+300_000)
	done := pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentCompleted, CreatedAt: t0, StartedAt: t0, CompletedAt: t0 + 600_000}
	record(p.AccountScopeID, p.UserID, "flow-own", "done", "session.run.completed", done, map[string]any{"run_id": "run-1"}, t0+600_000)
	if err := db.PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: "flow-own", AccountScopeID: p.AccountScopeID, UserID: p.UserID, RunID: "run-1", Provider: "test", Model: "test", InputTokens: 4000, OutputTokens: 300, Steps: 2, CreatedAt: t0, UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "flow-task", ProjectID: "flow-project", AccountID: p.AccountScopeID, Title: "Flow task", Agent: "coder", Status: "completed", SessionID: "flow-own",
		Attempts: []pebblestore.ProjectTaskAttempt{{ID: "a1", SessionID: "flow-own", Role: "coder"}, {ID: "a2", SessionID: "flow-foreign", Role: "coder"}}}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	// A session id the task's attempt names, created later under another
	// account (a stale reference): its runs are never read.
	session("account-other", "user-other", "flow-foreign")
	record("account-other", "user-other", "flow-foreign", "foreign-pending", "session.run_intent.recorded", pebblestore.V3SessionRunIntent{RunID: "run-x", Status: pebblestore.V3RunIntentPendingExecutor, CreatedAt: t0}, map[string]any{"run_id": "run-x"}, t0)
	record("account-other", "user-other", "flow-foreign", "foreign-running", "session.run.started", pebblestore.V3SessionRunIntent{RunID: "run-x", Status: pebblestore.V3RunIntentRunning, CreatedAt: t0, StartedAt: t0}, map[string]any{"run_id": "run-x"}, t0)
	if intents, err := db.ListV3SessionRunIntents("flow-foreign", 0, 10); err != nil || len(intents) != 1 {
		t.Fatalf("the foreign session must hold a run for the test to mean anything: %+v %v", intents, err)
	}

	get := func(req *http.Request) (int, map[string]json.RawMessage, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body, rec.Body.String()
	}
	code, body, raw := get(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/projects/flow-project/tasks/flow-task/flow", nil)))
	if code != http.StatusOK {
		t.Fatalf("flow: %d %s", code, raw)
	}
	var report jobautomation.FlowReport
	if err := json.Unmarshal(body["flow"], &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 || report.Runs[0].SessionID != "flow-own" || report.Runs[0].RunID != "run-1" {
		t.Fatalf("only the task's own account's run is read: %+v", report.Runs)
	}
	run := report.Runs[0]
	if run.Tools.Command != 1 || run.TestRuns != 1 || run.TestMS != 240_000 || run.Tokens.Input != 4000 || run.Tokens.Requests != 2 || run.ContextPerRequest != 2000 || run.DurationMS != 600_000 {
		t.Fatalf("run: %+v", run)
	}
	if report.Phases.TestingMS != 240_000 || report.Phases.CodingMS != 360_000 || report.WallMS != 600_000 {
		t.Fatalf("phases: %+v wall %d", report.Phases, report.WallMS)
	}
	var markdown string
	if err := json.Unmarshal(body["markdown"], &markdown); err != nil || !strings.Contains(markdown, "### Flow: Flow task") {
		t.Fatalf("markdown: %q %v", markdown, err)
	}

	// The Orchestrator's manage_projects get_task detail=flow (and so Swarm
	// Control's, which runs the same handler) answers the same report and
	// its Markdown; detail flow on another action is refused.
	runtime := tool.NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageProjectStore(db)
	runtime.SetProjectTaskLifecycleService(server)
	runtime.SetManageSessionService(server.sessions)
	session(p.AccountScopeID, p.UserID, "flow-orchestrator")
	scope := tool.WorkspaceScope{SessionID: "flow-orchestrator", Principal: p}
	out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(t.Context(), scope, tool.Call{Name: "manage_projects", Arguments: `{"action":"get_task","project_id":"flow-project","task_id":"flow-task","detail":"flow"}`})
	if err != nil {
		t.Fatalf("get_task detail=flow: %v %s", err, out)
	}
	for _, part := range []string{`"flow_markdown"`, `### Flow: Flow task`, `"run_id":"run-1"`, `"testing_ms":240000`} {
		if !strings.Contains(out, part) {
			t.Fatalf("get_task detail=flow lacks %s: %s", part, out)
		}
	}
	if strings.Contains(out, "run-x") {
		t.Fatalf("get_task detail=flow read another account's run: %s", out)
	}
	if out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(t.Context(), scope, tool.Call{Name: "manage_projects", Arguments: `{"action":"archive_task","project_id":"flow-project","task_id":"flow-task","expected_revision":1,"detail":"flow"}`}); err == nil || !strings.Contains(err.Error(), "detail flow is read with get_task") {
		t.Fatalf("archive_task detail=flow must be refused: %v %s", err, out)
	}
	if current, _, _ := db.GetProjectTask(p.AccountScopeID, "flow-project", "flow-task"); current == nil || current.Archived {
		t.Fatalf("a refused call changed the task: %+v", current)
	}

	// Another account does not see the task at all.
	other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-other", AccountScopeID: "account-other"}
	if _, err := server.ProjectTaskFlow(t.Context(), other, "flow-project", "flow-task"); err == nil || flowStatus(err) != http.StatusNotFound {
		t.Fatalf("another account read the flow: %v", err)
	}

	// The project roll-up: the period is checked; this task's run is outside
	// any period that ends now, so a valid period sums no task time.
	if code, _, raw := get(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/projects/flow-project/flow?period=forever", nil))); code != http.StatusBadRequest {
		t.Fatalf("unknown period: %d %s", code, raw)
	}
	code, body, raw = get(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/projects/flow-project/flow?period=today", nil)))
	if code != http.StatusOK {
		t.Fatalf("roll-up: %d %s", code, raw)
	}
	var rollup jobautomation.FlowRollup
	if err := json.Unmarshal(body["flow"], &rollup); err != nil || rollup.Period != "today" || rollup.ProjectID != "flow-project" {
		t.Fatalf("roll-up: %+v %v", rollup, err)
	}
}

// Requirement: every unfinished task status counts as live in the flow
// report, and a task waiting on a question or an approval waits on the
// person. The statuses are the task statuses of
// pebblestore.ProjectTaskCountBucket and the job statuses the daemon treats
// as still running (job_automations.go reopen busy check). Threat: a task in
// needs_input reported as finished, so its open run's wait counts as coding.
// Owner: flowTaskLive (pure mapping; the phase effect is covered in
// jobautomation.TestFlowWaitingOnPersonWithOpenRun).
func TestFlowTaskLiveStatuses(t *testing.T) {
	for status, want := range map[string][2]bool{
		"needs_input": {true, true}, "waiting_on_approval": {true, true}, "pending_approval": {true, true},
		"queued": {true, false}, "pending": {true, false}, "starting": {true, false}, "running": {true, false},
		"planning": {true, false}, "in_progress": {true, false}, pebblestore.ProjectTaskStatusLimitWaiting: {true, false},
		"completed": {false, false}, "needs_review": {false, false}, "failed": {false, false}, pebblestore.ProjectTaskStatusStopped: {false, false},
	} {
		live, person := flowTaskLive(status)
		if live != want[0] || person != want[1] {
			t.Errorf("%s: live %v person %v, want %v", status, live, person, want)
		}
	}
}
