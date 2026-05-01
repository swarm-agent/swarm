package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/ui"
)

const (
	quitChoiceKeepBackground = iota
	quitChoiceCloseSessions
)

type quitModalState struct {
	Visible            bool
	Selection          int
	RunningCount       int
	BlockedCount       int
	Interactive        bool
	ShutdownInProgress bool
	CancelRect         ui.Rect
	ConfirmRect        ui.Rect
}

func (a *App) interactiveLifecycleEnabled() bool {
	if a == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.homeModel.ServerMode)) {
	case "", "single":
		return true
	default:
		return false
	}
}

func (a *App) quitModalActive() bool {
	return a != nil && a.quitModal.Visible
}

func (a *App) closeQuitModal() {
	if a == nil {
		return
	}
	a.quitModal = quitModalState{}
}

func (a *App) openQuitModal(runningCount, blockedCount int) {
	if a == nil {
		return
	}
	a.quitModal = quitModalState{
		Visible:      true,
		Selection:    quitChoiceKeepBackground,
		RunningCount: runningCount,
		BlockedCount: blockedCount,
		Interactive:  a.interactiveLifecycleEnabled(),
	}
}

func (a *App) activeSessionExitCounts() (int, int) {
	if a == nil {
		return 0, 0
	}
	running := 0
	blocked := 0
	for _, summary := range a.homeModel.RecentSessions {
		pending := maxInt(0, summary.PendingPermissionCount)
		phase := ""
		active := false
		if summary.Lifecycle != nil {
			phase = strings.ToLower(strings.TrimSpace(summary.Lifecycle.Phase))
			active = summary.Lifecycle.Active
		}
		if pending > 0 || phase == "blocked" {
			blocked++
			continue
		}
		if active {
			running++
		}
	}
	return running, blocked
}

func (a *App) requestQuit() {
	if a == nil {
		return
	}
	runningCount, blockedCount := a.activeSessionExitCounts()
	if a.interactiveLifecycleEnabled() && (runningCount > 0 || blockedCount > 0) {
		a.openQuitModal(runningCount, blockedCount)
		return
	}
	a.finalizeQuit(false)
}

func (a *App) finalizeQuit(keepBackground bool) {
	if a == nil {
		return
	}
	a.closeQuitModal()
	if keepBackground || !a.interactiveLifecycleEnabled() {
		a.quitRequested = true
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
		}
		return
	}
	a.shutdownInteractiveDaemonAsync()
}

func (a *App) shutdownInteractiveDaemonAsync() {
	if a == nil {
		return
	}
	if a.quitModal.Visible {
		a.quitModal.ShutdownInProgress = true
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.api.Shutdown(ctx, "swarmtui exit")
		if err != nil {
			if a.route == "chat" && a.chat != nil {
				a.chat.SetStatus(fmt.Sprintf("exit failed: %v", err))
			} else if a.home != nil {
				a.home.SetStatus(fmt.Sprintf("exit failed: %v", err))
			}
			a.quitModal.ShutdownInProgress = false
			if a.screen != nil {
				a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
			}
			return
		}
		a.quitRequested = true
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
		}
	}()
}

func (a *App) handleQuitModalKey(ev *tcell.EventKey) bool {
	if a == nil || !a.quitModalActive() || ev == nil {
		return false
	}
	if a.keybinds == nil {
		a.keybinds = ui.NewDefaultKeyBindings()
	}
	if a.keybinds.Match(ev, ui.KeybindModalClose) || a.keybinds.Match(ev, ui.KeybindPlanExitCancel) {
		a.closeQuitModal()
		return true
	}
	if a.quitModal.ShutdownInProgress {
		return true
	}
	switch {
	case a.keybinds.Match(ev, ui.KeybindModalFocusLeft), a.keybinds.Match(ev, ui.KeybindModalMoveUp), a.keybinds.Match(ev, ui.KeybindModalMoveUpAlt):
		a.quitModal.Selection = quitChoiceKeepBackground
		return true
	case a.keybinds.Match(ev, ui.KeybindModalFocusRight), a.keybinds.Match(ev, ui.KeybindModalMoveDown), a.keybinds.Match(ev, ui.KeybindModalMoveDownAlt), a.keybinds.Match(ev, ui.KeybindModalFocusNext), a.keybinds.Match(ev, ui.KeybindModalFocusPrev), a.keybinds.Match(ev, ui.KeybindPlanExitToggle), a.keybinds.Match(ev, ui.KeybindPlanExitToggleLeft), a.keybinds.Match(ev, ui.KeybindPlanExitToggleRight):
		a.quitModal.Selection = 1 - a.quitModal.Selection
		return true
	case a.keybinds.Match(ev, ui.KeybindModalEnter), a.keybinds.Match(ev, ui.KeybindPlanExitConfirm):
		a.confirmQuitModalSelection()
		return true
	default:
		return true
	}
}

