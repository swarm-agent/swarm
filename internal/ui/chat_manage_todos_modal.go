package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	chatManageTodosInputMaxLines   = 3
	manageTodosPreviewDetailPrefix = "detail: "
)

func isManageTodosPermission(record ChatPermissionRecord) bool {
	return normalizePermissionToolName(record.ToolName) == "manage_todos"
}

func (p *ChatPage) manageTodosModalActive() bool {
	return strings.TrimSpace(p.manageTodosPermission) != ""
}

func (p *ChatPage) OpenManageTodosPermissionModal(record ChatPermissionRecord) bool {
	if !isManageTodosPermission(record) {
		return false
	}
	p.manageTodosPermission = strings.TrimSpace(record.ID)
	p.manageTodosScroll = 0
	p.manageTodosInput = ""
	p.manageTodosCancelRect = Rect{}
	p.manageTodosConfirmRect = Rect{}
	p.statusLine = "todo permission active"
	return true
}

func (p *ChatPage) closeManageTodosModal() {
	p.manageTodosPermission = ""
	p.manageTodosScroll = 0
	p.manageTodosInput = ""
	p.manageTodosCancelRect = Rect{}
	p.manageTodosConfirmRect = Rect{}
}

func (p *ChatPage) handleManageTodosModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.manageTodosModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.manageTodosCancelRect.Contains(x, y):
			p.resolveManageTodosModal(false)
			return true
		case p.manageTodosConfirmRect.Contains(x, y):
			p.resolveManageTodosModal(true)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftManageTodosScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftManageTodosScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleManageTodosModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.manageTodosModalActive() {
		return false
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolveManageTodosModal(false)
		return true
	case tcell.KeyEnter:
		p.resolveManageTodosModal(true)
		return true
	case tcell.KeyUp:
		p.shiftManageTodosScroll(-1)
		return true
	case tcell.KeyDown:
		p.shiftManageTodosScroll(1)
		return true
	case tcell.KeyPgUp:
		p.shiftManageTodosScroll(-6)
		return true
	case tcell.KeyPgDn:
		p.shiftManageTodosScroll(6)
		return true
	case tcell.KeyHome:
		p.manageTodosScroll = 0
		return true
	case tcell.KeyEnd:
		p.manageTodosScroll = 1 << 30
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(p.manageTodosInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.manageTodosInput)
			if sz > 0 {
				p.manageTodosInput = p.manageTodosInput[:len(p.manageTodosInput)-sz]
			}
		}
		return true
	case tcell.KeyCtrlU:
		p.manageTodosInput = ""
		return true
	case tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.manageTodosInput) < chatMaxInputRunes {
			p.manageTodosInput += string(r)
		}
		return true
	default:
		return true
	}
}

func (p *ChatPage) drawManageTodosModal(s tcell.Screen, screen Rect) {
	if !p.manageTodosModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.manageTodosPermission)
	if !ok {
		p.closeManageTodosModal()
		return
	}
	title, body, structuredLines, structured := manageTodosPermissionPayload(record)
	modalW, ok := planExitModalWidth(screen.W)
	if !ok {
		return
	}
	var lines []chatRenderLine
	if structured {
		lines = p.manageTodosStructuredModalLines(title, body, structuredLines, modalW-4)
	} else {
		lines = p.manageTodosModalLines(title, body, modalW-4)
	}
	inputRows := p.manageTodosInputRows(maxInt(1, modalW-6))
	modal, ok := p.manageTodosModalRect(screen, len(lines), inputRows)
	if !ok {
		return
	}

	p.manageTodosCancelRect = Rect{}
	p.manageTodosConfirmRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style {
		return styleWithBackgroundFrom(style, p.theme.Panel)
	}
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	header := clampEllipsis(title, modal.W-4)
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), header)
	subtitle := fmt.Sprintf("Approve permission %s to update workspace todos", clampEllipsis(strings.TrimSpace(record.ID), 24))
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
	p.clampManageTodosScroll(maxScroll)

	for row := 0; row < contentHeight; row++ {
		idx := p.manageTodosScroll + row
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
		visibleLines := p.manageTodosInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.manageTodosInput) == "" {
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
	help := "↑/↓ scroll • Enter approve • Esc cancel"
	helpWidth := modal.W - 4
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.manageTodosScroll+1, maxScroll+1)
		scrollWidth := utf8.RuneCountInString(scrollLabel)
		DrawTextRight(s, modal.X+modal.W-2, helpY, maxInt(scrollWidth, modal.W/2), onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, modal.W/2))
		remaining := modal.W - 4 - scrollWidth - 2
		if remaining > 12 {
			helpWidth = remaining
		}
	}
	DrawText(s, modal.X+2, helpY, helpWidth, onPanel(p.theme.TextMuted), clampEllipsis(help, helpWidth))

	buttonY := modal.Y + modal.H - 2
	cancelLabel := "[ Esc Cancel ]"
	confirmLabel := "[ Enter Approve ]"
	cancelStyle := onPanel(p.theme.TextMuted)
	confirmStyle := onPanel(p.theme.Success.Bold(true))
	cancelW := utf8.RuneCountInString(cancelLabel)
	confirmW := utf8.RuneCountInString(confirmLabel)
	gap := 2
	totalW := cancelW + gap + confirmW
	startX := modal.X + (modal.W-totalW)/2
	if startX < modal.X+2 {
		startX = modal.X + 2
	}
	DrawText(s, startX, buttonY, cancelW, cancelStyle, cancelLabel)
	confirmX := startX + cancelW + gap
	DrawText(s, confirmX, buttonY, confirmW, confirmStyle, confirmLabel)

	p.manageTodosCancelRect = Rect{X: startX, Y: buttonY, W: cancelW, H: 1}
	p.manageTodosConfirmRect = Rect{X: confirmX, Y: buttonY, W: confirmW, H: 1}
}

