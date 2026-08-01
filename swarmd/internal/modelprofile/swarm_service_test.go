package modelprofile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSwarmServiceMissingAndPrincipalRequirement(t *testing.T) {
	_, svc, _ := openSwarmServiceTest(t)
	if _, err := svc.Get(t.Context()); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("Get() error = %v, want %v", err, identity.ErrPrincipalRequired)
	}
	if _, err := svc.Put(t.Context(), SwarmSettingsInput{ActionFavoriteID: "action"}); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("Put() error = %v, want %v", err, identity.ErrPrincipalRequired)
	}
	if _, err := svc.Get(swarmTestPrincipal("account-a")); !errors.Is(err, ErrSwarmModeSettingsNotFound) {
		t.Fatalf("Get() error = %v, want %v", err, ErrSwarmModeSettingsNotFound)
	}
}

func TestSwarmServiceStoresActionOnlyAndActionWithPlan(t *testing.T) {
	_, svc, favorites := openSwarmServiceTest(t)
	ctx := swarmTestPrincipal("account-a")
	putSwarmTestFavorite(t, favorites, "account-a", "action")
	putSwarmTestFavorite(t, favorites, "account-a", "plan")
	svc.now = func() time.Time { return time.UnixMilli(1234) }

	actionOnly, err := svc.Put(ctx, SwarmSettingsInput{ActionFavoriteID: " action "})
	if err != nil {
		t.Fatalf("Put(action only): %v", err)
	}
	if actionOnly.ActionFavoriteID != "action" || actionOnly.PlanEnabled || actionOnly.PlanFavoriteID != "" || actionOnly.UpdatedAt != 1234 {
		t.Fatalf("Put(action only) = %+v", actionOnly)
	}
	got, err := svc.Get(ctx)
	if err != nil || got != actionOnly {
		t.Fatalf("Get(action only) = (%+v, %v), want %+v", got, err, actionOnly)
	}

	withPlan, err := svc.Update(ctx, SwarmSettingsInput{ActionFavoriteID: "action", PlanEnabled: true, PlanFavoriteID: "plan"})
	if err != nil {
		t.Fatalf("Update(with plan): %v", err)
	}
	if !withPlan.PlanEnabled || withPlan.PlanFavoriteID != "plan" {
		t.Fatalf("Update(with plan) = %+v", withPlan)
	}
	got, err = svc.Get(ctx)
	if err != nil || got != withPlan {
		t.Fatalf("Get(with plan) = (%+v, %v), want %+v", got, err, withPlan)
	}
}

