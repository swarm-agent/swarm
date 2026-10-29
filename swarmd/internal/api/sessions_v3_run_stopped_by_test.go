package api

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: a cancelled V3 run intent durably records an explicit
//     person stop (V3SessionRunIntent.StoppedBy = V3RunStopperUser) only when
//     the cancel carries the session stop routes' cause
//     (sessionruntime.PlanStoppedByUser), whatever the stop's free-text reason.
//     The account daily usage-limit enforcement (CancelRunsForAccount, with
//     "daily usage limit exceeded ...") records none. A worker job keeps its
//     job open after a cancel only on that mark (run.observeRun,
//     worker_job_stop_test.go), so a usage-limit cancel still cancels it.
//   - Threat/regression: every cancel without the job's cancel mark was read
//     as swarm_stop_run, so usage-limit cancels left jobs "stopped" and raised
//     worker.run.stopped instead of worker.run.cancelled.
//   - Boundary/authority: sessionV3Executor.CancelRun (called with the stop
//     cause exactly as handleSessionV3PrimaryRunStop does) and
//     CancelRunsForAccount over recordRunStatusInEpoch, read back from the
//     stored run intent. Real server, executor and Pebble store; only the
//     model provider is stubbed (blockingProvider).
//   - Narrowest layer: the classification is assigned where the executor
//     records the cancelled intent; only that stored intent shows it.
func TestV3CancelRecordsAPersonStopOnlyForTheStopRoute(t *testing.T) {
	f, p := newRunCancelFixture(t, "stopped-by")
	stopped := f.createSession("coder-job")
	limited := f.createSession("limited-job")
	stopRun := sendFollowup(t, f, p, stopped, "followup-stop")
	limitRun := sendFollowup(t, f, p, limited, "followup-limit")

	// The stop route's call (swarm_stop_run), with a caller-chosen reason.
	const reason = "wrong approach; I will send a correction"
	if _, cancelled, err := f.server.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: stopped, RunID: stopRun, StopCause: sessionruntime.PlanStoppedByUser}, reason); err != nil || !cancelled {
		t.Fatalf("CancelRun: cancelled=%v err=%v", cancelled, err)
	}
	f.waitRun(stopped, stopRun, sessionruntime.RunIntentCancelled)
	intent, ok, err := f.sessions.GetSessionRunIntent(stopped, stopRun)
	if err != nil || !ok || !pebblestore.V3RunIntentStoppedByPerson(intent) || intent.BlockedReason != reason {
		t.Fatalf("person stop intent = %+v %v %v, want stopped_by %q", intent, ok, err, pebblestore.V3RunStopperUser)
	}

	// The daily usage-limit enforcement's call: no person stop. The stopped
	// session's run is already cancelled, so only the limited run is left.
	const limitReason = "daily usage limit exceeded ($5.0100 spent today, limit is $5.00)"
	if n := f.server.v3SessionExecutor.CancelRunsForAccount(testPrincipal().AccountScopeID, limitReason); n != 1 {
		t.Fatalf("CancelRunsForAccount cancelled %d runs, want 1", n)
	}
	f.waitRun(limited, limitRun, sessionruntime.RunIntentCancelled)
	intent, ok, err = f.sessions.GetSessionRunIntent(limited, limitRun)
	if err != nil || !ok || intent.StoppedBy != "" || pebblestore.V3RunIntentStoppedByPerson(intent) || intent.BlockedReason != limitReason {
		t.Fatalf("usage-limit intent = %+v %v %v, want no person stop", intent, ok, err)
	}
	// The earlier person stop is unchanged by the account-wide cancel.
	if again, _, _ := f.sessions.GetSessionRunIntent(stopped, stopRun); !pebblestore.V3RunIntentStoppedByPerson(again) {
		t.Fatalf("account cancel rewrote the person stop: %+v", again)
	}
}
