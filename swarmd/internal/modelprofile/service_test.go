package modelprofile

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestValidateInputNormalizesFlatFavorite(t *testing.T) {
	got, err := ValidateInput(Input{
		Name: "  Daily driver  ", Provider: " codex ", Model: " gpt-5 ",
		Thinking: " high ", ServiceTier: " priority ", ContextMode: " full ",
	})
	if err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	want := Input{Name: "Daily driver", Provider: "codex", Model: "gpt-5", Thinking: "high", ServiceTier: "priority", ContextMode: "full"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidateInput = %#v, want %#v", got, want)
	}

	for name, input := range map[string]Input{
		"name":     {Provider: "codex", Model: "gpt-5", Thinking: "high"},
		"provider": {Name: "favorite", Model: "gpt-5", Thinking: "high"},
		"model":    {Name: "favorite", Provider: "codex", Thinking: "high"},
		"thinking": {Name: "favorite", Provider: "codex", Model: "gpt-5"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateInput(input); err == nil {
				t.Fatal("ValidateInput succeeded, want validation error")
			}
		})
	}
}

func TestServiceCRUDAccountIsolationAndNameUniqueness(t *testing.T) {
	svc, accountOne := newFavoriteTestService(t, "acct-one")
	accountTwo := favoritePrincipalContext("acct-two")

	created, err := svc.Create(accountOne, favoriteInput(" Primary ", " model-one "))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "Primary" || created.Provider != "codex" || created.Model != "model-one" || !created.IsDefault {
		t.Fatalf("created = %#v", created)
	}
	if _, err := svc.Create(accountOne, favoriteInput("primary", "other")); !errors.Is(err, pebblestore.ErrModelProfileNameConflict) {
		t.Fatalf("duplicate name error = %v", err)
	}
	other, err := svc.Create(accountTwo, favoriteInput("PRIMARY", "account-two-model"))
	if err != nil {
		t.Fatalf("same name in other account: %v", err)
	}
	if _, err := svc.Get(accountOne, other.ProfileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account Get error = %v", err)
	}

	updatedInput := favoriteInput(" Renamed ", " model-two ")
	updatedInput.ServiceTier = " flex "
	updatedInput.ContextMode = " compact "
	updated, err := svc.Update(accountOne, created.ProfileID, updatedInput)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" || updated.Model != "model-two" || updated.ServiceTier != "flex" || updated.ContextMode != "compact" || updated.CreatedAt != created.CreatedAt || updated.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("updated = %#v, created = %#v", updated, created)
	}
	if _, err := svc.Create(accountOne, favoriteInput("primary", "replacement")); err != nil {
		t.Fatalf("old normalized name was not released: %v", err)
	}
	deleted, err := svc.Delete(accountOne, updated.ProfileID)
	if err != nil || !deleted {
		t.Fatalf("Delete = %t, %v", deleted, err)
	}
	if _, err := svc.Get(accountOne, updated.ProfileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete error = %v", err)
	}
}

