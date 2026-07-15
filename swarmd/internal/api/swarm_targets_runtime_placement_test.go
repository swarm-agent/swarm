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
	if target.SwarmID != "runtime-no-backend" || target.BackendURL != "" || target.HostSwarmID != "host-swarm" || target.DeploymentID != "host-swarm:container-1" || !target.Selectable {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestSwarmTargetsTopologyRuntimeWithoutBackendURL(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{
		SwarmID:        "managed-host",
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Name:           "Managed Host",
		Relationship:   "managed",
	}); err != nil {
		t.Fatalf("upsert host topology runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyHostContainerForAccount(topologyStore, "account-1", pebblestore.TopologyHostContainerRecord{
		HostContainerID:     "managed-host:container-1",
		AccountScopeID:      "account-1",
		UserID:              "user-1",
		HostSwarmID:         "managed-host",
		RuntimeContainerRef: "container-1",
		Name:                "Managed Container",
	}); err != nil {
		t.Fatalf("upsert topology host container: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{
		SwarmID:              "managed-container",
		AccountScopeID:       "account-1",
		UserID:               "user-1",
		Name:                 "Managed Container",
		Relationship:         "child",
		Status:               "attached",
		OwnerHostSwarmID:     "managed-host",
		OwnerHostContainerID: "managed-host:container-1",
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	placement, ok, err := topologyStore.GetRuntimePlacementForAccount("account-1", "managed-container")
	if err != nil || !ok {
		t.Fatalf("get runtime placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "managed-host" {
		t.Fatalf("unexpected placement: %+v", placement)
	}

	server := &Server{topology: topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil)}
	targets, err := server.listTopologyTargetsForAccount("account-1")
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	var target swarmTarget
	for _, candidate := range targets {
		if candidate.SwarmID == "managed-container" {
			target = candidate
		}
	}
	if target.SwarmID != "managed-container" || target.BackendURL != "" || target.HostSwarmID != "managed-host" || !target.Selectable {
		t.Fatalf("unexpected target: %+v", target)
	}
}
