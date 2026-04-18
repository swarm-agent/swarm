package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type SandboxModalCheck struct {
	Name   string
	OK     bool
	Detail string
}

type SandboxModalData struct {
	Enabled      bool
	UpdatedAt    int64
	Ready        bool
	Summary      string
	Checks       []SandboxModalCheck
	Remediation  []string
	SetupCommand string
}

type SandboxModalActionKind string

const (
	SandboxModalActionRefresh    SandboxModalActionKind = "refresh"
	SandboxModalActionSetEnabled SandboxModalActionKind = "set_enabled"
	SandboxModalActionCopySetup  SandboxModalActionKind = "copy_setup"
)

type SandboxModalAction struct {
	Kind       SandboxModalActionKind
	Enabled    bool
	Command    string
	StatusHint string
}

type sandboxModalState struct {
	Visible  bool
	Loading  bool
	Selected int
	Status   string
	Error    string
	Data     SandboxModalData
}

func (p *HomePage) ShowSandboxModal() {
	p.sandboxModal.Visible = true
	if p.sandboxModal.Selected < 0 || p.sandboxModal.Selected > 3 {
		p.sandboxModal.Selected = 0
	}
	if strings.TrimSpace(p.sandboxModal.Status) == "" {
		p.sandboxModal.Status = "Enter: action  •  c copy setup  •  Esc close"
	}
}

func (p *HomePage) HideSandboxModal() {
	p.sandboxModal = sandboxModalState{}
	p.pendingSandboxAction = nil
}

func (p *HomePage) SandboxModalVisible() bool {
	return p.sandboxModal.Visible
}

func (p *HomePage) SetSandboxModalLoading(loading bool) {
	p.sandboxModal.Loading = loading
}

func (p *HomePage) SetSandboxModalStatus(status string) {
	p.sandboxModal.Status = strings.TrimSpace(status)
	if p.sandboxModal.Status != "" {
		p.sandboxModal.Error = ""
	}
}

func (p *HomePage) SetSandboxModalError(err string) {
	p.sandboxModal.Error = strings.TrimSpace(err)
	if p.sandboxModal.Error != "" {
		p.sandboxModal.Loading = false
	}
}

func (p *HomePage) SetSandboxModalData(data SandboxModalData) {
	data.Checks = append([]SandboxModalCheck(nil), data.Checks...)
	data.Remediation = append([]string(nil), data.Remediation...)
	data.SetupCommand = strings.TrimSpace(data.SetupCommand)
	p.sandboxModal.Data = data
}

func (p *HomePage) SandboxModalData() SandboxModalData {
	data := p.sandboxModal.Data
	data.Checks = append([]SandboxModalCheck(nil), data.Checks...)
	data.Remediation = append([]string(nil), data.Remediation...)
	return data
}

func (p *HomePage) PopSandboxModalAction() (SandboxModalAction, bool) {
	if p.pendingSandboxAction == nil {
		return SandboxModalAction{}, false
	}
	action := *p.pendingSandboxAction
	p.pendingSandboxAction = nil
	return action, true
}

func (p *HomePage) enqueueSandboxModalAction(action SandboxModalAction) {
	p.pendingSandboxAction = &action
	if strings.TrimSpace(action.StatusHint) != "" {
		p.sandboxModal.Status = strings.TrimSpace(action.StatusHint)
	}
	p.sandboxModal.Loading = true
	p.sandboxModal.Error = ""
}

func (p *HomePage) handleSandboxModalKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideSandboxModal()
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt), p.keybinds.Match(ev, KeybindModalFocusLeft):
		p.moveSandboxModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt), p.keybinds.Match(ev, KeybindModalFocusRight), p.keybinds.Match(ev, KeybindModalFocusNext):
		p.moveSandboxModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.triggerSandboxModalSelection()
		return
	}

	if ev.Key() != tcell.KeyRune {
		return
	}
	switch strings.ToLower(string(ev.Rune())) {
	case "e":
		p.triggerSandboxEnable(true)
	case "d":
		p.triggerSandboxEnable(false)
	case "c":
		p.triggerSandboxCopy()
	case "r":
		p.enqueueSandboxModalAction(SandboxModalAction{
			Kind:       SandboxModalActionRefresh,
			StatusHint: "Running sandbox preflight...",
		})
	}
}

func (p *HomePage) moveSandboxModalSelection(delta int) {
	if delta == 0 {
		return
	}
	next := p.sandboxModal.Selected + delta
	if next < 0 {
		next = 3
	}
	if next > 3 {
		next = 0
	}
	p.sandboxModal.Selected = next
}

func (p *HomePage) triggerSandboxModalSelection() {
	switch p.sandboxModal.Selected {
	case 0:
		p.triggerSandboxEnable(true)
	case 1:
		p.triggerSandboxEnable(false)
	case 2:
		p.triggerSandboxCopy()
	default:
		p.enqueueSandboxModalAction(SandboxModalAction{
			Kind:       SandboxModalActionRefresh,
			StatusHint: "Running sandbox preflight...",
		})
	}
}

func (p *HomePage) triggerSandboxEnable(enabled bool) {
	if enabled && !p.sandboxModal.Data.Ready {
		p.sandboxModal.Status = "Sandbox preflight failed. Copy setup commands and retry."
		return
	}
	verb := "Disabling"
	if enabled {
		verb = "Enabling"
	}
	p.enqueueSandboxModalAction(SandboxModalAction{
		Kind:       SandboxModalActionSetEnabled,
		Enabled:    enabled,
		StatusHint: verb + " sandbox...",
	})
}

