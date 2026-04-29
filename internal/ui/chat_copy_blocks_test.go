package ui

import (
	"strings"
	"testing"
)

func TestChatCommandPaletteIncludesCopyBlockCommands(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		CommandSuggestions: []CommandSuggestion{
			{Command: "/copy", Hint: "Copy chat snapshot"},
		},
	})
	page.appendMessage("assistant", "Here you go:\n\n<copy label=\"restart command\">swarm restart</copy>", 1)
	page.appendMessage("assistant", "Another:\n<copy>swarm status</copy>", 2)
	page.input = "/copy 1"
	page.inputCursor = len(page.input)

	matches := page.commandPaletteMatches()
	if len(matches) == 0 {
		t.Fatal("expected command palette matches")
	}
	if matches[0].Command != "/copy 1" {
		t.Fatalf("expected /copy 1 as first match, got %q", matches[0].Command)
	}
	if !strings.Contains(matches[0].Hint, "restart command") {
		t.Fatalf("expected copy block label in hint, got %q", matches[0].Hint)
	}

	selected, ok := page.selectedCommandSuggestion()
	if !ok {
		t.Fatal("expected selected command suggestion")
	}
	if selected.Command != "/copy 1" {
		t.Fatalf("expected selected /copy 1, got %q", selected.Command)
	}
}

func TestChatCommandPaletteIncludesLiveCopyBlockCommands(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		CommandSuggestions: []CommandSuggestion{
			{Command: "/copy", Hint: "Copy chat snapshot"},
		},
	})
	page.liveAssistant = "Streaming:\n<copy>swarm logs</copy>"
	page.input = "/copy 1"
	page.inputCursor = len(page.input)

	selected, ok := page.selectedCommandSuggestion()
	if !ok {
		t.Fatal("expected selected command suggestion")
	}
	if selected.Command != "/copy 1" {
		t.Fatalf("expected selected live /copy 1, got %q", selected.Command)
	}
}
