package google

import (
	"context"
	"strings"

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

func (a *Adapter) Status(context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("google")
	record, ok, err := a.authStore.GetActiveCredential("google")
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
