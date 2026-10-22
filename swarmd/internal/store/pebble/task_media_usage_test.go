package pebblestore

import (
	"testing"
	"time"
)

// Purpose: putTaskMediaUsage must atomically settle account budget holds and
// signal only committed receipts, just like V3 session media. Real Pebble is
// the narrowest boundary proving durable holds, idempotency and rejection.
func TestMediaCostTaskBudgetSettlement(t *testing.T) {
	ss, signals := openBudgetSignalStore(t)
	const account = "media-budget"
	worker := createBudgetSignalWorker(t, ss, account, 0)
	if err := ss.PutUsageLimit(UsageLimitRecord{AccountScopeID: account, DailyCostLimitUSD: .02, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: account, Title: "image", Agent: "image"}
	if err := ss.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	rec := SessionMediaUsageRecord{ID: "receipt", AccountScopeID: account, UserID: "user-budget", ProjectID: task.ProjectID, TaskID: task.ID, Kind: "image", CostUSD: .0336, PriceStatus: "known", CreatedAt: time.Now().UnixMilli()}
	*signals = nil
	for i := 0; i < 2; i++ {
		if err := ss.putTaskMediaUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	if len(*signals) != 1 {
		t.Fatalf("signals %+v", *signals)
	}
	var hold WorkerBudgetHold
	if found, err := ss.store.GetJSON(workerBudgetHoldKey(account, worker.ID, time.Now().UTC().Format("2006-01-02")), &hold); err != nil || !found {
		t.Fatalf("hold missing: %v %v", found, err)
	}
	if hold.Usage != .0336 {
		t.Fatalf("hold %+v", hold)
	}
	rec.ID = "rejected"
	rec.TaskID = "missing"
	if err := ss.putTaskMediaUsage(rec); err == nil {
		t.Fatal("missing task accepted")
	}
	if len(*signals) != 1 {
		t.Fatal("rejected receipt signalled")
	}
	receipts, err := ss.ListMediaUsage(account, 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts %v %v", receipts, err)
	}
}
