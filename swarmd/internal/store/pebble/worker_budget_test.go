package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func workerBudgetFixture(t *testing.T, db *Store) *SessionStore {
	t.Helper()
	s := NewSessionStore(db)
	if err := db.PutJSON(KeyWorker("account-1", "worker"), WorkerRecord{ID: "worker", AccountScopeID: "account-1"}); err != nil { t.Fatal(err) }
	for _, id := range []string{"budget-one", "budget-two"} {
		createV3SessionForTest(t, s, id)
		if err := db.PutJSON(usageBindingKey("account-1", id), []UsageScopeTotal{{Kind: "worker", ID: "worker"}}); err != nil { t.Fatal(err) }
	}
	return s
}

// Purpose: worker policy CAS must preserve account isolation and never reset
// usage; owner SetWorkerBudget/GetWorkerBudget and account mutation lock. A
// temporary Pebble store is the narrowest durable policy/concurrency layer.
func TestWorkerBudgetPolicyRevisionIsolation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil { t.Fatal(err) }; defer db.Close()
	s := workerBudgetFixture(t, db)
	policy, err := s.GetWorkerBudget("account-1", "worker")
	if err != nil || policy.Revision != 0 || policy.DailyCostLimitUSD != 0 || policy.DailyTokensLimit != 0 { t.Fatalf("defaults: %+v %v", policy, err) }
	if _, err := s.SetWorkerBudget("other-account", "worker", 0, 1, 1); !errors.Is(err, ErrWorkerNotFound) { t.Fatalf("foreign: %v", err) }
	policy, err = s.SetWorkerBudget("account-1", "worker", 0, 2, 100)
	if err != nil || policy.Revision != 1 { t.Fatalf("set: %+v %v", policy, err) }
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 0, 0); !errors.Is(err, ErrWorkerConflict) { t.Fatalf("stale: %v", err) }
	got, err := s.GetWorkerBudget("account-1", "worker")
	if err != nil || got != policy { t.Fatalf("stale changed policy: %+v %v", got, err) }
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ { wg.Add(1); go func() { defer wg.Done(); _, err := s.SetWorkerBudget("account-1", "worker", 1, 3, 200); results <- err }() }
	wg.Wait(); close(results)
	wins := 0
	for err := range results { if err == nil { wins++ } else if !errors.Is(err, ErrWorkerConflict) { t.Fatal(err) } }
	if wins != 1 { t.Fatalf("CAS winners: %d", wins) }
}

// Purpose: canonical worker daily totals and exclusive reservations must reject
// simultaneous spend, survive restart/cancel and retain late genuine receipts.
// Owners CheckWorkerSessionBudgetWithPrice, ReleaseWorkerBudgetReservation,
// ApplyV3SessionMutation/setUsageDays. Real temp storage proves durable atomic
// postconditions without providers or simulated telemetry.
func TestWorkerBudgetConcurrentRestartLateReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.pebble")
	db, err := Open(path)
	if err != nil { t.Fatal(err) }
	s := workerBudgetFixture(t, db)
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 100); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "unknown"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("unknown price: %v", err) }
	var wg sync.WaitGroup
	type result struct { id string; err error }
	results := make(chan result, 2)
	for _, id := range []string{"budget-one", "budget-two"} { wg.Add(1); go func(id string) { defer wg.Done(); results <- result{id, s.CheckWorkerSessionBudgetWithPrice("account-1", id, "known")} }(id) }
	wg.Wait(); close(results)
	winner, loser := "", ""
	for r := range results { if r.err == nil { winner = r.id } else if errors.Is(r.err, ErrWorkerBudget) { loser = r.id } else { t.Fatal(r.err) } }
	if winner == "" || loser == "" { t.Fatalf("exclusive reservation: %q %q", winner, loser) }
	// A cancelled call with no receipt is not silently refunded.
	if err := s.ReleaseWorkerBudgetReservation("account-1", winner); err != nil { t.Fatal(err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path)
	if err != nil { t.Fatal(err) }; defer db.Close()
	s = NewSessionStore(db)
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", loser, "known"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("restart reservation: %v", err) }
	now := time.Now().UnixMilli()
	turn := SessionTurnUsageSnapshot{RunID: "late-receipt", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 100, PriceStatus: "known", EstimatedCostUSD: 1, CreatedAt: now}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: winner, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: "late", PayloadHash: "late", TurnUsage: &turn, NowUnixMs: now}); err != nil { t.Fatal(err) }
	if err := s.ReleaseWorkerBudgetReservation("account-1", winner); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", loser, "known"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("late cost/token cap: %v", err) }
	if _, err := s.SetWorkerBudget("account-1", "worker", 1, 0, 0); err != nil { t.Fatal(err) }
	total, found, err := s.GetUsageScopeDay("account-1", "worker", "", "worker", time.Now().UTC().Format("2006-01-02"))
	if err != nil || !found || total.TotalTokens != 100 || total.CatalogCostUSD != 1 { t.Fatalf("unsetting erased receipts: %+v %v", total, err) }
}

