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
// the sessionID and runID match the lease. If sessionID or runID differ (such as in
// delegated subagent tasks or child sessions), it returns nil, false to prevent child
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
	return lease, true
}

// WithoutLease returns a context stripped of any execution capacity lease.
func WithoutLease(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, leaseContextKey{}, nil)
}
