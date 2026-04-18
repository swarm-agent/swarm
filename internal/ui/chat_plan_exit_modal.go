package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	chatPlanExitSelectCancel  = 0
	chatPlanExitSelectConfirm = 1
	chatPlanExitInputMaxLines = 3
)

func (p *ChatPage) OpenExitPlanModeModal(title, body string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Exit Plan Mode"
	}
	body = strings.TrimSpace(body)
	if body == "" {
		body = "Review and approve this plan to switch the session from plan mode to auto mode."
	}
	p.planExitVisible = true
	p.planExitTitle = title
	p.planExitBody = body
	p.planExitPermission = ""
	p.planExitPlanID = ""
	p.planExitScroll = 0
	p.planExitSelection = chatPlanExitSelectConfirm
	p.planExitInput = ""
	p.planExitCancelRect = Rect{}
	p.planExitConfirmRect = Rect{}
	return true
}

func (p *ChatPage) OpenExitPlanModePermissionModal(permissionID, planID, title, body string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Exit Plan Mode"
	}
	body = strings.TrimSpace(body)
	if body == "" {
		body = "Review and approve this request to exit plan mode and switch the session to auto mode."
	}
	p.planExitVisible = true
	p.planExitTitle = title
	p.planExitBody = body
	p.planExitPermission = strings.TrimSpace(permissionID)
	p.planExitPlanID = strings.TrimSpace(planID)
	p.planExitScroll = 0
	p.planExitSelection = chatPlanExitSelectConfirm
	p.planExitInput = ""
	p.planExitCancelRect = Rect{}
	p.planExitConfirmRect = Rect{}
	return true
}

func (p *ChatPage) planExitModalActive() bool {
	return p.planExitVisible
}

func (p *ChatPage) closePlanExitModal() {
	p.planExitVisible = false
	p.planExitPermission = ""
	p.planExitPlanID = ""
	p.planExitInput = ""
	p.planExitScroll = 0
	p.planExitSelection = chatPlanExitSelectConfirm
	p.planExitCancelRect = Rect{}
	p.planExitConfirmRect = Rect{}
}

func (p *ChatPage) handlePlanExitModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.planExitModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.planExitCancelRect.Contains(x, y):
			p.resolvePlanExitModal(false)
			return true
		case p.planExitConfirmRect.Contains(x, y):
			p.resolvePlanExitModal(true)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftPlanExitScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftPlanExitScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handlePlanExitModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.planExitModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	switch {
	case p.keybinds.Match(ev, KeybindPlanExitCancel):
		p.resolvePlanExitModal(false)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight), p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
		if p.planExitSelection == chatPlanExitSelectConfirm {
			p.planExitSelection = chatPlanExitSelectCancel
		} else {
			p.planExitSelection = chatPlanExitSelectConfirm
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp):
		p.shiftPlanExitScroll(-1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown):
		p.shiftPlanExitScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageUp):
		p.shiftPlanExitScroll(-6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageDown):
		p.shiftPlanExitScroll(6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpHome):
		p.planExitScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpEnd):
		p.planExitScroll = 1 << 30
		return true
	case p.keybinds.Match(ev, KeybindPlanExitConfirm):
		if p.planExitSelection == chatPlanExitSelectConfirm {
			p.resolvePlanExitModal(true)
			return true
		}
		p.resolvePlanExitModal(false)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		p.shiftPlanExitScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		p.shiftPlanExitScroll(-1)
		return true
	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		if len(p.planExitInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.planExitInput)
			if sz > 0 {
				p.planExitInput = p.planExitInput[:len(p.planExitInput)-sz]
			}
		}
		return true
	case ev.Key() == tcell.KeyCtrlU:
		p.planExitInput = ""
		return true
	case ev.Key() == tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.planExitInput) < chatMaxInputRunes {
			p.planExitInput += string(r)
		}
		return true
	}
	return true
}

