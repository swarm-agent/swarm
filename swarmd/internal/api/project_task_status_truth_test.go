package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// bindTaskSession names sessionID as the session of projectID/taskID, as a
// delegated task's session is, so its run ends reconcile into the task.
func bindTaskSession(t *testing.T, s *Server, p identity.Principal, sessionID, projectID, taskID string) {
	t.Helper()
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("session %s: %v %v", sessionID, ok, err)
	}
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	session.Metadata["project_id"], session.Metadata["task_id"] = projectID, taskID
	key := "bind-task-" + sessionID
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: key, PayloadHash: key, Session: &session}); err != nil {
		t.Fatal(err)
	}
}

// waitTaskStatus waits until the stored task leaves from, then returns it.
func waitTaskStatus(t *testing.T, db *pebblestore.SessionStore, account, projectID, taskID, from string) *pebblestore.ProjectTaskRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		task, found, err := db.GetProjectTask(account, projectID, taskID)
		if err != nil || !found {
			t.Fatalf("task: %v %v", found, err)
		}
		if task.Status != from || time.Now().After(deadline) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeToolAnswer(t *testing.T, out string) map[string]any {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return answer
}

// Purpose: a task the person stops (stop_task) is reported as stopped, not
// failed: the run's end leaves the stored task status stopped with no
// "user stopped task" error, get_task says stopped with message_task as the
// next action, list_tasks counts it under stopped (never needs_attention)
// both natively and on the task list route, and no task.run.* job end is
// signalled (the signal a watcher or workflow reads as a failed or cancelled
// job). A cancellation that is not the person's stop stays failed
// (TestProjectTaskStoppedAndCountRules).
// Threat: the live report: a deliberate stop listed as failed with error
// "user stopped task", counted among failures and raised to watchers.
// Owners: Server.StopProjectTask -> stopActiveSessionRun ->
// sessionV3Executor.CancelRun -> recordRunStatus -> settleTaskRunEnd
// (reconcileProjectTaskRunLifecycle, signalTaskRunEnded), syncTaskSessionState,
// compactProjectTask and the list counts (ProjectTaskCountBucket). Narrowest
// layer: the real executor with a provider that blocks until cancelled, since
// the status is written where the run's end is recorded.
func TestStopTaskLeavesTheTaskStoppedNotFailed(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "stopped.pebble"), nil)
	defer f.close()
	signals := pebblestore.NewSignalStore(f.sessions.Store().Underlying())
	f.server.SetSignalStore(signals)
	started := make(chan struct{}, 1)
	f.runner.handler = func(ctx context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return provideriface.Response{}, ctx.Err()
	}
	taskSession := f.createSession("task-session")
	orchestrator := f.createSession("orchestrator")
	p := testPrincipal()
	db := f.sessions.Store()
	seedMessageTask(t, db, p, taskSession)
	if _, err := db.UpdateProjectTask(p.AccountScopeID, messageTaskProject, messageTaskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "in_progress"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+taskSession+"/messages", `{"client_request_id":"start-1","role":"user","content":"work"}`); code != http.StatusOK {
		t.Fatalf("start: %d %s", code, out)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("task run never started")
	}
	// The running session becomes the task's own, as a delegated task's
	// session is, so its run's end reconciles into the task.
	bindTaskSession(t, f.server, p, taskSession, messageTaskProject, messageTaskID)
	runID := stableSessionsV3PrimaryRunID(taskSession, "start-1")
	rt := messageTaskRuntime(t, f.server)
	if _, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "stop_task", "project_id": messageTaskProject, "task_id": messageTaskID}); err != nil {
		t.Fatal(err)
	}
	f.waitRun(taskSession, runID, sessionruntime.RunIntentCancelled)

	task := waitTaskStatus(t, db, p.AccountScopeID, messageTaskProject, messageTaskID, "in_progress")
	if task.Status != pebblestore.ProjectTaskStatusStopped || task.LastError != "" || !strings.Contains(task.ActionNeeded, "message_task") {
		t.Fatalf("stopped task stored as status=%q last_error=%q action=%q, want stopped with no error", task.Status, task.LastError, task.ActionNeeded)
	}

	out, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "get_task", "project_id": messageTaskProject, "task_id": messageTaskID})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := decodeToolAnswer(t, out)["task"].(map[string]any)
	if view["status"] != "stopped" || view["next_action"] != "message_task" || view["last_error"] != nil {
		t.Fatalf("get_task of a stopped task: %s", out)
	}

	out, err = callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "list_tasks", "project_id": messageTaskProject})
	if err != nil {
		t.Fatal(err)
	}
	listed := decodeToolAnswer(t, out)
	counts, _ := listed["outcome_counts"].(map[string]any)
	rows, _ := listed["tasks"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["status"] != "stopped" || counts["stopped"] != float64(1) || counts["needs_attention"] != float64(0) {
		t.Fatalf("list_tasks of a stopped task: %s", out)
	}

	code, body := f.do(http.MethodGet, "/v3/projects/"+messageTaskProject+"/tasks?scope=all", "")
	var page struct {
		StatusCounts  map[string]int   `json:"status_counts"`
		OutcomeCounts map[string]int   `json:"outcome_counts"`
		Tasks         []map[string]any `json:"tasks"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &page) != nil {
		t.Fatalf("task list route: %d %s", code, body)
	}
	if page.StatusCounts["failed"] != 0 || page.OutcomeCounts["stopped"] != 1 || page.OutcomeCounts["needs_attention"] != 0 || len(page.Tasks) != 1 || page.Tasks[0]["status"] != "stopped" {
		t.Fatalf("task list route of a stopped task: %s", body)
	}

	ended, err := signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID, KindPrefixes: []string{"task.run."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ended.Signals) != 0 {
		t.Fatalf("a stop raised job end signals: %+v", ended.Signals)
	}
}

// outcomeCountsWant is seedOutcomeProject's default listing plus one stopped
// task, by outcome: pushed is done; sent-back and busy are in progress;
// blocked and plain (ready to integrate, no workflow) need attention;
// abandoned (stale) and the stopped task are stopped. Raw status counts say
// needs_review 4 for the same tasks.
var outcomeCountsWant = map[string]float64{"done": 1, "in_progress": 2, "waiting": 0, "needs_attention": 2, "stopped": 2}

func checkOutcomeCounts(t *testing.T, where string, answer map[string]any) {
	t.Helper()
	counts, _ := answer["outcome_counts"].(map[string]any)
	if len(counts) != len(outcomeCountsWant) {
		t.Fatalf("%s outcome_counts = %v, want %v", where, answer["outcome_counts"], outcomeCountsWant)
	}
	for bucket, n := range outcomeCountsWant {
		if counts[bucket] != n {
			t.Fatalf("%s outcome_counts[%s] = %v, want %v (all %v)", where, bucket, counts[bucket], n, counts)
		}
	}
}

func seedStoppedOutcomeTask(t *testing.T, db *pebblestore.SessionStore, account, projectID string) {
	t.Helper()
	if err := db.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "stopped", ProjectID: projectID, AccountID: account, Title: "Task stopped", Agent: "coder", Status: pebblestore.ProjectTaskStatusStopped}); err != nil {
		t.Fatal(err)
	}
}

// Purpose: list_tasks on this machine counts tasks by what happened to their
// work, not by raw status: a pushed task is done (never waiting on anyone),
// a sent-back or running one in progress, a blocked or ready-to-integrate
// one (no workflow hands it over) needs attention, a stale or stopped one is
// stopped; every bucket is present. Threat: "689 tasks waiting for review"
// from needs_review counts when the rows say pushed. Owners: tool
// executeManageProjectTasksContext list_tasks (ProjectTaskListPage.Counted,
// the store progress hook) and pebblestore.ProjectTaskCountBucket, over real
// task and job automation stores (seedOutcomeProject): the narrowest layer
// holding both the tasks and their deliveries.
func TestListTasksCountsByOutcomeOnThisMachine(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	db := w.f.s.sessions.Store()
	seedOutcomeProject(t, db, w.store, w.f.account, w.f.user, "proj-count", w.folder)
	seedStoppedOutcomeTask(t, db, w.f.account, "proj-count")
	moveTaskProgressClock(t)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(w.f.s.sessions)
	runtime.SetManageProjectStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"action": "list_tasks", "project_id": "proj-count", "limit": float64(2)})
	out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	answer := decodeToolAnswer(t, out)
	// The counts cover every matching task, not only the page.
	if rows, _ := answer["tasks"].([]any); len(rows) != 2 || answer["total"] != float64(7) {
		t.Fatalf("page: %s", out)
	}
	checkOutcomeCounts(t, "home", answer)
}

// Purpose: a linked machine's list_tasks counts the same way: the machine's
// task list route computes outcome_counts from its own task progress, the
// home passes them on and leaves out the raw status_counts (needs_review
// 4). Threat as TestListTasksCountsByOutcomeOnThisMachine, across the link.
// Owners: the machine's task list route (projectTaskOutcomeCounts) and
// compactFleetTaskList at home, over the real pinned link between two
// in-process daemons.
func TestListTasksCountsByOutcomeOnLinkedMachine(t *testing.T) {
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
	seedOutcomeProject(t, remote.s.sessions.Store(), autos, remote.account, remote.user, "remote-count", t.TempDir())
	seedStoppedOutcomeTask(t, remote.s.sessions.Store(), remote.account, "remote-count")
	moveTaskProgressClock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := home.s.CallFleetProject(ctx, identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}, "box", "list_tasks", map[string]any{"project_id": "remote-count", "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	answer := decodeToolAnswer(t, string(raw))
	if _, has := answer["status_counts"]; has {
		t.Fatalf("linked list carries raw status_counts: %s", raw)
	}
	checkOutcomeCounts(t, "linked", answer)
}

// taskMessages reads get_task's messages from a manage_projects answer.
func taskMessages(t *testing.T, out string) []map[string]any {
	t.Helper()
	view, _ := decodeToolAnswer(t, out)["task"].(map[string]any)
	list, _ := view["messages"].([]any)
	messages := make([]map[string]any, 0, len(list))
	for _, item := range list {
		messages = append(messages, item.(map[string]any))
	}
	return messages
}

// Purpose: a message sent with message_task can be confirmed with get_task:
// while the run has not taken it, the compact view lists it as queued for
// that run; once the run's model received it (the run loop's feedback
// delivery receipt), as delivered in that run; its text is cut to 80
// characters and the whole view stays small. Threat: the live report: the
// reply said queued and nothing later said whether it arrived.
// Owners: queueProjectTaskMessage (the note's message_task source),
// acceptSessionsV3QueuedNote, the run loop's RecordFeedbackDelivery,
// SessionStore.ProjectTaskMessages and compactProjectTask. Narrowest layer:
// the real HTTP route, executor and run loop with a scripted model, since
// queueing and delivery span them.
func TestGetTaskShowsMessageQueuedThenDelivered(t *testing.T) {
	steer := "please also update the changelog " + strings.Repeat("and keep going ", 10)
	var (
		mu       sync.Mutex
		duringRT *tool.Runtime
		orch     string
		during   string
		duringE  error
	)
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "finished", StopReason: "stop"}
		}
		body, _ := json.Marshal(map[string]any{"message": steer, "client_request_id": "steer-1"})
		f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/"+messageTaskID+"/message", string(body))
		mu.Lock()
		during, duringE = callManageProjects(t, duringRT, orch, testPrincipal(), map[string]any{"action": "get_task", "project_id": messageTaskProject, "task_id": messageTaskID})
		mu.Unlock()
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	p := testPrincipal()
	seedMessageTask(t, f.sessions.Store(), p, f.sessionID)
	orchestrator := f.createSession("orchestrator")
	mu.Lock()
	duringRT, orch = messageTaskRuntime(t, f.server), orchestrator
	mu.Unlock()
	f.say(f.sessionID, "start-1", "begin the task")
	runID := stableSessionsV3PrimaryRunID(f.sessionID, "start-1")

	mu.Lock()
	out, err := during, duringE
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	queued := taskMessages(t, out)
	if len(queued) != 1 || queued[0]["state"] != "queued" || queued[0]["run_id"] != runID || queued[0]["text"] != string([]rune(strings.Join(strings.Fields(steer), " "))[:80]) {
		t.Fatalf("get_task while the message waits: %s", out)
	}
	out, err = callManageProjects(t, duringRT, orchestrator, p, map[string]any{"action": "get_task", "project_id": messageTaskProject, "task_id": messageTaskID})
	if err != nil {
		t.Fatal(err)
	}
	delivered := taskMessages(t, out)
	if len(delivered) != 1 || delivered[0]["state"] != "delivered" || delivered[0]["run_id"] != runID {
		t.Fatalf("get_task after the run took the message: %s", out)
	}
	if len(out) > 2048 {
		t.Fatalf("get_task is %d bytes, want at most 2 KB: %s", len(out), out)
	}
	// A note the person typed into the session is not a message_task message.
	for _, m := range f.messages() {
		if runruntime.IsQueuedNote(m) && m.Metadata[pebblestore.ProjectTaskMessageSourceMetadata] != pebblestore.ProjectTaskMessageSourceTask {
			t.Fatalf("message_task note stored without its source: %+v", m.Metadata)
		}
	}
}

// Purpose: from the home, get_task on a linked machine's task shows the
// message_task message the same way: queued for the remote run, then
// delivered once the remote run loop receipts it; and stop_task leaves the
// remote task stopped (not failed) with message_task as the next action.
// Threat: linked work whose messages cannot be confirmed, or whose stop
// reads as a failure. Owners: the remote task route's messages
// (SessionStore.ProjectTaskMessages), CompactProjectTaskAnswer at home and
// the remote StopProjectTask -> settleTaskRunEnd, over the real pinned link
// between two in-process daemons.
func TestLinkedGetTaskShowsMessageDeliveryAndStop(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	remote := h.remote.s
	rp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}
	if _, err := remote.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", "t1", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "in_progress"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bindTaskSession(t, remote, rp, "rs-t1", "remote-project", "t1")
	for _, in := range []sessionruntime.SessionMutationInput{
		{ClientRequestID: "rs-pending", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", Status: pebblestore.V3RunIntentPendingExecutor}},
		{ClientRequestID: "rs-running", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.Kind, in.SessionID, in.UserID, in.AccountScopeID = sessionruntime.SessionMutationRecordRunIntent, "rs-t1", h.remote.user, h.remote.account
		in.IdempotencyKey, in.PayloadHash = in.ClientRequestID, in.ClientRequestID
		if _, err := remote.applySessionV3PrimaryMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	remote.v3SessionExecutor.mu.Lock()
	remote.v3SessionExecutor.openQueuedNotesLocked(sessionV3ExecutorJob{SessionID: "rs-t1", RunID: "child-run", queuedNotes: runruntime.NewQueuedNoteTracker(0)})
	remote.v3SessionExecutor.mu.Unlock()

	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "message_task", "machine": "box", "project_id": "remote-project", "task_id": "t1", "message": "also cover the edge case"}); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		t.Helper()
		answer, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := answer["data"].(map[string]any)
		task, _ := data["task"].(map[string]any)
		if task == nil {
			t.Fatalf("linked get_task: %v", answer)
		}
		return task
	}
	message := func(task map[string]any) map[string]any {
		t.Helper()
		list, _ := task["messages"].([]any)
		if len(list) != 1 {
			t.Fatalf("linked get_task messages: %v", task["messages"])
		}
		return list[0].(map[string]any)
	}
	if m := message(get()); m["state"] != "queued" || m["run_id"] != "child-run" || m["text"] != "also cover the edge case" {
		t.Fatalf("linked message before delivery: %v", m)
	}
	// The remote run loop receipts the note after the provider step that
	// carried it succeeded.
	messages, err := remote.sessions.ListSessionMessages("rs-t1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var notes []pebblestore.MessageSnapshot
	for _, m := range messages {
		if runruntime.IsQueuedNote(m) {
			notes = append(notes, m)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("remote notes: %+v", notes)
	}
	if err := runruntime.RecordFeedbackDelivery(remote.applySessionV3PrimaryMutation, "rs-t1", "child-run", notes); err != nil {
		t.Fatal(err)
	}
	if m := message(get()); m["state"] != "delivered" || m["run_id"] != "child-run" {
		t.Fatalf("linked message after delivery: %v", m)
	}

	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "stop_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"}); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, remote.sessions.Store(), h.remote.account, "remote-project", "t1", "in_progress")
	if task := get(); task["status"] != "stopped" || task["next_action"] != "message_task" || task["last_error"] != nil {
		t.Fatalf("linked get_task of a stopped task: %v", task)
	}
}
