package executioncapacity

import (
	"context"
	"strings"
)

type leaseContextKey struct{}

// WithLease returns a new context containing the provided execution capacity lease.
func WithLease(ctx context.Context, lease Lease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if lease == nil {
		return ctx
	}
	return context.WithValue(ctx, leaseContextKey{}, lease)
}

// LeaseFromContext retrieves the execution capacity lease from ctx if and only if
// the sessionID and runID match the lease and the lease is not released. If sessionID or runID
// differ (such as in delegated subagent tasks or child sessions), it returns nil, false to prevent child
// contexts from accidentally inheriting or reusing parent execution leases.
func LeaseFromContext(ctx context.Context, sessionID, runID string) (Lease, bool) {
	if ctx == nil {
		return nil, false
	}
	val := ctx.Value(leaseContextKey{})
	if val == nil {
		return nil, false
	}
	lease, ok := val.(Lease)
	if !ok || lease == nil {
		return nil, false
	}
	if strings.TrimSpace(lease.SessionID()) != strings.TrimSpace(sessionID) ||
		strings.TrimSpace(lease.RunID()) != strings.TrimSpace(runID) {
		return nil, false
	}
	if lease.IsReleased() {
		return nil, false
	}
	return lease, true
}

// LeaseFromContextForAccount retrieves the execution capacity lease from ctx if and only if
// the accountScopeID, sessionID, and runID match the lease and the lease is not released.
func LeaseFromContextForAccount(ctx context.Context, accountScopeID, sessionID, runID string) (Lease, bool) {
	lease, ok := LeaseFromContext(ctx, sessionID, runID)
	if !ok || lease == nil {
		return nil, false
	}
	if accountScopeID != "" && strings.TrimSpace(lease.AccountScopeID()) != strings.TrimSpace(accountScopeID) {
		return nil, false
	}
	return lease, true
}

// WithoutLease returns a context stripped of any execution capacity lease.
func WithoutLease(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, leaseContextKey{}, nil)
}

// ActiveSessionLeaseFromContext allows synchronous internal compaction to borrow
// its caller's lease across a run-id change, never via a global session lookup.
func ActiveSessionLeaseFromContext(ctx context.Context, accountScopeID, sessionID string) (Lease, bool) {
	if ctx == nil {
		return nil, false
	}
	l, ok := ctx.Value(leaseContextKey{}).(Lease)
	if !ok || l == nil || l.AccountScopeID() != strings.TrimSpace(accountScopeID) || l.SessionID() != strings.TrimSpace(sessionID) || !l.IsActive() {
		return nil, false
	}
	return l, true
}

// RunLeaseFromContext is for permission gates which may store the approval on
// a parent session. The owning run id must still match the trusted context.
func RunLeaseFromContext(ctx context.Context, runID string) (Lease, bool) {
	if ctx == nil {
		return nil, false
	}
	l, ok := ctx.Value(leaseContextKey{}).(Lease)
	if !ok || l == nil || l.RunID() != strings.TrimSpace(runID) || l.IsReleased() {
		return nil, false
	}
	return l, true
}