func (p *ChatPage) drawPlanExitModal(s tcell.Screen, screen Rect) {
	if !p.planExitModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}

	modalW, ok := planExitModalWidth(screen.W)
	if !ok {
		return
	}
	lines := p.planExitModalLines(modalW - 4)
	inputRows := p.planExitInputRows(maxInt(1, modalW-6))
	modal, ok := p.planExitModalRect(screen, len(lines), inputRows)
	if !ok {
		return
	}

	p.planExitCancelRect = Rect{}
	p.planExitConfirmRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style {
		return styleWithBackgroundFrom(style, p.theme.Panel)
	}
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	header := "Exit Plan Mode"
	if title := strings.TrimSpace(p.planExitTitle); title != "" && !strings.EqualFold(title, "Exit Plan Mode") {
		header = "Exit Plan Mode: " + title
	}
	header = clampEllipsis(header, modal.W-4)
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), header)
	subtitle := fmt.Sprintf("Current mode: %s  ->  target mode: auto", normalizeSessionMode(p.sessionMode))
	if strings.TrimSpace(p.planExitPermission) != "" {
		subtitle = fmt.Sprintf("Approve permission %s to exit plan mode", clampEllipsis(p.planExitPermission, 24))
	} else if strings.TrimSpace(p.planExitPlanID) != "" {
		subtitle = fmt.Sprintf("%s  -  plan %s", subtitle, clampEllipsis(p.planExitPlanID, 24))
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	contentTop := modal.Y + 3
	contentHeight := modal.H - (inputRows + 7)
	if contentHeight < 1 {
		contentHeight = 1
	}
	maxScroll := len(lines) - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	p.clampPlanExitScroll(maxScroll)

	for row := 0; row < contentHeight; row++ {
		idx := p.planExitScroll + row
		if idx < 0 || idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, modal.X+2, contentTop+row, modal.W-4, lines[idx])
	}

	inputY := modal.Y + modal.H - (inputRows + 3)
	textX := modal.X + 2
	textW := modal.W - 4
	if textW > 0 {
		inputLabel := "Message to agent (optional):"
		DrawText(s, modal.X+2, inputY-1, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(inputLabel, modal.W-4))
		visibleLines := p.planExitInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.planExitInput) == "" {
			DrawText(s, textX, inputY, textW, onPanel(p.theme.TextMuted), clampEllipsis("Type a note to send back with this action...", textW))
		} else {
			for i := 0; i < len(visibleLines) && i < inputRows; i++ {
				DrawText(s, textX, inputY+i, textW, onPanel(p.theme.Text), visibleLines[i])
			}
		}
		if (p.frameTick/chatCursorBlinkOn)%2 == 0 {
			cursorLine := 0
			if len(visibleLines) > 0 {
				cursorLine = len(visibleLines) - 1
			}
			if cursorLine < 0 {
				cursorLine = 0
			}
			if cursorLine >= inputRows {
				cursorLine = inputRows - 1
			}
			cursorText := ""
			if len(visibleLines) > 0 && cursorLine >= 0 && cursorLine < len(visibleLines) {
				cursorText = visibleLines[cursorLine]
			}
			cursorX := textX + utf8.RuneCountInString(cursorText)
			maxX := modal.X + modal.W - 3
			if cursorX > maxX {
				cursorX = maxX
			}
			if cursorX < textX {
				cursorX = textX
			}
			cursorY := inputY + cursorLine
			maxY := inputY + inputRows - 1
			if cursorY > maxY {
				cursorY = maxY
			}
			if cursorY < inputY {
				cursorY = inputY
			}
			s.SetContent(cursorX, cursorY, chatCursorRune, nil, onPanel(p.theme.Primary))
		}
	}

	helpY := modal.Y + modal.H - 3
	help := "↑/↓ scroll • Tab switch • Enter confirm • Esc cancel"
	helpWidth := modal.W - 4
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.planExitScroll+1, maxScroll+1)
		scrollWidth := utf8.RuneCountInString(scrollLabel)
		DrawTextRight(s, modal.X+modal.W-2, helpY, maxInt(scrollWidth, modal.W/2), onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, modal.W/2))
		remaining := modal.W - 4 - scrollWidth - 2
		if remaining > 12 {
			helpWidth = remaining
		}
	}
	DrawText(s, modal.X+2, helpY, helpWidth, onPanel(p.theme.TextMuted), clampEllipsis(help, helpWidth))

	buttonY := modal.Y + modal.H - 2
	cancelLabel := "Esc Cancel"
	confirmLabel := "Enter Exit Plan Mode"
	if strings.TrimSpace(p.planExitPermission) != "" {
		confirmLabel = "Enter Approve"
	}
	cancelStyle := filledButtonStyle(p.theme.TextMuted)
	confirmStyle := filledButtonStyle(p.theme.Success)
	if p.planExitSelection == chatPlanExitSelectCancel {
		cancelStyle = filledButtonStyle(p.theme.Warning)
		confirmStyle = filledButtonStyle(p.theme.TextMuted)
	}

	cancelW := utf8.RuneCountInString(cancelLabel) + 2
	confirmW := utf8.RuneCountInString(confirmLabel) + 2
	gap := 2
	totalW := cancelW + gap + confirmW
	startX := modal.X + (modal.W-totalW)/2
	if startX < modal.X+2 {
		startX = modal.X + 2
	}
	FillRect(s, Rect{X: startX, Y: buttonY, W: cancelW, H: 1}, cancelStyle)
	DrawCenteredText(s, startX, buttonY, cancelW, cancelStyle, cancelLabel)
	confirmX := startX + cancelW + gap
	FillRect(s, Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}, confirmStyle)
	DrawCenteredText(s, confirmX, buttonY, confirmW, confirmStyle, confirmLabel)

	p.planExitCancelRect = Rect{X: startX, Y: buttonY, W: cancelW, H: 1}
	p.planExitConfirmRect = Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}
}

