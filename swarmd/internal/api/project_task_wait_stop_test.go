package api

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a client's stop really cancels a run asleep on tasks.
// Requirement: while a run waits on wait_tasks the session has no active run
// intent (the detail's active_run_intent is empty), so a client must send the
// waiting run's own id; the stop path (CancelRun, what POST run/stop calls)
// then cancels that waiting run and records the "Wait ended: stopped." line.
// Threat: a stop button for a waiting run that finds nothing to stop, or a
// stop that leaves the wait registered. Authority:
// GetSessionActiveRunIntent and sessionV3Executor.CancelRun over the real
// store; the API fixture is the narrowest layer holding both.
func TestStopCancelsAWaitingRunByItsID(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	w.localWait(t)
	if w.f.s.v3SessionExecutor == nil {
		t.Fatal("fixture has no v3 executor")
	}
	if intent, ok, err := w.f.s.sessions.GetSessionActiveRunIntent("goal"); err != nil {
		t.Fatal(err)
	} else if ok && sessionV3RunIntentStatusActive(intent.Status) {
		t.Fatalf("a waiting run is listed as active: %+v", intent)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}
	if _, _, err := w.f.s.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: principal, SessionID: "goal", RunID: "run-1", StopCause: sessionruntime.PlanStoppedByUser}, ""); err != nil {
		t.Fatalf("stop of the waiting run: %v", err)
	}
	if got := w.goalIntent(t, "run-1"); got.Status != pebblestore.V3RunIntentCancelled {
		t.Fatalf("waiting run not cancelled: %+v", got)
	}
	messages, err := w.f.s.sessions.Store().ListMessages("goal", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	ended := 0
	for _, m := range messages {
		if pebblestore.IsProjectTaskWaitNoticeMessage(m) && m.Content == "Wait ended: stopped." {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("stop end lines = %d", ended)
	}
}
