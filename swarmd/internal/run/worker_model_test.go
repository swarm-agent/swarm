package run

import (
	"swarm/packages/swarmd/internal/model"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// ResolveWorkerRoleModelProfile must retain explicit reviewer settings and use
// the shared review recommendation otherwise. A temporary catalog/settings store
// proves the resolved worker slots, including the no-pick fallback, without a provider.
func TestWorkerReviewerModelProfile(t *testing.T) {
	s, settingsStore, db := taskFallbackFixture(t)
	catalog := model.NewCatalogService(store.NewModelCatalogStore(db))
	catalog.UseEmbeddedOnly()
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	s.model = model.NewService(store.NewModelStore(db), nil, catalog)
	records := store.NewModelCatalogStore(db)
	for _, name := range []string{"coder-choice", "review-choice", "explicit-choice"} {
		record := store.ModelCatalogRecord{Provider: "fixture-provider", Model: name, ThinkingOptions: []string{"low", "high"}, DefaultThinking: "low", ServiceTiers: []string{"priority"}}
		if name == "review-choice" {
			record.Recommendations = []store.ModelCatalogRecommendation{{Role: "review", Thinking: "high", Serving: "fast"}}
		}
		if err := records.SetRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	settings := taskFallbackSettings(t, settingsStore, "account-a", "coder-choice", "coder-choice")
	for _, explicit := range []bool{false, true} {
		wantModel, wantThinking, wantTier := "review-choice", "high", "priority"
		if explicit {
			settings.SystemAgents.Reviewer = store.AgentModelAssignment{Provider: "fixture-provider", Model: "explicit-choice", Thinking: "low"}
			if _, err := settingsStore.PutForAccount(settings); err != nil {
				t.Fatal(err)
			}
			wantModel, wantThinking, wantTier = "explicit-choice", "low", ""
		}
		got, err := s.ResolveWorkerRoleModelProfile("account-a", store.WorkerRoleReviewer, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Action.Model != wantModel || got.Action.Thinking != wantThinking || got.Action.ServiceTier != wantTier || *got.Plan != got.Action || got.ResolutionWarning != "" {
			t.Fatalf("profile %+v", got)
		}
	}
	settings.SystemAgents.Reviewer = store.AgentModelAssignment{}
	if _, err := settingsStore.PutForAccount(settings); err != nil {
		t.Fatal(err)
	}
	if err := records.SetRecord(store.ModelCatalogRecord{Provider: "fixture-provider", Model: "review-choice"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveWorkerRoleModelProfile("account-a", store.WorkerRoleReviewer, nil)
	if err != nil || got.Action.Model != settings.SystemAgents.Coder.Model || got.ResolutionWarning != "" {
		t.Fatalf("fallback %+v %v", got, err)
	}
}
