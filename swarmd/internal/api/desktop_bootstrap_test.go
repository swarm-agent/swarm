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
		AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "host-swarm-id", Name: "host-swarm", Relationship: "self", ObservedSources: []string{"swarm_local_node"}}); err != nil {
		t.Fatalf("upsert host topology runtime: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		SwarmID:         "managed-swarm-1",
		Name:            "managed-host",
		Relationship:    "managed",
		ObservedSources: []string{"swarm_trusted_peer"},
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "managed-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-swarm-1", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
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
		DestinationAuthorityHostSwarmID: "managed-swarm-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "managed-swarm-1",
		DestinationWorkspacePath:        "/workspaces/workspace-one",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "managed-swarm-1",
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
	if route.HostSwarmName != "managed-host" {
		t.Fatalf("host swarm name=%q route=%+v", route.HostSwarmName, route)
	}
}

// Requirement: handleWorkspaceOverview must paginate before workspaceOverviewGitStatuses/session enrichment,
// and saved-workspace callers may skip discovery. Otherwise one slow repository or home scan can stall the launcher.
// This handler-level test is the narrowest layer proving page identity, metadata, and discovery suppression together.
func TestWorkspaceOverviewPaginatesBeforeEnrichmentAndCanSkipDiscovery(t *testing.T) {
	server, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
	})
	secondWorkspacePath := filepath.Join(t.TempDir(), "workspace-two")
	if err := ensureTestWorkspaceDir(secondWorkspacePath); err != nil {
		t.Fatalf("mkdir second workspace: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(testPrincipal(), secondWorkspacePath, "workspace-two", "", false); err != nil {
		t.Fatalf("add second workspace: %v", err)
	}

	allWorkspaces, err := server.workspace.ListKnownForPrincipal(testPrincipal(), 1000)
	if err != nil {
		t.Fatalf("list expected workspaces: %v", err)
	}
	if len(allWorkspaces) != 2 {
		t.Fatalf("workspace count=%d want=2", len(allWorkspaces))
	}

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?cursor=1&limit=1&workspace_limit=1000&include_discovered=false", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(response.Workspaces) != 1 || response.Workspaces[0].Path != allWorkspaces[1].Path {
		t.Fatalf("paged workspaces=%+v want path=%q", response.Workspaces, allWorkspaces[1].Path)
	}
	if response.Cursor != 1 || response.Limit != 1 || response.TotalWorkspaces != 2 || response.HasMore || response.NextCursor != 0 {
		t.Fatalf("pagination metadata=%+v", response)
	}
	if len(response.Directories) != 0 {
		t.Fatalf("directories=%+v want discovery skipped", response.Directories)
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

// Requirement: overview route IDs follow binding identity, not materialization
// paths. Valid committed repositories and destination attestation let the API
// test reach that contract rather than failing admission first.
func TestWorkspaceOverviewTopologyRouteIDRemainsStableWhenRuntimePathChanges(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	changedWorkspacePath := filepath.Join(t.TempDir(), "workspace-one")
	if err := ensureTestWorkspaceDir(changedWorkspacePath); err != nil {
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
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "managed-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-swarm-1", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
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
		DestinationAuthorityHostSwarmID: "managed-swarm-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "managed-swarm-1",
		DestinationWorkspacePath:        "/workspaces/workspace-one",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "managed-swarm-1",
		ReplicationMode:                 "continuous",
		Writable:                        true,
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, binding); err != nil {
		t.Fatalf("put initial topology binding: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Managed", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatal(err)
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

func TestLocalWorkspaceBindingIDsByWorkspaceIDSelectsSelfAuthority(t *testing.T) {
	workspacePath := "/workspace/one"
	bindings := localWorkspaceBindingIDsByWorkspaceID(
		[]workspaceruntime.Entry{{Path: workspacePath, WorkspaceID: "workspace-1"}},
		map[string][]workspaceOverviewTopologyRoute{
			workspacePath: {
				{WorkspaceBindingID: "managed-binding", RuntimeSwarmID: "managed-swarm", AuthorityHostSwarmID: "managed-swarm"},
				{WorkspaceBindingID: "self-binding", RuntimeSwarmID: "host-swarm", AuthorityHostSwarmID: "host-swarm"},
			},
		},
		"host-swarm",
	)
	if bindings["workspace-1"] != "self-binding" {
		t.Fatalf("local binding=%q, want self-binding", bindings["workspace-1"])
	}
}

// Requirement: workspaceOverviewTopologyRoutesByWorkspace includes a live
// durable target but hides its binding once that target becomes offline.
// Exercise persisted status, not obsolete transport-presence inference.
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
				{GroupID: "group-1", SwarmID: "live-swarm", Name: "Live", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "live-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Live", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatal(err)
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
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "live-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Live", Relationship: "managed", Status: "offline"}); err != nil {
		t.Fatal(err)
	}
	stale := getWorkspaceOverviewForTest(t, server)
	if len(stale.Workspaces) != 1 || len(stale.Workspaces[0].TopologyRoutes) != 0 {
		t.Fatalf("stale target remained routable: %+v", stale.Workspaces)
	}
}

// Requirement: the overview API must not infer child liveness from an online
// owner. Persist explicit runtime status and valid placement so this negative
// assertion reaches the canonical target filter.
func TestWorkspaceOverviewSkipsOfflineChildEvenWhenOwnerHostIsSelectable(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		TrustedPeers: []swarmruntime.TrustedPeer{
			{SwarmID: "owner-swarm", Name: "Owner", Role: swarmruntime.RelationshipChild, Relationship: swarmruntime.RelationshipChild, RendezvousTransports: []swarmruntime.TransportSummary{{Kind: startupconfig.NetworkModeTailscale, Primary: "https://owner.example.test", All: []string{"https://owner.example.test"}}}},
			{SwarmID: "offline-child", Name: "Offline Child", Role: swarmruntime.RelationshipChild, Relationship: swarmruntime.RelationshipChild},
		},
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "owner-swarm", Name: "Owner", SwarmRole: swarmruntime.RelationshipChild, MembershipRole: swarmruntime.GroupMembershipRoleMember},
				{GroupID: "group-1", SwarmID: "offline-child", Name: "Offline Child", SwarmRole: swarmruntime.RelationshipChild, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "owner-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Owner", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "offline-child", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Offline Child", Relationship: "child", OwnerHostSwarmID: "owner-swarm", Status: "offline"}); err != nil {
		t.Fatalf("upsert offline child runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "offline-child", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "offline-child", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put offline child placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-offline-child",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-one",
		DestinationRuntimeSwarmID:       "offline-child",
		DestinationAuthorityHostSwarmID: "offline-child",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "offline-child",
		DestinationWorkspacePath:        "/workspaces/offline-child",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		AttestedByHostSwarmID:           "offline-child",
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		Writable:                        true,
	}); err != nil {
		t.Fatalf("put offline child binding: %v", err)
	}

	response := getWorkspaceOverviewForTest(t, server)
	var matched bool
	for _, workspace := range response.Workspaces {
		if workspace.Path != workspacePath {
			continue
		}
		matched = true
		if len(workspace.TopologyRoutes) != 0 {
			t.Fatalf("offline child route must remain hidden even when owner is selectable: %+v", workspace.TopologyRoutes)
		}
	}
	if !matched {
		t.Fatalf("workspace %q missing from response", workspacePath)
	}
}

// Requirement: overview excludes explicitly offline durable runtimes.
// A valid placement isolates liveness rejection at the handler layer.
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
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "offline-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "Offline", Relationship: "managed", Status: "offline"}); err != nil {
		t.Fatalf("upsert offline runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "offline-swarm", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "offline-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put offline runtime placement: %v", err)
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
		swarmTarget{SwarmID: binding.DestinationRuntimeSwarmID, Name: "managed-host", Relationship: swarmruntime.RelationshipManaged, Kind: "host", Online: true, Selectable: true},
		pebblestore.TopologyRuntimeRecord{SwarmID: binding.DestinationRuntimeSwarmID, Name: "managed-host", Relationship: "managed"},
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
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatalf("create committed workspace: %v", err)
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
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil))
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

