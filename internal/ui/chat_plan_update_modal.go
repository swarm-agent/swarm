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
	chatPlanUpdateSelectCancel  = 0
	chatPlanUpdateSelectConfirm = 1
	chatPlanUpdateInputMaxLines = 3
)

func isPlanUpdatePermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "plan_update")
}

func (p *ChatPage) planUpdateModalActive() bool {
	return strings.TrimSpace(p.planUpdatePermission) != ""
}

func (p *ChatPage) OpenPlanUpdatePermissionModal(record ChatPermissionRecord) bool {
	if !isPlanUpdatePermission(record) {
		return false
	}
	payload := decodePermissionArguments(record.ToolArguments)
	title := strings.TrimSpace(mapStringArg(payload, "title"))
	if title == "" {
		title = "Plan"
	}
	p.planUpdatePermission = strings.TrimSpace(record.ID)
	p.planUpdateTitle = title
	p.planUpdatePlanID = strings.TrimSpace(firstNonEmptyToolValue(mapStringArg(payload, "plan_id"), mapStringArg(payload, "id")))
	p.planUpdatePriorTitle = strings.TrimSpace(mapStringArg(payload, "prior_title"))
	p.planUpdatePriorPlan = strings.TrimSpace(mapStringArg(payload, "prior_plan"))
	p.planUpdatePlan = strings.TrimSpace(mapStringArg(payload, "plan"))
	p.planUpdateDiffLines = mapStringSliceArg(payload, "diff_lines")
	p.planUpdateScroll = 0
	p.planUpdateSelection = chatPlanUpdateSelectConfirm
	p.planUpdateInput = ""
	p.planUpdateCancelRect = Rect{}
	p.planUpdateConfirmRect = Rect{}
	p.statusLine = "plan update permission active"
	return true
}

func (p *ChatPage) closePlanUpdateModal() {
	p.planUpdatePermission = ""
	p.planUpdateTitle = ""
	p.planUpdatePlanID = ""
	p.planUpdatePriorTitle = ""
	p.planUpdatePriorPlan = ""
	p.planUpdatePlan = ""
	p.planUpdateDiffLines = nil
	p.planUpdateScroll = 0
	p.planUpdateSelection = chatPlanUpdateSelectConfirm
	p.planUpdateInput = ""
	p.planUpdateCancelRect = Rect{}
	p.planUpdateConfirmRect = Rect{}
}

func (p *ChatPage) handlePlanUpdateModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.planUpdateModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.planUpdateCancelRect.Contains(x, y):
			p.resolvePlanUpdateModal(false)
			return true
		case p.planUpdateConfirmRect.Contains(x, y):
			p.resolvePlanUpdateModal(true)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftPlanUpdateScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftPlanUpdateScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handlePlanUpdateModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.planUpdateModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	switch {
	case p.keybinds.Match(ev, KeybindPlanExitCancel):
		p.resolvePlanUpdateModal(false)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight), p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
		if p.planUpdateSelection == chatPlanUpdateSelectConfirm {
			p.planUpdateSelection = chatPlanUpdateSelectCancel
		} else {
			p.planUpdateSelection = chatPlanUpdateSelectConfirm
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp):
		p.shiftPlanUpdateScroll(-1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown):
		p.shiftPlanUpdateScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageUp):
		p.shiftPlanUpdateScroll(-6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageDown):
		p.shiftPlanUpdateScroll(6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpHome):
		p.planUpdateScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpEnd):
		p.planUpdateScroll = 1 << 30
		return true
	case p.keybinds.Match(ev, KeybindPlanExitConfirm):
		if p.planUpdateSelection == chatPlanUpdateSelectConfirm {
			p.resolvePlanUpdateModal(true)
			return true
		}
		p.resolvePlanUpdateModal(false)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		p.shiftPlanUpdateScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		p.shiftPlanUpdateScroll(-1)
		return true
	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		if len(p.planUpdateInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.planUpdateInput)
			if sz > 0 {
				p.planUpdateInput = p.planUpdateInput[:len(p.planUpdateInput)-sz]
			}
		}
		return true
	case ev.Key() == tcell.KeyCtrlU:
		p.planUpdateInput = ""
		return true
	case ev.Key() == tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.planUpdateInput) < chatMaxInputRunes {
			p.planUpdateInput += string(r)
		}
		return true
	}
	return true
}