func (p *ChatPage) manageTodosModalRect(screen Rect, contentLines, inputRows int) (Rect, bool) {
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
	if inputRows > chatManageTodosInputMaxLines {
		inputRows = chatManageTodosInputMaxLines
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

func (p *ChatPage) manageTodosStructuredModalLines(title, summary string, preview []chatRenderLine, width int) []chatRenderLine {
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

	if strings.TrimSpace(title) == "" {
		title = "Review Todo Changes"
	}
	if strings.TrimSpace(summary) == "" {
		summary = "No todo change details were provided."
	}

	lines := make([]chatRenderLine, 0, len(preview)+12)
	lines = appendPlain(lines, fmt.Sprintf("Request: %s", title), p.theme.Text.Bold(true))
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Approving this request applies the proposed todo changes through `manage_todos`.", p.theme.TextMuted)
	lines = appendPlain(lines, "Review each task row below. Details are shown on the next line in italic, muted text.", p.theme.TextMuted)
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Requested changes:", p.theme.Secondary.Bold(true))
	for _, row := range preview {
		displayRow := row
		style := p.theme.Text
		if detailText, ok := manageTodosPreviewDetailText(row.Text); ok {
			style = p.theme.TextMuted.Italic(true)
			displayRow.Text = detailText
		}
		if len(displayRow.Spans) == 0 {
			displayRow.Style = style
		}
		lines = appendWrapped(lines, displayRow)
	}
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, summary, p.theme.TextMuted)
	for i := range lines {
		lines[i] = renderLineForCurrentCellBackground(lines[i])
	}
	return lines
}

func (p *ChatPage) manageTodosModalLines(title, body string, width int) []chatRenderLine {
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

	if strings.TrimSpace(title) == "" {
		title = "Review Todo Changes"
	}
	if strings.TrimSpace(body) == "" {
		body = "No todo change details were provided."
	}

	lines := make([]chatRenderLine, 0, 32)
	lines = appendPlain(lines, fmt.Sprintf("Request: %s", title), p.theme.Text.Bold(true))
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Approving this request applies the proposed todo changes through `manage_todos`.", p.theme.TextMuted)
	lines = appendPlain(lines, "Review the task preview below. You can include a message to the agent before approving or cancelling.", p.theme.TextMuted)
	lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.Text)})
	lines = appendPlain(lines, "Requested changes:", p.theme.Secondary.Bold(true))
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

func (p *ChatPage) shiftManageTodosScroll(delta int) {
	p.manageTodosScroll += delta
	if p.manageTodosScroll < 0 {
		p.manageTodosScroll = 0
	}
}

func (p *ChatPage) manageTodosInputRows(textWidth int) int {
	lines := p.manageTodosInputWrappedLines(textWidth)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	if height > chatManageTodosInputMaxLines {
		height = chatManageTodosInputMaxLines
	}
	return height
}

func (p *ChatPage) manageTodosInputVisibleLines(textWidth, inputRows int) []string {
	lines := p.manageTodosInputWrappedLines(textWidth)
	if inputRows < 1 {
		inputRows = 1
	}
	if len(lines) <= inputRows {
		return lines
	}
	return lines[len(lines)-inputRows:]
}

