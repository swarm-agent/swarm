package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestSwarmTopologySnapshotReturnsCanonicalRuntimes(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	runtimeRecord, err := topologyStore.PutRuntimeForAccount(testAccountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:              "child-1",
		UserID:               testUserID,
		AccountScopeID:       testAccountScopeID,
		Name:                 "Child One",
		Relationship:         "child",
		BackendURL:           "https://retired-backend.example.test",
		DesktopURL:           "https://retired-desktop.example.test",
		OwnerHostSwarmID:     "retired-owner",
		OwnerHostContainerID: "retired-container",
	})
	if err != nil {
		t.Fatalf("put runtime: %v", err)
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
	if !response.OK || len(response.Runtimes) != 1 || response.Runtimes[0].SwarmID != runtimeRecord.SwarmID {
		t.Fatalf("unexpected topology response: %+v", response)
	}
	for _, retiredField := range []string{"backend_url", "desktop_url", "owner_host_swarm_id", "owner_host_container_id", "host_containers", "attachments", "session_routes", "migration_status"} {
		if strings.Contains(rr.Body.String(), `"`+retiredField+`"`) {
			t.Fatalf("retired topology field %q leaked in response: %s", retiredField, rr.Body.String())
		}
	}
}
