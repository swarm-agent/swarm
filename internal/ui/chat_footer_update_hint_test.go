package ui

import (
	"strings"
	"testing"
)

func TestChatFooterInitialUsageSummaryPopulatesRightLine(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		InitialUsageSummary: &ChatUsageSummary{
			ContextWindow:   1000,
			TotalTokens:     250,
			CacheReadTokens: 0,
			RemainingTokens: 750,
		},
	})

	if got := p.footerRightLine(1000); got != "75% left" {
		t.Fatalf("footerRightLine = %q, want hydrated context usage", got)
	}
}

func TestChatFooterUsageUpdatedEventUpdatesRightLine(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
	})

	p.applyRunStreamEvent(ChatRunStreamEvent{
		Type: "usage.updated",
		UsageSummary: &ChatUsageSummary{
			ContextWindow:   1000,
			TotalTokens:     500,
			RemainingTokens: 500,
		},
	}, 123)

	if got := p.footerRightLine(1000); got != "50% left" {
		t.Fatalf("footerRightLine = %q, want updated context usage", got)
	}
}

func TestChatFooterRightLineHidesVersionAndUpdateHint(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Version:           "v0.4.0",
			UpdateVersionHint: "v0.4.1",
			WorktreeEnabled:   true,
		},
	})
	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     250,
		CacheReadTokens: 0,
		RemainingTokens: 750,
	})

	got := p.footerRightLine(1000)
	if got != "75% left" {
		t.Fatalf("footerRightLine = %q, want only context usage metadata", got)
	}
	if strings.Contains(got, "v0.4") || strings.Contains(got, "update") {
		t.Fatalf("footerRightLine = %q, did not expect version/update metadata", got)
	}
}