func TestServiceReorderAndDefault(t *testing.T) {
	svc, ctx := newFavoriteTestService(t, "acct")
	first, err := svc.Create(ctx, favoriteInput("First", "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, favoriteInput("Second", "two"))
	if err != nil {
		t.Fatal(err)
	}
	third, err := svc.Create(ctx, favoriteInput("Third", "three"))
	if err != nil {
		t.Fatal(err)
	}

	ordered, err := svc.Reorder(ctx, []string{third.ProfileID, first.ProfileID, second.ProfileID})
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if got := []string{ordered[0].ProfileID, ordered[1].ProfileID, ordered[2].ProfileID}; !reflect.DeepEqual(got, []string{third.ProfileID, first.ProfileID, second.ProfileID}) {
		t.Fatalf("reordered IDs = %v", got)
	}
	set, err := svc.SetDefault(ctx, second.ProfileID)
	if err != nil || set.ProfileID != second.ProfileID || !set.IsDefault {
		t.Fatalf("SetDefault = %#v, %v", set, err)
	}
	gotDefault, ok, err := svc.GetDefault(ctx)
	if err != nil || !ok || gotDefault.ProfileID != second.ProfileID {
		t.Fatalf("GetDefault = %#v, %t, %v", gotDefault, ok, err)
	}
	state, err := svc.ListState(ctx)
	if err != nil || state.DefaultProfileID != second.ProfileID || len(state.Profiles) != 3 || !state.Profiles[2].IsDefault {
		t.Fatalf("ListState = %#v, %v", state, err)
	}
	if _, err := svc.SetDefault(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDefault missing error = %v", err)
	}
	if _, err := svc.Reorder(ctx, []string{first.ProfileID, second.ProfileID, "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reorder missing error = %v", err)
	}
}

func TestServiceBulkDeletePromotesDefault(t *testing.T) {
	svc, ctx := newFavoriteTestService(t, "acct")
	first, _ := svc.Create(ctx, favoriteInput("First", "one"))
	second, _ := svc.Create(ctx, favoriteInput("Second", "two"))
	third, _ := svc.Create(ctx, favoriteInput("Third", "three"))
	if _, err := svc.SetDefault(ctx, second.ProfileID); err != nil {
		t.Fatal(err)
	}

	result, err := svc.BulkDelete(ctx, []string{second.ProfileID, "missing", second.ProfileID})
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if !reflect.DeepEqual(result.DeletedIDs, []string{second.ProfileID}) || !reflect.DeepEqual(result.MissingIDs, []string{"missing"}) {
		t.Fatalf("BulkDelete result = %#v", result)
	}
	state, err := svc.ListState(ctx)
	if err != nil || state.DefaultProfileID != first.ProfileID || len(state.Profiles) != 2 || !state.Profiles[0].IsDefault || state.Profiles[1].ProfileID != third.ProfileID {
		t.Fatalf("state after default deletion = %#v, %v", state, err)
	}
}

func TestServiceCreateFirstForAccountIsAtomicAndIsolated(t *testing.T) {
	svc, accountOne := newFavoriteTestService(t, "acct-one")
	first, created, err := svc.CreateFirstForAccount(" acct-one ", favoriteInput("Recommended", "one"))
	if err != nil || !created || !first.IsDefault {
		t.Fatalf("first CreateFirstForAccount = %#v, %t, %v", first, created, err)
	}
	second, created, err := svc.CreateFirstForAccount("acct-one", favoriteInput("Replacement", "two"))
	if err != nil || created || second.ProfileID != "" {
		t.Fatalf("second CreateFirstForAccount = %#v, %t, %v", second, created, err)
	}
	state, err := svc.ListState(accountOne)
	if err != nil || len(state.Profiles) != 1 || state.Profiles[0].ProfileID != first.ProfileID {
		t.Fatalf("account one state = %#v, %v", state, err)
	}
	other, created, err := svc.CreateFirstForAccount("acct-two", favoriteInput("Other", "three"))
	if err != nil || !created || other.ProfileID == first.ProfileID {
		t.Fatalf("other account CreateFirstForAccount = %#v, %t, %v", other, created, err)
	}
}

func TestServiceRequiresPrincipalAndConfiguration(t *testing.T) {
	if _, err := NewService(nil).Create(favoritePrincipalContext("acct"), favoriteInput("Favorite", "one")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured Create error = %v", err)
	}
	svc, _ := newFavoriteTestService(t, "acct")
	if _, err := svc.Create(context.Background(), favoriteInput("Favorite", "one")); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("principal error = %v", err)
	}
}

func newFavoriteTestService(t *testing.T, accountScopeID string) (*Service, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "model-favorites.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewModelProfileStore(store))
	ids := []string{"mp_one", "mp_two", "mp_three", "mp_four", "mp_five", "mp_six"}
	svc.newID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}
	now := time.UnixMilli(1000)
	svc.now = func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
	return svc, favoritePrincipalContext(accountScopeID)
}

func favoritePrincipalContext(accountScopeID string) context.Context {
	return identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: accountScopeID,
	})
}

func favoriteInput(name, model string) Input {
	return Input{
		Name: name, Provider: " codex ", Model: model, Thinking: " high ",
		ServiceTier: " priority ", ContextMode: " full ",
	}
}
