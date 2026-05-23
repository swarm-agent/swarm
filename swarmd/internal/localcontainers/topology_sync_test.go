package localcontainers

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestLocalContainerTopologySyncWritesAccountOwnedHostContainers(t *testing.T) {
	svc, topology, cleanup := newTopologySyncTestService(t)
	defer cleanup()

	recordA := topologySyncLocalContainerRecord("user-a", "account-a", "shared", "runtime-shared")
	recordB := topologySyncLocalContainerRecord("user-b", "account-b", "shared", "runtime-shared")

	if err := svc.syncTopologyHostContainer(recordA); err != nil {
		t.Fatalf("sync account A topology: %v", err)
	}
	if err := svc.syncTopologyHostContainer(recordB); err != nil {
		t.Fatalf("sync account B topology: %v", err)
	}

	hostContainerID := pebblestore.CanonicalTopologyHostContainerID("local-host", "runtime-shared")
	loadedA, ok, err := topology.GetHostContainerForAccount("account-a", hostContainerID)
	if err != nil || !ok {
		t.Fatalf("get account A host container ok=%t err=%v", ok, err)
	}
	if loadedA.UserID != "user-a" || loadedA.AccountScopeID != "account-a" {
		t.Fatalf("account A ownership = %+v", loadedA)
	}
	loadedB, ok, err := topology.GetHostContainerForAccount("account-b", hostContainerID)
	if err != nil || !ok {
		t.Fatalf("get account B host container ok=%t err=%v", ok, err)
	}
	if loadedB.UserID != "user-b" || loadedB.AccountScopeID != "account-b" {
		t.Fatalf("account B ownership = %+v", loadedB)
	}
	if _, ok, err := topology.GetHostContainerForAccount("account-b", loadedA.HostContainerID); err != nil || !ok {
		t.Fatalf("account B should have only its own account-scoped collision row ok=%t err=%v", ok, err)
	}
}

func TestLocalContainerTopologyDeleteIsAccountScoped(t *testing.T) {
	svc, topology, cleanup := newTopologySyncTestService(t)
	defer cleanup()

	recordA := topologySyncLocalContainerRecord("user-a", "account-a", "shared", "runtime-shared")
	recordB := topologySyncLocalContainerRecord("user-b", "account-b", "shared", "runtime-shared")
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID("local-host", "runtime-shared")
	attachmentID := pebblestore.CanonicalTopologyAttachmentID(hostContainerID, "child-swarm")

	for _, item := range []struct {
		account string
		user    string
	}{
		{account: "account-a", user: "user-a"},
		{account: "account-b", user: "user-b"},
	} {
		if _, err := topology.PutHostContainerForAccount(item.account, pebblestore.TopologyHostContainerRecord{
			HostContainerID:     hostContainerID,
			UserID:              item.user,
			AccountScopeID:      item.account,
			HostSwarmID:         "local-host",
			RuntimeContainerRef: "runtime-shared",
			Name:                "shared",
		}); err != nil {
			t.Fatalf("put host container for %s: %v", item.account, err)
		}
		if _, err := topology.PutAttachmentForAccount(item.account, pebblestore.TopologyAttachmentRecord{
			AttachmentID:    attachmentID,
			UserID:          item.user,
			AccountScopeID:  item.account,
			HostContainerID: hostContainerID,
			RuntimeSwarmID:  "child-swarm",
		}); err != nil {
			t.Fatalf("put attachment for %s: %v", item.account, err)
		}
	}
	if _, err := topology.PutHostContainer(pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		UserID:              "legacy-user",
		AccountScopeID:      "legacy-account",
		HostSwarmID:         "local-host",
		RuntimeContainerRef: "runtime-shared",
		Name:                "legacy",
	}); err != nil {
		t.Fatalf("put legacy/global host container: %v", err)
	}
	if _, err := topology.PutAttachment(pebblestore.TopologyAttachmentRecord{
		AttachmentID:    attachmentID,
		UserID:          "legacy-user",
		AccountScopeID:  "legacy-account",
		HostContainerID: hostContainerID,
		RuntimeSwarmID:  "child-swarm",
	}); err != nil {
		t.Fatalf("put legacy/global attachment: %v", err)
	}

	if err := svc.deleteTopologyHostContainer(recordA); err != nil {
		t.Fatalf("delete account A topology: %v", err)
	}
	if _, ok, err := topology.GetHostContainerForAccount("account-a", hostContainerID); err != nil || ok {
		t.Fatalf("account A host container after delete ok=%t err=%v", ok, err)
	}
	if _, ok, err := topology.GetAttachmentForAccount("account-a", attachmentID); err != nil || ok {
		t.Fatalf("account A attachment after delete ok=%t err=%v", ok, err)
	}
	loadedB, ok, err := topology.GetHostContainerForAccount("account-b", hostContainerID)
	if err != nil || !ok {
		t.Fatalf("account B host container should remain ok=%t err=%v", ok, err)
	}
	if loadedB.AccountScopeID != "account-b" || loadedB.UserID != "user-b" {
		t.Fatalf("account B host container ownership changed: %+v", loadedB)
	}
	if _, ok, err := topology.GetAttachmentForAccount("account-b", attachmentID); err != nil || !ok {
		t.Fatalf("account B attachment should remain ok=%t err=%v", ok, err)
	}
	if _, ok, err := topology.GetHostContainer(hostContainerID); err != nil || !ok {
		t.Fatalf("legacy/global host container should remain ok=%t err=%v", ok, err)
	}
	if _, ok, err := topology.GetAttachment(attachmentID); err != nil || !ok {
		t.Fatalf("legacy/global attachment should remain ok=%t err=%v", ok, err)
	}

	if err := svc.deleteTopologyHostContainer(recordB); err != nil {
		t.Fatalf("delete account B topology: %v", err)
	}
	if _, ok, err := topology.GetHostContainerForAccount("account-b", hostContainerID); err != nil || ok {
		t.Fatalf("account B host container after delete ok=%t err=%v", ok, err)
	}
}

func newTopologySyncTestService(t *testing.T) (*Service, *pebblestore.TopologyStore, func()) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dataDir, "topology-sync.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	topology := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-host", Name: "Local Host", Role: "manager"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	svc := &Service{
		store:      pebblestore.NewSwarmLocalContainerStore(store),
		swarmStore: swarmStore,
		topology:   topology,
	}
	return svc, topology, func() { _ = store.Close() }
}

func topologySyncLocalContainerRecord(userID, accountScopeID, id, runtimeContainerRef string) pebblestore.SwarmLocalContainerRecord {
	return pebblestore.SwarmLocalContainerRecord{
		ID:             id,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Name:           id,
		ContainerName:  id,
		Runtime:        "podman",
		Status:         "running",
		ContainerID:    runtimeContainerRef,
		HostAPIBaseURL: "http://127.0.0.1:7781",
		HostPort:       7781,
		RuntimePort:    7781,
	}
}
