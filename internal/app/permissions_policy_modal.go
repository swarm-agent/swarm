package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

type permissionsPolicyModalState struct {
	Visible     bool
	Policy      client.PermissionPolicy
	Title       string
	Scroll      int
	Selected    int
	Input       string
	InputActive bool
	Busy        bool
	Status      string
	Err         string
	Rect        ui.Rect
	RulesRect   ui.Rect
	InputRect   ui.Rect
	Add         ui.Rect
	TurnOff     ui.Rect
	Remove      ui.Rect
	Close       ui.Rect
}

func (a *App) permissionsPolicyModalActive() bool {
	return a != nil && a.permissionsPolicyModal.Visible
}

func (a *App) openPermissionsPolicyModal(policy client.PermissionPolicy) {
	if a == nil {
		return
	}
	selected := 0
	if len(policy.Rules) == 0 {
		selected = -1
	}
	status := "Press a to add a trusted bash command prefix. Press o to turn permissions OFF."
	if a.homeModel.BypassPermissions {
		status = "Permissions are OFF. Press o to turn permissions ON again."
	}
	a.permissionsPolicyModal = permissionsPolicyModalState{
		Visible:  true,
		Policy:   policy,
		Title:    fmt.Sprintf("Permissions · policy v%d", policy.Version),
		Selected: selected,
		Status:   status,
	}
}

func (a *App) closePermissionsPolicyModal() {
	if a == nil || a.permissionsPolicyModal.Busy {
		return
	}
	a.permissionsPolicyModal = permissionsPolicyModalState{}
}

func (a *App) handlePermissionsPolicyModalKey(ev *tcell.EventKey) bool {
	if a == nil || !a.permissionsPolicyModalActive() || ev == nil {
		return false
	}
	if a.keybinds == nil {
		a.keybinds = ui.NewDefaultKeyBindings()
	}
	m := &a.permissionsPolicyModal

	if m.InputActive {
		if a.handlePermissionsPolicyModalInputKey(ev) {
			return true
		}
	}

	if ev.Key() == tcell.KeyRune {
		switch unicode.ToLower(ev.Rune()) {
		case 'a', 'i':
			m.InputActive = true
			m.Err = ""
			m.Status = "Type a bash command prefix to always allow, then press Enter."
			return true
		case 'o':
			a.togglePermissionsFromPolicyModal()
			return true
		case 'r', 'd':
			a.removeSelectedPermissionsPolicyRule()
			return true
		case 'q':
			a.closePermissionsPolicyModal()
			return true
		}
	}

	if a.keybinds.Match(ev, ui.KeybindModalClose) || a.keybinds.Match(ev, ui.KeybindPlanExitCancel) {
		a.closePermissionsPolicyModal()
		return true
	}
	if a.keybinds.Match(ev, ui.KeybindModalEnter) {
		m.InputActive = true
		m.Err = ""
		m.Status = "Type a bash command prefix to always allow, then press Enter."
		return true
	}
	switch {
	case a.keybinds.Match(ev, ui.KeybindModalMoveUp), a.keybinds.Match(ev, ui.KeybindModalMoveUpAlt):
		a.movePermissionsPolicySelection(-1)
	case a.keybinds.Match(ev, ui.KeybindModalMoveDown), a.keybinds.Match(ev, ui.KeybindModalMoveDownAlt):
		a.movePermissionsPolicySelection(1)
	case a.keybinds.Match(ev, ui.KeybindChatPageUp):
		m.Scroll -= 6
	case a.keybinds.Match(ev, ui.KeybindChatPageDown):
		m.Scroll += 6
	case a.keybinds.Match(ev, ui.KeybindChatJumpHome):
		m.Selected = 0
		m.Scroll = 0
	case a.keybinds.Match(ev, ui.KeybindChatJumpEnd):
		m.Selected = len(m.Policy.Rules) - 1
		m.Scroll = len(m.Policy.Rules)
	}
	a.clampPermissionsPolicyModalScroll()
	return true
}

func (a *App) handlePermissionsPolicyModalInputKey(ev *tcell.EventKey) bool {
	m := &a.permissionsPolicyModal
	switch ev.Key() {
	case tcell.KeyEnter:
		a.savePermissionsPolicyModalAllowCommand()
		return true
	case tcell.KeyEsc:
		if strings.TrimSpace(m.Input) != "" {
			m.Input = ""
			m.Err = ""
			m.Status = "Draft cleared. Press Esc again to close."
			return true
		}
		m.InputActive = false
		return false
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		m.Input = trimLastRune(m.Input)
		return true
	case tcell.KeyCtrlU:
		m.Input = ""
		return true
	case tcell.KeyRune:
		if !m.Busy && unicode.IsPrint(ev.Rune()) {
			m.Input += string(ev.Rune())
		}
		return true
	}
	return false
}

