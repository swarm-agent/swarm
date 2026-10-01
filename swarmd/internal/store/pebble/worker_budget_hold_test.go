package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

// Purpose: user-only SetWorkerBudget CAS may not exceed an enabled account
// ceiling or mutate policy on rejection. The real store is the narrowest layer
// owning policy, account isolation and lowered-account effective enforcement.
func TestWorkerBudgetCeilingAndEffectiveStatus(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 2, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		cost   float64
		tokens int64
	}{{3, 50}, {1, 101}} {
		if _, err := s.SetWorkerBudget("account-1", "worker", 0, input.cost, input.tokens); err == nil {
			t.Fatal("accepted above account ceiling")
		}
		p, err := s.GetWorkerBudget("account-1", "worker")
		if err != nil || p.Revision != 0 {
			t.Fatalf("rejection mutated policy: %+v %v", p, err)
		}
	}
	p, err := s.SetWorkerBudget("account-1", "worker", 0, 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 1, DailyTokensLimit: 50}); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || status.EffectiveCostLimitUSD == nil || *status.EffectiveCostLimitUSD != 1 || status.EffectiveTokensLimit == nil || *status.EffectiveTokensLimit != 50 {
		t.Fatalf("effective: %+v %v", status, err)
	}
	got, err := s.GetWorkerBudget("account-1", "worker")
	if err != nil || got != p {
		t.Fatalf("lowering account rewrote worker: %+v %v", got, err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 50); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale: %v", err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	status, err = s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || status.EffectiveCostLimitUSD != nil || status.EffectiveTokensLimit != nil || status.Blocked {
		t.Fatalf("unset is not zero allowance: %+v %v", status, err)
	}
}

// Purpose: genuine receipt settlement must atomically stop worker work and
// persist one account-scoped alert despite concurrency/restart. Admission and
// receipt store own the invariant; temp Pebble plus actual V3 mutations proves
// postconditions without provider calls or fabricated telemetry.
func TestWorkerBudgetHoldReceiptRestartAndDuplicateAlert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := workerBudgetFixture(t, db)
	if _, err := NewSwarmStore(db).PutLocalNode(SwarmLocalNodeRecord{SwarmID: "fixture-swarm", Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 100); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	turn := SessionTurnUsageSnapshot{RunID: "exhaustion", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 100, PriceStatus: "known", EstimatedCostUSD: 1, CreatedAt: now}
	input := V3SessionMutationInput{SessionID: "budget-one", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: "exhaustion", PayloadHash: "exhaustion", TurnUsage: &turn, NowUnixMs: now}
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	date := time.UnixMilli(now).UTC().Format("2006-01-02")
	var hold WorkerBudgetHold
	if found, err := db.GetJSON(workerBudgetHoldKey("account-1", "worker", date), &hold); err != nil || !found || hold.Reason != "daily_budget_exhausted" || hold.CapSource != "worker" {
		t.Fatalf("settlement hold: %+v %v %v", hold, found, err)
	}
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.CheckWorkerBudgetAdmission("account-1", "worker") }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, ErrWorkerBudget) {
			t.Fatalf("hold bypass: %v", err)
		}
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
	status, err := s.GetWorkerBudgetStatus("account-1", "worker")
	if err != nil || status.Hold == nil || !status.Blocked || *status.Hold != hold {
		t.Fatalf("restart: %+v %v", status, err)
	}
	ns := NewNotificationStore(db)
	notices, err := ns.ListNotificationsForAccount("account-1", "fixture-swarm", 10)
	if err != nil || len(notices) != 1 || notices[0].WorkerID != "worker" {
		t.Fatalf("one alert: %+v %v", notices, err)
	}
	foreign, err := ns.ListNotificationsForAccount("other-account", "fixture-swarm", 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign alert leak: %+v %v", foreign, err)
	}
	if _, err := ns.DeleteNotificationsForAccount("account-1", "fixture-swarm"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatal(err)
	}
	notices, err = ns.ListNotificationsForAccount("account-1", "fixture-swarm", 10)
	if err != nil || len(notices) != 0 {
		t.Fatalf("cleared alert recreated: %+v %v", notices, err)
	}
}

