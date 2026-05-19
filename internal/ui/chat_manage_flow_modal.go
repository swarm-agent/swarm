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
	chatManageFlowSelectCancel  = 0
	chatManageFlowSelectConfirm = 1
)

type manageFlowPermissionPayloadView struct {
	Title   string
	Summary string
	Action  string
	FlowID  string
	Name    string
	Lines   []string
}

func (p *ChatPage) OpenManageFlowPermissionModal(record ChatPermissionRecord) bool {
	view := manageFlowPermissionPayload(record)
	p.manageFlowPermission = strings.TrimSpace(record.ID)
	p.manageFlowTitle = view.Title
	p.manageFlowBody = strings.Join(view.Lines, "\n")
	p.manageFlowScroll = 0
	p.manageFlowSelection = chatManageFlowSelectConfirm
	p.manageFlowInput = ""
	p.manageFlowCancelRect = Rect{}
	p.manageFlowConfirmRect = Rect{}
	return strings.TrimSpace(p.manageFlowPermission) != ""
}

func (p *ChatPage) manageFlowModalActive() bool {
	return strings.TrimSpace(p.manageFlowPermission) != ""
}

func (p *ChatPage) closeManageFlowModal() {
	p.manageFlowPermission = ""
	p.manageFlowTitle = ""
	p.manageFlowBody = ""
	p.manageFlowScroll = 0
	p.manageFlowSelection = chatManageFlowSelectConfirm
	p.manageFlowInput = ""
	p.manageFlowCancelRect = Rect{}
	p.manageFlowConfirmRect = Rect{}
}

func (p *ChatPage) handleManageFlowModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.manageFlowModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.manageFlowCancelRect.Contains(x, y):
			p.resolveManageFlowModal(false)
			return true
		case p.manageFlowConfirmRect.Contains(x, y):
			p.resolveManageFlowModal(true)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftManageFlowScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftManageFlowScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleManageFlowModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.manageFlowModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	switch {
	case p.keybinds.Match(ev, KeybindPlanExitCancel):
		p.resolveManageFlowModal(false)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight), p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
		if p.manageFlowSelection == chatManageFlowSelectConfirm {
			p.manageFlowSelection = chatManageFlowSelectCancel
		} else {
			p.manageFlowSelection = chatManageFlowSelectConfirm
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp), p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		p.shiftManageFlowScroll(-1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown), p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		p.shiftManageFlowScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageUp):
		p.shiftManageFlowScroll(-6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageDown):
		p.shiftManageFlowScroll(6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpHome):
		p.manageFlowScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpEnd):
		p.manageFlowScroll = 1 << 30
		return true
	case p.keybinds.Match(ev, KeybindPlanExitConfirm):
		p.resolveManageFlowModal(p.manageFlowSelection == chatManageFlowSelectConfirm)
		return true
	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		if len(p.manageFlowInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.manageFlowInput)
			if sz > 0 {
				p.manageFlowInput = p.manageFlowInput[:len(p.manageFlowInput)-sz]
			}
		}
		return true
	case ev.Key() == tcell.KeyCtrlU:
		p.manageFlowInput = ""
		return true
	case ev.Key() == tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.manageFlowInput) < chatMaxInputRunes {
			p.manageFlowInput += string(r)
		}
		return true
	}
	return true
}

