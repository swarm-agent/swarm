package openai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
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
	return "openai"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("openai")
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:              "openai",
			Ready:           false,
			Reason:          "missing openai auth store",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     openAIAuthMethods(),
		}, nil
	}
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return provideriface.Status{
			ID:              "openai",
			Ready:           false,
			Reason:          "product identity is required",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     openAIAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "openai")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Status{
			ID:              "openai",
			Ready:           false,
			Reason:          "missing openai api key",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     openAIAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "openai",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     openAIAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	apiKey := strings.TrimSpace(credential.APIKey)
	if apiKey == "" {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, errors.New("openai api verification requires api_key")
	}
	client := codex.NewClient(nil)
	verification, err := client.VerifyOpenAIAPIKey(ctx, apiKey)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, err
	}
	return verification, nil
}

func openAIAuthMethods() []provideriface.AuthMethod {
	providerDefaults := defaults.MustLookup("openai")
	return []provideriface.AuthMethod{{
		ID:             "api",
		Label:          "API key",
		CredentialType: pebblestore.AuthTypeAPI,
		Description:    fmt.Sprintf("Use an OpenAI API key for the Responses API (%s).", providerDefaults.PrimaryModel),
	}}
}
