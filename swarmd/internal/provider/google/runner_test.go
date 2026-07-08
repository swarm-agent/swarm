package google

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestGoogleThinkingConfigUsesCatalogThinkingLevelMapping(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-3.5-flash",
		Thinking: "high",
		ModelCatalog: pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:           "high",
			ProviderParameter:      "generationConfig.thinkingConfig.thinkingLevel",
			ProviderValue:          "high",
			EffectiveProviderValue: "high",
			Behavior:               "effort",
		}}},
	})
	if cfg == nil || cfg.ThinkingLevel != "high" || cfg.ThinkingBudget != nil || !cfg.IncludeThoughts {
		t.Fatalf("google thinking config = %+v, want thinkingLevel=high with includeThoughts and without thinkingBudget", cfg)
	}
}

func TestGoogleThinkingConfigUsesCatalogBudgetMapping(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-2.5-flash",
		Thinking: "off",
		ModelCatalog: pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:      "off",
			ProviderParameter: "generationConfig.thinkingConfig.thinkingBudget",
			ProviderValue:     "0",
			Behavior:          "disabled",
		}}},
	})
	if cfg == nil || cfg.ThinkingBudget == nil || *cfg.ThinkingBudget != 0 || cfg.ThinkingLevel != "" || cfg.IncludeThoughts {
		t.Fatalf("google thinking config = %+v, want thinkingBudget=0 without thinkingLevel/includeThoughts", cfg)
	}
}

