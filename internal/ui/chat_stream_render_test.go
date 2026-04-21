package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestApplySessionLifecycleCompletedPreservesLiveAssistantUntilRunSuccess(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "streamed partial response"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"

	page.ApplySessionLifecycle(ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    false,
		Phase:     "completed",
	})

	if got := page.liveAssistant; got != "streamed partial response" {
		t.Fatalf("live assistant cleared on completed lifecycle: %q", got)
	}
	if got := page.ownedRunID; got != "run-1" {
		t.Fatalf("owned run id cleared too early: %q", got)
	}
}

func TestApplyRunSuccessClearsLiveAssistantAfterPersistingFinalMessage(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "streamed partial response"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"

	page.applyRunSuccess(ChatRunResponse{
		AssistantMessage: ChatMessageRecord{
			Content:   "streamed partial response",
			CreatedAt: time.Now().UnixMilli(),
		},
	})

	if got := page.liveAssistant; got != "" {
		t.Fatalf("live assistant not cleared after success: %q", got)
	}
	if got := page.ownedRunID; got != "" {
		t.Fatalf("owned run id not cleared after success: %q", got)
	}
	if len(page.timeline) == 0 {
		t.Fatal("expected assistant message appended to timeline")
	}
	last := page.timeline[len(page.timeline)-1]
	if last.Role != "assistant" {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	if last.Text != "streamed partial response" {
		t.Fatalf("last text = %q, want final assistant text", last.Text)
	}
}

func TestCachedLiveAssistantLinesReuseRecentParseResult(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "Hello **world**"

	first := page.cachedLiveAssistantLines(80)
	if len(first) == 0 {
		t.Fatal("expected rendered live assistant lines")
	}

	entry := page.liveAssistantRenderCache
	entry.LastParseAt = time.Now()
	page.liveAssistantRenderCache = entry

	second := page.cachedLiveAssistantLines(80)
	if len(second) == 0 {
		t.Fatal("expected cached live assistant lines")
	}
	if page.liveAssistantRenderCache.LastParseAt != entry.LastParseAt {
		t.Fatal("expected cached path to avoid reparsing within min interval")
	}
}

func TestStreamingMarkdownDrawRemainsVisibleAcrossCompletedLifecycleAndFinalPersist(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
		ShowHeader:     true,
	})
	page.liveAssistant = "# Title\n\n- item one\n- item two"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 24)

	page.Draw(screen)
	before := dumpScreenText(screen, 100, 24)
	if !containsAll(before, []string{"Title", "item one", "item two"}) {
		t.Fatalf("streamed markdown missing before completion:\n%s", before)
	}

	page.ApplySessionLifecycle(ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    false,
		Phase:     "completed",
	})
	page.Draw(screen)
	mid := dumpScreenText(screen, 100, 24)
	if !containsAll(mid, []string{"Title", "item one", "item two"}) {
		t.Fatalf("streamed markdown missing after completed lifecycle:\n%s", mid)
	}

	page.applyRunSuccess(ChatRunResponse{
		AssistantMessage: ChatMessageRecord{
			Content:   "# Title\n\n- item one\n- item two",
			CreatedAt: time.Now().UnixMilli(),
		},
	})
	page.Draw(screen)
	after := dumpScreenText(screen, 100, 24)
	if !containsAll(after, []string{"Title", "item one", "item two"}) {
		t.Fatalf("final markdown missing after success:\n%s", after)
	}
}

func TestCachedLiveAssistantLinesReparseAfterShortStreamingInterval(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "First line"

	first := page.cachedLiveAssistantLines(80)
	if len(first) == 0 {
		t.Fatal("expected initial rendered live assistant lines")
	}

	page.liveAssistant = "First line\nSecond line"
	entry := page.liveAssistantRenderCache
	entry.LastParseAt = time.Now().Add(-40 * time.Millisecond)
	page.liveAssistantRenderCache = entry

	second := page.cachedLiveAssistantLines(80)
	if len(second) <= len(first) {
		t.Fatalf("expected reparsed output to grow after short interval: first=%d second=%d", len(first), len(second))
	}
	if page.liveAssistantRenderCache.ParsedText != strings.TrimSpace(page.liveAssistant) {
		t.Fatalf("expected cache parsed text to refresh, got %q", page.liveAssistantRenderCache.ParsedText)
	}
}

func containsAll(text string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}
