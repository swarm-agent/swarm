package agentmodel

import (
	"path/filepath"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Native reviewer identities must share catalog defaults without overriding an
// explicit setting or losing the coder fallback. configuredAgentSettingsForID
// is the narrow selection boundary; real embedded catalog data avoids network.
func TestNativeReviewerAssignment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog := model.NewCatalogService(store.NewModelCatalogStore(db))
	catalog.UseEmbeddedOnly()
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := store.NewModelCatalogStore(db).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: "review-fixture", DefaultThinking: "high", Recommendations: []store.ModelCatalogRecommendation{{Role: "review", Serving: "fast"}}}); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), nil, catalog)
	settings := agentmodelsettings.Settings{}
	settings.SystemAgents.Coder = agentmodelsettings.Assignment{Provider: "codex", Model: "coder-choice", Thinking: "low", ServiceTier: "priority", ContextMode: "long"}
	records, ok, err := models.RecommendedCatalogRoleDefaults("codex", "review")
	if err != nil || !ok {
		t.Fatalf("catalog pick missing %v", err)
	}
	record := records["review"]
	want := agentmodelsettings.Assignment{Provider: "codex", Model: record.Model}
	for _, rec := range record.Recommendations {
		if rec.Role == "review" {
			want.Thinking = rec.Thinking
			if want.Thinking == "" {
				want.Thinking = record.DefaultThinking
			}
			if rec.Serving == "fast" || rec.Serving == "priority" {
				want.ServiceTier = "priority"
			}
		}
	}
	for _, tc := range []struct {
		name     string
		provider string
		explicit bool
		source   string
	}{
		{"catalog", "codex", false, "catalog"}, {"no-pick", "no-review-provider", false, "setting"}, {"explicit", "codex", true, "setting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := settings
			s.SystemAgents.Coder.Provider = tc.provider
			expected := want
			if tc.provider != "codex" {
				expected = s.SystemAgents.Coder
			}
			if tc.explicit {
				s.SystemAgents.Reviewer = agentmodelsettings.Assignment{Provider: "custom", Model: "explicit", Thinking: "high", ServiceTier: "priority"}
				expected = s.SystemAgents.Reviewer
			}
			for _, id := range []string{"system-reviewer", "system-reviewer-fixer"} {
				got, ok, err := configuredAgentSettingsForID(models, s, id)
				if err != nil || !ok || got != expected {
					t.Fatalf("%s got %+v %v %v want %+v", id, got, ok, err, expected)
				}
			}
			_, source, err := EffectiveReviewerAssignment(models, s)
			if err != nil || source != tc.source {
				t.Fatalf("source %q %v", source, err)
			}
		})
	}
	// Catalog failures cannot silently masquerade as a coder fallback.
	if _, _, err := EffectiveReviewerAssignment(nil, settings); err == nil {
		t.Fatal("missing catalog accepted")
	}
}
