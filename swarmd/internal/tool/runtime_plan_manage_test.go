package tool

import (
	"strings"
	"testing"
)

func TestPlanManageDefinitionIncludesExplicitNewOverride(t *testing.T) {
	rt := NewRuntime(1)
	var definition Definition
	found := false
	for _, candidate := range rt.Definitions() {
		if candidate.Name == "plan_manage" {
			definition = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("plan_manage definition not found")
	}
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
	for _, name := range []string{"patch", "operation", "section", "old_text", "new_text", "text", "checklist_item", "checked", "replace_all"} {
		if _, ok := params[name].(map[string]any); !ok {
			t.Fatalf("%s property missing or wrong type: %T", name, params[name])
		}
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
