package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// waitWorkflowFixture is one daemon (the machine owner's, so its task job
// ends are the ones the workflow runner reads) with a job automation store,
// a project folder and a coder task's own session.
type waitWorkflowFixture struct {
	f      *fleetTestSwarm
	store  *jobautomation.Store
	folder string
}

func newWaitWorkflowFixture(t *testing.T) *waitWorkflowFixture {
	t.Helper()
	w := newWaitWorkflowFixtureBeforeAutomations(t)
	w.f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: w.store}))
	return w
}

// newWaitWorkflowFixtureBeforeAutomations is the fixture before the daemon
// installed job automations (and with them the wait progress hook).
func newWaitWorkflowFixtureBeforeAutomations(t *testing.T) *waitWorkflowFixture {
	t.Helper()
	f := newFleetTestSwarm(t, "local")
	t.Cleanup(f.cleanup)
	if f.s.OutboxAccount() != f.account {
		t.Fatalf("fixture account %q is not the machine owner's %q", f.account, f.s.OutboxAccount())
	}
	store := jobautomation.NewStore(f.s.sessions.Store().Underlying())
	// The task's own session, named by its project and task as delegated
	// task sessions are.
	snapshot := pebblestore.SessionSnapshot{ID: "task-session", UserID: f.user, AccountScopeID: f.account, Metadata: map[string]any{"project_id": "proj-1", "task_id": "task-1"}}
	if _, err := f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: "task-session", UserID: f.user, AccountScopeID: f.account, Session: &snapshot, ClientRequestID: "task-session", IdempotencyKey: "task-session", PayloadHash: "task-session", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	return &waitWorkflowFixture{f: f, store: store, folder: t.TempDir()}
}

// endWindow opens the task session's run end window, as settleTaskRunEnd
// does around the status write and the job end signal; the returned
// function closes it.
func (w *waitWorkflowFixture) endWindow() func() {
	return w.f.s.openTaskRunEnd(sessionV3ExecutorJob{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}, SessionID: "task-session", RunID: pebblestore.InitialTaskRunID("task-1")})
}

// task is a coder task that finished its first run (needs_review), as the
// wait reads it; updatedAt is when it last changed.
func (w *waitWorkflowFixture) task(updatedAt int64) pebblestore.ProjectTaskRecord {
	task := pebblestore.ProjectTaskRecord{ID: "task-1", ProjectID: "proj-1", AccountID: w.f.account, Title: "Rate limit", Agent: "coder", Status: "needs_review",
		SessionID: "task-session", WorkspacePath: w.folder, UpdatedAt: updatedAt, ActiveAttemptID: "initial",
		Attempts: []pebblestore.ProjectTaskAttempt{{ID: "initial", SessionID: "task-session", Role: "coder", RunID: pebblestore.InitialTaskRunID("task-1"), Status: "needs_review"}}}
	task.SourceWorkspace.Path = w.folder
	return task
}

