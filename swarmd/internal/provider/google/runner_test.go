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
