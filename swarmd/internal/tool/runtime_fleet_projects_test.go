package tool

import "testing"

// Purpose: FleetProjectActionAllowed is the shared runtime/permission allowlist.
// This unit test prevents remote merge, deletion, waiting or future local-only
// actions from becoming remotely callable by default.
func TestFleetProjectActionAllowlist(t *testing.T) {
	for _, action := range []string{"list", "get", "list_tasks", "get_task", "propose_task", "create_task", "approve_task", "accept_task", "reopen_task", "refine_task", "archive_task"} {
		if !FleetProjectActionAllowed(action) {
			t.Fatalf("missing action %s", action)
		}
	}
	for _, action := range []string{"report_task", "integrate_task", "deploy_task", "delete", "delete_task", "wait_tasks", "create", "update", "unknown"} {
		if FleetProjectActionAllowed(action) {
			t.Fatalf("unsafe action %s", action)
		}
	}
}
