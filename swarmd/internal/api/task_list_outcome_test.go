package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const outcomeTestHead = "0123456789abcdef0123456789abcdef01234567"

// seedOutcomeProject writes, through the canonical stores, a project whose
// tasks ended four ways and three workflow reviewer tasks of one of them:
//   - pushed: its delivery reviewed it and ran the push step at outcomeTestHead
//   - sent-back: its delivery sent the work back (round 2)
//   - blocked: its delivery ran out of rounds
//   - plain: no workflow ran for it; one commit on its branch not integrated
//   - finder: a read-only finder task, no workflow, no commits, nothing to
//     hand over (finished, nothing to integrate)
//   - task_wf_r1..r3: reviewer tasks a workflow started for pushed
//   - abandoned: in_progress, no run, untouched for more than a day (stale)
//   - busy: in_progress with an active run in its session (not stale)
func seedOutcomeProject(t *testing.T, db *pebblestore.SessionStore, autos *jobautomation.Store, account, user, projectID, folder string) {
	t.Helper()
	if err := db.PutProject(account, &pebblestore.ProjectRecord{ID: projectID, AccountID: account, Name: "Outcomes"}); err != nil {
		t.Fatal(err)
	}
	put := func(task pebblestore.ProjectTaskRecord) {
		t.Helper()
		task.ProjectID, task.AccountID = projectID, account
		if task.Title == "" {
			task.Title = "Task " + task.ID
		}
		if err := db.PutProjectTask(account, &task); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"pushed", "sent-back", "blocked"} {
		put(pebblestore.ProjectTaskRecord{ID: id, Agent: "coder", Status: "needs_review"})
	}
	put(pebblestore.ProjectTaskRecord{ID: "plain", Agent: "coder", Status: "needs_review", WorktreeBranch: "agent/plain", BaseBranch: "dev", UnintegratedCommits: 1})
	put(pebblestore.ProjectTaskRecord{ID: "finder", Agent: "finder", Status: "needs_review"})
	for _, id := range []string{"task_wf_r1", "task_wf_r2", "task_wf_r3"} {
		put(pebblestore.ProjectTaskRecord{ID: id, Agent: "reviewer", Status: "completed", AutomationID: "workflow:" + id, ReviewJob: taskRunJobID(projectID, "pushed")})
	}
	put(pebblestore.ProjectTaskRecord{ID: "abandoned", Agent: "coder", Status: "in_progress"})
	// busy's session has a running run.
	session := "busy-session-" + projectID
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: session, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-" + session, Session: &pebblestore.SessionSnapshot{ID: session, Mode: "auto"}},
		{SessionID: session, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-" + session, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-busy", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: session, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-" + session, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-busy", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = account, user, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	if state, ok, err := db.GetV3SessionRunState(session); err != nil || !ok || !state.Active {
		t.Fatalf("busy fixture run is not active: %v %v %+v", err, ok, state)
	}
	put(pebblestore.ProjectTaskRecord{ID: "busy", Agent: "coder", Status: "in_progress", SessionID: session})

	auto, _, err := autos.Put(account, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review."},
		{ID: "send-back", Type: jobautomation.StepReopen, From: "review", Message: "Blocked.", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
		{ID: "push", Type: jobautomation.StepRun, Command: "push.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(taskID string, end func(*jobautomation.Delivery)) {
		t.Helper()
		d, _, err := autos.CreateDelivery(auto, jobautomation.Result{JobID: taskRunJobID(projectID, taskID), Kind: jobautomation.KindTask, ProjectID: projectID, RunID: taskID, Folder: folder, Status: jobautomation.StatusSucceeded})
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Steps) != 3 {
			t.Fatalf("delivery steps: %+v", d.Steps)
		}
		d.FinishedAt = time.Now().UnixMilli()
		end(&d)
		if err := autos.SaveDelivery(d); err != nil {
			t.Fatal(err)
		}
	}
	deliver("pushed", func(d *jobautomation.Delivery) {
		d.State, d.Head = jobautomation.StateSucceeded, outcomeTestHead
		d.Steps[0].State, d.Steps[1].State, d.Steps[2].State = jobautomation.StateSucceeded, jobautomation.StateSkipped, jobautomation.StateSucceeded
	})
	deliver("sent-back", func(d *jobautomation.Delivery) {
		d.State, d.Round = jobautomation.StateSentBack, 2
		d.SentBack = &jobautomation.SentBack{Step: "send-back", Round: 2, Reason: "review verdict block"}
		d.Steps[0].State, d.Steps[1].State, d.Steps[2].State = jobautomation.StateSucceeded, jobautomation.StateSucceeded, jobautomation.StateSkipped
	})
	deliver("blocked", func(d *jobautomation.Delivery) {
		d.State, d.BlockedReason = jobautomation.StateBlocked, jobautomation.RoundsExhaustedReason("send-back")
		d.Steps[0].State, d.Steps[1].State, d.Steps[2].State = jobautomation.StateSucceeded, jobautomation.StateFailed, jobautomation.StatePending
	})
}

// outcomeTestRows reads a list_tasks answer's rows by id.
func outcomeTestRows(t *testing.T, raw []byte) (map[string]map[string]any, map[string]any) {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("decode list: %v %s", err, raw)
	}
	rows := map[string]map[string]any{}
	list, _ := answer["tasks"].([]any)
	for _, item := range list {
		row := item.(map[string]any)
		rows[row["id"].(string)] = row
	}
	return rows, answer
}

// checkOutcomeListing asserts the default listing: only the person's tasks,
// each saying what happened, reviewer tasks counted on their task, and the
// stale flag only on the abandoned in_progress task.
func checkOutcomeListing(t *testing.T, where string, rows map[string]map[string]any, answer map[string]any) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "abandoned,blocked,busy,finder,plain,pushed,sent-back" {
		t.Fatalf("%s default listing = %v", where, ids)
	}
	if n, _ := answer["workflow_tasks"].(float64); n != 3 {
		t.Fatalf("%s workflow_tasks = %v", where, answer["workflow_tasks"])
	}
	// include_workflow_tasks is not in the schema; the page names it.
	if answer["workflow_tasks_hint"] != tool.WorkflowTasksHint {
		t.Fatalf("%s workflow_tasks_hint = %v", where, answer["workflow_tasks_hint"])
	}
	want := map[string]string{
		"pushed":    "pushed " + outcomeTestHead[:12],
		"sent-back": "sent_back (round 2)",
		"blocked":   "blocked rounds_exhausted: step send-back",
		"plain":     "ready to integrate",
		// Nothing to integrate: finished, as get_task says.
		"finder": "finished",
	}
	for id, outcome := range want {
		if rows[id]["outcome"] != outcome {
			t.Fatalf("%s %s outcome = %v, want %q (row %v)", where, id, rows[id]["outcome"], outcome, rows[id])
		}
	}
	// The task's state (pebblestore.ProjectTaskState) leads each row, the
	// same word on this machine and on a linked one; the record's status is
	// only the run's.
	wantState := map[string]string{
		"pushed":    pebblestore.ProjectTaskStateLanded,
		"sent-back": pebblestore.ProjectTaskStateRunning,
		"blocked":   pebblestore.ProjectTaskStateNeedsYou,
		"plain":     pebblestore.ProjectTaskStateNotIntegrated,
		"finder":    pebblestore.ProjectTaskStateDone,
		"abandoned": pebblestore.ProjectTaskStateStopped,
	}
	for id, state := range wantState {
		if rows[id]["state"] != state || rows[id]["run_status"] == nil || rows[id]["status"] != nil {
			t.Fatalf("%s %s state = %v, want %q (row %v)", where, id, rows[id]["state"], state, rows[id])
		}
	}
	if rows["pushed"]["reviews"] != float64(3) {
		t.Fatalf("%s pushed reviews = %v", where, rows["pushed"]["reviews"])
	}
	for id, row := range rows {
		if _, ok := row["project_id"]; ok {
			t.Fatalf("%s row %s carries project_id: %v", where, id, row)
		}
		if _, ok := row["archived"]; ok {
			t.Fatalf("%s row %s carries archived: %v", where, id, row)
		}
		if stale, _ := row["stale"].(bool); stale != (id == "abandoned") {
			t.Fatalf("%s row %s stale = %v", where, id, row["stale"])
		}
		if id != "pushed" && row["reviews"] != nil {
			t.Fatalf("%s row %s reviews = %v", where, id, row["reviews"])
		}
	}
}

