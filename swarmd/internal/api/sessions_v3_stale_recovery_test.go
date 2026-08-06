package api

import (
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionV3StaleRecoveryDecisionReasons(t *testing.T) {
	tests := []struct {
		name       string
		activity   *sessionV3RunActivity
		summary    pebblestore.SessionUsageSummary
		wantReason string
	}{
		{name: "active tool", activity: &sessionV3RunActivity{}, summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "codex_api_usage"}, wantReason: "tool_active"},
		{name: "ambiguous usage", activity: &sessionV3RunActivity{}, summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "estimated"}, wantReason: "usage_ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "active tool" {
				test.activity.toolActive.Store(true)
			}
			test.activity.lastProviderActivity.Store(time.Now().Add(-sessionV3StaleRecoveryMinInactivity - time.Second).UnixMilli())
			if test.name == "ambiguous usage" {
				if _, trusted := sessionV3TrustedContextUtilization(test.summary); trusted {
					t.Fatal("ambiguous usage was trusted")
				}
				return
			}
			if !test.activity.toolActive.Load() || test.wantReason != "tool_active" {
				t.Fatalf("activity=%+v wantReason=%s", test.activity, test.wantReason)
			}
		})
	}
}

func TestSessionV3TrustedContextUtilizationBounds(t *testing.T) {
	tests := []struct {
		name    string
		summary pebblestore.SessionUsageSummary
		want    float64
		trusted bool
	}{
		{name: "trusted 85 percent", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 850, Source: "codex_api_usage"}, want: 85, trusted: true},
		{name: "trusted 99 percent", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 990, Source: "anthropic_api_usage"}, want: 99, trusted: true},
		{name: "missing source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900}, trusted: false},
		{name: "untrusted source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "estimated"}, trusted: false},
		{name: "retired copilot source", summary: pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 900, Source: "copilot_session_usage"}, trusted: false},
		{name: "missing window", summary: pebblestore.SessionUsageSummary{TotalTokens: 900, Source: "codex_api_usage"}, trusted: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, trusted := sessionV3TrustedContextUtilization(test.summary)
			if trusted != test.trusted || got != test.want {
				t.Fatalf("utilization=%v trusted=%t, want %v/%t", got, trusted, test.want, test.trusted)
			}
		})
	}
}