func (a *App) handlePermissionsPolicyModalMouse(ev *tcell.EventMouse) bool {
	if a == nil || !a.permissionsPolicyModalActive() || ev == nil {
		return false
	}
	m := &a.permissionsPolicyModal
	buttons := ev.Buttons()
	if buttons&tcell.WheelUp != 0 {
		m.Scroll--
		a.clampPermissionsPolicyModalScroll()
		return true
	}
	if buttons&tcell.WheelDown != 0 {
		m.Scroll++
		a.clampPermissionsPolicyModalScroll()
		return true
	}
	if buttons&tcell.Button1 == 0 {
		return true
	}
	x, y := ev.Position()
	switch {
	case m.Close.Contains(x, y):
		a.closePermissionsPolicyModal()
	case m.TurnOff.Contains(x, y):
		a.togglePermissionsFromPolicyModal()
	case m.Add.Contains(x, y):
		a.savePermissionsPolicyModalAllowCommand()
	case m.Remove.Contains(x, y):
		a.removeSelectedPermissionsPolicyRule()
	case m.InputRect.Contains(x, y):
		m.InputActive = true
		m.Err = ""
		m.Status = "Type a bash command prefix to always allow, then press Enter."
	case m.RulesRect.Contains(x, y):
		row := y - m.RulesRect.Y
		idx := m.Scroll + row
		if idx >= 0 && idx < len(m.Policy.Rules) {
			m.Selected = idx
			a.clampPermissionsPolicyModalScroll()
		}
	}
	return true
}

func (a *App) togglePermissionsFromPolicyModal() {
	if a == nil || !a.permissionsPolicyModalActive() {
		return
	}
	m := &a.permissionsPolicyModal
	m.Err = ""
	if a.homeModel.BypassPermissions {
		m.Status = "Turning permissions ON..."
		a.applyPermissionsBypass(false)
		return
	}
	a.openPermissionsBypassModal()
}

func (a *App) savePermissionsPolicyModalAllowCommand() {
	if a == nil || a.api == nil || !a.permissionsPolicyModalActive() {
		return
	}
	m := &a.permissionsPolicyModal
	if m.Busy {
		return
	}
	pattern := strings.TrimSpace(m.Input)
	if pattern == "" {
		m.InputActive = true
		m.Err = "Enter a bash command prefix first. Example: go test ./..."
		return
	}
	m.Busy = true
	m.Err = ""
	m.Status = "Saving always-allow command prefix..."
	rule := client.PermissionRule{Decision: "allow", Kind: "bash_prefix", Tool: "bash", Pattern: pattern}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		saved, err := a.api.AddPermissionRule(ctx, rule)
		if err != nil {
			if a.permissionsPolicyModal.Visible {
				a.permissionsPolicyModal.Busy = false
				a.permissionsPolicyModal.Err = fmt.Sprintf("save failed: %v", err)
			}
			if a.screen != nil {
				a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
			}
			return
		}
		if a.permissionsPolicyModal.Visible {
			m := &a.permissionsPolicyModal
			m.Policy.Rules = append(m.Policy.Rules, saved)
			m.Selected = len(m.Policy.Rules) - 1
			m.Input = ""
			m.InputActive = true
			m.Busy = false
			m.Err = ""
			m.Status = "Always allow saved: " + permissionsPolicyRuleTarget(saved)
			m.Scroll = len(m.Policy.Rules)
			a.clampPermissionsPolicyModalScroll()
		}
		if a.home != nil {
			a.home.SetStatus("permission rule saved: " + strings.TrimSpace(saved.ID))
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
		}
	}()
}

