package modelprofile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceValidatesSingleAndSplitShapes(t *testing.T) {
	svc, ctx := newTestService(t, "acct-one")
	selection := completeSelection("model-one")

	single, err := svc.Create(ctx, Input{Name: "Solo", ModelMode: "single", Single: &selection})
	if err != nil || single.Single == nil || single.Plan != nil || single.Auto != nil || !single.IsDefault {
		t.Fatalf("create single = %+v, err=%v", single, err)
	}
	plan, auto := completeSelection("plan-model"), completeSelection("auto-model")
	split, err := svc.Create(ctx, Input{Name: "Split", ModelMode: "split", Plan: &plan, Auto: &auto})
	if err != nil || split.Single != nil || split.Plan == nil || split.Auto == nil || split.IsDefault {
		t.Fatalf("create split = %+v, err=%v", split, err)
	}
	if _, err := svc.SetDefault(ctx, split.ProfileID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	state, err := svc.ListState(ctx)
	if err != nil || state.DefaultProfileID != split.ProfileID || len(state.Profiles) != 2 || state.Profiles[0].IsDefault == state.Profiles[1].IsDefault {
		t.Fatalf("list state after set default = %+v, err=%v", state, err)
	}

	invalid := []Input{
		{Name: "mixed-single", ModelMode: "single", Single: &selection, Plan: &plan},
		{Name: "mixed-split", ModelMode: "split", Single: &selection, Plan: &plan, Auto: &auto},
		{Name: "missing-auto", ModelMode: "split", Plan: &plan},
		{Name: "incomplete", ModelMode: "single", Single: &Selection{Provider: "codex", Model: "m"}},
	}
	for _, input := range invalid {
		if _, err := svc.Create(ctx, input); err == nil {
			t.Fatalf("Create(%q) succeeded, want validation error", input.Name)
		}
	}
}

func TestServiceNameUniquenessRenameIsolationAndBulkDelete(t *testing.T) {
	svc, accountOne := newTestService(t, "acct-one")
	accountTwo := principalContext("acct-two")
	selection := completeSelection("model-one")
	selection.ServiceTier = ""
	selection.ContextMode = ""

	first, err := svc.Create(accountOne, Input{Name: " Primary ", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := svc.Create(accountOne, Input{Name: "primary", ModelMode: "single", Single: &selection}); !errors.Is(err, pebblestore.ErrModelProfileNameConflict) {
		t.Fatalf("duplicate name err=%v", err)
	}
	other, err := svc.Create(accountTwo, Input{Name: "PRIMARY", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("same name in other account: %v", err)
	}

	first, err = svc.Update(accountOne, first.ProfileID, Input{Name: "Renamed", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := svc.Create(accountOne, Input{Name: "primary", ModelMode: "single", Single: &selection}); err != nil {
		t.Fatalf("old name index was not released: %v", err)
	}
	if _, err := svc.Get(accountOne, other.ProfileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account get err=%v", err)
	}

	result, err := svc.BulkDelete(accountOne, []string{first.ProfileID, "missing", first.ProfileID})
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != first.ProfileID || len(result.MissingIDs) != 1 || result.MissingIDs[0] != "missing" {
		t.Fatalf("bulk result = %+v", result)
	}
	replacement, err := svc.Create(accountOne, Input{Name: "renamed", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("deleted name index was not released: %v", err)
	}
	state, err := svc.ListState(accountOne)
	if err != nil || state.DefaultProfileID == "" {
		t.Fatalf("default after delete/create = %+v, err=%v", state, err)
	}
	if state.DefaultProfileID != replacement.ProfileID && len(state.Profiles) == 1 {
		t.Fatalf("only remaining profile is not default: %+v", state)
	}
}

func newTestService(t *testing.T, accountScopeID string) (*Service, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewModelProfileStore(store))
	return svc, principalContext(accountScopeID)
}

func principalContext(accountScopeID string) context.Context {
	return identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "user-one",
		AccountScopeID: accountScopeID,
	})
}

func TestServiceCreateFirstForAccountIsIdempotentAndAccountIsolated(t *testing.T) {
	svc, accountOne := newTestService(t, "acct-one")
	selection := completeSelection("recommended")
	first, created, err := svc.CreateFirstForAccount("acct-one", Input{Name: "Swarm recommended", ModelMode: "single", Single: &selection})
	if err != nil || !created || !first.IsDefault {
		t.Fatalf("create first = %+v created=%t err=%v", first, created, err)
	}
	second, created, err := svc.CreateFirstForAccount("acct-one", Input{Name: "Replacement", ModelMode: "single", Single: &selection})
	if err != nil || created || second.ProfileID != "" {
		t.Fatalf("retry create first = %+v created=%t err=%v", second, created, err)
	}
	state, err := svc.ListState(accountOne)
	if err != nil || len(state.Profiles) != 1 || state.DefaultProfileID != first.ProfileID || state.Profiles[0].Name != "Swarm recommended" {
		t.Fatalf("account one state = %+v err=%v", state, err)
	}
	other, created, err := svc.CreateFirstForAccount("acct-two", Input{Name: "Other default", ModelMode: "single", Single: &selection})
	if err != nil || !created || !other.IsDefault || other.ProfileID == first.ProfileID {
		t.Fatalf("account two create first = %+v created=%t err=%v", other, created, err)
	}
	otherState, err := svc.ListState(principalContext("acct-two"))
	if err != nil || len(otherState.Profiles) != 1 || otherState.DefaultProfileID != other.ProfileID {
		t.Fatalf("account two state = %+v err=%v", otherState, err)
	}
}

func TestServiceBackfillsAndPromotesDeterministicDefault(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "legacy-default.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	profileStore := pebblestore.NewModelProfileStore(store)
	selection := completeSelection("legacy")
	for _, record := range []pebblestore.ModelProfileRecord{
		{ProfileID: "mp_b", AccountScopeID: "acct", Name: "Beta", ModelMode: "single", Single: &selection, CreatedAt: 1, UpdatedAt: 1},
		{ProfileID: "mp_a", AccountScopeID: "acct", Name: "Alpha", ModelMode: "single", Single: &selection, CreatedAt: 1, UpdatedAt: 1},
	} {
		payload, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := store.PutBytes(pebblestore.KeyModelProfileForAccount("acct", record.ProfileID), payload); err != nil {
			t.Fatal(err)
		}
		if err := store.PutBytes(pebblestore.KeyModelProfileNameForAccount("acct", pebblestore.NormalizeModelProfileName(record.Name)), []byte(record.ProfileID)); err != nil {
			t.Fatal(err)
		}
	}
	state, err := profileStore.ListStateForAccount("acct", 500)
	if err != nil || state.DefaultProfileID != "mp_a" {
		t.Fatalf("backfill state=%+v err=%v", state, err)
	}
	if _, err := profileStore.SetDefaultForAccount("acct", "mp_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := profileStore.DeleteForAccount("acct", "mp_b"); err != nil {
		t.Fatal(err)
	}
	state, err = profileStore.ListStateForAccount("acct", 500)
	if err != nil || state.DefaultProfileID != "mp_a" || len(state.Profiles) != 1 || !state.Profiles[0].IsDefault {
		t.Fatalf("promoted state=%+v err=%v", state, err)
	}
}

func completeSelection(model string) Selection {
	return Selection{
		Provider:    "codex",
		Model:       model,
		Thinking:    "high",
		ServiceTier: "priority",
		ContextMode: "full",
	}
}