func (p *ChatPage) planExitModalRect(screen Rect, contentLines, inputRows int) (Rect, bool) {
	if screen.H < 12 {
		return Rect{}, false
	}
	modalW, ok := planExitModalWidth(screen.W)
	if !ok {
		return Rect{}, false
	}
	if inputRows < 1 {
		inputRows = 1
	}
	if inputRows > chatPlanExitInputMaxLines {
		inputRows = chatPlanExitInputMaxLines
	}

	desiredH := contentLines + inputRows + 7
	if desiredH < 14 {
		desiredH = 14
	}
	maxH := screen.H - 4
	if maxH > 36 {
		maxH = 36
	}
	if maxH < 12 {
		maxH = screen.H - 2
	}
	modalH := desiredH
	if modalH > maxH {
		modalH = maxH
	}
	if modalH < 12 {
		return Rect{}, false
	}

	return Rect{
		X: maxInt(1, (screen.W-modalW)/2),
		Y: maxInt(1, (screen.H-modalH)/2),
		W: modalW,
		H: modalH,
	}, true
}

func planExitModalWidth(screenW int) (int, bool) {
	if screenW < 38 {
		return 0, false
	}
	modalW := screenW - 8
	if modalW > 112 {
		modalW = 112
	}
	if modalW < 52 {
		modalW = screenW - 2
	}
	if modalW < 38 {
		return 0, false
	}
	return modalW, true
}

