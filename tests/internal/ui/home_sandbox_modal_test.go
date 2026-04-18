package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestSandboxModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.sandboxModal.Visible = true
	p.sandboxModal.Data.Summary = "Sandbox summary"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 50, 14
	screen.SetSize(w, h)
	p.drawSandboxModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Sandbox Setup") {
		t.Fatalf("expected sandbox modal on narrow screen, got:\n%s", text)
	}
}
