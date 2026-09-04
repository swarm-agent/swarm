package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func initWorkspaceModalGitRepository(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	for _, args := range [][]string{
		{"-C", path, "init", "--initial-branch=main"},
		{"-C", path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return path
}

func workspaceDirectoryPickerTestEntries() []WorkspaceModalWorkspace {
	return []WorkspaceModalWorkspace{{
		Name:        "ws-one",
		Path:        "/tmp/ws-one",
		Directories: []string{"/tmp/ws-one", "/tmp/ws-one-linked-a", "/tmp/ws-one-linked-b"},
		SortIndex:   0,
		Active:      true,
	}}
}

func TestWorkspaceManagerIncludesNewWorkspaceCardStartingAtHome(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetWorkspaceModalData(workspaceDirectoryPickerTestEntries())
	p.ShowWorkspaceModal()
	p.workspaceModal.Focus = workspaceModalFocusList
	p.workspaceModal.SelectedWorkspace = len(p.workspaceModal.Workspaces)

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.workspaceModal.Editor == nil {
		t.Fatal("expected New Workspace card to open editor")
	}
	if len(p.workspaceModal.Editor.Fields) == 0 || p.workspaceModal.Editor.Fields[0].Key != "path" || p.workspaceModal.Editor.Fields[0].Value != "~/" {
		t.Fatalf("new workspace path field = %#v, want ~/", p.workspaceModal.Editor.Fields)
	}
}

func TestWorkspaceDirectoryPickerStartsAtHomeEvenWithPrefilledPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prefilled := t.TempDir()
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.OpenWorkspaceModalSaveEditor(prefilled, true)
	picker := p.openWorkspaceModalDirectoryPicker(p.workspaceModal.Editor)
	if picker == nil || picker.CurrentPath != home {
		t.Fatalf("picker = %#v, want home %q", picker, home)
	}
}

func TestWorkspaceDirectoryPickerStartsAtHomeAndHidesDotDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"Projects", "Documents", ".config", ".cache"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.workspaceModalNew()
	picker := p.openWorkspaceModalDirectoryPicker(p.workspaceModal.Editor)
	if picker == nil || picker.CurrentPath != home {
		t.Fatalf("picker = %#v, want home %q", picker, home)
	}
	got := make([]string, 0, len(picker.Entries))
	for _, entry := range picker.Entries {
		got = append(got, filepath.Base(entry))
	}
	if strings.Join(got, ",") != "Documents,Projects" {
		t.Fatalf("visible home folders = %v", got)
	}
}

