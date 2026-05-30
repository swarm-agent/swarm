package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-swarm-1", Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test", ObservedSources: []string{pebblestore.TopologyRuntimeSourceDeployContainer}}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
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
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
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
	if route.RouteID != "swarm:managed-swarm-1:binding:binding-1" {
		t.Fatalf("route id=%q", route.RouteID)
	}
	if route.HostSwarmName != "host-swarm" {
		t.Fatalf("host swarm name=%q route=%+v", route.HostSwarmName, route)
	}
	if len(response.Workspaces[0].ReplicationLinks) != 0 {
		t.Fatalf("test must prove topology routes do not come from legacy replication links: %+v", response.Workspaces[0].ReplicationLinks)
	}
}

func TestWorkspaceOverviewTopologyRouteIDRemainsStableWhenRuntimePathChanges(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	changedWorkspacePath := filepath.Join(t.TempDir(), "workspace-one")
	if err := os.MkdirAll(changedWorkspacePath, 0o755); err != nil {
		t.Fatalf("mkdir changed workspace: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(testPrincipal(), changedWorkspacePath, "workspace-one", "", true); err != nil {
		t.Fatalf("add changed workspace: %v", err)
	}
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
	binding := pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		BindingID:                 "binding-stable",
		SourceWorkspacePath:       workspacePath,
		SourceWorkspaceName:       "workspace-one",
		DestinationRuntimeSwarmID: "managed-swarm-1",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationContainerID:    "container-1",
		DestinationWorkspacePath:  "/workspaces/workspace-one",
		ReplicationMode:           "continuous",
		Writable:                  true,
		LegacyTargetKind:          "legacy-kind-must-not-be-needed",
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("upsert initial topology binding: %v", err)
	}
	initialRoute := workspaceOverviewRouteForTest(t, server)
	if initialRoute.RouteID != "swarm:managed-swarm-1:binding:binding-stable" {
		t.Fatalf("initial route id=%q", initialRoute.RouteID)
	}
	if initialRoute.RuntimeWorkspacePath != "/workspaces/workspace-one" {
		t.Fatalf("initial runtime path=%q", initialRoute.RuntimeWorkspacePath)
	}

	if err := topologyStore.DeleteWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding.BindingID); err != nil {
		t.Fatalf("delete initial topology binding: %v", err)
	}
	binding.SourceWorkspacePath = changedWorkspacePath
	binding.SourceWorkspaceName = "changed-workspace-one"
	binding.DestinationWorkspacePath = "/workspaces/workspace-one"
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("put changed topology binding: %v", err)
	}
	changedRoute := workspaceOverviewTopologyRouteForBindingTest(t, server, binding, changedWorkspacePath)
	if changedRoute.RouteID != initialRoute.RouteID {
		t.Fatalf("route id changed with runtime path: got %q want %q", changedRoute.RouteID, initialRoute.RouteID)
	}
	if changedRoute.RuntimeWorkspacePath != "/workspaces/workspace-one" || changedRoute.HostWorkspacePath != changedWorkspacePath {
		t.Fatalf("changed paths host=%q runtime=%q", changedRoute.HostWorkspacePath, changedRoute.RuntimeWorkspacePath)
	}
}

