package api

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Obviously fake key material. Nothing here is a real provider key and no
// provider is contacted: the fake adapter below decides verification.
const (
	providerKeyTestGood = "fake-test-key-good-000000000001"
	providerKeyTestBad  = "fake-test-key-bad-000000000009"
	providerKeyTestAddr = "127.0.0.1:7781"
)

type providerKeyTestAdapter struct {
	id       string
	takesKey bool
	notReady bool
}

func (a providerKeyTestAdapter) ID() string { return a.id }

func (a providerKeyTestAdapter) Status(context.Context) (provideriface.Status, error) {
	status := provideriface.Status{ID: a.id, Ready: !a.notReady}
	if a.takesKey {
		status.AuthMethods = []provideriface.AuthMethod{{ID: "api", Label: "API key", CredentialType: "api"}}
	} else {
		status.AuthMethods = []provideriface.AuthMethod{{ID: "oauth", Label: "Sign in", CredentialType: "oauth"}}
	}
	return status, nil
}

// VerifyCredential accepts only the good fake key and, like real providers,
// echoes a partly masked key in its refusal.
func (a providerKeyTestAdapter) VerifyCredential(_ context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	key := credential.APIKey
	if key == providerKeyTestGood {
		return provideriface.AuthVerification{Connected: true, Method: "api"}, nil
	}
	masked := key
	if len(key) > 10 {
		masked = key[:6] + "****" + key[len(key)-4:]
	}
	return provideriface.AuthVerification{Connected: false, Method: "api", Message: "Incorrect API key provided: " + masked + ". Check the key."}, nil
}
