package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// waitProgressStub is the store's task progress hook as the daemon installs
// it (Server.taskWaitProgressFor), reduced to a fixed answer per task: the
// shape of task progress (workflow.state, workflow.deliveries[], integration)
// is the contract the wait reads.
type waitProgressStub struct {
	mu sync.Mutex
	m  map[string]any
}

func (p *waitProgressStub) set(taskID string, progress any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[taskID] = progress
}

func (p *waitProgressStub) install(s *Store) {
	s.SetProjectTaskProgress(func(_ string, task ProjectTaskRecord) any {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.m[task.ID]
	})
}

// workflowProgress is task progress with one delivery of a review loop
// (review, send-back, push, rebase) at step/round, and an integration state.
func workflowProgress(state string, step int, stepType string, round int, integration string) map[string]any {
	delivery := map[string]any{"id": "jdel", "workflow": "review loop", "state": state, "step": step, "steps": 4, "step_type": stepType, "round": round}
	if state == "pending" {
		delivery = nil
	}
	deliveries := []any{}
	if delivery != nil {
		deliveries = append(deliveries, delivery)
	}
	out := map[string]any{"state": "workflow", "summary": "review loop", "workflow": map[string]any{"state": state, "deliveries": deliveries}}
	if integration != "" {
		out["integration"] = map[string]any{"state": integration, "branch": "agent/task", "target": "dev"}
	}
	return out
}

// newSettledWaitFixture is a project Orchestrator ("parent", running run-1)
// with one delegated task per id, each in its own owned child session, and
// the store's progress hook installed.
func newSettledWaitFixture(t *testing.T, path string, ids ...string) (*taskWaitFixture, *waitProgressStub) {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &taskWaitFixture{t: t, db: NewSessionStore(store)}
	// Closes the store the fixture holds at the end (a restart swaps it).
	t.Cleanup(func() { _ = f.db.store.Close() })
	if err := f.db.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationCreateSession, ClientRequestID: "create-parent", Session: &SessionSnapshot{ID: "parent", Mode: "auto", Metadata: map[string]any{"swarm_v3_project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator", "project_id": "project"}}})
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "pending-parent", RunIntent: &V3SessionRunIntent{RunID: "run-1", Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-parent", RunIntent: &V3SessionRunIntent{RunID: "run-1", Status: V3RunIntentRunning}})
	for _, id := range ids {
		child := "child-" + id
		f.apply(V3SessionMutationInput{SessionID: child, Kind: V3SessionMutationCreateSession, ClientRequestID: "create-" + child, Session: &SessionSnapshot{ID: child, Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": id}}})
		task := &ProjectTaskRecord{ID: id, ProjectID: "project", AccountID: "account", Title: "Task " + id, Agent: "coder", Status: "in_progress", SessionID: child, ActiveAttemptID: "a1", Attempts: []ProjectTaskAttempt{{ID: "a1", SessionID: child, Role: "coder", Status: "in_progress"}}}
		if err := f.db.PutProjectTask("account", task); err != nil {
			t.Fatal(err)
		}
	}
	progress := &waitProgressStub{m: map[string]any{}}
	progress.install(store)
	return f, progress
}

// register watches ids from run-1 and ends run-1, so the conversation is
// idle and a ready watch fires at once.
func (f *taskWaitFixture) register(until string, ids ...string) {
	f.t.Helper()
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, EventType: "session.task_watch.started", ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: ids, Until: until}})
	f.endRun("parent", "run-1")
}

func (f *taskWaitFixture) setTask(id string, mutate func(*ProjectTaskRecord)) {
	f.t.Helper()
	if _, err := f.db.UpdateProjectTask("account", "project", id, func(t *ProjectTaskRecord) error { mutate(t); return nil }); err != nil {
		f.t.Fatal(err)
	}
}

