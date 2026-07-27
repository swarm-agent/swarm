package ui

import (
	"errors"
	"strings"
	"testing"
)

func TestFriendlyRunErrorPreservesDispatchAuthorityErrors(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test"})
	got := page.friendlyRunError(errors.New("dispatch authority missing executor runtime"))
	if got != "Run failed: dispatch authority missing executor runtime" {
		t.Fatalf("friendlyRunError() = %q", got)
	}
	if strings.Contains(got, "Run /auth") {
		t.Fatalf("dispatch authority error was misclassified as auth: %q", got)
	}
}

func TestFriendlyRunErrorIncludesAuthDetails(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test"})
	got := page.friendlyRunError(errors.New("codex auth not configured"))
	if !strings.Contains(got, "Run /auth") {
		t.Fatalf("expected auth guidance, got %q", got)
	}
	if !strings.Contains(got, "codex auth not configured") {
		t.Fatalf("expected original error details, got %q", got)
	}
}
