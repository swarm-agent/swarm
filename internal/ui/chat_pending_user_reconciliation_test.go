package ui

import "testing"

func TestSetMessagesPreservesPendingSecondUserTurnUntilDurableSnapshot(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	firstTurn := []ChatMessageRecord{
		{ID: "u1", Role: "user", Content: "first", CreatedAt: 1},
		{ID: "a1", Role: "assistant", Content: "first reply", CreatedAt: 2},
	}
	page.SetMessages(firstTurn)
	page.trackPendingLocalUserMessage("second", 3)
	page.appendMessage("user", "second", 3)

	page.SetMessages(append(firstTurn, ChatMessageRecord{ID: "a2", Role: "assistant", Content: "second reply", CreatedAt: 4}))
	assertTimelineTurns(t, page, []string{"user:first", "assistant:first reply", "user:second", "assistant:second reply"})

	page.SetMessages(append(firstTurn,
		ChatMessageRecord{ID: "u2", Role: "user", Content: "second", CreatedAt: 3},
		ChatMessageRecord{ID: "a2", Role: "assistant", Content: "second reply", CreatedAt: 4},
	))
	assertTimelineTurns(t, page, []string{"user:first", "assistant:first reply", "user:second", "assistant:second reply"})
	if len(page.pendingLocalUserMessages) != 0 {
		t.Fatalf("pending local messages = %d, want 0", len(page.pendingLocalUserMessages))
	}
}

func TestSetMessagesReconcilesRepeatedIdenticalUserPromptsInOrder(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	firstTurn := []ChatMessageRecord{
		{ID: "u1", Role: "user", Content: "repeat", CreatedAt: 1},
		{ID: "a1", Role: "assistant", Content: "first reply", CreatedAt: 2},
	}
	page.SetMessages(firstTurn)
	page.trackPendingLocalUserMessage("repeat", 3)

	page.SetMessages(firstTurn)
	assertTimelineTurns(t, page, []string{"user:repeat", "assistant:first reply", "user:repeat"})

	page.SetMessages(append(firstTurn, ChatMessageRecord{ID: "u2", Role: "user", Content: "repeat", CreatedAt: 3}))
	assertTimelineTurns(t, page, []string{"user:repeat", "assistant:first reply", "user:repeat"})
	if len(page.pendingLocalUserMessages) != 0 {
		t.Fatalf("pending local messages = %d, want 0", len(page.pendingLocalUserMessages))
	}
}

func assertTimelineTurns(t *testing.T, page *ChatPage, want []string) {
	t.Helper()
	got := make([]string, 0, len(page.timeline))
	for _, item := range page.timeline {
		if item.Role == "user" || item.Role == "assistant" {
			got = append(got, item.Role+":"+item.Text)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("timeline = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeline[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}
