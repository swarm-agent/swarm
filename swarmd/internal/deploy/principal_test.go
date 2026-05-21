package deploy

import (
	"context"

	"swarm/packages/swarmd/internal/identity"
)

func testPrincipal() identity.Principal {
	return identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             "user-1",
		AccountScopeID:     "account-1",
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
}

func testPrincipalContext() context.Context {
	return identity.ContextWithPrincipal(context.Background(), testPrincipal())
}
