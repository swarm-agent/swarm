package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestWorkspaceKeybindsAreRegisteredAndRestoreOverrides(t *testing.T) {
	wantDefaults := map[KeybindID]string{
		KeybindGlobalWorkspaceSelect: "alt+w",
	}
	for slot := 1; slot <= WorkspaceSlotCount; slot++ {
		id, ok := WorkspaceSlotKeybindID(slot)
		if !ok {
			t.Fatalf("workspace slot %d has no keybind id", slot)
		}
		key := slot
		if slot == 10 {
			key = 0
		}
		wantDefaults[id] = "alt+" + string(rune('0'+key))
	}

	bindings := NewDefaultKeyBindings()
	for id, want := range wantDefaults {
		def, ok := LookupKeybindDefinition(id)
		if !ok {
			t.Fatalf("workspace keybind %q is not registered", id)
		}
		if !def.Editable || def.Group != "Global" || def.Default != want {
			t.Fatalf("workspace keybind definition = %#v, want editable Global default %q", def, want)
		}
		if got := bindings.Token(id); got != want {
			t.Fatalf("workspace keybind %q token = %q, want %q", id, got, want)
		}
		keyRune := rune(want[len(want)-1])
		if !bindings.Match(tcell.NewEventKey(tcell.KeyRune, keyRune, tcell.ModAlt), id) {
			t.Fatalf("workspace keybind %q did not match %q event", id, want)
		}
	}

	if !bindings.Match(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModAlt), KeybindGlobalWorkspaceSelect) {
		t.Fatal("workspace selector did not match Alt+W event")
	}

	bindings.ApplyOverrides(map[string]string{string(KeybindGlobalWorkspaceSelect): "ctrl+g"})
	if got := bindings.SerializeOverrides()[string(KeybindGlobalWorkspaceSelect)]; got != "ctrl+g" {
		t.Fatalf("workspace selector override = %q, want ctrl+g", got)
	}

	registered := 0
	for _, def := range KeybindDefinitions() {
		if strings.HasPrefix(string(def.ID), "global.workspace_") {
			registered++
		}
	}
	if registered != WorkspaceSlotCount+1 {
		t.Fatalf("registered workspace keybinds = %d, want %d", registered, WorkspaceSlotCount+1)
	}
	if _, ok := WorkspaceSlotKeybindID(0); ok {
		t.Fatal("slot 0 unexpectedly has a keybind id")
	}
	if _, ok := WorkspaceSlotKeybindID(WorkspaceSlotCount + 1); ok {
		t.Fatal("out-of-range slot unexpectedly has a keybind id")
	}
}
