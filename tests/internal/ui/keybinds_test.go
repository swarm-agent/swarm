package ui

import (
	"strings"
	"testing"
)

func TestCloseCancelBindingsDoNotUseSingleLetterDefaults(t *testing.T) {
	for _, def := range KeybindDefinitions() {
		action := strings.ToLower(strings.TrimSpace(def.Action))
		if !strings.Contains(action, "close") && !strings.Contains(action, "cancel") && !strings.Contains(action, "escape") {
			continue
		}

		token, err := NormalizeKeybindToken(def.Default)
		if err != nil {
			t.Fatalf("NormalizeKeybindToken(%q) for %s failed: %v", def.Default, def.ID, err)
		}
		if isSingleLetterOrDigitToken(token) {
			t.Fatalf("close/cancel keybind %s uses single-letter default %q", def.ID, token)
		}

		for _, alias := range def.Aliases {
			aliasToken, err := NormalizeKeybindToken(alias)
			if err != nil {
				t.Fatalf("NormalizeKeybindToken(%q) alias for %s failed: %v", alias, def.ID, err)
			}
			if isSingleLetterOrDigitToken(aliasToken) {
				t.Fatalf("close/cancel keybind %s uses single-letter alias %q", def.ID, aliasToken)
			}
		}
	}
}

func TestPlanExitAltBindingsRequireAltModifier(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindPlanExitMoveUpAlt); got != "alt+k" {
		t.Fatalf("KeybindPlanExitMoveUpAlt token = %q, want %q", got, "alt+k")
	}
	if got := keybinds.Token(KeybindPlanExitMoveDownAlt); got != "alt+j" {
		t.Fatalf("KeybindPlanExitMoveDownAlt token = %q, want %q", got, "alt+j")
	}
}

func TestModalAltBindingsRequireAltModifier(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindModalMoveUpAlt); got != "alt+k" {
		t.Fatalf("KeybindModalMoveUpAlt token = %q, want %q", got, "alt+k")
	}
	if got := keybinds.Token(KeybindModalMoveDownAlt); got != "alt+j" {
		t.Fatalf("KeybindModalMoveDownAlt token = %q, want %q", got, "alt+j")
	}
}

func TestChatAltBindingsRequireAltModifier(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindChatMoveUpAlt); got != "alt+k" {
		t.Fatalf("KeybindChatMoveUpAlt token = %q, want %q", got, "alt+k")
	}
	if got := keybinds.Token(KeybindChatMoveDownAlt); got != "alt+j" {
		t.Fatalf("KeybindChatMoveDownAlt token = %q, want %q", got, "alt+j")
	}
}

func TestKeybindsModalAltBindingsRequireAltModifier(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindKeybindsModalMoveUpAlt); got != "alt+k" {
		t.Fatalf("KeybindKeybindsModalMoveUpAlt token = %q, want %q", got, "alt+k")
	}
	if got := keybinds.Token(KeybindKeybindsModalMoveDownAlt); got != "alt+j" {
		t.Fatalf("KeybindKeybindsModalMoveDownAlt token = %q, want %q", got, "alt+j")
	}
}

func TestAuthVerifyBindingDefaultsToV(t *testing.T) {
	keybinds := NewDefaultKeyBindings()
	if got := keybinds.Token(KeybindAuthVerify); got != "v" {
		t.Fatalf("KeybindAuthVerify token = %q, want %q", got, "v")
	}
}

func isSingleLetterOrDigitToken(token string) bool {
	if len(token) != 1 {
		return false
	}
	b := token[0]
	if b >= 'a' && b <= 'z' {
		return true
	}
	if b >= '0' && b <= '9' {
		return true
	}
	return false
}
