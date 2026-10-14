package sandbox

import (
	"context"
	"errors"
	"testing"
)

// Purpose: identity enforcement and terminal cleanup must not downgrade review
// execution after retargeting or failed removal. Pure manager tests prove scope
// selection and failure/retry ordering without claiming Docker enforcement.
func TestReviewerIdentityAndCleanupRetry(t *testing.T) {
	defer reviewSessions.Delete("identity-test")
	checkout, source := t.TempDir(), t.TempDir()
	m := &Manager{reviews: map[string]string{checkout: source}, boxes: map[string]*box{}}
	if err := m.BindReviewSession("identity-test", checkout); err != nil {
		t.Fatal(err)
	}
	if err := m.BindReviewSession("identity-test", source); err == nil {
		t.Fatal("identity rebound")
	}
	ctx, err := WithReviewSession(context.Background(), "identity-test", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.enforceReviewIdentity(ctx, Scope{Root: source}, source); err == nil {
		t.Fatal("source override allowed")
	}
	scope, err := m.enforceReviewIdentity(ctx, Scope{Root: source}, checkout)
	if err != nil || scope.ReviewCheckout != checkout {
		t.Fatal(scope, err)
	}
	failure := errors.New("injected removal error")
	if err := m.CleanupReview(ctx, checkout, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := m.enforceReviewIdentity(ctx, Scope{}, checkout); err != nil {
		t.Fatal("registration lost after failure", err)
	}
	if err := m.CleanupReview(ctx, checkout, func() error {
		if _, err := m.enforceReviewIdentity(ctx, Scope{}, checkout); err != nil {
			t.Fatal("removed registration before removal", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.enforceReviewIdentity(ctx, Scope{}, checkout); err == nil {
		t.Fatal("terminal identity downgraded to writable")
	}
}
