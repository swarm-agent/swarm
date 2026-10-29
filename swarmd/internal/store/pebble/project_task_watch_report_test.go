package pebblestore

import (
	"testing"
)

// watchReportFixture is a project Orchestrator ("parent", run-1 running) with
// in-progress tasks whose sessions run "crun-<id>" deployed from parent.
func newWatchReportFixture(t *testing.T, ids ...string) *taskWaitFixture {
	t.Helper()
	f := newLocalWatchFixture(t, ids...)
	for _, id := range ids {
		if _, err := f.db.UpdateProjectTask("account", "project", id, func(t *ProjectTaskRecord) error {
			t.Status = "in_progress"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		child := "child-" + id
		f.apply(V3SessionMutationInput{SessionID: child, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "pending-" + child, RunIntent: &V3SessionRunIntent{RunID: "crun-" + id, Status: V3RunIntentPendingExecutor, ParentSessionID: "parent"}})
		f.apply(V3SessionMutationInput{SessionID: child, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "running-" + child, RunIntent: &V3SessionRunIntent{RunID: "crun-" + id, Status: V3RunIntentRunning, ParentSessionID: "parent"}})
	}
	return f
}

// report records a report_task update from task id's running session.
func (f *taskWaitFixture) report(id string, kind ProjectTaskUpdateKind, key string) {
	f.t.Helper()
	f.apply(V3SessionMutationInput{SessionID: "child-" + id, Kind: V3SessionMutationReportTask, ClientRequestID: "task-report:" + key, PayloadHash: "derived-by-store",
		TaskReport: &V3ProjectTaskReportMutation{RunID: "crun-" + id, ProjectID: "project", TaskID: id, Kind: kind, Summary: "report " + key}})
}

// wakeTurn starts the fired watch's new turn and returns the reports that turn
// reads (PendingProjectTaskUpdates, what the provider loop delivers).
func (f *taskWaitFixture) wakeTurnReports(watch V3SessionTaskWatch) (V3SessionRunIntent, []ProjectTaskUpdate) {
	f.t.Helper()
	next := f.assertContinuation("parent", watch.WatchID)
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-" + next.RunID, RunIntent: &V3SessionRunIntent{RunID: next.RunID, Status: V3RunIntentRunning}})
	updates, _, err := f.db.PendingProjectTaskUpdates("account", "user", "parent", next.RunID, "")
	if err != nil {
		f.t.Fatalf("reports for the wake turn: %v", err)
	}
	return next, updates
}

func hasReport(updates []ProjectTaskUpdate, task, summary string) bool {
	for _, u := range updates {
		if u.TaskID == task && u.Summary == summary {
			return true
		}
	}
	return false
}

// Purpose: a delegated task's report_task updates still reach the
// Orchestrator under the watch model. Requirement (the settle rules a wait
// had): with the Orchestrator idle after wait_tasks, a progress report from a
// watched task wakes nothing; an attention (or wake_request) report from it
// fires the watch (task_updates_pending true); the new turn carries the
// watch's report generation (TaskUpdateRootRunID = the registering run) and
// PendingProjectTaskUpdates gives that turn the reports, so the wake message
// itself did not supersede them. An attention report from a task the
// conversation does not watch wakes nothing (it is read by the next turn of
// the generation, as before). Threat (review finding): reports
// keyed to the deploying run never woke an idle Orchestrator and were never
// delivered to any later turn. Authority: projectTaskWatchLocalOutcome
// (projectTaskUpdateWakeEligible), bindTaskWatchReportsInBatch,
// prepareSessionWake (TaskUpdateRootRunID), advancesTaskUpdateFence and
// pendingProjectTaskUpdates over a real store: the narrowest layer holding the
// report records, the watch and the wake turn together.
func TestProjectTaskWatchReportsWakeAndReachTheTurn(t *testing.T) {
	f := newWatchReportFixture(t, "t1", "t2")
	f.apply(localWatch("run-1", "t1"))
	watch := f.onlyWatch("parent")
	if watch.UpdateRoot != "run-1" {
		t.Fatalf("watch report generation: %+v", watch)
	}
	f.endRun("parent", "run-1")

	f.report("t1", ProjectTaskUpdateProgress, "p1")
	if woke := f.reconcileCount(); woke != 0 {
		t.Fatalf("a progress report woke the watch: %d", woke)
	}
	f.report("t2", ProjectTaskUpdateAttention, "unwatched")
	if woke := f.reconcileCount(); woke != 0 {
		t.Fatalf("an unwatched task's report woke the watch: %d", woke)
	}
	f.onlyWatch("parent")

	f.report("t1", ProjectTaskUpdateAttention, "a1")
	var published []V3SessionMutationResult
	if err := f.db.ReconcileProjectTaskWaits("", "", func(r V3SessionMutationResult) { published = append(published, r) }); err != nil || len(published) != 1 {
		t.Fatalf("attention report did not wake the watch: %v %d", err, len(published))
	}
	if out := taskWaitOutcome(t, published[0].Message); out["task_updates_pending"] != true || out["all_ready"] != false {
		t.Fatalf("outcome: %v", out)
	}
	next, updates := f.wakeTurnReports(watch)
	if next.TaskUpdateRootRunID != "run-1" {
		t.Fatalf("wake turn report generation: %+v", next)
	}
	// The turn reads every report of its generation, as any turn of it does
	// (t2 was deployed by run-1 too): t2 did not wake the watch, but its
	// report is not lost either.
	if !hasReport(updates, "t1", "report a1") || !hasReport(updates, "t1", "report p1") || !hasReport(updates, "t2", "report unwatched") {
		t.Fatalf("reports for the wake turn: %+v", updates)
	}
}

// Purpose: a person's message does not cut a watch off from its tasks'
// reports. Requirement: after the person's message (which starts a new
// report generation and supersedes reports made before it), the watch reads
// the new run's generation; a later wake_request report from the watched
// task fires it and the wake turn (TaskUpdateRootRunID = the person's run)
// reads that report, not the superseded one. Threat: a watch that survives
// the message but whose tasks can never wake it again. Authority:
// moveTaskWatchesToGenerationInBatch and the task-update fence over a real
// store.
func TestProjectTaskWatchReportsFollowThePersonsMessage(t *testing.T) {
	f := newWatchReportFixture(t, "t1")
	f.apply(localWatch("run-1", "t1"))
	watch := f.onlyWatch("parent")
	f.report("t1", ProjectTaskUpdateProgress, "before")
	f.endRun("parent", "run-1")
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "msg-2", Message: &MessageSnapshot{ID: "m2", Role: "user", Content: "how is it going?"}, RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-2-running", RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentRunning}})
	f.endRun("parent", "run-2")
	if got := f.onlyWatch("parent"); got.WatchID != watch.WatchID || got.UpdateRoot != "run-2" {
		t.Fatalf("watch after the person's message: %+v", got)
	}
	f.report("t1", ProjectTaskUpdateWakeRequest, "after")
	if woke := f.reconcileCount(); woke != 1 {
		t.Fatalf("wake_request after the person's message: %d", woke)
	}
	next, updates := f.wakeTurnReports(watch)
	if next.TaskUpdateRootRunID != "run-2" || !hasReport(updates, "t1", "report after") || hasReport(updates, "t1", "report before") {
		t.Fatalf("wake turn %+v reads %+v", next, updates)
	}
}
