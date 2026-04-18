package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestSessionsPaletteDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ShowHeader: true, AuthConfigured: true, SessionMode: "plan"})
	page.OpenSessionsPalette([]ChatSessionPaletteItem{{ID: "s1", Title: "Session One", UpdatedAgo: "1m"}}, "")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 44, 14
	screen.SetSize(w, h)
	page.Draw(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Sessions") {
		t.Fatalf("expected sessions palette on narrow screen, got:\n%s", text)
	}
}
