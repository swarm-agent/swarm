package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Purpose: separate workers cannot concurrently reuse the same account allowance,
// and unknown account-only pricing must reject without partial worker reservations.
// Owners CheckWorkerSessionBudgetWithPrice/accountBudgetReservationKey; real
// temporary Pebble is the narrowest atomic concurrency and policy authority layer.
func TestWorkerBudgetSharedAccountReservation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if err := db.PutJSON(KeyWorker("account-1", "other"), WorkerRecord{ID: "other", AccountScopeID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(usageBindingKey("account-1", "budget-two"), []UsageScopeTotal{{Kind: "worker", ID: "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "unknown", "unknown"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("unknown account-only pricing: %v", err)
	}
	var reservation workerBudgetReservation
	if found, err := db.GetJSON(accountBudgetReservationKey("account-1"), &reservation); err != nil || found {
		t.Fatalf("denied mutation: %v %v", found, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"budget-one", "budget-two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- s.CheckWorkerSessionBudgetWithPrice("account-1", id, "known", id)
		}(id)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrWorkerBudget) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("shared account winners: %d", wins)
	}
	policy, _, err := s.GetUsageLimit("account-1")
	if err != nil || policy.DailyCostLimitUSD != 1 {
		t.Fatalf("account policy changed: %+v %v", policy, err)
	}
}

// Purpose: late corrections to old operations must remain counted without
// releasing a current operation, including restart and UTC-day rollover.
// Owners setUsageDays/ReleaseWorkerBudgetReservation/GetWorkerBudgetStatus;
// canonical V3 receipts in a real store prove exact settlement postconditions.
func TestWorkerBudgetExactOperationSettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := workerBudgetFixture(t, db)
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 10, 1000); err != nil {
		t.Fatal(err)
	}
	record := func(operation, run string, tokens int64) {
		t.Helper()
		now := time.Now().UnixMilli()
		turn := SessionTurnUsageSnapshot{BudgetOperationID: operation, RunID: run, Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: tokens, BilledInputTokens: tokens, PriceStatus: "known", CreatedAt: now}
		if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "budget-one", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: fmt.Sprintf("%s:%s:%d", run, operation, tokens), PayloadHash: fmt.Sprintf("%s:%s:%d", run, operation, tokens), TurnUsage: &turn, NowUnixMs: now}); err != nil {
			t.Fatal(err)
		}
	}
	record("old", "old-run", 10)
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "new"); err != nil {
		t.Fatal(err)
	}
	record("old", "old-run", 20)
	if err := s.ReleaseWorkerBudgetReservation("account-1", "budget-one", "old"); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || !status.Blocked || !status.Inflight || status.Usage.TotalTokens != 20 {
		t.Fatalf("late receipt settled new operation: %+v %v", status, err)
	}
	// A previous UTC window is never an expiration of uncertain billable work.
	key := workerBudgetKey("account-1", "worker") + "/reservation"
	var r workerBudgetReservation
	if _, err := db.GetJSON(key, &r); err != nil {
		t.Fatal(err)
	}
	r.Date = "2000-01-01"
	if err := db.PutJSON(key, r); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("restart/rollover refunded: %v", err)
	}
	record("new", "new-run", 40)
	if err := s.ReleaseWorkerBudgetReservation("account-1", "budget-one", "old"); err != nil {
		t.Fatal(err)
	}
	status, err = s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || !status.Inflight {
		t.Fatalf("wrong terminal identity released: %+v %v", status, err)
	}
	if err := s.ReleaseWorkerBudgetReservation("account-1", "budget-one", "new"); err != nil {
		t.Fatal(err)
	}
	status, err = s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || status.Blocked || status.Inflight || status.Usage.TotalTokens != 60 || status.RemainingTokens == nil || *status.RemainingTokens != 940 {
		t.Fatalf("current settlement: %+v %v", status, err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "after-restart"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseWorkerBudgetReservation("account-1", "budget-one", "after-restart"); err != nil {
		t.Fatal(err)
	}
	record("after-restart", "late-current", 5)
	status, err = s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || status.Inflight || status.Usage.TotalTokens != 65 {
		t.Fatalf("terminal then receipt did not settle atomically: %+v %v", status, err)
	}
}

// Purpose: multi-owner reservations are all-or-nothing even when a later owner
// rejects; AI/user policy unsetting cannot forgive uncertain existing work.
// Owner CheckWorkerSessionBudgetWithPrice; real store proves durable absence
// of partial writes and preservation across a revision-guarded policy edit.
func TestWorkerBudgetAtomicOwnersAndPolicyEdit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if err := db.PutJSON(KeyWorker("account-1", "other"), WorkerRecord{ID: "other", AccountScopeID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 10, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "other", 0, 10, 1000); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(usageBindingKey("account-1", "budget-one"), []UsageScopeTotal{{Kind: "worker", ID: "worker"}, {Kind: "worker", ID: "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(workerBudgetKey("account-1", "other")+"/reservation", workerBudgetReservation{SessionID: "budget-two", OperationID: "older"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyTokensLimit: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "denied"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("multi-owner: %v", err)
	}
	for _, key := range []string{accountBudgetReservationKey("account-1"), workerBudgetKey("account-1", "worker") + "/reservation"} {
		var r workerBudgetReservation
		if found, err := db.GetJSON(key, &r); err != nil || found {
			t.Fatalf("partial reservation: %+v %v", r, err)
		}
	}
	if _, err := s.SetWorkerBudget("account-1", "other", 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "new"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("unset refunded: %v", err)
	}
	var r workerBudgetReservation
	if found, err := db.GetJSON(workerBudgetKey("account-1", "other")+"/reservation", &r); err != nil || !found || r.OperationID != "older" {
		t.Fatalf("policy erased inflight: %+v %v", r, err)
	}
}

// Purpose: unknown recorded account pricing is not a zero bill under an account
// USD cap even without any worker-specific cap. Owners canonical V3 usage
// accumulator and CheckWorkerSessionBudgetWithPrice; direct receipt mutation
// exercises accounting and rejection without a second ledger or price guesses.
func TestWorkerBudgetAccountUnknownRecordedReceipt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	now := time.Now().UnixMilli()
	turn := SessionTurnUsageSnapshot{RunID: "unknown", Provider: "fixture", Model: "unpriced", BilledUsagePresent: true, BilledTokens: 7, BilledInputTokens: 7, PriceStatus: "unknown", CreatedAt: now}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "budget-one", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: "unknown", PayloadHash: "unknown", TurnUsage: &turn, NowUnixMs: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-two", "known", "next"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("unknown receipt became free: %v", err)
	}
	usage, _, err := s.GetDailyUsageAccumulator("account-1", time.Now().UTC().Format("2006-01-02"))
	if err != nil || usage.UnknownReceipts != 1 || usage.TotalTokens != 7 {
		t.Fatalf("unknown evidence: %+v %v", usage, err)
	}
}

// Purpose: a policy CAS must durably invalidate precisely the account/user worker
// resource and stale edits must emit no event. Owners SetWorkerBudget and
// commitWorkerRealtime; indexed durable outbox is the narrowest replay proof.
func TestWorkerBudgetPolicyRealtime(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 100, "user-1"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-1", "user-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Event.EventType == WorkerUpdatedEventType {
			var payload WorkerRealtimePayload
			if err := json.Unmarshal(row.Event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.WorkerID != "worker" || payload.BudgetRevision != 1 || row.AccountScopeID != "account-1" || row.UserID != "user-1" {
				t.Fatalf("invalidation scope: %+v %+v", row, payload)
			}
			count++
		}
	}
	if count != 1 {
		t.Fatalf("policy events: %d", count)
	}
	foreign, err := s.ListV3RealtimeOutboxForAuthScopeAfter("other-account", "user-1", 0, 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign invalidation: %+v %v", foreign, err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 0, 0, "user-1"); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale: %v", err)
	}
	after, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-1", "user-1", 0, 10)
	if err != nil || len(after) != len(rows) {
		t.Fatalf("stale emitted event: %d %d %v", len(rows), len(after), err)
	}
}
