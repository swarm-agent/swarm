package app

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleKeybindsCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") {
		a.openKeybindsModal()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "list":
		a.home.SetCommandOverlay(a.keybindListLines())
		a.home.SetStatus("keybind list")
	case "reset":
		if len(args) > 1 && strings.EqualFold(args[1], "all") {
			a.activeKeyBindings().ResetAll()
			a.persistKeybindConfig("reset all keybinds")
			return
		}
		a.openKeybindsModal()
		a.home.SetKeybindsModalStatus("Select a keybind and press r to reset")
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /keybinds [open|list|reset [all]]")
	}
}

func (a *App) openKeybindsModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.ShowKeybindsModal()
	a.home.SetStatus("keybind modal")
}

func (a *App) handleKeybindsModalAction(action ui.KeybindsModalAction) {
	if action.Kind != ui.KeybindsModalActionPersist {
		return
	}
	a.persistKeybindConfig(action.StatusHint)
}

func (a *App) persistKeybindConfig(statusHint string) {
	if err := a.saveKeybindConfig(statusHint, true); err != nil {
		a.home.SetStatus(fmt.Sprintf("keybind save failed: %v", err))
		a.showToast(ui.ToastWarning, fmt.Sprintf("keybind save failed: %v", err))
	}
}

func (a *App) saveKeybindConfig(statusHint string, showToast bool) error {
	a.config.Input.Keybinds = a.activeKeyBindings().SerializeOverrides()
	if err := saveInputSettings(a.api, a.config.Input); err != nil {
		return err
	}
	statusHint = strings.TrimSpace(statusHint)
	if statusHint == "" {
		statusHint = "keybinds saved"
	}
	a.home.SetStatus(statusHint)
	if showToast {
		a.showToast(ui.ToastSuccess, "keybinds saved")
	}
	return nil
}

func (a *App) keybindListLines() []string {
	defs := ui.KeybindDefinitions()
	lines := make([]string, 0, len(defs)+4)
	lines = append(lines, fmt.Sprintf("keybinds: %d", len(defs)))
	for i, def := range defs {
		label := a.activeKeyBindings().Label(def.ID)
		lines = append(lines, fmt.Sprintf("- [%s] %s = %s", def.Group, def.Action, label))
		if i >= 29 {
			break
		}
	}
	if len(defs) > 30 {
		lines = append(lines, fmt.Sprintf("... %d more", len(defs)-30))
	}
	lines = append(lines, "Use /keybinds for full editor")
	return lines
}

func (a *App) activeKeyBindings() *ui.KeyBindings {
	if a.keybinds == nil {
		a.keybinds = ui.NewDefaultKeyBindings()
		a.keybinds.ApplyOverrides(a.config.Input.Keybinds)
		if a.home != nil {
			a.home.SetKeyBindings(a.keybinds)
		}
		if a.chat != nil {
			a.chat.SetKeyBindings(a.keybinds)
		}
	}
	return a.keybinds
}
