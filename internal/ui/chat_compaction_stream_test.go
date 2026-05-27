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

func TestCompactionToolEventsRenderThroughNativeToolTimeline(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-compact-tool-stream",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.historyLoaded = true
	page.busy = true
	page.streamingRun = true
	page.runStarted = time.Now()
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.started",
		ToolName:  "compact",
		CallID:    "context-compact:manual:2",
		Arguments: `{"origin":"manual","label":"Manual compact"}`,
		Output:    "compacting full chat with memory agent (one shot, attempt 1)",
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "compact",
		CallID:   "context-compact:manual:2",
		Output:   "\ncontext checkpoint saved (Compact #2); usage counters reset",
	}, now+1)

	if strings.TrimSpace(page.liveAssistant) != "" {
		t.Fatalf("compaction tool stream merged into live assistant: %q", page.liveAssistant)
	}
	lines := page.renderLiveToolEntryLines(page.latestToolStreamEntry("context-compact:manual:2", "compact"), 100)
	text := renderCompactionLinesText(lines)
	if !strings.Contains(text, "compact") {
		t.Fatalf("native tool timeline did not render compact tool name:\n%s", text)
	}
	if !strings.Contains(text, "compacting full chat with memory agent") {
		t.Fatalf("native tool timeline did not render compact progress:\n%s", text)
	}
}

func renderCompactionLinesText(lines []chatRenderLine) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.Text)
	}
	return strings.Join(parts, "\n")
}
