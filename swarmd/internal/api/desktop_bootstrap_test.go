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
	if _, err := pebblestore.UpsertTopologyWorkspaceBinding(pebblestore.NewTopologyStore(store), pebblestore.TopologyWorkspaceBindingRecord{
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
	if len(response.Workspaces[0].ReplicationLinks) != 0 {
		t.Fatalf("test must prove topology routes do not come from legacy replication links: %+v", response.Workspaces[0].ReplicationLinks)
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
