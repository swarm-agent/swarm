package identity

import "context"

type principalContextKey struct{}

func ContextWithPrincipal(parent context.Context, principal Principal) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok || !principal.Valid() {
		return Principal{}, false
	}
	return principal, true
}