func (a *App) handleQuitModalMouse(ev *tcell.EventMouse) bool {
	if a == nil || !a.quitModalActive() || ev == nil {
		return false
	}
	if ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	x, y := ev.Position()
	if a.quitModal.CancelRect.Contains(x, y) {
		a.quitModal.Selection = quitChoiceKeepBackground
		a.confirmQuitModalSelection()
		return true
	}
	if a.quitModal.ConfirmRect.Contains(x, y) {
		a.quitModal.Selection = quitChoiceCloseSessions
		a.confirmQuitModalSelection()
		return true
	}
	return true
}

func (a *App) confirmQuitModalSelection() {
	if a == nil || !a.quitModalActive() || a.quitModal.ShutdownInProgress {
		return
	}
	keepBackground := a.quitModal.Selection == quitChoiceKeepBackground
	if !a.quitModal.Interactive {
		keepBackground = false
	}
	a.finalizeQuit(keepBackground)
}

func (a *App) drawQuitModal() {
	if a == nil || !a.quitModalActive() || a.screen == nil {
		return
	}
	w, h := a.screen.Size()
	if w < 48 || h < 12 {
		return
	}
	theme := a.effectiveThemeOption().Theme
	modalW := minInt(maxInt(56, w-12), 88)
	content := a.quitModalLines(modalW - 4)
	modalH := len(content) + 7
	if modalH > h-4 {
		modalH = h - 4
	}
	modal := ui.Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	ui.FillRect(a.screen, modal, theme.Panel)
	ui.DrawBox(a.screen, modal, theme.BorderActive)
	ui.DrawText(a.screen, modal.X+2, modal.Y+1, modal.W-4, theme.Warning.Bold(true), "Exit Swarm?")
	for i, line := range content {
		ui.DrawText(a.screen, modal.X+2, modal.Y+2+i, modal.W-4, theme.Text, line)
	}
	buttonY := modal.Y + modal.H - 2
	keepLabel := " Keep running in background "
	closeLabel := " Close sessions and exit "
	if !a.quitModal.Interactive {
		keepLabel = " Background keep-running unavailable "
	}
	if a.quitModal.ShutdownInProgress {
		closeLabel = " Closing interactive runtime... "
	}
	keepStyle := theme.Element
	closeStyle := theme.Element
	if a.quitModal.Selection == quitChoiceKeepBackground {
		keepStyle = theme.Accent
	} else {
		closeStyle = theme.Accent
	}
	keepW := len([]rune(keepLabel))
	closeW := len([]rune(closeLabel))
	gap := 2
	totalW := keepW + gap + closeW
	if totalW > modal.W-4 {
		keepLabel = " Keep background "
		keepW = len([]rune(keepLabel))
		totalW = keepW + gap + closeW
	}
	startX := modal.X + maxInt(2, (modal.W-totalW)/2)
	a.quitModal.CancelRect = ui.Rect{X: startX, Y: buttonY, W: keepW, H: 1}
	a.quitModal.ConfirmRect = ui.Rect{X: startX + keepW + gap, Y: buttonY, W: closeW, H: 1}
	ui.DrawText(a.screen, a.quitModal.CancelRect.X, buttonY, keepW, keepStyle, keepLabel)
	ui.DrawText(a.screen, a.quitModal.ConfirmRect.X, buttonY, closeW, closeStyle, closeLabel)
}

func (a *App) quitModalLines(width int) []string {
	if width < 16 {
		width = 16
	}
	lines := make([]string, 0, 8)
	if a.quitModal.RunningCount > 0 {
		lines = append(lines, wrapQuitText(fmt.Sprintf("%d running session(s) will be closed if you exit.", a.quitModal.RunningCount), width)...)
	}
	if a.quitModal.BlockedCount > 0 {
		lines = append(lines, wrapQuitText(fmt.Sprintf("%d session(s) are waiting for approval and will be closed if you exit.", a.quitModal.BlockedCount), width)...)
	}
	lines = append(lines, "")
	if a.quitModal.Interactive {
		lines = append(lines, wrapQuitText("Interactive mode only: you can instead leave this UI and keep Swarm running in the background.", width)...)
	} else {
		lines = append(lines, wrapQuitText("Background keep-running is only offered in interactive mode.", width)...)
	}
	return lines
}

func wrapQuitText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if width < 8 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}
	lines := make([]string, 0, len(words))
	current := words[0]
	for _, word := range words[1:] {
		candidate := current + " " + word
		if len([]rune(candidate)) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	lines = append(lines, current)
	return lines
}
