package tool

import (
	"strings"
	"testing"
)

// Purpose: the Orchestrator's environment actions are recognized by the
// runtime and the permission gate the same way, and the environment object is
// flattened without silently overriding a top-level field. Threat: an action
// the gate does not know (denied on linked machines) or an argument given
// twice where one copy wins unnoticed. Boundary: FolderEnvironmentAction,
// FleetEnvironmentPermissionAllowed, FleetEnvironmentChange,
// folderEnvironmentArgs. The end-to-end runtime paths (local and with
// machine) are covered with the real API server in
// api.TestManageProjectsEnvironmentActionsLocalAndLinked.
func TestFolderEnvironmentActionsAndArguments(t *testing.T) {
	for _, action := range FolderEnvironmentActions() {
		if !FolderEnvironmentAction(action) || !FleetEnvironmentPermissionAllowed("manage_projects", action) {
			t.Fatalf("%s not recognized", action)
		}
		if FleetEnvironmentPermissionAllowed("manage_workflows", action) {
			t.Fatalf("%s allowed on another tool", action)
		}
		if FleetProjectActionAllowed(action) {
			t.Fatalf("%s must not go through CallFleetProject", action)
		}
	}
	for action, change := range map[string]bool{FolderEnvironmentUpdate: true, FolderEnvironmentRebuild: true, FolderEnvironmentGet: false, FolderEnvironmentList: false} {
		if FleetEnvironmentChange("manage_projects", action) != change {
			t.Fatalf("%s change = %v", action, !change)
		}
	}
	if FolderEnvironmentAction("delete_environment") {
		t.Fatal("unknown action accepted")
	}

	sent, err := folderEnvironmentArgs(map[string]any{
		"action": "update_environment", "machine": "box", "workspace_path": "/w",
		"environment": map[string]any{"append_setup": []any{"cargo --version"}, "wait_seconds": float64(5)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["action"]; ok {
		t.Fatal("action sent")
	}
	if _, ok := sent["machine"]; ok {
		t.Fatal("machine sent")
	}
	if sent["workspace_path"] != "/w" || sent["wait_seconds"] != float64(5) || len(sent["append_setup"].([]any)) != 1 {
		t.Fatalf("flattened: %v", sent)
	}
	if _, err := folderEnvironmentArgs(map[string]any{"base_image": "a", "environment": map[string]any{"base_image": "b"}}); err == nil || !strings.Contains(err.Error(), "given twice") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := folderEnvironmentArgs(map[string]any{"environment": "debian"}); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("non-object: %v", err)
	}
}
