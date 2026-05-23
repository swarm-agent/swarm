package tool

import "testing"

func TestTaskDefinitionRequiresExplicitSavedAgentAssignments(t *testing.T) {
	rt := NewRuntime(1)
	var taskDefinition Definition
	for _, definition := range rt.Definitions() {
		if definition.Name == "task" {
			taskDefinition = definition
			break
		}
	}
	if taskDefinition.Name == "" {
		t.Fatal("task definition not found")
	}

	properties, ok := taskDefinition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("task properties missing: %#v", taskDefinition.Parameters)
	}
	if _, ok := properties["allow_bash"]; ok {
		t.Fatal("task schema must not expose launch-time allow_bash")
	}
	for _, key := range []string{"subagent_type", "agent", "purpose", "meta_prompt", "role"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("task single-launch shorthand missing %q", key)
		}
	}

	launches, ok := properties["launches"].(map[string]any)
	if !ok {
		t.Fatalf("task launches schema missing: %#v", properties["launches"])
	}
	items, ok := launches["items"].(map[string]any)
	if !ok {
		t.Fatalf("task launches item schema missing: %#v", launches)
	}
	allOf, ok := items["allOf"].([]any)
	if !ok || len(allOf) != 2 {
		t.Fatalf("task launches must require agent and assignment groups, got %#v", items["allOf"])
	}
	if !schemaRequirementGroupContains(allOf[0], "subagent_type", "agent", "purpose") {
		t.Fatalf("task launches missing saved-agent requirement group: %#v", allOf[0])
	}
	if !schemaRequirementGroupContains(allOf[1], "meta_prompt", "role") {
		t.Fatalf("task launches missing assignment requirement group: %#v", allOf[1])
	}
}

func schemaRequirementGroupContains(group any, required ...string) bool {
	object, ok := group.(map[string]any)
	if !ok {
		return false
	}
	anyOf, ok := object["anyOf"].([]any)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, item := range anyOf {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, ok := entry["required"].([]string)
		if !ok || len(values) != 1 {
			continue
		}
		seen[values[0]] = true
	}
	for _, value := range required {
		if !seen[value] {
			return false
		}
	}
	return true
}
