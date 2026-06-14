package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/tool"
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
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "host-swarm-id", Name: "host-swarm", Relationship: "self", BackendURL: "https://host.example.test", ObservedSources: []string{pebblestore.TopologyRuntimeSourceDeployContainer}}); err != nil {
		t.Fatalf("upsert host topology runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "managed-swarm-1",
		Name:                 "managed-host",
		Relationship:         "managed",
		BackendURL:           "https://managed.example.test",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "container-1",
		ObservedSources:      []string{pebblestore.TopologyRuntimeSourceDeployContainer},
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "managed-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "host-swarm-id", AuthorityContainerID: "container-1", RuntimeKind: pebblestore.TopologyRuntimeKindContainer, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		BindingID:                       "binding-1",
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-one",
		DestinationRuntimeSwarmID:       "managed-swarm-1",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationContainerID:          "container-1",
		DestinationWorkspacePath:        "/workspaces/workspace-one",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm-id",
		ReplicationMode:                 "continuous",
		Writable:                        true,
		Sync:                            pebblestore.WorkspaceReplicationSync{Enabled: true, Mode: "bidirectional"},
		CreatedAt:                       11,
		UpdatedAt:                       22,
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

func TestWorkspaceOverviewSessionStatusUsesCanonicalRunStateOnly(t *testing.T) {
	server, workspacePath, _ := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
	})
	canonical := createWorkspaceOverviewSessionForTest(t, server, "overview-canonical", workspacePath)
	lifecycleOnly := createWorkspaceOverviewSessionForTest(t, server, "overview-lifecycle-only", workspacePath)
	recordWorkspaceOverviewRunIntentForTest(t, server, canonical.ID, "run-canonical", session.RunIntentRunning, 2000)
	recordWorkspaceOverviewLifecycleForTest(t, server, canonical.ID, "run-canonical", false)
	recordWorkspaceOverviewLifecycleForTest(t, server, lifecycleOnly.ID, "run-lifecycle-only", true)

	response := getWorkspaceOverviewForTest(t, server)
	var canonicalSession *workspaceOverviewSession
	var lifecycleSession *workspaceOverviewSession
	for i := range response.Workspaces[0].Sessions {
		session := &response.Workspaces[0].Sessions[i]
		switch session.ID {
		case canonical.ID:
			canonicalSession = session
		case lifecycleOnly.ID:
			lifecycleSession = session
		}
	}
	if canonicalSession == nil || canonicalSession.ActiveRun == nil || canonicalSession.ActiveRun.RunID != "run-canonical" || canonicalSession.SessionStatus != "running" {
		t.Fatalf("canonical overview session = %+v", canonicalSession)
	}
	if lifecycleSession == nil {
		t.Fatalf("lifecycle-only session missing from overview: %+v", response.Workspaces[0].Sessions)
	}
	if lifecycleSession.ActiveRun != nil || lifecycleSession.SessionStatus != "idle" {
		t.Fatalf("lifecycle-only overview liveness = active_run %+v status %q", lifecycleSession.ActiveRun, lifecycleSession.SessionStatus)
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
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "managed-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "host-swarm-id", AuthorityContainerID: "container-1", RuntimeKind: pebblestore.TopologyRuntimeKindContainer, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	binding := pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		BindingID:                       "binding-stable",
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-one",
		DestinationRuntimeSwarmID:       "managed-swarm-1",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationContainerID:          "container-1",
		DestinationWorkspacePath:        "/workspaces/workspace-one",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "host-swarm-id",
		ReplicationMode:                 "continuous",
		Writable:                        true,
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("put initial topology binding: %v", err)
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
	binding.DestinationWorkspacePath = "/workspaces/workspace-one-renamed-materialization"
	binding.BindingGeneration++
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("put changed topology binding: %v", err)
	}
	changedRoute := workspaceOverviewTopologyRouteForBindingTest(t, server, binding, workspacePath)
	if changedRoute.RouteID != initialRoute.RouteID {
		t.Fatalf("route id changed with runtime path: got %q want %q", changedRoute.RouteID, initialRoute.RouteID)
	}
	if changedRoute.RuntimeWorkspacePath != "/workspaces/workspace-one-renamed-materialization" || changedRoute.HostWorkspacePath != workspacePath {
		t.Fatalf("changed paths host=%q runtime=%q", changedRoute.HostWorkspacePath, changedRoute.RuntimeWorkspacePath)
	}
}

