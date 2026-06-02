package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestCreateSessionSwarmIDForRouteDoesNotFallbackToOverviewTarget(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "host"}, &model.SwarmTarget{SwarmID: " host-swarm "})
	if got != "" {
		t.Fatalf("swarm id = %q, want no late target fallback", got)
	}
}

func TestCreateSessionSwarmIDForRouteKeepsHydratedRouteSwarmID(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "swarm:child", SwarmID: " child-swarm "}, &model.SwarmTarget{SwarmID: "host-swarm"})
	if got != "child-swarm" {
		t.Fatalf("swarm id = %q, want child-swarm", got)
	}
}

func TestCreateSessionSwarmIDForRouteDoesNotUseWorkspaceID(t *testing.T) {
	got := createSessionSwarmIDForRoute(model.ChatRoute{ID: "host", WorkspaceBindingID: "workspace-id"}, &model.SwarmTarget{SwarmID: "host-swarm"})
	if got != "" {
		t.Fatalf("swarm id = %q, want no target/workspace fallback", got)
	}
}
