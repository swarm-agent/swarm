package api

import (
	"context"
	"errors"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type v3BudgetReceiptRunner struct {
	calls   int
	started chan struct{}
	finish  chan struct{}
}

func (r *v3BudgetReceiptRunner) ID() string { return "codex" }
func (r *v3BudgetReceiptRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *v3BudgetReceiptRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.started != nil {
		close(r.started)
	}
	if r.finish != nil {
		<-r.finish
	}
	return provideriface.Response{Usage: provideriface.TokenUsage{Source: "codex_api_usage", APIUsageRawPath: "response.usage", InputTokens: 7, TotalTokens: 7}}, nil
}

// Purpose: the actual V3 provider supervisor and canonical usage mutation must
// settle two consecutive attempts by exact identity, reject dispatch while a
// cancelled operation is unresolved, and retain late usage. Real temporary
// session service plus channel-controlled provider is the narrowest V3 boundary
// proof; no live provider/benchmark claims. Every asynchronous wait is bounded.
func TestWorkerBudgetV3ExactAndLateSettlement(t *testing.T) {
	server, svc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "budget-v3", "budget")
	principal := testPrincipal()
	job := sessionV3ExecutorJob{Principal: principal, SessionID: created.ID, RunID: "budget-run"}
	exec := &sessionV3Executor{server: server}
	if err := svc.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: principal.AccountScopeID, Enabled: true, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	record := func(response provideriface.Response) {
		t.Helper()
		if _, recorded, err := exec.recordProviderUsage(job, sessionV3ResolvedRuntime{}, "codex", "fixture", 1, response.Usage, time.Now().UnixMilli()); err != nil || !recorded {
			t.Fatalf("receipt: %v %v", recorded, err)
		}
		if err := svc.Store().ReleaseWorkerBudgetReservation(principal.AccountScopeID, created.ID, response.Usage.BudgetOperationID); err != nil {
			t.Fatal(err)
		}
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &v3BudgetReceiptRunner{}
	if _, err := exec.runStaleSupervisedProviderAttempt(cancelCtx, job, r, provideriface.Request{}, nil); !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatalf("preflight cancel: %v", err)
	}
	foreignJob := job
	foreignJob.Principal.UserID = "foreign-user"
	if _, err := exec.runStaleSupervisedProviderAttempt(context.Background(), foreignJob, r, provideriface.Request{}, nil); err == nil || r.calls != 0 {
		t.Fatalf("foreign V3 dispatch: %v", err)
	}
	var old provideriface.Response
	for i := 0; i < 2; i++ {
		response, err := exec.runStaleSupervisedProviderAttempt(context.Background(), job, r, provideriface.Request{Model: "fixture"}, nil)
		if err != nil || response.Usage.BudgetOperationID == "" || response.Usage.BudgetOperationID == old.Usage.BudgetOperationID {
			t.Fatalf("operation %d: %+v %v", i, response, err)
		}
		record(response)
		old = response
	}
	late := &v3BudgetReceiptRunner{started: make(chan struct{}), finish: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := exec.runStaleSupervisedProviderAttemptWithTerminationTimeout(ctx, job, late, provideriface.Request{Model: "fixture"}, nil, time.Millisecond)
		done <- err
	}()
	select {
	case <-late.started:
	case <-time.After(time.Second):
		t.Fatal("provider not started")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel wait unbounded")
	}
	record(old)
	if _, err := exec.runStaleSupervisedProviderAttempt(context.Background(), job, r, provideriface.Request{}, nil); !errors.Is(err, store.ErrWorkerBudget) || r.calls != 2 {
		t.Fatalf("overlap after old correction: %v calls=%d", err, r.calls)
	}
	close(late.finish)
	// Wait for canonical account-lock settlement through a bounded explicit read.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := svc.Store().CheckWorkerSessionBudgetWithPrice(principal.AccountScopeID, created.ID, "subscription", "next")
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrWorkerBudget) || time.Now().After(deadline) {
			t.Fatalf("late settlement: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	acc, _, err := svc.Store().GetDailyUsageAccumulator(principal.AccountScopeID, time.Now().UTC().Format("2006-01-02"))
	if err != nil || acc.TotalTokens != 21 {
		t.Fatalf("late usage lost: %+v %v", acc, err)
	}
}
