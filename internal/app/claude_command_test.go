package app

import (
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

// TestClaudeUsageLinesShowPlanWindowsAndAge: /claude must say whether Claude
// Code can run, the plan, each usage window as a percentage with its reset, and
// how old the reading is; before any turn it must say there is no reading yet
// instead of showing zeros.
func TestClaudeUsageLinesShowPlanWindowsAndAge(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)
	ready := client.ClaudeCodeUsageCLI{Ready: true, Version: "2.1.296", LoggedIn: true, AuthMethod: "claude.ai", SubscriptionType: "max"}

	none := strings.Join(claudeUsageLines(client.ClaudeCodeUsage{CLI: ready}, now), "\n")
	if !strings.Contains(none, "Claude Code 2.1.296 · signed in (claude.ai) · plan max") || !strings.Contains(none, "no reading yet") {
		t.Fatalf("before a turn:\n%s", none)
	}

	limits := &client.ClaudeCodeRateLimits{Status: "allowed", OverageStatus: "rejected", ObservedAt: now.Add(-3 * time.Minute), Windows: []client.ClaudeCodeUsageWindow{
		{Name: "five_hour", Utilization: 0.18, ResetsAt: now.Add(2 * time.Hour).Unix()},
		{Name: "seven_day", Utilization: 0.004, ResetsAt: now.Add(72 * time.Hour).Unix()},
	}}
	got := strings.Join(claudeUsageLines(client.ClaudeCodeUsage{CLI: ready, RateLimits: limits}, now), "\n")
	for _, want := range []string{"5-hour     18% used · resets 14:00", "7-day       0% used · resets ", "Status allowed · extra usage rejected · as of 3 min ago"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	notReady := strings.Join(claudeUsageLines(client.ClaudeCodeUsage{CLI: client.ClaudeCodeUsageCLI{Reason: "Claude Code is not signed in"}}, now), "\n")
	if !strings.Contains(notReady, "not ready: Claude Code is not signed in") {
		t.Fatalf("not ready:\n%s", notReady)
	}
}
