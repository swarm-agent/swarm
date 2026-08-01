package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/uisettings"
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

func seedOnboardingProviderRecommendations(t *testing.T, catalogStore *pebblestore.ModelCatalogStore, providerID string) {
	t.Helper()
	records := []pebblestore.ModelCatalogRecord{
		{Provider: providerID, Model: "snapshot-main-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "auto", Thinking: "high"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-plan-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "plan", Thinking: "xhigh"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-compact-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "compact", Thinking: "low"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-finder-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "finder", Thinking: "medium"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-coder-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "coder", Thinking: "high"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-designer-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "designer", Thinking: "medium"}}, Source: "test"},
		{Provider: providerID, Model: "snapshot-router-model", Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "router", Thinking: "low"}}, Source: "test"},
	}
	if err := catalogStore.ReplaceSnapshot(records, pebblestore.ModelCatalogMeta{LiveSnapshotVersion: "test-snapshot", ExpiresAt: 4102444800000, RecordCount: len(records), SourceURL: "test://catalog"}); err != nil {
		t.Fatalf("seed catalog recommendations: %v", err)
	}
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
	if swarmProfile.ModelMode != "split" || swarmProfile.PlanProvider != "openai" || swarmProfile.PlanModel != "snapshot-plan-model" || swarmProfile.PlanThinking != "xhigh" || swarmProfile.AutoProvider != "openai" || swarmProfile.AutoModel != "snapshot-main-model" || swarmProfile.AutoThinking != "high" {
		t.Fatalf("swarm split model defaults not hydrated from snapshot: %+v", *swarmProfile)
	}
	if swarmProfile.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || swarmProfile.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto {
		t.Fatalf("swarm plan/auto runtime defaults not restored: %+v", *swarmProfile)
	}
	if len(agents.Profiles) != 1 {
		t.Fatalf("onboarding persisted unexpected utility agent rows: %+v", agents.Profiles)
	}
	pref, err := server.model.GetPreferenceForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("get model preference: %v", err)
	}
	if pref.Provider != "openai" || pref.Model != "snapshot-main-model" || pref.Thinking != "high" {
		t.Fatalf("composer/global preference not hydrated from snapshot: %+v", pref)
	}
	profileState, err := server.modelProfiles.ListState(identity.ContextWithPrincipal(context.Background(), principal))
	if err != nil {
		t.Fatalf("list onboarding model profiles: %v", err)
	}
	if len(profileState.Profiles) != 1 || profileState.DefaultProfileID != profileState.Profiles[0].ProfileID || profileState.Profiles[0].Name != "Swarm recommended" || !profileState.Profiles[0].IsDefault {
		t.Fatalf("onboarding recommended default = %+v", profileState)
	}
	recommended := profileState.Profiles[0]
	if recommended.ModelMode != pebblestore.ModelProfileModeSplit || recommended.Plan == nil || recommended.Auto == nil || recommended.Plan.Provider != "openai" || recommended.Plan.Model != "snapshot-plan-model" || recommended.Plan.Thinking != "xhigh" || recommended.Auto.Provider != "openai" || recommended.Auto.Model != "snapshot-main-model" || recommended.Auto.Thinking != "high" {
		t.Fatalf("onboarding recommended mapping = %+v", recommended)
	}
	uiSettings, err := server.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("get onboarding system-agent settings: %v", err)
	}
	wantSystemAgents := map[string]struct {
		got      uisettings.CompactAgentSettings
		model    string
		thinking string
	}{
		"compact":  {uiSettings.Agents.Compact, "snapshot-compact-model", "low"},
		"finder":   {uiSettings.Agents.Finder, "snapshot-finder-model", "medium"},
		"coder":    {uiSettings.Agents.Coder, "snapshot-coder-model", "high"},
		"designer": {uiSettings.Agents.Designer, "snapshot-designer-model", "medium"},
		"router":   {uiSettings.Agents.Router, "snapshot-router-model", "low"},
	}
	for name, want := range wantSystemAgents {
		if want.got.Provider != "openai" || want.got.Model != want.model || want.got.Thinking != want.thinking {
			t.Fatalf("%s onboarding settings = %+v, want openai/%s/%s", name, want.got, want.model, want.thinking)
		}
	}
}