func (p *HomePage) triggerSandboxCopy() {
	command := strings.TrimSpace(p.sandboxModal.Data.SetupCommand)
	if command == "" {
		p.sandboxModal.Status = "Setup command is unavailable. Press r to refresh."
		return
	}
	p.enqueueSandboxModalAction(SandboxModalAction{
		Kind:       SandboxModalActionCopySetup,
		Command:    command,
		StatusHint: "Copying sandbox setup commands...",
	})
}

func (p *HomePage) drawSandboxModal(s tcell.Screen) {
	if !p.sandboxModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 44 || h < 12 {
		return
	}

	modalW := minInt(110, w-6)
	if modalW < 72 {
		modalW = w - 2
	}
	if modalW < 44 {
		return
	}
	modalH := minInt(30, h-4)
	if modalH < 20 {
		modalH = h - 2
	}
	if modalH < 12 {
		return
	}
	rect := Rect{X: maxInt(1, (w-modalW)/2), Y: maxInt(1, (h-modalH)/2), W: modalW, H: modalH}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Sandbox Setup")

	statusLine := strings.TrimSpace(p.sandboxModal.Status)
	statusStyle := p.theme.TextMuted
	if strings.TrimSpace(p.sandboxModal.Error) != "" {
		statusLine = strings.TrimSpace(p.sandboxModal.Error)
		statusStyle = p.theme.Error
	}
	if statusLine == "" {
		statusLine = "Enter: action  •  c copy setup  •  Esc close"
	}
	if p.sandboxModal.Loading {
		statusLine = "loading sandbox status..."
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(statusLine, rect.W-4))

	data := p.sandboxModal.Data
	stateLabel := "OFF"
	stateStyle := p.theme.Warning
	if data.Enabled {
		stateLabel = "ON"
		stateStyle = p.theme.Success
	}
	readyLabel := "false"
	readyStyle := p.theme.Warning
	if data.Ready {
		readyLabel = "true"
		readyStyle = p.theme.Success
	}

	DrawText(s, rect.X+2, rect.Y+3, rect.W/2, p.theme.Text, "Sandbox:")
	DrawText(s, rect.X+12, rect.Y+3, rect.W-14, stateStyle, stateLabel)
	DrawText(s, rect.X+2, rect.Y+4, rect.W/2, p.theme.Text, "Ready:")
	DrawText(s, rect.X+12, rect.Y+4, rect.W-14, readyStyle, readyLabel)
	DrawText(s, rect.X+2, rect.Y+5, rect.W-4, p.theme.TextMuted, clampEllipsis(data.Summary, rect.W-4))

	y := rect.Y + 7
	DrawText(s, rect.X+2, y, rect.W-4, p.theme.Text, "Checks")
	y++
	maxCheckRows := 5
	for i := 0; i < len(data.Checks) && i < maxCheckRows; i++ {
		check := data.Checks[i]
		marker := "[failed]"
		lineStyle := p.theme.Error
		if check.OK {
			marker = "[ok]"
			lineStyle = p.theme.Success
		}
		line := marker + " " + strings.TrimSpace(check.Name)
		if detail := strings.TrimSpace(check.Detail); detail != "" {
			line += " - " + detail
		}
		DrawText(s, rect.X+2, y, rect.W-4, lineStyle, clampEllipsis(line, rect.W-4))
		y++
	}

	if !data.Ready {
		DrawText(s, rect.X+2, y, rect.W-4, p.theme.Warning, "Remediation")
		y++
		maxRemediationRows := 6
		for i := 0; i < len(data.Remediation) && i < maxRemediationRows; i++ {
			line := strings.TrimSpace(data.Remediation[i])
			if line == "" {
				line = " "
			}
			DrawText(s, rect.X+2, y, rect.W-4, p.theme.TextMuted, clampEllipsis(line, rect.W-4))
			y++
		}
	}

	actionY := rect.Y + rect.H - 4
	compact := rect.W < 72
	if compact {
		actionLine := "[e] Enable  [d] Disable  [c] Copy  [r] Refresh"
		DrawText(s, rect.X+2, actionY, rect.W-4, p.theme.Text, clampEllipsis(actionLine, rect.W-4))
	} else {
		actionW := (rect.W - 10) / 4
		if actionW < 14 {
			actionW = 14
		}
		drawSandboxButton := func(x int, idx int, label string, enabled bool) {
			style := p.theme.TextMuted
			if enabled {
				style = p.theme.Text
			}
			if idx == p.sandboxModal.Selected {
				if enabled {
					style = p.theme.Primary
				} else {
					style = p.theme.Warning
				}
			}
			text := label
			if !enabled {
				text += " (disabled)"
			}
			DrawText(s, x, actionY, actionW, style, clampEllipsis(text, actionW))
		}

		drawSandboxButton(rect.X+2, 0, "[e] Enable", data.Ready)
		drawSandboxButton(rect.X+2+actionW+1, 1, "[d] Disable", true)
		drawSandboxButton(rect.X+2+2*(actionW+1), 2, "[c] Copy Setup", strings.TrimSpace(data.SetupCommand) != "")
		drawSandboxButton(rect.X+2+3*(actionW+1), 3, "[r] Refresh", true)
	}

	copyHelp := "Shift+drag selects text when mouse capture is on. If copy fails: swarm sandbox_command"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(copyHelp, rect.W-4))
}

func sandboxModalStatusSummary(data SandboxModalData) string {
	state := "OFF"
	if data.Enabled {
		state = "ON"
	}
	return fmt.Sprintf("sandbox %s (ready=%t)", state, data.Ready)
}
