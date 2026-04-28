package ui

import (
	"strings"
	"testing"
)

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
	if got != "wt on  75% left" {
		t.Fatalf("footerRightLine = %q, want only agentic chat metadata", got)
	}
	if strings.Contains(got, "v0.4") || strings.Contains(got, "update") {
		t.Fatalf("footerRightLine = %q, did not expect version/update metadata", got)
	}
}