func TestOnboardingProviderCredentialRejectsLaterCredentialWithoutOverwritingPreferences(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true, message: "ok"})
	if _, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{Provider: "openai", Type: "api", APIKey: "sk-first"}); err != nil {
		t.Fatalf("accept first onboarding credential: %v", err)
	}
	before, err := server.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("read first onboarding settings: %v", err)
	}

	if _, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{Provider: "openai", Type: "api", APIKey: "sk-second"}); err == nil || !strings.Contains(err.Error(), "zero existing credentials") {
		t.Fatalf("second onboarding credential error = %v, want zero-existing-credentials rejection", err)
	}
	after, err := server.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("read settings after rejected credential: %v", err)
	}
	if after.Agents != before.Agents {
		t.Fatalf("later onboarding credential overwrote subagent preferences: before=%+v after=%+v", before.Agents, after.Agents)
	}
}

func TestOnboardingProviderCredentialPreservesExistingModelProfileAndDefault(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true, message: "ok"})
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	selection := modelprofile.Selection{Provider: "openai", Model: "existing-model", Thinking: "medium", ContextMode: "full"}
	existing, err := server.modelProfiles.Create(ctx, modelprofile.Input{Name: "My default", ModelMode: pebblestore.ModelProfileModeSingle, Single: &selection})
	if err != nil {
		t.Fatalf("create existing profile: %v", err)
	}

	if _, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{Provider: "openai", Type: "api", APIKey: "sk-test-valid"}); err != nil {
		t.Fatalf("accept onboarding provider credential: %v", err)
	}
	state, err := server.modelProfiles.ListState(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(state.Profiles) != 1 || state.DefaultProfileID != existing.ProfileID || state.Profiles[0].Name != "My default" || !state.Profiles[0].IsDefault {
		t.Fatalf("onboarding replaced existing profile/default: %+v", state)
	}
}

func TestOnboardingProviderCredentialHydratesPreferredProviderEvenWhenStatusIsNotYetRunnable(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "fireworks", ready: false, connected: true, message: "ok"})

	status, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{
		Provider: "fireworks",
		Type:     "api",
		APIKey:   "sk-test-valid",
	})
	if err != nil {
		t.Fatalf("accept fireworks onboarding provider credential: %v", err)
	}
	if status.AutoDefaults == nil || !status.AutoDefaults.Applied || status.AutoDefaults.Provider != "fireworks" || status.AutoDefaults.Model != "snapshot-main-model" || status.AutoDefaults.UtilityModel != "" {
		t.Fatalf("fireworks defaults not applied from catalog: %+v", status.AutoDefaults)
	}
}

func TestOnboardingProviderCredentialHydratesProviderFromSnapshotWithoutLegacyDefaults(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "snapshot-only", ready: true, connected: true, message: "ok"})

	status, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{
		Provider: "snapshot-only",
		Type:     "api",
		APIKey:   "sk-test-valid",
	})
	if err != nil {
		t.Fatalf("accept snapshot-only onboarding provider credential: %v", err)
	}
	if status.AutoDefaults == nil || !status.AutoDefaults.Applied || status.AutoDefaults.Provider != "snapshot-only" || status.AutoDefaults.Model != "snapshot-main-model" || status.AutoDefaults.UtilityModel != "" {
		t.Fatalf("snapshot-only defaults not applied from catalog: %+v", status.AutoDefaults)
	}
	agents, err := server.agents.ListStateForAccount(principal.AccountScopeID, 2000)
	if err != nil {
		t.Fatalf("list hydrated agents: %v", err)
	}
	var swarmProfile *pebblestore.AgentProfile
	for i := range agents.Profiles {
		if strings.EqualFold(agents.Profiles[i].Name, "swarm") {
			swarmProfile = &agents.Profiles[i]
			break
		}
	}
	if swarmProfile == nil || swarmProfile.PlanModel != "snapshot-plan-model" || swarmProfile.AutoModel != "snapshot-main-model" {
		t.Fatalf("snapshot-only swarm split defaults not hydrated: %+v", swarmProfile)
	}
}

