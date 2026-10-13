package run

import (
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// attachWorkerModels gives a test Service the model catalog and per-account
// role model settings worker proposals and dispatch resolve a worker's model
// from (codex's recommended catalog model for every role). Since workers stage
// changes for review with a resolved model profile, a Service without them
// refuses with "worker model catalog unavailable".
func attachWorkerModels(t *testing.T, svc *Service, db *store.Store, account string) {
	t.Helper()
	models := model.NewService(store.NewModelStore(db), nil, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, catalog, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatalf("catalog: %v", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err := settings.PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: account, Swarm: store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	svc.model = models
	svc.agentModelSettings = agentmodelsettings.NewService(settings)
}
