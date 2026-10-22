package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: refresh tolerance must never invent defaults without catalog
// recommendations. Threat: a failed refresh installs an incomplete record.
// Owners: hydrateOnboardingProviderDefaultsAttempt and ensureRecommendedAgentModels.
// This service-level negative case complements the offline pinned-catalog handler
// tests and asserts the durable store remains unchanged on resolution failure.
func TestAgentModelRecoveryWithoutCatalogFailsClosed(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying())
	s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	s.model = modelruntime.NewService(nil, nil, nil)
	p := testPrincipal()
	p.AccountScopeID = "no-catalog-account"
	req := httptest.NewRequest(http.MethodGet, AgentModelSettingsPath, nil)
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
