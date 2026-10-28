package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
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
	f := newFleetTestSwarm(t, "local")
	t.Cleanup(f.cleanup)
	if f.s.OutboxAccount() != f.account {
		t.Fatalf("fixture account %q is not the machine owner's %q", f.account, f.s.OutboxAccount())
	}
	store := jobautomation.NewStore(f.s.sessions.Store().Underlying())
	f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	fleetActivityTestSession(t, f, "task-session")
	return &waitWorkflowFixture{f: f, store: store, folder: t.TempDir()}
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
// Requirement: with a workflow that could apply and no evaluation, pending;
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

func pushed(t *testing.T, store *jobautomation.Store, d jobautomation.Delivery) {
	t.Helper()
	d.State, d.FinishedAt = jobautomation.StateSucceeded, time.Now().UnixMilli()
	d.Steps[0].State, d.Steps[1].State = jobautomation.StateSucceeded, jobautomation.StateSucceeded
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
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
// settled, read from the progress the machine reports through get_task.
// Requirement: the observation carries the workflow and integration progress;
// needs_review with the machine's workflow running does not wake the home;
// the workflow ending (pushed) wakes it once with workflow_state in the row;
// the registration result reports until=settled. Threat: the home's
// Orchestrator woken when only the remote coder finished. Boundary: tool
// executeFleetWaitTasks, Server.followRemoteTaskWait (observe) and the remote
// task detail route (taskWaitProgressFor) over two in-process daemons.
func TestRemoteTaskWaitSettledWakesAtWorkflowEnd(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	store, d := h.remoteWorkflow(t)
	if result := h.waitUntil(t, "", "t1"); result["until"] != pebblestore.ProjectTaskWaitUntilSettled {
		t.Fatalf("registration result: %v", result)
	}
	h.home.s.watchRemoteTaskWait(remoteWaitGoal, "run-1")
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Ready.", "")
	h.noWake(t, 400*time.Millisecond, "needs_review while the machine's workflow runs")
	pushed(t, store, d)
	h.touch(t, "rs-t1", "pushed")
	row := remoteWaitRow(t, h.awaitWake(t), 0)
	if row["status"] != "needs_review" || row["workflow_state"] != workflowDone || row["workflow"] != "review loop" || row["machine"] != "box" {
		t.Fatalf("pushed row: %v", row)
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
	pushed(t, store, d)
	h.touch(t, "rs-t1", "pushed")
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
