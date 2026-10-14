package sandbox

import (
	"context"
	"fmt"
	"sync"
)

type reviewIdentity struct{ Session, Checkout string }
type reviewIdentityKey struct{}

var reviewSessions sync.Map

// BindReviewSession is daemon-only admission/recovery authority. A session can
// never be rebound to another checkout, including after terminal cleanup.
func (m *Manager) BindReviewSession(session, checkout string) error {
	checkout = resolveExisting(checkout)
	m.mu.Lock()
	_, ok := m.reviews[checkout]
	m.mu.Unlock()
	if session == "" || !ok {
		return fmt.Errorf("review session requires registered checkout")
	}
	actual, loaded := reviewSessions.LoadOrStore(session, checkout)
	if loaded && actual != checkout {
		return fmt.Errorf("review session checkout is immutable")
	}
	return nil
}

func WithReviewSession(ctx context.Context, session, checkout string) (context.Context, error) {
	actual, ok := reviewSessions.Load(session)
	if !ok || actual != resolveExisting(checkout) {
		return nil, fmt.Errorf("review session is not registered")
	}
	return context.WithValue(ctx, reviewIdentityKey{}, reviewIdentity{session, checkout}), nil
}

// enforceReviewIdentity chooses isolation by execution identity, never cwd.
// An absent/removed registration is a refusal, not a writable-scope fallback.
func (m *Manager) enforceReviewIdentity(ctx context.Context, scope Scope, path string) (Scope, error) {
	id, ok := ctx.Value(reviewIdentityKey{}).(reviewIdentity)
	if !ok {
		return scope, nil
	}
	if !within(resolveExisting(id.Checkout), resolveExisting(path)) {
		return Scope{}, fmt.Errorf("reviewer command target is outside its checkout")
	}
	m.mu.Lock()
	root, registered := m.reviews[id.Checkout]
	m.mu.Unlock()
	if !registered {
		return Scope{}, fmt.Errorf("review checkout is no longer registered")
	}
	scope.Root = root
	scope.ReviewCheckout = id.Checkout
	return scope, nil
}

// ReviewSessionCheckout survives cleanup as a terminal deny identity.
func ReviewSessionCheckout(session string) string {
	value, ok := reviewSessions.Load(session)
	if !ok {
		return ""
	}
	return value.(string)
}
