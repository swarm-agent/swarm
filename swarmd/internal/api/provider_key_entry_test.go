package api

import (
	"context"
	"encoding/json"

	"strings"
	"testing"
	"time"

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

type providerKeyFixture struct {
	s       *Server
	call    func(tool string, args map[string]any) (string, bool)
	account string
	now     *time.Time
	tokenID string
	revoke  func()
}

func (f providerKeyFixture) providers(t *testing.T) map[string]map[string]any {
	t.Helper()
	text, isErr := f.call("swarm_list_providers", map[string]any{})
	if isErr {
		t.Fatalf("list_providers: %s", text)
	}
	for _, secret := range []string{providerKeyTestGood, providerKeyTestBad, "0001", "0009"} {
		if strings.Contains(text, secret) {
			t.Fatalf("list_providers leaked key material %q: %s", secret, text)
		}
	}
	var out struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, entry := range out.Providers {
		byID[entry["provider"].(string)] = entry
	}
	return byID
}
