package codex

import (
	"encoding/json"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

func TestToCodexToolsMarksSchemasNonStrictAndNormalizesNestedObjects(t *testing.T) {
	input := []provideriface.ToolDefinition{
		{
			Type:        "function",
			Name:        "websearch",
			Description: "test tool",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"contents": map[string]any{
						"type": "object",
					},
					"queries": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
						},
					},
				},
			},
		},
	}

	tools := toCodexTools(input)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Strict {
		t.Fatal("strict = true, want false")
	}

	properties, ok := tools[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want map", tools[0].Parameters["properties"])
	}
	contents, ok := properties["contents"].(map[string]any)
	if !ok {
		t.Fatalf("contents = %#v, want map", properties["contents"])
	}
	if got := strings.TrimSpace(asString(contents["type"])); got != "string" {
		t.Fatalf("contents.type = %q, want string", got)
	}
	if got := strings.TrimSpace(asString(contents["description"])); got != "JSON-encoded object value" {
		t.Fatalf("contents.description = %q, want JSON string description", got)
	}
	if _, ok := contents["properties"]; ok {
		t.Fatalf("contents.properties = %#v, want absent after rewrite", contents["properties"])
	}

	queries, ok := properties["queries"].(map[string]any)
	if !ok {
		t.Fatalf("queries = %#v, want map", properties["queries"])
	}
	items, ok := queries["items"].(map[string]any)
	if !ok {
		t.Fatalf("queries.items = %#v, want map", queries["items"])
	}
	if got := strings.TrimSpace(asString(items["type"])); got != "string" {
		t.Fatalf("queries.items.type = %q, want string", got)
	}
	if got := strings.TrimSpace(asString(items["description"])); got != "JSON-encoded object value" {
		t.Fatalf("queries.items.description = %q, want JSON string description", got)
	}

	originalProperties, ok := input[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("original properties = %#v, want map", input[0].Parameters["properties"])
	}
	originalContents, ok := originalProperties["contents"].(map[string]any)
	if !ok {
		t.Fatalf("original contents = %#v, want map", originalProperties["contents"])
	}
	if _, ok := originalContents["required"]; ok {
		t.Fatalf("source schema mutated with required: %#v", originalContents["required"])
	}
}

func TestToCodexToolsSanitizesRuntimeToolSchemasForCodexResponses(t *testing.T) {
	runtime := tool.NewRuntime(1)
	definitions := runtime.Definitions()
	input := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		input = append(input, provideriface.ToolDefinition{
			Type:        definition.Type,
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
		})
	}

	tools := toCodexTools(input)
	if len(tools) != len(definitions) {
		t.Fatalf("tools = %d, want %d", len(tools), len(definitions))
	}

	for _, definition := range tools {
		if definition.Strict {
			t.Fatalf("tool %q strict = true, want false", definition.Name)
		}
		if hasNilSchemaValue(definition.Parameters) {
			encoded, _ := json.Marshal(definition.Parameters)
			t.Fatalf("tool %q contains nil schema value: %s", definition.Name, string(encoded))
		}
	}

	websearch := findCodexToolByName(t, tools, "websearch")
	websearchProperties, ok := websearch.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("websearch.properties = %#v, want map", websearch.Parameters["properties"])
	}
	contents, ok := websearchProperties["contents"].(map[string]any)
	if !ok {
		t.Fatalf("websearch.contents = %#v, want map", websearchProperties["contents"])
	}
	if got := strings.TrimSpace(asString(contents["type"])); got != "string" {
		t.Fatalf("websearch.contents.type = %q, want string", got)
	}
	if got := strings.TrimSpace(asString(contents["description"])); !strings.Contains(got, "JSON-encoded object string") {
		t.Fatalf("websearch.contents.description = %q, want JSON object string guidance", got)
	}
	outputSchema, ok := websearchProperties["output_schema"].(map[string]any)
	if !ok {
		t.Fatalf("websearch.output_schema = %#v, want map", websearchProperties["output_schema"])
	}
	if got := strings.TrimSpace(asString(outputSchema["type"])); got != "string" {
		t.Fatalf("websearch.output_schema.type = %q, want string", got)
	}
	if got := strings.TrimSpace(asString(outputSchema["description"])); !strings.Contains(got, "JSON-encoded object string") {
		t.Fatalf("websearch.output_schema.description = %q, want JSON object string guidance", got)
	}

	webfetch := findCodexToolByName(t, tools, "webfetch")
	webfetchProperties, ok := webfetch.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("webfetch.properties = %#v, want map", webfetch.Parameters["properties"])
	}
	subpageTarget, ok := webfetchProperties["subpage_target"].(map[string]any)
	if !ok {
		t.Fatalf("webfetch.subpage_target = %#v, want map", webfetchProperties["subpage_target"])
	}
	if got := asSlice(subpageTarget["anyOf"]); len(got) != 2 {
		t.Fatalf("webfetch.subpage_target.anyOf = %#v, want 2 variants", got)
	}
	text, ok := webfetchProperties["text"].(map[string]any)
	if !ok {
		t.Fatalf("webfetch.text = %#v, want map", webfetchProperties["text"])
	}
	textVariants := asSlice(text["anyOf"])
	if len(textVariants) != 2 {
		t.Fatalf("webfetch.text.anyOf = %#v, want 2 variants", textVariants)
	}
	textObjectVariant, ok := textVariants[1].(map[string]any)
	if !ok {
		t.Fatalf("webfetch.text.anyOf[1] = %#v, want map", textVariants[1])
	}
	if got := strings.TrimSpace(asString(textObjectVariant["type"])); got != "string" {
		t.Fatalf("webfetch.text.anyOf[1].type = %q, want string", got)
	}
}

func TestBuildRequestPayloadIncludesExplicitNonStrictTools(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		Model: "gpt-5.4",
		Input: []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
		Tools: toCodexTools([]provideriface.ToolDefinition{
			{
				Type: "function",
				Name: "websearch",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}),
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	tools, ok := decoded["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("decoded tools = %#v, want single entry", decoded["tools"])
	}
	toolDef, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded tool = %#v, want object", tools[0])
	}
	if got, ok := toolDef["strict"].(bool); !ok || got {
		t.Fatalf("tool.strict = %#v, want false", toolDef["strict"])
	}
}

func findCodexToolByName(t *testing.T, tools []ToolDefinition, name string) ToolDefinition {
	t.Helper()
	for _, definition := range tools {
		if definition.Name == name {
			return definition
		}
	}
	t.Fatalf("tool %q not found", name)
	return ToolDefinition{}
}

func hasNilSchemaValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, item := range typed {
			if hasNilSchemaValue(item) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range typed {
			if hasNilSchemaValue(item) {
				return true
			}
		}
		return false
	case []string:
		return false
	default:
		return false
	}
}
