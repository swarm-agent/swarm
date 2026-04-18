package google

import (
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

func TestBuildGoogleRequestSanitizesToolParametersForGemini(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type": "string",
			},
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode": map[string]any{
						"type": "string",
					},
				},
				"additionalProperties": false,
			},
			"entries": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type": "string",
						},
					},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}

	request := buildGoogleRequest(provideriface.Request{
		Input: []map[string]any{
			{
				"role":    "user",
				"content": "run the tool",
			},
		},
		Tools: []provideriface.ToolDefinition{
			{
				Type:       "function",
				Name:       "read",
				Parameters: parameters,
			},
		},
	})

	if len(request.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(request.Tools))
	}
	declarations := request.Tools[0].FunctionDeclarations
	if len(declarations) != 1 {
		t.Fatalf("function declarations = %d, want 1", len(declarations))
	}
	schema := declarations[0].Parameters
	if len(schema) == 0 {
		t.Fatal("sanitized schema is empty")
	}
	if hasSchemaKey(schema, "additionalProperties") {
		t.Fatalf("sanitized schema still contains additionalProperties: %#v", schema)
	}
	if !hasSchemaKey(parameters, "additionalProperties") {
		t.Fatal("source schema was mutated; expected additionalProperties to remain in source")
	}
}

func TestBuildGoogleRequestSanitizesRuntimeToolSchemas(t *testing.T) {
	runtime := tool.NewRuntime(1)
	definitions := runtime.Definitions()
	tools := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, provideriface.ToolDefinition{
			Type:        definition.Type,
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
		})
	}

	request := buildGoogleRequest(provideriface.Request{
		Input: []map[string]any{
			{
				"role":    "user",
				"content": "run the tool",
			},
		},
		Tools: tools,
	})

	if len(request.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(request.Tools))
	}
	declarations := request.Tools[0].FunctionDeclarations
	if len(declarations) != len(definitions) {
		t.Fatalf("function declarations = %d, want %d", len(declarations), len(definitions))
	}
	for _, declaration := range declarations {
		if hasSchemaKey(declaration.Parameters, "additionalProperties") {
			t.Fatalf("declaration %q includes additionalProperties: %#v", declaration.Name, declaration.Parameters)
		}
	}
}

