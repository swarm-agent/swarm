package run

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a worker job's task error and action describe only its latest
// run. Terminal repair re-reads a job's receipt, which keeps its first run's
// outcome after follow-ups ran; it must not put that blocker back on a task a
// later follow-up settled (cancelling the job still closes the task). A run
// that succeeds replaces an earlier run's failed/blocked action.
// Threat (socials machine, 2026-10-30): after a blocked job's follow-ups
// succeeded, its task (swarm_get_job last_error/action_needed) still showed
// the morning's blocker, which then reached the job's workflow.
// Owner: WorkerExecutionService.reconcileWorkerTaskTerminal over a real Pebble
// store (limitRunFixture); calling it directly is the narrowest layer that
// proves the stored task's postconditions for each receipt shape.
func TestWorkerTaskReflectsLatestRun(t *testing.T) {
	const blocker = "blocked: Narration can't be generated"
	const staleAction = "Action Needed: Worker run failed (" + blocker + "). Retry or reassign task."
	const reviewAction = "Action Needed: Review agent deliverables and verify outcomes"
	for _, tc := range []struct {
		name                      string
		followup                  bool
		runStatus, runError       string
		taskStatus, taskLastError string
		taskAction                string
		wantStatus, wantLastError string
		wantAction                string
	}{
		{name: "repair after a successful follow-up keeps the task", followup: true, runStatus: "failed", runError: blocker,
			taskStatus: "needs_review", taskAction: reviewAction, wantStatus: "needs_review", wantAction: reviewAction},
		{name: "first run blocked still projects", runStatus: "failed", runError: blocker,
			taskStatus: "in_progress", wantStatus: "failed", wantLastError: blocker, wantAction: staleAction},
		{name: "cancelling the job still closes it", followup: true, runStatus: "cancelled", runError: "worker run cancelled",
			taskStatus: "needs_review", taskAction: reviewAction, wantStatus: "failed", wantLastError: "worker run cancelled", wantAction: "Action Needed: Run was cancelled. Retry or reassign task."},
		{name: "success replaces a failed run's error and action", runStatus: "succeeded",
			taskStatus: "failed", taskLastError: blocker, taskAction: staleAction, wantStatus: "needs_review", wantAction: reviewAction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLimitRunFixture(t, store.WorkerRoleCoder)
			db := f.svc.Store()
			if err := db.PutProjectTask("account-a", &store.ProjectTaskRecord{ID: "task-job", Title: "Narrated video", ProjectID: "project-a", AccountID: "account-a",
				WorkerID: f.worker.ID, WorkerRunID: f.run.ID, SessionID: f.run.SessionID, Agent: "coder", Status: tc.taskStatus, LastError: tc.taskLastError, ActionNeeded: tc.taskAction}); err != nil {
				t.Fatal(err)
			}
			r := f.current()
			r.Status, r.Error = tc.runStatus, tc.runError
			if tc.followup {
				r.FollowupRunID, r.FollowupStatus = "followup-1", "completed"
			}
			if err := f.execution.reconcileWorkerTaskTerminal("account-a", "project-a", "task-job", tc.runStatus, tc.runError, r); err != nil {
				t.Fatal(err)
			}
			task, found, err := db.GetProjectTask("account-a", "project-a", "task-job")
			if err != nil || !found {
				t.Fatalf("task: %v %v", found, err)
			}
			if task.Status != tc.wantStatus || task.LastError != tc.wantLastError || task.ActionNeeded != tc.wantAction {
				t.Fatalf("task = status %q last_error %q action %q, want %q %q %q", task.Status, task.LastError, task.ActionNeeded, tc.wantStatus, tc.wantLastError, tc.wantAction)
			}
		})
	}
}
