package fireworks

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	toolruntime "swarm/packages/swarmd/internal/tool"
)

func TestFireworksDebugChunkMetadataExcludesPrivateContent(t *testing.T) {
	const sentinel = "SEC08_PRIVATE_FIREWORKS_SENTINEL"
	metadata := fireworksDebugChunkMetadata("session-correlation", "model-category", chatCompletionChunk{
		Choices: []chatCompletionChoice{{
			Delta: &chatCompletionMessageDelta{
				Content:          sentinel,
				ReasoningContent: sentinel,
				ToolCalls: []chatCompletionToolCallDelta{{
					Function: &chatCompletionToolFunctionDelta{Arguments: sentinel},
				}},
			},
			FinishReason: "stop",
		}},
	})
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal debug metadata: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("Fireworks debug metadata exposed private sentinel: %s", encoded)
	}
	if metadata["choice_count"] != 1 || metadata["tool_call_count"] != 1 || metadata["finished_choices"] != 1 {
		t.Fatalf("Fireworks debug metadata = %#v, want bounded counts", metadata)
	}
}

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

func TestBuildChatCompletionRequestPreservesFunctionOutputToolName(t *testing.T) {
	request, err := buildChatCompletionRequest(provideriface.Request{
		Model: "test-model",
		Input: []map[string]any{
			{"type": "function_call", "call_id": "call_task", "name": "task", "arguments": `{}`},
			{"type": "function_call_output", "call_id": "call_task", "name": "task", "output": "completed"},
		},
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if len(request.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(request.Messages))
	}
	toolMessage := request.Messages[1]
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_task" || toolMessage["name"] != "task" || toolMessage["content"] != "completed" {
		t.Fatalf("tool message = %#v, want named task result", toolMessage)
	}
}

func TestBuildChatCompletionRequestNormalizesPlanCheckpointCompositionBranches(t *testing.T) {
	definitions := toolruntime.NewRuntime(1).Definitions()
	planTools := make([]provideriface.ToolDefinition, 0, 2)
	canonicalParameters := make(map[string][]byte, 2)
	for _, definition := range definitions {
		if definition.Name != "exit_plan_mode" && definition.Name != "plan_manage" {
			continue
		}
		if !hasFireworksRequiredOnlyCompositionBranch(definition.Parameters) {
			t.Fatalf("canonical %s schema no longer reproduces a required-only composition branch", definition.Name)
		}
		encoded, err := json.Marshal(definition.Parameters)
		if err != nil {
			t.Fatalf("marshal canonical %s parameters: %v", definition.Name, err)
		}
		canonicalParameters[definition.Name] = encoded
		planTools = append(planTools, provideriface.ToolDefinition{
			Type: definition.Type, Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
		})
	}
	if len(planTools) != 2 {
		t.Fatalf("found %d plan tools, want exit_plan_mode and plan_manage", len(planTools))
	}

	request, err := buildChatCompletionRequest(provideriface.Request{
		Model: "test-model",
		Input: []map[string]any{{"role": "user", "content": "plan the change"}},
		Tools: planTools,
	})
	if err != nil {
		t.Fatalf("build Fireworks request with plan tools: %v", err)
	}
	checkpointRequired := []string{"id", "title", "status", "order", "acceptance_criteria"}
	for _, tool := range request.Tools {
		assertFireworksCompositionRequiredSchemasHaveProperties(t, tool.Function.Parameters, "$.tools."+tool.Function.Name)
		if !hasFireworksRequiredAlternatives(tool.Function.Parameters, "objective", "tasks") {
			t.Fatalf("serialized %s parameters lost the checkpoint objective-or-tasks requirement", tool.Function.Name)
		}
		if !hasFireworksAlternativeRequiringAll(tool.Function.Parameters, append(checkpointRequired, "objective")...) {
			t.Fatalf("serialized %s parameters lost complete checkpoint fields from its objective alternative", tool.Function.Name)
		}
		if !hasFireworksAlternativeRequiringAll(tool.Function.Parameters, append(checkpointRequired, "tasks")...) {
			t.Fatalf("serialized %s parameters lost complete checkpoint fields from its tasks alternative", tool.Function.Name)
		}
		if !hasFireworksObjectPropertyWithFields(tool.Function.Parameters, "acceptance_criteria", "type", "minItems", "items") {
			t.Fatalf("serialized %s parameters lost the checkpoint acceptance_criteria schema", tool.Function.Name)
		}
		if tool.Function.Name == "plan_manage" && !hasFireworksObjectPropertyWithFields(tool.Function.Parameters, "subtask", "type", "properties") {
			t.Fatalf("serialized plan_manage parameters lost the nested subtask schema")
		}
	}
	for _, definition := range planTools {
		after, err := json.Marshal(definition.Parameters)
		if err != nil {
			t.Fatalf("marshal canonical %s parameters after request: %v", definition.Name, err)
		}
		if !reflect.DeepEqual(after, canonicalParameters[definition.Name]) {
			t.Fatalf("Fireworks serialization mutated canonical %s parameters", definition.Name)
		}
	}
}

