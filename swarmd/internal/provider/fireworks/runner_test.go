package fireworks

import "testing"

func TestSanitizeFireworksToolParametersDropsNilRequired(t *testing.T) {
	input := map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   nil,
	}

	got := sanitizeFireworksToolParameters(input)
	if _, ok := got["required"]; ok {
		t.Fatalf("required = %#v, want omitted", got["required"])
	}
	if _, ok := input["required"]; !ok {
		t.Fatalf("input was mutated")
	}
}

func TestSanitizeFireworksToolParametersPreservesTopLevelRequiredArray(t *testing.T) {
	input := map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []any{"path", ""},
	}

	got := sanitizeFireworksToolParameters(input)
	required, ok := got["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T, want []string", got["required"])
	}
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("required = %#v, want [path]", required)
	}
}

func TestSanitizeFireworksToolParametersDropsNestedPropertyRequired(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"options": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   nil,
			},
		},
	}

	got := sanitizeFireworksToolParameters(input)
	properties := got["properties"].(map[string]any)
	options := properties["options"].(map[string]any)
	if _, ok := options["required"]; ok {
		t.Fatalf("nested required = %#v, want omitted", options["required"])
	}
}

func TestSanitizeFireworksToolParametersDefaultsEmptyObjectSchema(t *testing.T) {
	got := sanitizeFireworksToolParameters(nil)
	if got["type"] != "object" {
		t.Fatalf("type = %#v, want object", got["type"])
	}
	properties, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", got["properties"])
	}
	if len(properties) != 0 {
		t.Fatalf("properties = %#v, want empty", properties)
	}
}
