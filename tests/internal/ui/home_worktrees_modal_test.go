package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestWorktreesModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.worktreesModal.Visible = true
	p.worktreesModal.Data.WorkspacePath = "/tmp/workspace"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 50, 14
	screen.SetSize(w, h)
	p.drawWorktreesModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Worktrees") {
		t.Fatalf("expected worktrees modal on narrow screen, got:\n%s", text)
	}
}
