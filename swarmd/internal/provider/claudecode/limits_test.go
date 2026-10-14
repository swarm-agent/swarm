package claudecode

import (
	"encoding/json"
	"testing"
	"time"
)

// A rate_limit_event line as Claude Code 2.1.296 writes it in stream-json.
const rateLimitEventLine = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791810000,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"seven_day":{"utilization":0,"resetsAt":1792411200},"five_hour":{"utilization":0.18,"resetsAt":1791810000}}},"uuid":"u","session_id":"s"}`

// TestTurnStreamRecordsTheLatestRateLimits: /claude shows the subscription
// usage Claude Code reports in a turn's stream; the runner must hand each
// rate_limit_event to the CLI, which keeps the latest reading with sorted
// windows, and ignores events without a status.
func TestTurnStreamRecordsTheLatestRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	cli := &CLI{now: func() time.Time { return now }}
	if _, ok := cli.LatestRateLimits(); ok {
		t.Fatal("no reading before a turn")
	}
	state := newTurnState("claude-sonnet-4-6", newToolUseCorrelator())
	state.onRateLimits = cli.RecordRateLimits
	state.handleLine([]byte(rateLimitEventLine), nil)

	got, ok := cli.LatestRateLimits()
	if !ok {
		t.Fatal("the reading was not recorded")
	}
	if got.Status != "allowed" || got.LimitType != "five_hour" || got.OverageStatus != "rejected" || got.IsUsingOverage || !got.ObservedAt.Equal(now) {
		t.Fatalf("unexpected reading %+v", got)
	}
	if len(got.Windows) != 2 || got.Windows[0].Name != "five_hour" || got.Windows[0].Utilization != 0.18 || got.Windows[0].ResetsAt != 1791810000 || got.Windows[1].Name != "seven_day" {
		t.Fatalf("unexpected windows %+v", got.Windows)
	}

	cli.RecordRateLimits(json.RawMessage(`{"unifiedWindows":{}}`))
	if again, _ := cli.LatestRateLimits(); again.Status != "allowed" {
		t.Fatal("an event without a status must not replace the reading")
	}
}

// TestAuthStatusReadsTheSubscriptionType: /claude names the plan, read from
// `claude auth status`, without any account identifiers.
func TestAuthStatusReadsTheSubscriptionType(t *testing.T) {
	status, err := parseAuthStatus([]byte(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"x@example.com","orgId":"o"}`))
	if err != nil || status.SubscriptionType != "max" || !status.LoggedIn {
		t.Fatalf("parseAuthStatus = %+v, %v", status, err)
	}
}