// reconcile runs the publication/startup reconciliation and returns the
// outcome if the wait woke, nil otherwise.
func (f *taskWaitFixture) reconcile() map[string]any {
	f.t.Helper()
	var published []V3SessionMutationResult
	if err := f.db.ReconcileProjectTaskWaits("", "", func(r V3SessionMutationResult) { published = append(published, r) }); err != nil {
		f.t.Fatal(err)
	}
	switch len(published) {
	case 0:
		if len(f.watches("parent")) == 0 {
			f.t.Fatal("watch ended without firing")
		}
		return nil
	case 1:
		return taskWaitOutcome(f.t, published[0].Message)
	}
	f.t.Fatalf("woke %d times", len(published))
	return nil
}

func (f *taskWaitFixture) mustSleep(step string) {
	f.t.Helper()
	if out := f.reconcile(); out != nil {
		f.t.Fatalf("%s: woke the Orchestrator early: %v", step, out)
	}
}

func outcomeRow(t *testing.T, out map[string]any, i int) map[string]any {
	t.Helper()
	rows, _ := out["tasks"].([]any)
	if len(rows) <= i {
		t.Fatalf("outcome rows: %v", out)
	}
	return rows[i].(map[string]any)
}

// Purpose: on a project with a workflow (review -> send-back -> push ->
// rebase) a local wait wakes the Orchestrator only when the work settled.
// Requirement: under the default until=settled, needs_review with a running
// or not-yet-evaluated workflow is not ready (any step, including a send-back
// and a rebase round); a review BLOCK that reopens the coder keeps the wait on
// the same task and follows its new attempt (not superseded); it wakes once,
// at pushed, with the workflow state/step/round and integration state in the
// row. Threat: the Orchestrator woken when only the coder finished, with
// nothing landed; a send-back ending the wait as superseded or pinning a stale
// attempt. Authority: SessionStore.prepareProjectTaskWait (wake branch) and
// projectTaskWaitReadiness via ReconcileProjectTaskWaits over a temporary real
// store; the progress hook stands in for Server.taskWaitProgressFor (tested in
// the api package), the narrowest layer that proves durable wake/no-wake.
func TestProjectTaskWaitSettledFollowsWorkflowToPushed(t *testing.T) {
	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	watch := f.onlyWatch("parent")
	if watch.Wait.Until != ProjectTaskWaitUntilSettled {
		t.Fatalf("default until not recorded as settled: %+v", watch.Wait)
	}
	f.mustSleep("coder running")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	progress.set("task", workflowProgress("pending", 0, "", 0, ""))
	f.mustSleep("coder finished, workflow not evaluated")
	progress.set("task", workflowProgress("running", 1, "agent", 0, "not_integrated"))
	f.mustSleep("review running")
	progress.set("task", workflowProgress("running", 2, "reopen", 0, "not_integrated"))
	f.mustSleep("review BLOCK, send-back running")
	// The send-back reopened the coder: a new attempt in the same session.
	f.setTask("task", func(t *ProjectTaskRecord) {
		t.Status = "in_progress"
		t.Attempts = append(t.Attempts, ProjectTaskAttempt{ID: "a2", SessionID: "child-task", Role: "coder", Status: "in_progress"})
		t.ActiveAttemptID = "a2"
	})
	progress.set("task", workflowProgress("done", 2, "reopen", 0, "not_integrated"))
	f.mustSleep("coder reopened")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review"; t.Attempts[1].Summary = "Fixed the findings." })
	progress.set("task", workflowProgress("pending", 0, "", 1, "not_integrated"))
	f.mustSleep("reopened coder finished, end not evaluated")
	progress.set("task", workflowProgress("running", 1, "agent", 1, "not_integrated"))
	f.mustSleep("second review running")
	progress.set("task", workflowProgress("running", 3, "run", 1, "not_integrated"))
	f.mustSleep("review OK, push running")
	progress.set("task", workflowProgress("done", 3, "run", 1, "integrated"))
	out := f.reconcile()
	if out == nil {
		t.Fatal("pushed did not wake the wait")
	}
	row := outcomeRow(t, out, 0)
	if row["status"] != "needs_review" || row["attempt_id"] != "a2" || row["session_id"] != "child-task" || row["pinned_attempt_id"] != "a1" || row["summary"] != "Fixed the findings." {
		t.Fatalf("followed attempt row: %v", row)
	}
	if row["workflow_state"] != "done" || row["workflow"] != "review loop" || row["workflow_step"] != float64(3) || row["workflow_steps"] != float64(4) || row["workflow_step_type"] != "run" || row["workflow_round"] != float64(1) || row["integration_state"] != "integrated" {
		t.Fatalf("workflow fields: %v", row)
	}
	if out["all_ready"] != true || !strings.Contains(out["guidance"].(string), "until=settled") {
		t.Fatalf("outcome: %v", out)
	}
	f.assertContinuation("parent", watch.WatchID)
}

