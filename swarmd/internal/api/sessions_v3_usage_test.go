package api

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestSessionV3ProviderUsageRecordRequiresNormalizedTotalTokens(t *testing.T) {
	if _, ok := sessionV3ProviderUsageRecord("codex", "gpt-5.5", 272000, "run-no-total", provideriface.TokenUsage{
		InputTokens:     252648,
		OutputTokens:    96,
		CacheReadTokens: 248320,
		Source:          "codex_api_usage",
		Transport:       "websocket",
	}); ok {
		t.Fatal("usage without normalized total_tokens must not update frontend usage summary")
	}

	record, ok := sessionV3ProviderUsageRecord("codex", "gpt-5.5", 272000, "run-total", provideriface.TokenUsage{
		InputTokens:     252648,
		OutputTokens:    96,
		CacheReadTokens: 248320,
		TotalTokens:     252744,
		Source:          "codex_api_usage",
		Transport:       "websocket",
	})
	if !ok {
		t.Fatal("usage with normalized total_tokens was not recorded")
	}
	if record.TotalTokens != 252744 || record.InputTokens != 252648 || record.OutputTokens != 96 || record.CacheReadTokens != 248320 {
		t.Fatalf("record = %+v, want exact normalized provider usage fields", record)
	}
}
