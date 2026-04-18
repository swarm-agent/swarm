package codex

import (
	"context"

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

func (a *Adapter) Status(context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("codex")
	record, ok, err := a.authStore.GetCodexAuthRecord()
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
	switch record.Type {
	case pebblestore.CodexAuthTypeOAuth:
		return record.AccessToken != "" && record.RefreshToken != ""
	default:
		return record.APIKey != ""
	}
}

func codexAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "api",
			Label:          "API key",
			CredentialType: "api",
			Description:    "Use a Codex API key.",
		},
		{
			ID:             "oauth",
			Label:          "OAuth token pair",
			CredentialType: "oauth",
			Description:    "Use access + refresh tokens from Codex OAuth.",
		},
	}
}
