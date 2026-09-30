package pebblestore

import (
	"encoding/base64"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: RepairUsageScopes must consume genuine indexed historic receipts
// exactly once, never recharge bills/context, and reject foreign cursors without
// mutation. The real temporary Pebble store is the narrowest transaction layer.
func TestUsageScopeRepairBoundedIdempotent(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "historic")
	snapshot, _, _ := s.GetSession("historic")
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	turn := SessionTurnUsageSnapshot{SessionID: snapshot.ID, AccountScopeID: "account-1", RunID: "one", Provider: "fixture", Model: "fixture", TotalTokens: 20, PriceStatus: "free", CreatedAt: 3000}
	if err := s.PutTurnUsage(turn); err != nil {
		t.Fatal(err)
	}
	// Emulate a pre-projection receipt while preserving genuine account charges.
	if err := db.PutJSON(KeySessionTurnUsage(snapshot.ID, "one"), turn); err != nil {
		t.Fatal(err)
	}
	batch := db.NewBatch()
	if err := batch.Delete([]byte(usageScopeKey("account-1", UsageScopeTotal{Kind: "task", ProjectID: "project", ID: "task"})), nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Delete([]byte(usageScopeDayKey("account-1", UsageScopeTotal{Kind: "task", ProjectID: "project", ID: "task"}, "1970-01-01")), nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	before, _, _ := s.GetDailyUsageAccumulator("account-1", "1970-01-01")
	result, err := s.RepairUsageScopes("account-1", "", 1)
	if err != nil || result.Repaired != 1 || result.Scanned != 1 || result.HistoryComplete {
		t.Fatalf("repair: %+v %v", result, err)
	}
	result, err = s.RepairUsageScopes("account-1", "", 100)
	if err != nil || result.Repaired != 0 || result.NextCursor != "" {
		t.Fatalf("retry: %+v %v", result, err)
	}
	total, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if total.TotalTokens != 20 || total.ReceiptCount != 1 || total.HistoryComplete || total.Coverage != "repaired_receipts_incomplete" {
		t.Fatalf("projection: %+v", total)
	}
	day, _, _ := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-01-01")
	if day.TotalTokens != 20 || day.ReceiptCount != 1 {
		t.Fatalf("day: %+v", day)
	}
	after, _, _ := s.GetDailyUsageAccumulator("account-1", "1970-01-01")
	if after.TotalTokens != before.TotalTokens || after.TotalCostUSD != before.TotalCostUSD || after.TurnCount != before.TurnCount {
		t.Fatal("repair recharged account")
	}
	foreign := base64.RawURLEncoding.EncodeToString([]byte(KeySessionTurnUsageByAccount("foreign", "session", "run")))
	if _, err := s.RepairUsageScopes("account-1", foreign, 1); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	again, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if again != total {
		t.Fatal("foreign cursor mutated projection")
	}
}

// Purpose: authoritative task linkage must freeze before the first charge,
// retaining late receipts after replacement/cancellation. Owners:
// bindTaskUsageInBatch/project mutation participant and resolveUsageScopes.
// A real batch isolates the association boundary from unrelated task scheduling.
func TestUsageScopeBindingBeforeFirstCharge(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "precharge")
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "precharge"}
	batch := db.NewBatch()
	if err := s.bindTaskUsageInBatch(batch, "account-1", task); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	snapshot, _, _ := s.GetSession("precharge")
	snapshot.Metadata = map[string]any{"project_id": "other", "task_id": "other"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: snapshot.ID, AccountScopeID: "account-1", RunID: "late", TotalTokens: 42, PriceStatus: "free"}); err != nil {
		t.Fatal(err)
	}
	total, found, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || !found || total.TotalTokens != 42 {
		t.Fatalf("lost precharge binding: %+v %v", total, err)
	}
}

