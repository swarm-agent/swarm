package fireworks

import (
	"context"
	"fmt"
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
	return "fireworks"
}

func (a *Adapter) Status(context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("fireworks")
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:              "fireworks",
			Ready:           false,
			Reason:          "missing fireworks auth store",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     fireworksAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredential("fireworks")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Status{
			ID:              "fireworks",
			Ready:           false,
			Reason:          "missing fireworks api key",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     fireworksAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "fireworks",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     fireworksAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	client := &Client{}
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

func fireworksAuthMethods() []provideriface.AuthMethod {
	providerDefaults := defaults.MustLookup("fireworks")
	return []provideriface.AuthMethod{{
		ID:             "api",
		Label:          "API key",
		CredentialType: "api",
		Description:    fmt.Sprintf("Use a Fireworks API key for %s.", providerDefaults.PrimaryModel),
	}}
}