func hasFireworksRequiredOnlyCompositionBranch(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if required := sanitizeFireworksRequired(typed["required"]); len(required) > 0 {
			if _, hasProperties := typed["properties"]; !hasProperties {
				return true
			}
		}
		for _, child := range typed {
			if hasFireworksRequiredOnlyCompositionBranch(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasFireworksRequiredOnlyCompositionBranch(child) {
				return true
			}
		}
	case []map[string]any:
		for _, child := range typed {
			if hasFireworksRequiredOnlyCompositionBranch(child) {
				return true
			}
		}
	}
	return false
}

func hasFireworksRequiredAlternatives(value any, requiredNames ...string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			alternatives, ok := typed[key].([]any)
			if !ok {
				continue
			}
			matched := make(map[string]bool, len(requiredNames))
			for _, alternative := range alternatives {
				schema, ok := alternative.(map[string]any)
				if !ok || schema["type"] != "object" {
					continue
				}
				properties, _ := schema["properties"].(map[string]any)
				for _, name := range sanitizeFireworksRequired(schema["required"]) {
					if _, ok := properties[name]; ok {
						matched[name] = true
					}
				}
			}
			allMatched := true
			for _, name := range requiredNames {
				allMatched = allMatched && matched[name]
			}
			if allMatched {
				return true
			}
		}
		for _, child := range typed {
			if hasFireworksRequiredAlternatives(child, requiredNames...) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasFireworksRequiredAlternatives(child, requiredNames...) {
				return true
			}
		}
	}
	return false
}

func hasFireworksAlternativeRequiringAll(value any, requiredNames ...string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			alternatives, ok := typed[key].([]any)
			if !ok {
				continue
			}
			for _, alternative := range alternatives {
				schema, ok := alternative.(map[string]any)
				if !ok {
					continue
				}
				required := make(map[string]bool, len(requiredNames))
				for _, name := range sanitizeFireworksRequired(schema["required"]) {
					required[name] = true
				}
				allRequired := true
				for _, name := range requiredNames {
					allRequired = allRequired && required[name]
				}
				if allRequired {
					return true
				}
			}
		}
		for _, child := range typed {
			if hasFireworksAlternativeRequiringAll(child, requiredNames...) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasFireworksAlternativeRequiringAll(child, requiredNames...) {
				return true
			}
		}
	}
	return false
}

func hasFireworksObjectPropertyWithFields(value any, propertyName string, fieldNames ...string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if properties, ok := typed["properties"].(map[string]any); ok {
			if property, ok := properties[propertyName].(map[string]any); ok {
				allPresent := true
				for _, fieldName := range fieldNames {
					_, present := property[fieldName]
					allPresent = allPresent && present
				}
				if allPresent {
					return true
				}
			}
		}
		for _, child := range typed {
			if hasFireworksObjectPropertyWithFields(child, propertyName, fieldNames...) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasFireworksObjectPropertyWithFields(child, propertyName, fieldNames...) {
				return true
			}
		}
	}
	return false
}

func assertFireworksCompositionRequiredSchemasHaveProperties(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if required := sanitizeFireworksRequired(typed["required"]); len(required) > 0 {
			if typed["type"] != "object" {
				t.Fatalf("%s required schema type = %#v, want object", path, typed["type"])
			}
			properties, ok := typed["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s required schema properties = %T, want object", path, typed["properties"])
			}
			for _, name := range required {
				if _, ok := properties[name]; !ok {
					t.Fatalf("%s requires %q without defining it in properties", path, name)
				}
			}
		}
		for key, child := range typed {
			assertFireworksCompositionRequiredSchemasHaveProperties(t, child, path+"."+key)
		}
	case []any:
		for _, child := range typed {
			assertFireworksCompositionRequiredSchemasHaveProperties(t, child, path+"[]")
		}
	}
}

