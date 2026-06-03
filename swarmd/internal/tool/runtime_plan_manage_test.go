package tool

import (
	"strings"
	"testing"
)

func TestPlanManageDefinitionIncludesExplicitNewOverride(t *testing.T) {
	definition := mustFindDefinition(t, "plan_manage")
	if definition.Description == "" {
		t.Fatal("plan_manage description is empty")
	}
	if !containsAll(definition.Description, "new", "active plan", "override=true") {
		t.Fatalf("description %q does not make override requirement obvious", definition.Description)
	}
	if !containsAll(definition.Description, "patch", "update_section", "targeted partial edits") {
		t.Fatalf("description %q does not advertise partial plan updates", definition.Description)
	}

	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", definition.Parameters["properties"])
	}
	override, ok := params["override"].(map[string]any)
	if !ok {
		t.Fatalf("override property missing or wrong type: %T", params["override"])
	}
	if override["type"] != "boolean" {
		t.Fatalf("override type = %v, want boolean", override["type"])
	}
	description, _ := override["description"].(string)
	if !containsAll(description, "action=new", "active plan", "replacement") {
		t.Fatalf("override description %q does not explain intentional replacement", description)
	}
	for _, name := range []string{"patch", "operation", "section", "old_text", "new_text", "text", "checklist_item", "checked", "replace_all", "document", "document_patch", "info", "checkpoint_order", "active_checkpoint_id"} {
		if _, ok := params[name].(map[string]any); !ok {
			t.Fatalf("%s property missing or wrong type: %T", name, params[name])
		}
	}
}

func TestExitPlanModeDefinitionAcceptsStructuredDocument(t *testing.T) {
	definition := mustFindDefinition(t, "exit_plan_mode")
	if !containsAll(definition.Description, "structured", "SessionPlanDocument", "document") {
		t.Fatalf("exit_plan_mode description %q does not advertise structured document", definition.Description)
	}
	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", definition.Parameters["properties"])
	}
	for _, name := range []string{"title", "plan", "document", "plan_id", "id"} {
		if _, ok := params[name].(map[string]any); !ok {
			t.Fatalf("%s property missing or wrong type: %T", name, params[name])
		}
	}
	if required, ok := definition.Parameters["required"]; ok {
		t.Fatalf("exit_plan_mode should not require markdown-only title/plan fields, got required=%#v", required)
	}
}

func mustFindDefinition(t *testing.T, name string) Definition {
	t.Helper()
	rt := NewRuntime(1)
	for _, candidate := range rt.Definitions() {
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("%s definition not found", name)
	return Definition{}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
