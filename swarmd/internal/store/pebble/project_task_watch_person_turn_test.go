package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// personTurnWatchCase is one kind of watch (local project tasks, or tasks on a
// linked machine) driven through the same conversation turns.
type personTurnWatchCase struct {
	name string
	// register is the wait_tasks registration made from run.
	register func(run string) V3SessionMutationInput
	// stage moves the watched task to a stage: "review" (review started),
	// "sent_back" (review sent it back) or "landed" (settled); wake is the
	// session wake its follower then offers at now.
	stage func(f *taskWaitFixture, progress *waitProgressStub, stage string)
	wake  func(f *taskWaitFixture, stage string, now int64) V3SessionMutationInput
}

func personTurnWatchCases() []personTurnWatchCase {
	localProgress := map[string]func() map[string]any{
		"review":    func() map[string]any { return workflowProgress("running", 1, "agent", 0, "not_integrated") },
		"sent_back": func() map[string]any { return workflowProgress("running", 2, "reopen", 1, "not_integrated") },
		"landed": func() map[string]any {
			p := workflowProgress("done", 3, "run", 1, "integrated")
			p["task_state"], p["task_state_reason"] = ProjectTaskStateLanded, "pushed 7ec9a8e"
			return p
		},
	}
	remoteProgress := map[string]*V3ProjectTaskWaitProgress{
		"review":    {WorkflowState: "running", WorkflowStep: 1, WorkflowSteps: 4, WorkflowStepType: "agent"},
		"sent_back": {WorkflowState: "running", WorkflowStep: 2, WorkflowSteps: 4, WorkflowStepType: "reopen", WorkflowRound: 1},
		"landed":    {WorkflowState: "done", WorkflowStep: 3, WorkflowSteps: 4, WorkflowStepType: "run", WorkflowRound: 1, TaskState: ProjectTaskStateLanded, TaskStateReason: "pushed 7ec9a8e"},
	}
	return []personTurnWatchCase{
		{
			name: "local",
			register: func(run string) V3SessionMutationInput {
				return V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:" + run, TaskWait: &V3ProjectTaskWaitMutation{RunID: run, ProjectID: "project", TaskIDs: []string{"task"}}}
			},
			stage: func(f *taskWaitFixture, progress *waitProgressStub, stage string) {
				f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
				progress.set("task", localProgress[stage]())
			},
			wake: func(f *taskWaitFixture, _ string, now int64) V3SessionMutationInput {
				in := ProjectTaskWatchWakeInput(f.onlyWatch("parent"), nil, nil)
				in.NowUnixMs = now
				return in
			},
		},
		{
			// The observed case: the project Orchestrator watches tasks on a
			// linked machine (manage_projects wait_tasks machine=<linked>).
			name: "linked machine",
			register: func(run string) V3SessionMutationInput {
				return V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:" + run, TaskWait: &V3ProjectTaskWaitMutation{RunID: run, ProjectID: "remote-project", Machine: "box", TaskIDs: []string{"rt"}, RemoteTargets: []V3ProjectTaskWaitTarget{{TaskID: "rt", AttemptID: "ra1", SessionID: "rs1"}}}}
			},
			stage: func(*taskWaitFixture, *waitProgressStub, string) {},
			wake: func(f *taskWaitFixture, stage string, now int64) V3SessionMutationInput {
				observed := []V3ProjectTaskRemoteObservation{{TaskID: "rt", Title: "Remote task", Status: "needs_review", AttemptID: "ra1", SessionID: "rs1", Progress: remoteProgress[stage]}}
				in := ProjectTaskWatchWakeInput(f.onlyWatch("parent"), observed, nil)
				in.NowUnixMs = now
				return in
			},
		},
	}
}

