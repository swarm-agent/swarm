package fireworks

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

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

func TestBuildChatCompletionRequestTaskToolHasNoNestedCombinators(t *testing.T) {
	req := buildChatCompletionRequest(provideriface.Request{
		Model: "test-model",
		Tools: []provideriface.ToolDefinition{{
			Name: "task",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt": map[string]any{"type": "string"},
					"launches": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"subagent_type": map[string]any{"type": "string"},
								"meta_prompt":   map[string]any{"type": "string"},
							},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"prompt"},
				"additionalProperties": false,
			},
		}},
	})
	if len(req.Tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(req.Tools))
	}
	parameters := req.Tools[0].Function.Parameters
	properties := parameters["properties"].(map[string]any)
	launches := properties["launches"].(map[string]any)
	items := launches["items"].(map[string]any)
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if _, ok := items[key]; ok {
			t.Fatalf("task launches item schema contains %s: %#v", key, items[key])
		}
	}
}

func TestBuildChatCompletionRequestMapsThinkingToReasoningEffort(t *testing.T) {
	req := buildChatCompletionRequest(provideriface.Request{
		Model:    "test-model",
		Thinking: "high",
	})
	if req.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", req.ReasoningEffort)
	}

	req = buildChatCompletionRequest(provideriface.Request{
		Model:    "test-model",
		Thinking: "xhigh",
	})
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want omitted for unsupported level", req.ReasoningEffort)
	}
}

func TestParseChatCompletionResponseExtractsReasoningContent(t *testing.T) {
	response := parseChatCompletionResponse(chatCompletionResponse{
		ID:    "chatcmpl-test",
		Model: "accounts/fireworks/models/reasoning",
		Choices: []chatCompletionChoice{{
			Message: chatCompletionMessage{
				Role:             "assistant",
				ReasoningContent: " inspect adapter ",
				Content:          " final answer ",
			},
			FinishReason: "stop",
		}},
	})
	if response.ReasoningSummary != "inspect adapter" {
		t.Fatalf("reasoning summary = %q, want inspect adapter", response.ReasoningSummary)
	}
	if response.Text != "final answer" {
		t.Fatalf("text = %q, want final answer", response.Text)
	}
}

func TestFireworksStreamStateAccumulatesReasoningContent(t *testing.T) {
	state := newFireworksStreamState()
	state.apply(chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ReasoningContent: "Plan: "},
	}}})
	state.apply(chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ReasoningContent: "inspect", Content: "done"},
	}}})

	response := parseChatCompletionResponse(state.response())
	if response.ReasoningSummary != "Plan: inspect" {
		t.Fatalf("reasoning summary = %q, want Plan: inspect", response.ReasoningSummary)
	}
	if response.Text != "done" {
		t.Fatalf("text = %q, want done", response.Text)
	}
}