// Requirement: handleWorkspaceOverview's explicit catalog mode must not depend
// on session/permission/todo services or execute Git. The handler layer proves
// response identity and compatibility defaults while unusable services and a
// recording fake Git executable make accidental enrichment observable.
func TestWorkspaceOverviewCatalogSkipsDetails(t *testing.T) {
	server, workspacePath, _ := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "host"},
	})
	server.sessions = nil
	fakeBin := t.TempDir()
	marker := filepath.Join(fakeBin, "git-called")
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\n: > \"$GIT_CALLED_MARKER\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	t.Setenv("GIT_CALLED_MARKER", marker)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?include_details=false&include_discovered=false&limit=1", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response workspaceOverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DetailsIncluded || len(response.Workspaces) != 1 || response.Workspaces[0].Path != workspacePath || response.Workspaces[0].WorkspaceID == "" {
		t.Fatalf("invalid catalog: %+v", response)
	}
	if response.CurrentWorkspace == nil || response.CurrentWorkspace.ResolvedPath != workspacePath || len(response.Workspaces[0].Sessions) != 0 || len(response.Directories) != 0 {
		t.Fatalf("invalid catalog current/details: %+v", response)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("catalog executed Git: %v", err)
	}

	// Omitted option retains the full response contract and its service errors.
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/overview?include_discovered=false", nil)))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "session service") {
		t.Fatalf("default swallowed missing details service: %d %s", rec.Code, rec.Body.String())
	}
	current, ok, err := server.workspace.CurrentBindingForPrincipal(testPrincipal())
	if err != nil || !ok || current.ResolvedPath != workspacePath {
		t.Fatalf("read changed selection: %+v %v", current, err)
	}
}
