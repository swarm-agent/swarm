package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
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

func TestOnboardingIdentityBootstrapDoesNotCreateAgents(t *testing.T) {
	server, _ := newOnboardingIdentityTestServer(t, false)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newJSONSameOriginDesktopRequest(t, map[string]any{"username": "Alice", "swarm_name": "Alice Laptop"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	state, err := server.agents.ListStateForAccount("acct_onboarding_test", 2000)
	if err != nil {
		t.Fatalf("list agent state: %v", err)
	}
	if len(state.Profiles) != 0 {
		t.Fatalf("identity bootstrap created %d agents; want 0", len(state.Profiles))
	}
}

func TestOnboardingProviderCredentialVerifiesActivatesHydratesBeforeReturning(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true, message: "ok"})

	status, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{
		Provider: "openai",
		Type:     "api",
		APIKey:   "sk-test-valid",
	})
	if err != nil {
		t.Fatalf("accept onboarding provider credential: %v", err)
	}
	if !status.Active {
		t.Fatalf("credential was not active: %+v", status)
	}
	if status.Connection == nil || !status.Connection.Connected {
		t.Fatalf("credential connection = %+v", status.Connection)
	}
	if status.AutoDefaults == nil || !status.AutoDefaults.Applied || !status.AutoDefaults.GlobalModel {
		t.Fatalf("auto defaults not applied before response: %+v", status.AutoDefaults)
	}

	agents, err := server.agents.ListStateForAccount(principal.AccountScopeID, 2000)
	if err != nil {
		t.Fatalf("list hydrated agents: %v", err)
	}
	if len(agents.Profiles) == 0 {
		t.Fatal("provider acceptance returned before creating agents")
	}
	var swarmProfile *pebblestore.AgentProfile
	for i := range agents.Profiles {
		if strings.EqualFold(agents.Profiles[i].Name, "swarm") {
			swarmProfile = &agents.Profiles[i]
			break
		}
	}
	if swarmProfile == nil {
		t.Fatalf("hydrated agents missing swarm profile: %+v", agents.Profiles)
	}
	if swarmProfile.PlanProvider != "openai" || swarmProfile.PlanModel == "" || swarmProfile.AutoProvider != "openai" || swarmProfile.AutoModel == "" {
		t.Fatalf("swarm split model defaults not hydrated: %+v", *swarmProfile)
	}
	pref, err := server.model.GetPreferenceForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("get model preference: %v", err)
	}
	if pref.Provider != "openai" || strings.TrimSpace(pref.Model) == "" {
		t.Fatalf("composer/global preference not hydrated: %+v", pref)
	}
}

func TestOnboardingProviderCredentialRejectsFailedVerificationWithoutPersisting(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: false, message: "invalid api key"})

	_, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{
		Provider: "openai",
		Type:     "api",
		APIKey:   "sk-test-invalid",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error = %v, want invalid api key", err)
	}
	credentials, err := server.auth.ListCredentialsForAccount(principal.AccountScopeID, "", "", 200)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if credentials.Total != 0 {
		t.Fatalf("failed verification persisted credentials: %+v", credentials)
	}
	agents, err := server.agents.ListStateForAccount(principal.AccountScopeID, 2000)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents.Profiles) != 0 {
		t.Fatalf("failed verification hydrated agents: %+v", agents.Profiles)
	}
}

func newOnboardingProviderCredentialTestServer(t *testing.T, adapter onboardingProviderTestAdapter) (*Server, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "onboarding-provider.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	identityStore := pebblestore.NewIdentityStore(store)
	identitySvc := identity.NewService(identityStore, identity.WithIDGenerator(onboardingIdentityTestIDGenerator))
	if _, err := identitySvc.BootstrapFirstIdentity("alice"); err != nil {
		t.Fatalf("bootstrap identity: %v", err)
	}
	authSvc := auth.NewService(pebblestore.NewAuthStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	catalogSvc := model.NewCatalogService(pebblestore.NewModelCatalogStore(store))
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, catalogSvc)
	if err := modelSvc.EnsureBootDefaults(); err != nil {
		t.Fatalf("ensure model boot defaults: %v", err)
	}
	providers := registry.New(adapter)
	providers.RegisterRunner(adapter)
	hub := stream.NewHub(eventLog)
	server := NewServer(authSvc, agentSvc, modelSvc, nil, nil, nil, nil, nil, providers, nil, nil, eventLog, hub)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user_onboarding_test", AccountScopeID: "acct_onboarding_test", AccountScopeSource: identity.AccountScopeSourceServerState}
	return server, principal
}
