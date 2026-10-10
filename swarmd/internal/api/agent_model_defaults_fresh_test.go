package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// Purpose: "use recommended models" must work on an account that has a
// connected provider but no model settings yet (its key was saved without the
// verification that normally creates them). Before, restore-defaults read the
// missing settings and answered 404 "agent model settings not found", which
// stranded the headless setup app on its models step. With a ready provider
// it now sets the recommended models up; with none it answers 409 saying a
// provider must be connected first and creates nothing. Owner:
// handleRestoreAgentModelDefaults over the real settings, catalog and provider
// services with a fixture adapter.
func TestRestoreAgentModelDefaultsSetsUpFreshAccount(t *testing.T) {
	restore := func(t *testing.T, s *Server, principal identity.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://local"+AgentModelSettingsPath+"/restore-defaults", strings.NewReader(`{}`))
		r = requestWithTestPrincipalForAccount(r, principal.UserID, principal.AccountScopeID)
		w := httptest.NewRecorder()
		s.handleRestoreAgentModelDefaults(w, r)
		return w
	}

	t.Run("ready provider", func(t *testing.T) {
		s, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: true, connected: true})
		ctx := identity.ContextWithPrincipal(context.Background(), principal)
		if _, err := s.agentModelSettings.Get(ctx); err == nil {
			t.Fatal("fixture already has model settings")
		}
		w := restore(t, s, principal)
		if w.Code != http.StatusOK {
			t.Fatalf("restore on a fresh account = %d %s", w.Code, w.Body.String())
		}
		settings, err := s.agentModelSettings.Get(ctx)
		if err != nil {
			t.Fatalf("settings not created: %v", err)
		}
		if settings.Swarm.Action.Model != "snapshot-main-model" || settings.Swarm.Plan.Model != "snapshot-plan-model" || settings.SystemAgents.Coder.Model != "snapshot-coder-model" {
			t.Fatalf("settings = %+v, want the catalog recommendations", settings)
		}
	})

	t.Run("no ready provider", func(t *testing.T) {
		s, principal := newOnboardingProviderCredentialTestServer(t, onboardingProviderTestAdapter{id: "openai", ready: false})
		w := restore(t, s, principal)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "connect a model provider first") {
			t.Fatalf("restore without a provider = %d %s", w.Code, w.Body.String())
		}
		if _, err := s.agentModelSettings.Get(identity.ContextWithPrincipal(context.Background(), principal)); err == nil {
			t.Fatal("settings were created with no ready provider")
		}
	})
}
