package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestWorkspaceOverviewIncludesTopologyRoutesFromWorkspaceBindings(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID:      "managed-swarm-1",
			Name:         "managed-host",
			Role:         swarmruntime.RelationshipManaged,
			Relationship: swarmruntime.RelationshipManaged,
			RendezvousTransports: []swarmruntime.TransportSummary{{
				Kind:    startupconfig.NetworkModeTailscale,
				Primary: "https://managed.example.test",
				All:     []string{"https://managed.example.test"},
			}},
		}},
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "managed-swarm-1", Name: "managed-host", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecord(topologyStore, pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm-1", Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test", ObservedSources: []string{pebblestore.TopologyRuntimeSourceDeployContainer}}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBinding(topologyStore, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-1",
		SourceWorkspacePath:       workspacePath,
		SourceWorkspaceName:       "workspace-one",
		DestinationRuntimeSwarmID: "managed-swarm-1",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationContainerID:    "container-1",
		DestinationWorkspacePath:  "/workspaces/workspace-one",
		ReplicationMode:           "continuous",
		Writable:                  true,
		Sync:                      pebblestore.WorkspaceReplicationSync{Enabled: true, Mode: "bidirectional"},
		LegacyTargetKind:          "legacy-kind-must-not-be-needed",
		CreatedAt:                 11,
		UpdatedAt:                 22,
	}); err != nil {
		t.Fatalf("upsert topology binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 1 {
		t.Fatalf("workspace count=%d response=%+v", len(response.Workspaces), response)
	}
	routes := response.Workspaces[0].TopologyRoutes
	if len(routes) != 1 {
		t.Fatalf("topology route count=%d routes=%+v", len(routes), routes)
	}
	route := routes[0]
	if route.RouteSource != workspaceOverviewTopologyRouteSource {
		t.Fatalf("route source=%q", route.RouteSource)
	}
	if route.WorkspaceBindingID != "binding-1" || route.RuntimeSwarmID != "managed-swarm-1" || route.RuntimeSwarmName != "managed-host" {
		t.Fatalf("unexpected runtime route fields: %+v", route)
	}
	if route.HostWorkspacePath != workspacePath || route.RuntimeWorkspacePath != "/workspaces/workspace-one" {
		t.Fatalf("unexpected workspace route paths: %+v", route)
	}
	if route.RouteID != "swarm:managed-swarm-1:/workspaces/workspace-one" {
		t.Fatalf("route id=%q", route.RouteID)
	}
	if route.HostSwarmName != "host-swarm" {
		t.Fatalf("host swarm name=%q route=%+v", route.HostSwarmName, route)
	}
	if len(response.Workspaces[0].ReplicationLinks) != 0 {
		t.Fatalf("test must prove topology routes do not come from legacy replication links: %+v", response.Workspaces[0].ReplicationLinks)
	}
}