// personTurn is the person's chat message on the idle conversation: it starts
// run, the executor claims it and reads its task reports (what the provider
// loop does every step), and the run ends without calling wait_tasks. during
// runs while the run is active (the AI's tool calls).
func (f *taskWaitFixture) personTurn(run string, during func()) {
	f.t.Helper()
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "msg-" + run, Message: &MessageSnapshot{ID: "m-" + run, Role: "user", Content: "how is it going?"}, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-" + run, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentRunning}})
	if _, _, err := f.db.PendingProjectTaskUpdates("account", "user", "parent", run, ""); err != nil {
		f.t.Fatalf("person's run %s reads its reports: %v", run, err)
	}
	if during != nil {
		during()
	}
	f.endRun("parent", run)
}

// runWakeTurn checks a delivery started a new turn and runs it as the
// executor does: claim it running, then read the task reports of its
// generation before the provider step. Either failing is the failed run the
// person saw ("project task wait owner is no longer current").
func (f *taskWaitFixture) runWakeTurn(step string, result V3SessionMutationResult) {
	f.t.Helper()
	if result.RunIntent == nil || result.RunIntent.Status != V3RunIntentPendingExecutor || result.Message == nil {
		f.t.Fatalf("%s: no new turn started: %+v", step, result.RunIntent)
	}
	run := result.RunIntent.RunID
	if _, err := f.try(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-" + run, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentRunning}}); err != nil {
		f.t.Fatalf("%s: claiming the wake turn: %v", step, err)
	}
	if _, _, err := f.db.PendingProjectTaskUpdates("account", "user", "parent", run, ""); err != nil {
		f.t.Fatalf("%s: the wake turn could not start its provider step: %v", step, err)
	}
	f.endRun("parent", run)
}

// Purpose: a watch survives the person talking, as wait_tasks promises
// ("their messages never end a watch"). Requirement: after wait_tasks the
// run ends; a milestone wakes a turn; the person then sends a message whose
// run ends without calling wait_tasks again (re-calling it on the watched task
// is refused as already watched and registers nothing); every later milestone
// and the final outcome start a turn that claims and reads its reports like
// any turn, the outcome ends the watch, and each delivery happens exactly
// once. Covered for a local project watch and for a linked-machine watch.
// Threat (observed): the linked-machine watch kept the report generation of
// the run that registered it, so after the person's message (which advances
// the task-update fence) every wake turn failed with ErrProjectTaskWaitStale
// and the outcome never reached the AI. Authority:
// moveTaskWatchesToGenerationInBatch, prepareProjectTaskWatchWake,
// prepareSessionWake and PendingProjectTaskUpdates (projectTaskDeliveryOwner,
// validateTaskUpdateOwnerFence) over a temporary real store: the narrowest
// layer holding the watch, the fence and the wake turns together.
func TestProjectTaskWatchWakesAfterPersonTurn(t *testing.T) {
	for _, c := range personTurnWatchCases() {
		t.Run(c.name, func(t *testing.T) {
			f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
			f.apply(c.register("run-1"))
			f.endRun("parent", "run-1")
			watch := f.onlyWatch("parent")
			t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()

			c.stage(f, progress, "review")
			first := f.apply(c.wake(f, "review", t0))
			if rows := milestoneRows(t, first.Message); len(rows) != 1 || rows[0]["milestone"] != "review started (round 0)" {
				t.Fatalf("first milestone: %v", rows)
			}
			f.runWakeTurn("first milestone", first)

			// The person talks; the AI answers without calling wait_tasks
			// again, then calls it on the watched task in a later turn.
			f.personTurn("run-2", nil)
			f.personTurn("run-3", func() {
				if _, err := f.try(c.register("run-3")); !errors.Is(err, ErrProjectTaskAlreadyWatched) {
					t.Fatalf("re-watch after the person's message: %v", err)
				}
			})
			if got := f.onlyWatch("parent"); got.WatchID != watch.WatchID || got.Deliveries != 1 {
				t.Fatalf("watch after the person's turns: %+v", got)
			}

			c.stage(f, progress, "sent_back")
			second, err := f.try(c.wake(f, "sent_back", t0+120_000))
			if err != nil {
				t.Fatalf("milestone after the person's message: %v", err)
			}
			if rows := milestoneRows(t, second.Message); len(rows) != 1 || rows[0]["milestone"] == "" || rows[0]["milestone"] == "review started (round 0)" {
				t.Fatalf("second milestone: %v", rows)
			}
			f.runWakeTurn("milestone after the person's message", second)
			// The same delivery offered again (a retried follower) starts
			// nothing new.
			if _, err := f.try(c.wake(f, "sent_back", t0+240_000)); !errors.Is(err, ErrProjectTaskWaitNotReady) {
				t.Fatalf("unchanged stage delivered again: %v", err)
			}

			f.personTurn("run-4", nil)
			c.stage(f, progress, "landed")
			final, err := f.try(c.wake(f, "landed", t0+250_000))
			if err != nil {
				t.Fatalf("outcome after the person's message: %v", err)
			}
			if out := taskWaitOutcome(t, final.Message); outcomeRow(t, out, 0)["task_state"] != ProjectTaskStateLanded {
				t.Fatalf("outcome: %v", out)
			}
			f.runWakeTurn("outcome after the person's message", final)
			if n := len(f.watches("parent")); n != 0 {
				t.Fatalf("settled watch kept: %d", n)
			}
		})
	}
}

