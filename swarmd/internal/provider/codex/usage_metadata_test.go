package codex

import "testing"

func TestParseResponsePreservesCodexUsageMetadata(t *testing.T) {
	response := parseResponse(map[string]any{
		"id":    "resp_usage_metadata",
		"model": "gpt-5.5",
		"usage": map[string]any{
			"input_tokens":       float64(100),
			"output_tokens":      float64(7),
			"total_tokens":       float64(107),
			"service_tier":       "priority",
			"estimated_cost_usd": float64(0.0123),
		},
	})

	if response.Usage.TotalTokens != 107 || response.Usage.InputTokens != 100 || response.Usage.OutputTokens != 7 {
		t.Fatalf("usage tokens = %+v", response.Usage)
	}
	if response.Usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", response.Usage.ServiceTier)
	}
	if response.Usage.EstimatedCostUSD != 0.0123 {
		t.Fatalf("estimated cost = %v, want 0.0123", response.Usage.EstimatedCostUSD)
	}
	if response.Usage.APIUsageRawPath != "response.usage" || response.Usage.APIUsageRaw["service_tier"] != "priority" {
		t.Fatalf("raw usage not preserved: path=%q raw=%#v", response.Usage.APIUsageRawPath, response.Usage.APIUsageRaw)
	}
}