func (p *ChatPage) drawPlanUpdateModal(s tcell.Screen, screen Rect) {
	if !p.planUpdateModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	if _, ok := p.pendingPermissionByID(p.planUpdatePermission); !ok {
		p.closePlanUpdateModal()
		return
	}

	modalW, ok := planExitModalWidth(screen.W)
	if !ok {
		return
	}
	lines := p.planUpdateModalLines(modalW - 4)
	inputRows := p.planUpdateInputRows(maxInt(1, modalW-6))
	modal, ok := p.planUpdateModalRect(screen, len(lines), inputRows)
	if !ok {
		return
	}

	p.planUpdateCancelRect = Rect{}
	p.planUpdateConfirmRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style {
		return styleWithBackgroundFrom(style, p.theme.Panel)
	}
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	header := "Review Plan Update"
	if title := strings.TrimSpace(p.planUpdateTitle); title != "" {
		header = "Review Plan Update: " + title
	}
	header = clampEllipsis(header, modal.W-4)
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), header)
	subtitle := fmt.Sprintf("Approve permission %s to update the saved plan", clampEllipsis(p.planUpdatePermission, 24))
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
	p.clampPlanUpdateScroll(maxScroll)

	for row := 0; row < contentHeight; row++ {
		idx := p.planUpdateScroll + row
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
		visibleLines := p.planUpdateInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.planUpdateInput) == "" {
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
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.planUpdateScroll+1, maxScroll+1)
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
	confirmLabel := "Enter Approve Update"
	cancelStyle := filledButtonStyle(p.theme.TextMuted)
	confirmStyle := filledButtonStyle(p.theme.Success)
	if p.planUpdateSelection == chatPlanUpdateSelectCancel {
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

	p.planUpdateCancelRect = Rect{X: startX, Y: buttonY, W: cancelW, H: 1}
	p.planUpdateConfirmRect = Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}
}

func (p *ChatPage) planUpdateModalRect(screen Rect, contentLines, inputRows int) (Rect, bool) {
	return p.planExitModalRect(screen, contentLines, inputRows)
}

