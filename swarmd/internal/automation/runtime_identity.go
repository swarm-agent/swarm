package automation

import (
	"context"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type runtimeIdentityKey struct{}
type runtimeIdentity struct {
	principal Principal
	explicit bool
}

// BindRuntimeIdentity is only for authenticated transport and daemon adapters.
// Callers must supply the verified identity, never fields decoded from JSON.
// Agent subjects are canonical execution session IDs; system subjects are the
// approving user resolved from the durable grant. Neither origin grants approval.
func BindRuntimeIdentity(ctx context.Context, verified identity.Principal, origin, sessionID string) (context.Context, error) {
	if !verified.Valid() { return nil, ErrDenied }
	p := Principal{AccountID: verified.AccountScopeID, SubjectID: verified.UserID, Role: origin}
	switch origin {
	case "user", "system":
		if sessionID != "" { return nil, ErrDenied }
	case "agent":
		if sessionID == "" { return nil, ErrDenied }
		p.SubjectID = sessionID
	default:
		return nil, ErrDenied
	}
	return context.WithValue(ctx, runtimeIdentityKey{}, runtimeIdentity{p, origin == "user"}), nil
}

func RuntimeApprovalIdentity() ApprovalIdentity {
	return ApprovalIdentity{Current: RuntimePrincipal, ExplicitUser: func(ctx context.Context) (Principal, error) {
		v, ok := ctx.Value(runtimeIdentityKey{}).(runtimeIdentity)
		if !ok || !v.explicit || v.principal.Role != "user" { return Principal{}, ErrDenied }
		return v.principal, nil
	}}
}

func RuntimePrincipal(ctx context.Context) (Principal, error) {
	v, ok := ctx.Value(runtimeIdentityKey{}).(runtimeIdentity)
	if !ok { return Principal{}, ErrDenied }
	return v.principal, nil
}

// RuntimeTriggers deliberately has no event-string authentication fallback.
// A future event adapter must verify an exact payload-bound credential.
type RuntimeTriggers struct{}
func (RuntimeTriggers) Verify(ctx context.Context, p Principal, scope store.AutomationScope, trigger Trigger) error {
	actual, err := RuntimePrincipal(ctx)
	if err != nil || actual != p || p.AccountID != scope.AccountID || scope.WorkspaceID == "" || trigger.Source != "" { return ErrDenied }
	if trigger.Kind == "manual" && p.Role == "user" { return nil }
	if trigger.Kind == "schedule" && p.Role == "system" { return nil }
	return ErrDenied
}
