package run

import (
	"path/filepath"
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a terminal reviewer run's checkout cleanup retry is bounded.
// After reviewCleanupMaxAttempts failed attempts the failure is reported once
// (Cleanup.FailureReported, which the daemon observer turns into one
// job.cleanup_failed warning) and later reconcile sweeps, even long after the
// backoff deadline, neither retry nor re-save the run.
// Threat: the forge's old failed Reviewer runs, whose cleanup was retried
// every backoff period all night; each retry re-saved the failed run twice,
// re-announcing it as job.failed about every 32 minutes with no new run.
// Boundary: WorkerExecutionService.performReviewCleanup (called by the worker
// reconcile through retryReviewCleanup) over a real temporary Pebble worker
// and session store. The cleanup fails deterministically because the review
// session has no captured head, before any filesystem side effect.
func TestReviewCleanupRetryIsBoundedThenReportedOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cleanup.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	svc := sessions.NewService(store.NewSessionStore(db), events)
	ws := svc.Store().WorkerStore()
	worker, err := ws.CreateWorker("account-a", "user-a", store.CreateWorkerRequest{Role: store.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pref := store.ModelPreference{Provider: "fixture-provider", Model: "fixture-model", Thinking: "high"}
	sess, _, err := svc.CreateSessionWithOptions(sessions.CreateSessionOptions{SessionID: "worker-execution-wrun_old", WorkspacePath: t.TempDir(), UserID: "user-a", AccountScopeID: "account-a", Preference: &pref, Metadata: map[string]any{"worker_id": worker.ID, "worker_execution_run_id": "wrun_old"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.RecordWorkerRun("account-a", store.WorkerRunRecord{ID: "wrun_old", WorkerID: worker.ID, UserID: "user-a", Role: store.WorkerRoleReviewer, SessionID: sess.ID,
		Status: "failed", Error: "review failed", Input: map[string]any{"review_job": "worker/c/r"}, Cleanup: &store.WorkerRunCleanup{State: "pending"}, CreatedAt: 1, StartedAt: 1, CompletedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	reported := 0
	db.SetWorkerRunObserver(func(_ store.WorkerRecord, r store.WorkerRunRecord) {
		if r.Cleanup != nil && r.Cleanup.FailureReported {
			reported++
		}
	})
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: svc}}}
	read := func() store.WorkerRunRecord {
		t.Helper()
		got, found, err := ws.GetWorkerRun("account-a", worker.ID, run.ID)
		if err != nil || !found || got.Cleanup == nil {
			t.Fatalf("read run: %+v %v %v", got, found, err)
		}
		return got
	}
	// Each sweep runs after the retry deadline passed (the deadline is moved
	// into the past, as the clock would).
	due := func() {
		t.Helper()
		r := read()
		if r.Cleanup.NextAttemptAt > 0 {
			r.Cleanup.NextAttemptAt = 1
			if _, err := ws.RecordWorkerRun("account-a", r); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 1; i <= reviewCleanupMaxAttempts; i++ {
		due()
		if err := execution.performReviewCleanup(read()); err != nil {
			t.Fatal(err)
		}
		if c := read().Cleanup; c.Attempts != i || c.State != "failed" || c.LastError == "" {
			t.Fatalf("attempt %d: cleanup %+v", i, c)
		}
	}
	exhausted := read()
	if !exhausted.Cleanup.FailureReported || exhausted.Cleanup.NextAttemptAt != 0 || reported != 1 {
		t.Fatalf("after %d attempts: cleanup %+v, failure reports %d; want reported once, no next attempt", reviewCleanupMaxAttempts, exhausted.Cleanup, reported)
	}
	before, err := svc.Store().ListV3RealtimeOutboxAfter(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Many later sweeps: no retry, no re-save, no second report.
	for i := 0; i < 20; i++ {
		if err := execution.performReviewCleanup(read()); err != nil {
			t.Fatal(err)
		}
		if err := execution.retryReviewCleanup(read()); err != nil {
			t.Fatal(err)
		}
	}
	execution.CloseCleanup()
	after, err := svc.Store().ListV3RealtimeOutboxAfter(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if c := read().Cleanup; c.Attempts != reviewCleanupMaxAttempts || len(after) != len(before) || reported != 1 {
		t.Fatalf("after exhaustion: cleanup %+v, outbox rows %d -> %d, reports %d; want no retry, no write, one report", c, len(before), len(after), reported)
	}
}