func TestWorkspaceOverviewSkipsStaleTopologyBindings(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID:      "live-swarm",
			Name:         "Live",
			Role:         swarmruntime.RelationshipManaged,
			Relationship: swarmruntime.RelationshipManaged,
			RendezvousTransports: []swarmruntime.TransportSummary{{
				Kind:    startupconfig.NetworkModeTailscale,
				Primary: "http://live.example",
				All:     []string{"http://live.example"},
			}},
		}},
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "live-swarm", Name: "Live", SwarmRole: swarmruntime.RelationshipChild, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	for _, binding := range []pebblestore.TopologyWorkspaceBindingRecord{
		{BindingID: "binding-live", SourceWorkspacePath: workspacePath, DestinationRuntimeSwarmID: "live-swarm", DestinationWorkspacePath: "/workspaces/live", Writable: true},
		{BindingID: "binding-stale", SourceWorkspacePath: workspacePath, DestinationRuntimeSwarmID: "missing-swarm", DestinationWorkspacePath: "/workspaces/stale", Writable: true},
	} {
		if _, err := pebblestore.UpsertTopologyWorkspaceBinding(topologyStore, binding); err != nil {
			t.Fatalf("upsert binding %q: %v", binding.BindingID, err)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 1 {
		t.Fatalf("workspace count=%d response=%+v", len(response.Workspaces), response)
	}
	routes := response.Workspaces[0].TopologyRoutes
	if len(routes) != 1 {
		t.Fatalf("topology route count=%d routes=%+v", len(routes), routes)
	}
	if routes[0].WorkspaceBindingID != "binding-live" || routes[0].RuntimeSwarmID != "live-swarm" {
		t.Fatalf("unexpected route after stale filter: %+v", routes[0])
	}
}

func TestWorkspaceOverviewIncludesManagedChildLoopbackRouteViaOwnerHost(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID:      "managed-swarm-1",
			Name:         "managed-host",
			Role:         swarmruntime.RelationshipManaged,
			Relationship: swarmruntime.RelationshipManaged,
			RendezvousTransports: []swarmruntime.TransportSummary{{
				Kind:    startupconfig.NetworkModeTailscale,
				Primary: "https://managed.example.test",
				All:     []string{"https://managed.example.test"},
			}},
		}},
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "managed-swarm-1", Name: "managed-host", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecord(topologyStore, pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm-1", Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test"}); err != nil {
		t.Fatalf("upsert host runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecord(topologyStore, pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm-1", Name: "heytest", Relationship: "child", BackendURL: "http://127.0.0.1:7782", OwnerHostSwarmID: "managed-swarm-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBinding(topologyStore, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-managed-child",
		SourceWorkspacePath:       workspacePath,
		SourceWorkspaceName:       "workspace-one",
		DestinationRuntimeSwarmID: "child-swarm-1",
		DestinationHostSwarmID:    "managed-swarm-1",
		DestinationContainerID:    "managed-swarm-1:container-1",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
	if _, err := server.swarmMirror.UpsertRemoteResource("managed-swarm-1", pebblestore.SwarmMirrorEventRecord{Sequence: 1, EventType: pebblestore.SwarmMirrorEventTypeUpsert, Kind: mirrorResourceTarget, ID: "child-swarm-1", Resource: []byte(`{"swarm_id":"child-swarm-1","name":"heytest","role":"child","relationship":"child","kind":"local","backend_url":"http://127.0.0.1:7782","online":false,"selectable":false}`)}); err != nil {
		t.Fatalf("upsert mirrored target: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 1 || len(response.Workspaces[0].TopologyRoutes) != 1 {
		t.Fatalf("overview response=%+v", response)
	}
	route := response.Workspaces[0].TopologyRoutes[0]
	if route.RuntimeSwarmID != "child-swarm-1" || route.RuntimeSwarmName != "heytest" || route.RuntimeBackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("runtime route fields = %+v", route)
	}
	if route.HostSwarmID != "managed-swarm-1" || route.HostSwarmName != "managed-host" || route.RuntimeKind != "mirrored" {
		t.Fatalf("host route fields = %+v", route)
	}
}

func TestWorkspaceOverviewSkipsOfflineTopologyBindings(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID:      "offline-swarm",
			Name:         "Offline",
			Role:         swarmruntime.RelationshipManaged,
			Relationship: swarmruntime.RelationshipManaged,
		}},
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "offline-swarm", Name: "Offline", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	if _, err := pebblestore.UpsertTopologyWorkspaceBinding(topologyStore, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-offline",
		SourceWorkspacePath:       workspacePath,
		DestinationRuntimeSwarmID: "offline-swarm",
		DestinationWorkspacePath:  "/workspaces/offline",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("upsert topology binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 1 {
		t.Fatalf("workspace count=%d response=%+v", len(response.Workspaces), response)
	}
	if routes := response.Workspaces[0].TopologyRoutes; len(routes) != 0 {
		t.Fatalf("expected offline route to be hidden, got %+v", routes)
	}
}

func newWorkspaceOverviewTopologyTestServer(t *testing.T) (*Server, string, *pebblestore.Store) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-overview.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	workspacePath := filepath.Join(t.TempDir(), "workspace-one")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if _, err := workspaceSvc.Add(workspacePath, "workspace-one", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	sessionSvc := session.NewService(pebblestore.NewSessionStore(store), nil)
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	server.SetSwarmMirrorStore(pebblestore.NewSwarmMirrorStore(store))
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, nil, workspaceStore))
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server, workspacePath, store
}