func (p *ChatPage) drawManageFlowModal(s tcell.Screen, screen Rect) {
	if !p.manageFlowModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	modalW, ok := planExitModalWidth(screen.W)
	if !ok {
		return
	}
	lines := p.manageFlowModalLines(modalW - 4)
	inputRows := p.manageFlowInputRows(maxInt(1, modalW-6))
	modal, ok := p.manageFlowModalRect(screen, len(lines), inputRows)
	if !ok {
		return
	}
	p.manageFlowCancelRect = Rect{}
	p.manageFlowConfirmRect = Rect{}
	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	header := clampEllipsis(firstNonEmptyString(strings.TrimSpace(p.manageFlowTitle), "Review Flow Change"), modal.W-4)
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), header)
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Read-only preview · approve or deny this manage-flow request", modal.W-4))

	contentTop := modal.Y + 3
	contentHeight := modal.H - (inputRows + 7)
	if contentHeight < 1 {
		contentHeight = 1
	}
	maxScroll := len(lines) - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	p.clampManageFlowScroll(maxScroll)
	for row := 0; row < contentHeight; row++ {
		idx := p.manageFlowScroll + row
		if idx < 0 || idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, modal.X+2, contentTop+row, modal.W-4, lines[idx])
	}

	inputY := modal.Y + modal.H - (inputRows + 3)
	textX := modal.X + 2
	textW := modal.W - 4
	if textW > 0 {
		DrawText(s, modal.X+2, inputY-1, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Message to agent (optional):", modal.W-4))
		visibleLines := p.manageFlowInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.manageFlowInput) == "" {
			DrawText(s, textX, inputY, textW, onPanel(p.theme.TextMuted), clampEllipsis("Type a note to send back with this action...", textW))
		} else {
			for i := 0; i < len(visibleLines) && i < inputRows; i++ {
				DrawText(s, textX, inputY+i, textW, onPanel(p.theme.Text), visibleLines[i])
			}
		}
		if (p.frameTick/chatCursorBlinkOn)%2 == 0 {
			cursorLine := maxInt(0, minInt(len(visibleLines)-1, inputRows-1))
			cursorText := ""
			if len(visibleLines) > 0 && cursorLine < len(visibleLines) {
				cursorText = visibleLines[cursorLine]
			}
			cursorX := minInt(textX+utf8.RuneCountInString(cursorText), modal.X+modal.W-3)
			s.SetContent(cursorX, inputY+cursorLine, chatCursorRune, nil, onPanel(p.theme.Primary))
		}
	}

	helpY := modal.Y + modal.H - 3
	help := "↑/↓ scroll • Tab switch • Enter approve • Esc deny"
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.manageFlowScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, helpY, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, modal.W/2))
	}
	DrawText(s, modal.X+2, helpY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(help, modal.W-4))

	buttonY := modal.Y + modal.H - 2
	cancelLabel := "Esc Deny"
	confirmLabel := "Enter Approve"
	cancelStyle := filledButtonStyle(p.theme.TextMuted)
	confirmStyle := filledButtonStyle(p.theme.Success)
	if p.manageFlowSelection == chatManageFlowSelectCancel {
		cancelStyle = filledButtonStyle(p.theme.Warning)
		confirmStyle = filledButtonStyle(p.theme.TextMuted)
	}
	cancelW := utf8.RuneCountInString(cancelLabel) + 2
	confirmW := utf8.RuneCountInString(confirmLabel) + 2
	gap := 2
	startX := maxInt(modal.X+2, modal.X+(modal.W-cancelW-gap-confirmW)/2)
	FillRect(s, Rect{X: startX, Y: buttonY, W: cancelW, H: 1}, cancelStyle)
	DrawCenteredText(s, startX, buttonY, cancelW, cancelStyle, cancelLabel)
	confirmX := startX + cancelW + gap
	FillRect(s, Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}, confirmStyle)
	DrawCenteredText(s, confirmX, buttonY, confirmW, confirmStyle, confirmLabel)
	p.manageFlowCancelRect = Rect{X: startX, Y: buttonY, W: cancelW, H: 1}
	p.manageFlowConfirmRect = Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}
}

func (p *ChatPage) manageFlowModalRect(screen Rect, contentLines, inputRows int) (Rect, bool) {
	return p.planExitModalRect(screen, contentLines, inputRows)
}

func (p *ChatPage) manageFlowModalLines(width int) []chatRenderLine {
	if width < 8 {
		width = 8
	}
	appendPlain := func(out []chatRenderLine, text string, style tcell.Style) []chatRenderLine {
		if strings.TrimSpace(text) == "" {
			return append(out, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(style)})
		}
		spans, _ := p.assistantInlineMarkdownSpans(text, style)
		wrapped := wrapMarkdownRenderLine(markdownLineWithInlineSpans("", spans, style), width)
		if len(wrapped) == 0 {
			return append(out, chatRenderLine{Text: "", Style: style})
		}
		for i := range wrapped {
			wrapped[i] = renderLineForCurrentCellBackground(wrapped[i])
		}
		return append(out, wrapped...)
	}
	lines := make([]chatRenderLine, 0, 32)
	for _, row := range strings.Split(strings.TrimSpace(p.manageFlowBody), "\n") {
		lines = appendPlain(lines, row, p.theme.Text)
	}
	if len(lines) == 0 {
		lines = appendPlain(lines, "No manage-flow preview was provided.", p.theme.TextMuted)
	}
	return lines
}

func (p *ChatPage) shiftManageFlowScroll(delta int) {
	p.manageFlowScroll += delta
	if p.manageFlowScroll < 0 {
		p.manageFlowScroll = 0
	}
}