// Purpose: settled readiness covers the other workflow ends and the no
// workflow case. Requirement: no workflow attached -> needs_review wakes (as
// before); push failed -> a rebase round keeps waiting until that round ends;
// out of rounds (workflow blocked) and a failed workflow wake at once as
// actionable, even while another task still runs; a task already needs_review
// whose workflow runs when the wait starts really waits. Threat: a dead end
// (a wait that returns at once while the workflow runs) or a stuck one (a
// blocked workflow never reported). Authority: projectTaskWaitReadiness via
// ReconcileProjectTaskWaits over a temporary real store.
func TestProjectTaskWaitSettledWorkflowEnds(t *testing.T) {
	t.Run("no_workflow", func(t *testing.T) {
		f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		f.register("", "task")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		progress.set("task", map[string]any{"state": "not_integrated", "integration": map[string]any{"state": "not_integrated"}})
		out := f.reconcile()
		if out == nil || outcomeRow(t, out, 0)["integration_state"] != "not_integrated" || out["all_ready"] != true {
			t.Fatalf("no workflow: %v", out)
		}
		if _, ok := outcomeRow(t, out, 0)["workflow_state"]; ok {
			t.Fatalf("no workflow reported a workflow: %v", out)
		}
	})
	t.Run("already_needs_review_with_running_workflow", func(t *testing.T) {
		f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		progress.set("task", workflowProgress("running", 1, "agent", 0, ""))
		f.register("", "task")
		f.mustSleep("registered while the workflow runs")
		progress.set("task", workflowProgress("done", 4, "reopen", 0, "integrated"))
		if out := f.reconcile(); out == nil {
			t.Fatal("workflow end did not wake")
		}
	})
	t.Run("push_failed_rebase_round", func(t *testing.T) {
		f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		f.register("", "task")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		progress.set("task", workflowProgress("running", 3, "run", 0, "not_integrated"))
		f.mustSleep("push running")
		progress.set("task", workflowProgress("running", 4, "reopen", 0, "not_integrated"))
		f.mustSleep("push failed, rebase sending")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "in_progress" })
		f.mustSleep("coder rebasing")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		progress.set("task", workflowProgress("running", 3, "run", 1, "not_integrated"))
		f.mustSleep("second push running")
		progress.set("task", workflowProgress("done", 3, "run", 1, "integrated"))
		out := f.reconcile()
		if out == nil || outcomeRow(t, out, 0)["workflow_round"] != float64(1) || outcomeRow(t, out, 0)["attempt_id"] != "a1" {
			t.Fatalf("rebase round end: %v", out)
		}
		if _, ok := outcomeRow(t, out, 0)["pinned_attempt_id"]; ok {
			t.Fatalf("same attempt reported as followed: %v", out)
		}
	})
	for _, state := range []string{"blocked", "failed"} {
		t.Run("workflow_"+state, func(t *testing.T) {
			f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task", "other")
			f.register("", "task", "other")
			f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
			progress.set("task", workflowProgress(state, 2, "reopen", 2, "not_integrated"))
			out := f.reconcile()
			if out == nil {
				t.Fatalf("%s workflow did not wake", state)
			}
			if out["all_ready"] != false || outcomeRow(t, out, 0)["workflow_state"] != state || outcomeRow(t, out, 1)["status"] != "in_progress" {
				t.Fatalf("%s outcome: %v", state, out)
			}
		})
	}
}