// Purpose: estimates and partial unknown pricing must never masquerade as
// invoice costs. Owner applyScopeReceipt; pure signed arithmetic is narrowest.
func TestUsageScopeProviderEstimateAndPartialUnknown(t *testing.T) {
	receipt := SessionTurnUsageSnapshot{PriceStatus: "known", ServiceTierStatus: "unknown", CostProvenance: "provider_estimate", EstimatedCostUSD: 2, TotalTokens: 10, ScopeProjectionVersion: 3}
	total := UsageScopeTotal{}
	applyScopeReceipt(&total, receipt, 1)
	if total.ProviderEstimateCostUSD != 2 || total.ProviderCostUSD != 0 || total.CatalogCostUSD != 0 || total.UnknownReceipts != 1 {
		t.Fatalf("estimate: %+v", total)
	}
	applyScopeReceipt(&total, receipt, -1)
	if total != (UsageScopeTotal{}) {
		t.Fatalf("signed replacement: %+v", total)
	}
}

// Purpose: retained regular task_launches and retired program generations, not
// bare child parent hints, authorize descendant accounting. Owners:
// resolveUsageScopes/GetTaskProgram/GetSession; temporary records are narrowest
// for trust, cycle rejection and no-partial-mutation postconditions.
func TestUsageScopeRegularAndRetiredLineage(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	for _, id := range []string{"lineage-parent", "lineage-child"} {
		createV3SessionForTest(t, s, id)
	}
	parent, _, _ := s.GetSession("lineage-parent")
	child, _, _ := s.GetSession("lineage-child")
	parent.Metadata = map[string]any{"project_id": "project", "task_id": "task", "task_launches": map[string]any{"call": map[string]any{"launches": []any{map[string]any{"child_session_id": child.ID}}}}}
	child.Metadata = map[string]any{"parent_session_id": parent.ID, "parent_task_call_id": "call"}
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeySession(child.ID), child); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.resolveUsageScopes("account-1", child.ID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("regular lineage: %+v %v", scopes, err)
	}
	parent.Metadata["task_launches"] = map[string]any{}
	child.Metadata["task_program_id"] = "program"
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeySession(child.ID), child); err != nil {
		t.Fatal(err)
	}
	program := TaskProgramRecord{ParentSessionID: parent.ID, ProgramID: "program", Jobs: []TaskProgramJobRecord{{ChildSessionID: "replacement", GenerationHistory: []TaskProgramJobGeneration{{SessionID: child.ID, State: "retired"}}}}}
	if err := db.PutJSON(KeyTaskProgram(parent.ID, "program"), program); err != nil {
		t.Fatal(err)
	}
	scopes, err = s.resolveUsageScopes("account-1", child.ID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("retired lineage: %+v %v", scopes, err)
	}
	parent.Metadata["parent_session_id"] = child.ID
	parent.Metadata["task_program_id"] = "cycle"
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyTaskProgram(child.ID, "cycle"), TaskProgramRecord{ParentSessionID: child.ID, ProgramID: "cycle", Jobs: []TaskProgramJobRecord{{ChildSessionID: parent.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: child.ID, AccountScopeID: "account-1", RunID: "cycle", TotalTokens: 99, PriceStatus: "free"}); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, found, _ := s.GetTurnUsage(child.ID, "cycle"); found {
		t.Fatal("cycle changed receipt")
	}
	if _, found, _ := s.GetUsageScope("account-1", "task", "project", "task"); found {
		t.Fatal("cycle changed projection")
	}
}

