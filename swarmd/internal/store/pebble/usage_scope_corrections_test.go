package pebblestore

import (
	"encoding/json"
	"testing"
)

// Purpose: a corrected cumulative receipt is replacement, including downward
// corrections and explicit billed zero. Owners: ApplyV3SessionMutation,
// billedComponents, daily accumulator and account rollup batch writes. The real
// temporary store is the narrowest layer proving all observable postconditions.
func TestUsageScopeCorrectionsAccountAndExplicitZero(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "corrections")
	for i, n := range []int64{100, 60, 100, 0} {
		turn := SessionTurnUsageSnapshot{RunID: "receipt", Provider: "fixture", Model: "fixture", TotalTokens: 900, InputTokens: 800, BilledUsagePresent: true, BilledTokens: n, BilledInputTokens: n, PriceStatus: "known", EstimatedCostUSD: float64(n) / 100}
		key := string(rune('a' + i))
		result, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "corrections", UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordUsage, IdempotencyKey: key, PayloadHash: key, TurnUsage: &turn, NowUnixMs: 3000})
		if err != nil {
			t.Fatal(err)
		}
		acc, ok, err := s.GetDailyUsageAccumulator("account-1", "1970-01-01")
		if err != nil || !ok || acc.TotalTokens != n || acc.InputTokens != n || acc.TotalCostUSD != float64(n)/100 || acc.TurnCount != 1 {
			t.Fatalf("daily replacement: %+v %v", acc, err)
		}
		rollup, ok, err := s.GetAccountUsageRollup("account-1", "1970-01-01", "corrections", "fixture", "fixture")
		if err != nil || !ok || rollup.TotalTokens != n || rollup.TokenCostUSD != float64(n)/100 || rollup.Turns != 1 {
			t.Fatalf("rollup replacement: %+v %v", rollup, err)
		}
		if result.UsageSummary.TotalTokens != 900 || result.UsageSummary.EstimatedCostUSD != float64(n)/100 {
			t.Fatalf("context/cost summary: %+v", result.UsageSummary)
		}
	}
	before, _, _ := s.GetTurnUsage("corrections", "receipt")
	bad := before
	bad.Model = "other-model"
	bad.BilledTokens = 999
	if err := s.PutTurnUsage(bad); err == nil {
		t.Fatal("bucket reassignment accepted")
	}
	after, _, _ := s.GetTurnUsage("corrections", "receipt")
	if after.Model != before.Model || after.BilledTokens != before.BilledTokens {
		t.Fatal("rejected reassignment changed receipt")
	}
}

// Purpose: media charges must join task usage in the same V3 receipt/outbox
// transaction and replay must not double-charge. Owners: PutMediaUsage,
// ApplyV3SessionMutation, prepareMediaScopeTotals. This real temporary store
// verifies durable projection and event payload, not fabricated telemetry.
func TestUsageScopeMediaAtomicReplay(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "media-scope")
	snapshot, _, _ := s.GetSession("media-scope")
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	media := SessionMediaUsageRecord{ID: "media-receipt", SessionID: snapshot.ID, AccountScopeID: "account-1", UserID: "user-1", Kind: "image", CostUSD: 2, PriceStatus: "known", CreatedAt: 3000}
	input := V3SessionMutationInput{SessionID: snapshot.ID, UserID: "user-1", AccountScopeID: "account-1", Kind: V3SessionMutationRecordMediaUsage, IdempotencyKey: "media", PayloadHash: "media", MediaUsage: &media, NowUnixMs: 3000}
	result, err := s.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	total, ok, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || !ok || total.MediaCostUSD != 2 || total.CatalogCostUSD != 2 || total.MediaReceipts != 1 || total.ReceiptCount != 1 || total.TotalTokens != 0 {
		t.Fatalf("media scope: %+v %v", total, err)
	}
	var payload struct {
		MediaUsage *SessionMediaUsageRecord `json:"media_usage"`
	}
	if err := json.Unmarshal(result.RealtimeOutbox.Event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MediaUsage == nil || len(payload.MediaUsage.ScopeTotals) != 1 || payload.MediaUsage.ScopeTotals[0] != total {
		t.Fatalf("missing atomic media scope event: %+v", payload)
	}
	media.AccountScopeID = "foreign"
	if err := s.PutMediaUsage(media); err == nil {
		t.Fatal("foreign media accepted")
	}
	after, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if after != total {
		t.Fatal("foreign receipt changed projection")
	}
}

