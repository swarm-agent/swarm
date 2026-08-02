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
		Action:         ModelProfileSelection{Provider: "Codex", Model: "action", Thinking: "high", ServiceTier: "fast", ContextMode: "full"},
		Plan:           ModelProfileSelection{Provider: "OpenAI", Model: "plan", Thinking: "xhigh", ServiceTier: "priority", ContextMode: "compact"},
		UpdatedAt:      101,
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

func TestSwarmModeSettingsStoreRejectsFavoriteReferencesInsteadOfResolvingProfiles(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	if err := store.PutJSON(swarmModeSettingsKeyForAccount("account"), map[string]any{
		"account_scope_id":   "account",
		"action_favorite_id": "action",
		"plan_favorite_id":   "plan",
	}); err != nil {
		t.Fatalf("put favorite references: %v", err)
	}
	if _, found, err := NewSwarmModeSettingsStore(store).GetForAccount("account"); !found || !errors.Is(err, ErrSwarmModeActionRequired) {
		t.Fatalf("GetForAccount() found=%v error=%v, want true and %v", found, err, ErrSwarmModeActionRequired)
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