// Purpose: corrected dates/models cannot silently move account billing buckets;
// cumulative decreases and partial-unknown changes replace, not add. Owners:
// PutTurnUsage and setUsageDays. Real temporary store verifies UTC postconditions.
func TestUsageScopeCorrectionPinsBillingBucket(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "bucket")
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "bucket"}
	batch := db.NewBatch()
	if err := s.bindTaskUsageInBatch(batch, "account-1", task); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	turn := SessionTurnUsageSnapshot{SessionID: "bucket", AccountScopeID: "account-1", RunID: "run", Provider: "fixture", Model: "fixture", TotalTokens: 100, PriceStatus: "known", ServiceTierStatus: "unknown", EstimatedCostUSD: 2, CreatedAt: 86399000}
	if err := s.PutTurnUsage(turn); err != nil {
		t.Fatal(err)
	}
	turn.TotalTokens = 40
	turn.EstimatedCostUSD = 1
	turn.ServiceTierStatus = "known"
	turn.CreatedAt = 86401000
	if err := s.PutTurnUsage(turn); err != nil {
		t.Fatal(err)
	}
	day, found, err := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-01-01")
	if err != nil || !found || day.TotalTokens != 40 || day.CatalogCostUSD != 1 || day.UnknownReceipts != 0 || day.ReceiptCount != 1 {
		t.Fatalf("date correction: %+v %v", day, err)
	}
	if _, found, _ := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-01-02"); found {
		t.Fatal("receipt silently moved date")
	}
	turn.Model = "other"
	if err := s.PutTurnUsage(turn); err == nil {
		t.Fatal("model correction silently moved bucket")
	}
	after, _, _ := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-01-01")
	if after != day {
		t.Fatal("rejected bucket move mutated day")
	}
}

// Purpose: worker task attempts must inherit the authoritative task/run link
// even when the run's execution session differs; cancellation/archive cannot
// erase late descendant receipts. Owners bindTaskUsageInBatch/resolveUsageScopes;
// real store batches prove billing attribution before the first charge.
func TestUsageScopeWorkerAttemptAndLateDescendant(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	for _, id := range []string{"execution", "attempt", "descendant"} {
		createV3SessionForTest(t, s, id)
	}
	if err := db.PutJSON(KeyWorkerRun("account-1", "worker", "run"), WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: "account-1", SessionID: "execution", Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "attempt", WorkerID: "worker", WorkerRunID: "run", Archived: true, Attempts: []ProjectTaskAttempt{{ID: "old", SessionID: "execution"}}}
	batch := db.NewBatch()
	if err := s.bindTaskUsageInBatch(batch, "account-1", task); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	parent, _, _ := s.GetSession("attempt")
	child, _, _ := s.GetSession("descendant")
	parent.Metadata = map[string]any{"task_launches": map[string]any{"call": map[string]any{"launches": []any{map[string]any{"child_session_id": child.ID}}}}}
	child.Metadata = map[string]any{"parent_session_id": parent.ID, "parent_task_call_id": "call"}
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeySession(child.ID), child); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"execution", "attempt", "descendant"} {
		if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: id, AccountScopeID: "account-1", RunID: "late", TotalTokens: 10, PriceStatus: "free"}); err != nil {
			t.Fatal(err)
		}
	}
	worker, _, _ := s.GetUsageScope("account-1", "worker", "", "worker")
	run, _, _ := s.GetUsageScope("account-1", "worker_run", "worker", "run")
	if worker.TotalTokens != 30 || run.TotalTokens != 30 || worker.ReceiptCount != 3 {
		t.Fatalf("late worker attribution: %+v %+v", worker, run)
	}
}

// Purpose: old media receipts join the existing projection/day without new
// account charges and durable repair events are replay-safe. Owners:
// RepairUsageScopes/prepareMediaScopeTotals/canonical repair participant. Real
// store indexes are the narrowest layer proving the media transaction.
func TestUsageScopeRepairHistoricMedia(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "historic-media")
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "historic-media"}
	batch := db.NewBatch()
	if err := s.bindTaskUsageInBatch(batch, "account-1", task); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	receipt := SessionMediaUsageRecord{ID: "image", SessionID: "historic-media", AccountScopeID: "account-1", UserID: "user-1", Kind: "image", CostUSD: 2, PriceStatus: "known", CreatedAt: 3000}
	if err := db.PutJSON(KeySessionMediaUsage("account-1", receipt.ID), receipt); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeySessionMediaUsageBySession(receipt.SessionID, receipt.ID), receipt); err != nil {
		t.Fatal(err)
	}
	result, err := s.RepairUsageScopes("account-1", "", 1)
	if err != nil || result.Repaired != 1 || result.HistoryComplete {
		t.Fatalf("media repair: %+v %v", result, err)
	}
	total, found, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || !found || total.MediaCostUSD != 2 || total.MediaReceipts != 1 || total.ReceiptCount != 1 {
		t.Fatalf("media total: %+v %v", total, err)
	}
	day, _, _ := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-01-01")
	if day.MediaCostUSD != 2 || day.Coverage != "repaired_receipts_incomplete" {
		t.Fatalf("media day: %+v", day)
	}
	result, err = s.RepairUsageScopes("account-1", "", 100)
	if err != nil || result.Repaired != 0 {
		t.Fatalf("media retry: %+v %v", result, err)
	}
	after, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if after != total {
		t.Fatal("media replay inflated projection")
	}
	if _, found, _ := s.GetDailyUsageAccumulator("account-1", "1970-01-01"); found {
		t.Fatal("repair invented account charge")
	}
}

