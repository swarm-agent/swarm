package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Purpose: a worker run's terminal activity (job.failed / job.finished in the
// v3 activity feed) and its terminal observer call are emitted once per
// transition, never on bookkeeping re-saves of the already-terminal run: the
// review checkout cleanup's two writes per attempt (pending, then failed) and
// SetSandboxCleanupError still publish a worker.updated invalidation, but
// without an activity kind, and the observer (worker.run.* signals, sandbox
// end) is not called again.
// Threat: the forge's v3 activity re-announcing the same three old failed
// Reviewer runs as job.failed, two events every ~32 minutes all night, with no
// new run: each review cleanup retry re-saved the failed run twice and
// commitWorkerRealtime labelled every write of a failed run job.failed.
// Boundary: WorkerStore.RecordWorkerRun / SetSandboxCleanupError ->
// Store.commitWorkerRealtime (the outbox row the activity feed reads) and the
// installed worker-run observer, on a temporary Pebble store.
func TestWorkerRunTerminalActivityEmittedOncePerTransition(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "activity.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const account, user = "acct-1", "user-1"
	ws := NewWorkerStore(db)
	sessions := NewSessionStore(db)
	observed := 0
	db.SetWorkerRunObserver(func(WorkerRecord, WorkerRunRecord) { observed++ })
	w, err := ws.CreateWorker(account, user, CreateWorkerRequest{Role: WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	activity := func() map[string]int {
		t.Helper()
		rows, err := sessions.ListV3RealtimeOutboxAfter(0, 500)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]int{}
		for _, row := range rows {
			if row.Event.EventType != WorkerUpdatedEventType {
				continue
			}
			var p WorkerRealtimePayload
			if err := json.Unmarshal(row.Event.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.RunID == "wrun_old" {
				kinds[p.ActivityKind]++
			}
		}
		return kinds
	}
	run := WorkerRunRecord{ID: "wrun_old", WorkerID: w.ID, UserID: user, Role: WorkerRoleReviewer, SessionID: "worker-execution-wrun_old", RequestSource: "direct", AccountScopeID: account,
		Status: "running", Input: map[string]any{"review_job": "worker/c/r"}, CreatedAt: 1, StartedAt: 1}
	if _, err := ws.RecordWorkerRun(account, run); err != nil {
		t.Fatal(err)
	}
	run.Status, run.Error, run.CompletedAt = "failed", "review failed", 2
	run.Cleanup = &WorkerRunCleanup{State: "pending"}
	if _, err := ws.RecordWorkerRun(account, run); err != nil {
		t.Fatal(err)
	}
	if got := activity(); got["job.failed"] != 1 || observed != 1 {
		t.Fatalf("after the failure: activity %v observer %d, want one job.failed and one observer call", got, observed)
	}

	// Bookkeeping re-saves: four cleanup attempts (two writes each, below
	// the reporting threshold) and two sandbox cleanup errors.
	for attempt := 1; attempt <= 4; attempt++ {
		run.Cleanup = &WorkerRunCleanup{State: "pending", Attempts: attempt, NextAttemptAt: int64(attempt)}
		if _, err := ws.RecordWorkerRun(account, run); err != nil {
			t.Fatal(err)
		}
		run.Cleanup = &WorkerRunCleanup{State: "failed", Attempts: attempt, NextAttemptAt: int64(attempt), LastError: "remove checkout: busy"}
		if _, err := ws.RecordWorkerRun(account, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, msg := range []string{"remove job sandbox: exit status 1", "remove job sandbox: timeout"} {
		if err := ws.SetSandboxCleanupError(run, msg); err != nil {
			t.Fatal(err)
		}
	}
	got := activity()
	if got["job.failed"] != 1 || got["job.finished"] != 0 || got["job.state_changed"] != 0 {
		t.Fatalf("re-saves re-announced the run: %v", got)
	}
	if got[""] != 10 {
		t.Fatalf("bookkeeping invalidations = %d, want 10 (worker.updated still published without an activity kind)", got[""])
	}
	if observed != 1 {
		t.Fatalf("observer calls after re-saves = %d, want still 1", observed)
	}
	// Negative control: a run's first terminal write without a prior record
	// is a transition and is announced.
	fresh := WorkerRunRecord{ID: "wrun_new", WorkerID: w.ID, UserID: user, Role: WorkerRoleReviewer, Status: "failed", Input: map[string]any{"review_job": "worker/c/r"}, CreatedAt: 3, StartedAt: 3, CompletedAt: 4}
	if _, err := ws.RecordWorkerRun(account, fresh); err != nil {
		t.Fatal(err)
	}
	rows, err := sessions.ListV3RealtimeOutboxAfter(0, 500)
	if err != nil {
		t.Fatal(err)
	}
	last := rows[len(rows)-1]
	var p WorkerRealtimePayload
	if err := json.Unmarshal(last.Event.Payload, &p); err != nil || p.RunID != "wrun_new" || p.ActivityKind != "job.failed" {
		t.Fatalf("a new failed run's activity = %+v %v", p, err)
	}
}