// moveTaskProgressClock reads task progress (and its stale flag) a little
// over a day from now.
func moveTaskProgressClock(t *testing.T) {
	t.Helper()
	moveTaskProgressClockBy(t, 25*time.Hour)
}

// moveTaskProgressClockBy reads task progress (stale and idle flags) by
// later than now.
func moveTaskProgressClockBy(t *testing.T, by time.Duration) {
	t.Helper()
	later := time.Now().Add(by)
	previous := taskProgressNow
	taskProgressNow = func() time.Time { return later }
	t.Cleanup(func() { taskProgressNow = previous })
}

// Purpose: the Orchestrator's list_tasks on this machine lists the person's
// work and says what happened to it. Requirement: tasks a workflow started
// (reviewer runs, task_wf_*) are left out by default and counted on the task
// they review (reviews: 3) and in workflow_tasks; include_workflow_tasks
// lists them; each row carries outcome from the task's latest workflow
// delivery (pushed <head12>, sent_back (round n), blocked <reason>) or, with
// no workflow, ready to integrate when it has commits and finished (as
// get_task's "finished, nothing to integrate") when it has none; an
// in_progress task with no active run for
// over a day is stale: true while one with an active run is not; rows drop
// project_id and archived:false; limit is at most 50.
// Threat: the live failure: a 16 KB answer of reviewer rows, needs_review
// read as "done" with no word on whether the change was pushed, and a
// week-old abandoned probe reported as running.
// Owners: tool executeManageProjectTasksContext list_tasks over
// ProjectTaskListFilter.Select (ExcludeWorkflowTasks) and the store progress
// hook (Server.installTaskProgress -> taskWaitProgressFor ->
// computeTaskProgress/taskProgressOutcome, markTaskProgressStale), over real
// session, task and job automation stores: the narrowest layer that holds
// both the task records and their deliveries.
func TestOrchestratorListTasksSaysWhatHappenedOnThisMachine(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	db := w.f.s.sessions.Store()
	seedOutcomeProject(t, db, w.store, w.f.account, w.f.user, "proj-out", w.folder)
	moveTaskProgressClock(t)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(w.f.s.sessions)
	runtime.SetManageProjectStore(db)
	scope := tool.WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}}
	list := func(args map[string]any) ([]byte, error) {
		t.Helper()
		args["action"], args["project_id"] = "list_tasks", "proj-out"
		raw, _ := json.Marshal(args)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			return nil, err
		}
		// The action's own answer is the envelope's data.
		return []byte(toolData(t, out)), nil
	}
	raw, err := list(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows, answer := outcomeTestRows(t, raw)
	checkOutcomeListing(t, "home", rows, answer)
	// get_task of the no-commit finder says the same thing as its row.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	getRaw, _ := json.Marshal(map[string]any{"action": "get_task", "project_id": "proj-out", "task_id": "finder"})
	got, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(getRaw)})
	if err != nil || !strings.Contains(got, "finished, nothing to integrate") {
		t.Fatalf("get_task finder: %v %s", err, got)
	}

	raw, err = list(map[string]any{"include_workflow_tasks": true})
	if err != nil {
		t.Fatal(err)
	}
	rows, answer = outcomeTestRows(t, raw)
	if len(rows) != 10 || rows["task_wf_r2"] == nil || answer["workflow_tasks"] != nil || answer["workflow_tasks_hint"] != nil || rows["pushed"]["reviews"] != nil {
		t.Fatalf("include_workflow_tasks listing: %d rows %v", len(rows), answer)
	}
	if _, err := list(map[string]any{"limit": float64(51)}); err == nil || !strings.Contains(err.Error(), "between 1 and 50") {
		t.Fatalf("limit 51: %v", err)
	}
	if raw, err := list(map[string]any{"limit": float64(50)}); err != nil || !strings.Contains(string(raw), `"limit":50`) {
		t.Fatalf("limit 50: %v %s", err, raw)
	}
}

