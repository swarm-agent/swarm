package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestChatEditorCursorMovesWithLeftRight(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetInput("hello")
	if got := p.InputCursor(); got != 5 {
		t.Fatalf("initial InputCursor() = %d, want 5", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := p.InputCursor(); got != 3 {
		t.Fatalf("InputCursor() after left = %d, want 3", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'X', tcell.ModNone))
	if got := p.InputValue(); got != "helXlo" {
		t.Fatalf("InputValue() after insert = %q, want %q", got, "helXlo")
	}
	if got := p.InputCursor(); got != 4 {
		t.Fatalf("InputCursor() after insert = %d, want 4", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := p.InputCursor(); got != 5 {
		t.Fatalf("InputCursor() after right = %d, want 5", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone))
	if got := p.InputValue(); got != "helXo" {
		t.Fatalf("InputValue() after backspace = %q, want %q", got, "helXo")
	}
	if got := p.InputCursor(); got != 4 {
		t.Fatalf("InputCursor() after backspace = %d, want 4", got)
	}
}

func TestChatDrawShowsCursorAtTrackedPosition(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetInput("hello")
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(80, 18)
	p.Draw(screen)
	text := dumpScreenText(screen, 80, 18)
	if !strings.Contains(text, "he█lo") {
		t.Fatalf("expected tracked chat cursor in render, got:\n%s", text)
	}
}