func (a *App) removeSelectedPermissionsPolicyRule() {
	if a == nil || a.api == nil || !a.permissionsPolicyModalActive() {
		return
	}
	m := &a.permissionsPolicyModal
	if m.Busy || m.Selected < 0 || m.Selected >= len(m.Policy.Rules) {
		return
	}
	rule := m.Policy.Rules[m.Selected]
	ruleID := strings.TrimSpace(rule.ID)
	if ruleID == "" {
		m.Err = "Selected rule has no id."
		return
	}
	m.Busy = true
	m.Err = ""
	m.Status = "Removing rule " + ruleID + "..."
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		removed, err := a.api.RemovePermissionRule(ctx, ruleID)
		if err != nil {
			if a.permissionsPolicyModal.Visible {
				a.permissionsPolicyModal.Busy = false
				a.permissionsPolicyModal.Err = fmt.Sprintf("remove failed: %v", err)
			}
			if a.screen != nil {
				a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
			}
			return
		}
		if a.permissionsPolicyModal.Visible {
			m := &a.permissionsPolicyModal
			m.Busy = false
			if removed {
				idx := m.Selected
				m.Policy.Rules = append(m.Policy.Rules[:idx], m.Policy.Rules[idx+1:]...)
				if len(m.Policy.Rules) == 0 {
					m.Selected = -1
				} else if idx >= len(m.Policy.Rules) {
					m.Selected = len(m.Policy.Rules) - 1
				}
				m.Err = ""
				m.Status = "Rule removed."
			} else {
				m.Err = "Rule was not found."
			}
			m.Scroll = minInt(m.Scroll, maxInt(0, len(m.Policy.Rules)-1))
			a.clampPermissionsPolicyModalScroll()
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
		}
	}()
}

func (a *App) movePermissionsPolicySelection(delta int) {
	if a == nil || !a.permissionsPolicyModalActive() {
		return
	}
	m := &a.permissionsPolicyModal
	if len(m.Policy.Rules) == 0 {
		m.Selected = -1
		m.Scroll = 0
		return
	}
	if m.Selected < 0 {
		m.Selected = 0
	} else {
		m.Selected += delta
	}
	if m.Selected < 0 {
		m.Selected = 0
	}
	if m.Selected >= len(m.Policy.Rules) {
		m.Selected = len(m.Policy.Rules) - 1
	}
	visibleRows := a.permissionsPolicyModalVisibleRows()
	if m.Selected < m.Scroll {
		m.Scroll = m.Selected
	}
	if visibleRows > 0 && m.Selected >= m.Scroll+visibleRows {
		m.Scroll = m.Selected - visibleRows + 1
	}
}

func (a *App) clampPermissionsPolicyModalScroll() {
	if a == nil {
		return
	}
	m := &a.permissionsPolicyModal
	visibleRows := a.permissionsPolicyModalVisibleRows()
	maxScroll := maxInt(0, len(m.Policy.Rules)-visibleRows)
	if m.Scroll < 0 {
		m.Scroll = 0
	}
	if m.Scroll > maxScroll {
		m.Scroll = maxScroll
	}
	if len(m.Policy.Rules) == 0 {
		m.Selected = -1
		return
	}
	if m.Selected < 0 {
		m.Selected = 0
	}
	if m.Selected >= len(m.Policy.Rules) {
		m.Selected = len(m.Policy.Rules) - 1
	}
}

func (a *App) permissionsPolicyModalVisibleRows() int {
	if a == nil || !a.permissionsPolicyModalActive() || a.screen == nil {
		return 0
	}
	_, h := a.screen.Size()
	modalH := minInt(maxInt(18, h-4), 30)
	return maxInt(1, modalH-12)
}

