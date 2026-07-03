package model

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newTestModelService(t *testing.T) (*Service, *pebblestore.Store) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "model.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("open event log: %v", err)
	}
	catalogStore := pebblestore.NewModelCatalogStore(store)
	catalog := NewCatalogService(catalogStore)
	service := NewService(pebblestore.NewModelStore(store), events, catalog)
	return service, store
}

func TestSetPreferenceForAccountPreservesCatalogBackedCodexServiceTier(t *testing.T) {
	service, store := newTestModelService(t)
	defer store.Close()

	if err := service.catalog.store.SetRecord(pebblestore.ModelCatalogRecord{
		Provider:     "codex",
		Model:        "gpt-5.4-mini",
		ServiceTiers: []string{"flex"},
	}); err != nil {
		t.Fatalf("set catalog record: %v", err)
	}

	resolved, _, err := service.SetPreferenceForAccount("account-a", "user-a", "codex", "gpt-5.4-mini", "medium", "flex")
	if err != nil {
		t.Fatalf("SetPreferenceForAccount returned error: %v", err)
	}
	if resolved.Preference.ServiceTier != "flex" {
		t.Fatalf("resolved service tier = %q, want flex", resolved.Preference.ServiceTier)
	}

	stored, err := service.GetPreferenceForAccount("account-a")
	if err != nil {
		t.Fatalf("read stored preference: %v", err)
	}
	if stored.ServiceTier != "flex" {
		t.Fatalf("stored service tier = %q, want flex", stored.ServiceTier)
	}
}

func TestResolvePreferenceClearsUnsupportedCatalogServiceTier(t *testing.T) {
	service, store := newTestModelService(t)
	defer store.Close()

	if err := service.catalog.store.SetRecord(pebblestore.ModelCatalogRecord{
		Provider:     "codex",
		Model:        "gpt-5.4-mini",
		ServiceTiers: []string{"flex"},
	}); err != nil {
		t.Fatalf("set catalog record: %v", err)
	}

	resolved, err := service.ResolvePreference(pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4-mini",
		Thinking:    "medium",
		ServiceTier: "fast",
	})
	if err != nil {
		t.Fatalf("ResolvePreference returned error: %v", err)
	}
	if resolved.Preference.ServiceTier != "" {
		t.Fatalf("resolved unsupported service tier = %q, want empty", resolved.Preference.ServiceTier)
	}
}
