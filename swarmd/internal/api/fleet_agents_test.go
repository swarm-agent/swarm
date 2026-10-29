package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func fleetAgentsTestSession(t *testing.T, f *fleetTestSwarm, id string, meta map[string]any) {
	t.Helper()
	snapshot := pebblestore.SessionSnapshot{ID: id, UserID: f.user, AccountScopeID: f.account, Title: "session " + id, Metadata: meta}
	if _, err := f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: f.user, AccountScopeID: f.account, Session: &snapshot, ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

// fleetAgentsTestRun records a run's pending intent, as admission does
// before the executor records its events.
func fleetAgentsTestRun(t *testing.T, f *fleetTestSwarm, session, run string) {
	t.Helper()
	id := session + ":start:" + run
	if _, err := f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: session, UserID: f.user, AccountScopeID: f.account,
		ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, RunIntent: &pebblestore.V3SessionRunIntent{RunID: run, Status: sessionruntime.RunIntentPendingExecutor}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

// fleetAgentsTestTool records one tool event the way the executor does
// (recordProviderToolEvent): a run-intent mutation carrying the event.
func fleetAgentsTestTool(t *testing.T, f *fleetTestSwarm, session, run, event, call, tool, args string) {
	t.Helper()
	payload := map[string]any{"run_id": run, "tool_name": tool, "call_id": call, "tool_instance_id": "step-1:" + call}
	if args != "" {
		payload["arguments"] = args
	}
	raw, _ := json.Marshal(payload)
	id := session + ":" + event + ":" + call
	if _, err := f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: session, UserID: f.user, AccountScopeID: f.account,
		ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, RequestHash: id, EventType: "session.tool." + event, EventPayload: raw,
		RunIntent: &pebblestore.V3SessionRunIntent{RunID: run, Status: sessionruntime.RunIntentRunning}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

func fleetAgentsTestLease(t *testing.T, f *fleetTestSwarm, session, run string) executioncapacity.Lease {
	t.Helper()
	manager := f.s.ExecutionCapacity()
	if manager == nil {
		// The fleet fixture has no permission service; give it the daemon's
		// own (permission.NewService, as sessions_v3_test_helpers_test.go
		// does), whose capacity manager holds the leases.
		if f.s.perm != nil {
			t.Fatal("fixture's permission service has no execution capacity manager")
		}
		db := f.s.sessions.Store().Underlying()
		events, err := pebblestore.NewEventLog(db)
		if err != nil {
			t.Fatal(err)
		}
		f.s.perm = permission.NewService(pebblestore.NewPermissionStore(db), events, nil)
		if manager = f.s.ExecutionCapacity(); manager == nil {
			t.Fatal("fixture has no execution capacity manager")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: f.account, SessionID: session, RunID: run})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	return lease
}

func fleetAgentsTestRead(t *testing.T, f *fleetTestSwarm, query string) fleetAgentsView {
	t.Helper()
	w := httptest.NewRecorder()
	f.s.handleFleetAgents(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, fleetAgentsPath+query, nil), f.user, f.account))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d %s", fleetAgentsPath+query, w.Code, w.Body.String())
	}
	var view fleetAgentsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func fleetAgentsTestMachine(view fleetAgentsView, name string) (fleetAgentMachine, bool) {
	for _, m := range view.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return fleetAgentMachine{}, false
}

// Purpose: one read answers "which agent runs are active right now on this
// machine and its linked machines, and what is each doing". A run is listed
// while it holds an execution lease (the set swarm_status counts as
// running_now), with its task, its workflow step, its last tool call, its
// counts and the files it edited; a linked machine's runs come through the
// link under the machine's name; a run whose lease was released (finished)
// is not listed; the day's finished tasks are counted and listed.
// Threat: a finished session shown as working; a linked machine's runs
// missing or attributed to the home; counts that disagree with the session's
// tool events; a task's title or step lost.
// Boundary: Server.handleFleetAgents (FleetAgents, localAgentsNow,
// fleetAgentsTools, localAgentsFinished, fleetAgentsMachine) over two
// in-process daemons, the second reached only through its link listener, with
// real executioncapacity leases and session events recorded through
// applySessionV3PrimaryMutation: the narrowest layer joining the leases, the
// session records, the task records and the link.
func TestFleetAgentsListsActiveRunsAcrossTheFleet(t *testing.T) {
	a, b, _ := fleetActivityTestLink(t)
	store := a.s.sessions.Store()
	if err := store.PutProject(a.account, &pebblestore.ProjectRecord{ID: "proj", Name: "Console"}); err != nil {
		t.Fatal(err)
	}
	// The running reviewer's own task: a workflow step, so its step is review.
	if err := store.PutProjectTask(a.account, &pebblestore.ProjectTaskRecord{ID: "review-task", ProjectID: "proj", AccountID: a.account, Title: "Review the composer row", Status: "in_progress", Agent: "reviewer", AutomationID: "workflow:review", SessionID: "home-run"}); err != nil {
		t.Fatal(err)
	}
	// A task that failed today: counted and listed as finished, never as running.
	if err := store.PutProjectTask(a.account, &pebblestore.ProjectTaskRecord{ID: "failed-task", ProjectID: "proj", AccountID: a.account, Title: "Broken build", Status: "failed", Agent: "coder", SessionID: "home-done"}); err != nil {
		t.Fatal(err)
	}

	fleetAgentsTestSession(t, a, "home-run", map[string]any{"project_id": "proj", "task_id": "review-task", "agent_name": "reviewer"})
	fleetAgentsTestRun(t, a, "home-run", "run-1")
	fleetAgentsTestTool(t, a, "home-run", "run-1", "started", "c1", "read", `{"path":"console/README.md"}`)
	fleetAgentsTestTool(t, a, "home-run", "run-1", "completed", "c1", "read", "")
	fleetAgentsTestTool(t, a, "home-run", "run-1", "started", "c2", "edit", `{"path":"console/src/data.rs","old_string":"a","new_string":"b"}`)
	fleetAgentsTestTool(t, a, "home-run", "run-1", "completed", "c2", "edit", "")
	fleetAgentsTestTool(t, a, "home-run", "run-1", "started", "c3", "git_commit", `{"message":"console: fit the row\n\nbody"}`)
	fleetAgentsTestTool(t, a, "home-run", "run-1", "completed", "c3", "git_commit", "")
	fleetAgentsTestTool(t, a, "home-run", "run-1", "started", "c4", "bash", `{"command":"go test ./internal/api/\necho second line"}`)
	held := fleetAgentsTestLease(t, a, "home-run", "run-1")
	defer held.Release()

	// A run that finished: its lease is released.
	fleetAgentsTestSession(t, a, "home-done", map[string]any{"project_id": "proj", "task_id": "failed-task"})
	fleetAgentsTestRun(t, a, "home-done", "run-2")
	fleetAgentsTestTool(t, a, "home-done", "run-2", "started", "d1", "bash", `{"command":"make"}`)
	done := fleetAgentsTestLease(t, a, "home-done", "run-2")
	if err := done.Release(); err != nil {
		t.Fatal(err)
	}

	// The linked machine's one run.
	fleetAgentsTestSession(t, b, "box-run", nil)
	fleetAgentsTestRun(t, b, "box-run", "run-b")
	fleetAgentsTestTool(t, b, "box-run", "run-b", "started", "b1", "write", `{"path":"notes.md"}`)
	boxLease := fleetAgentsTestLease(t, b, "box-run", "run-b")
	defer boxLease.Release()

	view := fleetAgentsTestRead(t, a, "")
	if view.Home != "home" || view.Totals.Agents != 2 || len(view.Agents) != 2 {
		t.Fatalf("view = %+v, want 2 agents under home", view)
	}
	if m, ok := fleetAgentsTestMachine(view, "home"); !ok || m.State != "online" || m.Agents != 1 || m.FailedToday != 1 {
		t.Fatalf("home machine = %+v (%v), want online with 1 agent and 1 failed today", m, ok)
	}
	if m, ok := fleetAgentsTestMachine(view, "b"); !ok || m.State != "online" || m.Agents != 1 {
		t.Fatalf("linked machine = %+v (%v), want online with 1 agent", m, ok)
	}
	var home, box *fleetAgentRun
	for i := range view.Agents {
		switch view.Agents[i].SessionID {
		case "home-run":
			home = &view.Agents[i]
		case "box-run":
			box = &view.Agents[i]
		case "home-done":
			t.Fatalf("a finished run is listed: %+v", view.Agents[i])
		}
	}
	if home == nil || box == nil {
		t.Fatalf("agents = %+v, want home-run and box-run", view.Agents)
	}
	if home.Machine != "home" || home.Agent != "reviewer" || home.RunID != "run-1" || home.TaskID != "review-task" || home.TaskTitle != "Review the composer row" {
		t.Fatalf("home run = %+v", home)
	}
	if home.Stage == nil || home.Stage.Step != fleetAgentStepReview {
		t.Fatalf("home run stage = %+v, want review", home.Stage)
	}
	if home.Now == nil || home.Now.Kind != "command" || home.Now.Target != "go test ./internal/api/" || home.Now.State != "running" {
		t.Fatalf("home run now = %+v, want the running command's first line", home.Now)
	}
	if c := home.Counts; c.Reads != 1 || c.Edits != 1 || c.Commands != 1 || c.Commits != 1 || c.LastMinute != 4 {
		t.Fatalf("home run counts = %+v", c)
	}
	if len(home.Files) != 1 || home.Files[0] != "console/src/data.rs" || home.FilesTotal != 1 {
		t.Fatalf("home run files = %v (%d)", home.Files, home.FilesTotal)
	}
	if len(home.Timeline) != 4 || home.Timeline[0].Target != "console/README.md" || home.Timeline[0].State != "done" || home.Timeline[2].Target != "console: fit the row" || home.Timeline[3].Kind != "command" {
		t.Fatalf("home run timeline = %+v, want oldest first", home.Timeline)
	}
	if home.StartedAt == 0 {
		t.Fatalf("home run has no start: %+v", home)
	}
	if box.Machine != "b" || box.Now == nil || box.Now.Kind != "edit" || box.Now.Target != "notes.md" {
		t.Fatalf("linked run = %+v, want b's write", box)
	}
	if view.Totals.FailedToday != 1 || len(view.Recent) == 0 || view.Recent[0].TaskID != "failed-task" || view.Recent[0].State == "" {
		t.Fatalf("recent = %+v totals = %+v, want the failed task", view.Recent, view.Totals)
	}
	for _, r := range view.Recent {
		if r.TaskID == "review-task" {
			t.Fatalf("a running task is listed as finished: %+v", r)
		}
	}

	// scope=machine is this Swarm alone, as a home reads it through the link.
	local := fleetAgentsTestRead(t, a, "?scope=machine")
	if len(local.Agents) != 1 || len(local.Machines) != 1 || local.Agents[0].SessionID != "home-run" {
		t.Fatalf("scope=machine = %+v, want home's run only", local)
	}

	// Released: nothing runs on the home; b's run still shows.
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	after := fleetAgentsTestRead(t, a, "")
	if after.Totals.Agents != 1 || len(after.Agents) != 1 || after.Agents[0].Machine != "b" {
		t.Fatalf("after release = %+v, want only b's run", after.Agents)
	}
}

// Purpose: a linked machine that cannot be read is reported with its name and
// why, not dropped: one whose link does not answer is offline with the
// transport's reason; one whose linked.{name}.work is off is not_shown with
// that reason; the machines that answer are still read.
// Threat: the indicator says "0 agents" for a machine that is down, or one
// slow machine hides the rest. Boundary: Server.FleetAgents and
// fleetAgentsMachine over in-process daemons, one of them stopped.
func TestFleetAgentsReportsMachinesItCannotRead(t *testing.T) {
	a, b, _ := fleetActivityTestLink(t)
	c := newFleetTestSwarm(t, "gone")
	body, _ := json.Marshal(map[string]string{"code": c.invite(t), "name": "c"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link c: %d %v", code, out)
	}
	c.cleanup() // c's link listener and store close: it no longer answers.
	d := newFleetTestSwarm(t, "off")
	t.Cleanup(d.cleanup)
	body, _ = json.Marshal(map[string]string{"code": d.invite(t), "name": "d"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link d: %d %v", code, out)
	}
	fleetActivityTestSetWork(t, a, "d", "off")

	fleetAgentsTestSession(t, b, "box-run", nil)
	lease := fleetAgentsTestLease(t, b, "box-run", "run-b")
	defer lease.Release()

	view := fleetAgentsTestRead(t, a, "")
	if m, ok := fleetAgentsTestMachine(view, "c"); !ok || m.State != "offline" || strings.TrimSpace(m.Reason) == "" || m.Agents != 0 {
		t.Fatalf("stopped machine = %+v (%v), want offline with a reason", m, ok)
	}
	if m, ok := fleetAgentsTestMachine(view, "d"); !ok || m.State != "not_shown" || m.Reason != fleetActivityOffReason {
		t.Fatalf("work-off machine = %+v (%v), want not_shown: %s", m, ok, fleetActivityOffReason)
	}
	if m, ok := fleetAgentsTestMachine(view, "b"); !ok || m.State != "online" || m.Agents != 1 || view.Totals.Agents != 1 {
		t.Fatalf("answering machine = %+v (%v), totals %+v: want b read", m, ok, view.Totals)
	}
}

// Purpose: the route refuses what it must: gateway tokens (one sealed agent,
// never the fleet), other methods, an unknown scope and a since in the future.
// Threat: an application gateway reading every machine's runs. Boundary:
// Server.handleFleetAgents.
func TestFleetAgentsRefusals(t *testing.T) {
	a := newFleetTestSwarm(t, "home")
	t.Cleanup(a.cleanup)
	call := func(method, path string, gateway bool) int {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(method, path, nil), a.user, a.account)
		if gateway {
			r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}, AgentName: "app"})
		}
		w := httptest.NewRecorder()
		a.s.handleFleetAgents(w, r)
		return w.Code
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	for _, tc := range []struct {
		method, path string
		gateway      bool
		want         int
	}{
		{http.MethodGet, fleetAgentsPath, true, http.StatusForbidden},
		{http.MethodPost, fleetAgentsPath, false, http.StatusMethodNotAllowed},
		{http.MethodGet, fleetAgentsPath + "?scope=everything", false, http.StatusBadRequest},
		{http.MethodGet, fleetAgentsPath + "?since=soon", false, http.StatusBadRequest},
		{http.MethodGet, fleetAgentsPath + "?since=" + jsonNumber(future), false, http.StatusBadRequest},
		{http.MethodGet, fleetAgentsPath, false, http.StatusOK},
	} {
		if got := call(tc.method, tc.path, tc.gateway); got != tc.want {
			t.Errorf("%s %s gateway=%v: HTTP %d, want %d", tc.method, tc.path, tc.gateway, got, tc.want)
		}
	}
}

