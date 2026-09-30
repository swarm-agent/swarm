package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type budgetBoundaryRunner struct { calls int }
func (r *budgetBoundaryRunner) ID() string { return "fixture" }
func (r *budgetBoundaryRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) { r.calls++; return provideriface.Response{}, nil }
func (r *budgetBoundaryRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) { r.calls++; return provideriface.Response{}, nil }

// Purpose: unknown-price and foreign-principal calls must never reach the
// provider, including the Compact boundary. Owners checkProviderWorkerBudget,
// runProviderAttempt, runCompactProviderCall. A counting runner and temp store
// prove dispatch suppression (not simulated billing or a benchmark).
func TestWorkerBudgetProviderBoundary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil { t.Fatal(err) }; defer db.Close()
	s := store.NewSessionStore(db)
	account, session := "account", "budget-session"
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil { t.Fatal(err) }
	if err := db.PutJSON(store.KeySession(session), store.SessionSnapshot{ID: session, AccountScopeID: account, UserID: "user", Metadata: map[string]any{"worker_id": "worker", "worker_run_id": "run"}}); err != nil { t.Fatal(err) }
	if err := db.PutJSON(store.KeyWorkerRun(account, "worker", "run"), store.WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: account, SessionID: session}); err != nil { t.Fatal(err) }
	if _, err := s.SetWorkerBudget(account, "worker", 0, 1, 100); err != nil { t.Fatal(err) }
	ctx := withWorkerBudget(context.Background(), s, account, session)
	runner := &budgetBoundaryRunner{}
	if _, err := runProviderAttempt(ctx, runner, provideriface.Request{Model: "unpriced"}, 0, nil); !errors.Is(err, store.ErrWorkerBudget) { t.Fatalf("unknown: %v", err) }
	if _, err := runCompactProviderCall(ctx, runner, provideriface.Request{Model: "unpriced"}, nil); !errors.Is(err, store.ErrWorkerBudget) { t.Fatalf("compact unknown: %v", err) }
	foreign := identity.ContextWithPrincipal(ctx, identity.Principal{AccountScopeID: "foreign", UserID: "user", Type: identity.PrincipalTypeUser})
	if _, err := runProviderAttempt(foreign, runner, provideriface.Request{}, 0, nil); err == nil { t.Fatal("foreign provider call accepted") }
	foreignUser := identity.ContextWithPrincipal(ctx, identity.Principal{AccountScopeID: account, UserID: "other-user", Type: identity.PrincipalTypeUser})
	if _, err := runProviderAttempt(foreignUser, runner, provideriface.Request{}, 0, nil); err == nil { t.Fatal("foreign user provider call accepted") }
	if runner.calls != 0 { t.Fatalf("unauthorized provider calls: %d", runner.calls) }
	policy, err := s.GetWorkerBudget(account, "worker")
	if err != nil || policy.Revision != 1 { t.Fatalf("rejections changed policy: %+v %v", policy, err) }
}
