package permission

import "testing"

// Purpose: buildPolicyEvalContext must separate automation read, mutation, run
// and cancel identities so a read-tool grant cannot match a dangerous action.
// This is the narrowest layer that owns normalized policy-rule identity.
func TestAutomationPolicyIsolation(t *testing.T) {
	for action, want := range map[string]string{"list": "automation_read", "history": "automation_read", "context": "automation_read", "save": "automation_change", "update_context": "automation_change", "run": "automation_run", "cancel": "automation_cancel", "forged": "automation_change"} {
		got := buildPolicyEvalContext("manage_automation", `{"action":"`+action+`"}`)
		if got.ToolName != want {
			t.Fatal(action, got.ToolName, want)
		}
	}
	if automationPolicyIdentity(`{`) == "automation_read" {
		t.Fatal("malformed input granted read identity")
	}
}
