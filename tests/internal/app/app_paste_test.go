package app

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestSetPasteActive_PropagatesToHomeAndChat(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1"}),
	}

	a.home.ClearPrompt()
	a.chat.ClearInput()
	a.setPasteActive(true)

	a.home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	a.chat.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if !a.pasteActive {
		t.Fatalf("pasteActive = false, want true")
	}
	if got := a.home.PromptValue(); got != "" {
		t.Fatalf("home prompt = %q, want buffered empty prompt during active paste", got)
	}
	if got := a.chat.InputValue(); got != "" {
		t.Fatalf("chat input = %q, want buffered empty input during active paste", got)
	}

	a.setPasteActive(false)

	if a.pasteActive {
		t.Fatalf("pasteActive = true, want false")
	}
	if got := a.home.PromptValue(); got != " " {
		t.Fatalf("home prompt = %q, want flushed single pasted space", got)
	}
	if got := a.chat.InputValue(); got != " " {
		t.Fatalf("chat input = %q, want flushed single pasted space", got)
	}
}