func TestWorkspaceDirectoryPickerSearchNavigateParentAndSelect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projects := filepath.Join(home, "Projects")
	child := filepath.Join(projects, "swarm")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.workspaceModalNew()
	p.openWorkspaceModalDirectoryPicker(p.workspaceModal.Editor)
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	picker := p.workspaceModal.Editor.DirectoryPicker
	if picker.Filter != "p" || len(picker.Entries) != 1 || picker.Entries[0] != projects {
		t.Fatalf("filtered picker = %#v", picker)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if picker.CurrentPath != projects || picker.Filter != "" || len(picker.Entries) != 1 || picker.Entries[0] != child {
		t.Fatalf("entered picker = %#v", picker)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if picker.CurrentPath != home {
		t.Fatalf("parent path = %q, want %q", picker.CurrentPath, home)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.workspaceModal.Editor.Selected != 1 || p.workspaceModal.Editor.Fields[0].Value != "~/Projects" {
		t.Fatalf("selected highlighted folder editor = %#v", p.workspaceModal.Editor)
	}
}

func TestWorkspaceDirectoryPickerScrollWindowTracksSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for i := 0; i < 20; i++ {
		if err := os.Mkdir(filepath.Join(home, fmt.Sprintf("folder-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.workspaceModalNew()
	picker := p.openWorkspaceModalDirectoryPicker(p.workspaceModal.Editor)
	picker.VisibleRows = 4
	p.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	if picker.Selected != 8 {
		t.Fatalf("selected = %d, want 8", picker.Selected)
	}
	start := workspaceModalDirectoryPickerWindowStart(len(picker.Entries), picker.Selected, picker.VisibleRows)
	if start != 5 || picker.Selected < start || picker.Selected >= start+picker.VisibleRows {
		t.Fatalf("window start=%d selected=%d visible=%d", start, picker.Selected, picker.VisibleRows)
	}
}

func TestWorkspaceDirectoryPickerEnterSelectsHighlightedFolderAndCtrlSDoesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	selected := filepath.Join(home, "Projects")
	if err := os.Mkdir(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.workspaceModalNew()
	p.openWorkspaceModalDirectoryPicker(p.workspaceModal.Editor)
	p.HandleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModCtrl))
	if p.workspaceModal.Editor.Selected != 0 || p.workspaceModal.Editor.Fields[0].Value != "~/" {
		t.Fatalf("Ctrl+S changed picker selection: %#v", p.workspaceModal.Editor)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.workspaceModal.Editor.Selected != 1 || p.workspaceModal.Editor.Fields[0].Value != "~/Projects" {
		t.Fatalf("highlighted selection editor = %#v", p.workspaceModal.Editor)
	}
}

// Requirement: the TUI workspace editor must reject a plain directory before it
// queues a catalog mutation. The threat is saving a workspace that cannot back a
// managed worktree; this editor submission test is the narrowest UI boundary.
func TestWorkspaceSetupRejectsNonRepositoryBeforeSave(t *testing.T) {
	path := t.TempDir()
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.OpenWorkspaceModalSaveEditor(path, true)
	for i := range p.workspaceModal.Editor.Fields {
		switch p.workspaceModal.Editor.Fields[i].Key {
		case "path":
			p.workspaceModal.Editor.Fields[i].Value = path
		case "name":
			p.workspaceModal.Editor.Fields[i].Value = "plain"
		case "save":
			p.workspaceModal.Editor.Selected = i
		}
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if _, ok := p.PopWorkspaceModalAction(); ok {
		t.Fatal("plain directory queued workspace save")
	}
	if !strings.Contains(p.workspaceModal.Error, "not a committed Git repository") || !strings.Contains(p.workspaceModal.Error, "managed worktrees") {
		t.Fatalf("plain directory guidance = %q", p.workspaceModal.Error)
	}
}

func TestWorkspaceSetupEndsWithSaveActionsAndSupportsDownNavigation(t *testing.T) {
	path := initWorkspaceModalGitRepository(t)
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.workspaceModalNew()
	if p.workspaceModal.Editor == nil {
		t.Fatal("expected workspace setup editor")
	}
	fields := p.workspaceModal.Editor.Fields
	if len(fields) < 2 || fields[len(fields)-2].Key != "save" || fields[len(fields)-2].Label != "Save" || fields[len(fields)-1].Key != "save_and_switch" || fields[len(fields)-1].Label != "Save and Switch" {
		t.Fatalf("workspace setup final fields = %#v", fields)
	}
	for _, field := range fields {
		if field.Key == "active" || strings.Contains(strings.ToLower(field.Label), "set active") {
			t.Fatalf("workspace setup retained active field: %#v", field)
		}
	}

	for i := range p.workspaceModal.Editor.Fields {
		switch p.workspaceModal.Editor.Fields[i].Key {
		case "path":
			p.workspaceModal.Editor.Fields[i].Value = path
		case "name":
			p.workspaceModal.Editor.Fields[i].Value = "new-workspace"
		}
	}
	p.workspaceModal.Editor.Selected = len(p.workspaceModal.Editor.Fields) - 2
	if got := p.workspaceModal.Editor.Fields[p.workspaceModal.Editor.Selected].Key; got != "save" {
		t.Fatalf("first Down selected %q, want save", got)
	}
	if got := workspaceModalEditorFieldLine(p.workspaceModal.Editor.Fields[p.workspaceModal.Editor.Selected], true); got != "> Save" {
		t.Fatalf("focused Save line = %q, want visible selection marker", got)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionSave || action.MakeCurrent {
		t.Fatalf("save action = %#v, ok=%v", action, ok)
	}

	p.workspaceModalNew()
	for i := range p.workspaceModal.Editor.Fields {
		switch p.workspaceModal.Editor.Fields[i].Key {
		case "path":
			p.workspaceModal.Editor.Fields[i].Value = path
		case "name":
			p.workspaceModal.Editor.Fields[i].Value = "new-workspace"
		}
	}
	p.workspaceModal.Editor.Selected = len(p.workspaceModal.Editor.Fields) - 2
	p.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := p.workspaceModal.Editor.Fields[p.workspaceModal.Editor.Selected].Key; got != "save_and_switch" {
		t.Fatalf("second Down selected %q, want save_and_switch", got)
	}
	if got := workspaceModalEditorFieldLine(p.workspaceModal.Editor.Fields[p.workspaceModal.Editor.Selected], true); got != "> Save and Switch" {
		t.Fatalf("focused Save and Switch line = %q, want visible selection marker", got)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := p.workspaceModal.Editor.Fields[p.workspaceModal.Editor.Selected].Key; got != "save_and_switch" {
		t.Fatalf("Down from final action selected %q, want to remain on save_and_switch", got)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok = p.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionSave || !action.MakeCurrent {
		t.Fatalf("save and switch action = %#v, ok=%v", action, ok)
	}
}
