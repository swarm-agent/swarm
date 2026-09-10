package testbenchcodex

// Purpose: Configure must persist all seven system/direct assignments plus the
// global default as Luna medium in an isolated candidate store. This temp-store
// test exercises real identity/catalog/settings authorities, not source strings.
import (
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	db "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

func TestConfigureAllCandidateRoles(t *testing.T) {
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids := identity.NewService(db.NewIdentityStore(store))
	settings := db.NewAgentModelSettingsStore(store)
	events, err := db.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	models := model.NewService(db.NewModelStore(store), events, model.NewCatalogService(db.NewModelCatalogStore(store)))
	if err := Configure(ids, settings, models); err != nil {
		t.Fatal(err)
	}
	state, err := ids.StateSummary()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := settings.GetForAccount(state.AccountScope.ID)
	if err != nil || !ok {
		t.Fatalf("settings: %v", err)
	}
	expected := db.AgentModelAssignment{Provider: "codex", Model: Model, Thinking: Thinking}
	for _, a := range []db.AgentModelAssignment{record.Swarm.Action, record.Swarm.Plan, record.SystemAgents.Compact, record.SystemAgents.Finder, record.SystemAgents.Coder, record.SystemAgents.Designer, record.SystemAgents.Router} {
		if a != expected {
			t.Fatalf("wrong assignment: %+v", a)
		}
	}
	pref, err := models.GetPreferenceForAccount(state.AccountScope.ID)
	if err != nil || pref.Provider != "codex" || pref.Model != Model || pref.Thinking != Thinking {
		t.Fatalf("default: %+v %v", pref, err)
	}
	if err := Configure(ids, settings, models); err != nil {
		t.Fatal("restart configuration:", err)
	}
	_, configured, err := db.NewAuthStore(store).GetCodexAuthRecordForAccount(state.AccountScope.ID)
	if err != nil || configured {
		t.Fatal("candidate must have no OAuth record")
	}
}
