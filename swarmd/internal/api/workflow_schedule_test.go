package api

import (
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/tool"
	"testing"
	"time"
)

// Purpose: the shared read model and tool schema must expose schedules without
// requiring event or nonempty steps. These pure adapter boundaries are the
// narrowest layer proving both MCP/native schema reuse and next-run visibility.
func TestWorkflowScheduleViewAndSchema(t *testing.T) {
	a := jobautomation.Automation{Enabled: true, On: &jobautomation.Trigger{Schedule: &jobautomation.TriggerSchedule{Cron: "* * * * *"}}}
	view := jobAutomationView(a)
	next, ok := view["next_run_at"].(int64)
	if !ok || next <= time.Now().UnixMilli() || view["on"] != a.On {
		t.Fatalf("view=%+v", view)
	}
	a.Enabled = false
	if _, ok := jobAutomationView(a)["next_run_at"]; ok {
		t.Fatal("disabled schedule has next run")
	}
	schema := tool.WorkflowTriggerSchema()
	required := schema["required"].([]string)
	if len(required) != 1 || required[0] != "start" {
		t.Fatal(required)
	}
	props := schema["properties"].(map[string]any)
	if props["schedule"] == nil || props["event"] == nil || len(schema["oneOf"].([]any)) != 2 {
		t.Fatal(schema)
	}
	if controlMCPAutomationStepsSchema()["minItems"] != 0 {
		t.Fatal("MCP refuses trigger-only workflows")
	}
}