func TestWorkspaceOverviewTopologyRouteIDUsesUnambiguousWorkspaceNameWhenBindingMissing(t *testing.T) {
	if got := workspaceOverviewTopologyRouteID("managed-swarm-1", "", "workspace-one", true); got != "swarm:managed-swarm-1:workspace:workspace-one" {
		t.Fatalf("route id=%q", got)
	}
	if got := workspaceOverviewTopologyRouteID("managed-swarm-1", "", "workspace-one", false); got != "" {
		t.Fatalf("ambiguous name route id=%q, want empty", got)
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
		{BindingID: "binding-live", SourceWorkspacePath: workspacePath, SourceWorkspaceName: "workspace-one", DestinationRuntimeSwarmID: "live-swarm", DestinationHostSwarmID: "host-swarm-id", DestinationWorkspacePath: "/workspaces/live", Writable: true},
		{BindingID: "binding-stale", SourceWorkspacePath: workspacePath, SourceWorkspaceName: "workspace-one", DestinationRuntimeSwarmID: "missing-swarm", DestinationHostSwarmID: "host-swarm-id", DestinationWorkspacePath: "/workspaces/stale", Writable: true},
	} {
		binding.UserID = testPrincipal().UserID
		binding.AccountScopeID = testPrincipal().AccountScopeID
		if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, binding); err != nil {
			t.Fatalf("upsert binding %q: %v", binding.BindingID, err)
		}
	}

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
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
	managedWorkspacePath := filepath.Join(t.TempDir(), "swarm-go")
	if err := os.MkdirAll(managedWorkspacePath, 0o755); err != nil {
		t.Fatalf("mkdir managed workspace: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(testPrincipal(), managedWorkspacePath, "swarm-go", "", true); err != nil {
		t.Fatalf("add managed workspace: %v", err)
	}
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
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test"}); err != nil {
		t.Fatalf("upsert host runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "heytest", Relationship: "child", BackendURL: "http://127.0.0.1:7782", OwnerHostSwarmID: "managed-swarm-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-managed-child",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
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
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 2 {
		t.Fatalf("workspace count=%d response=%+v", len(response.Workspaces), response)
	}
	var route workspaceOverviewTopologyRoute
	for _, workspace := range response.Workspaces {
		if workspace.Path == managedWorkspacePath {
			if len(workspace.TopologyRoutes) != 1 {
				t.Fatalf("managed workspace route count=%d routes=%+v", len(workspace.TopologyRoutes), workspace.TopologyRoutes)
			}
			route = workspace.TopologyRoutes[0]
		}
	}
	if route.RouteID == "" {
		t.Fatalf("managed workspace route not found in response=%+v", response)
	}
	if route.RuntimeSwarmID != "child-swarm-1" || route.RuntimeSwarmName != "heytest" || route.RuntimeBackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("runtime route fields = %+v", route)
	}
	if route.HostSwarmID != "managed-swarm-1" || route.HostSwarmName != "managed-host" || route.RuntimeKind != "mirrored" {
		t.Fatalf("host route fields = %+v", route)
	}
	if route.HostWorkspacePath != managedWorkspacePath || route.HostWorkspaceName != "swarm-go" {
		t.Fatalf("managed workspace fields = %+v", route)
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
	if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		BindingID:                 "binding-offline",
		SourceWorkspacePath:       workspacePath,
		SourceWorkspaceName:       "workspace-one",
		DestinationRuntimeSwarmID: "offline-swarm",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "/workspaces/offline",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("upsert topology binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
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

func workspaceOverviewTopologyRouteForBindingTest(t *testing.T, server *Server, binding pebblestore.TopologyWorkspaceBindingRecord, workspacePath string) workspaceOverviewTopologyRoute {
	t.Helper()
	route, ok := server.workspaceOverviewTopologyRouteForBinding(
		binding,
		swarmTarget{SwarmID: binding.DestinationRuntimeSwarmID, Name: "managed-host", Relationship: swarmruntime.RelationshipManaged, Kind: "host", BackendURL: "https://managed.example.test", Online: true, Selectable: true},
		pebblestore.TopologyRuntimeRecord{SwarmID: binding.DestinationRuntimeSwarmID, Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test"},
		map[string]swarmTarget{"host-swarm-id": {SwarmID: "host-swarm-id", Name: "host-swarm"}},
		workspacePath,
		workspacePath,
		strings.TrimSpace(binding.SourceWorkspaceName),
		true,
	)
	if !ok {
		t.Fatalf("expected topology route for binding %+v", binding)
	}
	return route
}

func workspaceOverviewRouteForTest(t *testing.T, server *Server) workspaceOverviewTopologyRoute {
	t.Helper()
	return workspaceOverviewRouteForPathTest(t, server, "")
}

func workspaceOverviewRouteForPathTest(t *testing.T, server *Server, workspacePath string) workspaceOverviewTopologyRoute {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	var matched []workspaceOverviewTopologyRoute
	for _, workspace := range response.Workspaces {
		if strings.TrimSpace(workspacePath) != "" && workspace.Path != workspacePath {
			continue
		}
		matched = append(matched, workspace.TopologyRoutes...)
	}
	if len(matched) != 1 {
		t.Fatalf("topology route count=%d routes=%+v response=%+v", len(matched), matched, response)
	}
	return matched[0]
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
	if _, err := workspaceSvc.AddForPrincipal(testPrincipal(), workspacePath, "workspace-one", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	sessionSvc := session.NewService(pebblestore.NewSessionStore(store), nil)
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	server := NewServer(nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	server.SetSwarmMirrorStore(pebblestore.NewSwarmMirrorStore(store))
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, nil, workspaceStore))
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmName = "host-swarm"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server, workspacePath, store
}
