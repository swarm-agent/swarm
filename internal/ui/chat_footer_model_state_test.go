package ui

import (
	"strings"
	"testing"
)

func TestChatFooterClearsModelStateWhenSetModelStateReceivesEmptyValues(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		ModelProvider:  "codex",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
		Meta:           ChatSessionMeta{Agent: "swarm"},
	})

	before := page.footerSettingsLine(1000)
	if !strings.Contains(before, "[m:gpt-5-codex]") {
		t.Fatalf("expected seeded footer model chip, got %q", before)
	}

	page.SetModelState("", "", "", "", "")

	after := page.footerSettingsLine(1000)
	if !strings.Contains(after, "[m:unset]") {
		t.Fatalf("expected cleared footer model chip, got %q", after)
	}
	if !strings.Contains(after, "[t:-]") {
		t.Fatalf("expected cleared footer thinking chip, got %q", after)
	}
	if strings.Contains(after, "gpt-5-codex") {
		t.Fatalf("did not expect stale model label after clear, got %q", after)
	}
}