// Purpose: new runs and late receipts retain corroborated task attribution after
// metadata rebinding, and UTC day reads remain bounded point reads. Owners:
// resolveUsageScopes, setUsageBinding and setUsageDays; real store writes exercise
// the atomic index, while forged caller ScopeTotals must be ignored.
func TestUsageScopeBindingComponentsAndDay(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "bound")
	snapshot, _, _ := s.GetSession("bound")
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	turn := SessionTurnUsageSnapshot{SessionID: snapshot.ID, AccountScopeID: "account-1", RunID: "one", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 40, BilledOutputTokens: 20, BilledCacheReadTokens: 10, BilledCacheWriteTokens: 15, BilledThinkingTokens: 15, PriceStatus: "known", CostProvenance: "provider", EstimatedCostUSD: 2, CreatedAt: 3000, ScopeTotals: []UsageScopeTotal{{Kind: "worker", ID: "forged"}}}
	if err := s.PutTurnUsage(turn); err != nil {
		t.Fatal(err)
	}
	snapshot.Metadata = map[string]any{"project_id": "unrelated", "task_id": "unrelated"}
	if err := db.PutJSON(KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	turn.RunID = "two"
	turn.CreatedAt = 86400000 + 3000
	if err := s.PutTurnUsage(turn); err != nil {
		t.Fatal(err)
	}
	total, _, err := s.GetUsageScope("account-1", "task", "project", "task")
	if err != nil || total.TotalTokens != 200 || total.InputTokens != 80 || total.OutputTokens != 40 || total.CacheReadTokens != 20 || total.CacheWriteTokens != 30 || total.ThinkingTokens != 30 || total.ProviderCostUSD != 4 || total.CatalogCostUSD != 0 {
		t.Fatalf("binding/components: %+v %v", total, err)
	}
	for _, date := range []string{"1970-01-01", "1970-01-02"} {
		day, found, err := s.GetUsageScopeDay("account-1", "task", "project", "task", date)
		if err != nil || !found || day.TotalTokens != 100 || day.ProviderCostUSD != 2 {
			t.Fatalf("day: %+v %v", day, err)
		}
	}
	if _, found, err := s.GetUsageScope("account-1", "worker", "", "forged"); err != nil || found {
		t.Fatal("caller forged attribution")
	}
	if _, _, err := s.GetUsageScopeDay("account-1", "task", "project", "task", "1970-02-30"); err == nil {
		t.Fatal("invalid day accepted")
	}
}

// Purpose: program-child attribution needs a durable job receipt; mutable parent
// metadata alone must not redirect charges. Owners: resolveUsageScopes and
// GetTaskProgram. Direct temporary records isolate trust checks from scheduling.
func TestUsageScopeProgramReceiptAndForgedParent(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	createV3SessionForTest(t, s, "parent-scope")
	createV3SessionForTest(t, s, "child-scope")
	parent, _, _ := s.GetSession("parent-scope")
	parent.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	child, _, _ := s.GetSession("child-scope")
	child.Metadata = map[string]any{"parent_session_id": parent.ID, "task_program_id": "program"}
	if err := db.PutJSON(KeySession(child.ID), child); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.resolveUsageScopes("account-1", child.ID)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("metadata granted lineage: %+v %v", scopes, err)
	}
	program := TaskProgramRecord{ParentSessionID: parent.ID, ProgramID: "program", Jobs: []TaskProgramJobRecord{{ChildSessionID: child.ID}}}
	if err := db.PutJSON(KeyTaskProgram(parent.ID, "program"), program); err != nil {
		t.Fatal(err)
	}
	scopes, err = s.resolveUsageScopes("account-1", child.ID)
	if err != nil || len(scopes) != 1 || scopes[0].ID != "task" {
		t.Fatalf("durable child missing: %+v %v", scopes, err)
	}
	parent.AccountScopeID = "foreign"
	if err := db.PutJSON(KeySession(parent.ID), parent); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: child.ID, AccountScopeID: "account-1", RunID: "rejected", TotalTokens: 10, PriceStatus: "free"}); err == nil {
		t.Fatal("cross-account parent accepted")
	}
	if _, found, _ := s.GetTurnUsage(child.ID, "rejected"); found {
		t.Fatal("rejected lineage mutated receipt")
	}
	if _, found, _ := s.GetUsageScope("account-1", "task", "project", "task"); found {
		t.Fatal("rejected lineage mutated projection")
	}
}