func (w *waitWorkflowFixture) workflow(t *testing.T) jobautomation.Automation {
	t.Helper()
	auto, _, err := w.store.Put(w.f.account, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: w.folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review."},
		{ID: "send-back", Type: jobautomation.StepReopen, From: "review", Message: "Blocked.", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return auto
}

func workflowState(p *taskProgress) string {
	if p == nil || p.Workflow == nil {
		return ""
	}
	return p.Workflow.State
}

// Purpose: the wait's progress says "pending" while the task's latest end has
// not been evaluated against the workflows, closing the window in which the
// task is already needs_review but its job end is not marked yet (the status
// write reconciles waits before signalTaskRunEnded marks the end).
// Requirement: inferred pending holds only inside that run end window
// (settleTaskRunEnd); outside it, an unmarked end is never pending (see
// TestTaskWaitUnmarkedEndDoesNotHoldTheWait). Inside it: with a workflow
// that could apply and no evaluation, pending;
// after the runner evaluated the end with nothing matched, none (ready); a
// send-back that went out for a follow-up whose end is not evaluated is
// pending, also once the follow-up finished, until that end is evaluated. Negative: no workflow for the folder,
// a workflow created after the task's end, a task started by a workflow, a
// task of an agent whose ends are not job ends, and another account's task
// never wait on a workflow (a wait on them must not hang).
// Threat: the Orchestrator woken with nothing landed; a wait stuck forever.
// Authority: Server.taskWaitProgressFor / taskEndAwaitsWorkflows over a real
// store and job automation store (the narrowest layer with both records).
func TestTaskWaitProgressPendingUntilEndEvaluated(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	now := time.Now().UnixMilli()
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(now), now)); got != "" {
		t.Fatalf("no workflow for the folder: workflow %q", got)
	}
	auto := w.workflow(t)
	later := time.Now().UnixMilli() + 1
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(later), later)); got == workflowPending {
		t.Fatal("an unmarked end with no run end in flight was inferred pending")
	}
	closeWindow := w.endWindow()
	defer closeWindow()
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(later), later)); got != workflowPending {
		t.Fatalf("first end not evaluated yet: workflow %q, want pending", got)
	}
	for name, task := range map[string]pebblestore.ProjectTaskRecord{
		"workflow created after the end": w.task(auto.CreatedAt - 1),
		"started by a workflow": func() pebblestore.ProjectTaskRecord {
			task := w.task(later)
			task.AutomationID = auto.ID
			return task
		}(),
		"not a job end agent": func() pebblestore.ProjectTaskRecord {
			task := w.task(later)
			task.Agent = "image"
			return task
		}(),
		"still running": func() pebblestore.ProjectTaskRecord {
			task := w.task(later)
			task.Status = "in_progress"
			return task
		}(),
	} {
		if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, task, later)); got == workflowPending {
			t.Fatalf("%s: workflow pending", name)
		}
	}
	if got := workflowState(w.f.s.taskWaitProgressFor("another-account", w.task(later), later)); got == workflowPending {
		t.Fatal("another account's task waits on this machine owner's workflows")
	}
	jobID := taskRunJobID("proj-1", "task-1")
	if err := w.store.MarkJobEnded(w.f.account, "proj-1", jobID, "", 1, later); err != nil {
		t.Fatal(err)
	}
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(later), later)); got != workflowPending {
		t.Fatalf("end marked, not evaluated: workflow %q", got)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 1, later, nil); err != nil {
		t.Fatal(err)
	}
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(later), later)); got != workflowNone {
		t.Fatalf("evaluated, nothing matched: workflow %q, want none", got)
	}
	// A later run in the task's session that is not a job end (its session's
	// latest run) does not hold the wait.
	chat := w.task(later)
	chat.Attempts[0].RunID = "unrelated-run"
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, chat, later)); got != workflowNone {
		t.Fatalf("a non job end run held the wait: workflow %q", got)
	}
	// A review BLOCK sent the work back: the reopen step succeeded for the
	// follow-up run-2, whose end is not evaluated yet.
	d, _, err := w.store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, ProjectID: "proj-1", RunID: "task-1", Folder: w.folder, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State, d.FinishedAt = jobautomation.StateSucceeded, later
	d.Steps[0].State = jobautomation.StateSucceeded
	d.Steps[1].State, d.Steps[1].Outcome = jobautomation.StateSucceeded, map[string]any{"followup": "run-2"}
	if err := w.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 2, later, []string{d.ID}); err != nil {
		t.Fatal(err)
	}
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.task(later), later)); got != workflowPending {
		t.Fatalf("sent back, follow-up end not evaluated: workflow %q", got)
	}
	// The follow-up run-2 finished; its end is not evaluated yet.
	followup := w.task(later)
	followup.Attempts[0].RunID = "run-2"
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, followup, later)); got != workflowPending {
		t.Fatalf("follow-up finished, end not evaluated: workflow %q", got)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "run-2", 3, later, nil); err != nil {
		t.Fatal(err)
	}
	// Evaluated, no new delivery: the latest delivery (the finished
	// send-back round) stands, so the workflow ended.
	if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, followup, later)); got != workflowDone {
		t.Fatalf("follow-up end evaluated: workflow %q, want done", got)
	}
}