func TestBuildGoogleInteractionsRequestUsesThinkingSummaries(t *testing.T) {
	req := buildGoogleInteractionsRequest(provideriface.Request{
		Model:        "gemini-3.5-flash",
		Thinking:     "high",
		Instructions: "be concise",
		ToolChoice:   "auto",
		Input: []map[string]any{{
			"role":    "user",
			"content": "explain",
		}},
		Tools: []provideriface.ToolDefinition{{
			Name:        "lookup",
			Description: "look up a fact",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, "gemini-3.5-flash", true)

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal interactions request: %v", err)
	}
	encoded := string(raw)
	for _, want := range []string{
		`"model":"gemini-3.5-flash"`,
		`"stream":true`,
		`"store":false`,
		`"system_instruction":"be concise"`,
		`"thinking_level":"high"`,
		`"thinking_summaries":"auto"`,
		`"tool_choice":"auto"`,
		`"type":"function"`,
		`"name":"lookup"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("interactions request JSON = %s, want %s", encoded, want)
		}
	}
}

func TestBuildGoogleContentsPreservesThoughtSignatureMetadata(t *testing.T) {
	contents := buildGoogleContents([]map[string]any{{
		"type":      "function_call",
		"call_id":   "call_weather",
		"name":      "weather",
		"arguments": `{"city":"Paris"}`,
		"metadata":  map[string]any{"google": map[string]any{"thought_signature": "sig-123"}},
	}})
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("contents = %+v, want one function call part", contents)
	}
	part := contents[0].Parts[0]
	if part.ThoughtSignature != "sig-123" {
		t.Fatalf("thought signature = %q, want sig-123", part.ThoughtSignature)
	}
	if part.FunctionCall == nil || part.FunctionCall.ID != "call_weather" {
		t.Fatalf("function call = %+v, want provider call id preserved", part.FunctionCall)
	}
}

func TestBuildGoogleInteractionsInputAttachesFunctionCallSignature(t *testing.T) {
	input := buildGoogleInteractionsInput([]map[string]any{
		{
			"role":    "user",
			"content": "Check my plan",
		},
		{
			"type":      "function_call",
			"call_id":   "call_plan",
			"name":      "plan_manage",
			"arguments": `{"action":"get-active"}`,
			"metadata": map[string]any{
				"google": map[string]any{
					"thought_signature": "sig-call",
				},
			},
		},
		{
			"type":    "function_call_output",
			"call_id": "call_plan",
			"output":  `{"status":"empty"}`,
		},
	})
	steps, ok := input.([]map[string]any)
	if !ok {
		t.Fatalf("interactions input = %#v, want step slice", input)
	}
	if len(steps) != 3 {
		t.Fatalf("steps = %#v, want user input, function call, function result", steps)
	}
	callStep := steps[1]
	if got := callStep["type"]; got != "function_call" {
		t.Fatalf("call type = %#v, want function_call", got)
	}
	if got := callStep["signature"]; got != "sig-call" {
		t.Fatalf("call signature = %#v, want sig-call", got)
	}
	if _, ok := callStep["call_id"]; ok {
		t.Fatalf("call step unexpectedly used call_id field: %#v", callStep)
	}
	if got := steps[2]["call_id"]; got != "call_plan" {
		t.Fatalf("function result call_id = %#v, want call_plan", got)
	}
}

func TestParseGoogleInteractionResponsePreservesFunctionCallSignatureMetadata(t *testing.T) {
	response := parseGoogleInteractionResponse(googleInteractionResponse{
		Steps: []googleInteractionStep{{
			Type:      "function_call",
			ID:        "call_plan",
			Name:      "plan_manage",
			Signature: "sig-call",
			Arguments: map[string]any{"action": "get-active"},
		}},
	})
	if len(response.FunctionCalls) != 1 {
		t.Fatalf("function calls = %+v, want one", response.FunctionCalls)
	}
	googleMetadata, ok := response.FunctionCalls[0].Metadata["google"].(map[string]any)
	if !ok {
		t.Fatalf("missing google metadata: %#v", response.FunctionCalls[0].Metadata)
	}
	if got, _ := googleMetadata["thought_signature"].(string); got != "sig-call" {
		t.Fatalf("thought_signature = %q, want sig-call", got)
	}
}

func TestGoogleThinkingConfigIncludesVisibleThoughtsForLegacyBudget(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-2.5-flash",
		Thinking: "medium",
	})
	if cfg == nil || cfg.ThinkingBudget == nil || *cfg.ThinkingBudget != 4096 || !cfg.IncludeThoughts {
		t.Fatalf("google thinking config = %+v, want legacy budget with includeThoughts", cfg)
	}
}

func TestParseGoogleResponseSeparatesThoughtTextFromOutput(t *testing.T) {
	response := parseGoogleResponse(googleResponse{Candidates: []googleCandidate{{
		FinishReason: "STOP",
		Content: googleContent{Parts: []googlePart{
			{Text: "plan", Thought: true},
			{Text: "answer"},
			{Text: " inspect", Thought: true},
		}},
	}}})
	if response.Text != "answer" {
		t.Fatalf("text = %q, want answer", response.Text)
	}
	if response.ReasoningSummary != "plan\n\ninspect" {
		t.Fatalf("reasoning summary = %q, want thought text only", response.ReasoningSummary)
	}
}

func TestParseGoogleUsageMapsResponseAndThoughtCacheTokens(t *testing.T) {
	usage := parseGoogleUsage(googleResponse{UsageMetadata: &googleUsageMetadata{
		PromptTokenCount:        10,
		ResponseTokenCount:      7,
		ThoughtsTokenCount:      5,
		TotalTokenCount:         22,
		CachedContentTokenCount: 3,
		ToolUsePromptTokenCount: 2,
	}})
	if usage.InputTokens != 10 || usage.OutputTokens != 7 || usage.ThinkingTokens != 5 || usage.TotalTokens != 22 || usage.CacheReadTokens != 3 {
		t.Fatalf("usage = %+v, want prompt/output/thought/total/cache tokens mapped", usage)
	}
	if usage.APIUsageRaw["responseTokenCount"] != int64(7) || usage.APIUsageRaw["toolUsePromptTokenCount"] != int64(2) {
		t.Fatalf("raw usage = %+v, want response/tool-use fields preserved", usage.APIUsageRaw)
	}
}

func TestGoogleInteractionsStreamEmitsThoughtSummaryDeltas(t *testing.T) {
	acc := newGoogleInteractionsStreamAccumulator("gemini-3.5-flash")
	events := make([]provideriface.StreamEvent, 0, 3)
	payloads := []string{
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"Plan:"}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":" inspect"}}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"Answer"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"opaque-sig"}}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_test","status":"completed","usage":{"total_input_tokens":2,"total_output_tokens":3,"total_thought_tokens":5,"total_tokens":10}}}`,
	}
	for _, payload := range payloads {
		if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
			t.Fatalf("applyPayload error: %v", err)
		}
	}
	response := acc.response()
	if response.Text != "Answer" {
		t.Fatalf("text = %q, want Answer", response.Text)
	}
	if response.ReasoningSummary != "Plan: inspect" {
		t.Fatalf("reasoning summary = %q, want thought summary only", response.ReasoningSummary)
	}
	if response.Usage.ThinkingTokens != 5 {
		t.Fatalf("usage = %+v, want thinking tokens", response.Usage)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want 3 rendered events", events)
	}
	if events[0].Type != provideriface.StreamEventReasoningSummaryDelta || events[0].Delta != "Plan:" || events[0].ReasoningKey != googleReasoningKey {
		t.Fatalf("first event = %+v, want reasoning snapshot", events[0])
	}
	if events[1].Type != provideriface.StreamEventReasoningSummaryDelta || events[1].Delta != "Plan: inspect" || events[1].ReasoningKey != googleReasoningKey {
		t.Fatalf("second event = %+v, want reasoning snapshot", events[1])
	}
	if events[2].Type != provideriface.StreamEventOutputTextDelta || events[2].Delta != "Answer" {
		t.Fatalf("third event = %+v, want output text", events[2])
	}
	rawJSON, _ := json.Marshal(response.Raw)
	if !strings.Contains(string(rawJSON), "opaque-sig") || strings.Contains(response.ReasoningSummary, "opaque-sig") {
		t.Fatalf("raw = %s reasoning = %q, want signature preserved out of reasoning text", rawJSON, response.ReasoningSummary)
	}
}

func TestGoogleInteractionsResponseParsesThoughtSummariesAndSignatures(t *testing.T) {
	response := parseGoogleInteractionResponse(googleInteractionResponse{
		ID:     "v1_test",
		Model:  "gemini-3.5-flash",
		Status: "completed",
		Steps: []googleInteractionStep{
			{Type: "thought", Signature: "sig-step", Summary: []googleInteractionContent{{Type: "text", Text: "inspect"}}},
			{Type: "model_output", Content: []googleInteractionContent{{Type: "text", Text: "answer"}}},
			{Type: "function_call", ID: "call_1", Name: "lookup", Arguments: map[string]any{"q": "x"}},
		},
	})
	if response.Text != "answer" || response.ReasoningSummary != "inspect" {
		t.Fatalf("response text/reasoning = %q/%q", response.Text, response.ReasoningSummary)
	}
	if len(response.FunctionCalls) != 1 || response.FunctionCalls[0].CallID != "call_1" || response.FunctionCalls[0].Arguments != `{"q":"x"}` {
		t.Fatalf("function calls = %+v", response.FunctionCalls)
	}
	rawJSON, _ := json.Marshal(response.Raw)
	if !strings.Contains(string(rawJSON), "sig-step") {
		t.Fatalf("raw = %s, want thought signature", rawJSON)
	}
}

func TestGoogleStreamEmitsReasoningSummaryDeltas(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-2.5-flash")
	events := make([]provideriface.StreamEvent, 0, 3)
	payloads := []string{
		`{"candidates":[{"content":{"parts":[{"text":"Plan:","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":" inspect","thought":true},{"text":"Answer"}]}}]}`,
	}
	for _, payload := range payloads {
		if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
			t.Fatalf("applyPayload error: %v", err)
		}
	}
	response := acc.response()
	if response.Text != "Answer" {
		t.Fatalf("text = %q, want Answer", response.Text)
	}
	if response.ReasoningSummary != "Plan: inspect" {
		t.Fatalf("reasoning summary = %q, want Plan: inspect", response.ReasoningSummary)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want 3 events", events)
	}
	if events[0].Type != provideriface.StreamEventReasoningSummaryDelta || events[0].Delta != "Plan:" || events[0].ReasoningKey != googleReasoningKey {
		t.Fatalf("first event = %+v, want reasoning snapshot", events[0])
	}
	if events[1].Type != provideriface.StreamEventReasoningSummaryDelta || events[1].Delta != "Plan: inspect" || events[1].ReasoningKey != googleReasoningKey {
		t.Fatalf("second event = %+v, want reasoning snapshot", events[1])
	}
	if events[2].Type != provideriface.StreamEventOutputTextDelta || events[2].Delta != "Answer" {
		t.Fatalf("third event = %+v, want output text", events[2])
	}
}

