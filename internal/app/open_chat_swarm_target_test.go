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
