package google

import (
	"context"
	"strings"

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
	return "google"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("google")
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return provideriface.Status{
			ID:              "google",
			Ready:           false,
			Reason:          "product identity is required",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     googleAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || !recordReady(record) {
		return provideriface.Status{
			ID:              "google",
			Ready:           false,
			Reason:          "missing google auth",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     googleAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "google",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     googleAuthMethods(),
	}, nil
}

func recordReady(record pebblestore.AuthCredentialRecord) bool {
	return strings.TrimSpace(record.APIKey) != ""
}

func googleAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "api",
			Label:          "API key",
			CredentialType: "api",
			Description:    "Use a Google API key.",
		},
	}
}
