package app

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleMouseCommand(args []string) {
	if len(args) == 0 {
		a.applyMouseSetting(!a.config.Input.MouseEnabled)
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "toggle":
		a.applyMouseSetting(!a.config.Input.MouseEnabled)
	case "on", "enable", "true", "1":
		a.applyMouseSetting(true)
	case "off", "disable", "false", "0":
		a.applyMouseSetting(false)
	case "status":
		a.home.ClearCommandOverlay()
		a.home.SetStatus("mouse capture " + enabledLabel(a.config.Input.MouseEnabled))
		a.showToast(ui.ToastInfo, a.mouseToastMessage("status"))
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /mouse [on|off|toggle|status]")
		a.showToast(ui.ToastWarning, "usage: /mouse [on|off|toggle|status]")
	}
}

func (a *App) applyMouseSetting(enabled bool) {
	a.config.Input.MouseEnabled = enabled
	a.setMouseCapture(enabled)
	a.mouseHintShown = false

	if err := saveAppConfig(a.api, a.config); err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("mouse capture %s (settings save failed: %v)", enabledLabel(enabled), err))
		a.showToast(ui.ToastWarning, fmt.Sprintf("mouse capture %s, but settings save failed", enabledLabel(enabled)))
		return
	}

	a.home.ClearCommandOverlay()
	if enabled {
		a.home.SetStatus("mouse capture on (F8 or /mouse off to disable)")
		a.showToast(ui.ToastSuccess, a.mouseToastMessage("on"))
		return
	}
	a.home.SetStatus("mouse capture off")
	a.showToast(ui.ToastInfo, a.mouseToastMessage("off"))
}

func (a *App) mouseToastMessage(mode string) string {
	switch mode {
	case "on":
		return "Mouse capture on. Shift+drag selects text. Press F8 or /mouse off to disable."
	case "off":
		return "Mouse capture off. Press F8 or /mouse on to enable click capture."
	default:
		return fmt.Sprintf("Mouse capture %s. Shift+drag selects text. F8 toggles.", enabledLabel(a.config.Input.MouseEnabled))
	}
}

func (a *App) setMouseCapture(enabled bool) {
	if a.screen == nil {
		return
	}
	a.screen.DisableMouse()
	if enabled {
		a.screen.EnableMouse(tcell.MouseButtonEvents | tcell.MouseMotionEvents)
	}
}
