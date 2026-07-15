package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestWorktreeBranchSlug(t *testing.T) {
	if got := worktreeBranchSlug("Fix the backend"); got != "fix-the-backend" {
		t.Fatalf("worktreeBranchSlug() = %q, want fix-the-backend", got)
	}
}

func TestWorktreeCreateTitleSuggestsEditableBranch(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowWorktreeCreateModal()
	for _, r := range "Fix the backend" {
		page.handleWorktreesModalKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	if got := page.worktreesModal.CreateBranch; got != "fix-the-backend" {
		t.Fatalf("suggested branch = %q, want fix-the-backend", got)
	}

	page.handleWorktreesModalKey(tcell.NewEventKey(tcell.KeyTAB, 0, 0))
	page.handleWorktreesModalKey(tcell.NewEventKey(tcell.KeyCtrlU, 0, 0))
	for _, r := range "custom-branch" {
		page.handleWorktreesModalKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	page.handleWorktreesModalKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	action, ok := page.PopWorktreesModalAction()
	if !ok {
		t.Fatal("expected create action")
	}
	if action.Kind != WorktreesModalActionCreateSession || action.Title != "Fix the backend" || action.BranchName != "custom-branch" {
		t.Fatalf("action = %+v", action)
	}
}
