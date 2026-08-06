package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func openModelProfileTestStore(t *testing.T) *ModelProfileStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "model-profiles.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return NewModelProfileStore(store)
}

func modelProfileTestRecord(accountScopeID, profileID, name string) ModelProfileRecord {
	return ModelProfileRecord{
		ProfileID:      profileID,
		AccountScopeID: accountScopeID,
		Name:           name,
		Provider:       "codex",
		Model:          "gpt-test",
		Thinking:       "high",
		ServiceTier:    "fast",
		ContextMode:    "full",
		CreatedAt:      100,
		UpdatedAt:      200,
	}
}

func putModelProfileForTest(t *testing.T, profiles *ModelProfileStore, record ModelProfileRecord) ModelProfileRecord {
	t.Helper()
	stored, err := profiles.PutForAccount(record)
	if err != nil {
		t.Fatalf("put profile %q: %v", record.ProfileID, err)
	}
	return stored
}

func TestModelProfileStoreFlatRoundTrip(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	want := modelProfileTestRecord("account-a", "favorite-a", "Primary")

	created := putModelProfileForTest(t, profiles, want)
	if !created.IsDefault {
		t.Fatal("first profile IsDefault = false, want true")
	}
	got, ok, err := profiles.GetForAccount(want.AccountScopeID, want.ProfileID)
	if err != nil || !ok {
		t.Fatalf("get profile: ok=%t err=%v", ok, err)
	}
	want.IsDefault = true
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	raw, ok, err := profiles.store.GetBytes(KeyModelProfileForAccount(want.AccountScopeID, want.ProfileID))
	if err != nil || !ok {
		t.Fatalf("get raw profile: ok=%t err=%v", ok, err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode raw profile: %v", err)
	}
	for _, key := range []string{"model_mode", "single", "plan", "auto", "is_default"} {
		if _, exists := payload[key]; exists {
			t.Errorf("persisted profile contains legacy or derived field %q: %s", key, raw)
		}
	}
	for _, key := range []string{"profile_id", "account_scope_id", "name", "provider", "model", "thinking", "service_tier", "context_mode", "created_at", "updated_at", "sort_order"} {
		if _, exists := payload[key]; !exists {
			t.Errorf("persisted flat profile is missing field %q: %s", key, raw)
		}
	}
}

func TestModelProfileStoreAccountIsolation(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "shared-id", "Account A"))
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-b", "shared-id", "Account B"))

	accountA, ok, err := profiles.GetForAccount("account-a", "shared-id")
	if err != nil || !ok {
		t.Fatalf("get account A: ok=%t err=%v", ok, err)
	}
	accountB, ok, err := profiles.GetForAccount("account-b", "shared-id")
	if err != nil || !ok {
		t.Fatalf("get account B: ok=%t err=%v", ok, err)
	}
	if accountA.Name != "Account A" || accountB.Name != "Account B" {
		t.Fatalf("cross-account records = %q/%q", accountA.Name, accountB.Name)
	}
	if _, ok, err := profiles.GetForAccount("account-c", "shared-id"); err != nil || ok {
		t.Fatalf("missing account read: ok=%t err=%v", ok, err)
	}

	listedA, err := profiles.ListForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list account A: %v", err)
	}
	if len(listedA) != 1 || listedA[0].AccountScopeID != "account-a" {
		t.Fatalf("account A list = %+v", listedA)
	}
}

func TestModelProfileStoreFailsClosedOnMissingOrDanglingDefault(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "one", "One"))
	if err := profiles.store.Delete(KeyModelProfileDefaultForAccount("account-a")); err != nil {
		t.Fatalf("delete default: %v", err)
	}
	if _, err := profiles.ListStateForAccount("account-a", 10); err == nil || err.Error() != "model profile default is required" {
		t.Fatalf("missing default error = %v", err)
	}
	if err := profiles.store.PutBytes(KeyModelProfileDefaultForAccount("account-a"), []byte("missing")); err != nil {
		t.Fatalf("seed dangling default: %v", err)
	}
	if _, err := profiles.ListStateForAccount("account-a", 10); err == nil || err.Error() != "model profile default is dangling" {
		t.Fatalf("dangling default error = %v", err)
	}
}

