package api

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type onboardingProviderTestAdapter struct {
	id        string
	ready     bool
	connected bool
	message   string
}

func (a onboardingProviderTestAdapter) ID() string { return a.id }

func (a onboardingProviderTestAdapter) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{
		ID:              a.id,
		Ready:           a.ready,
		Runnable:        a.ready,
		DefaultModel:    "gpt-5.5",
		DefaultThinking: "high",
		AuthMethods: []provideriface.AuthMethod{{
			ID:             "api",
			Label:          "API key",
			CredentialType: "api",
		}},
	}, nil
}

func (a onboardingProviderTestAdapter) VerifyCredential(context.Context, provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	return provideriface.AuthVerification{Connected: a.connected, Method: "api", Message: a.message}, nil
}

func (a onboardingProviderTestAdapter) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}

func (a onboardingProviderTestAdapter) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
