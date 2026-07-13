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

func TestSetMessagesPreservesProjectionOrderWhenMessageTimestampsAreGrouped(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.SetMessages([]ChatMessageRecord{
		{ID: "u1", GlobalSeq: 1, Role: "user", Content: "hey", CreatedAt: 1},
		{ID: "a1", GlobalSeq: 2, Role: "assistant", Content: "hello", CreatedAt: 4},
		{ID: "u2", GlobalSeq: 3, Role: "user", Content: "still there?", CreatedAt: 2},
		{ID: "a2", GlobalSeq: 4, Role: "assistant", Content: "yes", CreatedAt: 4},
		{ID: "u3", GlobalSeq: 5, Role: "user", Content: "great", CreatedAt: 3},
		{ID: "a3", GlobalSeq: 6, Role: "assistant", Content: "thumbs up", CreatedAt: 4},
	})

	assertTimelineTurns(t, page, []string{
		"user:hey",
		"assistant:hello",
		"user:still there?",
		"assistant:yes",
		"user:great",
		"assistant:thumbs up",
	})
}

func TestMergeToolTimelineMessagesDoesNotReorderConversationTurns(t *testing.T) {
	messages := []chatMessageItem{
		{Role: "user", Text: "first", CreatedAt: 1},
		{Role: "assistant", Text: "first reply", CreatedAt: 4},
		{Role: "user", Text: "second", CreatedAt: 2},
		{Role: "assistant", Text: "second reply", CreatedAt: 4},
	}
	got := mergeToolTimelineMessages(messages, []chatMessageItem{
		{Role: "tool", Text: "tool result", CreatedAt: 3},
	})

	turns := make([]string, 0, len(got))
	for _, item := range got {
		turns = append(turns, item.Role+":"+item.Text)
	}
	want := []string{"user:first", "tool:tool result", "assistant:first reply", "user:second", "assistant:second reply"}
	if len(got) != len(want) {
		t.Fatalf("timeline = %#v, want %#v", turns, want)
	}
	for i := range want {
		if turns[i] != want[i] {
			t.Fatalf("timeline[%d] = %q, want %q (all: %#v)", i, turns[i], want[i], turns)
		}
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