// remoteWorkflow gives the remote machine a job automation store, a review
// workflow for t1 and a delivery for t1's end in state running, evaluated.
func (h *remoteWaitHarness) remoteWorkflow(t *testing.T) (*jobautomation.Store, jobautomation.Delivery) {
	t.Helper()
	store := jobautomation.NewStore(h.remote.s.sessions.Store().Underlying())
	h.remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	folder := t.TempDir()
	auto, _, err := store.Put(h.remote.account, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review."},
		{ID: "push", Type: jobautomation.StepRun, Command: "push.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := taskRunJobID("remote-project", "t1")
	d, _, err := store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, ProjectID: "remote-project", RunID: "t1", Folder: folder, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State, d.Steps[0].State = jobautomation.StateRunning, jobautomation.StateRunning
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvaluation(h.remote.account, "remote-project", jobID, "", 1, 1, []string{d.ID}); err != nil {
		t.Fatal(err)
	}
	return store, d
}

// endDelivery ends the machine's delivery the way the runner does after its
// last step (the push run step): only the delivery record is saved, no session
// on the machine records anything.
func endDelivery(t *testing.T, store *jobautomation.Store, d jobautomation.Delivery, state string) {
	t.Helper()
	d.State, d.FinishedAt = state, time.Now().UnixMilli()
	d.Steps[0].State, d.Steps[1].State = jobautomation.StateSucceeded, state
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
}

func pushed(t *testing.T, store *jobautomation.Store, d jobautomation.Delivery) {
	t.Helper()
	endDelivery(t, store, d, jobautomation.StateSucceeded)
}

// waitUntil calls wait_tasks with machine=box and until.
func (h *remoteWaitHarness) waitUntil(t *testing.T, until string, ids ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: remoteWaitGoal, RunID: "run-1"}), 20*time.Second)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"action": "wait_tasks", "machine": "box", "project_id": "remote-project", "task_ids": ids, "until": until})
	out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Purpose: a linked-machine wait wakes when the task's work on the machine
// settled, read from the progress the machine reports through get_task, even
// when the workflow's end records no session activity on the machine.
// Requirement: the observation carries the workflow and integration progress;
// needs_review with the machine's workflow running does not wake the home;
// the workflow ending wakes it once with workflow_state in the row, whether it
// pushed (done), ran out of rounds (blocked) or failed; the registration
// result reports until=settled. After the coder's session ends nothing but
// the machine's delivery record changes: the wake must come from the
// machine's project.updated activity (job automation notify ->
// NotifyProjectTaskChanged -> activity feed), not from session events.
// Threat: the home's Orchestrator woken when only the remote coder finished,
// or never woken once the workflow ended by a run step or as blocked/failed
// (heartbeats keep the link up, so the unreachable deadline never fires).
// Boundary: tool executeFleetWaitTasks, Server.followRemoteTaskWait, the
// machine's activityPage and its task detail route (taskWaitProgressFor) over
// two in-process daemons.
func TestRemoteTaskWaitSettledWakesAtWorkflowEnd(t *testing.T) {
	for _, end := range []struct{ delivery, workflow string }{
		{jobautomation.StateSucceeded, workflowDone},
		{jobautomation.StateBlocked, workflowBlocked},
		{jobautomation.StateFailed, workflowFailed},
	} {
		t.Run(end.workflow, func(t *testing.T) {
			h := newRemoteWaitHarness(t, "t1")
			store, d := h.remoteWorkflow(t)
			if result := h.waitUntil(t, "", "t1"); result["until"] != pebblestore.ProjectTaskWaitUntilSettled {
				t.Fatalf("registration result: %v", result)
			}
			h.home.s.watchRemoteTaskWait(remoteWaitGoal, "run-1")
			time.Sleep(200 * time.Millisecond)
			h.finish(t, "t1", "needs_review", "Ready.", "")
			h.noWake(t, 400*time.Millisecond, "needs_review while the machine's workflow runs")
			endDelivery(t, store, d, end.delivery) // no session activity follows
			row := remoteWaitRow(t, h.awaitWake(t), 0)
			if row["status"] != "needs_review" || row["workflow_state"] != end.workflow || row["workflow"] != "review loop" || row["machine"] != "box" {
				t.Fatalf("%s row: %v", end.workflow, row)
			}
		})
	}
}

