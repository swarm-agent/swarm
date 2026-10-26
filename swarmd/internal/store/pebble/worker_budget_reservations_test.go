package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Purpose: CheckWorkerMediaAdmission must sum operation estimates atomically,
// allowing nested media under a subscription turn without overspending. Real
// temporary Pebble is the narrowest layer proving concurrency, scoped release,
// unknown-pricing refusal, and crash expiry without provider dependencies.
// GetWorkerBudgetStatus must also report zero-cost open operations and exclude
// released/stale operations in both worker and account in-flight fields.
func TestWorkerBudgetNestedReservations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, ws := NewSessionStore(db), NewWorkerStore(db)
	w, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Name: "worker", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, Budget: &WorkerBudgetRequest{DailyUSD: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("acct", "user", w.ID, w.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: "first", Input: map[string]any{"prompt": "Review"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(SessionSnapshot{ID: r.SessionID, AccountScopeID: "acct", UserID: "user", Metadata: map[string]any{"worker_id": w.ID, "worker_run_id": r.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "acct", Enabled: true, DailyCostLimitUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture", "turn"); err != nil {
		t.Fatal(err)
	}
	assertInflight := func(want bool) {
		t.Helper()
		status, err := s.GetWorkerBudgetStatus("acct", w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Inflight != want || status.AccountInflight != want {
			t.Fatalf("inflight=%v account_inflight=%v, want %v", status.Inflight, status.AccountInflight, want)
		}
	}
	assertInflight(true) // The subscription turn reserves zero dollars.
	if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "turn"); err != nil {
		t.Fatal(err)
	}
	assertInflight(false)
	if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture", "turn"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"media-a", "media-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{CostUSD: .4, PriceStatus: "known"}, id)
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{CostUSD: .3, PriceStatus: "known"}, "over"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("over budget admitted: %v", err)
	}
	if err := s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{PriceStatus: "unknown"}, "unknown"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("unknown admitted: %v", err)
	}
	// A failed operation has no receipt; releasing it must not release its sibling.
	if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "media-a"); err != nil {
		t.Fatal(err)
	}
	base := workerBudgetKey("acct", w.ID) + "/reservation"
	cost, err := s.openWorkerBudgetCost(base)
	if err != nil || cost != .4 {
		t.Fatalf("release cost=%v err=%v", cost, err)
	}
	var reservation workerBudgetReservation
	if found, err := db.GetJSON(budgetOperationKey(base, r.SessionID, "over"), &reservation); err != nil || found {
		t.Fatalf("refused operation persisted: %v %v", found, err)
	}
	assertInflight(true) // Releasing one operation must not hide its siblings.
	if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "turn"); err != nil {
		t.Fatal(err)
	}
	key := budgetOperationKey(base, r.SessionID, "media-b")
	if found, err := db.GetJSON(key, &reservation); err != nil || !found {
		t.Fatalf("missing sibling: %v %v", found, err)
	}
	reservation.CreatedAt = time.Now().Add(-2 * time.Hour).UnixMilli()
	if err := db.PutJSON(key, reservation); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(budgetOperationKey(accountBudgetReservationKey("acct"), r.SessionID, "media-b"), reservation); err != nil {
		t.Fatal(err)
	}
	assertInflight(false) // Expiry is visible even before admission prunes the records.
	if err := s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{CostUSD: 1, PriceStatus: "known"}, "after-crash"); err != nil {
		t.Fatal(err)
	}
	if found, err := db.GetJSON(key, &reservation); err != nil || found {
		t.Fatalf("stale reservation retained: %v %v", found, err)
	}
}
