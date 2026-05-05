package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestDefaultEnterKeybindsRemainAllowed(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindChatSubmit); got != "enter" {
		t.Fatalf("KeybindChatSubmit token = %q, want %q", got, "enter")
	}
}

func TestSetRejectsBareEnterCustomKeybind(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	before := keybinds.Token(KeybindGlobalOpenAgents)
	if err := keybinds.Set(KeybindGlobalOpenAgents, "enter"); err == nil {
		t.Fatal("expected bare Enter custom keybind to be rejected")
	}
	if got := keybinds.Token(KeybindGlobalOpenAgents); got != before {
		t.Fatalf("token changed after rejected Enter bind: got %q, want %q", got, before)
	}
}

func TestSetRejectsReturnAliasCustomKeybind(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if err := keybinds.Set(KeybindGlobalOpenAgents, "return"); err == nil {
		t.Fatal("expected Return alias custom keybind to be rejected")
	}
}

func TestSetFromEventRejectsEnterCustomKeybind(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	before := keybinds.Token(KeybindGlobalOpenAgents)
	err := keybinds.SetFromEvent(KeybindGlobalOpenAgents, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if err == nil {
		t.Fatal("expected captured Enter custom keybind to be rejected")
	}
	if got := keybinds.Token(KeybindGlobalOpenAgents); got != before {
		t.Fatalf("token changed after rejected Enter event: got %q, want %q", got, before)
	}
}

func TestKeybindsModalRejectsAccidentalEnterEdit(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowKeybindsModal()
	p.keybindsModal.Editing = true
	p.keybindsModal.EditingID = KeybindGlobalOpenAgents

	before := p.keybinds.Token(KeybindGlobalOpenAgents)
	p.handleKeybindsModalKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if got := p.keybinds.Token(KeybindGlobalOpenAgents); got != before {
		t.Fatalf("token changed after rejected Enter edit: got %q, want %q", got, before)
	}
	if !p.keybindsModal.Editing {
		t.Fatal("editing should remain active after rejected Enter edit")
	}
	if !strings.Contains(p.keybindsModal.Status, "Enter cannot be assigned") {
		t.Fatalf("status = %q, want Enter rejection", p.keybindsModal.Status)
	}
	if action, ok := p.PopKeybindsModalAction(); ok {
		t.Fatalf("unexpected persist action after rejected Enter edit: %#v", action)
	}
}
