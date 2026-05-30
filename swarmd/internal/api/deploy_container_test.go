package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestDeployContainerAttachApproveAcceptsPeerAuthTokens(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	payload, err := json.Marshal(map[string]any{
		"deployment_id":                 "deployment-1",
		"bootstrap_secret":              "bootstrap-secret",
		"host_swarm_id":                 "host-swarm",
		"host_display_name":             "Host",
		"host_public_key":               "host-public-key",
		"host_fingerprint":              "host-fingerprint",
		"host_backend_url":              "http://127.0.0.1:7781",
		"host_desktop_url":              "http://127.0.0.1:5555",
		"host_to_child_peer_auth_token": "host-to-child-token",
		"child_to_host_peer_auth_token": "child-to-host-token",
		"group_id":                      "group-1",
		"group_name":                    "Primary Group",
		"group_network_name":            "group-net",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/attach/approve", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fakeDeploy.lastAttachApproveInput.HostToChildPeerAuthToken != "host-to-child-token" {
		t.Fatalf("host to child token = %q, want %q", fakeDeploy.lastAttachApproveInput.HostToChildPeerAuthToken, "host-to-child-token")
	}
	if fakeDeploy.lastAttachApproveInput.ChildToHostPeerAuthToken != "child-to-host-token" {
		t.Fatalf("child to host token = %q, want %q", fakeDeploy.lastAttachApproveInput.ChildToHostPeerAuthToken, "child-to-host-token")
	}
}

func TestDeployContainerAttachRequestDoesNotRetireSessionRoutes(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{attachRequestState: deployruntime.ContainerAttachState{
		DeploymentID:    "deployment-1",
		AttachStatus:    "requested",
		ChildSwarmID:    "new-child-swarm",
		ChildBackendURL: "http://127.0.0.1:7781/",
	}}
	server.SetDeployContainerService(fakeDeploy)
	routeStore, topologyStore, cleanup := installDeployAttachStaleRouteTestStores(t, server)
	defer cleanup()

	payload, err := json.Marshal(map[string]any{
		"deployment_id":     "deployment-1",
		"bootstrap_secret":  "bootstrap-secret",
		"child_swarm_id":    "new-child-swarm",
		"child_backend_url": "http://127.0.0.1:7781/",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/attach/request", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertDeployAttachStaleRouteStillPresent(t, routeStore, topologyStore)
}

func TestDeployContainerAttachApproveDoesNotRetireSessionRoutes(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{attachApproveState: deployruntime.ContainerAttachState{
		DeploymentID:    "deployment-1",
		AttachStatus:    "attached",
		ChildSwarmID:    "new-child-swarm",
		ChildBackendURL: "http://127.0.0.1:7781/",
	}}
	server.SetDeployContainerService(fakeDeploy)
	routeStore, topologyStore, cleanup := installDeployAttachStaleRouteTestStores(t, server)
	defer cleanup()

	payload, err := json.Marshal(map[string]any{
		"deployment_id":      "deployment-1",
		"bootstrap_secret":   "bootstrap-secret",
		"host_swarm_id":      "host-swarm",
		"host_backend_url":   "http://127.0.0.1:7780",
		"group_id":           "group-1",
		"group_name":         "Primary Group",
		"group_network_name": "group-net",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/attach/approve", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertDeployAttachStaleRouteStillPresent(t, routeStore, topologyStore)
}

func installDeployAttachStaleRouteTestStores(t *testing.T, server *Server) (*pebblestore.SessionRouteStore, *pebblestore.TopologyStore, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "deploy-attach-routes.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	routeStore := pebblestore.NewSessionRouteStore(store)
	topologyStore := pebblestore.NewTopologyStore(store)
	server.SetSessionRouteStore(routeStore)
	server.SetTopologyService(topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, nil))
	route := pebblestore.SessionRouteRecord{
		SessionID:       "session-stale",
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ChildSwarmID:    "old-child-swarm",
		ChildBackendURL: "http://127.0.0.1:7781",
		HostSwarmID:     "host-swarm",
	}
	if _, err := routeStore.Put(route); err != nil {
		t.Fatalf("put session route: %v", err)
	}
	if _, err := topologyStore.PutSessionRouteForAccount(testPrincipal().AccountScopeID, pebblestore.TopologySessionRouteRecord{
		SessionID:      route.SessionID,
		UserID:         route.UserID,
		AccountScopeID: route.AccountScopeID,
		RuntimeSwarmID: route.ChildSwarmID,
		HostSwarmID:    route.HostSwarmID,
		BackendURL:     route.ChildBackendURL,
	}); err != nil {
		t.Fatalf("put topology session route: %v", err)
	}
	return routeStore, topologyStore, func() { _ = store.Close() }
}

func assertDeployAttachStaleRouteStillPresent(t *testing.T, routeStore *pebblestore.SessionRouteStore, topologyStore *pebblestore.TopologyStore) {
	t.Helper()
	if route, ok, err := routeStore.Get("session-stale"); err != nil || !ok || route.ChildSwarmID != "old-child-swarm" {
		t.Fatalf("session route after attach ok=%t route=%+v err=%v", ok, route, err)
	}
	if route, ok, err := topologyStore.GetSessionRouteForAccount(testPrincipal().AccountScopeID, "session-stale"); err != nil || !ok || route.RuntimeSwarmID != "old-child-swarm" {
		t.Fatalf("topology route after attach ok=%t route=%+v err=%v", ok, route, err)
	}
}

func TestDeployContainerManagedCredentialsApplyAcknowledgesBundle(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	payload, err := json.Marshal(deployruntime.ContainerSyncCredentialBundle{
		OwnerSwarmID:   "host-swarm",
		BundlePassword: "bundle-password",
		Bundle:         []byte("bundle-payload"),
		Exported:       1,
		SnapshotHash:   "credential-snapshot",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/managed/credentials/apply", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fakeDeploy.lastAppliedCredentialBundle.OwnerSwarmID != "host-swarm" {
		t.Fatalf("owner swarm id = %q, want host-swarm", fakeDeploy.lastAppliedCredentialBundle.OwnerSwarmID)
	}
	if string(fakeDeploy.lastAppliedCredentialBundle.Bundle) != "bundle-payload" {
		t.Fatalf("bundle payload = %q, want bundle-payload", string(fakeDeploy.lastAppliedCredentialBundle.Bundle))
	}
}

func TestDeployContainerManagedAgentsApplyAcknowledgesBundle(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	payload, err := json.Marshal(deployruntime.ContainerSyncAgentBundle{
		State:        agentruntime.State{Profiles: []pebblestore.AgentProfile{{Name: "probe", Mode: "subagent", Enabled: true}}},
		SnapshotHash: "agent-snapshot",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/managed/agents/apply", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fakeDeploy.lastAppliedAgentBundle.SnapshotHash != "agent-snapshot" {
		t.Fatalf("agent snapshot = %q, want agent-snapshot", fakeDeploy.lastAppliedAgentBundle.SnapshotHash)
	}
	if len(fakeDeploy.lastAppliedAgentBundle.State.Profiles) != 1 || fakeDeploy.lastAppliedAgentBundle.State.Profiles[0].Name != "probe" {
		t.Fatalf("applied agent profiles = %#v", fakeDeploy.lastAppliedAgentBundle.State.Profiles)
	}
}
