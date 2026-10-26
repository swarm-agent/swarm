package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Purpose: CheckWorkerBudgetAdmission and GetWorkerBudgetStatus must rejudge old
// unknown model receipts using CalculateCostWithStatus, without mutating evidence
// or admitting still-unpriced usage. A temporary Pebble ledger is the narrowest
// layer proving both budget consumers and persisted receipt/projection invariance.
func TestWorkerBudgetStoredUnknownPricing(t *testing.T) {
	for _, provider := range []string{"claudecode", "unpriced-provider"} {
		t.Run(provider, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewSessionStore(db)
			w, err := NewWorkerStore(db).CreateWorker("acct", "user", CreateWorkerRequest{Name: "Budget worker", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, Budget: &WorkerBudgetRequest{DailyUSD: 1}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			w, err = NewWorkerStore(db).AcceptWorker("acct", "user", w.ID, w.Revision, nil)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			date := now.Format("2006-01-02")
			scope := UsageScopeTotal{Kind: "worker", ID: w.ID, Coverage: "observed_receipts_only"}
			receipt := SessionTurnUsageSnapshot{AccountScopeID: "acct", SessionID: "fixture-session", RunID: "fixture-run", Provider: provider, Model: "fixture-model", PriceStatus: "unknown", InputTokens: 7, OutputTokens: 3, TotalTokens: 10, CreatedAt: now.UnixMilli(), ScopeProjectionVersion: 3, ScopeTotals: []UsageScopeTotal{scope}}
			// Seed the pre-fix durable format, bypassing today's write-time pricing.
			if err := db.PutJSON(KeySessionTurnUsage(receipt.SessionID, receipt.RunID), receipt); err != nil {
				t.Fatal(err)
			}
			if err := db.db.Set([]byte(KeySessionTurnUsageByAccount("acct", receipt.SessionID, receipt.RunID)), []byte(receipt.RunID), nil); err != nil {
				t.Fatal(err)
			}
			applyScopeReceipt(&scope, receipt, 1)
			if err := db.PutJSON(usageScopeDayKey("acct", scope, date), scope); err != nil {
				t.Fatal(err)
			}
			err = s.CheckWorkerBudgetAdmission("acct", w.ID)
			blocked := provider != "claudecode"
			if (blocked && !errors.Is(err, ErrWorkerBudget)) || (!blocked && err != nil) {
				t.Fatalf("admission: %v", err)
			}
			status, err := s.GetWorkerBudgetStatus("acct", w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status.Blocked != blocked || status.Inflight || status.Hold != nil || status.Usage.TotalTokens != 10 || status.RemainingCostUSD == nil || *status.RemainingCostUSD != 1 {
				t.Fatalf("status: %+v", status)
			}
			if blocked {
				if status.Usage.UnknownReceipts != 1 || status.Usage.SubscriptionReceipts != 0 {
					t.Fatalf("unknown: %+v", status.Usage)
				}
			} else if status.Usage.UnknownReceipts != 0 || status.Usage.SubscriptionReceipts != 1 || status.Usage.CatalogCostUSD+status.Usage.ProviderCostUSD+status.Usage.ProviderEstimateCostUSD != 0 {
				t.Fatalf("subscription: %+v", status.Usage)
			}
			stored, found, err := s.GetTurnUsage(receipt.SessionID, receipt.RunID)
			if err != nil || !found || !reflect.DeepEqual(stored, receipt) {
				t.Fatalf("receipt changed: %+v %v", stored, err)
			}
			projected, found, err := s.GetUsageScopeDay("acct", "worker", "", w.ID, date)
			if err != nil || !found || !reflect.DeepEqual(projected, scope) {
				t.Fatalf("projection changed: %+v %v", projected, err)
			}
		})
	}
}