// Purpose: a linked machine's list_tasks answers in the same shape as this
// machine's: its default page leaves out workflow tasks, counts reviews and
// carries outcome and stale computed on the machine, with no project_id or
// archived:false. Requirement as TestOrchestratorListTasksSaysWhatHappenedOnThisMachine;
// include_workflow_tasks and the 50-row bound cross the link.
// Threat: linked rows that only say needs_review, or carry the machine's
// reviewer runs, so the person hears the agent's run ended but not whether
// the work was pushed.
// Owners: Server.CallFleetProject, fleetTaskListQuery and
// compactFleetTaskList at home; the machine's task list route
// (projectTaskListFilterFromQuery include_workflow_tasks, taskProgressFor),
// over the real pinned link between two in-process daemons.
func TestOrchestratorListTasksSaysWhatHappenedOnLinkedMachine(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	autos := jobautomation.NewStore(remote.s.sessions.Store().Underlying())
	remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: autos}))
	seedOutcomeProject(t, remote.s.sessions.Store(), autos, remote.account, remote.user, "remote-out", t.TempDir())
	moveTaskProgressClock(t)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list := func(args map[string]any) ([]byte, error) {
		args["project_id"] = "remote-out"
		return home.s.CallFleetProject(ctx, p, "box", "list_tasks", args)
	}
	raw, err := list(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows, answer := outcomeTestRows(t, raw)
	checkOutcomeListing(t, "linked", rows, answer)
	for id, row := range rows {
		for key := range row {
			switch key {
			case "state", "id", "title", "run_status", "agent", "session_id", "updated_at", "outcome", "stale", "reviews", "needs_attention":
			default:
				t.Fatalf("linked row %s carries %q: %v", id, key, row)
			}
		}
	}
	raw, err = list(map[string]any{"include_workflow_tasks": true})
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := outcomeTestRows(t, raw); len(rows) != 10 || rows["task_wf_r1"] == nil {
		t.Fatalf("linked include_workflow_tasks: %d rows", len(rows))
	}
	for _, args := range []map[string]any{{"limit": float64(51)}, {"include_workflow_tasks": "yes"}} {
		if _, err := list(args); err == nil {
			t.Fatalf("linked list accepted %v", args)
		}
	}
}

