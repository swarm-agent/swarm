package api

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestSessionV3ProviderUsageRecordRequiresNormalizedTotalTokens(t *testing.T) {
	if _, ok := sessionV3ProviderUsageRecord("codex", "gpt-5.5", 272000, "run-no-total", 1, provideriface.TokenUsage{
		InputTokens:     252648,
		OutputTokens:    96,
		CacheReadTokens: 248320,
		Source:          "codex_api_usage",
		Transport:       "websocket",
		APIUsageRawPath: "response.usage",
	}); ok {
		t.Fatal("usage without normalized total_tokens must not update frontend usage summary")
	}

	record, ok := sessionV3ProviderUsageRecord("codex", "gpt-5.5", 272000, "run-total", 3, provideriface.TokenUsage{
		InputTokens:     252648,
		OutputTokens:    96,
		CacheReadTokens: 248320,
		TotalTokens:     252744,
		Source:          "codex_api_usage",
		Transport:       "websocket",
		APIUsageRawPath: "response.usage",
		APIUsageRaw:     map[string]any{"service_tier": "priority"},
	})
	if !ok {
		t.Fatal("usage with normalized total_tokens was not recorded")
	}
	if record.TotalTokens != 252744 || record.InputTokens != 252648 || record.OutputTokens != 96 || record.CacheReadTokens != 248320 {
		t.Fatalf("record = %+v, want exact normalized provider usage fields", record)
	}
	if record.APIUsageRawPath != "response.usage" || record.APIUsageRaw["service_tier"] != "priority" {
		t.Fatalf("raw usage not preserved: path=%q raw=%#v", record.APIUsageRawPath, record.APIUsageRaw)
	}
	if record.Steps != 3 {
		t.Fatalf("record step = %d, want provider loop step 3", record.Steps)
	}
}

func TestSessionV3ProviderUsageRecordRejectsNonCanonicalCodexUsage(t *testing.T) {
	if _, ok := sessionV3ProviderUsageRecord("codex", "gpt-5.5", 272000, "run-top-level", 1, provideriface.TokenUsage{
		InputTokens:     100,
		OutputTokens:    2,
		TotalTokens:     102,
		Source:          "codex_api_usage",
		APIUsageRawPath: "usage",
	}); ok {
		t.Fatal("codex top-level usage fallback must not update frontend usage summary")
	}
}

func TestSessionV3ProviderUsageRecordRejectsUnknownProvider(t *testing.T) {
	if _, ok := sessionV3ProviderUsageRecord("acme-ai", "acme-model", 1234, "run-any-provider", 1, provideriface.TokenUsage{
		InputTokens:     10,
		OutputTokens:    5,
		TotalTokens:     15,
		Source:          "acme_api_usage",
		APIUsageRawPath: "usage",
	}); ok {
		t.Fatal("generic provider usage must not be tracked without a provider-specific source/path case")
	}
}
