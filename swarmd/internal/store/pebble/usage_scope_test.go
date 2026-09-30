package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Purpose: cumulative task billing must survive replay, corrected receipts and
// restart without summing context occupancy or leaking another account. Owners:
// ApplyV3SessionMutation, prepareUsageScopeTotals and GetUsageScope. A temporary
// Pebble store is the narrowest layer proving atomic durable event/projection state.
func TestUsageScopeV3ReplacementReplayRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.pebble")
	db, err := Open(path)
	if err != nil { t.Fatal(err) }
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "scope-session")
	snapshot, _, err := s.GetSession("scope-session")
	if err != nil { t.Fatal(err) }
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil { t.Fatal(err) }
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil { t.Fatal(err) }
	apply := func(key string, tokens int64, status string, cost float64) V3SessionMutationResult {
		t.Helper()
		turn := SessionTurnUsageSnapshot{RunID: "receipt", Provider: "fixture-provider", Model: "fixture-model", TotalTokens: 5, BilledTokens: tokens, EstimatedCostUSD: cost, PriceStatus: status}
		result, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: snapshot.ID, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: key, PayloadHash: key, TurnUsage: &turn, NowUnixMs: 3000})
		if err != nil { t.Fatal(err) }
		return result
	}
	apply("first", 100, "unknown", 1)
	apply("first", 100, "unknown", 1)
	result := apply("correction", 60, "known", 2)
	total, found, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || !found || total.TotalTokens != 60 || total.ReceiptCount != 1 || total.UnknownReceipts != 0 || total.CatalogCostUSD != 2 || total.HistoryComplete { t.Fatalf("incorrect scope: %+v %v", total, err) }
	if result.UsageSummary == nil || result.UsageSummary.TotalTokens != 5 { t.Fatalf("context summary changed: %+v", result.UsageSummary) }
	if result.RealtimeOutbox == nil { t.Fatal("missing durable outbox") }
	var payload struct { TurnUsage *SessionTurnUsageSnapshot `json:"turn_usage"` }
	if err := json.Unmarshal(result.RealtimeOutbox.Event.Payload, &payload); err != nil { t.Fatal(err) }
	if payload.TurnUsage == nil || len(payload.TurnUsage.ScopeTotals) != 1 || payload.TurnUsage.ScopeTotals[0].TotalTokens != 60 { t.Fatalf("event missing scope projection: %+v", payload) }
	if _, found, err := s.GetUsageScope("other-account", "task", "project", "task"); err != nil || found { t.Fatalf("cross-account projection: %v %v", found, err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s = NewSessionStore(db)
	apply("correction", 60, "known", 2)
	restarted, _, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || restarted != total { t.Fatalf("restart changed projection: %+v %v", restarted, err) }
}

// Purpose: concurrent receipt writes for a shared task cannot lose deltas, and
// account reassignment must fail without changing either ledger or projection.
// Owners: PutTurnUsage account lock and receipt ownership check. Temporary store
// concurrency is the smallest deterministic layer exercising the actual lock.
func TestUsageScopeConcurrentReceiptsAndAccountRejection(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "scope-concurrent")
	snapshot, _, _ := s.GetSession("scope-concurrent")
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil { t.Fatal(err) }
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil { t.Fatal(err) }
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs <- s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: snapshot.ID, AccountScopeID: "account-1", RunID: fmt.Sprint(i), TotalTokens: 10, PriceStatus: "free", Provider: "codex"}) }(i)
	}
	wg.Wait(); close(errs)
	for err := range errs { if err != nil { t.Fatal(err) } }
	total, _, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || total.TotalTokens != 80 || total.FreeReceipts != 8 || total.ReceiptCount != 8 { t.Fatalf("lost concurrent usage: %+v %v", total, err) }
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: snapshot.ID, AccountScopeID: "other-account", RunID: "0", TotalTokens: 1000}); err == nil { t.Fatal("account reassignment accepted") }
	after, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if after != total { t.Fatalf("rejected write changed scope: %+v", after) }
	receipt, _, _ := s.GetTurnUsage(snapshot.ID, "0")
	if receipt.AccountScopeID != "account-1" || receipt.TotalTokens != 10 { t.Fatalf("rejected write changed receipt: %+v", receipt) }
}

// Purpose: metadata alone cannot charge a worker/task; durable run and historical
// attempt linkage must corroborate it. resolveUsageScopes is the owning boundary;
// direct temporary store records isolate attribution from executor deployment.
func TestUsageScopeHistoricalAttemptAndWorkerOwnership(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "scope-old-attempt")
	snapshot, _, _ := s.GetSession("scope-old-attempt")
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task", "worker_id": "worker", "worker_run_id": "worker-run"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil { t.Fatal(err) }
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "new-attempt", Attempts: []ProjectTaskAttempt{{ID: "old", SessionID: snapshot.ID}}}); err != nil { t.Fatal(err) }
	if err := db.PutJSON(KeyWorkerRun("account-1", "worker", "worker-run"), WorkerRunRecord{ID: "worker-run", AccountScopeID: "account-1", WorkerID: "worker", SessionID: snapshot.ID}); err != nil { t.Fatal(err) }
	scopes, err := s.resolveUsageScopes("account-1", snapshot.ID)
	if err != nil || len(scopes) != 3 { t.Fatalf("missing durable historical scopes: %+v %v", scopes, err) }
	if err := db.PutJSON(KeyWorkerRun("account-1", "worker", "worker-run"), WorkerRunRecord{ID: "worker-run", AccountScopeID: "account-1", WorkerID: "worker", SessionID: "unrelated-session"}); err != nil { t.Fatal(err) }
	scopes, err = s.resolveUsageScopes("account-1", snapshot.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Kind != "task" { t.Fatalf("metadata impersonated worker scope: %+v %v", scopes, err) }
	if _, err := s.resolveUsageScopes("other-account", snapshot.ID); err == nil { t.Fatal("foreign session lineage accepted") }
}