// Purpose: the HTTP task board Swarm Console reads for "Needs you" and its
// task rows (GET /v3/projects/{id}/tasks, no list parameters) carries each
// task's derived state and reason in progress.task_state and
// progress.task_state_reason, next to the unchanged run status.
// Requirement: a pushed task whose run status stays needs_review reads
// landed; one with commits never handed over reads not_integrated; a
// workflow that ran out of rounds reads needs_you.
// Threat: a client that only has the run status lists landed work as
// waiting on the person.
// Owners: the project task list route (Server.handleProjects;
// projectTaskBoardRow.Progress from
// Server.taskProgressFor -> markTaskProgressAttention ->
// pebblestore.ProjectTaskState), over real session, task and job
// automation stores: the narrowest layer that serves the board's JSON.
func TestProjectTaskBoardRouteCarriesDerivedState(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	seedOutcomeProject(t, w.f.s.sessions.Store(), w.store, w.f.account, w.f.user, "proj-board", w.folder)
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/projects/proj-board/tasks", nil), w.f.user, w.f.account)
	rec := httptest.NewRecorder()
	// The route handler itself, as fleetTestSwarm.call reaches handleFleet
	// (attach-token transport auth is not what this test is about).
	w.f.s.handleProjects(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("task board: %d %s", rec.Code, rec.Body.String())
	}
	rows, _ := outcomeTestRows(t, rec.Body.Bytes())
	for id, want := range map[string]string{
		"pushed":  pebblestore.ProjectTaskStateLanded,
		"plain":   pebblestore.ProjectTaskStateNotIntegrated,
		"blocked": pebblestore.ProjectTaskStateNeedsYou,
		"finder":  pebblestore.ProjectTaskStateDone,
	} {
		row := rows[id]
		progress, _ := row["progress"].(map[string]any)
		reason, _ := progress["task_state_reason"].(string)
		if row["status"] != "needs_review" || progress["task_state"] != want || strings.TrimSpace(reason) == "" {
			t.Fatalf("%s: status=%v task_state=%v reason=%q, want needs_review, %s and a reason (row %v)", id, row["status"], progress["task_state"], reason, want, row)
		}
	}
	if reason := rows["pushed"]["progress"].(map[string]any)["task_state_reason"]; reason != "pushed "+outcomeTestHead[:12] {
		t.Fatalf("pushed reason = %v", reason)
	}
}

// Purpose: the stale rule itself. Requirement: only an in_progress (working)
// or blocked task, not archived, with no active run, whose task and session
// run state are both older than 24 h, is stale.
// Threat: a long active run flagged stale, or a waiting/finished task
// flagged. Owner: taskProgressStale (pure; the listing tests prove wiring).
func TestTaskProgressStaleRule(t *testing.T) {
	const day = taskStaleAfterMillis
	now := int64(10 * day)
	old := now - day - 1
	for _, c := range []struct {
		name      string
		task      pebblestore.ProjectTaskRecord
		active    bool
		lastRunAt int64
		want      bool
	}{
		{"in_progress idle a day", pebblestore.ProjectTaskRecord{Status: "in_progress", UpdatedAt: old}, false, 0, true},
		{"blocked idle a day", pebblestore.ProjectTaskRecord{Status: "blocked", UpdatedAt: old}, false, old, true},
		{"active run", pebblestore.ProjectTaskRecord{Status: "in_progress", UpdatedAt: old}, true, old, false},
		{"run changed recently", pebblestore.ProjectTaskRecord{Status: "in_progress", UpdatedAt: old}, false, now - 1000, false},
		{"task changed recently", pebblestore.ProjectTaskRecord{Status: "in_progress", UpdatedAt: now - 1000}, false, 0, false},
		{"needs_review", pebblestore.ProjectTaskRecord{Status: "needs_review", UpdatedAt: old}, false, 0, false},
		{"pending_approval", pebblestore.ProjectTaskRecord{Status: "pending_approval", UpdatedAt: old}, false, 0, false},
		{"archived", pebblestore.ProjectTaskRecord{Status: "in_progress", UpdatedAt: old, Archived: true}, false, 0, false},
	} {
		if got := taskProgressStale(c.task, c.active, c.lastRunAt, now); got != c.want {
			t.Fatalf("%s: stale = %v, want %v", c.name, got, c.want)
		}
	}
}
