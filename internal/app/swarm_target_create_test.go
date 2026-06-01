package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestCreateSessionSwarmIDForRouteUsesOverviewTargetForHost(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "host"}, &model.SwarmTarget{SwarmID: " host-swarm "})
	if got != "host-swarm" {
		t.Fatalf("swarm id = %q, want host-swarm", got)
	}
}

func TestCreateSessionSwarmIDForRouteKeepsTopologyRouteSwarmID(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "swarm:child", SwarmID: " child-swarm "}, &model.SwarmTarget{SwarmID: "host-swarm"})
	if got != "child-swarm" {
		t.Fatalf("swarm id = %q, want child-swarm", got)
	}
}

func TestCreateSessionSwarmIDForRouteDoesNotUseWorkspaceID(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "host", WorkspaceBindingID: "workspace-id"}, &model.SwarmTarget{SwarmID: "host-swarm"})
	if got != "host-swarm" {
		t.Fatalf("swarm id = %q, want host-swarm", got)
	}
	if got == "workspace-id" {
		t.Fatalf("swarm id used workspace id")
	}
}