func TestSwarmServiceRejectsDanglingAndCrossAccountFavorites(t *testing.T) {
	_, svc, favorites := openSwarmServiceTest(t)
	ctx := swarmTestPrincipal("account-a")
	putSwarmTestFavorite(t, favorites, "account-a", "action-a")
	putSwarmTestFavorite(t, favorites, "account-b", "action-b")
	putSwarmTestFavorite(t, favorites, "account-b", "plan-b")

	tests := []struct {
		name  string
		input SwarmSettingsInput
		want  error
	}{
		{name: "missing action", input: SwarmSettingsInput{ActionFavoriteID: "missing"}, want: ErrSwarmActionFavoriteNotFound},
		{name: "cross account action", input: SwarmSettingsInput{ActionFavoriteID: "action-b"}, want: ErrSwarmActionFavoriteNotFound},
		{name: "missing plan", input: SwarmSettingsInput{ActionFavoriteID: "action-a", PlanEnabled: true, PlanFavoriteID: "missing"}, want: ErrSwarmPlanFavoriteNotFound},
		{name: "cross account plan", input: SwarmSettingsInput{ActionFavoriteID: "action-a", PlanEnabled: true, PlanFavoriteID: "plan-b"}, want: ErrSwarmPlanFavoriteNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := svc.Put(ctx, test.input); !errors.Is(err, test.want) {
				t.Fatalf("Put() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSwarmServiceRejectsContradictoryPlanConfiguration(t *testing.T) {
	_, svc, favorites := openSwarmServiceTest(t)
	ctx := swarmTestPrincipal("account-a")
	putSwarmTestFavorite(t, favorites, "account-a", "action")

	tests := []struct {
		input SwarmSettingsInput
		storeError error
	}{
		{input: SwarmSettingsInput{ActionFavoriteID: "action", PlanEnabled: true}, storeError: pebblestore.ErrSwarmModePlanFavoriteIDRequired},
		{input: SwarmSettingsInput{ActionFavoriteID: "action", PlanFavoriteID: "plan"}, storeError: pebblestore.ErrSwarmModePlanFavoriteIDUnexpected},
	}
	for _, test := range tests {
		_, err := svc.Put(ctx, test.input)
		if !errors.Is(err, ErrSwarmPlanConfigurationContradictory) || !errors.Is(err, test.storeError) {
			t.Fatalf("Put(%+v) error = %v, want contradictory and %v", test.input, err, test.storeError)
		}
	}
}

func TestSwarmServiceRejectsPersistedDanglingReferences(t *testing.T) {
	_, svc, favorites := openSwarmServiceTest(t)
	ctx := swarmTestPrincipal("account-a")
	putSwarmTestFavorite(t, favorites, "account-a", "action")

	if _, err := svc.settings.PutForAccount(SwarmSettings{AccountScopeID: "account-a", ActionFavoriteID: "missing"}); err != nil {
		t.Fatalf("seed dangling action: %v", err)
	}
	if _, err := svc.Get(ctx); !errors.Is(err, ErrSwarmActionFavoriteNotFound) {
		t.Fatalf("Get(dangling action) error = %v, want %v", err, ErrSwarmActionFavoriteNotFound)
	}

	if _, err := svc.settings.PutForAccount(SwarmSettings{AccountScopeID: "account-a", ActionFavoriteID: "action", PlanEnabled: true, PlanFavoriteID: "missing"}); err != nil {
		t.Fatalf("seed dangling plan: %v", err)
	}
	if _, err := svc.Get(ctx); !errors.Is(err, ErrSwarmPlanFavoriteNotFound) {
		t.Fatalf("Get(dangling plan) error = %v, want %v", err, ErrSwarmPlanFavoriteNotFound)
	}
}

func TestSwarmServiceMapsPersistedContradictoryShape(t *testing.T) {
	store, svc, favorites := openSwarmServiceTest(t)
	putSwarmTestFavorite(t, favorites, "account-a", "action")
	if err := store.PutJSON("swarm/mode_settings_by_account/account-a", SwarmSettings{
		AccountScopeID: "account-a", ActionFavoriteID: "action", PlanFavoriteID: "plan",
	}); err != nil {
		t.Fatalf("seed contradictory settings: %v", err)
	}
	_, err := svc.Get(swarmTestPrincipal("account-a"))
	if !errors.Is(err, ErrSwarmPlanConfigurationContradictory) || !errors.Is(err, pebblestore.ErrSwarmModePlanFavoriteIDUnexpected) {
		t.Fatalf("Get() error = %v, want contradictory and storage sentinel", err)
	}
}

func openSwarmServiceTest(t *testing.T) (*pebblestore.Store, *SwarmService, *pebblestore.ModelProfileStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	favorites := pebblestore.NewModelProfileStore(store)
	return store, NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favorites), favorites
}

func swarmTestPrincipal(accountScopeID string) context.Context {
	return identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type: identity.PrincipalTypeUser, UserID: "user-" + accountScopeID, AccountScopeID: accountScopeID,
	})
}

func putSwarmTestFavorite(t *testing.T, favorites *pebblestore.ModelProfileStore, accountScopeID, favoriteID string) {
	t.Helper()
	_, err := favorites.PutForAccount(pebblestore.ModelProfileRecord{
		ProfileID: favoriteID, AccountScopeID: accountScopeID, Name: favoriteID,
		Provider: "codex", Model: "gpt", Thinking: "high",
	})
	if err != nil {
		t.Fatalf("put favorite %q for %q: %v", favoriteID, accountScopeID, err)
	}
}
