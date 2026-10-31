package api

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the V3 executor writes one usage record per provider step. For a
// provider that reports each request (Claude Code runs its own tool loop in
// one step), that record must bill the sum of its request receipts while its
// context occupancy stays the last request; otherwise remaining-context and
// plan-guard readings would see the sum and daily totals would see the last
// request only. sessionV3ProviderUsageRecord is the executor's single record
// constructor, so this pure boundary is the narrowest layer. Negative case:
// untagged history (every other provider) keeps the reported counters.
func TestSessionV3ProviderUsageRecordSumsRequestReceipts(t *testing.T) {
	usage := provideriface.TokenUsage{Source: "claudecode_cli_usage", APIUsageRawPath: "usage", InputTokens: 15, CacheReadTokens: 2200, CacheWriteTokens: 200, OutputTokens: 350, TotalTokens: 2765}
	for _, request := range []map[string]any{
		{"account_id": "slot-a", "input_tokens": int64(10), "cache_read_tokens": int64(1000), "cache_write_tokens": int64(200), "output_tokens": int64(50)},
		{"account_id": "slot-a", "input_tokens": int64(5), "cache_read_tokens": int64(1200), "output_tokens": int64(300)},
	} {
		usage.APIUsageHistory = append(usage.APIUsageHistory, map[string]any{pebblestore.UsageRequestHistoryKey: request})
	}
	record, ok := sessionV3ProviderUsageRecord("claudecode", "claude-test", 200000, "run-1", 1, usage)
	if !ok {
		t.Fatal("claudecode usage was not tracked")
	}
	if !record.BilledUsagePresent || record.BilledTokens != 2765 || record.RequestCount != 2 || record.Requests[1].Step != 1 || record.Requests[0].AccountID != "slot-a" {
		t.Fatalf("billing = present %v tokens %d requests %d", record.BilledUsagePresent, record.BilledTokens, record.RequestCount)
	}
	if record.TotalTokens != 1505 || record.FirstRequestContextTokens != 1010 {
		t.Fatalf("occupancy %d first-request context %d", record.TotalTokens, record.FirstRequestContextTokens)
	}

	plain := provideriface.TokenUsage{Source: "anthropic_api_usage", APIUsageRawPath: "usage", InputTokens: 7, OutputTokens: 3, TotalTokens: 10, APIUsageHistory: []map[string]any{{"input_tokens": 7}}}
	legacy, ok := sessionV3ProviderUsageRecord("anthropic", "claude-test", 200000, "run-2", 1, plain)
	if !ok || legacy.BilledUsagePresent || legacy.RequestCount != 0 || legacy.TotalTokens != 10 {
		t.Fatalf("untagged usage changed: %+v", legacy)
	}
}
