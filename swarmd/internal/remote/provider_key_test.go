package remote

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a local write client may ask for a human-entered provider key
// (manage_setup request_provider_key is write, as swarm_request_provider_key
// was in the swarmToolLevels record), while a relay client never reaches it:
// the action is local only, which binds every forwarded level, and the relay
// lists neither the removed swarm_request_provider_key nor forwards it.
// Threat: accidental relay exposure of the key page. Boundary: the catalog
// declaration, tool.CatalogLevelsAllow, filterTools and Service.handle's name
// gate; these pure policy boundaries are the narrowest test.
func TestProviderKeyWriteAndRelayExclusion(t *testing.T) {
	setup, ok := tool.CatalogToolNamed("manage_setup")
	if !ok {
		t.Fatal("manage_setup is not a catalog tool")
	}
	action, ok := setup.Action("request_provider_key")
	if !ok || action.Level.Scope() != ScopeWrite || swarmToolLevel("swarm_request_provider_key") != ScopeWrite {
		t.Fatalf("key request must be write: %+v", action)
	}
	if !action.LocalOnly || !swarmLocalOnlyTools["swarm_request_provider_key"] {
		t.Fatal("key request exposed to relay")
	}
	for _, scopes := range [][]string{{ScopeRead}, {ScopeRead, ScopeWrite}, {ScopeRead, ScopeWrite, ScopeApprove, ScopeManage}} {
		if tool.CatalogLevelsAllow(action, scopes, true) {
			t.Fatalf("a relay client with %v reaches request_provider_key", scopes)
		}
	}
	got := string(filterTools([]byte(`{"result":{"tools":[{"name":"swarm_request_provider_key"},{"name":"swarm_list_providers"},{"name":"manage_setup"}]}}`)))
	if strings.Contains(got, "swarm_request_provider_key") || strings.Contains(got, "swarm_list_providers") || !strings.Contains(got, "manage_setup") {
		t.Fatalf("incorrect relay tools: %s", got)
	}
}