func TestOnboardingProviderCredentialRequiresSnapshotRecommendationsAndRollsBack(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServerWithoutRecommendations(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true, message: "ok"})

	_, err := server.acceptFirstOnboardingProviderCredential(context.Background(), principal, onboardingProviderCredentialRequest{
		Provider: "openai",
		Type:     "api",
		APIKey:   "sk-test-valid",
	})
	if err == nil || !strings.Contains(err.Error(), "missing required snapshot recommendations") {
		t.Fatalf("error = %v, want missing required snapshot recommendations", err)
	}
	credentials, err := server.auth.ListCredentialsForAccount(principal.AccountScopeID, "", "", 200)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if credentials.Total != 0 {
		t.Fatalf("missing recommendations persisted credentials: %+v", credentials)
	}
	agents, err := server.agents.ListStateForAccount(principal.AccountScopeID, 2000)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents.Profiles) != 0 {
		t.Fatalf("missing recommendations hydrated agents: %+v", agents.Profiles)
	}
	profiles, profileErr := server.modelProfiles.ListState(identity.ContextWithPrincipal(context.Background(), principal))
	if profileErr != nil || len(profiles.Profiles) != 0 || profiles.DefaultProfileID != "" {
		t.Fatalf("missing recommendations persisted model profile: %+v err=%v", profiles, profileErr)
	}
}

func TestOnboardingProviderCredentialEndpointAcceptsDesktopActiveField(t *testing.T) {
	server, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true, message: "ok"})
	payload := map[string]any{
		"provider": "openai",
		"type":     "api",
		"api_key":  "sk-test-valid",
		"active":   true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5555/v1/onboarding/provider/credential", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/onboarding/provider/credential status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status auth.CredentialStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !status.Active || status.AutoDefaults == nil || !status.AutoDefaults.Applied {
		t.Fatalf("credential not activated and hydrated: %+v", status)
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
	profiles, profileErr := server.modelProfiles.ListState(identity.ContextWithPrincipal(context.Background(), principal))
	if profileErr != nil || len(profiles.Profiles) != 0 || profiles.DefaultProfileID != "" {
		t.Fatalf("failed verification persisted model profile: %+v err=%v", profiles, profileErr)
	}
}

func newOnboardingProviderCredentialTestServer(t *testing.T, adapter onboardingProviderTestAdapter) (*Server, identity.Principal) {
	t.Helper()
	return newOnboardingProviderCredentialTestServerWithCatalog(t, adapter, true)
}

func newOnboardingProviderCredentialTestServerWithoutRecommendations(t *testing.T, adapter onboardingProviderTestAdapter) (*Server, identity.Principal) {
	t.Helper()
	return newOnboardingProviderCredentialTestServerWithCatalog(t, adapter, false)
}

func newOnboardingProviderCredentialTestServerWithCatalog(t *testing.T, adapter onboardingProviderTestAdapter, seedRecommendations bool) (*Server, identity.Principal) {
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
	catalogStore := pebblestore.NewModelCatalogStore(store)
	if seedRecommendations {
		seedOnboardingProviderRecommendations(t, catalogStore, adapter.id)
	}
	catalogSvc := model.NewCatalogService(catalogStore)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, catalogSvc)
	providers := registry.New(adapter)
	providers.RegisterRunner(adapter)
	hub := stream.NewHub(eventLog)
	server := NewServer(authSvc, agentSvc, modelSvc, nil, nil, nil, nil, nil, providers, nil, nil, eventLog, hub)
	server.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(store)))
	server.uiSettings = uisettings.NewService(pebblestore.NewUISettingsStore(store))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user_onboarding_test", AccountScopeID: "acct_onboarding_test", AccountScopeSource: identity.AccountScopeSourceServerState}
	return server, principal
}
