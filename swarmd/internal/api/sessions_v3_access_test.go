package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: per-turn instructions describe account authority and authenticated
// device liveness without exposing another account's devices. Pure formatting
// is the narrowest layer for these user-visible instructions.
func TestSessionAccessInstructions(t *testing.T) {
	p := pebblestore.DefaultAccountPermissions()
	p.Tasks = "auto"
	clients := []pebblestore.ClientSessionRecord{{SessionID: "d", Device: "computer", AccountScopeID: "a", ExpiresAt: 300000, LastSeenAt: 200000}, {SessionID: "other", Device: "private-device", AccountScopeID: "b", ExpiresAt: 300000, LastSeenAt: 200000}}
	got := sessionAccessInstructions(p, clients, "a", 200000)
	if strings.Contains(got, "private-device") {
		t.Fatal("cross-account device leak")
	}
	for _, want := range []string{"starts at once; none waits", "connected", "files and terminal off", "browser off", "only approval is before tasks start", "default to auto"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions %q lack %q", got, want)
		}
	}
	if !isProtectedSessionsV3MetadataKey(pebblestore.SessionAccessMetadataKey) {
		t.Fatal("clients could set a session's access through metadata")
	}
	if err := validateSessionsV3CreateMetadata(map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "free"}}); err == nil {
		t.Fatal("create metadata could set a session's access")
	}
}
