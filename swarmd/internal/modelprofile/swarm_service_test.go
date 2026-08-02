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

func TestSwarmServiceStoresDirectActionAndPlanSelections(t *testing.T) {
	_, svc := openSwarmServiceTest(t)
	ctx := swarmTestPrincipal("account-a")
	svc.now = func() time.Time { return time.UnixMilli(1234) }

	stored, err := svc.Put(ctx, SwarmSettingsInput{
		Action: pebblestore.ModelProfileSelection{Provider: " CODEX ", Model: "action-model", Thinking: "high", ServiceTier: "fast", ContextMode: "full"},
		Plan:   pebblestore.ModelProfileSelection{Provider: "OpenAI", Model: "plan-model", Thinking: "xhigh", ServiceTier: "priority", ContextMode: "compact"},
	})
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}
	if stored.Action.Provider != "codex" || stored.Action.Model != "action-model" || stored.Plan.Provider != "openai" || stored.Plan.Model != "plan-model" || stored.UpdatedAt != 1234 {
		t.Fatalf("Put() = %+v", stored)
	}
	got, err := svc.Get(ctx)
	if err != nil || got != stored {
		t.Fatalf("Get() = (%+v, %v), want %+v", got, err, stored)
	}
}

func TestSwarmServiceRequiresPrincipalAndBothSelections(t *testing.T) {
	_, svc := openSwarmServiceTest(t)
	selection := pebblestore.ModelProfileSelection{Provider: "codex", Model: "gpt", Thinking: "high"}
	if _, err := svc.Put(t.Context(), SwarmSettingsInput{Action: selection, Plan: selection}); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("Put() error = %v, want principal required", err)
	}
	ctx := swarmTestPrincipal("account-a")
	if _, err := svc.Get(ctx); !errors.Is(err, ErrSwarmModeSettingsNotFound) {
		t.Fatalf("Get() error = %v, want not found", err)
	}
	if _, err := svc.Put(ctx, SwarmSettingsInput{Plan: selection}); err == nil {
		t.Fatal("Put() accepted missing Action selection")
	}
	if _, err := svc.Put(ctx, SwarmSettingsInput{Action: selection}); err == nil {
		t.Fatal("Put() accepted missing Plan selection")
	}
}

func openSwarmServiceTest(t *testing.T) (*pebblestore.Store, *SwarmService) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store))
}

func swarmTestPrincipal(accountScopeID string) context.Context {
	return identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type: identity.PrincipalTypeUser, UserID: "user-" + accountScopeID, AccountScopeID: accountScopeID,
	})
}
