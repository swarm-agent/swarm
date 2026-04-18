package ui

import "testing"

func newPaletteChatPage() *ChatPage {
	return NewChatPage(ChatPageOptions{
		SessionID: "session-test",
		CommandSuggestions: []CommandSuggestion{
			{Command: "/auth", Hint: "Auth status or key setup"},
			{Command: "/header toggle", Hint: "Toggle chat header"},
			{Command: "/help", Hint: "Show help"},
		},
		ShowHeader: true,
	})
}

func TestChatAcceptCommandPaletteEnter_CompletesSelection(t *testing.T) {
	p := newPaletteChatPage()
	p.input = "/"

	if handled := p.AcceptCommandPaletteEnter(); !handled {
		t.Fatalf("AcceptCommandPaletteEnter() = false, want true")
	}
	if got := p.InputValue(); got != "/auth " {
		t.Fatalf("input = %q, want %q", got, "/auth ")
	}
}

func TestChatAcceptCommandPaletteEnter_DoesNotEatExactCommand(t *testing.T) {
	p := newPaletteChatPage()
	p.input = "/auth"

	if handled := p.AcceptCommandPaletteEnter(); handled {
		t.Fatalf("AcceptCommandPaletteEnter() = true, want false")
	}
	if got := p.InputValue(); got != "/auth" {
		t.Fatalf("input = %q, want %q", got, "/auth")
	}
}

func TestChatAcceptCommandPaletteEnter_DoesNotEatWhenArgsPresent(t *testing.T) {
	p := newPaletteChatPage()
	p.input = "/header off"

	if handled := p.AcceptCommandPaletteEnter(); handled {
		t.Fatalf("AcceptCommandPaletteEnter() = true, want false")
	}
	if got := p.InputValue(); got != "/header off" {
		t.Fatalf("input = %q, want %q", got, "/header off")
	}
}
