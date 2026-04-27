package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/ui"
)

const permissionsBypassWarningText = "This will turn OFF permissions, and may be dangerous depending on how you set up your environment."

type permissionsBypassModalState struct {
	Visible bool
	Busy    bool
	Rect    ui.Rect
	Cancel  ui.Rect
	Confirm ui.Rect
}

func (a *App) permissionsBypassModalActive() bool {
	return a != nil && a.permissionsBypassModal.Visible
}

func (a *App) openPermissionsBypassModal() {
	if a == nil {
		return
	}
	a.permissionsBypassModal = permissionsBypassModalState{Visible: true}
}

func (a *App) closePermissionsBypassModal() {
	if a == nil || a.permissionsBypassModal.Busy {
		return
	}
	a.permissionsBypassModal = permissionsBypassModalState{}
}

func (a *App) setPermissionsBypass(enabled bool) {
	if a == nil || a.api == nil {
		return
	}
	if enabled && !a.homeModel.BypassPermissions {
		a.openPermissionsBypassModal()
		return
	}
	a.applyPermissionsBypass(enabled)
}

func (a *App) applyPermissionsBypass(enabled bool) {
	if a == nil || a.api == nil {
		return
	}
	if a.permissionsBypassModal.Visible {
		a.permissionsBypassModal.Busy = true
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		saved, err := a.api.SetBypassPermissions(ctx, enabled)
		if err != nil {
			status := fmt.Sprintf("permissions toggle failed: %v", err)
			a.setPermissionsBypassStatus(status)
			if a.permissionsPolicyModal.Visible {
				a.permissionsPolicyModal.Busy = false
				a.permissionsPolicyModal.Err = status
			}
			if a.permissionsBypassModal.Visible {
				a.permissionsBypassModal.Busy = false
			}
			if a.screen != nil {
				a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
			}
			return
		}
		a.homeModel.BypassPermissions = saved
		if a.home != nil {
			a.home.SetModel(a.homeModel)
		}
		if a.chat != nil {
			meta := a.chat.Meta()
			meta.BypassPermissions = saved
			a.chat.SetMeta(meta)
		}
		a.permissionsBypassModal = permissionsBypassModalState{}
		status := "Permissions ON: prompts enabled"
		if saved {
			status = "Permissions OFF: bypass permissions enabled"
		}
		if a.permissionsPolicyModal.Visible {
			a.permissionsPolicyModal.Busy = false
			a.permissionsPolicyModal.Err = ""
			a.permissionsPolicyModal.Status = status
		}
		a.setPermissionsBypassStatus(status)
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
		}
	}()
}

func (a *App) setPermissionsBypassStatus(status string) {
	status = strings.TrimSpace(status)
	if status == "" || a == nil {
		return
	}
	if a.route == "chat" && a.chat != nil {
		a.chat.SetStatus(status)
	}
	if a.home != nil {
		a.home.SetStatus(status)
	}
}

func (a *App) handlePermissionsBypassModalKey(ev *tcell.EventKey) bool {
	if a == nil || !a.permissionsBypassModalActive() || ev == nil {
		return false
	}
	if a.keybinds == nil {
		a.keybinds = ui.NewDefaultKeyBindings()
	}
	if a.keybinds.Match(ev, ui.KeybindModalClose) || a.keybinds.Match(ev, ui.KeybindPlanExitCancel) {
		a.closePermissionsBypassModal()
		return true
	}
	if a.permissionsBypassModal.Busy {
		return true
	}
	if a.keybinds.Match(ev, ui.KeybindModalEnter) || a.keybinds.Match(ev, ui.KeybindPlanExitConfirm) {
		a.applyPermissionsBypass(true)
		return true
	}
	return true
}

func (a *App) handlePermissionsBypassModalMouse(ev *tcell.EventMouse) bool {
	if a == nil || !a.permissionsBypassModalActive() || ev == nil {
		return false
	}
	if ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	x, y := ev.Position()
	if a.permissionsBypassModal.Cancel.Contains(x, y) {
		a.closePermissionsBypassModal()
		return true
	}
	if a.permissionsBypassModal.Confirm.Contains(x, y) && !a.permissionsBypassModal.Busy {
		a.applyPermissionsBypass(true)
		return true
	}
	return true
}

func (a *App) drawPermissionsBypassModal() {
	if a == nil || !a.permissionsBypassModalActive() || a.screen == nil {
		return
	}
	w, h := a.screen.Size()
	if w < 48 || h < 10 {
		return
	}
	theme := a.effectiveThemeOption().Theme
	modalW := minInt(maxInt(56, w-12), 78)
	lines := wrapQuitText(permissionsBypassWarningText, modalW-4)
	modalH := len(lines) + 7
	modal := ui.Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	a.permissionsBypassModal.Rect = modal
	ui.FillRect(a.screen, modal, theme.Panel)
	ui.DrawBox(a.screen, modal, theme.BorderActive)
	ui.DrawText(a.screen, modal.X+2, modal.Y+1, modal.W-4, theme.Warning.Bold(true), "Turn OFF permissions?")
	for i, line := range lines {
		ui.DrawText(a.screen, modal.X+2, modal.Y+3+i, modal.W-4, theme.Text, line)
	}
	buttonY := modal.Y + modal.H - 2
	cancelLabel := " Cancel "
	confirmLabel := " Turn permissions OFF "
	if a.permissionsBypassModal.Busy {
		confirmLabel = " Saving... "
	}
	cancelW := len([]rune(cancelLabel))
	confirmW := len([]rune(confirmLabel))
	gap := 2
	startX := modal.X + maxInt(2, (modal.W-cancelW-gap-confirmW)/2)
	a.permissionsBypassModal.Cancel = ui.Rect{X: startX, Y: buttonY, W: cancelW, H: 1}
	a.permissionsBypassModal.Confirm = ui.Rect{X: startX + cancelW + gap, Y: buttonY, W: confirmW, H: 1}
	ui.DrawText(a.screen, a.permissionsBypassModal.Cancel.X, buttonY, cancelW, theme.Element, cancelLabel)
	ui.DrawText(a.screen, a.permissionsBypassModal.Confirm.X, buttonY, confirmW, theme.Accent, confirmLabel)
}
