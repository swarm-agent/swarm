package tool

import "testing"

func TestTaskDefinitionKeepsProviderSchemaSimpleAndDocumentsRuntimeRequirements(t *testing.T) {
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
	for _, key := range []string{"subagent_type", "agent", "purpose", "meta_prompt", "role"} {
		if _, ok := itemsProperty(items, key); !ok {
			t.Fatalf("task launches item missing property %q", key)
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if _, ok := items[key]; ok {
			t.Fatalf("task launches provider schema must not enforce runtime-only %s requirements: %#v", key, items[key])
		}
	}
	if _, ok := items["required"]; ok {
		t.Fatalf("task launches provider schema must not require agent/assignment fields: %#v", items["required"])
	}
}

func itemsProperty(items map[string]any, key string) (any, bool) {
	properties, ok := items["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := properties[key]
	return value, ok
}