// Purpose: worker allowances cannot override account token/cost limits, and
// unknown recorded price cannot become free. Owners canonical day aggregates
// and checkWorkerBudgetLocked; direct store assertions isolate enforcement.
func TestWorkerBudgetAccountAndUnknownReceipt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil { t.Fatal(err) }; defer db.Close()
	s := workerBudgetFixture(t, db)
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 10, 1000); err != nil { t.Fatal(err) }
	date := time.Now().UTC().Format("2006-01-02")
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyTokensLimit: 10}); err != nil { t.Fatal(err) }
	if err := s.PutDailyUsageAccumulator(DailyUsageAccumulator{AccountScopeID: "account-1", Date: date, TotalTokens: 10}); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("account token limit: %v", err) }
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1", Enabled: true, DailyCostLimitUSD: 1}); err != nil { t.Fatal(err) }
	if err := s.PutDailyUsageAccumulator(DailyUsageAccumulator{AccountScopeID: "account-1", Date: date, TotalCostUSD: 1}); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("account cost limit: %v", err) }
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "account-1"}); err != nil { t.Fatal(err) }
	total := UsageScopeTotal{Kind: "worker", ID: "worker", UnknownReceipts: 1, Coverage: "observed_receipts_only"}
	if err := db.PutJSON(usageScopeDayKey("account-1", total, date), total); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerBudgetAdmission("account-1", "worker"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("unknown counted free: %v", err) }
}

// Purpose: forged metadata must not grant lineage or evade a real delegated
// parent's budget. Owner resolveUsageScopes/CheckWorkerUnmeteredOperation;
// store fixtures exercise existing corroborated regular task-launch authority.
func TestWorkerBudgetDescendantLineage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil { t.Fatal(err) }; defer db.Close()
	s := workerBudgetFixture(t, db)
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 1, 0); err != nil { t.Fatal(err) }
	createV3SessionForTest(t, s, "budget-child")
	child, _, err := s.GetSession("budget-child")
	if err != nil { t.Fatal(err) }
	child.Metadata = map[string]any{"parent_session_id": "budget-one", "parent_task_call_id": "launch"}
	if err := db.PutJSON(KeySession(child.ID), child); err != nil { t.Fatal(err) }
	// An uncorroborated parent name cannot charge or reserve its worker.
	if err := s.CheckWorkerUnmeteredOperation("account-1", child.ID); err != nil { t.Fatalf("forged lineage: %v", err) }
	parent, _, err := s.GetSession("budget-one")
	if err != nil { t.Fatal(err) }
	parent.Metadata = map[string]any{"task_launches": map[string]any{"launch": map[string]any{"launches": []any{map[string]any{"child_session_id": child.ID}}}}}
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerUnmeteredOperation("account-1", child.ID); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("descendant internal call: %v", err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", child.ID, "unknown"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("descendant unknown price: %v", err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("other-account", child.ID, "known"); err == nil { t.Fatal("cross-account descendant accepted") }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", child.ID, "known"); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", parent.ID, "known"); !errors.Is(err, ErrWorkerBudget) { t.Fatalf("parent bypassed child reservation: %v", err) }
}

// Purpose: admission of direct/scheduled/trigger work must reject exhausted
// limits without creating a run receipt or idempotency record; settlement of a
// genuinely metered completed operation may unblock another session. Owners
// AdmitWorkerRun and ReleaseWorkerBudgetReservation; store layer proves durable
// postconditions independently of transport and scheduler wake-up behavior.
func TestWorkerBudgetAdmissionAndSettlement(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil { t.Fatal(err) }; defer db.Close()
	s := workerBudgetFixture(t, db)
	worker, _, err := s.WorkerStore().GetWorker("account-1", "worker")
	if err != nil { t.Fatal(err) }
	worker.LifecycleState = WorkerLifecycleStateActive
	worker.Revision = 1
	worker.WorkspaceRequirements = []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}
	worker.LocalBindings = map[string]string{"primary": "workspace"}
	if err := db.PutJSON(KeyWorker("account-1", "worker"), worker); err != nil { t.Fatal(err) }
	if _, err := s.SetWorkerBudget("account-1", "worker", 0, 2, 1000); err != nil { t.Fatal(err) }
	date := time.Now().UTC().Format("2006-01-02")
	total := UsageScopeTotal{Kind: "worker", ID: "worker", TotalTokens: 1000}
	if err := db.PutJSON(usageScopeDayKey("account-1", total, date), total); err != nil { t.Fatal(err) }
	_, err = s.WorkerStore().AdmitWorkerRun("account-1", WorkerRunAdmission{WorkerID: "worker", UserID: "user-1", ExpectedWorkerRevision: 1, RequestSource: "direct", IdempotencyKey: "budget-denied", Input: map[string]any{"prompt": "work"}})
	if !errors.Is(err, ErrWorkerBudget) { t.Fatalf("admission: %v", err) }
	var prior workerRunIdempotency
	if found, err := db.GetJSON(KeyWorkerRunIdempotency("account-1", "budget-denied"), &prior); err != nil || found { t.Fatalf("denied run persisted: %+v %v", prior, err) }
	// Clear only fixture projection, then persist one real canonical receipt.
	if err := db.PutJSON(usageScopeDayKey("account-1", total, date), UsageScopeTotal{Kind: "worker", ID: "worker"}); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-one", "known"); err != nil { t.Fatal(err) }
	now := time.Now().UnixMilli()
	turn := SessionTurnUsageSnapshot{RunID: "settled", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 10, BilledInputTokens: 10, PriceStatus: "known", EstimatedCostUSD: 0.25, CreatedAt: now}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "budget-one", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: "settled", PayloadHash: "settled", TurnUsage: &turn, NowUnixMs: now}); err != nil { t.Fatal(err) }
	if err := s.ReleaseWorkerBudgetReservation("account-1", "budget-one"); err != nil { t.Fatal(err) }
	if err := s.CheckWorkerSessionBudgetWithPrice("account-1", "budget-two", "known"); err != nil { t.Fatalf("settlement did not unblock next call: %v", err) }
}
