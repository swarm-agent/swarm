package agentmodelsettings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceUsesPrincipalAndTargetedAssignments(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewAgentModelSettingsStore(store))
	svc.now = func() time.Time { return time.UnixMilli(123) }
	selection := Assignment{Provider: "codex", Model: "gpt", Thinking: "high"}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"})
	if _, err := pebblestore.NewAgentModelSettingsStore(store).PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: selection, Plan: selection},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: selection, Finder: selection, Coder: selection, Designer: selection, Router: selection,
		},
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := svc.ReplaceSwarm(ctx, SwarmInput{Action: selection, Plan: selection}); err != nil {
		t.Fatalf("ReplaceSwarm(): %v", err)
	}
	if _, err := svc.UpdateSystemAgent(ctx, "router", Assignment{Provider: "openai", Model: "router", Thinking: "medium"}); err != nil {
		t.Fatalf("UpdateSystemAgent(): %v", err)
	}
	got, err := svc.Get(ctx)
	if err != nil || got.AccountScopeID != "account" || got.SystemAgents.Router.Model != "router" || got.UpdatedAt != 123 {
		t.Fatalf("Get() = (%+v, %v)", got, err)
	}
	if _, err := svc.Get(context.Background()); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("Get() without principal error = %v", err)
	}
}
