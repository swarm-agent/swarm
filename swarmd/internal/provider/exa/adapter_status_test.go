package exa

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: provider status text reaches the desktop, the terminal UI, SDK apps
// and MCP clients alike, so an Exa account without a key must say so plainly
// and must not point at one client's own UI or slash commands (Settings >
// Providers, /auth). Owner: Adapter.Status, the only producer of this reason.
func TestStatusWithoutKeyIsClientNeutral(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"})
	status, err := NewAdapter(pebblestore.NewAuthStore(store)).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Reason != "no Exa API key yet" {
		t.Fatalf("status = %+v, want not ready with a plain reason", status)
	}
	for _, clientSpecific := range []string{"Settings", "/auth"} {
		if strings.Contains(status.Reason, clientSpecific) {
			t.Fatalf("reason %q names a client-specific surface %q", status.Reason, clientSpecific)
		}
	}
}
