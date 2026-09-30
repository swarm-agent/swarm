package pebblestore

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// Purpose: compaction must reset only context occupancy, preserving durable
// receipts and their billing/index/attribution state so replay and late upward
// or downward corrections replace one charge, including after restart. Owners:
// SessionStore.ResetUsage, ApplyV3SessionMutation and RepairUsageScopes. A real
// temporary store is the narrowest layer proving persisted postconditions and
// foreign-principal rejection without providers or worker execution.
func TestUsageScopeResetPreservesBillingReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-reset.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "reset-context")
	snapshot, found, err := s.GetSession("reset-context")
	if err != nil || !found {
		t.Fatalf("session: %+v %v", snapshot, err)
	}
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task", "worker_id": "worker", "worker_run_id": "worker-run"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyWorkerRun("account-1", "worker", "worker-run"), WorkerRunRecord{ID: "worker-run", WorkerID: "worker", AccountScopeID: "account-1", SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	turn := SessionTurnUsageSnapshot{RunID: "receipt", Provider: "fixture", Model: "fixture", Source: "provider", ContextWindow: 1000, InputTokens: 800, OutputTokens: 50, ThinkingTokens: 20, CacheReadTokens: 10, CacheWriteTokens: 5, TotalTokens: 850, BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 100, PriceStatus: "known", CostProvenance: "provider", EstimatedCostUSD: 1}
	record := func(key string, tokens int64) {
		t.Helper()
		turn.BilledTokens = tokens
		turn.BilledInputTokens = tokens
		turn.EstimatedCostUSD = float64(tokens) / 100
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: snapshot.ID, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, IdempotencyKey: key, PayloadHash: key, TurnUsage: &turn, NowUnixMs: 3000})
		if err != nil {
			t.Fatal(err)
		}
	}
	scopes := []UsageScopeTotal{{Kind: "task", ProjectID: "project", ID: "task"}, {Kind: "worker", ID: "worker"}, {Kind: "worker_run", ProjectID: "worker", ID: "worker-run"}}
	assertBills := func(tokens int64) {
		t.Helper()
		cost := float64(tokens) / 100
		for _, scope := range scopes {
			total, found, err := s.GetUsageScope("account-1", scope.Kind, scope.ProjectID, scope.ID)
			if err != nil || !found || total.TotalTokens != tokens || total.InputTokens != tokens || total.ProviderCostUSD != cost || total.ReceiptCount != 1 {
				t.Fatalf("scope %s: %+v %v", scope.Kind, total, err)
			}
			day, found, err := s.GetUsageScopeDay("account-1", scope.Kind, scope.ProjectID, scope.ID, "1970-01-01")
			if err != nil || !found || day.TotalTokens != tokens || day.ProviderCostUSD != cost || day.ReceiptCount != 1 {
				t.Fatalf("scope day %s: %+v %v", scope.Kind, day, err)
			}
		}
		acc, found, err := s.GetDailyUsageAccumulator("account-1", "1970-01-01")
		if err != nil || !found || acc.TotalTokens != tokens || acc.InputTokens != tokens || acc.TotalCostUSD != cost || acc.TurnCount != 1 {
			t.Fatalf("daily: %+v %v", acc, err)
		}
		rollup, found, err := s.GetAccountUsageRollup("account-1", "1970-01-01", snapshot.ID, "fixture", "fixture")
		if err != nil || !found || rollup.TotalTokens != tokens || rollup.TokenCostUSD != cost || rollup.Turns != 1 {
			t.Fatalf("account: %+v %v", rollup, err)
		}
		summary, found, err := s.GetUsageSummary(snapshot.ID)
		if err != nil || !found || summary.EstimatedCostUSD != cost || summary.TurnCount != 1 {
			t.Fatalf("session billing: %+v %v", summary, err)
		}
	}
	readState := func() map[string][]byte {
		t.Helper()
		out := map[string][]byte{}
		if err := db.IteratePrefix("", 1000, func(key string, value []byte) error {
			out[key] = append([]byte(nil), value...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	record("initial", 100)
	assertBills(100)
	before := readState()
	reset := SessionUsageSummary{SessionID: snapshot.ID, AccountScopeID: "account-1", UserID: "user-1", ContextWindow: 2000, Provider: "fixture", Model: "fixture", Source: "compaction", UpdatedAt: 4000, TotalTokens: 999, InputTokens: 999}
	if err := s.ResetUsage(snapshot.ID, reset); err != nil {
		t.Fatal(err)
	}
	assertBills(100)
	after := readState()
	for _, key := range []string{KeySessionUsageSummary(snapshot.ID), KeySessionUsageSummaryByAccount("account-1", snapshot.ID)} {
		delete(before, key)
		delete(after, key)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("context reset changed receipt, index, attribution or billing records")
	}
	summary, _, err := s.GetUsageSummary(snapshot.ID)
	if err != nil || summary.InputTokens != 0 || summary.OutputTokens != 0 || summary.ThinkingTokens != 0 || summary.CacheReadTokens != 0 || summary.CacheWriteTokens != 0 || summary.TotalTokens != 0 || summary.ContextWindow != 2000 || summary.RemainingTokens != 2000 {
		t.Fatalf("context reset: %+v %v", summary, err)
	}
	index, found, err := db.GetBytes(KeySessionTurnUsageByAccount("account-1", snapshot.ID, "receipt"))
	if err != nil || !found || !bytes.Equal(index, []byte("receipt")) {
		t.Fatalf("receipt index missing: %q %v", index, err)
	}
	// Rejection must leave every record untouched, not just the receipt.
	for _, foreign := range []SessionUsageSummary{{AccountScopeID: "foreign", UserID: "user-1"}, {AccountScopeID: "account-1", UserID: "foreign"}} {
		before = readState()
		if err := s.ResetUsage(snapshot.ID, foreign); err == nil {
			t.Fatal("foreign reset accepted")
		}
		if !reflect.DeepEqual(before, readState()) {
			t.Fatal("rejected reset mutated durable state")
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	assertBills(100)
	result, err := s.RepairUsageScopes("account-1", "", 1)
	if err != nil || result.Scanned != 1 || result.Repaired != 0 || result.Unresolved != 0 || result.NextCursor != "" {
		t.Fatalf("restart repair: %+v %v", result, err)
	}
	assertBills(100)
	// Identical idempotency replay and a fresh delivery of the same receipt
	// must both leave one charge; subsequent corrections replace that charge.
	record("initial", 100)
	assertBills(100)
	for i, tokens := range []int64{100, 50, 150, 0} {
		record(fmt.Sprintf("correction-%d", i), tokens)
		assertBills(tokens)
	}
}