// Purpose: until=finished keeps the status-only rule. Requirement: needs_review
// wakes even while a workflow runs; a replaced attempt ends the row as
// superseded (no following); an unknown until is refused without changing
// the run. Authority: NormalizeProjectTaskWaitUntil and the wake branch.
func TestProjectTaskWaitUntilFinishedKeepsStatusRule(t *testing.T) {
	t.Run("workflow_running", func(t *testing.T) {
		f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		f.register("finished", "task")
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		progress.set("task", workflowProgress("running", 1, "agent", 0, ""))
		out := f.reconcile()
		if out == nil || outcomeRow(t, out, 0)["workflow_state"] != "running" || !strings.Contains(out["guidance"].(string), "until=finished") {
			t.Fatalf("finished wait: %v", out)
		}
	})
	t.Run("reopen_superseded", func(t *testing.T) {
		f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		f.register("finished", "task")
		f.setTask("task", func(t *ProjectTaskRecord) {
			t.Attempts = append(t.Attempts, ProjectTaskAttempt{ID: "a2", SessionID: "child-task", Role: "coder"})
			t.ActiveAttemptID = "a2"
		})
		out := f.reconcile()
		if out == nil || outcomeRow(t, out, 0)["status"] != "superseded" || outcomeRow(t, out, 0)["attempt_id"] != "a1" {
			t.Fatalf("finished wait followed a reopen: %v", out)
		}
	})
	t.Run("unknown_until", func(t *testing.T) {
		f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
		if _, err := f.try(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, EventType: "session.task_watch.started", ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}, Until: "merged"}}); err == nil {
			t.Fatal("unknown until accepted")
		}
		if got := f.intent("parent", "run-1"); got.Status != V3RunIntentRunning || got.TaskWait != nil || len(f.watches("parent")) != 0 {
			t.Fatalf("refusal changed the run or recorded a watch: %+v", got)
		}
	})
}

// Purpose: a daemon restart while waiting keeps the wait's rule, in the
// daemon's real startup order. Requirement: the persisted until survives
// reopening the store; the daemon declares the progress hook awaited
// (AwaitProjectTaskProgress, right after opening the store), runs the startup
// reconciliation (ReconcileProjectTaskWaits("", "")) before the hook exists,
// and installs the hook later (SetJobAutomations). A settled wait on a
// needs_review task is not woken by that hook-less reconciliation, nor after
// the hook reports the workflow running, and wakes when the workflow ends; a
// finished wait still wakes on needs_review without the hook. Threat: every
// auto-update restart waking settled waits with nothing landed, or losing
// until. Authority: the persisted V3SessionRunIntent.TaskWait, the store's
// progress-awaited gate and the startup reconciliation over a reopened real
// store.
func TestProjectTaskWaitRuleSurvivesRestart(t *testing.T) {
	for _, until := range []string{ProjectTaskWaitUntilSettled, ProjectTaskWaitUntilFinished} {
		t.Run(until, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store")
			f, _ := newSettledWaitFixture(t, path, "task")
			f.register(until, "task")
			f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
			if err := f.db.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			f.db = NewSessionStore(reopened)
			reopened.AwaitProjectTaskProgress() // as the daemon does on open
			if got := f.onlyWatch("parent"); got.Wait.Until != until {
				t.Fatalf("persisted watch after restart: %+v", got.Wait)
			}
			if until == ProjectTaskWaitUntilFinished {
				if out := f.reconcile(); out == nil {
					t.Fatal("finished wait did not wake after restart")
				}
				return
			}
			f.mustSleep("startup reconciliation before the progress hook")
			progress := &waitProgressStub{m: map[string]any{"task": workflowProgress("running", 3, "run", 0, "not_integrated")}}
			progress.install(reopened)
			f.mustSleep("restart while the workflow runs")
			progress.set("task", workflowProgress("done", 3, "run", 0, "integrated"))
			if out := f.reconcile(); out == nil || outcomeRow(t, out, 0)["integration_state"] != "integrated" {
				t.Fatalf("workflow end after restart: %v", out)
			}
		})
	}
}

