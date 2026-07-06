package openrouter

import (
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildChatCompletionRequestUsesLineageScopedSessionID(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{
		SessionID:          "durable-session",
		ProviderLineageID:  "provider-lineage",
		ProviderCacheKey:   "cache-lineage",
		SessionAffinityKey: "affinity-lineage",
		Model:              "openai/gpt-test",
		Input: []map[string]any{{
			"role":    "user",
			"content": "hello",
		}},
	})
	if payload.SessionID != "swarm-lineage-affinity-lineage" {
		t.Fatalf("session_id = %q, want lineage-scoped affinity key", payload.SessionID)
	}
	if payload.SessionID == "durable-session" {
		t.Fatalf("session_id used raw durable session id")
	}
}

func TestBuildChatCompletionRequestOmitsSessionIDWithoutLineage(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{
		SessionID: "durable-session",
		Model:     "openai/gpt-test",
		Input: []map[string]any{{
			"role":    "user",
			"content": "hello",
		}},
	})
	if payload.SessionID != "" {
		t.Fatalf("session_id = %q, want omitted without provider lineage", payload.SessionID)
	}
}

func TestBuildChatCompletionRequestMapsReasoningAndServiceTierFromCatalog(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{
		Model:       "openai/gpt-test",
		Thinking:    "xhigh",
		ServiceTier: "fast",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
				{SwarmSetting: "off", ProviderParameter: "reasoning.effort", ProviderValue: "none", EffectiveProviderValue: "none"},
				{SwarmSetting: "xhigh", ProviderParameter: "reasoning.effort", ProviderValue: "xhigh", EffectiveProviderValue: "xhigh"},
			},
			ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{
				{Tier: "priority", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority"},
			},
		},
		Input: []map[string]any{{
			"role":    "user",
			"content": "hello",
		}},
	})
	if !reflect.DeepEqual(payload.Reasoning, map[string]any{"effort": "xhigh"}) {
		t.Fatalf("reasoning = %#v, want effort xhigh", payload.Reasoning)
	}
	if payload.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority", payload.ServiceTier)
	}
}

func TestParseUsagePreservesOpenRouterDetails(t *testing.T) {
	usage := parseUsage(&chatCompletionUsage{
		PromptTokens:     194,
		CompletionTokens: 2,
		TotalTokens:      196,
		PromptTokensDetails: &chatPromptTokensDetails{
			CachedTokens:     30,
			CacheWriteTokens: 100,
			AudioTokens:      4,
		},
		CompletionTokensDetails: &chatCompletionTokensDetails{ReasoningTokens: 7},
		Cost:                    0.95,
		CostDetails:             map[string]any{"upstream_inference_cost": float64(19)},
		ServiceTier:             "priority",
	})
	if usage.InputTokens != 194 || usage.OutputTokens != 2 || usage.TotalTokens != 196 || usage.CacheReadTokens != 30 || usage.CacheWriteTokens != 100 || usage.ThinkingTokens != 7 {
		t.Fatalf("usage token mapping = %+v", usage)
	}
	if usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", usage.ServiceTier)
	}
	if usage.APIUsageRawPath != "usage" || usage.APIUsageRaw["cost"] != 0.95 {
		t.Fatalf("raw usage missing cost/path: %+v", usage.APIUsageRaw)
	}
	promptDetails, ok := usage.APIUsageRaw["prompt_tokens_details"].(map[string]any)
	if !ok || promptDetails["cached_tokens"] != int64(30) || promptDetails["cache_write_tokens"] != int64(100) {
		t.Fatalf("prompt token details = %#v", usage.APIUsageRaw["prompt_tokens_details"])
	}
	completionDetails, ok := usage.APIUsageRaw["completion_tokens_details"].(map[string]any)
	if !ok || completionDetails["reasoning_tokens"] != int64(7) {
		t.Fatalf("completion token details = %#v", usage.APIUsageRaw["completion_tokens_details"])
	}
}

func TestOpenRouterStreamingToolCallConstructionEvents(t *testing.T) {
	state := newOpenRouterToolCallConstructionState()
	events := make([]provideriface.StreamEvent, 0, 3)
	emitOpenRouterToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index: 0,
			ID:    "call_weather",
			Type:  "function",
			Function: &chatCompletionToolFunctionDelta{
				Name:      "get_weather",
				Arguments: "{\"location\":",
			},
		}}},
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })
	emitOpenRouterToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index:        0,
		FinishReason: "tool_calls",
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index:    0,
			Function: &chatCompletionToolFunctionDelta{Arguments: "\"Paris\"}"},
		}}},
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })
	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events len = %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d].Type = %s, want %s", i, events[i].Type, want)
		}
	}
	if events[0].ToolCallID != "call_weather" || events[0].ToolName != "get_weather" {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[3].Arguments != "{\"location\":\"Paris\"}" {
		t.Fatalf("completed args = %q", events[3].Arguments)
	}
}
