package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openModelProfileFlatMigrationTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "flat-migration.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func seedLegacyModelProfile(t *testing.T, store *Store, record legacyModelProfileRecord) []byte {
	t.Helper()
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal legacy profile: %v", err)
	}
	if err := store.PutBytes(KeyModelProfileForAccount(record.AccountScopeID, record.ProfileID), payload); err != nil {
		t.Fatalf("seed legacy profile: %v", err)
	}
	if err := store.PutBytes(KeyModelProfileNameForAccount(record.AccountScopeID, NormalizeModelProfileName(record.Name)), []byte(record.ProfileID)); err != nil {
		t.Fatalf("seed legacy name index: %v", err)
	}
	return payload
}

func seedLegacyModelProfileDefault(t *testing.T, store *Store, accountScopeID, profileID string) {
	t.Helper()
	if err := store.PutBytes(KeyModelProfileDefaultForAccount(accountScopeID), []byte(profileID)); err != nil {
		t.Fatalf("seed legacy default: %v", err)
	}
}

func migrationSelection(provider, model, thinking, tier, contextMode string) *legacyModelProfileSelection {
	return &legacyModelProfileSelection{Provider: provider, Model: model, Thinking: thinking, ServiceTier: tier, ContextMode: contextMode}
}

func TestRunModelProfileFlatMigrationRewritesSingleAndSplitAccounts(t *testing.T) {
	store := openModelProfileFlatMigrationTestStore(t)
	single := legacyModelProfileRecord{
		ProfileID: "solo", AccountScopeID: "account-single", Name: "Solo", ModelMode: "single",
		Single: migrationSelection("codex", "single-model", "high", "fast", "full"),
		CreatedAt: 10, UpdatedAt: 20, SortOrder: 7,
	}
	split := legacyModelProfileRecord{
		ProfileID: "standard", AccountScopeID: "account-split", Name: "Standard", ModelMode: "split",
		Plan: migrationSelection("codex", "plan-model", "xhigh", "fast", "full"),
		Auto: migrationSelection("openai", "action-model", "medium", "flex", "compact"),
		CreatedAt: 30, UpdatedAt: 40, SortOrder: 11,
	}
	seedLegacyModelProfile(t, store, single)
	seedLegacyModelProfile(t, store, split)
	seedLegacyModelProfileDefault(t, store, single.AccountScopeID, single.ProfileID)
	seedLegacyModelProfileDefault(t, store, split.AccountScopeID, split.ProfileID)

	result, err := RunModelProfileFlatMigration(store)
	if err != nil {
		t.Fatalf("run migration: %v", err)
	}
	wantResult := ModelProfileFlatMigrationResult{Version: 1, Applied: true, AccountsMigrated: 2, ProfilesMigrated: 2, FavoritesCreated: 3}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("migration result = %+v, want %+v", result, wantResult)
	}

	profiles := NewModelProfileStore(store)
	singleState, err := profiles.ListStateForAccount(single.AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list migrated single account: %v", err)
	}
	wantSingle := ModelProfileRecord{
		ProfileID: "solo", AccountScopeID: "account-single", Name: "Solo",
		Provider: "codex", Model: "single-model", Thinking: "high", ServiceTier: "fast", ContextMode: "full",
		CreatedAt: 10, UpdatedAt: 20, SortOrder: 7, IsDefault: true,
	}
	if singleState.DefaultProfileID != "solo" || !reflect.DeepEqual(singleState.Profiles, []ModelProfileRecord{wantSingle}) {
		t.Fatalf("single state = %+v, want default solo and %+v", singleState, wantSingle)
	}

	splitState, err := profiles.ListStateForAccount(split.AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list migrated split account: %v", err)
	}
	wantSplit := []ModelProfileRecord{
		{ProfileID: "standard_action", AccountScopeID: "account-split", Name: "Standard Action", Provider: "openai", Model: "action-model", Thinking: "medium", ServiceTier: "flex", ContextMode: "compact", CreatedAt: 30, UpdatedAt: 40, SortOrder: 11, IsDefault: true},
		{ProfileID: "standard_plan", AccountScopeID: "account-split", Name: "Standard Plan", Provider: "codex", Model: "plan-model", Thinking: "xhigh", ServiceTier: "fast", ContextMode: "full", CreatedAt: 30, UpdatedAt: 40, SortOrder: 12},
	}
	if splitState.DefaultProfileID != "standard_action" || !reflect.DeepEqual(splitState.Profiles, wantSplit) {
		t.Fatalf("split state = %+v, want default standard_action and %+v", splitState, wantSplit)
	}

	modeStore := NewSwarmModeSettingsStore(store)
	singleMode, found, err := modeStore.GetForAccount(single.AccountScopeID)
	if err != nil || !found {
		t.Fatalf("get single mode settings: found=%v err=%v", found, err)
	}
	if want := (SwarmModeSettingsRecord{AccountScopeID: single.AccountScopeID, ActionFavoriteID: "solo", UpdatedAt: 20}); singleMode != want {
		t.Fatalf("single mode settings = %+v, want %+v", singleMode, want)
	}
	splitMode, found, err := modeStore.GetForAccount(split.AccountScopeID)
	if err != nil || !found {
		t.Fatalf("get split mode settings: found=%v err=%v", found, err)
	}
	if want := (SwarmModeSettingsRecord{AccountScopeID: split.AccountScopeID, ActionFavoriteID: "standard_action", PlanEnabled: true, PlanFavoriteID: "standard_plan", UpdatedAt: 40}); splitMode != want {
		t.Fatalf("split mode settings = %+v, want %+v", splitMode, want)
	}

	if _, found, err := store.GetBytes(modelProfileFlatMigrationKey); err != nil || !found {
		t.Fatalf("migration marker: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetBytes(KeyModelProfileForAccount(split.AccountScopeID, split.ProfileID)); err != nil || found {
		t.Fatalf("obsolete split profile row: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetBytes(KeyModelProfileNameForAccount(split.AccountScopeID, NormalizeModelProfileName(split.Name))); err != nil || found {
		t.Fatalf("obsolete split name index: found=%v err=%v", found, err)
	}
	for _, id := range []string{"solo", "standard_action", "standard_plan"} {
		account := split.AccountScopeID
		if id == "solo" {
			account = single.AccountScopeID
		}
		raw, found, err := store.GetBytes(KeyModelProfileForAccount(account, id))
		if err != nil || !found {
			t.Fatalf("read canonical profile %q: found=%v err=%v", id, found, err)
		}
		if strings.Contains(string(raw), "model_mode") || strings.Contains(string(raw), "\"single\"") || strings.Contains(string(raw), "\"plan\"") || strings.Contains(string(raw), "\"auto\"") {
			t.Errorf("canonical profile %q retained legacy shape: %s", id, raw)
		}
	}
}

func TestRunModelProfileFlatMigrationFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, store *Store) (key string, original []byte)
	}{
		{
			name: "malformed legacy row",
			seed: func(t *testing.T, store *Store) (string, []byte) {
				key := KeyModelProfileForAccount("account-a", "broken")
				payload := []byte(`{"profile_id":"broken","account_scope_id":"account-a","name":"Broken","model_mode":"split","plan":{"provider":"codex","model":"plan","thinking":"high"}}`)
				if err := store.PutBytes(key, payload); err != nil {
					t.Fatal(err)
				}
				seedLegacyModelProfileDefault(t, store, "account-a", "broken")
				return key, payload
			},
		},
		{
			name: "generated id collision",
			seed: func(t *testing.T, store *Store) (string, []byte) {
				split := legacyModelProfileRecord{ProfileID: "base", AccountScopeID: "account-a", Name: "Base", ModelMode: "split", Plan: migrationSelection("codex", "plan", "high", "", ""), Auto: migrationSelection("codex", "action", "high", "", ""), SortOrder: 1}
				single := legacyModelProfileRecord{ProfileID: "base_action", AccountScopeID: "account-a", Name: "Other", ModelMode: "single", Single: migrationSelection("codex", "other", "high", "", ""), SortOrder: 2}
				payload := seedLegacyModelProfile(t, store, split)
				seedLegacyModelProfile(t, store, single)
				seedLegacyModelProfileDefault(t, store, "account-a", "base")
				return KeyModelProfileForAccount("account-a", "base"), payload
			},
		},
		{
			name: "generated name collision",
			seed: func(t *testing.T, store *Store) (string, []byte) {
				split := legacyModelProfileRecord{ProfileID: "base", AccountScopeID: "account-a", Name: "Base", ModelMode: "split", Plan: migrationSelection("codex", "plan", "high", "", ""), Auto: migrationSelection("codex", "action", "high", "", ""), SortOrder: 1}
				single := legacyModelProfileRecord{ProfileID: "other", AccountScopeID: "account-a", Name: "base action", ModelMode: "single", Single: migrationSelection("codex", "other", "high", "", ""), SortOrder: 2}
				payload := seedLegacyModelProfile(t, store, split)
				seedLegacyModelProfile(t, store, single)
				seedLegacyModelProfileDefault(t, store, "account-a", "base")
				return KeyModelProfileForAccount("account-a", "base"), payload
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openModelProfileFlatMigrationTestStore(t)
			key, original := test.seed(t, store)
			if _, err := RunModelProfileFlatMigration(store); err == nil {
				t.Fatal("migration succeeded, want failure")
			}
			if _, found, err := store.GetBytes(modelProfileFlatMigrationKey); err != nil || found {
				t.Fatalf("marker after failed migration: found=%v err=%v", found, err)
			}
			got, found, err := store.GetBytes(key)
			if err != nil || !found || !reflect.DeepEqual(got, original) {
				t.Fatalf("legacy row changed after failure: found=%v err=%v got=%s want=%s", found, err, got, original)
			}
			if _, found, err := store.GetBytes(swarmModeSettingsKeyForAccount("account-a")); err != nil || found {
				t.Fatalf("mode settings after failed migration: found=%v err=%v", found, err)
			}
		})
	}
}

