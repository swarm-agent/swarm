package fireworks

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want omitted without snapshot mapping", req.ReasoningEffort)
	}

	req = buildChatCompletionRequest(provideriface.Request{
		Model:    "glm-5p2",
		Thinking: "xhigh",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider: "fireworks",
			Model:    "glm-5p2",
			ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
				{SwarmSetting: "off", ProviderValue: "none", EffectiveProviderValue: "none"},
				{SwarmSetting: "high", ProviderValue: "high", EffectiveProviderValue: "high"},
				{SwarmSetting: "xhigh", ProviderValue: "max", EffectiveProviderValue: "max"},
			},
		},
	})
	if req.ReasoningEffort != "max" {
		t.Fatalf("snapshot reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req = buildChatCompletionRequest(provideriface.Request{
		Model:    "glm-5p2",
		Thinking: "off",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider: "fireworks",
			Model:    "glm-5p2",
			ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
				{SwarmSetting: "off", ProviderValue: "none", EffectiveProviderValue: "none"},
			},
		},
	})
	if req.ReasoningEffort != "none" {
		t.Fatalf("snapshot disabled reasoning_effort = %q, want none", req.ReasoningEffort)
	}
}

func TestApplyServingResolutionUsesFireworksResourceNameAndPriorityTier(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{Model: "glm-5p2"})
	serving := ResolveServingTier(provideriface.Request{Model: "glm-5p2", ServiceTier: "priority"}, ServingConfig{
		ModelID:        "accounts/fireworks/models/glm-5p2",
		SupportedTiers: []string{"standard", "priority"},
		DefaultTier:    "standard",
		Tiers: map[string]ServingTier{
			"priority": {Tier: "priority", ProviderParameter: "service_tier", ProviderValue: "priority"},
		},
	})
	applyServingResolutionToPayload(&payload, serving)

	if payload.Model != "accounts/fireworks/models/glm-5p2" {
		t.Fatalf("model = %q, want Fireworks resource name", payload.Model)
	}
	if payload.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority", payload.ServiceTier)
	}
}

func TestResolveServingTierNormalizesBareFireworksModelWithoutCatalog(t *testing.T) {
	serving := ResolveServingTier(provideriface.Request{Model: "glm-5p2", ServiceTier: "priority"}, ServingConfig{})
	if serving.ModelID != "accounts/fireworks/models/glm-5p2" {
		t.Fatalf("model = %q, want Fireworks resource name", serving.ModelID)
	}
	if serving.ServiceTier != "" || serving.EffectiveTier != "standard" {
		t.Fatalf("priority without catalog resolution = %+v", serving)
	}
}

func TestServingConfigFromFastRouterCatalogKeepsRouterModelStandardOnly(t *testing.T) {
	catalog := pebblestore.ModelCatalogRecord{
		Provider:           "fireworks",
		Model:              "kimi-k2p6-fast",
		ServiceTiers:       []string{"standard"},
		DefaultServiceTier: "standard",
		ProviderSpecific:   []byte(`{"fireworks":{"resource_name":"accounts/fireworks/routers/kimi-k2p6-fast","serving":{"supported_tiers":["standard"],"default_tier":"standard","standard":{"tier":"standard","provider_parameter":null,"provider_value":null,"request_model_path":"accounts/fireworks/routers/kimi-k2p6-fast"},"priority":null,"fast":null}}}`),
	}
	serving := ResolveServingTier(provideriface.Request{Model: "kimi-k2p6-fast", ServiceTier: "priority", ModelCatalog: catalog}, ServingConfigFromCatalog(catalog))
	if serving.ModelID != "accounts/fireworks/routers/kimi-k2p6-fast" || serving.ServiceTier != "" || serving.EffectiveTier != "standard" {
		t.Fatalf("fast router catalog resolution = %+v", serving)
	}
}

func TestResolveServingTierUsesProviderNativeFireworksSemantics(t *testing.T) {
	cfg := ServingConfig{
		SupportedTiers: []string{"standard", "priority", "fast"},
		DefaultTier:    "standard",
		Tiers: map[string]ServingTier{
			"standard": {Tier: "standard", RequestModelPath: "accounts/fireworks/models/glm-5p1"},
			"priority": {Tier: "priority", ProviderParameter: "service_tier", ProviderValue: "priority", RequestModelPath: "accounts/fireworks/models/glm-5p1"},
			"fast":     {Tier: "fast", RequestModelPath: "accounts/fireworks/routers/glm-5p1-fast"},
		},
	}

	standard := ResolveServingTier(provideriface.Request{SessionID: "s1", SessionAffinityKey: "lineage-1", Model: "accounts/fireworks/models/glm-5p1"}, cfg)
	if standard.ModelID != "accounts/fireworks/models/glm-5p1" || standard.ServiceTier != "" || standard.EffectiveTier != "standard" {
		t.Fatalf("standard resolution = %+v", standard)
	}
	priority := ResolveServingTier(provideriface.Request{SessionID: "s1", SessionAffinityKey: "lineage-1", Model: "accounts/fireworks/models/glm-5p1", ServiceTier: "priority"}, cfg)
	if priority.ModelID != "accounts/fireworks/models/glm-5p1" || priority.ServiceTier != "priority" || priority.EffectiveTier != "priority" {
		t.Fatalf("priority resolution = %+v", priority)
	}
	fast := ResolveServingTier(provideriface.Request{SessionID: "s1", SessionAffinityKey: "lineage-1", Model: "accounts/fireworks/models/glm-5p1", ServiceTier: "fast"}, cfg)
	if fast.ModelID != "accounts/fireworks/routers/glm-5p1-fast" || fast.ServiceTier != "" || fast.EffectiveTier != "fast" {
		t.Fatalf("fast resolution = %+v", fast)
	}
	if fast.SessionAffinity == "" {
		t.Fatalf("fast session affinity was empty")
	}
	if fast.SessionAffinity == stableSessionAffinity("s1") {
		t.Fatalf("fast session affinity used raw durable session id: %q", fast.SessionAffinity)
	}
}

