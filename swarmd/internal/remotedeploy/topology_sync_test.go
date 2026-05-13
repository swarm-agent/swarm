package remotedeploy

import (
	"path/filepath"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCleanupRemoteChildStateUsesCanonicalAttachmentIDs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "remote-topology-cleanup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	nodeStore := pebblestore.NewSwarmNodeStore(store, topologyStore)
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "child-swarm", Name: "Child", Relationship: "child"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	if _, err := nodeStore.Put(pebblestore.SwarmNodeRecord{SwarmID: "child-swarm", Name: "Child", Role: "child", BackendURL: "http://child.example:7781", Status: "online"}); err != nil {
		t.Fatalf("put node: %v", err)
	}

	record := pebblestore.RemoteDeploySessionRecord{
		ID:          "session-1",
		Name:        "remote child",
		HostSwarmID: "host-swarm",
	}
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID("host-swarm", remoteContainerNameForSession(record.ID))
	if err := pebblestore.UpsertTopologyHostContainer(topologyStore, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		HostSwarmID:         "host-swarm",
		RuntimeContainerRef: remoteContainerNameForSession(record.ID),
		Name:                "remote child",
		ContainerName:       remoteContainerNameForSession(record.ID),
		Runtime:             "docker",
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceRemoteDeploySession},
	}); err != nil {
		t.Fatalf("put host container: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecord(topologyStore, pebblestore.TopologyRuntimeRecord{
		SwarmID:         "child-swarm",
		Name:            "Child",
		Relationship:    "child",
		ObservedSources: []string{pebblestore.TopologyRuntimeSourceRemoteDeploySession},
	}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	attachmentID := pebblestore.CanonicalTopologyAttachmentID(hostContainerID, "child-swarm")
	if err := pebblestore.UpsertTopologyAttachment(topologyStore, pebblestore.TopologyAttachmentRecord{
		AttachmentID:          attachmentID,
		HostContainerID:       hostContainerID,
		RuntimeSwarmID:        "child-swarm",
		RemoteDeploySessionID: record.ID,
		State:                 "attached",
	}); err != nil {
		t.Fatalf("put attachment: %v", err)
	}

	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), nodeStore, nil, swarmStore, nil, nil, nil, "", "", topologyStore)
	item := &localcontainers.DeleteItemResult{}
	if err := svc.cleanupRemoteChildState(record, item); err != nil {
		t.Fatalf("cleanup remote child state: %v", err)
	}
	if _, ok, err := topologyStore.GetAttachment(attachmentID); err != nil || ok {
		t.Fatalf("attachment remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := topologyStore.GetHostContainer(hostContainerID); err != nil || ok {
		t.Fatalf("host container remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := topologyStore.GetRuntime("child-swarm"); err != nil || ok {
		t.Fatalf("runtime remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := swarmStore.GetTrustedPeer("child-swarm"); err != nil || ok {
		t.Fatalf("trusted peer remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := nodeStore.Get("child-swarm"); err != nil || ok {
		t.Fatalf("node remaining ok=%t err=%v", ok, err)
	}
}
