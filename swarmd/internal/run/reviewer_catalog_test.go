package run

import (
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: ResolveRoleModelProfile selects catalog review recommendations when
// present, coder otherwise, and honors explicit canonical settings. Real catalog
// and account stores are the narrowest resolution-authority proof.
func TestReviewerCatalogDefault(t *testing.T) {
	_, ss, s, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	db := ss.Store().Underlying()
	settings, err := s.host.runs.agentModelSettings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveRoleModelProfile("account", store.WorkerRoleReviewer, nil)
	if err != nil || p.Action.Model != settings.SystemAgents.Coder.Model {
		t.Fatal(p, err)
	}
	catalog := store.NewModelCatalogStore(db)
	record, found, err := catalog.GetRecord(settings.SystemAgents.Coder.Provider, settings.SystemAgents.Coder.Model)
	if err != nil || !found {
		t.Fatal(err)
	}
	record.Model = "review-fixture"
	record.Recommendations = []store.ModelCatalogRecommendation{{Role: "review", Thinking: record.DefaultThinking}}
	if err := catalog.SetRecord(record); err != nil {
		t.Fatal(err)
	}
	p, err = s.ResolveRoleModelProfile("account", store.WorkerRoleReviewer, nil)
	if err != nil || p.Action.Model != "review-fixture" {
		t.Fatal(p, err)
	}
	_, err = store.NewAgentModelSettingsStore(db).UpdateSystemAgentForAccount("account", "system-reviewer", settings.SystemAgents.Coder, 123)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ResolveRoleModelProfile("account", store.WorkerRoleReviewer, nil)
	if err != nil || p.Action.Model != settings.SystemAgents.Coder.Model {
		t.Fatal(p, err)
	}
}
