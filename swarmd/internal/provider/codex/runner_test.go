package codex

import (
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
