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
	anyOf, ok := items["anyOf"].([]any)
	if !ok || len(anyOf) != 6 {
		t.Fatalf("task launches must require saved-agent plus assignment combinations in one anyOf, got %#v", items["anyOf"])
	}
	for _, agentKey := range []string{"subagent_type", "agent", "purpose"} {
		for _, assignmentKey := range []string{"meta_prompt", "role"} {
			if !schemaAnyOfContainsRequiredSet(anyOf, agentKey, assignmentKey) {
				t.Fatalf("task launches missing required combination %s + %s: %#v", agentKey, assignmentKey, anyOf)
			}
		}
	}
}

func schemaAnyOfContainsRequiredSet(anyOf []any, required ...string) bool {
	want := map[string]bool{}
	for _, value := range required {
		want[value] = true
	}
	for _, item := range anyOf {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, ok := entry["required"].([]string)
		if !ok || len(values) != len(required) {
			continue
		}
		seen := map[string]bool{}
		for _, value := range values {
			seen[value] = true
		}
		matches := true
		for value := range want {
			if !seen[value] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
