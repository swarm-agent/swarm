package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func workspaceModalTestEntries() []WorkspaceModalWorkspace {
	return []WorkspaceModalWorkspace{
		{
			Name:        "ws-one",
			Path:        "/tmp/ws-one",
			Directories: []string{"/tmp/ws-one", "/tmp/ws-one-linked-a", "/tmp/ws-one-linked-b"},
			SortIndex:   0,
			Active:      true,
		},
	}
}

func TestWorkspaceModalEnterEditsSelectedWorkspace(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceModalTestEntries())
	p.ShowWorkspaceModal()
	p.workspaceModal.Focus = workspaceModalFocusList

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.workspaceModal.ActionMenuVisible {
		t.Fatalf("did not expect action menu to open")
	}
	if p.workspaceModal.Editor == nil {
		t.Fatalf("expected workspace editor to open")
	}
	if p.workspaceModal.Editor.WorkspacePath != "/tmp/ws-one" {
		t.Fatalf("editor workspace path = %q, want /tmp/ws-one", p.workspaceModal.Editor.WorkspacePath)
	}
	if _, ok := p.PopWorkspaceModalAction(); ok {
		t.Fatalf("did not expect queued action when opening editor")
	}
}

func TestWorkspaceModalRightArrowMovesCardSelection(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	entries := []WorkspaceModalWorkspace{
		{Name: "ws-one", Path: "/tmp/ws-one", SortIndex: 0, Active: true},
		{Name: "ws-two", Path: "/tmp/ws-two", SortIndex: 1},
	}
	p.SetWorkspaceModalData(entries)
	p.ShowWorkspaceModal()
	p.workspaceModal.CardColumns = 2
	p.workspaceModal.Focus = workspaceModalFocusList

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))

	if p.workspaceModal.SelectedWorkspace != 1 {
		t.Fatalf("selected workspace = %d, want 1", p.workspaceModal.SelectedWorkspace)
	}
}

func TestWorkspaceModalEscClosesActionMenuBeforeModal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceModalTestEntries())
	p.ShowWorkspaceModal()
	p.workspaceModal.Focus = workspaceModalFocusDetails
	p.workspaceModal.ActionMenuVisible = true

	p.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))

	if !p.WorkspaceModalVisible() {
		t.Fatalf("expected workspace modal to remain open")
	}
	if p.workspaceModal.ActionMenuVisible {
		t.Fatalf("expected action menu to close")
	}
	if p.workspaceModal.Focus != workspaceModalFocusList {
		t.Fatalf("focus = %v, want list", p.workspaceModal.Focus)
	}
}

func TestWorkspaceModalActivateKeyStillActivatesSelectedWorkspaceFromDetails(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceModalTestEntries())
	p.ShowWorkspaceModal()
	p.workspaceModal.Focus = workspaceModalFocusDetails
	p.workspaceModal.ActionMenuVisible = true

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))

	action, ok := p.PopWorkspaceModalAction()
	if !ok {
		t.Fatalf("expected activate action")
	}
	if action.Kind != WorkspaceModalActionSelect {
		t.Fatalf("action kind = %q, want %q", action.Kind, WorkspaceModalActionSelect)
	}
	if action.Path != "/tmp/ws-one" {
		t.Fatalf("action path = %q, want /tmp/ws-one", action.Path)
	}
}

func TestWorkspaceModalTabDoesNotLeaveCardNavigation(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceModalTestEntries())
	p.ShowWorkspaceModal()
	p.workspaceModal.Focus = workspaceModalFocusList

	p.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))

	if p.workspaceModal.Focus != workspaceModalFocusList {
		t.Fatalf("focus = %v, want list", p.workspaceModal.Focus)
	}
	if p.workspaceModal.ActionMenuVisible {
		t.Fatalf("did not expect action menu to open")
	}
}

func TestWorkspaceModalKeybindLeftMovesCardSelection(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	entries := []WorkspaceModalWorkspace{
		{Name: "ws-one", Path: "/tmp/ws-one", SortIndex: 0, Active: true},
		{Name: "ws-two", Path: "/tmp/ws-two", SortIndex: 1},
	}
	p.SetWorkspaceModalData(entries)
	p.ShowWorkspaceModal()
	p.workspaceModal.CardColumns = 2
	p.workspaceModal.SelectedWorkspace = 1
	p.workspaceModal.Focus = workspaceModalFocusList

	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))

	if p.workspaceModal.SelectedWorkspace != 0 {
		t.Fatalf("selected workspace = %d, want 0", p.workspaceModal.SelectedWorkspace)
	}
}

func TestWorkspaceModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceModalTestEntries())
	p.ShowWorkspaceModal()

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 54, 16
	screen.SetSize(w, h)
	p.drawWorkspaceModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Workspace Manager") {
		t.Fatalf("expected workspace modal on narrow screen, got:\n%s", text)
	}
}
