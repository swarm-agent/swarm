package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestApplyHomeWorkspaceBootstrapSelectsRegisteredLaunchCWD(t *testing.T) {
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:    client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/default", WorkspaceName: "Default"},
			{Path: "/launch", WorkspaceName: "Launch"},
		},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: "/launch", Workspace: &client.WorkspaceResolution{WorkspacePath: "/launch", ResolvedPath: "/launch"}},
	}, "/launch")

	if selected != "/launch" {
		t.Fatalf("selected path = %q, want /launch", selected)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	if len(next.Workspaces) != 2 || next.Workspaces[0].Active || !next.Workspaces[1].Active {
		t.Fatalf("workspace selection = %#v", next.Workspaces)
	}
}

func TestApplyHomeWorkspaceBootstrapGuidesUnregisteredLaunchCWD(t *testing.T) {
	_, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:         client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent:      true,
		workspaces:      []client.WorkspaceEntry{{Path: "/default", WorkspaceName: "Default"}},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: "/other"},
	}, "/other")

	if selected != "/default" {
		t.Fatalf("selected path = %q, want /default", selected)
	}
	if len(warnings) != 1 || warnings[0] != "launch directory is not registered; use /workspace to add it" {
		t.Fatalf("warnings = %#v", warnings)
	}
}