// Purpose: only the explicit ends end a watch, and a wake for an ended watch
// is dropped quietly. Requirement: after a person's turn, cancel_watch, or
// archiving the conversation, the watched task's next stage starts no turn
// and writes no message (the follower gets ErrProjectTaskWaitStale, which
// fireProjectTaskWatch and the linked follower swallow); the run state is
// unchanged, so no failed run can appear. Threat: a dead watch surfacing as a
// raw failed run, or a cancelled watch still waking the AI. Authority:
// prepareProjectTaskWatchCancel, setProjectTaskWatchesInBatch (archive) and
// prepareProjectTaskWatchWake over a temporary real store.
func TestProjectTaskWatchEndedAfterPersonTurnWakesNothing(t *testing.T) {
	ends := map[string]func(f *taskWaitFixture){
		"cancel_watch": func(f *taskWaitFixture) {
			f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "cancel", TaskWait: &V3ProjectTaskWaitMutation{Cancel: true, CancelledBy: ProjectTaskWaitEndedByAgent}})
		},
		"archive": func(f *taskWaitFixture) {
			if err := f.db.ArchiveSession("parent"); err != nil {
				f.t.Fatal(err)
			}
		},
	}
	for _, c := range personTurnWatchCases() {
		for endName, end := range ends {
			t.Run(c.name+"/"+endName, func(t *testing.T) {
				f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
				f.apply(c.register("run-1"))
				f.endRun("parent", "run-1")
				wake := c.wake(f, "landed", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
				f.personTurn("run-2", nil)
				end(f)
				if n := len(f.watches("parent")); n != 0 {
					t.Fatalf("watches after %s: %d", endName, n)
				}
				before, _, err := f.db.GetV3SessionRunState("parent")
				if err != nil {
					t.Fatal(err)
				}
				c.stage(f, progress, "landed")
				if result, err := f.try(wake); !errors.Is(err, ErrProjectTaskWaitStale) || result.RunIntent != nil || result.Message != nil {
					t.Fatalf("wake after %s: %+v %v", endName, result, err)
				}
				if woke := f.reconcileCount(); woke != 0 {
					t.Fatalf("reconcile woke after %s: %d", endName, woke)
				}
				after, _, err := f.db.GetV3SessionRunState("parent")
				if err != nil || after.RunID != before.RunID || after.Status != before.Status {
					t.Fatalf("run state changed after %s: %+v -> %+v %v", endName, before, after, err)
				}
				if _, found, err := f.db.GetV3SessionRunIntent("parent", SessionWakeRunID("parent", wake.TaskWait.WatchID+":outcome")); err != nil || found {
					t.Fatalf("a wake run was recorded after %s: %v %v", endName, found, err)
				}
			})
		}
	}
}

