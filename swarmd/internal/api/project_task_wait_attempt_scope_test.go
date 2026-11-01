package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// attemptScopeFixture is a coder task whose first attempt ("initial") ended
// and whose workflow delivery for that end failed, in the wait workflow
// fixture's real session, task and job automation stores.
type attemptScopeFixture struct {
	w      *waitWorkflowFixture
	auto   jobautomation.Automation
	jobID  string
	failed jobautomation.Delivery
}

func newAttemptScopeFixture(t *testing.T) *attemptScopeFixture {
	t.Helper()
	w := newWaitWorkflowFixture(t)
	auto := w.workflow(t)
	db := w.f.s.sessions.Store()
	if err := db.PutProject(w.f.account, &pebblestore.ProjectRecord{ID: "proj-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	task := w.task(time.Now().UnixMilli())
	task.SourceWorkspace = pebblestore.ProjectTaskSource{}
	if err := db.PutProjectTask(w.f.account, &task); err != nil {
		t.Fatal(err)
	}
	jobID := taskRunJobID("proj-1", "task-1")
	d, _, err := w.store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, ProjectID: "proj-1", RunID: "task-1", Folder: w.folder, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State, d.FinishedAt = jobautomation.StateFailed, d.CreatedAt+500
	d.Steps[0].State, d.Steps[0].StartedAt, d.Steps[0].FinishedAt, d.Steps[0].Reason = jobautomation.StateFailed, d.CreatedAt+100, d.CreatedAt+400, "console: exited with code 1: OLD-DELIVERY-FAILURE"
	d.Steps[1].State = jobautomation.StateSkipped
	if err := w.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", jobID, "", 1, d.CreatedAt, []string{d.ID}); err != nil {
		t.Fatal(err)
	}
	return &attemptScopeFixture{w: w, auto: auto, jobID: jobID, failed: d}
}

// sendBack is message_task on the ended task: a new attempt a2, started
// after the failed delivery was made, whose run is in progress. (The store
// stamps deliveries with the real clock; a later delivery of the job is
// stamped after this one, so after a2 too.)
func (a *attemptScopeFixture) sendBack(t *testing.T) {
	t.Helper()
	if _, err := a.w.f.s.sessions.Store().UpdateProjectTask(a.w.f.account, "proj-1", "task-1", func(task *pebblestore.ProjectTaskRecord) error {
		task.Attempts = append(task.Attempts, pebblestore.ProjectTaskAttempt{ID: "a2", SessionID: "task-session", Role: "coder", RunID: "desktop-v3-run:task-followup-a2", CreatedAt: a.failed.CreatedAt + 1, Status: "in_progress"})
		task.ActiveAttemptID, task.Status = "a2", "in_progress"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// watch registers a wait_tasks watch (milestones on) on task-1 from the
// project Orchestrator's run-1, then ends run-1 so a wake starts a turn.
func (a *attemptScopeFixture) watch(t *testing.T) {
	t.Helper()
	w := a.w
	db := w.f.s.sessions.Store()
	goal := map[string]any{"swarm_v3_project_id": "proj-1", "project_id": "proj-1", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-goal", Session: &pebblestore.SessionSnapshot{ID: "goal", Mode: "auto", Metadata: goal}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.started", ClientRequestID: "task-watch:run-1", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "proj-1", TaskIDs: []string{"task-1"}}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "end-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentCompleted}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = w.f.account, w.f.user, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	watches, err := db.ListV3SessionTaskWatches("goal")
	if err != nil || len(watches) != 1 || !watches[0].Milestones {
		t.Fatalf("watch not registered with milestones: %+v %v", watches, err)
	}
	w.watchID = watches[0].WatchID
}

// goalBodies are the JSON bodies of the goal's wake messages with prefix
// ("Delegated project task outcomes:" or "... milestones:").
func (a *attemptScopeFixture) goalBodies(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	messages, err := a.w.f.s.sessions.Store().ListMessages("goal", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, m := range messages {
		if body, ok := strings.CutPrefix(m.Content, prefix+"\n"); ok {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				t.Fatal(err)
			}
			out = append(out, parsed)
		}
	}
	return out
}

const (
	attemptScopeOutcomes   = "Delegated project task outcomes:"
	attemptScopeMilestones = "Delegated project task milestones:"
)

// Purpose: a task sent back with message_task after its workflow delivery
// failed is a new attempt, and the old delivery's result is never announced
// as the new attempt's milestone. Requirement: with the old failed delivery
// still the workflow's newest and the new attempt's run in progress, a
// watch (milestones on) registered on the task neither wakes with a `failed`
// milestone nor settles; task progress lists the old delivery as
// previous_attempt (attempt_id initial) and its workflow state is not
// failed; the Swarm Control wait's milestone is `started`, not `failed`.
// Negative control: the same failed delivery while it still belongs to the
// active attempt does wake the watch with workflow_state failed (the rule
// scopes, it does not hide failures). Threat (observed): the watch woke
// within seconds with milestone failed and the old delivery's
// workflow_steps_result attached to the new attempt. Boundary: Server.
// taskWaitProgressFor (computeTaskProgress / deliveryAttempt) feeding the
// store's real wake (reconcileProjectTaskWaits -> prepareProjectTaskWatchWake)
// over real session, task and job automation stores, the narrowest layer
// owning both the delivery-to-attempt rule and the wake.
func TestTaskWatchSendBackAfterFailedDeliveryIsNotAFailedMilestone(t *testing.T) {
	t.Run("own_attempt_failure_still_wakes", func(t *testing.T) {
		a := newAttemptScopeFixture(t)
		a.watch(t)
		a.w.f.s.reconcileProjectTaskWaits(a.w.f.account, "proj-1")
		outcomes := a.goalBodies(t, attemptScopeOutcomes)
		if len(outcomes) != 1 {
			t.Fatalf("the active attempt's failed delivery did not settle the watch: %v", outcomes)
		}
		row := remoteWaitRow(t, outcomes[0], 0)
		if row["workflow_state"] != workflowFailed || row["delivery_id"] != a.failed.ID || row["delivery_attempt_id"] != "initial" || row["delivery_run"] != float64(1) {
			t.Fatalf("own failed delivery row: %v", row)
		}
	})
	t.Run("sent_back_attempt_is_not_failed", func(t *testing.T) {
		a := newAttemptScopeFixture(t)
		a.sendBack(t)
		a.watch(t)
		a.w.f.s.reconcileProjectTaskWaits(a.w.f.account, "proj-1")
		a.w.f.s.reconcileProjectTaskWaits("", "")
		if got := a.goalBodies(t, attemptScopeMilestones); len(got) != 0 {
			t.Fatalf("the old delivery woke the new attempt's watch with a milestone: %v", got)
		}
		if got := a.goalBodies(t, attemptScopeOutcomes); len(got) != 0 {
			t.Fatalf("the old delivery settled the new attempt's watch: %v", got)
		}
		if watches, err := a.w.f.s.sessions.Store().ListV3SessionTaskWatches("goal"); err != nil || len(watches) != 1 {
			t.Fatalf("watch ended: %+v %v", watches, err)
		}
		task := a.w.currentTask(t)
		p := a.w.f.s.taskWaitProgressFor(a.w.f.account, task, time.Now().UnixMilli())
		if p.Workflow == nil || p.Workflow.State == workflowFailed || len(p.Workflow.Deliveries) != 1 {
			t.Fatalf("progress workflow: %+v", p.Workflow)
		}
		if d := p.Workflow.Deliveries[0]; d.ID != a.failed.ID || !d.PreviousAttempt || d.AttemptID != "initial" || d.Run != 1 {
			t.Fatalf("old delivery not marked previous_attempt: %+v", d)
		}
		wake := pebblestore.ProjectTaskWaitProgressFrom(p).ForAttempt("a2")
		if wake != nil && (wake.WorkflowState == workflowFailed || wake.DeliveryID != "" || len(wake.StepsResult) != 0) {
			t.Fatalf("wake progress carries the old delivery: %+v", wake)
		}
		if got := pebblestore.ProjectTaskMilestone(task.Status, wake); got != pebblestore.ProjectTaskMilestoneStarted {
			t.Fatalf("Swarm Control milestone: %q, want started", got)
		}
		if raw, _ := json.Marshal(tool.ProjectTaskWorkflow(p)); strings.Contains(string(raw), a.failed.ID) || strings.Contains(string(raw), `"failed"`) {
			t.Fatalf("get_task shows the old delivery as the workflow: %s", raw)
		}
	})
}

// Purpose: a settled wake row and get_task agree at the same instant
// because both are built from the same store state through the same
// progress (Server.taskWaitProgressFor). Requirement: after a send-back,
// the new attempt finishes and its own delivery succeeds; the watch settles
// with a row whose workflow_state, delivery_id, delivery_attempt_id (a2),
// delivery_run, workflow_steps_result and task_state equal get_task's
// progress.workflow (state, delivery_id, delivery_attempt_id, delivery_run,
// steps_result) and progress task_state read from the same records; none of
// it is the old failed delivery's. Threat (observed): a settled row showing
// "needs_you, workflow failed run console" with the old delivery's steps
// while get_task showed the new delivery. Boundary: the store's real wake
// and the get_task route's progress over real stores; tool.
// ProjectTaskWorkflow is MCP get_task's view of that progress.
func TestTaskWaitSettledRowEqualsGetTask(t *testing.T) {
	a := newAttemptScopeFixture(t)
	a.sendBack(t)
	a.watch(t)
	w := a.w
	db := w.f.s.sessions.Store()
	// a2 works: nothing about the old delivery reaches the watch.
	w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
	if got := append(a.goalBodies(t, attemptScopeOutcomes), a.goalBodies(t, attemptScopeMilestones)...); len(got) != 0 {
		t.Fatalf("woke while a2 works: %v", got)
	}
	// a2's run ends: its job end is marked for the workflow runner, then
	// the task's status is written (the order the run end settles in).
	followup := "desktop-v3-run:task-followup-a2"
	if err := w.store.MarkJobEnded(w.f.account, "proj-1", a.jobID, followup, 2, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateProjectTask(w.f.account, "proj-1", "task-1", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.Attempts[1].Status = "needs_review", "needs_review"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
	if got := a.goalBodies(t, attemptScopeOutcomes); len(got) != 0 {
		t.Fatalf("settled before a2's end was evaluated (on the old delivery?): %v", got)
	}
	if got := a.goalBodies(t, attemptScopeMilestones); len(got) != 0 {
		t.Fatalf("milestone from the old delivery: %v", got)
	}
	d2, created, err := w.store.CreateDelivery(a.auto, jobautomation.Result{JobID: a.jobID, Followup: followup, Kind: jobautomation.KindTask, ProjectID: "proj-1", RunID: "task-1", Folder: w.folder, Status: jobautomation.StatusSucceeded})
	if err != nil || !created || d2.ID == a.failed.ID {
		t.Fatalf("new delivery: %+v %v %v", d2, created, err)
	}
	d2.State, d2.FinishedAt = jobautomation.StateSucceeded, d2.CreatedAt+300
	d2.Steps[0].State, d2.Steps[0].StartedAt, d2.Steps[0].FinishedAt, d2.Steps[0].Outcome = jobautomation.StateSucceeded, d2.CreatedAt+10, d2.CreatedAt+250, map[string]any{"verdict": "ok", "findings": "Looks right."}
	d2.Steps[1].State = jobautomation.StateSkipped
	if err := w.store.SaveDelivery(d2); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvaluation(w.f.account, "proj-1", a.jobID, followup, 2, d2.CreatedAt, []string{d2.ID}); err != nil {
		t.Fatal(err)
	}
	w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
	outcomes := a.goalBodies(t, attemptScopeOutcomes)
	if len(outcomes) != 1 || outcomes[0]["all_ready"] != true {
		t.Fatalf("settled outcome: %v", outcomes)
	}
	row := remoteWaitRow(t, outcomes[0], 0)

	// get_task at the same instant, from the same records.
	progress := w.f.s.taskWaitProgressFor(w.f.account, w.currentTask(t), time.Now().UnixMilli())
	raw, err := json.Marshal(tool.ProjectTaskWorkflow(progress))
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		State             string          `json:"state"`
		DeliveryID        string          `json:"delivery_id"`
		DeliveryAttemptID string          `json:"delivery_attempt_id"`
		DeliveryRun       int             `json:"delivery_run"`
		StepsResult       json.RawMessage `json:"steps_result"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.DeliveryID != d2.ID || view.DeliveryAttemptID != "a2" || view.DeliveryRun != 1 || view.State != workflowDone {
		t.Fatalf("get_task workflow: %s", raw)
	}
	rowSteps, _ := json.Marshal(row["workflow_steps_result"])
	var a1, b1 any
	_ = json.Unmarshal(rowSteps, &a1)
	_ = json.Unmarshal(view.StepsResult, &b1)
	if row["attempt_id"] != "a2" || row["workflow_state"] != view.State || row["delivery_id"] != view.DeliveryID || row["delivery_attempt_id"] != view.DeliveryAttemptID || row["delivery_run"] != float64(view.DeliveryRun) || !reflect.DeepEqual(a1, b1) || a1 == nil {
		t.Fatalf("settled row differs from get_task:\nrow %v\nget_task %s", row, raw)
	}
	if row["task_state"] != progress.TaskState || progress.TaskState == "" {
		t.Fatalf("task_state: row %v get_task %q", row["task_state"], progress.TaskState)
	}
	if strings.Contains(string(rowSteps), "OLD-DELIVERY-FAILURE") || strings.Contains(string(raw), "OLD-DELIVERY-FAILURE") {
		t.Fatalf("the old delivery's steps leaked: row %s get_task %s", rowSteps, raw)
	}
}

// Purpose: a re-run of a delivery shows the current run's step results,
// never the earlier run's as current. Requirement: Store.Rerun counts the
// run (Reruns) and records when it started (RerunAt); task progress then
// names the delivery's run (2), drops the failed step it reset (no result
// until it runs again), marks the step it kept from the earlier run
// previous_run, and the wake progress carries delivery_run 2 with the same
// steps; the earlier failure is not the workflow's state or a `failed`
// milestone. When the reset step finishes in this run it is shown without
// previous_run. Threat (observed): wake rows carrying an earlier run's step
// results (identical durations) as the current run's. Boundary:
// jobautomation.Store.Rerun over a real store, deliveryStepsResult /
// computeTaskProgress and pebblestore.ProjectTaskWaitProgressFrom.
func TestTaskWakeRerunShowsOnlyCurrentRunSteps(t *testing.T) {
	a := newAttemptScopeFixture(t)
	w := a.w
	d := a.failed
	// The first run's steps finished before the re-run (the store's real
	// clock stamps it).
	ran := time.Now().UnixMilli() - 60_000
	d.FinishedAt = ran + 500
	d.Steps[0].State, d.Steps[0].StartedAt, d.Steps[0].FinishedAt, d.Steps[0].Reason, d.Steps[0].Outcome = jobautomation.StateSucceeded, ran+100, ran+400, "", map[string]any{"verdict": "block", "findings": "BLOCKING: fix it"}
	d.Steps[1].State, d.Steps[1].StartedAt, d.Steps[1].FinishedAt, d.Steps[1].Reason = jobautomation.StateFailed, ran+400, ran+450, "send-back failed: EARLIER-RUN"
	if err := w.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	before := deliveryStepsResult(d)
	if len(before) != 2 || before[1].Status != jobautomation.StateFailed || before[0].PreviousRun || before[1].PreviousRun {
		t.Fatalf("first run steps: %+v", before)
	}
	out, err := w.store.Rerun(w.f.account, d.ID)
	if err != nil || !out.Rerun {
		t.Fatalf("rerun: %+v %v", out, err)
	}
	stored, err := w.store.GetDelivery(w.f.account, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Reruns != 1 || stored.RerunAt <= d.FinishedAt || stored.RerunAt < stored.CreatedAt {
		t.Fatalf("rerun not recorded: reruns %d rerun_at %d", stored.Reruns, stored.RerunAt)
	}
	steps := deliveryStepsResult(stored)
	if len(steps) != 1 || steps[0].ID != "review" || !steps[0].PreviousRun {
		t.Fatalf("current run steps: %+v", steps)
	}
	task := w.currentTask(t)
	p := computeTaskProgress(task, []jobautomation.Delivery{stored}, nil, time.Now().UnixMilli())
	p.Workflow.Deliveries[0].StepsResult = steps
	if dv := p.Workflow.Deliveries[0]; dv.Run != 2 || dv.AttemptID != "initial" || dv.PreviousAttempt || p.Workflow.State == workflowFailed {
		t.Fatalf("rerun delivery view: %+v state %s", dv, p.Workflow.State)
	}
	wake := pebblestore.ProjectTaskWaitProgressFrom(p)
	if wake == nil || wake.DeliveryRun != 2 || wake.DeliveryAttemptID != "initial" || !reflect.DeepEqual(wake.StepsResult, steps) || wake.WorkflowState == workflowFailed {
		t.Fatalf("wake progress: %+v", wake)
	}
	if got := pebblestore.ProjectTaskMilestone(task.Status, wake); got == pebblestore.ProjectTaskMilestoneFailed {
		t.Fatal("the earlier run's failure is the rerun's milestone")
	}
	if raw, _ := json.Marshal(wake); strings.Contains(string(raw), "EARLIER-RUN") {
		t.Fatalf("the earlier run's failed step is shown: %s", raw)
	}
	// The reset step runs again in this run: shown as this run's.
	stored.Steps[1].State, stored.Steps[1].StartedAt, stored.Steps[1].FinishedAt = jobautomation.StateSucceeded, stored.RerunAt+10, stored.RerunAt+20
	if again := deliveryStepsResult(stored); len(again) != 2 || !again[0].PreviousRun || again[1].PreviousRun || again[1].Status != jobautomation.StateSucceeded {
		t.Fatalf("step finished in the current run: %+v", again)
	}
}