// Purpose: worker lifetime totals accumulate multiple durable runs and historical
// task attempts without deduplicating distinct receipts or trusting metadata.
// Owners: resolveUsageScopes and PutTurnUsage account lock; temporary real store
// is the smallest deterministic proof without executing a worker/provider.
func TestUsageScopeWorkerRunsAndAttempts(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	for i, id := range []string{"worker-one", "worker-two"} {
		createV3SessionForTest(t, s, id)
		snapshot, _, _ := s.GetSession(id)
		run := string(rune('a' + i))
		snapshot.Metadata = map[string]any{"worker_id": "worker", "worker_run_id": run, "project_id": "project", "task_id": "task"}
		if err := db.PutJSON(KeySession(id), snapshot); err != nil {
			t.Fatal(err)
		}
		if err := db.PutJSON(KeyWorkerRun("account-1", "worker", run), WorkerRunRecord{ID: run, WorkerID: "worker", AccountScopeID: "account-1", SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutJSON(KeyProjectTask("account-1", "project", "task"), ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-1", SessionID: "worker-two", Attempts: []ProjectTaskAttempt{{ID: "old", SessionID: "worker-one"}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"worker-one", "worker-two"} {
		if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: id, AccountScopeID: "account-1", RunID: "receipt", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 50, PriceStatus: "known", EstimatedCostUSD: 1}); err != nil {
			t.Fatal(err)
		}
	}
	worker, _, _ := s.GetUsageScope("account-1", "worker", "", "worker")
	task, _, _ := s.GetUsageScope("account-1", "task", "project", "task")
	if worker.TotalTokens != 100 || worker.CatalogCostUSD != 2 || worker.ReceiptCount != 2 || task.TotalTokens != 100 {
		t.Fatalf("worker/task aggregation: %+v %+v", worker, task)
	}
	for _, run := range []string{"a", "b"} {
		total, _, _ := s.GetUsageScope("account-1", "worker_run", "worker", run)
		if total.TotalTokens != 50 || total.CatalogCostUSD != 1 || total.ReceiptCount != 1 {
			t.Fatalf("run: %+v", total)
		}
	}
}

// Purpose: subscription nominal estimates use cumulative billed components and
// price transitions remove the old contribution. Owner: applyScopeReceipt;
// the pure projection layer is narrowest for arithmetic, not live pricing proof.
func TestUsageScopeNominalAndPriceTransitions(t *testing.T) {
	old := SessionTurnUsageSnapshot{Provider: "codex", Model: "fixture", ScopeProjectionVersion: 2, PriceStatus: "subscription", BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 70, BilledOutputTokens: 30, InputTokens: 1, OutputTokens: 1}
	total := UsageScopeTotal{}
	applyScopeReceipt(&total, old, 1)
	want := CalculateBaselineCost("openai", old.Model, 70, 30, 0, 0)
	if total.NominalSubscriptionCostUSD != want || total.SubscriptionReceipts != 1 {
		t.Fatalf("nominal: %+v want %v", total, want)
	}
	applyScopeReceipt(&total, old, -1)
	current := old
	current.PriceStatus = "known"
	current.CostProvenance = "provider"
	current.EstimatedCostUSD = 2
	applyScopeReceipt(&total, current, 1)
	if total.SubscriptionReceipts != 0 || total.NominalSubscriptionCostUSD != 0 || total.ProviderCostUSD != 2 || total.CatalogCostUSD != 0 || total.ReceiptCount != 1 {
		t.Fatalf("transition: %+v", total)
	}
	applyScopeReceipt(&total, current, -1)
	current.PriceStatus = "free"
	current.EstimatedCostUSD = 0
	applyScopeReceipt(&total, current, 1)
	if total.ProviderCostUSD != 0 || total.FreeReceipts != 1 || total.TotalTokens != 100 {
		t.Fatalf("free transition: %+v", total)
	}
}
