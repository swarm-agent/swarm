package api

import (
	"net/http"
	"net/http/httptest"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/registry"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Model listing must expose the runtime reviewer choice without persisting it.
// handleAgentModelSettings and controlMCPListModels own this read contract; a
// routed temporary-store test proves both transport layers and no mutation.
func TestListModelsEffectiveReviewer(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	s.providers = registry.New()
	if err := store.NewModelCatalogStore(sessions.Store().Underlying()).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: "review-fixture", Recommendations: []store.ModelCatalogRecommendation{{Role: "review"}}}); err != nil {
		t.Fatal(err)
	}
	ctx := identity.ContextWithPrincipal(httptest.NewRequest(http.MethodGet, "/", nil).Context(), testPrincipal())
	for _, explicit := range []bool{false, true} {
		if explicit {
			if _, err := s.agentModelSettings.UpdateSystemAgent(ctx, "reviewer", store.AgentModelAssignment{Provider: "codex", Model: "explicit-review", Thinking: "high"}); err != nil {
				t.Fatal(err)
			}
		}
		before, err := s.agentModelSettings.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, source, err := agentmodel.EffectiveReviewerAssignment(s.model, before)
		if err != nil {
			t.Fatal(err)
		}
		if explicit && source != "setting" || !explicit && source != "catalog" {
			t.Fatalf("unexpected source %q", source)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
		req = requestWithScopedToken(req, &store.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
		c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
		response, err := c.dispatch(http.MethodGet, AgentModelSettingsPath, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		effective := controlMCPMap(controlMCPMap(response["effective_roles"])["system-reviewer"])
		if effective["source"] != source || controlMCPMap(effective["assignment"])["model"] != want.Model {
			t.Fatalf("REST effective %+v", effective)
		}
		listed, err := controlMCPListModels(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range listed.(map[string]any)["roles"].([]map[string]any) {
			if row["role"] == "system-reviewer" {
				found = true
				if row["model"] != want.Model || row["source"] != source {
					t.Fatalf("MCP row %+v", row)
				}
			}
		}
		if !found {
			t.Fatal("missing reviewer")
		}
		after, err := s.agentModelSettings.Get(ctx)
		if err != nil || after != before {
			t.Fatalf("read mutated settings %v", err)
		}
	}
}
