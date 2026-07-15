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

func TestSetMessagesKeepsPendingResumedTurnInSequenceWhenTailWindowRotates(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.SetMessages([]ChatMessageRecord{
		{ID: "u1", GlobalSeq: 10, Role: "user", Content: "first", CreatedAt: 1},
		{ID: "a1", GlobalSeq: 12, Role: "assistant", Content: "partial before stop", CreatedAt: 2},
	})
	page.trackPendingLocalUserMessage("continue", 3)
	page.appendMessage("user", "continue", 3)

	// The tail snapshot has rotated u1 out before the resumed assistant record
	// arrives. The pending turn still belongs between the two authoritative seqs.
	page.SetMessages([]ChatMessageRecord{
		{ID: "a1", GlobalSeq: 12, Role: "assistant", Content: "partial before stop", CreatedAt: 2},
		{ID: "a2", GlobalSeq: 14, Role: "assistant", Content: "resumed stream", CreatedAt: 4},
	})
	assertTimelineTurns(t, page, []string{
		"assistant:partial before stop",
		"user:continue",
		"assistant:resumed stream",
	})

	page.SetMessages([]ChatMessageRecord{
		{ID: "a1", GlobalSeq: 12, Role: "assistant", Content: "partial before stop", CreatedAt: 2},
		{ID: "u2", GlobalSeq: 13, Role: "user", Content: "continue", CreatedAt: 3},
		{ID: "a2", GlobalSeq: 14, Role: "assistant", Content: "resumed stream", CreatedAt: 4},
	})
	assertTimelineTurns(t, page, []string{
		"assistant:partial before stop",
		"user:continue",
		"assistant:resumed stream",
	})
	if len(page.pendingLocalUserMessages) != 0 {
		t.Fatalf("pending local messages = %d, want 0", len(page.pendingLocalUserMessages))
	}
}

func TestSetMessagesKeepsPendingResumedTurnAheadOfEveryLaterTimelineObject(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.SetMessages([]ChatMessageRecord{
		{ID: "a1", GlobalSeq: 10, Role: "assistant", Content: "before stop", CreatedAt: 100},
	})
	page.trackPendingLocalUserMessage("continue", 200)

	page.SetMessages([]ChatMessageRecord{
		{ID: "a1", GlobalSeq: 10, Role: "assistant", Content: "before stop", CreatedAt: 100},
		{ID: "tool-start", GlobalSeq: 12, Role: "tool", Content: `{"type":"session.tool.started","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","status":"started"}`, Metadata: map[string]any{"v3_tool_event": true}, CreatedAt: 150},
		{ID: "reasoning", GlobalSeq: 13, Role: "reasoning", Content: "checking", CreatedAt: 160},
		{ID: "system", GlobalSeq: 14, Role: "system", Content: "status", CreatedAt: 170},
		{ID: "assistant", GlobalSeq: 15, Role: "assistant", Content: "after tool", CreatedAt: 180},
		{ID: "tool-done", GlobalSeq: 16, Role: "tool", Content: `{"type":"session.tool.completed","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","output":"done","status":"completed"}`, Metadata: map[string]any{"v3_tool_event": true}, CreatedAt: 190},
	})

	assertTimelineItems(t, page, []string{
		"assistant:before stop",
		"user:continue",
		"tool",
		"reasoning:checking",
		"system:status",
		"assistant:after tool",
	})
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

func TestMergeToolTimelineMessagesUsesGlobalSequenceForHeterogeneousChronology(t *testing.T) {
	messages := []chatMessageItem{
		{GlobalSeq: 1, Role: "user", Text: "first", CreatedAt: 100},
		{GlobalSeq: 3, Role: "reasoning", Text: "checking", CreatedAt: 100},
		{GlobalSeq: 5, Role: "system", Text: "status", CreatedAt: 100},
		{GlobalSeq: 6, Role: "assistant", Text: "done", CreatedAt: 100},
	}
	got := mergeToolTimelineMessages(messages, []chatMessageItem{
		{GlobalSeq: 2, Role: "tool", Text: "first tool", CreatedAt: 500},
		{GlobalSeq: 4, Role: "tool", Text: "second tool", CreatedAt: 50},
	})

	assertTimelineItemSlice(t, got, []string{
		"user:first",
		"tool:first tool",
		"reasoning:checking",
		"tool:second tool",
		"system:status",
		"assistant:done",
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

func assertTimelineItems(t *testing.T, page *ChatPage, want []string) {
	t.Helper()
	got := make([]string, 0, len(page.timeline))
	for _, item := range page.timeline {
		if item.Role == "tool" {
			got = append(got, "tool")
			continue
		}
		got = append(got, item.Role+":"+item.Text)
	}
	assertStringSlice(t, got, want)
}

func assertTimelineItemSlice(t *testing.T, items []chatMessageItem, want []string) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Role+":"+item.Text)
	}
	assertStringSlice(t, got, want)
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("timeline = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeline[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
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
