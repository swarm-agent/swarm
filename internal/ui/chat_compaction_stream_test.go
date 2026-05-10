package ui

import (
	"strings"
	"testing"
	"time"
)

func TestCompactionStatusDoesNotMergeIntoLiveAssistant(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-compact-stream",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.historyLoaded = true
	page.busy = true
	page.streamingRun = true
	page.runStarted = time.Now()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:    "session.status",
		Status:  "compacting",
		Summary: "compacting full chat with memory agent",
	}, time.Now().UnixMilli())

	if strings.TrimSpace(page.liveAssistant) != "" {
		t.Fatalf("compaction status merged into live assistant: %q", page.liveAssistant)
	}
	if !strings.Contains(page.statusLine, "compacting full chat with memory agent") {
		t.Fatalf("status line did not show compaction progress: %q", page.statusLine)
	}
}
