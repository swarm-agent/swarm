package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

func TestSanitizeAnthropicToolSchemaTransformsComplexUnions(t *testing.T) {
	schema, err := sanitizeAnthropicToolSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"description": "boolean or options object",
				"anyOf": []any{
					map[string]any{"type": "boolean"},
					map[string]any{
						"type":                 "object",
						"properties":           map[string]any{},
						"required":             []string{},
						"additionalProperties": true,
					},
				},
			},
			"choice": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "string"},
					map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("sanitize schema: %v", err)
	}

	encoded := mustMarshalJSON(t, schema)
	assertNotContains(t, encoded, `"additionalProperties":true`)
	assertNotContains(t, encoded, `"oneOf"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertContains(t, encoded, `"anyOf"`)

	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props := decoded["properties"].(map[string]any)
	textSchema := props["text"].(map[string]any)
	variants := textSchema["anyOf"].([]any)
	objectVariant := variants[1].(map[string]any)
	if got := objectVariant["additionalProperties"]; got != false {
		t.Fatalf("nested object additionalProperties = %v, want false", got)
	}
	choiceSchema := props["choice"].(map[string]any)
	if _, ok := choiceSchema["oneOf"]; ok {
		t.Fatalf("oneOf should have been converted to anyOf: %v", choiceSchema)
	}
	if _, ok := choiceSchema["anyOf"]; !ok {
		t.Fatalf("anyOf missing after oneOf conversion: %v", choiceSchema)
	}
}

func TestSanitizeAnthropicToolSchemaMovesUnsupportedConstraintsToDescription(t *testing.T) {
	schema, err := sanitizeAnthropicToolSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"date": map[string]any{
				"type":        "string",
				"format":      "regex",
				"pattern":     "^[a-z]+$",
				"description": "A constrained string",
			},
			"items": map[string]any{
				"type":     "array",
				"minItems": 2,
				"items":    map[string]any{"type": "string"},
			},
		},
	})
	if err != nil {
		t.Fatalf("sanitize schema: %v", err)
	}

	encoded := mustMarshalJSON(t, schema)
	assertNotContains(t, encoded, `"format":"regex"`)
	assertNotContains(t, encoded, `"pattern"`)
	assertNotContains(t, encoded, `"minItems":2`)
	assertContains(t, encoded, `format: regex`)
	assertContains(t, encoded, `pattern: ^[a-z]+$`)
	assertContains(t, encoded, `minItems: 2`)
}

func TestBuildAnthropicToolsSanitizesWebFetchRuntimeSchema(t *testing.T) {
	var webfetch tool.Definition
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name == "webfetch" {
			webfetch = definition
			break
		}
	}
	if webfetch.Name == "" {
		t.Fatal("webfetch definition not found")
	}

	tools, _, err := buildAnthropicTools([]provideriface.ToolDefinition{{
		Name:        webfetch.Name,
		Description: webfetch.Description,
		Parameters:  webfetch.Parameters,
	}})
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 1 || tools[0].OfTool == nil {
		t.Fatalf("expected one custom tool, got %#v", tools)
	}
	encoded := mustMarshalJSON(t, tools[0].OfTool.InputSchema)
	assertContains(t, encoded, `"type":"object"`)
	assertContains(t, encoded, `"properties"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertNotContains(t, encoded, `"additionalProperties":true`)
	assertNotContains(t, encoded, `"oneOf"`)
}

func TestBuildAnthropicToolsUsesFullSchemaExtraFields(t *testing.T) {
	tools, _, err := buildAnthropicTools([]provideriface.ToolDefinition{{
		Name:        "webfetch",
		Description: "Fetch content",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "boolean"},
						map[string]any{
							"type":       "object",
							"properties": map[string]any{},
						},
					},
				},
			},
		},
	}})
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 1 || tools[0].OfTool == nil {
		t.Fatalf("expected one custom tool, got %#v", tools)
	}
	encoded := mustMarshalJSON(t, tools[0].OfTool.InputSchema)
	assertContains(t, encoded, `"type":"object"`)
	assertContains(t, encoded, `"properties"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertNotContains(t, encoded, `"additionalProperties":true`)
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(encoded)
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected %q not to contain %q", haystack, needle)
	}
}
