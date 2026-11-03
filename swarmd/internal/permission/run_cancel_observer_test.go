package permission

import (
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: every run cancellation of pending permission requests is observable
// exactly once, whoever cancels them, including the restart reconciliation
// that runs before the API server (the observer) exists.
// Requirement: CancelRunPending tells the installed RunPendingCancelledObserver
// of exactly the requests that call moved from pending to cancelled;
// ReconcilePendingRuns' cancellations made with no observer installed are kept
// and delivered once when SetRunPendingCancelledObserver installs one; a later
// CancelRunPending of the same run (the run's deferred cancellation losing to
// a stop) cancels nothing and tells nothing; an already decided request is
// never reported cancelled. Threat: a canceller (the run's own deferred
// cancel, the restart reconciliation) cancelling requests in the store with
// nobody told, so the session's approval stays open forever, or a duplicate
// notification. Boundary: Service.CancelRunPending, ReconcilePendingRuns,
// SetRunPendingCancelledObserver. Narrowest layer: the permission service over
// a temporary Pebble store.
func TestRunPendingCancelledObserverSeesEveryCanceller(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"session-a", "session-b"} {
		if err := db.PutJSON(pebblestore.KeySession(id), pebblestore.SessionSnapshot{ID: id, AccountScopeID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	pending := func(sessionID, runID, callID string) pebblestore.PermissionRecord {
		t.Helper()
		record, err := s.CreatePending(CreateInput{SessionID: sessionID, RunID: runID, CallID: callID, ToolName: "bash", ToolArguments: `{"command":"ls"}`, Mode: "auto"})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	restartA := pending("session-a", "run-a", "call-a")
	answered := pending("session-a", "run-a", "call-answered")
	if _, err := s.Resolve("session-a", answered.ID, "deny", "no"); err != nil {
		t.Fatal(err)
	}

	type seen struct {
		sessionID, runID, permissionID, status string
	}
	var (
		mu  sync.Mutex
		got []seen
	)
	observer := func(sessionID, runID string, cancelled []pebblestore.PermissionRecord) {
		mu.Lock()
		defer mu.Unlock()
		for _, record := range cancelled {
			got = append(got, seen{sessionID, runID, record.ID, record.Status})
		}
	}
	take := func() []seen {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}

	// The restart reconciliation runs before any observer exists.
	if err := s.ReconcilePendingRuns("daemon restarted"); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ := s.GetPermission("session-a", restartA.ID); stored.Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("restart left %s %s", restartA.ID, stored.Status)
	}
	if len(take()) != 0 {
		t.Fatal("an observer that was not installed was told")
	}
	s.SetRunPendingCancelledObserver(observer)
	if delivered := take(); len(delivered) != 1 || delivered[0] != (seen{"session-a", "run-a", restartA.ID, pebblestore.PermissionStatusCancelled}) {
		t.Fatalf("restart backlog delivered = %+v, want exactly %s cancelled", delivered, restartA.ID)
	}
	// Installing again delivers nothing twice.
	s.SetRunPendingCancelledObserver(observer)
	if again := take(); len(again) != 0 {
		t.Fatalf("backlog delivered twice: %+v", again)
	}

	// A live cancellation (stop or the run's own end) is told at once; the
	// losing second canceller of the same run tells nothing.
	liveB := pending("session-b", "run-b", "call-b")
	cancelled, err := s.CancelRunPending("session-b", "run-b", "run terminated before permission resolution")
	if err != nil || len(cancelled) != 1 || cancelled[0].ID != liveB.ID {
		t.Fatalf("cancel = %+v %v", cancelled, err)
	}
	if delivered := take(); len(delivered) != 1 || delivered[0] != (seen{"session-b", "run-b", liveB.ID, pebblestore.PermissionStatusCancelled}) {
		t.Fatalf("live cancellation delivered = %+v", delivered)
	}
	if second, err := s.CancelRunPending("session-b", "run-b", "the person stopped it"); err != nil || len(second) != 0 {
		t.Fatalf("second cancel = %+v %v", second, err)
	}
	if delivered := take(); len(delivered) != 0 {
		t.Fatalf("the losing canceller was told: %+v", delivered)
	}
	if stored, _, _ := s.GetPermission("session-a", answered.ID); stored.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("the answered request became %s", stored.Status)
	}
}