func TestBuildGoogleRequestIncludesThinkingBudgetForGeminiThinkingModels(t *testing.T) {
	tests := []struct {
		name     string
		thinking string
		want     int
	}{
		{name: "off", thinking: "off", want: 0},
		{name: "low", thinking: "low", want: 1024},
		{name: "medium", thinking: "medium", want: 4096},
		{name: "high", thinking: "high", want: 8192},
		{name: "xhigh", thinking: "xhigh", want: 16384},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := buildGoogleRequest(provideriface.Request{
				Model:    "gemini-3-pro-preview",
				Thinking: tc.thinking,
				Input: []map[string]any{
					{
						"role":    "user",
						"content": "hello",
					},
				},
			})
			if request.GenerationConfig == nil || request.GenerationConfig.ThinkingConfig == nil || request.GenerationConfig.ThinkingConfig.ThinkingBudget == nil {
				t.Fatalf("thinking config is missing for %s", tc.thinking)
			}
			if got := *request.GenerationConfig.ThinkingConfig.ThinkingBudget; got != tc.want {
				t.Fatalf("thinking budget = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildGoogleRequestOmitsThinkingBudgetForNonThinkingModel(t *testing.T) {
	request := buildGoogleRequest(provideriface.Request{
		Model:    "gemini-2.0-flash",
		Thinking: "high",
		Input: []map[string]any{
			{
				"role":    "user",
				"content": "hello",
			},
		},
	})
	if request.GenerationConfig != nil {
		t.Fatalf("generation config should be empty for non-thinking model: %#v", request.GenerationConfig)
	}
}

func TestBuildGoogleContentsGroupsParallelFunctionCallsAndOutputs(t *testing.T) {
	contents := buildGoogleContents([]map[string]any{
		{
			"role":    "user",
			"content": "Check two tools",
		},
		{
			"type":      "function_call",
			"call_id":   "call_1",
			"name":      "check_flight",
			"arguments": `{"flight":"AA100"}`,
			"metadata": map[string]any{
				"google": map[string]any{
					"thought_signature": "SignatureA",
				},
			},
		},
		{
			"type":      "function_call",
			"call_id":   "call_2",
			"name":      "book_taxi",
			"arguments": `{"time":"10 AM"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_1",
			"output":  `{"status":"delayed"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_2",
			"output":  `{"booking_status":"success"}`,
		},
	})

	if len(contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(contents))
	}
	if contents[1].Role != "model" {
		t.Fatalf("model role = %q, want model", contents[1].Role)
	}
	if len(contents[1].Parts) != 2 {
		t.Fatalf("model parts = %d, want 2", len(contents[1].Parts))
	}
	if got := contents[1].Parts[0].ThoughtSignature; got != "SignatureA" {
		t.Fatalf("first thought signature = %q, want SignatureA", got)
	}
	if got := contents[1].Parts[1].ThoughtSignature; got != "" {
		t.Fatalf("second thought signature = %q, want empty", got)
	}
	if contents[2].Role != "user" {
		t.Fatalf("response role = %q, want user", contents[2].Role)
	}
	if len(contents[2].Parts) != 2 {
		t.Fatalf("response parts = %d, want 2", len(contents[2].Parts))
	}
	if got := contents[2].Parts[0].FunctionResponse.Name; got != "check_flight" {
		t.Fatalf("first function response name = %q, want check_flight", got)
	}
	if got := contents[2].Parts[1].FunctionResponse.Name; got != "book_taxi" {
		t.Fatalf("second function response name = %q, want book_taxi", got)
	}
}

func TestParseGoogleResponseIncludesThoughtSignatureMetadata(t *testing.T) {
	parsed := parseGoogleResponse(googleResponse{
		Candidates: []googleCandidate{
			{
				Content: googleContent{
					Parts: []googlePart{
						{
							FunctionCall: &googleFunctionCall{
								Name: "check_flight",
								Args: map[string]any{"flight": "AA100"},
							},
							ThoughtSignature: "SignatureA",
						},
						{
							FunctionCall: &googleFunctionCall{
								Name: "book_taxi",
								Args: map[string]any{"time": "10 AM"},
							},
						},
					},
				},
			},
		},
	})

	if len(parsed.FunctionCalls) != 2 {
		t.Fatalf("function calls = %d, want 2", len(parsed.FunctionCalls))
	}
	firstMetadata := parsed.FunctionCalls[0].Metadata
	if len(firstMetadata) == 0 {
		t.Fatalf("first function call metadata is empty")
	}
	googleMetadata, ok := firstMetadata["google"].(map[string]any)
	if !ok {
		t.Fatalf("missing google metadata: %#v", firstMetadata)
	}
	if got, _ := googleMetadata["thought_signature"].(string); got != "SignatureA" {
		t.Fatalf("thought_signature = %q, want SignatureA", got)
	}
	if parsed.FunctionCalls[1].Metadata != nil {
		t.Fatalf("second function call metadata = %#v, want nil", parsed.FunctionCalls[1].Metadata)
	}
}

func TestParseGoogleResponseIncludesThoughtSignatureMetadataFromSnakeCaseField(t *testing.T) {
	parsed := parseGoogleResponse(googleResponse{
		Candidates: []googleCandidate{
			{
				Content: googleContent{
					Parts: []googlePart{
						{
							FunctionCall: &googleFunctionCall{
								Name: "bash",
								Args: map[string]any{"command": "echo hi"},
							},
							ThoughtSignatureSnake: "SnakeSig",
						},
					},
				},
			},
		},
	})
	if len(parsed.FunctionCalls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(parsed.FunctionCalls))
	}
	googleMetadata, ok := parsed.FunctionCalls[0].Metadata["google"].(map[string]any)
	if !ok {
		t.Fatalf("missing google metadata: %#v", parsed.FunctionCalls[0].Metadata)
	}
	if got, _ := googleMetadata["thought_signature"].(string); got != "SnakeSig" {
		t.Fatalf("thought_signature = %q, want SnakeSig", got)
	}
}

func TestParseGoogleResponseUsesPendingThoughtSignatureFromNonFunctionPart(t *testing.T) {
	parsed := parseGoogleResponse(googleResponse{
		Candidates: []googleCandidate{
			{
				Content: googleContent{
					Parts: []googlePart{
						{
							Text:                  "",
							ThoughtSignature:      "PendingSig",
							ThoughtSignatureSnake: "",
						},
						{
							FunctionCall: &googleFunctionCall{
								Name: "bash",
								Args: map[string]any{"command": "echo hi"},
							},
						},
					},
				},
			},
		},
	})
	if len(parsed.FunctionCalls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(parsed.FunctionCalls))
	}
	googleMetadata, ok := parsed.FunctionCalls[0].Metadata["google"].(map[string]any)
	if !ok {
		t.Fatalf("missing google metadata: %#v", parsed.FunctionCalls[0].Metadata)
	}
	if got, _ := googleMetadata["thought_signature"].(string); got != "PendingSig" {
		t.Fatalf("thought_signature = %q, want PendingSig", got)
	}
}

func TestParseGoogleResponseMapsUsageMetadata(t *testing.T) {
	parsed := parseGoogleResponse(googleResponse{
		Candidates: []googleCandidate{
			{
				Content: googleContent{
					Parts: []googlePart{{Text: "hello"}},
				},
			},
		},
		UsageMetadata: &googleUsageMetadata{
			PromptTokenCount:        120,
			CandidatesTokenCount:    30,
			ThoughtsTokenCount:      12,
			TotalTokenCount:         150,
			CachedContentTokenCount: 45,
		},
	})

	if got := parsed.Usage.Source; got != "google_api_usage" {
		t.Fatalf("usage source = %q, want google_api_usage", got)
	}
	if got := parsed.Usage.InputTokens; got != 120 {
		t.Fatalf("input tokens = %d, want 120", got)
	}
	if got := parsed.Usage.OutputTokens; got != 30 {
		t.Fatalf("output tokens = %d, want 30", got)
	}
	if got := parsed.Usage.ThinkingTokens; got != 12 {
		t.Fatalf("thinking tokens = %d, want 12", got)
	}
	if got := parsed.Usage.TotalTokens; got != 150 {
		t.Fatalf("total tokens = %d, want 150", got)
	}
	if got := parsed.Usage.CacheReadTokens; got != 45 {
		t.Fatalf("cache read tokens = %d, want 45", got)
	}
	if got := parsed.Usage.APIUsageRawPath; got != "usageMetadata" {
		t.Fatalf("api usage raw path = %q, want usageMetadata", got)
	}
	if got := len(parsed.Usage.APIUsageHistory); got != 1 {
		t.Fatalf("api usage history entries = %d, want 1", got)
	}
	if got := len(parsed.Usage.APIUsagePaths); got != 1 || parsed.Usage.APIUsagePaths[0] != "usageMetadata" {
		t.Fatalf("api usage paths = %#v, want [usageMetadata]", parsed.Usage.APIUsagePaths)
	}
	if rawTotal, ok := parsed.Usage.APIUsageRaw["totalTokenCount"].(int64); !ok || rawTotal != 150 {
		t.Fatalf("api usage raw totalTokenCount = %#v, want int64(150)", parsed.Usage.APIUsageRaw["totalTokenCount"])
	}
}

func TestParseGoogleResponseUsageKeepsAPITotalWithoutLocalFallback(t *testing.T) {
	parsed := parseGoogleResponse(googleResponse{
		Candidates: []googleCandidate{
			{
				Content: googleContent{
					Parts: []googlePart{{Text: "hello"}},
				},
			},
		},
		UsageMetadata: &googleUsageMetadata{
			PromptTokenCount:     200,
			CandidatesTokenCount: 80,
			ThoughtsTokenCount:   15,
			TotalTokenCount:      0,
		},
	})

	if got := parsed.Usage.TotalTokens; got != 0 {
		t.Fatalf("total tokens = %d, want 0", got)
	}
	if got := parsed.Usage.InputTokens; got != 200 {
		t.Fatalf("input tokens = %d, want 200", got)
	}
}

func hasSchemaKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for currentKey, item := range typed {
			if currentKey == key {
				return true
			}
			if hasSchemaKey(item, key) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if hasSchemaKey(item, key) {
				return true
			}
		}
	}
	return false
}

func TestSanitizeGoogleTextRedactsGoogleAPIKeyQuery(t *testing.T) {
	raw := "Post \"https://generativelanguage.googleapis.com/v1beta/models/gemini-3-flash:generateContent?key=AIzaSySecret123456789\""
	sanitized := sanitizeGoogleText(raw)
	if strings.Contains(sanitized, "AIzaSySecret123456789") {
		t.Fatalf("sanitizeGoogleText leaked api key: %s", sanitized)
	}
	if !strings.Contains(sanitized, "key=[redacted]") {
		t.Fatalf("sanitizeGoogleText = %q, want redacted query key", sanitized)
	}
}