// Purpose: holds apply only to their UTC day, never altering manual lifecycle,
// pending review or disabled automation. Unknown price/reservations must not
// emit exhaustion alerts. checkWorkerBudgetLocked is the narrowest clock-
// independent domain layer; no sleeps or scheduler catch-up are needed.
func TestWorkerBudgetHoldUTCRolloverAndNonExhaustion(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	w := WorkerRecord{ID: "worker", AccountScopeID: "account-1", LifecycleState: WorkerLifecycleStatePaused, Automations: []WorkerAutomationDefinition{{ID: "disabled", Enabled: false}}}
	if err := db.PutJSON(KeyWorker("account-1", "worker"), w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 100); err != nil {
		t.Fatal(err)
	}
	total := UsageScopeTotal{Kind: "worker", ID: "worker", CatalogCostUSD: 1, TotalTokens: 100}
	if err := db.PutJSON(usageScopeDayKey("account-1", total, "2026-01-01"), total); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.checkWorkerBudgetLocked("account-1", "worker", "2026-01-01"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatal(err)
	}
	if _, _, err := s.checkWorkerBudgetLocked("account-1", "worker", "2026-01-02"); err != nil {
		t.Fatalf("rollover held: %v", err)
	}
	for _, state := range []WorkerLifecycleState{WorkerLifecycleStatePaused, WorkerLifecycleStateArchived, WorkerLifecycleStatePending, WorkerLifecycleStateIdle, WorkerLifecycleStateActive} {
		w.LifecycleState = state
		if err := db.PutJSON(KeyWorker("account-1", "worker"), w); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.checkWorkerBudgetLocked("account-1", "worker", "2026-01-02"); err != nil {
			t.Fatalf("rollover budget for %s: %v", state, err)
		}
		got, _, err := s.WorkerStore().GetWorker("account-1", "worker")
		if err != nil || got.LifecycleState != state || got.Revision != w.Revision || got.Automations[0].Enabled {
			t.Fatalf("rollover changed lifecycle/disabled automation: %+v %v", got, err)
		}
	}
	if err := db.PutJSON(workerBudgetKey("account-1", "worker")+"/reservation", workerBudgetReservation{SessionID: "budget-one", Date: "2026-01-01"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("outstanding reservation admitted: %v", err)
	}
	var reservationHold WorkerBudgetHold
	if found, err := db.GetJSON(workerBudgetHoldKey("account-1", "worker", time.Now().UTC().Format("2006-01-02")), &reservationHold); err != nil || found {
		t.Fatalf("reservation became daily exhaustion: %+v %v", reservationHold, err)
	}
	total.CatalogCostUSD, total.TotalTokens, total.UnknownReceipts = 0, 0, 1
	if err := db.PutJSON(usageScopeDayKey("account-1", total, "2026-01-02"), total); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.checkWorkerBudgetLocked("account-1", "worker", "2026-01-02"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatal(err)
	}
	var hold WorkerBudgetHold
	if found, err := db.GetJSON(workerBudgetHoldKey("account-1", "worker", "2026-01-02"), &hold); err != nil || found {
		t.Fatalf("unknown pricing became exhaustion: %+v %v", hold, err)
	}
}

// Purpose: account exhaustion atomically holds every account worker at receipt
// settlement, but an abandoned batch has no hold or notification side effects.
// setAccountWorkerBudgetHolds participates in the canonical receipt batch;
// direct batch abort/commit is the narrowest failure-injection boundary.
func TestWorkerBudgetAccountHoldAtomicAbort(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if _, err := NewSwarmStore(db).PutLocalNode(SwarmLocalNodeRecord{SwarmID: "fixture-swarm", Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyWorker("other-account", "worker"), WorkerRecord{ID: "worker", AccountScopeID: "other-account"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyTokensLimit: 10}); err != nil {
		t.Fatal(err)
	}
	acc := DailyUsageAccumulator{AccountScopeID: "account-1", Date: "2026-01-01", TotalTokens: 10}
	batch := db.NewBatch()
	if err := s.setAccountWorkerBudgetHolds(batch, acc); err != nil {
		t.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	var hold WorkerBudgetHold
	if found, err := db.GetJSON(workerBudgetHoldKey("account-1", "worker", acc.Date), &hold); err != nil || found {
		t.Fatalf("aborted hold: %+v %v", hold, err)
	}
	notices, err := NewNotificationStore(db).ListNotificationsForAccount("account-1", "fixture-swarm", 10)
	if err != nil || len(notices) != 0 {
		t.Fatalf("aborted alert: %+v %v", notices, err)
	}
	batch = db.NewBatch()
	defer batch.Close()
	if err := s.setAccountWorkerBudgetHolds(batch, acc); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if found, err := db.GetJSON(workerBudgetHoldKey("account-1", "worker", acc.Date), &hold); err != nil || !found || hold.CapSource != "account" || hold.Dimension != "tokens" || hold.Usage != 10 || hold.Limit != 10 {
		t.Fatalf("account hold: %+v %v", hold, err)
	}
	if found, err := db.GetJSON(workerBudgetHoldKey("other-account", "worker", acc.Date), &hold); err != nil || found {
		t.Fatalf("foreign hold: %+v %v", hold, err)
	}
}

// Purpose: scheduler status reads can create an exhaustion hold after a lowered
// ceiling; notification publication must happen after unlocking account state,
// with stable notice identity on repeated ticks. GetWorkerBudgetStatus is the
// narrowest production boundary; a reentrant policy read detects lock inversion.
func TestWorkerBudgetStatusPublishesAfterUnlock(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := workerBudgetFixture(t, db)
	if _, err := NewSwarmStore(db).PutLocalNode(SwarmLocalNodeRecord{SwarmID: "fixture-swarm", Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyTokensLimit: 1}); err != nil {
		t.Fatal(err)
	}
	date := time.Now().UTC().Format("2006-01-02")
	if err := s.PutDailyUsageAccumulator(DailyUsageAccumulator{AccountScopeID: "account-1", Date: date, TotalTokens: 1}); err != nil {
		t.Fatal(err)
	}
	var first NotificationRecord
	calls := 0
	db.SetWorkerBudgetPublisher(func(n NotificationRecord) {
		// This mutation acquires the same account lock and must not deadlock.
		if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyTokensLimit: 1}); err != nil {
			t.Fatal(err)
		}
		if calls == 0 {
			first = n
		} else if n.ID != first.ID || n.UpdatedAt != first.UpdatedAt {
			t.Fatal("unstable notice identity")
		}
		calls++
	})
	for i := 0; i < 2; i++ {
		status, err := s.GetWorkerBudgetStatus("account-1", "worker")
		if err != nil || status.Hold == nil || !status.Blocked {
			t.Fatalf("status hold: %+v %v", status, err)
		}
	}
	if calls != 2 {
		t.Fatalf("missing publication accelerator: %d", calls)
	}
	notices, err := NewNotificationStore(db).ListNotificationsForAccount("account-1", "fixture-swarm", 10)
	if err != nil || len(notices) != 1 {
		t.Fatalf("duplicate notices: %+v %v", notices, err)
	}
}
