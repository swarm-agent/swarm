package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: CalculateCostWithStatus must classify plan model usage as subscription,
// not unknown or API spend. This unit boundary proves normalization and preserves
// unknown-provider rejection inputs without needing a provider or catalog.
func TestWorkerBudgetSubscriptionPricing(t *testing.T) {
	s := &SessionStore{}
	for _, provider := range []string{"claudecode", " ClaudeCode ", "codex"} {
		cost, status := s.CalculateCostWithStatus(provider, "fixture-model", 100, 50, 10, 10, 5)
		if cost != 0 || status != "subscription" {
			t.Fatalf("%q: cost=%v status=%s", provider, cost, status)
		}
	}
	cost, status := s.CalculateCostWithStatus("unpriced-provider", "fixture-model", 100, 50, 0, 0, 0)
	if cost != 0 || status != "unknown" {
		t.Fatalf("unknown provider: %v %s", cost, status)
	}
}

// Purpose: AdmitWorkerRun, CheckWorkerSessionBudget and durable receipt accounting
// must allow a $1 plan-backed worker while preserving media and token stops.
// Real temporary Pebble records are the narrowest layer proving admission,
// per-call reservations, receipt totals used by listings, and fail-closed pricing.
func TestWorkerBudgetSubscriptionAccounting(t *testing.T) {
	for _, mode := range []string{"media", "tokens", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s, ws := NewSessionStore(db), NewWorkerStore(db)
			w, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Name: "Budget worker", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, Budget: &WorkerBudgetRequest{DailyUSD: 1}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			w, err = ws.AcceptWorker("acct", "user", w.ID, w.Revision, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "tokens" {
				p, err := s.GetWorkerBudget("acct", w.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.SetWorkerBudget("acct", w.ID, p.Revision, 1, 10, "user"); err != nil {
					t.Fatal(err)
				}
			}
			r, err := ws.AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: "first", Input: map[string]any{"prompt": "Review"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateSession(SessionSnapshot{ID: r.SessionID, AccountScopeID: "acct", UserID: "user", Metadata: map[string]any{"worker_id": w.ID, "worker_run_id": r.ID}}); err != nil {
				t.Fatal(err)
			}
			if mode == "unknown" {
				if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "unpriced-provider", "fixture-model", "unknown"); !errors.Is(err, ErrWorkerBudget) {
					t.Fatalf("unknown pricing admitted: %v", err)
				}
				var reservation workerBudgetReservation
				if found, err := db.GetJSON(workerBudgetKey("acct", w.ID)+"/reservation", &reservation); err != nil || found {
					t.Fatalf("rejected call reserved budget: %v %v", found, err)
				}
			}
			if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "model-call"); err != nil {
				t.Fatal(err)
			}
			if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: r.SessionID, AccountScopeID: "acct", UserID: "user", RunID: "turn", Provider: "claudecode", Model: "fixture-model", InputTokens: 7, OutputTokens: 3, TotalTokens: 10, BudgetOperationID: "model-call"}); err != nil {
				t.Fatal(err)
			}
			if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "model-call"); err != nil {
				t.Fatal(err)
			}
			receipt, found, err := s.GetTurnUsage(r.SessionID, "turn")
			if err != nil || !found || receipt.PriceStatus != "subscription" || receipt.EstimatedCostUSD != 0 {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			total, found, err := s.GetUsageScopeDay("acct", "worker", "", w.ID, time.Now().UTC().Format("2006-01-02"))
			if err != nil || !found || total.TotalTokens != 10 || total.UnknownReceipts != 0 || total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD != 0 {
				t.Fatalf("model totals: %+v %v", total, err)
			}
			if mode == "tokens" {
				if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "next"); !errors.Is(err, ErrWorkerBudget) {
					t.Fatalf("token limit not enforced: %v", err)
				}
				return
			}
			if err := s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{CostUSD: 0.75, PriceStatus: "known"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutMediaUsage(SessionMediaUsageRecord{ID: "image", SessionID: r.SessionID, AccountScopeID: "acct", UserID: "user", Kind: "image", Provider: "media-provider", CostUSD: 0.75, PriceStatus: "known"}); err != nil {
				t.Fatal(err)
			}
			total, _, err = s.GetUsageScopeDay("acct", "worker", "", w.ID, time.Now().UTC().Format("2006-01-02"))
			if err != nil || total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD != 0.75 {
				t.Fatalf("media totals: %+v %v", total, err)
			}
			if err := s.CheckWorkerMediaAdmission("acct", r.SessionID, MediaCostEstimate{CostUSD: 0.5, PriceStatus: "known"}); !errors.Is(err, ErrWorkerBudget) {
				t.Fatalf("over-budget media admitted: %v", err)
			}
			if err := s.CheckWorkerBudgetAdmission("acct", w.ID); err != nil {
				t.Fatalf("rejected estimate changed allowance: %v", err)
			}
			if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "next"); err != nil {
				t.Fatalf("subscription call blocked below budget: %v", err)
			}
			// An already admitted concurrent media operation can overshoot.
			if err := s.PutMediaUsage(SessionMediaUsageRecord{ID: "video", SessionID: r.SessionID, AccountScopeID: "acct", UserID: "user", Kind: "video", Provider: "media-provider", CostUSD: 0.5, PriceStatus: "known"}); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "over"); !errors.Is(err, ErrWorkerBudget) {
				t.Fatalf("media spend did not block model call: %v", err)
			}
			if err := s.CheckWorkerBudgetAdmission("acct", w.ID); !errors.Is(err, ErrWorkerBudget) {
				t.Fatalf("media spend did not block admission: %v", err)
			}
		})
	}
}