// Purpose: versioned repair of old partial-unknown/pricing projections must
// replace their exact prior contribution, not subtract fields never projected.
// Owner applyScopeReceipt; pure versioned arithmetic isolates migration math.
func TestUsageScopeVersionedPartialUnknownReplacement(t *testing.T) {
	old := SessionTurnUsageSnapshot{ScopeProjectionVersion: 2, PriceStatus: "known", ServiceTierStatus: "unknown", CostProvenance: "provider", EstimatedCostUSD: 2, TotalTokens: 10}
	total := UsageScopeTotal{TotalTokens: 10, ProviderCostUSD: 2, ReceiptCount: 1}
	applyScopeReceipt(&total, old, -1)
	current := old
	current.ScopeProjectionVersion = 3
	current.CostProvenance = "provider_estimate"
	applyScopeReceipt(&total, current, 1)
	if total.TotalTokens != 10 || total.ProviderCostUSD != 0 || total.ProviderEstimateCostUSD != 2 || total.UnknownReceipts != 1 || total.ReceiptCount != 1 {
		t.Fatalf("versioned replacement: %+v", total)
	}
}

// Purpose: bounded repair must resume across pages without skipping receipts,
// and unresolved lineage must remain incomplete rather than certified zero.
// Owner RepairUsageScopes; canonical account indexes in a real temp store are
// the smallest proof of cursor and projection postconditions.
func TestUsageScopeRepairCursorAndUnresolved(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "repair-pages")
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "repair-pages"}
	batch := db.NewBatch()
	if err := s.bindTaskUsageInBatch(batch, "account-1", task); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	for _, run := range []string{"one", "two"} {
		receipt := SessionTurnUsageSnapshot{SessionID: "repair-pages", AccountScopeID: "account-1", RunID: run, TotalTokens: 10, PriceStatus: "free", CreatedAt: 3000}
		if err := db.PutJSON(KeySessionTurnUsage(receipt.SessionID, run), receipt); err != nil {
			t.Fatal(err)
		}
		if err := db.PutBytes(KeySessionTurnUsageByAccount("account-1", receipt.SessionID, run), []byte(run)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.RepairUsageScopes("account-1", "", 1)
	if err != nil || first.Scanned != 1 || first.Repaired != 1 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := s.RepairUsageScopes("account-1", first.NextCursor, 1)
	if err != nil || second.Scanned != 1 || second.Repaired != 1 {
		t.Fatalf("second page: %+v %v", second, err)
	}
	total, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if total.TotalTokens != 20 || total.ReceiptCount != 2 {
		t.Fatalf("page totals: %+v", total)
	}
	createV3SessionForTest(t, s, "unresolved")
	receipt := SessionTurnUsageSnapshot{SessionID: "unresolved", AccountScopeID: "account-1", RunID: "run", TotalTokens: 99, PriceStatus: "free"}
	if err := db.PutJSON(KeySessionTurnUsage(receipt.SessionID, receipt.RunID), receipt); err != nil {
		t.Fatal(err)
	}
	if err := db.PutBytes(KeySessionTurnUsageByAccount("account-1", receipt.SessionID, receipt.RunID), []byte(receipt.RunID)); err != nil {
		t.Fatal(err)
	}
	result, err := s.RepairUsageScopes("account-1", "", 100)
	if err != nil || result.Unresolved != 1 || result.HistoryComplete {
		t.Fatalf("unresolved: %+v %v", result, err)
	}
}