func TestModelProfileStoreNameConflictIsAccountScoped(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "one", " Favorite "))

	_, err := profiles.PutForAccount(modelProfileTestRecord("account-a", "two", "favorite"))
	if !errors.Is(err, ErrModelProfileNameConflict) {
		t.Fatalf("same-account normalized name error = %v, want %v", err, ErrModelProfileNameConflict)
	}
	if _, err := profiles.PutForAccount(modelProfileTestRecord("account-b", "two", "FAVORITE")); err != nil {
		t.Fatalf("same name in another account: %v", err)
	}
}

func TestModelProfileStoreReorderAndDefaultMetadata(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "one", "One"))
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "two", "Two"))
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "three", "Three"))

	selected, err := profiles.SetDefaultForAccount("account-a", "two")
	if err != nil {
		t.Fatalf("set default: %v", err)
	}
	if !selected.IsDefault || selected.ProfileID != "two" {
		t.Fatalf("selected default = %+v", selected)
	}

	ordered, err := profiles.ReorderForAccount("account-a", []string{"three", "two", "one"})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(ordered) != 3 {
		t.Fatalf("reordered length = %d, want 3", len(ordered))
	}
	for i, wantID := range []string{"three", "two", "one"} {
		if ordered[i].ProfileID != wantID || ordered[i].SortOrder != i {
			t.Errorf("ordered[%d] = %+v, want id=%q order=%d", i, ordered[i], wantID, i)
		}
		if ordered[i].IsDefault != (wantID == "two") {
			t.Errorf("ordered[%d].IsDefault = %t for %q", i, ordered[i].IsDefault, wantID)
		}
	}

	state, err := profiles.ListStateForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if state.DefaultProfileID != "two" {
		t.Fatalf("default id = %q, want two", state.DefaultProfileID)
	}
	for i, wantID := range []string{"three", "two", "one"} {
		if state.Profiles[i].ProfileID != wantID || state.Profiles[i].IsDefault != (wantID == "two") {
			t.Errorf("state profile[%d] = %+v", i, state.Profiles[i])
		}
	}
}

func TestModelProfileStoreDeletePromotesFirstDeterministically(t *testing.T) {
	profiles := openModelProfileTestStore(t)
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "one", "One"))
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "two", "Two"))
	putModelProfileForTest(t, profiles, modelProfileTestRecord("account-a", "three", "Three"))
	if _, err := profiles.ReorderForAccount("account-a", []string{"three", "one", "two"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if _, err := profiles.SetDefaultForAccount("account-a", "one"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	deleted, err := profiles.DeleteForAccount("account-a", "one")
	if err != nil || !deleted {
		t.Fatalf("delete default: deleted=%t err=%v", deleted, err)
	}
	state, err := profiles.ListStateForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if state.DefaultProfileID != "three" || len(state.Profiles) != 2 {
		t.Fatalf("state after promotion = %+v", state)
	}
	if !state.Profiles[0].IsDefault || state.Profiles[0].ProfileID != "three" || state.Profiles[1].IsDefault {
		t.Fatalf("default metadata after promotion = %+v", state.Profiles)
	}

	deleted, err = profiles.DeleteForAccount("account-a", "three")
	if err != nil || !deleted {
		t.Fatalf("delete promoted default: deleted=%t err=%v", deleted, err)
	}
	state, err = profiles.ListStateForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list after second delete: %v", err)
	}
	if state.DefaultProfileID != "two" || len(state.Profiles) != 1 || !state.Profiles[0].IsDefault {
		t.Fatalf("second promotion state = %+v", state)
	}
}