func jsonNumber(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Purpose: a task's workflow step is read from its progress, the same
// record get_task shows: code while the first run works and a workflow
// applies, review while a workflow's reviewer runs, fix with the round while
// it was sent back, push while a run or deliver step hands it over, landed
// once it landed; no step without a workflow.
// Threat: the pipeline strip shows a step the task is not at, or the wrong
// round. Boundary: fleetAgentStageOf, the pure rule the route applies.
func TestFleetAgentStageFollowsTheTaskProgress(t *testing.T) {
	working := pebblestore.ProjectTaskRecord{ID: "t", ProjectID: "p", Status: "in_progress"}
	finished := pebblestore.ProjectTaskRecord{ID: "t", ProjectID: "p", Status: "needs_review"}
	delivery := func(state, stepType string, round int, sentBack *jobautomation.SentBack) *taskProgress {
		return &taskProgress{Workflow: &taskProgressWorkflow{State: state, Deliveries: []taskProgressDelivery{{Workflow: "review and push", State: state, StepType: stepType, Round: round, SentBack: sentBack}}}}
	}
	for _, tc := range []struct {
		name     string
		task     pebblestore.ProjectTaskRecord
		progress *taskProgress
		applies  bool
		step     string
		round    int
	}{
		{"first run, a workflow applies", working, &taskProgress{}, true, fleetAgentStepCode, 0},
		{"first run, no workflow", working, &taskProgress{}, false, "", 0},
		{"reviewer runs", finished, delivery(workflowRunning, jobautomation.StepAgent, 0, nil), true, fleetAgentStepReview, 0},
		{"sent back, round 2", working, delivery(workflowSentBack, jobautomation.StepReopen, 2, &jobautomation.SentBack{Step: "send-back", Round: 2}), true, fleetAgentStepFix, 2},
		{"the agent works again after a reopen", working, delivery(workflowRunning, jobautomation.StepAgent, 1, nil), true, fleetAgentStepFix, 1},
		{"push script runs", finished, delivery(workflowRunning, jobautomation.StepRun, 0, nil), true, fleetAgentStepPush, 0},
		{"deliver step", finished, delivery(workflowRunning, jobautomation.StepDeliver, 0, nil), true, fleetAgentStepPush, 0},
		{"landed", finished, &taskProgress{TaskState: pebblestore.ProjectTaskStateLanded}, true, fleetAgentStepLanded, 0},
		{"a reviewer's own task", pebblestore.ProjectTaskRecord{ID: "r", AutomationID: "workflow:x", Status: "in_progress"}, nil, false, fleetAgentStepReview, 0},
		{"no progress", working, nil, true, "", 0},
	} {
		got := fleetAgentStageOf(tc.task, tc.progress, tc.applies)
		switch {
		case tc.step == "" && got != nil:
			t.Errorf("%s: stage %+v, want none", tc.name, got)
		case tc.step != "" && (got == nil || got.Step != tc.step || got.Round != tc.round):
			t.Errorf("%s: stage %+v, want %s round %d", tc.name, got, tc.step, tc.round)
		}
	}
}

// Purpose: a tool call's target is one short line: the path for reads and
// edits, the first line of a command or a commit message, clipped; a linked
// machine's tool kinds are derived from the tool name, not taken as sent.
// Threat: a multi-line command or a long payload flooding a card; a machine
// claiming a commit that was a read. Boundary: fleetAgentToolTarget,
// fleetAgentToolKind and fleetAgentsClean.
func TestFleetAgentToolTargetsAndCleaning(t *testing.T) {
	if got := fleetAgentToolTarget("bash", `{"command":"  go   test ./...\nrm -rf x"}`); got != "go test ./..." {
		t.Errorf("bash target = %q", got)
	}
	if got := fleetAgentToolTarget("edit", `{"path":"console/src/data.rs"}`); got != "console/src/data.rs" {
		t.Errorf("edit target = %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := fleetAgentToolTarget("bash", `{"command":"`+long+`"}`); len([]rune(got)) != fleetAgentsTarget || !strings.HasSuffix(got, "…") {
		t.Errorf("long target = %d runes", len([]rune(got)))
	}
	if got := fleetAgentToolTarget("bash", `not json`); got != "" {
		t.Errorf("bad arguments target = %q", got)
	}
	in := fleetAgentsView{Agents: []fleetAgentRun{{Machine: "home", SessionID: "s", Now: &fleetAgentTool{Kind: "commit", Tool: "read", State: "weird"}, Stage: &fleetAgentStage{Step: "deploy-everything"},
		Timeline: make([]fleetAgentTool, 40), Files: make([]string, 99)}}}
	out := fleetAgentsClean(in, "b")
	a := out.Agents[0]
	if a.Machine != "b" || a.Now.Kind != "read" || a.Now.State != "" || a.Stage != nil || len(a.Timeline) != fleetAgentsTimeline || len(a.Files) != fleetAgentsFiles || out.Totals.Agents != 1 {
		t.Fatalf("cleaned = %+v", a)
	}
}
