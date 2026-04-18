package openrouter

import (
	"context"
	"strings"

	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Adapter struct {
	authStore *pebblestore.AuthStore
	client    *Client
}

func NewAdapter(authStore *pebblestore.AuthStore) *Adapter {
	return &Adapter{authStore: authStore, client: NewClient()}
}

func (a *Adapter) ID() string {
	return "openrouter"
}

func (a *Adapter) Status(context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("openrouter")
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:              "openrouter",
			Ready:           false,
			Reason:          "missing openrouter auth store",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     openRouterAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredential("openrouter")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Status{
			ID:              "openrouter",
			Ready:           false,
			Reason:          "missing openrouter api key",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     openRouterAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "openrouter",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     openRouterAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	client := a.client
	if client == nil {
		client = NewClient()
	}
	message, err := client.VerifyAPIKey(ctx, strings.TrimSpace(credential.APIKey))
	if err != nil {
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, err
	}
	return provideriface.AuthVerification{
		Connected: true,
		Method:    "api",
		Message:   strings.TrimSpace(message),
	}, nil
}

func openRouterAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{{
		ID:             "api",
		Label:          "API key",
		CredentialType: pebblestore.AuthTypeAPI,
		Description:    "Use an OpenRouter API key.",
	}}
}