// Purpose: linked-machine waits use the same settled rule on what the machine
// reports. Requirement: an observation whose progress shows a running or
// pending workflow is not ready; a reopened attempt is followed; pushed wakes
// with the workflow/integration fields; a blocked workflow is actionable; an
// unreachable machine still resolves a row whose workflow was running; under
// until=finished the running workflow does not hold the wake. Threat: a remote
// wait waking when only the coder finished, or never resolving on a lost link.
// Authority: prepareProjectTaskWatchWake (projectTaskWatchRemoteOutcome) over a
// temporary real store.
func TestProjectTaskWaitRemoteSettled(t *testing.T) {
	running := func(attempt, session string, p *V3ProjectTaskWaitProgress) []V3ProjectTaskRemoteObservation {
		return []V3ProjectTaskRemoteObservation{{TaskID: "t1", Title: "One", Status: "needs_review", AttemptID: attempt, SessionID: session, AttemptSummary: "done", Progress: p}}
	}
	registerRemote := func(f *taskWaitFixture, until string) {
		in := remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"})
		in.TaskWait.Until = until
		f.apply(in)
		f.endRun("home", "run-1")
	}
	t.Run("settled", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		registerRemote(f, "")
		for name, p := range map[string]*V3ProjectTaskWaitProgress{
			"pending":   {WorkflowState: "pending"},
			"review":    {WorkflowState: "running", Workflow: "review loop", WorkflowStep: 1, WorkflowSteps: 4, WorkflowStepType: "agent"},
			"send-back": {WorkflowState: "running", WorkflowStep: 2, WorkflowStepType: "reopen"},
		} {
			if _, err := f.try(f.remoteWake(running("a1", "rs1", p), nil)); !errors.Is(err, ErrProjectTaskWaitNotReady) {
				t.Fatalf("%s woke: %v", name, err)
			}
		}
		// The send-back reopened the task on the machine: a new attempt.
		reopened := running("a2", "rs2", &V3ProjectTaskWaitProgress{WorkflowState: "running", WorkflowStep: 3, WorkflowStepType: "run", WorkflowRound: 1})
		if _, err := f.try(f.remoteWake(reopened, nil)); !errors.Is(err, ErrProjectTaskWaitNotReady) {
			t.Fatalf("reopened attempt ended the wait: %v", err)
		}
		pushed := running("a2", "rs2", &V3ProjectTaskWaitProgress{WorkflowState: "done", Workflow: "review loop", WorkflowStep: 3, WorkflowSteps: 4, WorkflowStepType: "run", WorkflowRound: 1, IntegrationState: "integrated"})
		out := taskWaitOutcome(t, f.apply(f.remoteWake(pushed, nil)).Message)
		row := outcomeRow(t, out, 0)
		if row["status"] != "needs_review" || row["attempt_id"] != "a2" || row["session_id"] != "rs2" || row["pinned_attempt_id"] != "a1" || row["workflow_state"] != "done" || row["workflow_round"] != float64(1) || row["integration_state"] != "integrated" || row["machine"] != "box" {
			t.Fatalf("pushed row: %v", row)
		}
		if !strings.Contains(out["guidance"].(string), "until=settled") || out["untrusted_content"] != true {
			t.Fatalf("remote guidance: %v", out)
		}
	})
	t.Run("out_of_rounds", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		registerRemote(f, "")
		out := taskWaitOutcome(t, f.apply(f.remoteWake(running("a1", "rs1", &V3ProjectTaskWaitProgress{WorkflowState: "blocked", WorkflowStepType: "reopen", WorkflowRound: 2}), nil)).Message)
		if outcomeRow(t, out, 0)["workflow_state"] != "blocked" {
			t.Fatalf("blocked: %v", out)
		}
	})
	t.Run("unreachable_while_running", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		registerRemote(f, "")
		out := taskWaitOutcome(t, f.apply(f.remoteWake(running("a1", "rs1", &V3ProjectTaskWaitProgress{WorkflowState: "running"}), &V3ProjectTaskRemoteResolution{Reason: V3ProjectTaskRemoteUnreachable})).Message)
		if outcomeRow(t, out, 0)["status"] != V3ProjectTaskRemoteUnreachable || out["resolution"] != V3ProjectTaskRemoteUnreachable {
			t.Fatalf("unreachable: %v", out)
		}
	})
	t.Run("finished", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		registerRemote(f, "finished")
		out := taskWaitOutcome(t, f.apply(f.remoteWake(running("a1", "rs1", &V3ProjectTaskWaitProgress{WorkflowState: "running"}), nil)).Message)
		if outcomeRow(t, out, 0)["status"] != "needs_review" || !strings.Contains(out["guidance"].(string), "until=finished") {
			t.Fatalf("finished: %v", out)
		}
	})
}

