package codex

import "testing"

func TestExtractTokenUsageAcceptsOnlyResponseUsage(t *testing.T) {
	usageObj := map[string]any{
		"input_tokens":  float64(252648),
		"output_tokens": float64(96),
		"total_tokens":  float64(252744),
		"input_tokens_details": map[string]any{
			"cached_tokens": float64(248320),
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(64),
		},
	}
	usage := extractTokenUsage(nil, map[string]any{
		codexTransportMetadataKey:      "websocket",
		codexConnectedViaWSMetadataKey: true,
		"response": map[string]any{
			"usage": usageObj,
		},
	})
	if usage.Source != "codex_api_usage" || usage.APIUsageRawPath != "response.usage" {
		t.Fatalf("usage identity = %+v", usage)
	}
	if usage.InputTokens != 252648 || usage.OutputTokens != 96 || usage.TotalTokens != 252744 || usage.CacheReadTokens != 248320 || usage.ThinkingTokens != 64 {
		t.Fatalf("usage tokens = %+v", usage)
	}
	if usage.Transport != "websocket" || usage.ConnectedViaWS == nil || !*usage.ConnectedViaWS {
		t.Fatalf("usage transport = %+v", usage)
	}
}

func TestExtractTokenUsageRejectsTopLevelUsageFallback(t *testing.T) {
	usage := extractTokenUsage(nil, map[string]any{
		"usage": map[string]any{
			"input_tokens": float64(10),
			"total_tokens": float64(10),
		},
	})
	if usage.TotalTokens != 0 || usage.Source != "" || usage.APIUsageRawPath != "" {
		t.Fatalf("top-level usage fallback was accepted: %+v", usage)
	}
}
