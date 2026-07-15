package topology

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestEnsureLocalSelfPlacementForAccountCreatesHostRuntimeAndPlacement(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "self-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}

	service := NewService(topologyStore, swarmStore, nil, nil, nil, nil)
	placement, err := service.EnsureLocalSelfPlacementForPrincipal("account-a", "user-a")
	if err != nil {
		t.Fatalf("ensure self placement: %v", err)
	}
	if placement.PlacementID == "" {
		t.Fatalf("placement id missing: %+v", placement)
	}
	if placement.RuntimeSwarmID != "local-swarm" || placement.AuthorityHostSwarmID != "local-swarm" || placement.AuthorityContainerID != "" {
		t.Fatalf("unexpected host placement authority: %+v", placement)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindHost || placement.State != pebblestore.TopologyRuntimePlacementStateActive || placement.PlacementGeneration != 1 {
		t.Fatalf("unexpected host placement defaults: %+v", placement)
	}

	runtime, ok, err := topologyStore.GetRuntimeForAccount("account-a", "local-swarm")
	if err != nil || !ok {
		t.Fatalf("get account runtime ok=%t err=%v", ok, err)
	}
	if runtime.Relationship != "self" || runtime.BackendURL != "" || runtime.Name != "Local" || runtime.UserID != "user-a" {
		t.Fatalf("unexpected account runtime: %+v", runtime)
	}

	placementAgain, err := service.EnsureLocalSelfPlacementForPrincipal("account-a", "user-a")
	if err != nil {
		t.Fatalf("ensure self placement again: %v", err)
	}
	if placementAgain.PlacementGeneration != placement.PlacementGeneration || placementAgain.CreatedAt != placement.CreatedAt || placementAgain.PlacementID != placement.PlacementID {
		t.Fatalf("self placement identity/generation/creation not stable: first=%+v second=%+v", placement, placementAgain)
	}
	placements, err := topologyStore.ListRuntimePlacementsForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list placements: %v", err)
	}
	if len(placements) != 1 || placements[0].RuntimeSwarmID != "local-swarm" {
		t.Fatalf("unexpected placements: %+v", placements)
	}
}

func TestEnsureLocalSelfPlacementForAccountFailsWhenLocalNodeMissing(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "self-placement-missing-node.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	service := NewService(pebblestore.NewTopologyStore(store), pebblestore.NewSwarmStore(store), nil, nil, nil, nil)
	_, err = service.EnsureLocalSelfPlacementForPrincipal("account-a", "user-a")
	if err == nil || !strings.Contains(err.Error(), "local swarm id is required") {
		t.Fatalf("expected missing local swarm id error, got %v", err)
	}
}

func TestEnsureLocalSelfPlacementForAccountRequiresUserIDWhenUsingPrincipalAPI(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "self-placement-missing-user.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	service := NewService(topologyStore, swarmStore, nil, nil, nil, nil)
	_, err = service.EnsureLocalSelfPlacementForPrincipal("account-a", "")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected missing user id error, got %v", err)
	}
}
