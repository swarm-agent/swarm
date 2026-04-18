package anthropic

import (
	"context"
	"fmt"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"

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
	return "anthropic"
}

func (a *Adapter) Status(context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("anthropic")
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:              "anthropic",
			Ready:           false,
			Reason:          "missing anthropic auth store",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     anthropicAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredential("anthropic")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Status{
			ID:              "anthropic",
			Ready:           false,
			Reason:          "missing anthropic api key",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     anthropicAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "anthropic",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     anthropicAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	apiKey := strings.TrimSpace(credential.APIKey)
	if apiKey == "" {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, fmt.Errorf("anthropic api verification requires api_key")
	}
	client := anthropicapi.NewClient(anthropicClientOptions(apiKey)...)
	_, err := client.Models.List(ctx, anthropicapi.ModelListParams{Limit: anthropicapi.Int(1)})
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, err
	}
	return provideriface.AuthVerification{
		Connected: true,
		Method:    "api",
		Message:   "Anthropic API key verified via /v1/models",
	}, nil
}

func anthropicAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{{
		ID:             "api",
		Label:          "API key",
		CredentialType: pebblestore.AuthTypeAPI,
		Description:    "Use an Anthropic API key.",
	}}
}