// Purpose: the wait reads progress the way the hook and get_task produce it.
// Requirement: the running delivery is shown (else the newest), integration
// state is read, and progress without workflow or integration is nil (no
// workflow attached). Authority: ProjectTaskWaitProgressFrom.
func TestProjectTaskWaitProgressFrom(t *testing.T) {
	p := ProjectTaskWaitProgressFrom(map[string]any{"workflow": map[string]any{"state": "running", "deliveries": []any{
		map[string]any{"workflow": "lint", "state": "done", "step": 1, "steps": 1, "step_type": "run", "round": 0},
		map[string]any{"workflow": "review loop", "state": "running", "step": 2, "steps": 4, "step_type": "reopen", "round": 1},
	}}, "integration": map[string]any{"state": "not_integrated"}})
	if p == nil || *p != (V3ProjectTaskWaitProgress{WorkflowState: "running", Workflow: "review loop", WorkflowStep: 2, WorkflowSteps: 4, WorkflowStepType: "reopen", WorkflowRound: 1, IntegrationState: "not_integrated"}) {
		t.Fatalf("progress: %+v", p)
	}
	if got := ProjectTaskWaitProgressFrom(map[string]any{"state": "done", "summary": "finished"}); got != nil {
		t.Fatalf("no workflow read as %+v", got)
	}
	if got := ProjectTaskWaitProgressFrom([]byte("not json")); got != nil {
		t.Fatalf("garbage read as %+v", got)
	}
}