// Purpose: an outcome queued for a person's run that ended unread still
// reaches the AI after the person's next message. Requirement: the watch
// fires during run-2 (a note for it), run-2 ends without reading it, the
// person's next message starts and ends run-3, then the note is re-delivered
// as a new turn: that turn claims and reads reports normally (it no longer
// names run-2's superseded report generation) and carries the outcome.
// Threat: a re-delivered outcome failing as "project task wait owner is no
// longer current" and never reaching the AI. Authority: prepareSessionWake
// (currentSessionWakeRoot) and PendingProjectTaskUpdates over a real store.
func TestProjectTaskWatchRedeliveredOutcomeAfterPersonTurn(t *testing.T) {
	f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}}})
	f.endRun("parent", "run-1")
	f.personTurn("run-2", func() {
		f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
		if woke := f.reconcileCount(); woke != 1 {
			t.Fatalf("outcome during the person's run: %d", woke)
		}
	})
	notes, err := f.db.ListV3SessionWakeNotes("parent")
	if err != nil || len(notes) != 1 || notes[0].RunID != "run-2" {
		t.Fatalf("unread note: %+v %v", notes, err)
	}
	f.personTurn("run-3", nil)
	again := f.redeliver(notes[0])
	taskWaitOutcome(t, again.Message)
	f.runWakeTurn("re-delivered outcome", again)
	if notes, _ := f.db.ListV3SessionWakeNotes("parent"); len(notes) != 0 {
		t.Fatalf("note kept after re-delivery: %+v", notes)
	}
}

// Purpose: a turn that no longer owns the conversation's task reports goes
// on without them instead of failing. Requirement: StepProjectTaskUpdates
// (what both provider loops read every step) gives a superseded run no
// reports and no error where PendingProjectTaskUpdates says
// ErrProjectTaskWaitStale; those reports stay pending for the current owner;
// another principal is still refused with an error. Threat: the raw "project
// task wait owner is no longer current" text failing a run in the transcript,
// or a superseded run consuming (or a foreign principal reading) reports.
// Authority: StepProjectTaskUpdates over PendingProjectTaskUpdates
// (projectTaskDeliveryOwner) on a real store.
func TestStepProjectTaskUpdatesSkipsASupersededRun(t *testing.T) {
	f := newWatchReportFixture(t, "t1")
	f.apply(localWatch("run-1", "t1"))
	f.endRun("parent", "run-1")
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "msg-2", Message: &MessageSnapshot{ID: "m2", Role: "user", Content: "status?"}, RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-2-running", RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentRunning}})
	f.report("t1", ProjectTaskUpdateProgress, "current")
	if _, _, err := f.db.PendingProjectTaskUpdates("account", "user", "parent", "run-1", ""); !errors.Is(err, ErrProjectTaskWaitStale) {
		t.Fatalf("superseded run: %v", err)
	}
	updates, next, err := f.db.StepProjectTaskUpdates("account", "user", "parent", "run-1", "")
	if err != nil || len(updates) != 0 || next != "" {
		t.Fatalf("superseded run's step: %+v %q %v", updates, next, err)
	}
	if _, _, err := f.db.StepProjectTaskUpdates("account", "intruder", "parent", "run-2", ""); err == nil || errors.Is(err, ErrProjectTaskWaitStale) {
		t.Fatalf("another principal's step: %v", err)
	}
	current, _, err := f.db.StepProjectTaskUpdates("account", "user", "parent", "run-2", "")
	if err != nil || !hasReport(current, "t1", "report current") {
		t.Fatalf("current run's step: %+v %v", current, err)
	}
}
