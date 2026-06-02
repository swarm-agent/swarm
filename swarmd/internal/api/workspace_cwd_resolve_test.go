package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestWorkspaceCWDResolveNonWorkspaceReturnsTUIPrimaryRoute(t *testing.T) {
	server, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		CurrentGroupID: "group-1",
		Groups: []swarmruntime.GroupState{{
			Group:   swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost}},
		}},
	})
	cwd := t.TempDir()

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/cwd/resolve?cwd="+url.QueryEscape(cwd), nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceCWDResolveResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode cwd resolve: %v", err)
	}
	if response.ResolutionKind != workspaceCWDResolutionKindNonWorkspace {
		t.Fatalf("resolution kind = %q, want non_workspace", response.ResolutionKind)
	}
	if response.PrimarySwarmTarget == nil || response.PrimarySwarmTarget.Name != "host-swarm" {
		t.Fatalf("primary target = %+v, want host-swarm", response.PrimarySwarmTarget)
	}
	if len(response.Routes) != 1 {
		t.Fatalf("route count=%d routes=%+v", len(response.Routes), response.Routes)
	}
	route := response.Routes[0]
	if route.WorkspaceBindingID != "" || !route.TUIPrimaryCWD || route.RouteSource != workspaceCWDRouteSourcePrimaryCWD {
		t.Fatalf("non-workspace primary route = %+v", route)
	}
	if route.RuntimeSwarmName != "host-swarm" || route.RuntimeSwarmID == "" {
		t.Fatalf("primary route target fields = %+v", route)
	}
}

func TestWorkspaceCWDResolveKnownWorkspaceUsesTopologyBindingsIgnoringSelectedTargetAndPagination(t *testing.T) {
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	workspacePath := filepath.Join(t.TempDir(), "workspace-two")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(testPrincipal(), workspacePath, "workspace-two", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
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
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "managed-swarm-1",
		Name:           "Managed Desk",
		Relationship:   "managed",
		BackendURL:     "https://managed.example.test",
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "managed-swarm-1", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-swarm-1", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-managed-two",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-two",
		DestinationRuntimeSwarmID:       "managed-swarm-1",
		DestinationAuthorityHostSwarmID: "managed-swarm-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "managed-swarm-1",
		DestinationWorkspacePath:        "/workspaces/workspace-two",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "managed-swarm-1",
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		Writable:                        true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/cwd/resolve?cwd="+url.QueryEscape(workspacePath)+"&limit=1&swarm_id=managed-swarm-1", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceCWDResolveResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode cwd resolve: %v", err)
	}
	if response.ResolutionKind != workspaceCWDResolutionKindWorkspace || response.Workspace == nil || response.Workspace.WorkspaceID != workspaceEntry.WorkspaceID {
		t.Fatalf("workspace resolution = %+v", response)
	}
	if response.PrimarySwarmTarget == nil || response.PrimarySwarmTarget.SwarmID != "host-swarm-id" {
		t.Fatalf("primary target = %+v, want host-swarm-id even with selected managed target", response.PrimarySwarmTarget)
	}
	if len(response.Routes) != 1 {
		t.Fatalf("route count=%d routes=%+v", len(response.Routes), response.Routes)
	}
	route := response.Routes[0]
	if route.WorkspaceBindingID != "binding-managed-two" || route.RuntimeSwarmID != "managed-swarm-1" || route.RuntimeSwarmName != "managed-host" {
		t.Fatalf("topology route = %+v", route)
	}
	if route.TUIPrimaryCWD || route.RouteSource != workspaceOverviewTopologyRouteSource {
		t.Fatalf("route source/marker = %+v", route)
	}
}

func TestWorkspaceCWDResolveKnownWorkspaceIncludesPrimarySelfBindingRoute(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	setReplicateFakeSwarmState(server, swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "Primary Desk", Role: "master"},
		CurrentGroupID: "group-1",
		Groups: []swarmruntime.GroupState{{
			Group:   swarmruntime.Group{ID: "group-1", Name: "Primary Group", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "Primary Desk", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost}},
		}},
	})
	topologyStore := pebblestore.NewTopologyStore(store)
	workspaceEntry, ok, err := pebblestore.NewWorkspaceStore(store).GetForAccount(testPrincipal().AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%t err=%v", ok, err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "host-swarm-id",
		Name:           "Primary Desk",
		Relationship:   "self",
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert topology runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "host-swarm-id", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "host-swarm-id", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-primary-one",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-one",
		DestinationRuntimeSwarmID:       "host-swarm-id",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationWorkspacePath:        workspacePath,
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "host-swarm-id",
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		Writable:                        true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/cwd/resolve?cwd="+url.QueryEscape(workspacePath)+"&swarm_id=managed-swarm-1", nil))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceCWDResolveResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode cwd resolve: %v", err)
	}
	if response.PrimarySwarmTarget == nil || response.PrimarySwarmTarget.Name != "Primary Desk" {
		t.Fatalf("primary target = %+v, want Primary Desk", response.PrimarySwarmTarget)
	}
	if len(response.Routes) == 0 {
		t.Fatalf("routes empty: %+v", response)
	}
	route := response.Routes[0]
	if route.WorkspaceBindingID != "binding-primary-one" || route.RuntimeSwarmID != "host-swarm-id" || route.RuntimeSwarmName != "Primary Desk" {
		t.Fatalf("primary route = %+v, want primary self binding first", route)
	}
	if route.TUIPrimaryCWD || route.RouteSource != workspaceOverviewTopologyRouteSource {
		t.Fatalf("primary route source/marker = %+v", route)
	}
}
