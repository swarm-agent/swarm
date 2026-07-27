package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestWorktreeCreateModalDrawsStyledFields(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowWorktreeCreateModal()
	p.worktreesModal.CreateTitle = "Fix the TUI"
	p.worktreesModal.CreateBranch = "fix-the-tui"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 100, 24
	screen.SetSize(w, h)
	p.drawWorktreesModal(screen)

	text := dumpScreenText(screen, w, h)
	for _, want := range []string{"New Worktree Session", "Session title", "Branch", "Create & open", "fix-the-tui"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected create modal to contain %q, got:\n%s", want, text)
		}
	}
}

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