func (a *App) drawPermissionsPolicyModal() {
	if a == nil || !a.permissionsPolicyModalActive() || a.screen == nil {
		return
	}
	w, h := a.screen.Size()
	if w < 58 || h < 16 {
		return
	}
	theme := a.effectiveThemeOption().Theme
	modalW := minInt(maxInt(78, w-8), 118)
	modalH := minInt(maxInt(18, h-4), 30)
	modal := ui.Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	m := &a.permissionsPolicyModal
	m.Rect = modal

	ui.FillRect(a.screen, modal, theme.Panel)
	ui.DrawBox(a.screen, modal, theme.BorderActive)

	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "Permissions"
	}
	ui.DrawText(a.screen, modal.X+2, modal.Y+1, modal.W-4, theme.Accent.Bold(true), title)
	statusLabel := "ON · prompts enforced"
	statusStyle := theme.Success.Bold(true)
	if a.homeModel.BypassPermissions {
		statusLabel = "OFF · bypass enabled"
		statusStyle = theme.Warning.Bold(true)
	}
	ui.DrawTextRight(a.screen, modal.X+modal.W-3, modal.Y+1, 28, statusStyle, statusLabel)

	subtitle := "Allow trusted commands once here instead of answering future prompts. Press o to toggle permissions ON/OFF."
	ui.DrawText(a.screen, modal.X+2, modal.Y+2, modal.W-4, theme.TextMuted, permissionsModalClamp(subtitle, modal.W-4))

	contentY := modal.Y + 4
	contentH := modal.H - 9
	if contentH < 5 {
		contentH = 5
	}
	leftW := maxInt(34, (modal.W*58)/100)
	if leftW > modal.W-28 {
		leftW = modal.W - 28
	}
	rightW := modal.W - leftW - 7
	if rightW < 24 {
		rightW = 24
		leftW = modal.W - rightW - 7
	}
	rulesRect := ui.Rect{X: modal.X + 2, Y: contentY + 2, W: leftW, H: maxInt(1, contentH-2)}
	a.drawPermissionsPolicyRules(theme, ui.Rect{X: modal.X + 1, Y: contentY, W: leftW + 2, H: contentH})
	m.RulesRect = rulesRect

	right := ui.Rect{X: modal.X + leftW + 5, Y: contentY, W: rightW, H: contentH}
	a.drawPermissionsPolicyComposer(theme, right)

	messageY := modal.Y + modal.H - 4
	message := strings.TrimSpace(m.Status)
	messageStyle := theme.TextMuted
	if strings.TrimSpace(m.Err) != "" {
		message = m.Err
		messageStyle = theme.Error
	}
	if message == "" {
		message = "↑/↓ select · a add command · r remove selected · o turn OFF · Esc close"
	}
	ui.DrawText(a.screen, modal.X+2, messageY, modal.W-4, messageStyle, permissionsModalClamp(message, modal.W-4))

	buttonY := modal.Y + modal.H - 2
	x := modal.X + 2
	turnOffLabel := " o Turn permissions OFF "
	if a.homeModel.BypassPermissions {
		turnOffLabel = " o Turn permissions ON "
	}
	m.TurnOff = drawPermissionsPolicyButton(a.screen, x, buttonY, turnOffLabel, theme.Warning)
	x += m.TurnOff.W + 2
	m.Add = drawPermissionsPolicyButton(a.screen, x, buttonY, " Enter Save always allow ", theme.Accent)
	x += m.Add.W + 2
	m.Remove = drawPermissionsPolicyButton(a.screen, x, buttonY, " r Remove rule ", theme.Element)
	closeLabel := " Esc Close "
	closeW := utf8.RuneCountInString(closeLabel)
	m.Close = ui.Rect{X: modal.X + modal.W - closeW - 2, Y: buttonY, W: closeW, H: 1}
	ui.DrawText(a.screen, m.Close.X, buttonY, m.Close.W, theme.Primary, closeLabel)
}

func (a *App) drawPermissionsPolicyRules(theme ui.Theme, panel ui.Rect) {
	m := &a.permissionsPolicyModal
	ui.DrawBox(a.screen, panel, theme.Border)
	ui.DrawText(a.screen, panel.X+2, panel.Y, panel.W-4, theme.TextMuted.Bold(true), " Current rules ")
	count := len(m.Policy.Rules)
	if count == 0 {
		lines := ui.Wrap("No explicit rules yet. Add a command prefix on the right to always allow trusted bash commands.", panel.W-4)
		for i, line := range lines {
			if i >= panel.H-2 {
				break
			}
			ui.DrawText(a.screen, panel.X+2, panel.Y+2+i, panel.W-4, theme.TextMuted, line)
		}
		return
	}
	visibleRows := maxInt(1, panel.H-3)
	m.RulesRect = ui.Rect{X: panel.X + 1, Y: panel.Y + 2, W: panel.W - 2, H: visibleRows}
	a.clampPermissionsPolicyModalScroll()
	start := m.Scroll
	end := minInt(count, start+visibleRows)
	for idx := start; idx < end; idx++ {
		rowY := m.RulesRect.Y + idx - start
		rule := m.Policy.Rules[idx]
		rowStyle := theme.Text
		prefix := "  "
		if idx == m.Selected {
			rowStyle = theme.Primary.Bold(true)
			prefix = "› "
			ui.FillRect(a.screen, ui.Rect{X: m.RulesRect.X, Y: rowY, W: m.RulesRect.W, H: 1}, theme.Element)
		}
		label := prefix + permissionsPolicyRuleLabel(rule)
		ui.DrawText(a.screen, m.RulesRect.X+1, rowY, m.RulesRect.W-2, rowStyle, permissionsModalClamp(label, m.RulesRect.W-2))
	}
	if count > visibleRows {
		counter := fmt.Sprintf("%d-%d/%d", start+1, end, count)
		ui.DrawTextRight(a.screen, panel.X+panel.W-2, panel.Y, 16, theme.TextMuted, counter)
	}
}

