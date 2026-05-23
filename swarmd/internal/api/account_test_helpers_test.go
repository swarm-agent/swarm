package api

import (
	"context"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

const (
	testUserID         = "test-user"
	testAccountScopeID = "test-account"
)

func requestWithTestPrincipal(r *http.Request) *http.Request {
	return requestWithTestPrincipalForAccount(r, testUserID, testAccountScopeID)
}

func requestWithTestPrincipalForAccount(r *http.Request, userID, accountScopeID string) *http.Request {
	if r == nil {
		return nil
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             userID,
		AccountScopeID:     accountScopeID,
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
	ctx = identity.ContextWithPrincipal(ctx, principal)
	return r.WithContext(ctx)
}
