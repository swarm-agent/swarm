package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestConvertToolDefinitionsPreservesEmptyRequiredArrayForWebsearch(t *testing.T) {
	definitions := convertToolDefinitions(tool.NewRuntime(1).Definitions())

	var parameters map[string]any
	for _, definition := range definitions {
		if definition.Name == "websearch" {
			parameters = definition.Parameters
			break
		}
	}
	if parameters == nil {
		t.Fatal("websearch tool definition not found")
	}

	required, ok := parameters["required"].([]string)
	if !ok {
		t.Fatalf("websearch required = %#v, want []string", parameters["required"])
	}
	if required == nil || len(required) != 0 {
		t.Fatalf("websearch required = %#v, want non-nil empty array", required)
	}

	encoded, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("marshal websearch parameters: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode websearch parameters: %v", err)
	}
	if got, ok := decoded["required"].([]any); !ok || !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("serialized websearch required = %#v, want []", decoded["required"])
	}
}

func TestTaskToolSchemaExposesOnlyDesignerOutputModeSelection(t *testing.T) {
	definitions := tool.NewRuntime(1).Definitions()
	var properties map[string]any
	for _, definition := range definitions {
		if definition.Name == "task" {
			properties, _ = definition.Parameters["properties"].(map[string]any)
			break
		}
	}
	if properties == nil {
		t.Fatal("task tool definition not found")
	}
	outputMode, ok := properties["output_mode"].(map[string]any)
	if !ok || !reflect.DeepEqual(outputMode["enum"], []string{"managed", "workspace"}) {
		t.Fatalf("task output_mode = %#v", outputMode)
	}
	source, ok := properties["source_artifact"].(map[string]any)
	if !ok {
		t.Fatal("task schema omits exact source_artifact")
	}
	sourceProperties, _ := source["properties"].(map[string]any)
	if !reflect.DeepEqual(source["required"], []string{"session_id", "collection_id", "variant_id", "event_seq"}) || sourceProperties["event_seq"] == nil {
		t.Fatalf("task source_artifact schema = %#v", source)
	}
	if description, _ := source["description"].(string); !strings.Contains(description, "managed Designer work") || !strings.Contains(description, "Regular mode requires every launch to be a managed Designer") || !strings.Contains(description, "passes the opaque reference to each worker") {
		t.Fatalf("task source_artifact description = %q", description)
	}
	sectionTarget, ok := properties["section_target"].(map[string]any)
	if !ok {
		t.Fatal("task schema omits exact section_target")
	}
	if description, _ := sectionTarget["description"].(string); !strings.Contains(description, "managed Designer work") || !strings.Contains(description, "Requires source_artifact") || !strings.Contains(description, "focused corrections") {
		t.Fatalf("task section_target description = %q", description)
	}
	controls, ok := properties["iteration_controls"].(map[string]any)
	if !ok || !reflect.DeepEqual(controls["required"], []string{"change"}) || controls["additionalProperties"] != false {
		t.Fatalf("task iteration_controls schema = %#v", controls)
	}
	controlProperties, _ := controls["properties"].(map[string]any)
	if controlProperties["preserve"] == nil || controlProperties["change"] == nil || controlProperties["exclude"] == nil {
		t.Fatalf("task iteration_controls fields = %#v", controlProperties)
	}
	for _, forbidden := range []string{"target_session_id", "collection_id", "variant_id", "artifact_target"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("task schema exposes trusted destination field %q", forbidden)
		}
	}
	launches := properties["launches"].(map[string]any)
	items := launches["items"].(map[string]any)
	launchProperties := items["properties"].(map[string]any)
	if _, ok := launchProperties["output_mode"]; !ok {
		t.Fatal("task launch schema omits output_mode")
	}
	program := properties["program"].(map[string]any)
	programProperties := program["properties"].(map[string]any)
	jobs := programProperties["jobs"].(map[string]any)
	jobItems := jobs["items"].(map[string]any)
	jobProperties := jobItems["properties"].(map[string]any)
	programOutputMode, ok := jobProperties["output_mode"].(map[string]any)
	if !ok || !reflect.DeepEqual(programOutputMode["enum"], []string{"managed", "workspace"}) {
		t.Fatalf("task program job output_mode = %#v", programOutputMode)
	}
	required := jobItems["required"].([]string)
	for _, name := range required {
		if name == "owned_scope" {
			t.Fatalf("task program job schema requires owned_scope, preventing managed Designer jobs: %#v", required)
		}
	}
}

func TestNormalizeProviderToolParametersPreservesNestedEmptySchemaArrays(t *testing.T) {
	parameters := map[string]any{
		"type":     "object",
		"required": []string{},
		"properties": map[string]any{
			"nested": map[string]any{
				"type":     "object",
				"required": []string{},
			},
		},
	}

	normalized := normalizeProviderToolParameters(parameters)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal normalized parameters: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode normalized parameters: %v", err)
	}
	if _, ok := decoded["required"].([]any); !ok {
		t.Fatalf("top-level required = %#v, want array", decoded["required"])
	}
	properties := decoded["properties"].(map[string]any)
	nested := properties["nested"].(map[string]any)
	if _, ok := nested["required"].([]any); !ok {
		t.Fatalf("nested required = %#v, want array", nested["required"])
	}
}
