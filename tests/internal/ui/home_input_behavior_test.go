package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeDesiredInputBarHeightGrowsWithPrompt(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	if got := p.desiredInputBarHeight(80); got != 3 {
		t.Fatalf("desiredInputBarHeight(80) = %d, want 3 for empty prompt", got)
	}
	p.SetPrompt(strings.Repeat("x", 500))
	if got := p.desiredInputBarHeight(80); got <= 3 {
		t.Fatalf("desiredInputBarHeight(80) = %d, want > 3 for long prompt", got)
	}
}

func TestHomeEditorCursorMovesWithLeftRight(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPrompt("hello")
	if got := p.PromptCursor(); got != 5 {
		t.Fatalf("initial PromptCursor() = %d, want 5", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := p.PromptCursor(); got != 3 {
		t.Fatalf("PromptCursor() after left = %d, want 3", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'X', tcell.ModNone))
	if got := p.PromptValue(); got != "helXlo" {
		t.Fatalf("PromptValue() after insert = %q, want %q", got, "helXlo")
	}
	if got := p.PromptCursor(); got != 4 {
		t.Fatalf("PromptCursor() after insert = %d, want 4", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := p.PromptCursor(); got != 5 {
		t.Fatalf("PromptCursor() after right = %d, want 5", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone))
	if got := p.PromptValue(); got != "helXo" {
		t.Fatalf("PromptValue() after backspace = %q, want %q", got, "helXo")
	}
	if got := p.PromptCursor(); got != 4 {
		t.Fatalf("PromptCursor() after backspace = %d, want 4", got)
	}
}

func TestHomeDrawShowsCursorAtTrackedPosition(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPrompt("hello")
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(80, 24)
	p.Draw(screen)
	text := dumpScreenText(screen, 80, 24)
	if !strings.Contains(text, "he█lo") {
		t.Fatalf("expected tracked home cursor in render, got:\n%s", text)
	}
}