func (a *App) drawPermissionsPolicyComposer(theme ui.Theme, rect ui.Rect) {
	m := &a.permissionsPolicyModal
	ui.DrawBox(a.screen, rect, theme.Border)
	ui.DrawText(a.screen, rect.X+2, rect.Y, rect.W-4, theme.TextMuted.Bold(true), " Always allow command ")
	lines := []string{
		"Add a bash command prefix you trust.",
		"Examples: go test ./..., npm test, make build",
	}
	y := rect.Y + 2
	for _, line := range lines {
		for _, wrapped := range ui.Wrap(line, rect.W-4) {
			if y >= rect.Y+rect.H-5 {
				break
			}
			ui.DrawText(a.screen, rect.X+2, y, rect.W-4, theme.TextMuted, wrapped)
			y++
		}
	}

	inputY := rect.Y + rect.H - 4
	m.InputRect = ui.Rect{X: rect.X + 2, Y: inputY, W: rect.W - 4, H: 1}
	inputStyle := theme.Element
	if m.InputActive {
		inputStyle = theme.Panel.Reverse(true)
	}
	ui.FillRect(a.screen, m.InputRect, inputStyle)
	placeholder := "press a, type command prefix, Enter saves"
	text := strings.TrimRight(m.Input, "\n\r")
	textStyle := theme.Text
	if text == "" {
		text = placeholder
		textStyle = theme.TextMuted
	}
	visible := permissionsModalTail(text, maxInt(1, m.InputRect.W-2))
	ui.DrawText(a.screen, m.InputRect.X+1, inputY, m.InputRect.W-2, textStyle, visible)
	if m.InputActive && !m.Busy {
		cursorX := m.InputRect.X + 1 + utf8.RuneCountInString(visible)
		maxX := m.InputRect.X + m.InputRect.W - 2
		if cursorX > maxX {
			cursorX = maxX
		}
		a.screen.SetContent(cursorX, inputY, '█', nil, theme.Primary)
	}

	hint := "Enter save · Ctrl+U clear · Esc leave/close"
	if m.Busy {
		hint = "Saving..."
	}
	ui.DrawText(a.screen, rect.X+2, rect.Y+rect.H-2, rect.W-4, theme.TextMuted, permissionsModalClamp(hint, rect.W-4))
}

func drawPermissionsPolicyButton(s tcell.Screen, x, y int, label string, style tcell.Style) ui.Rect {
	w := utf8.RuneCountInString(label)
	if w <= 0 {
		return ui.Rect{}
	}
	ui.DrawText(s, x, y, w, style, label)
	return ui.Rect{X: x, Y: y, W: w, H: 1}
}

func permissionsPolicyRuleLabel(rule client.PermissionRule) string {
	target := permissionsPolicyRuleTarget(rule)
	decision := strings.ToUpper(strings.TrimSpace(rule.Decision))
	if decision == "" || decision == "ALLOW" {
		return target
	}
	return fmt.Sprintf("%s · %s", decision, target)
}

func permissionsPolicyRuleTarget(rule client.PermissionRule) string {
	switch strings.TrimSpace(rule.Kind) {
	case "bash_prefix":
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			pattern = "<empty>"
		}
		return "bash prefix: " + pattern
	case "phrase":
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			pattern = "<empty>"
		}
		return "phrase: " + pattern
	default:
		tool := strings.TrimSpace(rule.Tool)
		if tool == "" {
			tool = "tool"
		}
		return "tool: " + tool
	}
}

func permissionsModalClamp(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxWidth {
		return text
	}
	if maxWidth <= 1 {
		return "…"
	}
	runes := []rune(text)
	return string(runes[:maxWidth-1]) + "…"
}

func permissionsModalTail(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxWidth {
		return text
	}
	return string(runes[len(runes)-maxWidth:])
}

func trimLastRune(text string) string {
	if text == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(text)
	if size <= 0 {
		return ""
	}
	return text[:len(text)-size]
}