func (p *ChatPage) planUpdateModalLines(width int) []chatRenderLine {
	if width < 8 {
		width = 8
	}
	appendWrapped := func(out []chatRenderLine, line chatRenderLine) []chatRenderLine {
		line = renderLineForCurrentCellBackground(line)
		if chatRenderLineText(line) == "" {
			return append(out, chatRenderLine{Text: "", Style: line.Style})
		}
		wrapped := wrapRenderLineWithCustomPrefixes("", "", line, width)
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
	appendDiffBlock := func(out []chatRenderLine, lines []string) []chatRenderLine {
		if len(lines) == 0 {
			return appendPlain(out, "No textual diff lines were provided.", p.theme.TextMuted)
		}
		for _, raw := range lines {
			text := strings.TrimRight(raw, "\r")
			style := p.theme.Text
			switch {
			case strings.HasPrefix(text, "@@"):
				style = p.theme.Accent
			case strings.HasPrefix(text, "+"):
				style = p.theme.Success
			case strings.HasPrefix(text, "-"):
				style = p.theme.Error
			}
			out = appendWrapped(out, chatRenderLine{Text: text, Style: styleForCurrentCellBackground(style)})
		}
		return out
	}

	title := strings.TrimSpace(p.planUpdateTitle)
	if title == "" {
		title = "Plan"
	}
	lines := make([]chatRenderLine, 0, 64)
	lines = appendPlain(lines, fmt.Sprintf("Plan: %s", title), p.theme.Text.Bold(true))
	if strings.TrimSpace(p.planUpdatePlanID) != "" {
		lines = appendPlain(lines, fmt.Sprintf("Plan ID: %s", p.planUpdatePlanID), p.theme.TextMuted)
	}
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Approve this request to revise an existing saved plan.", p.theme.TextMuted)
	lines = appendPlain(lines, "Review the diff and the resulting plan body before accepting.", p.theme.TextMuted)
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})

	lines = appendPlain(lines, "Diff preview:", p.theme.Secondary.Bold(true))
	lines = appendDiffBlock(lines, p.planUpdateDiffLines)
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})

	priorTitle := strings.TrimSpace(p.planUpdatePriorTitle)
	if priorTitle == "" {
		priorTitle = title
	}
	lines = appendPlain(lines, fmt.Sprintf("Previous plan: %s", priorTitle), p.theme.Warning.Bold(true))
	priorPlan := strings.TrimSpace(p.planUpdatePriorPlan)
	if priorPlan == "" {
		priorPlan = "No prior plan text was provided."
	}
	for _, row := range p.assistantMarkdownRows(priorPlan, p.theme.TextMuted) {
		lines = appendWrapped(lines, row)
	}
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})

	lines = appendPlain(lines, "Updated plan:", p.theme.Success.Bold(true))
	updatedPlan := strings.TrimSpace(p.planUpdatePlan)
	if updatedPlan == "" {
		updatedPlan = "No updated plan text was provided."
	}
	for _, row := range p.assistantMarkdownRows(updatedPlan, p.theme.Text) {
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

func (p *ChatPage) shiftPlanUpdateScroll(delta int) {
	p.planUpdateScroll += delta
	if p.planUpdateScroll < 0 {
		p.planUpdateScroll = 0
	}
}

func (p *ChatPage) planUpdateInputRows(textWidth int) int {
	lines := p.planUpdateInputWrappedLines(textWidth)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	if height > chatPlanUpdateInputMaxLines {
		height = chatPlanUpdateInputMaxLines
	}
	return height
}

func (p *ChatPage) planUpdateInputVisibleLines(textWidth, inputRows int) []string {
	lines := p.planUpdateInputWrappedLines(textWidth)
	if inputRows < 1 {
		inputRows = 1
	}
	if len(lines) <= inputRows {
		return lines
	}
	return lines[len(lines)-inputRows:]
}

func (p *ChatPage) planUpdateInputWrappedLines(textWidth int) []string {
	if textWidth <= 0 {
		return []string{""}
	}
	text := p.planUpdateInput
	if text == "" {
		return []string{""}
	}
	lines := wrapWithCustomPrefixes("", "", text, textWidth)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (p *ChatPage) clampPlanUpdateScroll(maxScroll int) {
	if p.planUpdateScroll < 0 {
		p.planUpdateScroll = 0
		return
	}
	if p.planUpdateScroll > maxScroll {
		p.planUpdateScroll = maxScroll
	}
}

func (p *ChatPage) resolvePlanUpdateModal(approve bool) {
	permissionID := strings.TrimSpace(p.planUpdatePermission)
	note := strings.TrimSpace(p.planUpdateInput)
	record, _ := p.pendingPermissionByID(permissionID)
	p.closePlanUpdateModal()
	if permissionID == "" {
		return
	}
	if approve {
		reason := strings.TrimSpace(planUpdateApprovalReason(record, note))
		p.queueResolvePermissionByID(permissionID, "approve", reason)
		p.statusLine = "plan update approved"
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", note)
	p.statusLine = "plan update denied"
}

func planUpdateApprovalReason(record ChatPermissionRecord, note string) string {
	payload := decodePermissionArguments(record.ToolArguments)
	request := map[string]any{}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok && len(approved) > 0 {
		request["approved_arguments"] = approved
	}
	if action := strings.TrimSpace(mapStringArg(payload, "action")); action != "" {
		request["action"] = action
	}
	if strings.TrimSpace(note) != "" {
		request["note"] = strings.TrimSpace(note)
	}
	if len(request) == 0 {
		if strings.TrimSpace(note) == "" {
			return ""
		}
		return strings.TrimSpace(note)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return strings.TrimSpace(note)
	}
	return string(raw)
}

func mapStringSliceArg(payload map[string]any, key string) []string {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
