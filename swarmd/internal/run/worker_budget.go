package run

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workerBudgetContextKey struct{}
type workerBudgetContext struct {
	repository       *store.SessionStore
	account, session string
	mu               sync.Mutex
	operation        string
}

func withWorkerBudget(ctx context.Context, repository *store.SessionStore, account, session string) context.Context {
	return context.WithValue(ctx, workerBudgetContextKey{}, &workerBudgetContext{repository: repository, account: account, session: session})
}

// Each provider attempt, including a retry, checks immediately before dispatch.
// Keep the reservation until the caller has persisted usage and terminated.
func checkProviderWorkerBudget(ctx context.Context, runner provideriface.Runner, req provideriface.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	budget, ok := ctx.Value(workerBudgetContextKey{}).(*workerBudgetContext)
	if !ok {
		return nil
	}
	if principal, found := identity.PrincipalFromContext(ctx); found {
		snapshot, exists, err := budget.repository.GetSession(budget.session)
		if err != nil {
			return err
		}
		if !exists || snapshot.AccountScopeID != budget.account || principal.AccountScopeID != budget.account || principal.UserID != snapshot.UserID {
			return errors.New("worker budget principal mismatch")
		}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	operation := uuid.NewString()
	if err := budget.repository.CheckWorkerSessionBudget(budget.account, budget.session, runner.ID(), req.Model, operation); err != nil {
		return err
	}
	budget.operation = operation
	return nil
}

func releaseProviderWorkerBudget(ctx context.Context) error {
	budget, ok := ctx.Value(workerBudgetContextKey{}).(*workerBudgetContext)
	if !ok {
		return nil
	}
	return budget.repository.ReleaseWorkerBudgetReservation(budget.account, budget.session, providerBudgetOperation(ctx))
}

func providerBudgetOperation(ctx context.Context) string {
	budget, ok := ctx.Value(workerBudgetContextKey{}).(*workerBudgetContext)
	if !ok {
		return ""
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.operation
}

type lateProviderReceiptKey struct{}
type lateProviderReceiptCallback func(provideriface.Response) error

func withLateProviderReceipt(ctx context.Context, callback lateProviderReceiptCallback) context.Context {
	return context.WithValue(ctx, lateProviderReceiptKey{}, callback)
}
