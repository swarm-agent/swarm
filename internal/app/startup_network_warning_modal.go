package app

import (
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/ui"
)

type startupNetworkWarningModalState struct {
	Visible    bool
	Message    string
	ButtonRect ui.Rect
}

func (a *App) startupNetworkWarningModalActive() bool {
	return a != nil && a.startupNetworkWarningModal.Visible
}

func (a *App) openStartupNetworkWarningModal() {
	if a == nil {
		return
	}
	message := strings.TrimSpace(a.config.Startup.DirectLANDesktopWarning)
	if message == "" {
		return
	}
	a.startupNetworkWarningModal = startupNetworkWarningModalState{
		Visible: true,
		Message: message,
	}
	if a.home != nil {
		a.home.SetStatus("startup blocked: direct LAN desktop access is not safe in this MVP")
	}
}

func (a *App) exitFromStartupNetworkWarningModal() {
	if a == nil {
		return
	}
	a.startupNetworkWarningModal = startupNetworkWarningModalState{}
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}

func (a *App) handleStartupNetworkWarningModalKey(ev *tcell.EventKey) bool {
	if a == nil || !a.startupNetworkWarningModalActive() || ev == nil {
		return false
	}
	switch ev.Key() {
	case tcell.KeyEnter, tcell.KeyEsc, tcell.KeyCtrlC:
		a.exitFromStartupNetworkWarningModal()
		return true
	default:
		return true
	}
}

func (a *App) handleStartupNetworkWarningModalMouse(ev *tcell.EventMouse) bool {
	if a == nil || !a.startupNetworkWarningModalActive() || ev == nil {
		return false
	}
	if ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	x, y := ev.Position()
	if a.startupNetworkWarningModal.ButtonRect.Contains(x, y) {
		a.exitFromStartupNetworkWarningModal()
	}
	return true
}

func (a *App) drawStartupNetworkWarningModal() {
	if a == nil || !a.startupNetworkWarningModalActive() || a.screen == nil {
		return
	}
	w, h := a.screen.Size()
	if w < 44 || h < 12 {
		return
	}
	theme := a.effectiveThemeOption().Theme
	modalW := minInt(maxInt(58, w-12), 92)
	textW := modalW - 4
	lines := startupNetworkWarningLines(a.startupNetworkWarningModal.Message, textW)
	modalH := len(lines) + 7
	if modalH > h-4 {
		modalH = h - 4
	}
	modal := ui.Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	ui.FillRect(a.screen, modal, theme.Panel)
	ui.DrawBox(a.screen, modal, theme.Warning)
	ui.DrawText(a.screen, modal.X+2, modal.Y+1, modal.W-4, theme.Warning.Bold(true), "Unsafe LAN desktop config")
	for i, line := range lines {
		if modal.Y+2+i >= modal.Y+modal.H-3 {
			break
		}
		ui.DrawText(a.screen, modal.X+2, modal.Y+2+i, modal.W-4, theme.Text, line)
	}
	label := " OK, exit "
	buttonW := len([]rune(label))
	buttonX := modal.X + (modal.W-buttonW)/2
	buttonY := modal.Y + modal.H - 2
	a.startupNetworkWarningModal.ButtonRect = ui.Rect{X: buttonX, Y: buttonY, W: buttonW, H: 1}
	ui.DrawText(a.screen, buttonX, buttonY, buttonW, theme.Accent, label)
}

func startupNetworkWarningLines(message string, width int) []string {
	if width < 16 {
		width = 16
	}
	lines := make([]string, 0, 10)
	lines = append(lines, ui.Wrap(strings.TrimSpace(message), width)...)
	lines = append(lines, "")
	lines = append(lines, ui.Wrap("Edit swarm.conf, set host = 127.0.0.1, then restart Swarm. For another device, use ssh -L 5555:127.0.0.1:5555 <host> or use Tailscale.", width)...)
	lines = append(lines, "")
	lines = append(lines, "Press Enter or Esc to exit.")
	return lines
}
