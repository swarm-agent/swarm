package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestOpenChatSessionHostRouteUsesOverviewSwarmTarget(t *testing.T) {
	route := model.ChatRoute{ID: "host", Label: "host", HostWorkspacePath: "/repo", RuntimeWorkspacePath: "/repo"}
	got := createSessionSwarmIDForRoute(route, &model.SwarmTarget{SwarmID: "host-swarm", Name: "Host Swarm", Relationship: "self", Kind: "local", Current: true})
	if got != "host-swarm" {
		t.Fatalf("swarm id = %q, want host-swarm from overview swarm_target", got)
	}
}

func TestBuildChatRoutesForHomeModelTreatsSelfKindAsPrimaryHostTarget(t *testing.T) {
	routes := buildChatRoutesForHomeModel(model.HomeModel{
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "host-swarm", Name: "Host Swarm", Relationship: "self", Kind: "self", Current: true},
		Workspaces: []model.Workspace{{
			Path:                    testWorkspacePath,
			LocalWorkspaceBindingID: "local-binding",
		}},
	}, testWorkspacePath)

	if len(routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(routes))
	}
	route := routes[0]
	if route.ID != "swarm:host-swarm:binding:local-binding" || route.SwarmID != "host-swarm" || route.WorkspaceBindingID != "local-binding" {
		t.Fatalf("primary self route did not preserve v2 shape: %+v", route)
	}
}

func TestOpenChatSessionRemoteRouteKeepsTopologyRouteSwarmID(t *testing.T) {
	routes := buildChatRoutesForWorkspaces(testRoutingWorkspaces(), testWorkspacePath)
	selected := ""
	for _, route := range routes {
		if route.ID == testRemoteRouteID {
			selected = createSessionSwarmIDForRoute(route, &model.SwarmTarget{SwarmID: "host-swarm"})
			break
		}
	}
	if selected != "child-swarm" {
		t.Fatalf("swarm id = %q, want child-swarm from topology route", selected)
	}
}
