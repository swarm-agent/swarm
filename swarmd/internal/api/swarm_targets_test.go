package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestHandleSwarmTargetsReturnsRenamedSelfTargetImmediately(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-local-rename.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	swarmSvc := swarmruntime.NewService(pebblestore.NewSwarmStore(store), nil, nil)
	initial, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{Name: "Initial Primary", Role: "master"})
	if err != nil {
		t.Fatalf("ensure local swarm: %v", err)
	}
	if _, err := swarmSvc.RenameLocalSwarm(swarmruntime.RenameLocalSwarmInput{Name: "Renamed Primary"}); err != nil {
		t.Fatalf("rename local swarm: %v", err)
	}

	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm:             swarmSvc,
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	rec := httptest.NewRecorder()
	server.handleSwarmTargets(rec, requestWithTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/swarm/targets status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmTargetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Targets) == 0 {
		t.Fatal("expected at least one target")
	}
	self := response.Targets[0]
	if self.Name != "Renamed Primary" {
		t.Fatalf("self target name = %q, want renamed DB name", self.Name)
	}
	if self.SwarmID != initial.Node.SwarmID {
		t.Fatalf("self target swarm id = %q, want stable %q", self.SwarmID, initial.Node.SwarmID)
	}
	if !self.Current {
		t.Fatalf("self target should be current: %+v", self)
	}
}

func TestSwarmTargetsForRequestIncludesTopologyRuntime(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-topology.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "runtime-1", AccountScopeID: testPrincipal().AccountScopeID, UserID: testPrincipal().UserID, Name: "Runtime One", Relationship: "child", Status: "online"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm:             fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "local-swarm", Name: "local", Role: "master"}}},
		topology:          topologyruntime.NewService(topologyStore, nil),
	}
	targets, _, err := server.swarmTargetsForRequest(requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)))
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	for _, target := range targets {
		if target.SwarmID == "runtime-1" {
			return
		}
	}
	t.Fatalf("topology target missing: %+v", targets)
}
