package ui

import (
	"strings"
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

	items := page.workspaceItems()
	if len(items) != 3 || items[0].Action != "workspace-selector" || items[1].Action != "workspace-select" || items[1].Index != 0 || items[2].Index != 1 {
		t.Fatalf("workspace indicators = %#v", items)
	}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Label), "alt+") {
			t.Fatalf("workspace indicator advertises a removed shortcut: %q", item.Label)
		}
	}
}

func TestEmptyWorkspaceIndicatorKeepsMouseSelectorWithoutShortcutLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{})
	items := page.workspaceItems()
	if len(items) != 1 || items[0].Action != "workspace-selector" || strings.Contains(strings.ToLower(items[0].Label), "alt+") {
		t.Fatalf("empty workspace indicator = %#v", items)
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