func TestRunModelProfileFlatMigrationMarkerPreventsRescan(t *testing.T) {
	store := openModelProfileFlatMigrationTestStore(t)
	legacy := legacyModelProfileRecord{
		ProfileID: "solo", AccountScopeID: "account-a", Name: "Solo", ModelMode: "single",
		Single: migrationSelection("codex", "model", "high", "fast", "full"), UpdatedAt: 25,
	}
	seedLegacyModelProfile(t, store, legacy)
	seedLegacyModelProfileDefault(t, store, legacy.AccountScopeID, legacy.ProfileID)
	first, err := RunModelProfileFlatMigration(store)
	if err != nil || !first.Applied {
		t.Fatalf("first migration = %+v, err=%v", first, err)
	}

	corrupt := []byte(`{"profile_id":"solo","account_scope_id":"account-a","name":"Corrupt","model_mode":"unknown"}`)
	key := KeyModelProfileForAccount(legacy.AccountScopeID, legacy.ProfileID)
	if err := store.PutBytes(key, corrupt); err != nil {
		t.Fatalf("write old-looking data after marker: %v", err)
	}
	second, err := RunModelProfileFlatMigration(store)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	want := ModelProfileFlatMigrationResult{Version: 1, AlreadyApplied: true}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second result = %+v, want %+v", second, want)
	}
	got, found, err := store.GetBytes(key)
	if err != nil || !found || !reflect.DeepEqual(got, corrupt) {
		t.Fatalf("marked migration rescanned data: found=%v err=%v got=%s", found, err, got)
	}
}
