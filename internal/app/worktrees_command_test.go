package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestWorktreesNewOpensCreateModal(t *testing.T) {
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{home: home}
	app.handleWorktreesCommand([]string{"new"})
	if !home.WorktreeCreateModalVisible() {
		t.Fatal("/wt new did not open worktree create modal")
	}
}

func TestWorktreesRetiredToggleNoLongerEnables(t *testing.T) {
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{home: home}
	app.handleWorktreesCommand([]string{"o" + "n"})
	if home.WorktreesModalVisible() {
		t.Fatal("retired short worktree toggle unexpectedly opened or enabled worktrees")
	}
}
