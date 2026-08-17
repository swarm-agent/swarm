package tool

import (
	"strings"
	"testing"
)

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
	for _, key := range []string{"mode", "agent_type", "count", "themes", "groups", "output_contract", "owned_scope_template", "animation_profile"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("task schema missing swarm-mode field %q", key)
		}
	}
	for _, want := range []string{"omit launches and regular-launch fields such as concurrency_reason", "Omit in mode=swarm", "swarm concurrency is defined by count"} {
		if !definitionTextContains(taskDefinition, want) {
			t.Fatalf("task schema missing regular/swarm field boundary %q: %#v", want, taskDefinition)
		}
	}
	for _, key := range []string{"swarm_strategy", "assembly_parts", "integration_contract"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("task schema must not advertise disabled Assembly field %q", key)
		}
	}
	for _, definition := range rt.Definitions() {
		if definition.Name == "swarm_mode" {
			t.Fatal("swarm mode must remain part of task, not a separate tool")
		}
	}
	for _, want := range []string{
		"same subagent policy",
		"same question directly without Router",
		"Iteration Swarm",
		"fast parallel alternatives or independent trials",
		"internal explore strategy remains implicit for backward compatibility",
		"Idea Swarm",
	} {
		if !definitionTextContains(taskDefinition, want) {
			t.Fatalf("task definition missing launch swarm contract %q: %#v", want, taskDefinition)
		}
	}
	if !definitionTextContains(taskDefinition, "structured launches array") || !definitionTextContains(taskDefinition, "Do not embed launch JSON") || !definitionTextContains(taskDefinition, "not text embedded in prompt") {
		t.Fatalf("task definition must warn models to use structured launch fields instead of prompt-embedded launch markup: %#v", taskDefinition)
	}
	for _, want := range []string{
		"explicitly requested multiple UI/design iterations or variants",
		"prohibited for ordinary UI work and single-design requests",
		"share the parent checkout",
		"read/search/find/list",
		"write/edit",
		"no Bash or Git",
		"ordinary reusable artifacts",
		"distinct non-overlapping workspace-relative",
	} {
		if !definitionTextContains(taskDefinition, want) {
			t.Fatalf("task definition missing Designer contract %q: %#v", want, taskDefinition)
		}
	}
	for _, key := range []string{"subagent_type", "agent", "purpose", "title", "meta_prompt", "role"} {
		property, ok := properties[key]
		if !ok {
			t.Fatalf("task single-launch shorthand missing %q", key)
		}
		if key == "subagent_type" || key == "agent" || key == "purpose" {
			assertTaskSubagentEnum(t, property, key)
		}
	}

	launches, ok := properties["launches"].(map[string]any)
	if !ok {
		t.Fatalf("task launches schema missing: %#v", properties["launches"])
	}
	if !containsInAny(launches["description"], "Regular mode only") || !containsInAny(launches["description"], "Omit launches in mode=swarm") {
		t.Fatalf("task launches schema does not distinguish regular mode from swarm mode: %#v", launches)
	}
	items, ok := launches["items"].(map[string]any)
	if !ok {
		t.Fatalf("task launches item schema missing: %#v", launches)
	}
	for _, key := range []string{"subagent_type", "agent", "purpose", "title", "meta_prompt", "role", "animation_profile"} {
		property, ok := itemsProperty(items, key)
		if !ok {
			t.Fatalf("task launches item missing property %q", key)
		}
		if key == "subagent_type" || key == "agent" || key == "purpose" {
			assertTaskSubagentEnum(t, property, "launches."+key)
		}
	}
	animationProfile, ok := properties["animation_profile"].(map[string]any)
	if !ok || !containsInAny(animationProfile, "spatial_3d") {
		t.Fatalf("task animation_profile schema does not expose the closed profile registry: %#v", properties["animation_profile"])
	}
	program, ok := properties["program"].(map[string]any)
	if !ok {
		t.Fatalf("task program schema missing: %#v", properties["program"])
	}
	jobs, ok := itemsProperty(program, "jobs")
	if !ok {
		t.Fatalf("task program jobs schema missing: %#v", program)
	}
	jobArray, ok := jobs.(map[string]any)
	if !ok {
		t.Fatalf("task program jobs schema = %#v, want object", jobs)
	}
	jobItems, ok := jobArray["items"].(map[string]any)
	if !ok {
		t.Fatalf("task program job item schema missing: %#v", jobArray)
	}
	if _, ok := itemsProperty(jobItems, "animation_profile"); !ok {
		t.Fatalf("task program job schema missing animation_profile: %#v", jobItems)
	}
	if !definitionTextContains(taskDefinition, "ideally three words") || !definitionTextContains(taskDefinition, "full instructive") {
		t.Fatalf("task definition must separate concise cosmetic titles from full child instructions: %#v", taskDefinition)
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

func assertTaskSubagentEnum(t *testing.T, property any, label string) {
	t.Helper()
	schema, ok := property.(map[string]any)
	if !ok {
		t.Fatalf("task %s schema = %#v, want object", label, property)
	}
	values, ok := schema["enum"].([]string)
	if !ok || len(values) != 3 || values[0] != "coder" || values[1] != "finder" || values[2] != "designer" {
		t.Fatalf("task %s enum = %#v, want coder, finder, and designer", label, schema["enum"])
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

func definitionTextContains(definition Definition, needle string) bool {
	if containsInAny(definition.Description, needle) {
		return true
	}
	return containsInMap(definition.Parameters, needle)
}

func containsInMap(value map[string]any, needle string) bool {
	for _, entry := range value {
		if containsInAny(entry, needle) {
			return true
		}
	}
	return false
}

func containsInAny(value any, needle string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, needle)
	case map[string]any:
		return containsInMap(typed, needle)
	case []any:
		for _, entry := range typed {
			if containsInAny(entry, needle) {
				return true
			}
		}
	}
	return false
}