func (p *ChatPage) planExitModalLines(width int) []chatRenderLine {
	if width < 8 {
		width = 8
	}
	appendWrapped := func(out []chatRenderLine, line chatRenderLine) []chatRenderLine {
		line = renderLineForCurrentCellBackground(line)
		if chatRenderLineText(line) == "" {
			return append(out, chatRenderLine{Text: "", Style: line.Style})
		}
		wrapped := wrapMarkdownRenderLine(line, width)
		if len(wrapped) == 0 {
			return append(out, chatRenderLine{Text: "", Style: line.Style})
		}
		for i := range wrapped {
			wrapped[i] = renderLineForCurrentCellBackground(wrapped[i])
		}
		return append(out, wrapped...)
	}
	appendPlain := func(out []chatRenderLine, text string, style tcell.Style) []chatRenderLine {
		if strings.TrimSpace(text) == "" {
			return append(out, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(style)})
		}
		spans, _ := p.assistantInlineMarkdownSpans(text, style)
		return appendWrapped(out, markdownLineWithInlineSpans("", spans, style))
	}

	title := strings.TrimSpace(p.planExitTitle)
	if title == "" {
		title = "Exit plan mode"
	}
	lines := make([]chatRenderLine, 0, 32)
	lines = appendPlain(lines, fmt.Sprintf("Plan: %s", title), p.theme.Text.Bold(true))
	if strings.TrimSpace(p.planExitPlanID) != "" {
		lines = appendPlain(lines, fmt.Sprintf("Plan ID: %s", p.planExitPlanID), p.theme.TextMuted)
	}
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Approving this request switches the session from plan mode to auto mode.", p.theme.TextMuted)
	lines = appendPlain(lines, "Execution can then proceed with normal tool permissions for auto mode.", p.theme.TextMuted)
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Plan details:", p.theme.Secondary.Bold(true))

	body := strings.TrimSpace(p.planExitBody)
	if body == "" {
		body = "No additional plan details were provided."
	}
	for _, row := range p.assistantMarkdownRows(body, p.theme.Text) {
		lines = appendWrapped(lines, row)
	}
	if len(lines) == 0 {
		return []chatRenderLine{{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)}}
	}
	for i := range lines {
		lines[i] = renderLineForCurrentCellBackground(lines[i])
	}
	return lines
}

func (p *ChatPage) shiftPlanExitScroll(delta int) {
	p.planExitScroll += delta
	if p.planExitScroll < 0 {
		p.planExitScroll = 0
	}
}

func (p *ChatPage) planExitInputRows(textWidth int) int {
	lines := p.planExitInputWrappedLines(textWidth)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	if height > chatPlanExitInputMaxLines {
		height = chatPlanExitInputMaxLines
	}
	return height
}

func (p *ChatPage) planExitInputVisibleLines(textWidth, inputRows int) []string {
	lines := p.planExitInputWrappedLines(textWidth)
	if inputRows < 1 {
		inputRows = 1
	}
	if len(lines) <= inputRows {
		return lines
	}
	return lines[len(lines)-inputRows:]
}

func (p *ChatPage) planExitInputWrappedLines(textWidth int) []string {
	if textWidth <= 0 {
		return []string{""}
	}
	text := p.planExitInput
	if text == "" {
		return []string{""}
	}
	lines := wrapWithCustomPrefixes("", "", text, textWidth)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (p *ChatPage) clampPlanExitScroll(maxScroll int) {
	if p.planExitScroll < 0 {
		p.planExitScroll = 0
		return
	}
	if p.planExitScroll > maxScroll {
		p.planExitScroll = maxScroll
	}
}

func (p *ChatPage) resolvePlanExitModal(approve bool) {
	permissionID := strings.TrimSpace(p.planExitPermission)
	note := strings.TrimSpace(p.planExitInput)
	p.closePlanExitModal()
	if permissionID != "" {
		if approve {
			p.queueResolvePermissionByID(permissionID, "approve", note)
			p.statusLine = "exit plan mode approved"
		} else {
			p.queueResolvePermissionByID(permissionID, "deny", note)
			p.statusLine = "exit plan mode denied"
		}
		return
	}
	if approve {
		p.queueSetMode("auto", true)
		return
	}
	p.statusLine = "exit plan mode cancelled"
}

func isExitPlanPermission(record ChatPermissionRecord) bool {
	name := normalizePermissionToolName(record.ToolName)
	return name == "exit_plan_mode"
}

func exitPlanPermissionPayload(record ChatPermissionRecord) (string, string, string) {
	title := "Exit Plan Mode"
	body := "Review and approve this plan to switch the session from plan mode to auto mode. Once approved, execution continues on the same active plan/checklist, and plan_manage can still update it later."
	planID := ""

	raw := strings.TrimSpace(record.ToolArguments)
	if raw == "" {
		return title, body, planID
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return title, body, planID
	}
	if value := mapStringArg(args, "title"); value != "" {
		title = value
	}
	if value := mapStringArg(args, "plan"); value != "" {
		body = value
	}
	planID = mapStringArg(args, "plan_id")
	if planID == "" {
		planID = mapStringArg(args, "planID")
	}
	return title, body, planID
}
