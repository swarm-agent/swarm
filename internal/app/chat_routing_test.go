package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestBuildChatRoutesForWorkspacesKeepsTargetSwarmID(t *testing.T) {
	routes := buildChatRoutesForWorkspaces([]model.Workspace{{
		Name: "Host Repo",
		Path: "/host/workspace",
		ReplicationLinks: []model.WorkspaceReplicationLink{{
			TargetSwarmID:       "child-swarm",
			TargetSwarmName:     "Child Desk",
			TargetWorkspacePath: "/workspaces/swarm-go",
		}},
	}}, "/host/workspace")

	if len(routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(routes))
	}
	remote := routes[1]
	if remote.SwarmID != "child-swarm" {
		t.Fatalf("remote SwarmID = %q, want child-swarm", remote.SwarmID)
	}
	if remote.HostWorkspacePath != "/host/workspace" {
		t.Fatalf("remote HostWorkspacePath = %q, want /host/workspace", remote.HostWorkspacePath)
	}
	if remote.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("remote RuntimeWorkspacePath = %q, want /workspaces/swarm-go", remote.RuntimeWorkspacePath)
	}
}
