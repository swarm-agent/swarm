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

func TestSwarmTopologyRoutesUseAccountScopedReadsNoGlobalFallback(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-route-no-fallback.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	if _, err := topologyStore.PutRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "global-runtime", Name: "global"}); err != nil {
		t.Fatalf("put global runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimeForAccount("account-a", pebblestore.TopologyRuntimeRecord{SwarmID: "runtime-a", UserID: "user-a", AccountScopeID: "account-a", Name: "runtime a"}); err != nil {
		t.Fatalf("put account A runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimeForAccount("account-b", pebblestore.TopologyRuntimeRecord{SwarmID: "runtime-b", UserID: "user-b", AccountScopeID: "account-b", Name: "runtime b"}); err != nil {
		t.Fatalf("put account B runtime: %v", err)
	}

	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologyruntime.NewService(topologyStore, nil, nil, nil, nil))

	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v1/swarm/topology", nil), "user-a", "account-a")
	rec := httptest.NewRecorder()
	server.handleSwarmTopologySnapshot(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response topologySnapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Runtimes) != 1 || response.Runtimes[0].SwarmID != "runtime-a" {
		t.Fatalf("account A runtimes = %+v", response.Runtimes)
	}
}
