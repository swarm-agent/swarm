package run

import (
	"reflect"
	"testing"
)

// Purpose: planDocumentFromArgsForTool and execution cloning must preserve the
// explicit automation contract, not turn recurring prose into authority. This
// unit layer isolates serialization loss before canonical lifecycle validation.
func TestPlanAutomationIntentFidelity(t *testing.T) {
	args := map[string]any{"document": map[string]any{
		"title": "Hourly review", "info": map[string]any{"goal": "Preserve full instructions"},
		"automation": map[string]any{
			"scope":         map[string]any{"account_id": "account", "workspace_id": "workspace"},
			"automation_id": "automation", "definition_revision": 7, "existing": true,
			"definition": map[string]any{"name": "review", "session_id": "parent", "schedule": map[string]any{"kind": "cron", "expression": "0 18 * * *", "timezone": "UTC", "missed_policy": "skip", "overlap_policy": "serialize"}, "authorization": map[string]any{"mode": "approval_required", "expires_at": 200000}},
		},
	}}
	for _, name := range []string{"exit_plan_mode", "plan_manage"} {
		doc, err := planDocumentFromArgsForTool(args, name)
		if err != nil {
			t.Fatal(err)
		}
		clone, err := clonePlanDocumentForExecutionAction(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(doc, clone) || clone.Automation.DefinitionRevision != 7 || clone.Automation.Definition.Schedule.Expression != "0 18 * * *" {
			t.Fatal("intent lost")
		}
		clone.Automation.Definition.Schedule.Expression = "changed"
		if doc.Automation.Definition.Schedule.Expression != "0 18 * * *" {
			t.Fatal("clone aliases source")
		}
	}
	doc, err := planDocumentFromArgs(map[string]any{"document": map[string]any{"title": "Hourly review"}})
	if err != nil || doc.Automation != nil {
		t.Fatal("prose became automation intent", err)
	}
}