func (p *ChatPage) manageTodosInputWrappedLines(textWidth int) []string {
	if textWidth <= 0 {
		return []string{""}
	}
	text := p.manageTodosInput
	if text == "" {
		return []string{""}
	}
	lines := wrapWithCustomPrefixes("", "", text, textWidth)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (p *ChatPage) clampManageTodosScroll(maxScroll int) {
	if p.manageTodosScroll < 0 {
		p.manageTodosScroll = 0
		return
	}
	if p.manageTodosScroll > maxScroll {
		p.manageTodosScroll = maxScroll
	}
}

func (p *ChatPage) resolveManageTodosModal(approve bool) {
	permissionID := strings.TrimSpace(p.manageTodosPermission)
	note := strings.TrimSpace(p.manageTodosInput)
	p.closeManageTodosModal()
	if permissionID == "" {
		return
	}
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", note)
		p.statusLine = "todo changes approved"
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", note)
	p.statusLine = "todo changes denied"
}

func manageTodosPermissionPayload(record ChatPermissionRecord) (string, string, []chatRenderLine, bool) {
	args := decodePermissionArguments(record.ToolArguments)
	title := "Review Todo Changes"
	if args == nil {
		return title, "No todo change details were provided.", nil, false
	}
	action := strings.ToLower(strings.TrimSpace(mapStringArg(args, "action")))
	if label := manageTodosActionLabel(action); label != "" {
		title = title + ": " + label
	}
	if action == "batch" {
		return title, manageTodosPermissionMarkdown(args), manageTodosBatchPreviewLines(args), true
	}
	return title, manageTodosPermissionMarkdown(args), nil, false
}

func manageTodosPermissionMarkdown(payload map[string]any) string {
	if payload == nil {
		return "No todo change details were provided."
	}
	action := strings.ToLower(strings.TrimSpace(mapStringArg(payload, "action")))
	workspacePath := strings.TrimSpace(mapStringArg(payload, "workspace_path"))
	ownerKind := manageTodosOwnerKindLabel(strings.TrimSpace(mapStringArg(payload, "owner_kind")))
	itemID := strings.TrimSpace(mapStringArg(payload, "id"))
	text := strings.TrimSpace(mapStringArg(payload, "text"))
	priority := strings.TrimSpace(mapStringArg(payload, "priority"))
	group := strings.TrimSpace(mapStringArg(payload, "group"))
	tags := jsonStringSlice(payload, "tags")
	orderedIDs := jsonStringSlice(payload, "ordered_ids")
	sessionID := strings.TrimSpace(mapStringArg(payload, "session_id"))
	parentID := strings.TrimSpace(mapStringArg(payload, "parent_id"))
	done, hasDone := manageTodosBoolArg(payload, "done")
	inProgress, hasInProgress := manageTodosBoolArg(payload, "in_progress")
	operations := jsonObjectSlice(payload, "operations")

	var b strings.Builder
	b.WriteString("Approve this request to change workspace todos.\n\n")
	if action == "batch" {
		target := "workspace todos"
		if ownerKind != "" {
			target = ownerKind
		}
		if workspacePath != "" {
			return fmt.Sprintf("Atomic batch for `%s` on %s with `%d` %s.", workspacePath, target, len(operations), pluralizeManageTodosOperations(len(operations)))
		}
		return fmt.Sprintf("Atomic batch on %s with `%d` %s.", target, len(operations), pluralizeManageTodosOperations(len(operations)))
	}

	b.WriteString("## Task preview\n\n")
	switch action {
	case "create", "update":
		checkbox := "[ ]"
		if done {
			checkbox = "[x]"
		}
		label := text
		if label == "" && itemID != "" {
			label = fmt.Sprintf("todo `%s`", itemID)
		}
		if label == "" {
			label = "todo change"
		}
		fmt.Fprintf(&b, "- %s %s\n", checkbox, label)
	case "delete":
		if itemID != "" {
			fmt.Fprintf(&b, "- Delete task `%s`\n", itemID)
		} else {
			b.WriteString("- Delete a task\n")
		}
	case "delete_done":
		b.WriteString("- Delete completed tasks\n")
	case "delete_all":
		b.WriteString("- Delete all tasks\n")
	case "in_progress":
		if itemID != "" {
			fmt.Fprintf(&b, "- Mark task `%s` in progress\n", itemID)
		} else {
			b.WriteString("- Change in-progress task\n")
		}
	case "reorder":
		b.WriteString("- Reorder workspace tasks\n")
	default:
		b.WriteString("- Update workspace tasks\n")
	}
	if ownerKind != "" {
		fmt.Fprintf(&b, "- List: `%s`\n", ownerKind)
	}
	if priority != "" && ownerKind != "Agent Checklist" {
		fmt.Fprintf(&b, "- Priority: `%s`\n", priority)
	}
	if group != "" {
		fmt.Fprintf(&b, "- Group: `%s`\n", group)
	}
	if len(tags) > 0 {
		fmt.Fprintf(&b, "- Tags: %s\n", manageTodosTagList(tags))
	}
	if sessionID != "" {
		fmt.Fprintf(&b, "- Conversation: `%s`\n", sessionID)
	}
	if parentID != "" {
		fmt.Fprintf(&b, "- Parent Task: `%s`\n", parentID)
	}
	if hasInProgress {
		fmt.Fprintf(&b, "- In Progress: `%s`\n", manageTodosYesNo(inProgress))
	}
	if hasDone && action != "create" && action != "update" {
		fmt.Fprintf(&b, "- Done: `%s`\n", manageTodosYesNo(done))
	}
	if len(orderedIDs) > 0 {
		b.WriteString("\n## Requested order\n\n")
		for i, orderedID := range orderedIDs {
			fmt.Fprintf(&b, "%d. `%s`\n", i+1, strings.TrimSpace(orderedID))
		}
	}
	b.WriteString("\n## Details\n\n")
	if action != "" {
		fmt.Fprintf(&b, "- action: `%s`\n", action)
	}
	if workspacePath != "" {
		fmt.Fprintf(&b, "- workspace: `%s`\n", workspacePath)
	}
	if ownerKind != "" {
		fmt.Fprintf(&b, "- owner_kind: `%s`\n", ownerKind)
	}
	if itemID != "" {
		fmt.Fprintf(&b, "- id: `%s`\n", itemID)
	}
	if action == "update" && !hasDone {
		b.WriteString("- done: _unchanged_\n")
	}
	if action == "update" && text == "" {
		b.WriteString("- text: _unchanged_\n")
	}
	return strings.TrimSpace(b.String())
}

func manageTodosActionLabel(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "create":
		return "Create Task"
	case "update":
		return "Update Task"
	case "delete":
		return "Delete Task"
	case "delete_done":
		return "Delete Done Tasks"
	case "delete_all":
		return "Delete All Tasks"
	case "in_progress":
		return "In Progress Task"
	case "reorder":
		return "Reorder Tasks"
	case "batch":
		return "Atomic Batch"
	default:
		return ""
	}
}

