package runtime

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a worker job whose own run was stopped (swarm_stop_run) but stays
// open (pebblestore.WorkerRunStopped) has not finished, so workerRunSignal
// raises worker.run.stopped at info severity, never worker.run.failed: the
// job automations that consume worker.run.succeeded/failed/cancelled must not
// run a review or delivery for a paused job. A stopped job whose follow-up
// then completes raises worker.run.succeeded with that follow-up's identity,
// as any follow-up does. A job cancelled by anything but a person stop (the
// daily usage limit) settles cancelled and raises worker.run.cancelled, which
// outbox delivery and job automations consume. Threat: pausing a job to
// correct it fired its failure automations, or a usage-limit cancel read as a
// pause and fired none. Owner: workerRunSignal over a real temporary signal
// store, the narrowest layer that observes the emitted kind.
func TestWorkerRunSignalStoppedJobIsNotAFailure(t *testing.T) {
	for _, tc := range []struct {
		name, status, runError, followup, want, severity string
	}{
		{"stopped", "failed", pebblestore.WorkerRunStoppedPrefix + "the caller stopped the run", "", "worker.run.stopped", pebblestore.SignalSeverityInfo},
		{"continued_and_completed", "failed", pebblestore.WorkerRunStoppedPrefix + "the caller stopped the run", "completed", "worker.run.succeeded", pebblestore.SignalSeverityInfo},
		{"usage_limit_cancelled", "cancelled", "", "", "worker.run.cancelled", pebblestore.SignalSeverityInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			feed := pebblestore.NewSignalStore(db)
			sessions := pebblestore.NewSessionStore(db)
			run := pebblestore.WorkerRunRecord{ID: "run-test", AccountScopeID: "account-test", SessionID: "session-test", Role: pebblestore.WorkerRoleCoder,
				Status: tc.status, Error: tc.runError, RequestSource: "direct", FollowupStatus: tc.followup}
			if tc.followup != "" {
				run.FollowupRunID = "followup-test"
				if err := sessions.CreateSession(pebblestore.SessionSnapshot{ID: run.SessionID, AccountScopeID: run.AccountScopeID}); err != nil {
					t.Fatal(err)
				}
			}
			workerRunSignal(signals.NewEmitter(feed), sessions)(pebblestore.WorkerRecord{ID: "worker-test", Name: "Test worker"}, run)
			page, err := feed.ListAfter(0, 10, pebblestore.SignalFilter{Account: run.AccountScopeID})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Signals) != 1 || page.Signals[0].Kind != tc.want || page.Signals[0].Severity != tc.severity {
				t.Fatalf("signals = %+v, want one %s (%s)", page.Signals, tc.want, tc.severity)
			}
			if page.Signals[0].DedupKey != "worker.run:run-test:"+run.FollowupRunID {
				t.Fatalf("signal identity changed: %+v", page.Signals[0])
			}
		})
	}

	// A stopped job that was then cancelled is no longer "stopped".
	cancelled := pebblestore.WorkerRunRecord{Status: "failed", Error: pebblestore.WorkerRunStoppedPrefix + "x", CancelRequested: true}
	if pebblestore.WorkerRunStopped(cancelled) {
		t.Fatal("a cancelled stopped job still reads as stopped")
	}
}
