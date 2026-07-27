package codex

import (
	"encoding/json"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestToCodexRequestUsesSnapshotServiceTierAndThinkingMappings(t *testing.T) {
	req := toCodexRequest(provideriface.Request{
		Model:       "gpt-5.5",
		Thinking:    "high",
		ServiceTier: "fast",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider: "codex",
			Model:    "gpt-5.5",
			ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
				{SwarmSetting: "high", ProviderValue: "high", EffectiveProviderValue: "high"},
			},
			ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{
				{Tier: "fast", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority"},
			},
		},
	})

	if req.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want snapshot provider value priority", req.ServiceTier)
	}
	if req.ReasoningProviderValue != "high" {
		t.Fatalf("reasoning provider value = %q, want high", req.ReasoningProviderValue)
	}
}

func TestToCodexRequestFallsBackToReasoningEffortWhenMappingsMissing(t *testing.T) {
	req := toCodexRequest(provideriface.Request{
		Model:    "gpt-5.5",
		Thinking: "high",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider:                  "openai",
			Model:                     "gpt-5.5",
			Reasoning:                 true,
			ThinkingProviderParameter: "reasoning.effort",
		},
	})
	if req.ReasoningProviderValue != "high" {
		t.Fatalf("reasoning provider value = %q, want high", req.ReasoningProviderValue)
	}
	payload, err := buildRequestPayload(Request{
		Model:                  req.Model,
		ReasoningProviderValue: req.ReasoningProviderValue,
		Input:                  []map[string]any{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode request payload: %v", err)
	}
	reasoning, ok := decoded["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning payload = %#v, want high effort with auto summary in %s", decoded["reasoning"], string(payload))
	}
	include, ok := decoded["include"].([]any)
	if !ok || len(include) != 1 || include[0] != includeReasoningEncryptedContentPath {
		t.Fatalf("include = %#v, want reasoning encrypted content include in %s", decoded["include"], string(payload))
	}

	off := toCodexRequest(provideriface.Request{
		Model:    "gpt-5.5",
		Thinking: "off",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider:                  "openai",
			Model:                     "gpt-5.5",
			Reasoning:                 true,
			ThinkingProviderParameter: "reasoning_effort",
		},
	})
	if off.ReasoningProviderValue != "none" {
		t.Fatalf("off reasoning provider value = %q, want none", off.ReasoningProviderValue)
	}

	mediumNoParameter := toCodexRequest(provideriface.Request{
		Model:    "gpt-5.5",
		Thinking: "medium",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider:  "openai",
			Model:     "gpt-5.5",
			Reasoning: true,
		},
	})
	if mediumNoParameter.ReasoningProviderValue != "medium" {
		t.Fatalf("medium reasoning provider value without parameter = %q, want medium", mediumNoParameter.ReasoningProviderValue)
	}

	xhigh := toCodexRequest(provideriface.Request{
		Model:    "gpt-5.5",
		Thinking: "xhigh",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider:                  "openai",
			Model:                     "gpt-5.5",
			Reasoning:                 true,
			ThinkingProviderParameter: "reasoning.effort",
		},
	})
	if xhigh.ReasoningProviderValue != "xhigh" {
		t.Fatalf("xhigh reasoning provider value = %q, want xhigh", xhigh.ReasoningProviderValue)
	}

	xhighUnsupported := toCodexRequest(provideriface.Request{
		Model:    "gpt-5.1",
		Thinking: "xhigh",
		ModelCatalog: pebblestore.ModelCatalogRecord{
			Provider:                  "openai",
			Model:                     "gpt-5.1",
			Reasoning:                 true,
			ThinkingProviderParameter: "reasoning.effort",
		},
	})
	if xhighUnsupported.ReasoningProviderValue != "" {
		t.Fatalf("unsupported xhigh reasoning provider value = %q, want empty", xhighUnsupported.ReasoningProviderValue)
	}
}

func TestReasoningPayloadForRequestUsesSnapshotProviderValue(t *testing.T) {
	reasoning := reasoningPayloadForRequest(Request{Thinking: "xhigh", ReasoningProviderValue: "max"})
	if reasoning["effort"] != "max" {
		t.Fatalf("reasoning effort = %#v, want max", reasoning["effort"])
	}
	if reasoningPayloadForRequest(Request{Thinking: "off", ReasoningProviderValue: "none"}) != nil {
		t.Fatalf("off/none reasoning payload should be omitted")
	}
	if reasoningPayloadForRequest(Request{Thinking: "high"}) != nil {
		t.Fatalf("reasoning without snapshot provider value should be omitted")
	}
}
