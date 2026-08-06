package run

import (
	"encoding/json"
	"reflect"
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
