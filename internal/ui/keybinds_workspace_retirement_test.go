package ui

import (
	"strings"
	"testing"
)

func TestRemovedWorkspaceKeybindsAreNotRegisteredOrRestoredFromOverrides(t *testing.T) {
	removed := []KeybindID{
		"global.workspace_select",
		"global.workspace_prev",
		"global.workspace_next",
		"global.workspace_slot_1",
		"global.workspace_slot_2",
		"global.workspace_slot_3",
		"global.workspace_slot_4",
		"global.workspace_slot_5",
		"global.workspace_slot_6",
		"global.workspace_slot_7",
		"global.workspace_slot_8",
		"global.workspace_slot_9",
		"global.workspace_slot_10",
	}

	bindings := NewDefaultKeyBindings()
	overrides := make(map[string]string, len(removed))
	for _, id := range removed {
		if _, ok := LookupKeybindDefinition(id); ok {
			t.Fatalf("removed keybind %q is still registered", id)
		}
		overrides[string(id)] = "ctrl+g"
	}
	bindings.ApplyOverrides(overrides)
	if got := bindings.SerializeOverrides(); len(got) != 0 {
		t.Fatalf("stale workspace overrides were retained: %#v", got)
	}

	for _, def := range KeybindDefinitions() {
		if strings.HasPrefix(string(def.ID), "global.workspace_") {
			t.Fatalf("workspace keyboard definition remains: %#v", def)
		}
		if strings.HasPrefix(def.Default, "alt+") && len(def.Default) == len("alt+0") {
			last := def.Default[len(def.Default)-1]
			if last >= '0' && last <= '9' {
				t.Fatalf("workspace slot-shaped default remains: %#v", def)
			}
		}
	}
}
