package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: SetWorkerBudget must publish after releasing the account lock so a
// subscriber can reenter canonical status. The exact policy exception must not
// authorize foreign policy or reservation writes. Real Pebble and the publisher
// seam are the narrowest persistence and lock-order proof (no live telemetry).
func TestWorkerBudgetPublicationAndExactMutationScope(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	published := make(chan error, 1)
	db.SetWorkerPublisher(func(V3RealtimeOutboxRecord) {
		_, err := s.GetWorkerBudgetStatus("account-1", "worker")
		published <- err
	})
	done := make(chan error, 1)
	go func() { _, err := s.SetWorkerBudget("account-1", "worker", 0, 0, 100); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher deadlocked on account status")
	}
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publication absent")
	}
	for _, key := range []string{workerBudgetKey("account-1", "foreign"), workerBudgetKey("foreign", "worker"), workerBudgetKey("account-1", "worker") + "/reservation", accountBudgetReservationKey("account-1")} {
		m := &workerRealtimeMutation{accountScopeID: "account-1", workerID: "worker"}
		if err := m.put(key, map[string]any{"invalid": true}); err != nil {
			t.Fatal(err)
		}
		batch := db.NewBatch()
		err := setWorkerRealtimeMutationInBatch(batch, "account-1", m)
		batch.Close()
		if !errors.Is(err, ErrWorkerInvalid) {
			t.Fatalf("foreign write %s: %v", key, err)
		}
		var data map[string]any
		if found, err := db.GetJSON(key, &data); err != nil || found {
			t.Fatalf("rejected write persisted %s: %v %v", key, found, err)
		}
	}
}

// Purpose: a legacy accumulator without explicit pricing coverage cannot prove
// that old unknown receipts were free. GetDailyUsageAccumulator and budget
// admission must carry uncertainty across incremental new receipts. A raw
// legacy record and canonical V3 receipt prove this at the narrow store layer.
func TestWorkerBudgetLegacyPricingCoverage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	date := time.Now().UTC().Format("2006-01-02")
	if err := db.PutJSON(KeyDailyUsageAccumulator("account-1", date), map[string]any{"account_scope_id": "account-1", "date": date, "total_tokens": 7, "turn_count": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "denied"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("legacy admitted: %v", err)
	}
	var reservation workerBudgetReservation
	if found, err := db.GetJSON(accountBudgetReservationKey("account-1"), &reservation); err != nil || found {
		t.Fatalf("legacy denial reserved: %v %v", found, err)
	}
	now := time.Now().UnixMilli()
	turn := SessionTurnUsageSnapshot{RunID: "new-known", Provider: "fixture", Model: "fixture", PriceStatus: "known", BilledUsagePresent: true, BilledTokens: 3, BilledInputTokens: 3, CreatedAt: now}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "budget-one", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: "new-known", PayloadHash: "new-known", TurnUsage: &turn, NowUnixMs: now}); err != nil {
		t.Fatal(err)
	}
	acc, _, err := s.GetDailyUsageAccumulator("account-1", date)
	if err != nil || !acc.PricingCoverageIncomplete || acc.TotalTokens != 10 {
		t.Fatalf("legacy uncertainty erased: %+v %v", acc, err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known", "still-denied"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("new receipt certified old pricing: %v", err)
	}
	status, err := s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || !status.Blocked || status.AccountCoverage != "legacy_pricing_incomplete" {
		t.Fatalf("coverage status: %+v %v", status, err)
	}
}

// Purpose: policy revision wraparound must reject without resetting the CAS
// authority. SetWorkerBudget's real store boundary is the narrowest proof.
func TestWorkerBudgetRevisionOverflow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	policy := WorkerBudgetPolicy{AccountScopeID: "account-1", WorkerID: "worker", Revision: ^uint64(0), DailyTokensLimit: 100}
	if err := db.PutJSON(workerBudgetKey("account-1", "worker"), policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", policy.Revision, 0, 0); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("overflow accepted: %v", err)
	}
	after, err := s.GetWorkerBudget("account-1", "worker")
	if err != nil || after != policy {
		t.Fatalf("overflow changed policy: %+v %v", after, err)
	}
}