// Purpose: a follow-up reopen (ReopenProjectTask) keeps a settled wait on the
// task while the follow-up launches. Requirement: the reservation write
// (ReserveTaskFollowupWithRepositories through UpdateProjectTask, which
// reconciles waits before the launch creates the attempt's session) leaves
// the wait asleep, neither ready nor superseded; once the session exists the
// ownership check applies: this owner's delegated session for the task is
// followed (the row names the new attempt), and a session owned by another
// task ends the row as superseded. Threat: a follow-up ending the wait as
// superseded before anything ran, or a reserved attempt redirecting the wait
// to a session it does not own. Authority: ReserveTaskFollowup (real
// reservation path) and the wake branch of prepareProjectTaskWait over a
// temporary real store.
func TestProjectTaskWaitSettledFollowsReservedFollowup(t *testing.T) {
	reserve := func(t *testing.T) (*taskWaitFixture, string) {
		t.Helper()
		f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task", "other")
		f.register("", "task")
		progress.set("task", workflowProgress("running", 1, "agent", 0, ""))
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status, t.Attempts[0].Status = "needs_review", "needs_review" })
		f.mustSleep("before the follow-up")
		progress.set("task", workflowProgress("done", 1, "agent", 0, ""))
		task, ok, err := f.db.GetProjectTask("account", "project", "task")
		if err != nil || !ok {
			t.Fatalf("task: %v %v", ok, err)
		}
		reserved, err := f.db.ReserveTaskFollowup("account", "project", "task", "user", "followup-1", "Address the review.", task.Revision, 1)
		if err != nil {
			t.Fatal(err)
		}
		a := reserved.ActiveAttempt()
		if a == nil || a.ID == "a1" || a.LaunchState != ProjectTaskAttemptLaunchReserved || a.SessionID != "task-followup-"+a.ID {
			t.Fatalf("reservation: %+v", a)
		}
		// The reservation's own write reconciled the waits; so does this.
		if out := f.reconcile(); out != nil {
			t.Fatalf("reserved follow-up ended the wait: %v", out)
		}
		return f, a.SessionID
	}
	createSession := func(f *taskWaitFixture, session, taskID string) {
		f.apply(V3SessionMutationInput{SessionID: session, Kind: V3SessionMutationCreateSession, ClientRequestID: "create-" + session, Session: &SessionSnapshot{ID: session, Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": taskID}}})
	}
	launched := func(f *taskWaitFixture) {
		f.setTask("task", func(t *ProjectTaskRecord) { t.ActiveAttempt().LaunchState = "launched" })
	}
	t.Run("owned_session_followed", func(t *testing.T) {
		f, session := reserve(t)
		createSession(f, session, "task")
		launched(f)
		f.mustSleep("follow-up launched and running")
		f.setTask("task", func(t *ProjectTaskRecord) {
			t.Status, t.ActiveAttempt().Status, t.ActiveAttempt().Summary = "needs_review", "needs_review", "Addressed."
		})
		out := f.reconcile()
		if out == nil {
			t.Fatal("follow-up end did not wake")
		}
		row := outcomeRow(t, out, 0)
		if row["status"] != "needs_review" || row["session_id"] != session || row["pinned_attempt_id"] != "a1" || row["attempt_id"] == "a1" {
			t.Fatalf("followed row: %v", row)
		}
	})
	t.Run("foreign_session_superseded", func(t *testing.T) {
		f, session := reserve(t)
		createSession(f, session, "other")
		launched(f)
		out := f.reconcile()
		if out == nil {
			t.Fatal("foreign follow-up session did not end the row")
		}
		if row := outcomeRow(t, out, 0); row["status"] != "superseded" || row["attempt_id"] != "a1" || row["session_id"] != "child-task" {
			t.Fatalf("foreign follow-up followed: %v", row)
		}
	})
}

// Purpose: a settled watch does not fire while the task's derived state
// (ProjectTaskState, what get_task shows) still says the work runs, and its
// outcome row names that state. Requirement: needs_review with workflow done
// but task_state running (the final step's result not recorded) is not ready;
// once task_state is landed the watch fires and the row carries task_state
// landed with its reason next to integration_state. Threat (observed live): a
// wake reporting not_integrated for work get_task called landed moments
// later. Authority: projectTaskWaitReadiness and projectTaskWaitProgressRow
// through ReconcileProjectTaskWaits over a temporary real store.
func TestProjectTaskWaitSettledReadsTheDerivedTaskState(t *testing.T) {
	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	running := workflowProgress("done", 2, "run", 0, "not_integrated")
	running["task_state"], running["task_state_reason"] = ProjectTaskStateRunning, "workflow running"
	progress.set("task", running)
	f.mustSleep("workflow done but the push result not recorded")
	landed := workflowProgress("done", 2, "run", 0, "not_integrated")
	landed["task_state"], landed["task_state_reason"] = ProjectTaskStateLanded, "pushed 7ec9a8e"
	progress.set("task", landed)
	out := f.reconcile()
	if out == nil {
		t.Fatal("landed task did not fire the watch")
	}
	if row := outcomeRow(t, out, 0); row["task_state"] != ProjectTaskStateLanded || row["task_state_reason"] != "pushed 7ec9a8e" || row["integration_state"] != "not_integrated" {
		t.Fatalf("outcome row: %v", row)
	}
	if !strings.Contains(out["guidance"].(string), "task_state says where the work is") {
		t.Fatalf("guidance: %v", out["guidance"])
	}
}
