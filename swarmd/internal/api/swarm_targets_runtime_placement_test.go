package api

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestMapTopologyRuntimeTargetKeepsIdentityWithoutBackendURL(t *testing.T) {
	target, ok := mapTopologyRuntimeTarget(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "runtime-no-backend",
		Name:                 "Runtime Without Backend",
		Relationship:         "child",
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm",
		OwnerHostContainerID: "host-swarm:container-1",
	})
	if !ok {
		t.Fatal("expected runtime target without backend URL to map")
	}
	if target.SwarmID != "runtime-no-backend" || target.BackendURL != "" || target.HostSwarmID != "host-swarm" || target.DeploymentID != "host-swarm:container-1" || target.Kind != "mirrored" || !target.Selectable {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestSwarmTargetsLocalContainerRuntimeWithoutBackendURL(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{
		SwarmID:        "local-host",
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Name:           "Local Host",
		Relationship:   "self",
	}); err != nil {
		t.Fatalf("upsert local host topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount("account-1", pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:      "local-host",
		AccountScopeID:      "account-1",
		AuthorityHostSwarmID: "local-host",
		RuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	}); err != nil {
		t.Fatalf("put local host placement: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{
		SwarmID:              "local-container",
		AccountScopeID:       "account-1",
		UserID:               "user-1",
		Name:                 "Local Container",
		Relationship:         "child",
		Status:               "attached",
		OwnerHostSwarmID:     "local-host",
		OwnerHostContainerID: "local-host:container-1",
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount("account-1", pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:      "local-container",
		AccountScopeID:      "account-1",
		AuthorityHostSwarmID: "local-host",
		AuthorityContainerID: "local-host:container-1",
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	}); err != nil {
		t.Fatalf("put local container placement: %v", err)
	}
	placement, ok, err := topologyStore.GetRuntimePlacementForAccount("account-1", "local-container")
	if err != nil || !ok {
		t.Fatalf("get runtime placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "local-host" {
		t.Fatalf("unexpected placement: %+v", placement)
	}

	server := &Server{topology: topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil)}
	targets, err := server.listTopologyTargetsForAccount("account-1")
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	var target swarmTarget
	for _, candidate := range targets {
		if candidate.SwarmID == "local-container" {
			target = candidate
		}
	}
	if target.SwarmID != "local-container" || target.BackendURL != "" || target.HostSwarmID != "local-host" || target.DeploymentID != "local-host:container-1" || target.Kind != "mirrored" || !target.Selectable {
		t.Fatalf("unexpected target: %+v", target)
	}
}
