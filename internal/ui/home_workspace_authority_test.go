package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: submitWorkspaceModalEditor must send workspace validation to the
// authenticated daemon, whose filesystem identity can differ from the TUI's.
// A client-side Git ownership rejection must not prevent that request. This UI
// boundary test injects a failing Git executable and asserts the requested path
// and switch intent, not successful persistence (which remains daemon-owned).
func TestWorkspaceSaveDefersGitValidationToDaemon(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called > \"$GIT_TEST_MARKER\"\necho 'fatal: detected dubious ownership in repository' >&2\nexit 128\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_TEST_MARKER", marker)
	path := t.TempDir()
	p := NewHomePage(model.EmptyHome())
	p.ShowWorkspaceModal()
	p.OpenWorkspaceModalSaveAndSwitchEditor(path, true)
	p.workspaceModal.Editor.Selected = len(p.workspaceModal.Editor.Fields) - 1
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionSave || action.Path != path || !action.MakeCurrent {
		t.Fatalf("daemon validation request missing: action=%+v ok=%v error=%q", action, ok, p.workspaceModal.Error)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("TUI executed Git instead of deferring to daemon: %v", err)
	}
}