func manageTodosPreviewDetailText(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, manageTodosPreviewDetailPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, manageTodosPreviewDetailPrefix)), true
}

func manageTodosBatchPreviewLines(payload map[string]any) []chatRenderLine {
	operations := jsonObjectSlice(payload, "operations")
	if len(operations) == 0 {
		return []chatRenderLine{{Text: "No operations were provided."}}
	}
	lines := make([]chatRenderLine, 0, len(operations)*3)
	for i, operation := range operations {
		rowText, detailText := manageTodosBatchOperationPreview(operation)
		lines = append(lines, chatRenderLine{Text: rowText})
		if detailText != "" {
			lines = append(lines, chatRenderLine{Text: manageTodosPreviewDetailPrefix + detailText})
		}
		if i < len(operations)-1 {
			lines = append(lines, chatRenderLine{Text: ""})
		}
	}
	return lines
}

func manageTodosBatchOperationPreview(payload map[string]any) (string, string) {
	action := strings.ToLower(strings.TrimSpace(mapStringArg(payload, "action")))
	itemID := strings.TrimSpace(mapStringArg(payload, "id"))
	text := strings.TrimSpace(mapStringArg(payload, "text"))
	return manageTodosOperationRow(action, itemID, text, payload), manageTodosOperationDetail(action, itemID, payload)
}

