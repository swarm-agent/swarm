package permission

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: approvedArgumentsForResolution is the narrow common permission
// boundary preventing a V2 document from becoming approved one-shot arguments,
// even when a client attempts to replace the canonical pending document.
func TestAutomationV2GenericApprovalDenied(t *testing.T) {
	for _, tool := range []string{"plan_manage", "exit_plan_mode"} {
		record := store.PermissionRecord{ToolName: tool, ToolArguments: `{"action":"request_new_plan","document":{"automation_v2":{"schema_version":2}}}`}
		for _, action := range []string{ActionAllowOnce, ActionAllowAlways} {
			for _, overlay := range []string{"", `{"document":{}}`} {
				args, err := approvedArgumentsForResolution(record, action, overlay)
				if err == nil || args != "" { t.Fatalf("recurring proposal released: %q %v", args, err) }
			}
		}
		if _, err := approvedArgumentsForResolution(record, ActionCancel, ""); err != nil { t.Fatal("cancellation blocked", err) }
	}
}
