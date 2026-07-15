package api

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestValidateAccountSessionWorkspaceBindingPreservesLocalBindingAuthority(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	binding := pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-self",
		UserID:                          principal.UserID,
		AccountScopeID:                  principal.AccountScopeID,
		SourceWorkspaceID:               "workspace-self",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/workspace/self",
		DestinationRuntimeSwarmID:       "self-swarm",
		DestinationAuthorityHostSwarmID: "self-swarm",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        "/workspace/self",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "self-swarm",
	}
	if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
		t.Fatalf("store binding: %v", err)
	}
	stored, ok, err := server.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, binding.BindingID)
	if err != nil || !ok {
		t.Fatalf("get binding: ok=%t err=%v", ok, err)
	}
	if err := server.validateAccountSessionWorkspaceBinding(principal, stored, "self-swarm", "session"); err != nil {
		t.Fatalf("validate local binding: %v", err)
	}
	if err := server.validateAccountSessionWorkspaceBinding(identity.Principal{}, binding, "self-swarm", "session"); err == nil {
		t.Fatal("missing principal was accepted")
	}
}