// Purpose: a linked wait wakes when the machine's runner evaluated the task's
// end and no workflow matched ("none"), which writes only an evaluation
// record on the machine. Requirement: with the job end marked and not yet
// evaluated the wait sleeps; the evaluation with nothing matched wakes it,
// with no session activity after it. Threat: a wait stuck on a machine whose
// workflows did not apply to the task. Boundary: as above.
func TestRemoteTaskWaitSettledWakesWhenNoWorkflowMatched(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	store := jobautomation.NewStore(h.remote.s.sessions.Store().Underlying())
	h.remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	jobID := taskRunJobID("remote-project", "t1")
	if err := store.MarkJobEnded(h.remote.account, "remote-project", jobID, "", 1, 1); err != nil {
		t.Fatal(err)
	}
	h.register(t, "t1")
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Ready.", "")
	h.noWake(t, 400*time.Millisecond, "needs_review with the end not evaluated")
	if err := store.RecordEvaluation(h.remote.account, "remote-project", jobID, "", 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["status"] != "needs_review" || row["workflow_state"] != workflowNone {
		t.Fatalf("none row: %v", row)
	}
}

// Purpose: until=finished keeps the status-only rule for linked waits.
// Requirement: needs_review wakes while the machine's workflow still runs, and
// the row says the workflow is running. Boundary: as above.
func TestRemoteTaskWaitUntilFinished(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.remoteWorkflow(t)
	if result := h.waitUntil(t, "finished", "t1"); result["until"] != pebblestore.ProjectTaskWaitUntilFinished {
		t.Fatalf("registration result: %v", result)
	}
	if owner, _ := h.intent(t, "run-1"); owner.TaskWait == nil || owner.TaskWait.Until != pebblestore.ProjectTaskWaitUntilFinished {
		t.Fatalf("until not persisted: %+v", owner.TaskWait)
	}
	h.home.s.watchRemoteTaskWait(remoteWaitGoal, "run-1")
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Ready.", "")
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["status"] != "needs_review" || row["workflow_state"] != workflowRunning {
		t.Fatalf("finished row: %v", row)
	}
}

// Purpose: a daemon restart while a linked wait waits keeps the settled rule.
// Requirement: ResumeLinkedTaskWaits resumes the persisted wait; the remote
// task at needs_review with its workflow running does not wake it; the
// workflow ending does. Threat: recovery waking early or never. Boundary: the
// startup hook over the same durable store (not a full process restart).
func TestRemoteTaskWaitResumeKeepsSettledRule(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	store, d := h.remoteWorkflow(t)
	h.commitWait(t, "t1")
	h.home.s.ResumeLinkedTaskWaits()
	if n := h.watchers(); n != 1 {
		t.Fatalf("watchers after recovery: %d", n)
	}
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Ready.", "")
	h.noWake(t, 400*time.Millisecond, "recovered wait woke while the workflow runs")
	pushed(t, store, d) // no session activity follows
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["workflow_state"] != workflowDone {
		t.Fatalf("outcome after recovery: %v", row)
	}
}

// Purpose: the machine going unreachable for less than the grace period and
// coming back does not end a settled wait, and the work settling while it was
// away is read on reconnect. Requirement: no wake while offline (the workflow
// was running), then on reconnect the pushed state wakes the goal with
// workflow_state done (not machine_unreachable). Threat: a short outage
// resolving the wait as unreachable, or the change made while offline never
// being read. Boundary: Server.followRemoteTaskWait's offline/online handling
// over the real link listener closed and reopened on the same address.
func TestRemoteTaskWaitUnreachableThenBack(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.home.s.remoteTaskWaitGrace = time.Minute
	store, d := h.remoteWorkflow(t)
	h.register(t, "t1")
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Ready.", "")
	h.noWake(t, 300*time.Millisecond, "needs_review while the machine's workflow runs")
	address := h.remote.s.fleetListenAddress()
	h.remote.s.CloseFleetListener()
	pushed(t, store, d)
	h.noWake(t, 300*time.Millisecond, "woke while the machine was unreachable")
	if _, err := h.remote.s.listenForHomes(address); err != nil {
		t.Fatal(err)
	}
	row := remoteWaitRow(t, h.awaitWake(t), 0)
	if row["status"] != "needs_review" || row["workflow_state"] != workflowDone {
		t.Fatalf("outcome after the machine came back: %v", row)
	}
}

// localWait puts task-1 (in_progress, in the workflow's folder, delegated in
// task-session) in project proj-1 and registers a settled wait on it for a
// project Orchestrator goal running run-1.
func (w *waitWorkflowFixture) localWait(t *testing.T) {
	t.Helper()
	db := w.f.s.sessions.Store()
	if err := db.PutProject(w.f.account, &pebblestore.ProjectRecord{ID: "proj-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	task := w.task(time.Now().UnixMilli())
	task.Status, task.Attempts[0].Status = "in_progress", "in_progress"
	// The store accepts only a complete source binding; the folder is its
	// workspace path then.
	task.SourceWorkspace = pebblestore.ProjectTaskSource{}
	if err := db.PutProjectTask(w.f.account, &task); err != nil {
		t.Fatal(err)
	}
	goal := map[string]any{"swarm_v3_project_id": "proj-1", "project_id": "proj-1", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-goal", Session: &pebblestore.SessionSnapshot{ID: "goal", Mode: "auto", Metadata: goal}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.waiting_tasks", ClientRequestID: "task-wait:run-1", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "proj-1", TaskIDs: []string{"task-1"}}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = w.f.account, w.f.user, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.goalIntent(t, "run-1"); got.Status != pebblestore.V3RunIntentWaitingTasks {
		t.Fatalf("wait not registered: %+v", got)
	}
}

func (w *waitWorkflowFixture) goalIntent(t *testing.T, run string) pebblestore.V3SessionRunIntent {
	t.Helper()
	intent, _, err := w.f.s.sessions.Store().GetV3SessionRunIntent("goal", run)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func (w *waitWorkflowFixture) woke(t *testing.T) bool {
	t.Helper()
	_, found, err := w.f.s.sessions.Store().GetV3SessionRunIntent("goal", pebblestore.ProjectTaskWaitResumeID("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// finishTask moves task-1 to needs_review the way the run end's status write
// does; the write reconciles the project's waits.
func (w *waitWorkflowFixture) finishTask(t *testing.T) {
	t.Helper()
	if _, err := w.f.s.sessions.Store().UpdateProjectTask(w.f.account, "proj-1", "task-1", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.Attempts[0].Status = "needs_review", "needs_review"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (w *waitWorkflowFixture) currentTask(t *testing.T) pebblestore.ProjectTaskRecord {
	t.Helper()
	task, found, err := w.f.s.sessions.Store().GetProjectTask(w.f.account, "proj-1", "task-1")
	if err != nil || !found {
		t.Fatalf("task: %v %v", found, err)
	}
	return *task
}

// Purpose: inferred "pending" (a finished task with an applicable workflow
// whose job end is not marked yet) is bounded by the run end being settled,
// so a settled wait on such a task never stalls forever.
// Requirement: while the run end window is open the status write does not
// wake the wait; when the window closes with the end never marked (no
// signal store, a failed signal write, a run end that is not a job end), the
// waits are re-checked and the wait wakes with no workflow pending; when the
// end was marked, closing the window does not wake it early and the runner's
// evaluation (nothing matched) wakes it. With no window open at all (a crash
// between the status write and the signal, a restart, an end from before
// marking existed) the unmarked end is not pending and the startup
// reconciliation wakes the wait.
// Threat: the Orchestrator's goal stalled with no wake and no visible reason
// (reviewer finding: unbounded inference from workflow definitions).
// Authority: Server.openTaskRunEnd / taskEndAwaitsWorkflows / markTaskJobEnded
// with the real project publisher (ConfigureProjectRealtime) reconciling the
// durable wait in a real store; the narrowest layer owning both the window
// and the wake.
func TestTaskWaitUnmarkedEndDoesNotHoldTheWait(t *testing.T) {
	t.Run("window_closes_unmarked", func(t *testing.T) {
		w := newWaitWorkflowFixture(t)
		w.workflow(t)
		w.localWait(t)
		closeWindow := w.endWindow()
		w.finishTask(t)
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if w.woke(t) {
			t.Fatal("woke while the run end was being settled")
		}
		if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.currentTask(t), time.Now().UnixMilli())); got != workflowPending {
			t.Fatalf("in the window: workflow %q, want pending", got)
		}
		closeWindow() // the end was never marked
		if !w.woke(t) {
			t.Fatalf("closing the window unmarked did not wake the wait: %+v", w.goalIntent(t, "run-1"))
		}
		if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.currentTask(t), time.Now().UnixMilli())); got == workflowPending {
			t.Fatal("still pending after the window closed")
		}
	})
	t.Run("window_closes_marked", func(t *testing.T) {
		w := newWaitWorkflowFixture(t)
		w.workflow(t)
		w.localWait(t)
		closeWindow := w.endWindow()
		w.finishTask(t)
		jobID := taskRunJobID("proj-1", "task-1")
		now := time.Now().UnixMilli()
		w.f.s.markTaskJobEnded(w.f.account, "proj-1", pebblestore.Signal{Seq: 7, At: now, Refs: map[string]string{"session_id": "task-session"}, Attrs: map[string]string{"job_id": jobID}}, "")
		closeWindow()
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if w.woke(t) {
			t.Fatal("woke with the end marked and not evaluated")
		}
		if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 7, now, nil); err != nil {
			t.Fatal(err)
		}
		if !w.woke(t) {
			t.Fatalf("evaluation with nothing matched did not wake: %+v", w.goalIntent(t, "run-1"))
		}
	})
	t.Run("no_window_restart", func(t *testing.T) {
		w := newWaitWorkflowFixture(t)
		w.workflow(t)
		w.localWait(t)
		// The status write happened (and the daemon stopped) without the
		// end ever being marked; startup reconciles every wait.
		w.finishTask(t)
		w.f.s.reconcileProjectTaskWaits("", "")
		if !w.woke(t) {
			t.Fatalf("unmarked end with nothing in flight held the wait: %+v", w.goalIntent(t, "run-1"))
		}
	})
}

// taskRun records run's intent in the task's session at status.
func (w *waitWorkflowFixture) taskRun(t *testing.T, run, status string) {
	t.Helper()
	in := pebblestore.V3SessionMutationInput{SessionID: "task-session", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "task-run-" + run + "-" + status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: run, Status: status}}
	in.AccountScopeID, in.UserID, in.PayloadHash = w.f.account, w.f.user, in.ClientRequestID
	if _, err := w.f.s.sessions.Store().ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
}

// sendBack records the first end's delivery as the runner leaves it after a
// review BLOCK: the delivery finished with its reopen step succeeded for the
// follow-up run-2, and the first end evaluated with that delivery. Before
// that, as reopenTask does, the follow-up's run intent is pending in the
// task's own session. The task record is not touched: production keeps its
// attempt and its stored needs_review.
func (w *waitWorkflowFixture) sendBack(t *testing.T, auto jobautomation.Automation, jobID string, now int64) {
	t.Helper()
	w.taskRun(t, "run-2", pebblestore.V3RunIntentPendingExecutor)
	d, _, err := w.store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, ProjectID: "proj-1", RunID: "task-1", Folder: w.folder, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State, d.FinishedAt = jobautomation.StateSucceeded, now
	d.Steps[0].State = jobautomation.StateSucceeded
	d.Steps[1].State, d.Steps[1].Outcome = jobautomation.StateSucceeded, map[string]any{"followup": "run-2"}
	if err := w.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 1, now, []string{d.ID}); err != nil {
		t.Fatal(err)
	}
}

// Purpose: a workflow send-back (or rebase) keeps a settled wait asleep for
// the whole rework, decided from durable records, not the in-memory run end
// window. Requirement: after the first end was evaluated with a delivery
// whose reopen step sent the follow-up run-2, and with no run end window open
// and the task left needs_review on its original attempt (as reopenTask leaves
// it), the wait does not wake while run-2's intent in the task's session is
// pending or running; when run-2 ended and its end was marked, it still does
// not wake until the runner evaluated that end; and a run-2 that ended
// without its end ever being marked does not hold the wait for good.
// Threat: the Orchestrator woken (and a fresh wait_tasks returning at once)
// with nothing pushed while the coder reworks; a lost signal stalling a goal.
// Authority: Server.taskEndAwaitsWorkflows / taskFollowupRunLive and the
// store's wake branch, with the real project publisher reconciling the
// durable wait over real session, task and job automation stores.
func TestTaskWaitSendBackHoldsTheWaitDuringRework(t *testing.T) {
	setup := func(t *testing.T) (*waitWorkflowFixture, string, int64) {
		t.Helper()
		w := newWaitWorkflowFixture(t)
		auto := w.workflow(t)
		w.localWait(t)
		jobID := taskRunJobID("proj-1", "task-1")
		now := time.Now().UnixMilli()
		if err := w.store.MarkJobEnded(w.f.account, "proj-1", jobID, "", 1, now); err != nil {
			t.Fatal(err)
		}
		w.finishTask(t)
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if w.woke(t) {
			t.Fatal("woke with the first end marked and not evaluated")
		}
		w.sendBack(t, auto, jobID, now)
		for _, status := range []string{"", pebblestore.V3RunIntentRunning} {
			if status != "" {
				w.taskRun(t, "run-2", status)
			}
			w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
			w.f.s.reconcileProjectTaskWaits("", "")
			if w.woke(t) {
				t.Fatalf("woke during the rework (run-2 %s)", firstNonEmptyString(status, "pending"))
			}
			if got := workflowState(w.f.s.taskWaitProgressFor(w.f.account, w.currentTask(t), time.Now().UnixMilli())); got != workflowPending {
				t.Fatalf("rework progress: workflow %q, want pending", got)
			}
		}
		if task := w.currentTask(t); task.Status != "needs_review" || task.ActiveAttemptID != "initial" {
			t.Fatalf("fixture changed the task as production does not: %+v", task)
		}
		return w, jobID, now
	}
	t.Run("follow_up_end_evaluated", func(t *testing.T) {
		w, jobID, now := setup(t)
		w.taskRun(t, "run-2", pebblestore.V3RunIntentCompleted)
		if err := w.store.MarkJobEnded(w.f.account, "proj-1", jobID, "run-2", 2, now); err != nil {
			t.Fatal(err)
		}
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if w.woke(t) {
			t.Fatal("woke with the follow-up end marked and not evaluated")
		}
		if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "run-2", 2, now, nil); err != nil {
			t.Fatal(err)
		}
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if !w.woke(t) {
			t.Fatalf("follow-up end evaluated did not wake: %+v", w.goalIntent(t, "run-1"))
		}
	})
	t.Run("follow_up_end_never_marked", func(t *testing.T) {
		w, _, _ := setup(t)
		w.taskRun(t, "run-2", pebblestore.V3RunIntentCompleted)
		w.f.s.reconcileProjectTaskWaits("", "")
		if !w.woke(t) {
			t.Fatalf("an unmarked follow-up end held the wait: %+v", w.goalIntent(t, "run-1"))
		}
	})
}

// Purpose: a daemon restart (every auto-update) does not wake a local
// until=settled wait before the wait can read task progress. Requirement:
// in the daemon's order (the store is told the progress hook is awaited right
// after it opens, the startup reconciliation runs from newSessionV3Executor,
// and SetJobAutomations installs the hook later), a wait on a needs_review
// task whose job end is marked is not woken by the task write or the startup
// reconciliation before the hook exists. Installing the hook reconciles the
// waits itself: with the workflow not evaluated yet the wait still sleeps and
// wakes when the runner's evaluation (nothing matched) lands; with the end
// already evaluated before the restart, installing the hook alone wakes it.
// Threat: every persisted settled wait woken at startup with nothing landed
// (the hook-less read treats "no progress" as "no workflow"), or left
// undecided after the hook arrives. Authority: Store.AwaitProjectTaskProgress
// / SetProjectTaskProgress, the store's wake branch and
// Server.installTaskProgress over real session, task and job automation
// stores; the narrowest layer that owns the hook installation and the wake.
func TestTaskWaitStartupBeforeProgressHookDoesNotWake(t *testing.T) {
	setup := func(t *testing.T, evaluated bool) (*waitWorkflowFixture, string, int64) {
		t.Helper()
		w := newWaitWorkflowFixtureBeforeAutomations(t)
		w.f.s.sessions.Store().Underlying().AwaitProjectTaskProgress() // as daemon.go does on open
		w.workflow(t)
		w.localWait(t)
		jobID := taskRunJobID("proj-1", "task-1")
		now := time.Now().UnixMilli()
		if err := w.store.MarkJobEnded(w.f.account, "proj-1", jobID, "", 1, now); err != nil {
			t.Fatal(err)
		}
		if evaluated {
			if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 1, now, nil); err != nil {
				t.Fatal(err)
			}
		}
		w.finishTask(t)
		w.f.s.reconcileProjectTaskWaits("", "") // the executor's startup reconciliation
		if w.woke(t) {
			t.Fatal("woke before the progress hook was installed")
		}
		return w, jobID, now
	}
	t.Run("workflow_not_evaluated", func(t *testing.T) {
		w, jobID, now := setup(t, false)
		w.f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: w.store}))
		if w.woke(t) {
			t.Fatal("woke on installing the hook while the workflow is pending")
		}
		if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 1, now, nil); err != nil {
			t.Fatal(err)
		}
		w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
		if !w.woke(t) {
			t.Fatalf("evaluation did not wake: %+v", w.goalIntent(t, "run-1"))
		}
	})
	t.Run("evaluated_before_restart", func(t *testing.T) {
		w, _, _ := setup(t, true)
		w.f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: w.store}))
		if !w.woke(t) {
			t.Fatalf("installing the hook did not decide the wait: %+v", w.goalIntent(t, "run-1"))
		}
	})
}