func TestWorkspaceOverviewTopologyRouteIDRequiresBindingID(t *testing.T) {
	if got := workspaceOverviewTopologyRouteID("managed-swarm-1", "binding-one"); got != "swarm:managed-swarm-1:binding:binding-one" {
		t.Fatalf("route id=%q", got)
	}
	if got := workspaceOverviewTopologyRouteID("managed-swarm-1", ""); got != "" {
		t.Fatalf("name/path fallback route id=%q, want empty", got)
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
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "live-swarm", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "live-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put live runtime placement: %v", err)
	}
	binding := pebblestore.TopologyWorkspaceBindingRecord{BindingID: "binding-live", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SourceWorkspaceID: workspaceEntry.WorkspaceID, SourceWorkspaceGeneration: workspaceEntry.WorkspaceGeneration, SourceWorkspacePath: workspacePath, SourceWorkspaceName: "workspace-one", DestinationRuntimeSwarmID: "live-swarm", DestinationAuthorityHostSwarmID: "live-swarm", DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationHostSwarmID: "live-swarm", DestinationWorkspacePath: "/workspaces/live", PlacementGeneration: 1, BindingGeneration: 1, State: pebblestore.TopologyWorkspaceBindingStateBound, AttestedByHostSwarmID: "live-swarm", Writable: true}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("put binding %q: %v", binding.BindingID, err)
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
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
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
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, managedWorkspacePath)
	if err != nil || !ok {
		t.Fatalf("get managed workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "managed-host", Relationship: "managed", BackendURL: "https://managed.example.test"}); err != nil {
		t.Fatalf("upsert host runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "heytest", Relationship: "child", BackendURL: "http://127.0.0.1:7782", OwnerHostSwarmID: "managed-swarm-1", OwnerHostContainerID: "managed-swarm-1:container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "child-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-swarm-1", AuthorityContainerID: "managed-swarm-1:container-1", RuntimeKind: pebblestore.TopologyRuntimeKindContainer, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put child runtime placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-managed-child",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             managedWorkspacePath,
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       "child-swarm-1",
		DestinationAuthorityHostSwarmID: "managed-swarm-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationHostSwarmID:          "managed-swarm-1",
		DestinationContainerID:          "managed-swarm-1:container-1",
		DestinationWorkspacePath:        "/workspaces/swarm-go",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "managed-swarm-1",
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		Writable:                        true,
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
	if route.RuntimeSwarmID != "child-swarm-1" || route.RuntimeSwarmName != "heytest" {
		t.Fatalf("runtime route fields = %+v", route)
	}
	if route.AuthorityHostSwarmID != "managed-swarm-1" || route.HostSwarmID != "managed-swarm-1" || route.HostSwarmName != "managed-host" || route.RuntimeKind != pebblestore.TopologyRuntimeKindContainer {
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
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "offline-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Offline", Relationship: "managed"}); err != nil {
		t.Fatalf("upsert offline runtime: %v", err)
	}
	if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		BindingID:                       "binding-offline",
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-one",
		DestinationRuntimeSwarmID:       "offline-swarm",
		DestinationAuthorityHostSwarmID: "offline-swarm",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "offline-swarm",
		DestinationWorkspacePath:        "/workspaces/offline",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "offline-swarm",
		Writable:                        true,
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
	server := NewServer(nil, nil, nil, workspaceOverviewNoopRunService{}, sessionSvc, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
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

func createWorkspaceOverviewSessionForTest(t *testing.T, server *Server, sessionID, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	created, err := server.sessions.ApplySessionMutation(session.SessionMutationInput{
		SessionID:      sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "create-" + sessionID,
		PayloadHash:    "hash-create-" + sessionID,
		Kind:           session.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			WorkspacePath:  workspacePath,
			WorkspaceName:  "workspace-one",
			Title:          sessionID,
			CreatedAt:      1000,
			UpdatedAt:      1000,
		},
		NowUnixMs: 1000,
	})
	if err != nil || created.Session == nil {
		t.Fatalf("create overview session %s: result=%+v err=%v", sessionID, created, err)
	}
	return *created.Session
}

func recordWorkspaceOverviewRunIntentForTest(t *testing.T, server *Server, sessionID, runID, status string, now int64) {
	t.Helper()
	if status != session.RunIntentPendingExecutor {
		recordWorkspaceOverviewRunIntentForTest(t, server, sessionID, runID, session.RunIntentPendingExecutor, now-1)
	}
	if _, err := server.applySessionV3PrimaryMutation(session.SessionMutationInput{
		SessionID:      sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "run-" + sessionID + "-" + status,
		PayloadHash:    "hash-run-" + sessionID + "-" + status,
		Kind:           session.SessionMutationRecordRunIntent,
		RunIntent:      &pebblestore.V3SessionRunIntent{RunID: runID, Status: status},
		NowUnixMs:      now,
	}); err != nil {
		t.Fatalf("record overview run intent %s/%s: %v", runID, status, err)
	}
}

func recordWorkspaceOverviewLifecycleForTest(t *testing.T, server *Server, sessionID, runID string, active bool) {
	t.Helper()
	if _, err := server.applySessionV3PrimaryMutation(session.SessionMutationInput{
		SessionID:      sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "lifecycle-" + sessionID,
		PayloadHash:    "hash-lifecycle-" + sessionID,
		Kind:           session.SessionMutationUpsertLifecycle,
		EventType:      "session.lifecycle.updated",
		Lifecycle:      &pebblestore.SessionLifecycleSnapshot{RunID: runID, Active: active, Phase: "running", UpdatedAt: 3000},
		NowUnixMs:      3000,
	}); err != nil {
		t.Fatalf("record overview lifecycle %s: %v", sessionID, err)
	}
}

func getWorkspaceOverviewForTest(t *testing.T, server *Server) workspaceOverviewResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?limit=25&discover_limit=1", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) == 0 {
		t.Fatalf("overview has no workspaces: %+v", response)
	}
	return response
}

type workspaceOverviewNoopRunService struct{}

func (workspaceOverviewNoopRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (workspaceOverviewNoopRunService) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (workspaceOverviewNoopRunService) StopSessionRun(string, string, string) error { return nil }

func (workspaceOverviewNoopRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (workspaceOverviewNoopRunService) ListAgentToolDefinitions() []tool.Definition { return nil }

func (workspaceOverviewNoopRunService) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (workspaceOverviewNoopRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (workspaceOverviewNoopRunService) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
