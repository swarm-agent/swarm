package pebblestore

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Purpose: CheckWorkerBudgetAdmission and GetWorkerBudgetStatus must rejudge old
// unknown model receipts using CalculateCostWithStatus, without mutating evidence
// or admitting still-unpriced usage. A temporary Pebble ledger is the narrowest
// layer proving both budget consumers and persisted receipt/projection invariance.
// The budget read observer also proves status/per-call admission reuse one day read.
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
			r, err := s.WorkerStore().AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: "first", Input: map[string]any{"prompt": "Review"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateSession(SessionSnapshot{ID: r.SessionID, AccountScopeID: "acct", UserID: "user", Metadata: map[string]any{"worker_id": w.ID, "worker_run_id": r.ID}}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			date := now.Format("2006-01-02")
			scope := UsageScopeTotal{Kind: "worker", ID: w.ID, Coverage: "observed_receipts_only"}
			receipt := SessionTurnUsageSnapshot{AccountScopeID: "acct", SessionID: "fixture-session", RunID: "fixture-run", Provider: provider, Model: "fixture-model", PriceStatus: "unknown", InputTokens: 7, OutputTokens: 3, TotalTokens: 10, CreatedAt: now.UnixMilli(), ScopeProjectionVersion: 3, ScopeTotals: []UsageScopeTotal{scope}}
			// Exercise a delegated session's frozen worker attribution amid more
			// than 100 unrelated receipts, including earlier days of this worker.
			seedWorkerBudgetPricingLocator(t, db, receipt)
			for i := 0; i < 240; i++ {
				other := receipt
				other.SessionID = fmt.Sprintf("unrelated-%03d", i)
				if i%2 == 0 {
					other.CreatedAt = now.AddDate(0, 0, -1).UnixMilli()
				} else {
					other.ScopeTotals = []UsageScopeTotal{{Kind: "worker", ID: "other-worker"}}
				}
				seedWorkerBudgetPricingLocator(t, db, other)
				if err := db.PutJSON(KeySessionTurnUsage(other.SessionID, other.RunID), other); err != nil {
					t.Fatal(err)
				}
				if err := db.db.Set([]byte(KeySessionTurnUsageByAccount("acct", other.SessionID, other.RunID)), []byte(other.RunID), nil); err != nil {
					t.Fatal(err)
				}
			}
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
			reads := 0
			db.SetBudgetReadObserver(func(kind string) {
				if kind == BudgetReadUsageScopeDay {
					reads++
				}
			})
			status, err := s.GetWorkerBudgetStatus("acct", w.ID)
			if reads != 1 {
				t.Fatalf("status day reads = %d, want 1", reads)
			}
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
			reads = 0
			err = s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "call")
			if (blocked && !errors.Is(err, ErrWorkerBudget)) || (!blocked && err != nil) {
				t.Fatalf("per-call admission: %v", err)
			}
			if reads != 1 {
				t.Fatalf("per-call day reads = %d, want 1", reads)
			}
			db.SetBudgetReadObserver(nil)
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

// Purpose: workerBudgetUsageDay must bound legacy history reads and discard partial
// repricing; checkWorkerBudgetLocked must not scan for token-only/unset policies.
// A real temporary ledger with a corrupt sentinel proves the read boundary without
// timing assertions, and admission/status assertions prove fail-closed postconditions.
func TestWorkerBudgetPricingReadBound(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	w, err := NewWorkerStore(db).CreateWorker("acct", "user", CreateWorkerRequest{Name: "Budget worker", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	date := time.Now().UTC().Format("2006-01-02")
	scope := UsageScopeTotal{Kind: "worker", ID: w.ID, UnknownReceipts: 1, TotalTokens: 10, Coverage: "observed_receipts_only"}
	if err := db.PutJSON(usageScopeDayKey("acct", scope, date), scope); err != nil {
		t.Fatal(err)
	}
	seedWorkerBudgetPricingLocator(t, db, SessionTurnUsageSnapshot{AccountScopeID: "acct", SessionID: "000", RunID: "run", CreatedAt: time.Now().UnixMilli(), ScopeTotals: []UsageScopeTotal{{Kind: "worker", ID: w.ID}}})
	// An invalid first index entry makes any unnecessary receipt scan observable.
	sentinel := KeySessionTurnUsageByAccount("acct", "000", "run")
	if err := db.db.Set([]byte(sentinel), []byte("run"), nil); err != nil {
		t.Fatal(err)
	}
	for _, tokens := range []int64{0, 100} {
		policy, err := s.GetWorkerBudget("acct", w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetWorkerBudget("acct", w.ID, policy.Revision, 0, tokens); err != nil {
			t.Fatal(err)
		}
		if err := s.CheckWorkerBudgetAdmission("acct", w.ID); err != nil {
			t.Fatalf("non-USD admission scanned receipts: %v", err)
		}
	}
	if _, err := s.GetWorkerBudgetStatus("acct", w.ID); err == nil {
		t.Fatal("status must still attempt pricing")
	}
	scope.UnknownReceipts = workerBudgetPricingReceiptLimit + 1
	if err := db.PutJSON(usageScopeDayKey("acct", scope, date), scope); err != nil {
		t.Fatal(err)
	}
	// Overflow only this worker's current-day receipts; discard partial repricing.
	for i := 0; i <= workerBudgetPricingReceiptLimit; i++ {
		session := fmt.Sprintf("%03d", i)
		receipt := SessionTurnUsageSnapshot{AccountScopeID: "acct", SessionID: session, RunID: "run", Provider: "claudecode", PriceStatus: "unknown", ScopeProjectionVersion: 3, CreatedAt: time.Now().UnixMilli(), ScopeTotals: []UsageScopeTotal{{Kind: "worker", ID: w.ID}}}
		seedWorkerBudgetPricingLocator(t, db, receipt)
		if err := db.PutJSON(KeySessionTurnUsage(session, "run"), receipt); err != nil {
			t.Fatal(err)
		}
		if err := db.db.Set([]byte(KeySessionTurnUsageByAccount("acct", session, "run")), []byte("run"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.Set([]byte(KeySessionTurnUsageByAccount("acct", "zzz", "run")), []byte("run"), nil); err != nil {
		t.Fatal(err)
	}
	total, err := s.workerBudgetUsageDay("acct", w.ID, date)
	if err != nil || !reflect.DeepEqual(total, scope) {
		t.Fatalf("bounded read must discard partial repricing: %+v %v", total, err)
	}
	policy, err := s.GetWorkerBudget("acct", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("acct", w.ID, policy.Revision, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerBudgetAdmission("acct", w.ID); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("must fail closed: %v", err)
	}
	status, err := s.GetWorkerBudgetStatus("acct", w.ID)
	if err != nil || !status.Blocked || status.Inflight || status.Hold != nil || !reflect.DeepEqual(status.Usage, scope) {
		t.Fatalf("status: %+v %v", status, err)
	}
	stored, _, err := s.GetUsageScopeDay("acct", "worker", "", w.ID, date)
	if err != nil || !reflect.DeepEqual(stored, scope) {
		t.Fatalf("projection mutated: %+v %v", stored, err)
	}
}

// Seed the existing write-time locator records alongside a historical receipt.
func seedWorkerBudgetPricingLocator(t *testing.T, db *Store, receipt SessionTurnUsageSnapshot) {
	t.Helper()
	date := time.UnixMilli(receipt.CreatedAt).UTC().Format("2006-01-02")
	rollup := AccountUsageRollup{AccountScopeID: receipt.AccountScopeID, Date: date, SessionID: receipt.SessionID, Provider: receipt.Provider, Model: receipt.Model, Turns: 1}
	if err := db.PutJSON(KeyAccountUsageRollup(receipt.AccountScopeID, date, receipt.SessionID, receipt.Provider, receipt.Model), rollup); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(usageBindingKey(receipt.AccountScopeID, receipt.SessionID), receipt.ScopeTotals); err != nil {
		t.Fatal(err)
	}
}
