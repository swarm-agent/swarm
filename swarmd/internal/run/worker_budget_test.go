package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type budgetBoundaryRunner struct{ calls int }

func (r *budgetBoundaryRunner) ID() string { return "fixture" }
func (r *budgetBoundaryRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{}, nil
}
func (r *budgetBoundaryRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{}, nil
}

// Purpose: unknown-price and foreign-principal calls must never reach the
// provider, including the Compact boundary. Owners checkProviderWorkerBudget,
// runProviderAttempt, runCompactProviderCall. A counting runner and temp store
// prove dispatch suppression (not simulated billing or a benchmark).
func TestWorkerBudgetProviderBoundary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := store.NewSessionStore(db)
	account, session := "account", "budget-session"
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeySession(session), store.SessionSnapshot{ID: session, AccountScopeID: account, UserID: "user", Metadata: map[string]any{"worker_id": "worker", "worker_run_id": "run"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorkerRun(account, "worker", "run"), store.WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: account, SessionID: session}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget(account, "worker", 0, 1, 100); err != nil {
		t.Fatal(err)
	}
	ctx := withWorkerBudget(context.Background(), s, account, session)
	runner := &budgetBoundaryRunner{}
	if _, err := runProviderAttempt(ctx, runner, provideriface.Request{Model: "unpriced"}, 0, nil); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := runCompactProviderCall(ctx, runner, provideriface.Request{Model: "unpriced"}, nil); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("compact unknown: %v", err)
	}
	foreign := identity.ContextWithPrincipal(ctx, identity.Principal{AccountScopeID: "foreign", UserID: "user", Type: identity.PrincipalTypeUser})
	if _, err := runProviderAttempt(foreign, runner, provideriface.Request{}, 0, nil); err == nil {
		t.Fatal("foreign provider call accepted")
	}
	foreignUser := identity.ContextWithPrincipal(ctx, identity.Principal{AccountScopeID: account, UserID: "other-user", Type: identity.PrincipalTypeUser})
	if _, err := runProviderAttempt(foreignUser, runner, provideriface.Request{}, 0, nil); err == nil {
		t.Fatal("foreign user provider call accepted")
	}
	if runner.calls != 0 {
		t.Fatalf("unauthorized provider calls: %d", runner.calls)
	}
	policy, err := s.GetWorkerBudget(account, "worker")
	if err != nil || policy.Revision != 1 {
		t.Fatalf("rejections changed policy: %+v %v", policy, err)
	}
}

// Purpose: cancelled-before-dispatch operations cannot strand allowance, and
// available late provider receipts retain the exact operation identity. Owners
// checkProviderWorkerBudget/retainLateProviderReceipt; deterministic channels
// exercise the narrow provider boundary without provider/network billing.
func TestWorkerBudgetCancelledAndLateReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &budgetBoundaryRunner{}
	if _, err := runProviderAttempt(ctx, runner, provideriface.Request{}, 0, nil); !errors.Is(err, context.Canceled) || runner.calls != 0 {
		t.Fatalf("cancel dispatch: %d %v", runner.calls, err)
	}
	results := make(chan providerAttemptResult, 1)
	received := make(chan provideriface.Response, 1)
	ctx = withLateProviderReceipt(context.Background(), func(response provideriface.Response) error { received <- response; return nil })
	retainLateProviderReceipt(ctx, results)
	results <- providerAttemptResult{response: provideriface.Response{Usage: provideriface.TokenUsage{BudgetOperationID: "exact-attempt", TotalTokens: 7}}}
	select {
	case response := <-received:
		if response.Usage.BudgetOperationID != "exact-attempt" || response.Usage.TotalTokens != 7 {
			t.Fatalf("late evidence: %+v", response.Usage)
		}
	case <-time.After(time.Second):
		t.Fatal("late receipt callback not invoked")
	}
}

// Purpose: metadata preparation is an actual unmetered utility dispatch and
// cannot bypass account policy when invoked without a budget context or session.
// Owners PrepareAITaskMetadata/CheckWorkerUnmeteredOperation; existing compiled
// model fixture plus a counting provider is the narrowest no-dispatch proof.
func TestWorkerBudgetAITaskMetadataAccountBoundary(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	runner := &principalCapturingAITaskRunner{}
	svc.providers = registry.New()
	svc.providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account"}
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	if _, err := svc.agentModelSettings.UpdateSystemAgent(ctx, store.SystemAgentCompact, store.AgentModelAssignment{Provider: "codex", Model: "gpt-5.4", Thinking: "medium", ServiceTier: "fast"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.sessions.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: principal.AccountScopeID, Enabled: true, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PrepareAITaskMetadata(context.Background(), "task", "request", store.ModelPreference{Provider: "codex", Model: "gpt-5.4"}, principal); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("metadata bypass: %v", err)
	}
	if runner.request.Model != "" {
		t.Fatal("capped metadata dispatched")
	}
}

// Purpose: actual worker dispatch must create corroborated ownership before any
// provider call, including a retained intent after wake failure/retry. Owners
// WorkerExecutionService.Dispatch/AutomationV2ExecutionHost.Prepare and the
// canonical budget store; real temporary Git/Pebble fixture is the narrowest
// preparation-boundary test, with a bounded fake enqueue (not live telemetry).
func TestWorkerBudgetDispatchBindingBeforeProvider(t *testing.T) {
	wake := false
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return wake })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	worker, err := ss.Store().WorkerStore().CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "budget", Instructions: "Review", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = execution.Activate("account", "owner", worker.ID, worker.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Store().SetWorkerBudget("account", worker.ID, 0, 1, 100); err != nil {
		t.Fatal(err)
	}
	request := store.WorkerRunAdmission{WorkerID: worker.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "budget-dispatch"}
	receipt, err := execution.Dispatch(ctx, "account", "owner", request)
	if err == nil {
		t.Fatal("wake failure hidden")
	}
	snapshot, found, err := ss.GetSession(receipt.SessionID)
	if err != nil || !found || snapshot.Metadata["worker_id"] != worker.ID || snapshot.Metadata["worker_run_id"] != receipt.ID {
		t.Fatalf("unbound dispatch: %+v %v", snapshot, err)
	}
	if err := ss.Store().CheckWorkerSessionBudgetWithPrice("account", receipt.SessionID, "unknown", "first"); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("prepared budget bypass: %v", err)
	}
	wake = true
	replay, err := execution.Dispatch(ctx, "account", "owner", request)
	if err != nil || replay.ID != receipt.ID {
		t.Fatalf("retry: %+v %v", replay, err)
	}
	if err := ss.Store().CheckWorkerSessionBudgetWithPrice("account", replay.SessionID, "unknown", "retry"); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("retry budget bypass: %v", err)
	}
}

// Purpose: directly invoking the title utility must not bypass its outer
// generateAndApply guard. generateMemorySessionTitle owns dispatch; the existing
// model/service fixture proves account-only caps deny before provider access.
func TestWorkerBudgetDirectTitleBoundary(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	runner := &principalCapturingAITaskRunner{}
	svc.providers = registry.New()
	svc.providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account"}
	if err := svc.sessions.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: principal.AccountScopeID, Enabled: true, DailyTokensLimit: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.generateMemorySessionTitle("context", "provisional", 2, 4, store.ModelPreference{}, store.AgentProfile{}, principal); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("direct title bypass: %v", err)
	}
	if runner.request.Model != "" {
		t.Fatal("capped title dispatched")
	}
}
