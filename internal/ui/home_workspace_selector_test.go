package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestWorkspaceSelectorNumberKeyActivatesSlotImmediately(t *testing.T) {
	page := NewHomePage(testHomeModel())
	page.SetWorkspaceModalIntent("select", "")
	page.SetWorkspaceModalData([]WorkspaceModalWorkspace{
		{Name: "alpha", Path: "/work/alpha", SortIndex: 0, Active: true},
		{Name: "beta", Path: "/work/beta", SortIndex: 1},
	})
	page.ShowWorkspaceModal()

	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone))

	action, ok := page.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionSelect || action.Path != "/work/beta" {
		t.Fatalf("slot selection action = %#v, ok=%v", action, ok)
	}
}

func TestWorkspaceManagerAdvertisesAndOpensWorkspaceActions(t *testing.T) {
	page := NewHomePage(testHomeModel())
	page.SetWorkspaceModalData([]WorkspaceModalWorkspace{{Name: "alpha", Path: "/work/alpha", Active: true}})
	page.ShowWorkspaceModal()

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 32)
	page.drawWorkspaceModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	if text := rendered.String(); !strings.Contains(text, "M Workspace Actions") {
		t.Fatalf("workspace manager did not advertise actions shortcut:\n%s", text)
	}

	page.handleWorkspaceModalKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	if !page.workspaceModal.ActionMenuVisible || page.workspaceModal.Focus != workspaceModalFocusDetails || page.workspaceModal.SelectedAction < 0 {
		t.Fatalf("workspace actions state = visible:%v focus:%v selected:%d", page.workspaceModal.ActionMenuVisible, page.workspaceModal.Focus, page.workspaceModal.SelectedAction)
	}
}

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
	if !strings.Contains(strings.ToLower(items[0].Label), "alt+w") {
		t.Fatalf("workspace selector indicator does not advertise Alt+W: %q", items[0].Label)
	}
}

func TestWorkspaceHeaderLimitsVisibleWorkspacesAndKeepsActiveVisible(t *testing.T) {
	workspaces := make([]model.Workspace, 7)
	for i := range workspaces {
		workspaces[i] = model.Workspace{Name: string(rune('a' + i)), Path: "/work/" + string(rune('a'+i))}
	}
	workspaces[6].Active = true
	page := NewHomePage(model.HomeModel{Workspaces: workspaces})

	indexes, overflow := headerWorkspaceIndexes(workspaces, maxHeaderWorkspaces)
	if !overflow || len(indexes) != 5 || indexes[0] != 0 || indexes[3] != 3 || indexes[4] != 6 {
		t.Fatalf("header indexes = %#v, overflow=%v", indexes, overflow)
	}
	items := page.workspaceItems()
	if len(items) != 7 || items[5].Label != "..." || items[6].Index != 6 || !strings.Contains(items[6].Label, "g") {
		t.Fatalf("workspace header items = %#v", items)
	}
}

func TestWorkspaceHeaderShowsOverflowAfterVisibleActiveWorkspace(t *testing.T) {
	workspaces := make([]model.Workspace, 6)
	for i := range workspaces {
		workspaces[i] = model.Workspace{Name: string(rune('a' + i)), Path: "/work/" + string(rune('a'+i))}
	}
	workspaces[1].Active = true
	page := NewHomePage(model.HomeModel{Workspaces: workspaces})

	items := page.workspaceItems()
	if len(items) != 7 || items[len(items)-1].Label != "..." {
		t.Fatalf("workspace header items = %#v", items)
	}
}

func TestEmptyWorkspaceIndicatorKeepsSelectorWithShortcutLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{})
	items := page.workspaceItems()
	if len(items) != 1 || items[0].Action != "workspace-selector" || !strings.Contains(strings.ToLower(items[0].Label), "alt+w") {
		t.Fatalf("empty workspace indicator = %#v", items)
	}
}

func TestWorkspaceHeaderWarningUsesSaveCommandAndConfiguredSelectorKey(t *testing.T) {
	page := NewHomePage(model.HomeModel{WorkspaceSetupPath: "/outside"})
	if got := page.workspaceSetupWarning(); got != "Detected launch path: /outside is not a workspace. Type /workspace save to save this directory and switch." {
		t.Fatalf("workspace setup warning = %q", got)
	}
	if err := page.keybinds.Set(KeybindGlobalWorkspaceSelect, "ctrl+w"); err != nil {
		t.Fatal(err)
	}
	items := page.workspaceItems()
	if len(items) != 1 || !strings.Contains(items[0].Label, "Ctrl+W") {
		t.Fatalf("workspace selector label = %#v", items)
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