func TestResolveServingTierUsesLineageScopedCacheKeys(t *testing.T) {
	first := ResolveServingTier(provideriface.Request{SessionID: "durable-session", SessionAffinityKey: "lineage-a", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	second := ResolveServingTier(provideriface.Request{SessionID: "durable-session", SessionAffinityKey: "lineage-b", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	if first.SessionAffinity == "" || first.PromptCacheIsolationKey == "" {
		t.Fatalf("lineage scoped keys missing: %+v", first)
	}
	if first.SessionAffinity != first.PromptCacheIsolationKey {
		t.Fatalf("session affinity and prompt cache isolation differ within lineage: %+v", first)
	}
	if first.SessionAffinity == second.SessionAffinity || first.PromptCacheIsolationKey == second.PromptCacheIsolationKey {
		t.Fatalf("different lineages reused cache keys: first=%+v second=%+v", first, second)
	}
	if first.SessionAffinity == stableSessionAffinity("durable-session") {
		t.Fatalf("session affinity used raw durable session id: %q", first.SessionAffinity)
	}
	stable := ResolveServingTier(provideriface.Request{SessionID: "durable-session", SessionAffinityKey: "lineage-a", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	if stable.SessionAffinity != first.SessionAffinity || stable.PromptCacheIsolationKey != first.PromptCacheIsolationKey {
		t.Fatalf("same lineage did not keep stable cache keys: first=%+v stable=%+v", first, stable)
	}
	checkpoint := ResolveServingTier(provideriface.Request{SessionID: "durable-session", SessionAffinityKey: "checkpoint-lineage", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	if checkpoint.SessionAffinity == first.SessionAffinity || checkpoint.PromptCacheIsolationKey == first.PromptCacheIsolationKey {
		t.Fatalf("checkpoint boundary reused previous lineage cache keys: first=%+v checkpoint=%+v", first, checkpoint)
	}
}

func TestRequestHeadersIncludesPromptCacheIsolationKey(t *testing.T) {
	headers := requestHeaders(requestOptions{SessionAffinity: "affinity-lineage", PromptCacheIsolationKey: "cache-lineage"})
	if got := headers["x-session-affinity"]; got != "affinity-lineage" {
		t.Fatalf("x-session-affinity = %q, want affinity-lineage", got)
	}
	if got := headers["x-prompt-cache-isolation-key"]; got != "cache-lineage" {
		t.Fatalf("x-prompt-cache-isolation-key = %q, want cache-lineage", got)
	}
}

func TestParseUsageCapturesCachedTokensAndCost(t *testing.T) {
	usage := parseUsage(&chatCompletionUsage{
		PromptTokens:        1000,
		CompletionTokens:    200,
		TotalTokens:         1200,
		PromptTokensDetails: &chatPromptTokensDetails{CachedTokens: 300},
	})
	serving := requestServingResolution{EffectiveTier: "priority", ModelID: "accounts/fireworks/models/glm-5p1", ServingTier: ServingTier{Tier: "priority", Pricing: ServingTierPricing{UncachedInputPerMillion: 2.1, CachedInputPerMillion: 0.39, OutputPerMillion: 6.6}}}
	annotateUsage(&usage, serving)
	if usage.InputTokens != 1000 || usage.CacheReadTokens != 300 || usage.OutputTokens != 200 || usage.TotalTokens != 1200 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.APIUsageRaw["uncached_prompt_tokens"] != int64(700) {
		t.Fatalf("uncached prompt tokens raw = %#v", usage.APIUsageRaw["uncached_prompt_tokens"])
	}
	if usage.ServiceTier != "priority" || usage.APIUsageRaw["service_tier"] != "priority" || usage.APIUsageRaw["provider_model"] != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("usage annotations = usage=%+v raw=%#v", usage, usage.APIUsageRaw)
	}
	if usage.EstimatedCostUSD <= 0 || usage.APIUsageRaw["estimated_cost_usd"] == nil {
		t.Fatalf("estimated cost missing from usage: usage=%+v raw=%#v", usage, usage.APIUsageRaw)
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

func TestCreateChatCompletionStreamRequestsUsage(t *testing.T) {
	payload := chatCompletionRequest{Model: "accounts/fireworks/models/kimi-k2p6"}
	ensureChatCompletionStreamOptions(&payload)
	if !payload.Stream {
		t.Fatalf("stream = false, want true")
	}
	if payload.StreamOptions == nil || !payload.StreamOptions.IncludeUsage {
		t.Fatalf("stream options = %+v, want include_usage", payload.StreamOptions)
	}
}
