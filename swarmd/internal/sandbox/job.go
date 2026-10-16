package sandbox

import "context"

type jobScopeKey struct{}

// WithJobCheckout pins tool execution to its managed checkout's OOM domain even
// when the command explicitly changes cwd to the source or another mount.
// The caller is daemon-owned workspace scope resolution, never command input.
func WithJobCheckout(ctx context.Context, checkout string) context.Context {
	m := Default()
	if m == nil {
		return ctx
	}
	scope, ok, err := m.ScopeFor(checkout)
	if err != nil || !ok || scope.JobCheckout == "" || scope.ReviewCheckout != "" {
		return ctx
	}
	return context.WithValue(ctx, jobScopeKey{}, scope)
}

func jobScope(ctx context.Context, scope Scope) Scope {
	if pinned, ok := ctx.Value(jobScopeKey{}).(Scope); ok && scope.ReviewCheckout == "" && scope.landingID == "" {
		return pinned
	}
	return scope
}