func TestGoogleStreamPreservesEmptyTextThoughtSignatureChunk(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-2.5-flash")
	events := make([]provideriface.StreamEvent, 0, 3)
	payloads := []string{
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig-empty"}]}}]}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}}}]}}]}`,
	}
	for _, payload := range payloads {
		if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
			t.Fatalf("applyPayload error: %v", err)
		}
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want tool call lifecycle", events)
	}
	metadataJSON, _ := json.Marshal(events[2].Metadata)
	if string(metadataJSON) == "null" || !strings.Contains(string(metadataJSON), "sig-empty") {
		t.Fatalf("completed metadata = %s, want empty-text chunk thought signature preserved", metadataJSON)
	}
}

func TestGoogleStreamEmitsToolCallConstructionLifecycle(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-3.5-flash")
	events := make([]provideriface.StreamEvent, 0, 3)
	payload := `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}},"thoughtSignature":"sig-123"}]}}]}`
	if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
		t.Fatalf("applyPayload error: %v", err)
	}
	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted,
	}
	gotTypes := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v (events=%+v)", gotTypes, wantTypes, events)
	}
	if events[0].ToolCallID != "call_weather" || events[0].ToolName != "weather" {
		t.Fatalf("started event = %+v, want call id/name", events[0])
	}
	if events[1].ArgumentsSnapshot != `{"city":"Paris"}` {
		t.Fatalf("snapshot arguments = %q, want JSON args", events[1].ArgumentsSnapshot)
	}
	if events[2].Arguments != `{"city":"Paris"}` {
		t.Fatalf("completed arguments = %q, want JSON args", events[2].Arguments)
	}
	metadataJSON, _ := json.Marshal(events[2].Metadata)
	if string(metadataJSON) == "null" || !strings.Contains(string(metadataJSON), "sig-123") {
		t.Fatalf("completed metadata = %s, want thought signature preserved", metadataJSON)
	}
}
