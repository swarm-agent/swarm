package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSwarmModeSettingsStoreRoundTripsDirectSelections(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)
	want := SwarmModeSettingsRecord{
		AccountScopeID: "account-a",
		Action: ModelProfileSelection{Provider: "Codex", Model: "action", Thinking: "high", ServiceTier: "fast", ContextMode: "full"},
		Plan: ModelProfileSelection{Provider: "OpenAI", Model: "plan", Thinking: "xhigh", ServiceTier: "priority", ContextMode: "compact"},
		UpdatedAt: 101,
	}
	stored, err := settings.PutForAccount(want)
	if err != nil {
		t.Fatalf("PutForAccount(): %v", err)
	}
	want.Action.Provider, want.Plan.Provider = "codex", "openai"
	if stored != want {
		t.Fatalf("PutForAccount() = %+v, want %+v", stored, want)
	}
	got, found, err := settings.GetForAccount("account-a")
	if err != nil || !found || got != want {
		t.Fatalf("GetForAccount() = (%+v, %v, %v), want %+v, true, nil", got, found, err, want)
	}
	if _, found, err := settings.GetForAccount("account-b"); err != nil || found {
		t.Fatalf("isolated GetForAccount() found=%v err=%v", found, err)
	}
}

func TestSwarmModeSettingsStoreMigratesLegacyFavoriteReferences(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	for _, record := range []ModelProfileRecord{
		{ProfileID: "action", AccountScopeID: "account", Name: "Action", Provider: "codex", Model: "action-model", Thinking: "high"},
		{ProfileID: "plan", AccountScopeID: "account", Name: "Plan", Provider: "openai", Model: "plan-model", Thinking: "xhigh"},
	} {
		if _, err := NewModelProfileStore(store).PutForAccount(record); err != nil {
			t.Fatalf("put favorite: %v", err)
		}
	}
	if err := store.PutJSON(swarmModeSettingsKeyForAccount("account"), legacySwarmModeSettingsRecord{AccountScopeID: "account", ActionFavoriteID: "action", PlanEnabled: true, PlanFavoriteID: "plan", UpdatedAt: 7}); err != nil {
		t.Fatalf("put legacy settings: %v", err)
	}
	got, found, err := NewSwarmModeSettingsStore(store).GetForAccount("account")
	if err != nil || !found || got.Action.Model != "action-model" || got.Plan.Model != "plan-model" || got.UpdatedAt != 7 {
		t.Fatalf("migrated settings = (%+v, %v, %v)", got, found, err)
	}
}

func TestSwarmModeSettingsStoreRejectsMissingSelections(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)
	selection := ModelProfileSelection{Provider: "codex", Model: "gpt", Thinking: "high"}
	tests := []struct {
		record SwarmModeSettingsRecord
		want   error
	}{
		{record: SwarmModeSettingsRecord{Action: selection, Plan: selection}, want: ErrSwarmModeAccountScopeIDRequired},
		{record: SwarmModeSettingsRecord{AccountScopeID: "account", Plan: selection}, want: ErrSwarmModeActionRequired},
		{record: SwarmModeSettingsRecord{AccountScopeID: "account", Action: selection}, want: ErrSwarmModePlanRequired},
	}
	for _, test := range tests {
		if _, err := settings.PutForAccount(test.record); !errors.Is(err, test.want) {
			t.Fatalf("PutForAccount(%+v) error = %v, want %v", test.record, err, test.want)
		}
	}
}

func openSwarmModeSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "swarm-mode-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
