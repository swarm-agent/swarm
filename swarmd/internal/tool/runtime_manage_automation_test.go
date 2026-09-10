package tool

import (
	"context"
	"testing"
)

// Purpose: the runtime decoder, rather than provider schema compliance, rejects
// forged authority and unbounded input before accessing services. This unit layer
// isolates dispatch ingress and does not claim execution approval integration.
func TestAutomationToolIngress(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "list", "role": "user"},
		{"action": "list", "account_id": "foreign"},
		{"action": "list", "limit": 51},
		{"action": "history", "before": -1},
		{"action": "update_context", "user_instructions": map[string]string{"grant": "all"}},
	} {
		if _, err := decodeAutomationToolRequest(args); err == nil { t.Fatal("accepted forged/unbounded request", args) }
	}
	if _, err := (&Runtime{}).executeManageAutomation(context.Background(), WorkspaceScope{}, map[string]any{"action": "run"}); err == nil { t.Fatal("missing adapter reported success") }
	d := manageAutomationDefinition()
	if d.Name != "manage_automation" || d.Parameters["additionalProperties"] != false { t.Fatal("schema lost strict envelope") }
	properties := d.Parameters["properties"].(map[string]any)
	for _, forbidden := range []string{"role", "principal", "account_id", "approval_reference", "user_instructions"} {
		if _, ok := properties[forbidden]; ok { t.Fatal("schema exposes authority", forbidden) }
	}
}
