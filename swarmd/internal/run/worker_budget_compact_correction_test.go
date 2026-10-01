package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type compactBudgetReceiptRunner struct {
	calls   int
	started chan struct{}
	finish  chan struct{}
	failure error
}

func (r *compactBudgetReceiptRunner) ID() string { return "fixture" }
func (r *compactBudgetReceiptRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *compactBudgetReceiptRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.started != nil {
		close(r.started)
	}
	if r.finish != nil {
		<-r.finish
	}
	return provideriface.Response{Usage: provideriface.TokenUsage{InputTokens: 7, TotalTokens: 7}}, r.failure
}

// Purpose: Compact success/error and genuinely late cancelled usage must carry
// immutable operation identity, settle only after canonical receipts, and gate
// the next call while unresolved. runCompactProviderCall and the canonical V3
// store are exercised with channel-controlled provider completion, not billing
// telemetry. Fixtures bind a real worker record and matching durable run; an
// account cap alone must never make an ordinary Compact call worker work.
// Explicit timeouts bound every wait and avoid mutable global clocks.
func TestWorkerBudgetCompactExactAndLateSettlement(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := store.NewSessionStore(db)
	if _, err := s.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: "compact", UserID: "user", AccountScopeID: "account", Kind: store.V3SessionMutationCreateSession, IdempotencyKey: "create-compact", RequestHash: "create-compact", Session: &store.SessionSnapshot{ID: "compact", WorkspacePath: t.TempDir(), Metadata: map[string]any{"worker_id": "worker", "worker_run_id": "run"}}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(store.UsageLimitRecord{AccountScopeID: "account", Enabled: true, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorker("account", "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorkerRun("account", "worker", "run"), store.WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: "account", SessionID: "compact"}); err != nil {
		t.Fatal(err)
	}
	ctx := withWorkerBudget(context.Background(), s, "account", "compact")
	record := func(response provideriface.Response) error {
		u := response.Usage
		now := time.Now().UnixMilli()
		turn := store.SessionTurnUsageSnapshot{BudgetOperationID: u.BudgetOperationID, RunID: u.BudgetOperationID, Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: u.TotalTokens, BilledInputTokens: u.InputTokens, PriceStatus: "known", CreatedAt: now}
		if _, err := s.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: "compact", UserID: "user", AccountScopeID: "account", Kind: store.V3SessionMutationRecordUsage, EventType: "run.usage.updated", IdempotencyKey: u.BudgetOperationID, PayloadHash: u.BudgetOperationID, TurnUsage: &turn, NowUnixMs: now}); err != nil {
			return err
		}
		return s.ReleaseWorkerBudgetReservation("account", "compact", u.BudgetOperationID)
	}
	var old provideriface.Response
	for i, failure := range []error{nil, errors.New("provider failure")} {
		r := &compactBudgetReceiptRunner{failure: failure}
		response, err := runCompactProviderCall(ctx, r, provideriface.Request{}, nil)
		if !errors.Is(err, failure) || r.calls != 1 || response.Usage.BudgetOperationID == "" {
			t.Fatalf("call %d: %+v %v", i, response, err)
		}
		if err := record(response); err != nil {
			t.Fatal(err)
		}
		old = response
	}
	late := &compactBudgetReceiptRunner{started: make(chan struct{}), finish: make(chan struct{})}
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	recorded := make(chan error, 1)
	cancelCtx = withLateProviderReceipt(cancelCtx, func(response provideriface.Response) error { err := record(response); recorded <- err; return err })
	done := make(chan error, 1)
	go func() {
		_, err := runCompactProviderCallWithTerminationTimeout(cancelCtx, late, provideriface.Request{}, nil, time.Millisecond)
		done <- err
	}()
	select {
	case <-late.started:
	case <-time.After(time.Second):
		t.Fatal("provider not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation wait unbounded")
	}
	denied := &compactBudgetReceiptRunner{}
	if _, err := runCompactProviderCall(ctx, denied, provideriface.Request{}, nil); !errors.Is(err, store.ErrWorkerBudget) || denied.calls != 0 {
		t.Fatalf("unresolved overlap: %v", err)
	}
	if err := s.ReleaseWorkerBudgetReservation("account", "compact", old.Usage.BudgetOperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runCompactProviderCall(ctx, denied, provideriface.Request{}, nil); !errors.Is(err, store.ErrWorkerBudget) || denied.calls != 0 {
		t.Fatalf("old correction released new call: %v", err)
	}
	close(late.finish)
	select {
	case err := <-recorded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("late usage lost")
	}
	response, err := runCompactProviderCall(ctx, denied, provideriface.Request{}, nil)
	if err != nil || denied.calls != 1 {
		t.Fatalf("late receipt did not settle: %v", err)
	}
	if err := record(response); err != nil {
		t.Fatal(err)
	}
	acc, _, err := s.GetDailyUsageAccumulator("account", time.Now().UTC().Format("2006-01-02"))
	if err != nil || acc.TotalTokens != 28 {
		t.Fatalf("receipt accounting: %+v %v", acc, err)
	}
}

// Purpose: provider termination alone is not a billing receipt. The Compact
// boundary returns its operation identity even on empty/error responses; exact
// release records termination but must retain allowance. Narrow store+provider
// seam with corroborated worker ownership proves no guessed zero usage or
// hidden next-call dispatch. Ordinary sessions are outside this policy.
func TestWorkerBudgetCompactMissingReceiptRetainsAllowance(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := store.NewSessionStore(db)
	if err := s.CreateSession(store.SessionSnapshot{ID: "empty", UserID: "user", AccountScopeID: "account", Metadata: map[string]any{"worker_id": "worker", "worker_run_id": "run"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUsageLimit(store.UsageLimitRecord{AccountScopeID: "account", Enabled: true, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorker("account", "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorkerRun("account", "worker", "run"), store.WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: "account", SessionID: "empty"}); err != nil {
		t.Fatal(err)
	}
	ctx := withWorkerBudget(context.Background(), s, "account", "empty")
	r := &budgetBoundaryRunner{}
	response, err := runCompactProviderCall(ctx, r, provideriface.Request{}, nil)
	if err != nil || response.Usage.BudgetOperationID == "" {
		t.Fatalf("empty termination lost identity: %+v %v", response, err)
	}
	if err := s.ReleaseWorkerBudgetReservation("account", "empty", response.Usage.BudgetOperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runCompactProviderCall(ctx, r, provideriface.Request{}, nil); !errors.Is(err, store.ErrWorkerBudget) || r.calls != 1 {
		t.Fatalf("missing receipt admitted more work: %v", err)
	}
	if _, found, err := s.GetDailyUsageAccumulator("account", time.Now().UTC().Format("2006-01-02")); err != nil || found {
		t.Fatalf("fabricated usage: %v %v", found, err)
	}
}