func (p *ChatPage) clampManageFlowScroll(maxScroll int) {
	if p.manageFlowScroll < 0 {
		p.manageFlowScroll = 0
		return
	}
	if p.manageFlowScroll > maxScroll {
		p.manageFlowScroll = maxScroll
	}
}

func (p *ChatPage) manageFlowInputRows(textWidth int) int {
	lines := p.manageFlowInputWrappedLines(textWidth)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	if height > chatPlanExitInputMaxLines {
		height = chatPlanExitInputMaxLines
	}
	return height
}

func (p *ChatPage) manageFlowInputVisibleLines(textWidth, inputRows int) []string {
	lines := p.manageFlowInputWrappedLines(textWidth)
	if inputRows < 1 {
		inputRows = 1
	}
	if len(lines) <= inputRows {
		return lines
	}
	return lines[len(lines)-inputRows:]
}

func (p *ChatPage) manageFlowInputWrappedLines(textWidth int) []string {
	if textWidth <= 0 || p.manageFlowInput == "" {
		return []string{""}
	}
	lines := wrapWithCustomPrefixes("", "", p.manageFlowInput, textWidth)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (p *ChatPage) resolveManageFlowModal(approve bool) {
	permissionID := strings.TrimSpace(p.manageFlowPermission)
	note := strings.TrimSpace(p.manageFlowInput)
	p.closeManageFlowModal()
	if permissionID == "" {
		return
	}
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", note)
		p.statusLine = "flow change approved"
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", note)
	p.statusLine = "flow change denied"
}

func isManageFlowPermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "flow_change") || normalizePermissionToolName(record.ToolName) == "manage_flow"
}

func manageFlowPermissionPayload(record ChatPermissionRecord) manageFlowPermissionPayloadView {
	view := manageFlowPermissionPayloadView{Title: "Review Flow Change"}
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(record.ToolArguments)), &args); err != nil {
		view.Lines = []string{"Unable to parse manage-flow payload.", err.Error()}
		return view
	}
	action := strings.ToLower(strings.TrimSpace(mapStringArg(args, "action")))
	change, _ := args["change"].(map[string]any)
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(mapStringArg(change, "operation")))
	}
	approved, _ := args["approved_arguments"].(map[string]any)
	content, _ := approved["content"].(map[string]any)
	after, _ := change["after"].(map[string]any)
	before, _ := change["before"].(map[string]any)
	flowView := firstMap(content, after, before)
	view.Action = firstNonEmptyString(action, "change")
	view.FlowID = firstNonEmptyString(mapStringArg(approved, "flow_id"), mapStringArg(args, "flow_id"), mapStringArg(flowView, "flow_id"))
	view.Name = firstNonEmptyString(mapStringArg(flowView, "name"), mapStringArg(args, "name"), view.FlowID)
	view.Summary = firstNonEmptyString(mapStringArg(change, "approval_summary"), mapStringArg(args, "approval_summary"), mapStringArg(args, "summary"))
	view.Title = fmt.Sprintf("%s Flow", strings.Title(view.Action))
	lines := []string{firstNonEmptyString(view.Summary, fmt.Sprintf("%s flow %s", view.Action, firstNonEmptyString(view.Name, view.FlowID, "(new flow)"))), ""}
	appendField := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", label, strings.TrimSpace(value)))
		}
	}
	appendField("Action", view.Action)
	appendField("Flow", view.Name)
	appendField("ID", view.FlowID)
	if agent, ok := flowView["agent"].(map[string]any); ok {
		appendField("Agent", strings.TrimSpace(firstNonEmptyString(mapStringArg(agent, "profile_name"), mapStringArg(agent, "target_name")))+" "+strings.TrimSpace(firstNonEmptyString(mapStringArg(agent, "profile_mode"), mapStringArg(agent, "target_kind"))))
	}
	if workspace, ok := flowView["workspace"].(map[string]any); ok {
		appendField("Workspace", firstNonEmptyString(mapStringArg(workspace, "workspace_path"), mapStringArg(workspace, "host_workspace_path"), mapStringArg(workspace, "cwd")))
	}
	if schedule, ok := flowView["schedule"].(map[string]any); ok {
		appendField("Schedule", strings.Join([]string{mapStringArg(schedule, "cadence"), mapStringArg(schedule, "time"), mapStringArg(schedule, "timezone"), mapStringArg(schedule, "cron")}, " "))
	}
	if intent, ok := flowView["intent"].(map[string]any); ok {
		appendField("Prompt", mapStringArg(intent, "prompt"))
	}
	view.Lines = lines
	return view
}

func firstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return map[string]any{}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
