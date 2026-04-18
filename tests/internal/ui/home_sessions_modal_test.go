package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeSessionsModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.sessionsModal.Visible = true
	p.sessionsModal.Items = []ChatSessionPaletteItem{{ID: "s1", Title: "Session One", UpdatedAgo: "1m"}}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 44, 14
	screen.SetSize(w, h)
	p.drawSessionsModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Sessions") {
		t.Fatalf("expected sessions modal on narrow screen, got:\n%s", text)
	}
}
