package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestScopedSessionTabsForPathMergesPrimaryAndLocalContainerBySourceWorkspace(t *testing.T) {
	sourcePath := "/home/installer/swarm-go"
	runtimePath := "/workspaces/swarm-go"

	tabs := scopedSessionTabsForPath(sourcePath, []model.SessionSummary{
		{
			ID:            "session-primary",
			Title:         "Primary session",
			WorkspacePath: sourcePath,
		},
		{
			ID:            "session-container",
			Title:         "Container session",
			WorkspacePath: runtimePath,
			SessionExecution: &client.SessionExecutionV2{
				ExecutionClass:       "local_container",
				SourceWorkspacePath:  sourcePath,
				RuntimeWorkspacePath: runtimePath,
				WorkspaceBindingID:   "binding-container-v2",
				RuntimeSwarmID:       "container-swarm",
				RuntimeKind:          "container",
				AuthorityHostSwarmID: "host-swarm",
				AuthorityContainerID: "container-1",
				SourceWorkspaceID:    "source-workspace-id",
				BindingGeneration:    1,
				PlacementGeneration:  1,
			},
		},
	}, nil)

	got := map[string]string{}
	for _, tab := range tabs {
		got[tab.ID] = tab.WorkspacePath
	}
	if got["session-primary"] != sourcePath {
		t.Fatalf("primary session path = %q, want %q; tabs=%#v", got["session-primary"], sourcePath, tabs)
	}
	if got["session-container"] != sourcePath {
		t.Fatalf("container session path = %q, want source path %q; tabs=%#v", got["session-container"], sourcePath, tabs)
	}
}

func TestModelSessionSummaryFromClientUsesSourceWorkspacePathForLocalContainer(t *testing.T) {
	sourcePath := "/home/installer/swarm-go"
	runtimePath := "/workspaces/swarm-go"

	got := modelSessionSummaryFromClient(client.SessionSummary{
		ID:            "session-container",
		Title:         "Container session",
		WorkspacePath: runtimePath,
		SessionExecution: &client.SessionExecutionV2{
			ExecutionClass:       "local_container",
			SourceWorkspacePath:  sourcePath,
			RuntimeWorkspacePath: runtimePath,
		},
	})

	if got.WorkspacePath != sourcePath {
		t.Fatalf("workspace path = %q, want source path %q", got.WorkspacePath, sourcePath)
	}
	if got.SessionExecution == nil || got.SessionExecution.RuntimeWorkspacePath != runtimePath {
		t.Fatalf("runtime workspace path not preserved in execution: %#v", got.SessionExecution)
	}
}

func TestMergeHomeSessionSummaryKeepsSourceWorkspacePathForLocalContainer(t *testing.T) {
	sourcePath := "/home/installer/swarm-go"
	runtimePath := "/workspaces/swarm-go"
	execution := &client.SessionExecutionV2{
		ExecutionClass:       "local_container",
		SourceWorkspacePath:  sourcePath,
		RuntimeWorkspacePath: runtimePath,
	}

	got := mergeHomeSessionSummary(model.SessionSummary{
		ID:               "session-container",
		WorkspacePath:    sourcePath,
		SessionExecution: execution,
	}, model.SessionSummary{
		ID:               "session-container",
		WorkspacePath:    runtimePath,
		SessionExecution: execution,
	})

	if got.WorkspacePath != sourcePath {
		t.Fatalf("merged workspace path = %q, want source path %q", got.WorkspacePath, sourcePath)
	}
}
