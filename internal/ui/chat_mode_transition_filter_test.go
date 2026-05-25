package ui

import (
	"strings"
	"testing"
	"time"
)

const testPlanModeReentrySystemMessage = "Session mode changed to plan. The user explicitly re-entered plan mode; immediately follow plan-mode behavior for the next turn, use plan_manage to inspect or revise the active plan, and call exit_plan_mode only after presenting an actionable plan for approval."

const testAutoModeReentrySystemMessage = "Session mode changed to auto. The user explicitly exited plan mode; immediately follow auto-mode behavior for the next turn, do not call exit_plan_mode, and use plan_manage to inspect or revise any active plan."

func newModeTransitionFilterTestPage() *ChatPage {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.timeline = nil
	page.resetTimelineRenderCache()
	return page
}

func TestModeTransitionSystemMessagesSuppressedFromHistory(t *testing.T) {
	page := newModeTransitionFilterTestPage()

	page.applyHistory([]ChatMessageRecord{
		{
			ID:        "msg-plan-mode",
			Role:      "system",
			Content:   testPlanModeReentrySystemMessage,
			Metadata:  map[string]any{"source": "session_mode_transition", "mode": "plan"},
			CreatedAt: time.Now().UnixMilli(),
		},
		{
			ID:        "msg-auto-mode",
			Role:      "system",
			Content:   testAutoModeReentrySystemMessage,
			Metadata:  map[string]any{"source": "session_mode_transition", "mode": "auto"},
			CreatedAt: time.Now().UnixMilli(),
		},
		{
			ID:        "msg-user",
			Role:      "user",
			Content:   "hello",
			CreatedAt: time.Now().UnixMilli(),
		},
	})

	if len(page.timeline) != 1 {
		t.Fatalf("timeline length = %d, want only user message", len(page.timeline))
	}
	if page.timeline[0].Role != "user" || page.timeline[0].Text != "hello" {
		t.Fatalf("timeline[0] = (%q, %q), want user hello", page.timeline[0].Role, page.timeline[0].Text)
	}
}

func TestModeTransitionSystemMessagesSuppressedFromLiveStoredEvents(t *testing.T) {
	page := newModeTransitionFilterTestPage()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type: "message.stored",
		Message: &ChatMessageRecord{
			ID:        "msg-plan-mode",
			Role:      "system",
			Content:   testPlanModeReentrySystemMessage,
			Metadata:  map[string]any{"source": "session_mode_transition", "mode": "plan"},
			CreatedAt: time.Now().UnixMilli(),
		},
	}, time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type: "message.updated",
		Message: &ChatMessageRecord{
			ID:        "msg-auto-mode",
			Role:      "system",
			Content:   testAutoModeReentrySystemMessage,
			Metadata:  map[string]any{"source": "session_mode_transition", "mode": "auto"},
			CreatedAt: time.Now().UnixMilli(),
		},
	}, time.Now().UnixMilli())

	if len(page.timeline) != 0 {
		t.Fatalf("timeline length = %d, want no visible mode-transition messages", len(page.timeline))
	}
}

func TestModeTransitionSystemMessagesSuppressedByContentFallback(t *testing.T) {
	page := newModeTransitionFilterTestPage()

	page.applyHistory([]ChatMessageRecord{
		{ID: "msg-plan-mode", Role: "system", Content: testPlanModeReentrySystemMessage, CreatedAt: time.Now().UnixMilli()},
		{ID: "msg-auto-mode", Role: "system", Content: testAutoModeReentrySystemMessage, CreatedAt: time.Now().UnixMilli()},
		{ID: "msg-system", Role: "system", Content: "A different system notice", CreatedAt: time.Now().UnixMilli()},
	})

	if len(page.timeline) != 1 {
		t.Fatalf("timeline length = %d, want unrelated system message only", len(page.timeline))
	}
	if got := page.timeline[0].Text; got != "A different system notice" {
		t.Fatalf("visible system message = %q", got)
	}
}

func TestModeTransitionSystemMessagesSkippedDuringRender(t *testing.T) {
	page := newModeTransitionFilterTestPage()
	page.timeline = append(page.timeline,
		chatMessageItem{Role: "system", Text: testPlanModeReentrySystemMessage, Metadata: map[string]any{"source": "session_mode_transition"}},
		chatMessageItem{Role: "user", Text: "visible prompt"},
		chatMessageItem{Role: "system", Text: testAutoModeReentrySystemMessage},
	)

	rendered := chatRenderLinesText(page.buildTimelineLines(100))
	if strings.Contains(rendered, "Session mode changed") {
		t.Fatalf("mode-transition message rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, "visible prompt") {
		t.Fatalf("expected visible user prompt, got:\n%s", rendered)
	}

	blocks := page.buildTimelineRenderBlocks(100)
	for _, block := range blocks {
		text := chatRenderLinesText(block.Lines)
		if strings.Contains(text, "Session mode changed") {
			t.Fatalf("mode-transition block rendered:\n%s", text)
		}
	}
}