func manageTodosOperationRow(action, itemID, text string, payload map[string]any) string {
	done, hasDone := manageTodosBoolArg(payload, "done")
	checkbox := "[ ]"
	if hasDone && done {
		checkbox = "[x]"
	}
	switch action {
	case "create":
		return fmt.Sprintf("%s %s", checkbox, firstNonEmptyStringUI(text, "New task"))
	case "update":
		if text != "" {
			return fmt.Sprintf("%s %s", checkbox, text)
		}
		if itemID != "" {
			return fmt.Sprintf("%s Update %s", checkbox, itemID)
		}
		return fmt.Sprintf("%s Update task", checkbox)
	case "delete":
		if itemID != "" {
			return fmt.Sprintf("%s Delete %s", checkbox, itemID)
		}
		return fmt.Sprintf("%s Delete task", checkbox)
	case "delete_done":
		return fmt.Sprintf("%s Delete done tasks", checkbox)
	case "delete_all":
		return fmt.Sprintf("%s Delete all tasks", checkbox)
	case "in_progress":
		if itemID != "" {
			return fmt.Sprintf("%s Mark %s in progress", checkbox, itemID)
		}
		return fmt.Sprintf("%s Mark task in progress", checkbox)
	case "reorder":
		orderedIDs := jsonStringSlice(payload, "ordered_ids")
		if len(orderedIDs) == 0 {
			return fmt.Sprintf("%s Reorder tasks", checkbox)
		}
		return fmt.Sprintf("%s Reorder %d tasks", checkbox, len(orderedIDs))
	default:
		if text != "" {
			return fmt.Sprintf("%s %s", checkbox, text)
		}
		if itemID != "" {
			return fmt.Sprintf("%s Todo %s", checkbox, itemID)
		}
		if action != "" {
			return fmt.Sprintf("%s %s", checkbox, action)
		}
		return fmt.Sprintf("%s Todo change", checkbox)
	}
}

func manageTodosOperationDetail(action, itemID string, payload map[string]any) string {
	details := make([]string, 0, 9)
	if label := manageTodosOperationActionDetail(action); label != "" {
		details = append(details, label)
	}
	if action == "update" && itemID != "" {
		details = append(details, "ID: "+itemID)
	}
	ownerKind := manageTodosOwnerKindLabel(strings.TrimSpace(mapStringArg(payload, "owner_kind")))
	if priority := strings.TrimSpace(mapStringArg(payload, "priority")); priority != "" && ownerKind != "Agent Checklist" {
		details = append(details, "Priority: "+priority)
	}
	if group := strings.TrimSpace(mapStringArg(payload, "group")); group != "" {
		details = append(details, "Group: "+group)
	}
	tags := jsonStringSlice(payload, "tags")
	if len(tags) > 0 {
		cleaned := make([]string, 0, len(tags))
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			cleaned = append(cleaned, "#"+tag)
		}
		if len(cleaned) > 0 {
			details = append(details, "Tags: "+strings.Join(cleaned, ", "))
		}
	}
	if ownerKind != "" {
		details = append(details, "List: "+ownerKind)
	}
	if sessionID := strings.TrimSpace(mapStringArg(payload, "session_id")); sessionID != "" {
		details = append(details, "Conversation: "+sessionID)
	}
	if parentID := strings.TrimSpace(mapStringArg(payload, "parent_id")); parentID != "" {
		details = append(details, "Parent: "+parentID)
	}
	if inProgress, ok := manageTodosBoolArg(payload, "in_progress"); ok {
		details = append(details, "In Progress: "+manageTodosYesNo(inProgress))
	}
	if done, ok := manageTodosBoolArg(payload, "done"); ok {
		details = append(details, "Done: "+manageTodosYesNo(done))
	}
	if action == "reorder" {
		if orderedIDs := jsonStringSlice(payload, "ordered_ids"); len(orderedIDs) > 0 {
			details = append(details, fmt.Sprintf("Items: %d", len(orderedIDs)))
		}
	}
	return strings.Join(details, " • ")
}

func manageTodosOperationActionDetail(action string) string {
	switch action {
	case "create":
		return "Create"
	case "update":
		return "Update"
	case "delete":
		return "Delete"
	case "delete_done":
		return "Delete Done"
	case "delete_all":
		return "Delete All"
	case "in_progress":
		return "Mark In Progress"
	case "reorder":
		return "Reorder"
	default:
		if action == "" {
			return ""
		}
		return strings.ToUpper(action[:1]) + action[1:]
	}
}

func pluralizeManageTodosOperations(count int) string {
	if count == 1 {
		return "operation"
	}
	return "operations"
}

func manageTodosBoolArg(payload map[string]any, key string) (bool, bool) {
	if payload == nil {
		return false, false
	}
	value, ok := payload[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(typed))
		if trimmed == "" {
			return false, false
		}
		return trimmed == "1" || trimmed == "true" || trimmed == "yes" || trimmed == "y" || trimmed == "on", true
	default:
		return false, false
	}
}

func manageTodosTagList(tags []string) string {
	parts := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		parts = append(parts, "`#"+tag+"`")
	}
	if len(parts) == 0 {
		return "_none_"
	}
	return strings.Join(parts, " ")
}

func manageTodosYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func manageTodosOwnerKindLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user":
		return "User Todos"
	case "agent":
		return "Agent Checklist"
	default:
		return ""
	}
}
