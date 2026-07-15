package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestSwarmTopologySnapshotUsesSeededCanonicalSnapshot(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	runtimeRecord, err := topologyStore.PutRuntimeForAccount(testAccountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:        "child-1",
		UserID:         testUserID,
		AccountScopeID: testAccountScopeID,
		Name:           "Child One",
		Relationship:   "child",
	})
	if err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := topologyStore.PutMigrationStatus(pebblestore.TopologyMigrationStatusRecord{
		Version:      "checkpoint1-v1",
		RuntimeCount: 1,
	}); err != nil {
		t.Fatalf("put migration status: %v", err)
	}

	topologySvc := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil)
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologySvc)

	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/swarm/topology", nil))
	rr := httptest.NewRecorder()
	server.handleSwarmTopologySnapshot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response topologySnapshotResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK {
		t.Fatal("expected ok response")
	}
	if len(response.Runtimes) != 1 {
		t.Fatalf("runtime count = %d", len(response.Runtimes))
	}
	if response.Runtimes[0].SwarmID != runtimeRecord.SwarmID {
		t.Fatalf("runtime swarm id = %q", response.Runtimes[0].SwarmID)
	}
	if response.MigrationStatus.RuntimeCount != 1 {
		t.Fatalf("migration runtime count = %d", response.MigrationStatus.RuntimeCount)
	}
}

func TestSwarmTopologyRuntimeOwnerUsesSeededCanonicalAttachment(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-owner-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	runtimeRecord, err := topologyStore.PutRuntimeForAccount(testAccountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:        "child-1",
		UserID:         testUserID,
		AccountScopeID: testAccountScopeID,
		Name:           "Child One",
		Relationship:   "child",
	})
	if err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	hostContainerRecord, err := topologyStore.PutHostContainerForAccount(testAccountScopeID, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     "manager-1:ctr-1",
		UserID:              testUserID,
		AccountScopeID:      testAccountScopeID,
		HostSwarmID:         "manager-1",
		RuntimeContainerRef: "ctr-1",
		ContainerName:       "app",
	})
	if err != nil {
		t.Fatalf("put host container: %v", err)
	}
	attachmentRecord, err := topologyStore.PutAttachmentForAccount(testAccountScopeID, pebblestore.TopologyAttachmentRecord{
		AttachmentID:    "manager-1:ctr-1=>child-1",
		UserID:          testUserID,
		AccountScopeID:  testAccountScopeID,
		HostContainerID: hostContainerRecord.HostContainerID,
		RuntimeSwarmID:  runtimeRecord.SwarmID,
		State:           "attached",
	})
	if err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	if _, err := topologyStore.PutMigrationStatus(pebblestore.TopologyMigrationStatusRecord{
		Version:            "checkpoint1-v1",
		RuntimeCount:       1,
		HostContainerCount: 1,
		AttachmentCount:    1,
	}); err != nil {
		t.Fatalf("put migration status: %v", err)
	}

	topologySvc := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil)
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologySvc)

	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/swarm/topology/runtime-owner?runtime_swarm_id=child-1", nil))
	rr := httptest.NewRecorder()
	server.handleSwarmTopologyRuntimeOwner(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response topologyRuntimeOwnerResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Attachment == nil || response.HostContainer == nil {
		t.Fatalf("expected attachment + host container in response: %+v", response)
	}
	if response.Attachment.AttachmentID != attachmentRecord.AttachmentID {
		t.Fatalf("attachment id = %q", response.Attachment.AttachmentID)
	}
	if response.HostContainer.HostContainerID != hostContainerRecord.HostContainerID {
		t.Fatalf("host container id = %q", response.HostContainer.HostContainerID)
	}
}
