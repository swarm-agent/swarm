package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestWorkspaceSelectorFiltersAndActivates(t *testing.T) {
	page := NewHomePage(testHomeModel())
	page.SetWorkspaceModalIntent("select", "")
	page.SetWorkspaceModalData([]WorkspaceModalWorkspace{
		{Name: "alpha", Path: "/work/alpha", Active: true},
		{Name: "beta", Path: "/work/beta"},
	})
	page.ShowWorkspaceModal()

	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModNone))
	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	matches := page.workspaceFilteredIndexes()
	if len(matches) != 1 || matches[0] != 1 {
		t.Fatalf("filtered matches = %#v", matches)
	}
	if page.workspaceModal.SelectedWorkspace != 1 {
		t.Fatalf("selected workspace = %d", page.workspaceModal.SelectedWorkspace)
	}

	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := page.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionSelect || action.Path != "/work/beta" {
		t.Fatalf("selection action = %#v, ok=%v", action, ok)
	}
}

func TestFirstRegisteredWorkspaceIsInitialSelection(t *testing.T) {
	page := NewHomePage(model.HomeModel{Workspaces: []model.Workspace{{Name: "first", Path: "/work/first"}, {Name: "second", Path: "/work/second"}}})
	if got := page.activeWorkspaceIndex(); got != 0 {
		t.Fatalf("initial workspace index = %d, want 0", got)
	}
	state := page.HomepageState()
	if state.SelectedWorkspace.Name != "first" || state.SelectedWorkspace.Path != "/work/first" {
		t.Fatalf("initial workspace = %#v", state.SelectedWorkspace)
	}
}

func TestWorkspaceSelectorCancelClearsState(t *testing.T) {
	page := NewHomePage(testHomeModel())
	page.SetWorkspaceModalIntent("select", "")
	page.ShowWorkspaceModal()
	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.WorkspaceModalVisible() {
		t.Fatal("workspace selector remained visible")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	if page.prompt != "x" {
		t.Fatalf("composer focus was not restored, prompt = %q", page.prompt)
	}
}

func testHomeModel() model.HomeModel {
	return model.HomeModel{}
}
