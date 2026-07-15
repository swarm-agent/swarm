package topology

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestEnsureLocalWorkspaceSelfBindingForPrincipalCreatesAndReusesBinding(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-self-binding.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount("account-a", pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: "account-a", AuthorityHostSwarmID: "local-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 3, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put local self placement: %v", err)
	}
	service := NewService(topologyStore, swarmStore)
	workspaceEntry := pebblestore.WorkspaceEntry{AccountScopeID: "account-a", WorkspaceID: "ws-a", WorkspaceGeneration: 1, Path: "/workspace-a", Name: "workspace-a"}

	binding, err := service.EnsureLocalWorkspaceSelfBindingForPrincipal("account-a", "user-a", workspaceEntry)
	if err != nil {
		t.Fatalf("ensure binding: %v", err)
	}
	if binding.BindingID == "" || binding.SourceWorkspaceID != workspaceEntry.WorkspaceID || binding.DestinationRuntimeSwarmID != "local-swarm" {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if binding.PlacementGeneration != 3 {
		t.Fatalf("binding did not preserve placement generation: %+v", binding)
	}
	bindingAgain, err := service.EnsureLocalWorkspaceSelfBindingForPrincipal("account-a", "user-a", workspaceEntry)
	if err != nil {
		t.Fatalf("ensure binding again: %v", err)
	}
	if bindingAgain.BindingID != binding.BindingID || bindingAgain.CreatedAt != binding.CreatedAt {
		t.Fatalf("binding was not reused: first=%+v second=%+v", binding, bindingAgain)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForPrincipalRequiresExistingPlacement(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-self-binding-missing-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	service := NewService(topologyStore, swarmStore)
	workspaceEntry := pebblestore.WorkspaceEntry{AccountScopeID: "account-a", WorkspaceID: "ws-a", WorkspaceGeneration: 1, Path: "/workspace-a", Name: "workspace-a"}

	_, err = service.EnsureLocalWorkspaceSelfBindingForPrincipal("account-a", "user-a", workspaceEntry)
	if err == nil || !strings.Contains(err.Error(), "local self runtime placement is required") {
		t.Fatalf("expected missing placement error, got %v", err)
	}
	if _, ok, err := topologyStore.GetRuntimePlacementForAccount("account-a", "local-swarm"); err != nil || ok {
		t.Fatalf("self-binding lazily created placement ok=%t err=%v", ok, err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForPrincipalRejectsInvalidNonHostPlacement(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-self-binding-invalid-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if err := store.PutJSON(pebblestore.KeyTopologyRuntimePlacementForAccount("account-a", "local-swarm"), pebblestore.TopologyRuntimePlacementRecord{PlacementID: "rtp_invalid", RuntimeSwarmID: "local-swarm", AccountScopeID: "account-a", AuthorityHostSwarmID: "local-swarm", AuthorityContainerID: "container-a", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put invalid local placement: %v", err)
	}
	service := NewService(topologyStore, swarmStore)
	workspaceEntry := pebblestore.WorkspaceEntry{AccountScopeID: "account-a", WorkspaceID: "ws-a", WorkspaceGeneration: 1, Path: "/workspace-a", Name: "workspace-a"}

	_, err = service.EnsureLocalWorkspaceSelfBindingForPrincipal("account-a", "user-a", workspaceEntry)
	if err == nil || (!strings.Contains(err.Error(), "local self placement is invalid") && !strings.Contains(err.Error(), "topology host runtime placement authority container id must be empty")) {
		t.Fatalf("expected invalid placement error, got %v", err)
	}
}
