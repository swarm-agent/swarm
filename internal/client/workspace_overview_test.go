package client

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceOverviewDecodesSwarmTarget(t *testing.T) {
	var overview WorkspaceOverviewResponse
	if err := json.Unmarshal([]byte(`{
		"ok": true,
		"workspaces": [
			{
				"path": "/repo",
				"workspace_id": "workspace-1",
				"workspace_generation": 2,
				"local_workspace_binding_id": "binding-local",
				"workspace_name": "repo",
				"directories": ["/repo"]
			}
		],
		"directories": [],
		"swarm_target": {
			"swarm_id": "target-swarm",
			"name": "Target Swarm",
			"role": "master",
			"relationship": "self",
			"kind": "local",
			"deployment_id": "deploy-1",
			"attach_status": "attached",
			"host_swarm_id": "host-swarm",
			"online": true,
			"selectable": true,
			"current": true,
			"backend_url": "http://127.0.0.1:7781",
			"desktop_url": "http://127.0.0.1:7780",
			"last_error": ""
		}
	}`), &overview); err != nil {
		t.Fatalf("decode workspace overview: %v", err)
	}
	if len(overview.Workspaces) != 1 {
		t.Fatalf("workspace count = %d, want 1", len(overview.Workspaces))
	}
	if overview.Workspaces[0].LocalWorkspaceBindingID != "binding-local" {
		t.Fatalf("LocalWorkspaceBindingID = %q, want binding-local", overview.Workspaces[0].LocalWorkspaceBindingID)
	}
	if overview.Workspaces[0].WorkspaceID != "workspace-1" || overview.Workspaces[0].WorkspaceGeneration != 2 {
		t.Fatalf("workspace identity = %q/%d, want workspace-1/2", overview.Workspaces[0].WorkspaceID, overview.Workspaces[0].WorkspaceGeneration)
	}
	if overview.SwarmTarget == nil {
		t.Fatalf("SwarmTarget is nil")
	}
	if overview.SwarmTarget.SwarmID != "target-swarm" {
		t.Fatalf("SwarmTarget.SwarmID = %q, want target-swarm", overview.SwarmTarget.SwarmID)
	}
	if overview.SwarmTarget.HostSwarmID != "host-swarm" {
		t.Fatalf("SwarmTarget.HostSwarmID = %q, want host-swarm", overview.SwarmTarget.HostSwarmID)
	}
	if !overview.SwarmTarget.Online || !overview.SwarmTarget.Selectable || !overview.SwarmTarget.Current {
		t.Fatalf("SwarmTarget booleans not decoded: %+v", overview.SwarmTarget)
	}
}
