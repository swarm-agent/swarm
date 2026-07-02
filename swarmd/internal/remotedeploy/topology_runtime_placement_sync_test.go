package remotedeploy

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRemoteDeploySyncCreatesContainerRuntimePlacement(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "remote-topology-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store, topologyStore), nil, pebblestore.NewSwarmStore(store, topologyStore), nil, nil, "", "", topologyStore)
	record := pebblestore.RemoteDeploySessionRecord{
		ID:           "session-1",
		Name:         "remote child",
		Status:       "attached",
		ChildSwarmID: "child-swarm",
		ChildName:    "Child",
		HostSwarmID:  "host-swarm",
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "host-swarm", pebblestore.TopologyRuntimeRecord{
		SwarmID:        "host-swarm",
		AccountScopeID: "host-swarm",
		UserID:         "host-swarm",
		Name:           "Host",
		Relationship:   "managed",
	}); err != nil {
		t.Fatalf("upsert host runtime: %v", err)
	}
	if err := svc.syncCanonicalRemoteDeployState(record); err != nil {
		t.Fatalf("sync remote topology: %v", err)
	}
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID("host-swarm", remoteContainerNameForSession(record.ID))
	placement, ok, err := topologyStore.GetRuntimePlacementForAccount("host-swarm", "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get runtime placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "host-swarm" || placement.AuthorityContainerID != hostContainerID {
		t.Fatalf("unexpected placement: %+v", placement)
	}
	runtimeRecord, ok, err := topologyStore.GetRuntimeForAccount("host-swarm", "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.BackendURL != "" {
		t.Fatalf("runtime backend URL should not be required for identity, got %q", runtimeRecord.BackendURL)
	}
}
