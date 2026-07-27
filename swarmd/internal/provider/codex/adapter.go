package codex

import (
	"context"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Adapter struct {
	authStore *pebblestore.AuthStore
}

func NewAdapter(authStore *pebblestore.AuthStore) *Adapter {
	return &Adapter{authStore: authStore}
}

func (a *Adapter) ID() string {
	return "codex"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("codex")
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return provideriface.Status{
			ID:              "codex",
			Ready:           false,
			Reason:          "product identity is required",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     codexAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetCodexAuthRecordForAccount(principal.AccountScopeID)
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || !recordReady(record) {
		return provideriface.Status{
			ID:              "codex",
			Ready:           false,
			Reason:          "missing codex auth",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     codexAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "codex",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     codexAuthMethods(),
	}, nil
}

func recordReady(record pebblestore.CodexAuthRecord) bool {
	return record.Type == pebblestore.CodexAuthTypeOAuth && record.AccessToken != "" && record.RefreshToken != ""
}

func codexAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "oauth",
			Label:          "OAuth token pair",
			CredentialType: "oauth",
			Description:    "Use access + refresh tokens from Codex OAuth.",
		},
	}
}
