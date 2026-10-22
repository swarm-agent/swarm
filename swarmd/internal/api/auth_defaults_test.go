package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: refresh tolerance must never invent defaults without catalog
// recommendations. Threat: a failed refresh installs an incomplete record.
// Owners: hydrateOnboardingProviderDefaultsAttempt and ensureRecommendedAgentModels.
// This PATCH handler negative case is the narrowest layer proving repair rejects
// missing recommendations and leaves the durable store unchanged.
func TestAgentModelRecoveryWithoutCatalogFailsClosed(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying())
	s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	s.model = modelruntime.NewService(nil, nil, nil)
	p := testPrincipal()
	p.AccountScopeID = "no-catalog-account"
	req := httptest.NewRequest(http.MethodPatch, AgentModelSettingsPath, strings.NewReader(`{"system_agents":{"coder":{"provider":"codex","model":"requested-model","thinking":"high"}}}`))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	s.handleAgentModelSettings(rec, req)
	if rec.Code < 400 {
		t.Fatalf("missing recommendations succeeded: %s", rec.Body.String())
	}
	if _, err := s.agentModelSettings.Get(req.Context()); err == nil {
		t.Fatal("installed defaults without recommendations")
	}
}