func TestFireworksExecutionEpochRequestContainsOnlyExplicitInput(t *testing.T) {
	payload, err := buildChatCompletionRequest(provideriface.Request{
		Model:        "accounts/fireworks/models/test",
		Instructions: "current instructions",
		Input: []map[string]any{
			{"role": "user", "content": "epoch user"},
			{"role": "assistant", "content": "epoch assistant"},
			{"role": "user", "content": "epoch follow-up"},
		},
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := string(encoded)
	for _, text := range []string{"current instructions", "epoch user", "epoch assistant", "epoch follow-up"} {
		if !strings.Contains(body, text) {
			t.Fatalf("payload missing %q: %s", text, body)
		}
	}
	for _, forbidden := range []string{"previous_response_id", "conversation", "predecessor"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("payload contains stateful continuation field %q: %s", forbidden, body)
		}
	}
}

func TestBuildChatCompletionRequestTaskToolHasNoNestedCombinators(t *testing.T) {
	req, err := buildChatCompletionRequest(provideriface.Request{
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
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
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
	req, err := buildChatCompletionRequest(provideriface.Request{
		Model:    "test-model",
		Thinking: "high",
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want omitted without snapshot mapping", req.ReasoningEffort)
	}

	req, err = buildChatCompletionRequest(provideriface.Request{
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
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("snapshot reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req, err = buildChatCompletionRequest(provideriface.Request{
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
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.ReasoningEffort != "none" {
		t.Fatalf("snapshot disabled reasoning_effort = %q, want none", req.ReasoningEffort)
	}
}

func TestApplyServingResolutionUsesFireworksResourceNameAndPriorityTier(t *testing.T) {
	payload, err := buildChatCompletionRequest(provideriface.Request{Model: "glm-5p2"})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
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
	lifecycle := (&Runner{}).ExecutionEpochLifecycle()
	if lifecycle.ContextMode != provideriface.ExecutionEpochContextStatelessFullInput || !lifecycle.EpochScopedCacheKey || !lifecycle.EpochScopedSessionAffinity || !lifecycle.Valid() {
		t.Fatalf("lifecycle = %+v, want valid stateless epoch-scoped cache and affinity", lifecycle)
	}
	first := ResolveServingTier(provideriface.Request{SessionID: "durable-session", ExecutionEpochID: "epoch-a", SessionAffinityKey: "epoch-a-lineage", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	second := ResolveServingTier(provideriface.Request{SessionID: "durable-session", ExecutionEpochID: "epoch-b", SessionAffinityKey: "epoch-b-lineage", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
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
	stable := ResolveServingTier(provideriface.Request{SessionID: "durable-session", ExecutionEpochID: "epoch-a", SessionAffinityKey: "epoch-a-lineage", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
	if stable.SessionAffinity != first.SessionAffinity || stable.PromptCacheIsolationKey != first.PromptCacheIsolationKey {
		t.Fatalf("same lineage did not keep stable cache keys: first=%+v stable=%+v", first, stable)
	}
	checkpoint := ResolveServingTier(provideriface.Request{SessionID: "durable-session", ExecutionEpochID: "epoch-c", SessionAffinityKey: "epoch-c-lineage", Model: "accounts/fireworks/models/glm-5p1"}, ServingConfig{})
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
	if usage.ServiceTier != "" || usage.APIUsageRaw["service_tier"] != nil || usage.APIUsageRaw["provider_model"] != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("usage annotations must not claim an unreturned served tier: usage=%+v raw=%#v", usage, usage.APIUsageRaw)
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

func TestFireworksReasoningSnapshotsDeclareReplaceMode(t *testing.T) {
	var events []provideriface.StreamEvent
	emitFireworksReasoningSnapshot(func(event provideriface.StreamEvent) { events = append(events, event) }, fireworksReasoningKey(0), "Plan: inspect")
	if len(events) != 1 {
		t.Fatalf("reasoning events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Delta != "Plan: inspect" || event.DeltaMode != provideriface.StreamEventDeltaModeReplace || event.ReasoningKey != "fireworks-reasoning" {
		t.Fatalf("reasoning event contract = %#v", event)
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
