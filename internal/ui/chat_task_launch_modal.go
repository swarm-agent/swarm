package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func isTaskLaunchPermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "task_launch")
}

func (p *ChatPage) taskLaunchModalActive() bool {
	return strings.TrimSpace(p.taskLaunchPermission) != ""
}

func (p *ChatPage) OpenTaskLaunchPermissionModal(record ChatPermissionRecord) bool {
	if !isTaskLaunchPermission(record) {
		return false
	}
	p.taskLaunchPermission = strings.TrimSpace(record.ID)
	p.taskLaunchScroll = 0
	p.taskLaunchInput = ""
	p.taskLaunchApproveRect = Rect{}
	p.taskLaunchDenyRect = Rect{}
	p.statusLine = "task launch permission active"
	return true
}

func (p *ChatPage) closeTaskLaunchModal() {
	p.taskLaunchPermission = ""
	p.taskLaunchScroll = 0
	p.taskLaunchInput = ""
	p.taskLaunchApproveRect = Rect{}
	p.taskLaunchDenyRect = Rect{}
}

func (p *ChatPage) handleTaskLaunchModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.taskLaunchModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.taskLaunchApproveRect.Contains(x, y):
			p.resolveTaskLaunchModal(true)
			return true
		case p.taskLaunchDenyRect.Contains(x, y):
			p.resolveTaskLaunchModal(false)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.taskLaunchScroll--
		if p.taskLaunchScroll < 0 {
			p.taskLaunchScroll = 0
		}
		return true
	case buttons&tcell.WheelDown != 0:
		p.taskLaunchScroll++
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleTaskLaunchModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.taskLaunchModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	switch {
	case p.keybinds.Match(ev, KeybindPermissionApprove):
		p.resolveTaskLaunchModal(true)
		return true
	case p.keybinds.Match(ev, KeybindPermissionDeny):
		p.resolveTaskLaunchModal(false)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.taskLaunchScroll -= 6
		if p.taskLaunchScroll < 0 {
			p.taskLaunchScroll = 0
		}
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.taskLaunchScroll += 6
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.taskLaunchScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.taskLaunchScroll = 1 << 30
		return true
	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		if len(p.taskLaunchInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.taskLaunchInput)
			if sz > 0 {
				p.taskLaunchInput = p.taskLaunchInput[:len(p.taskLaunchInput)-sz]
			}
		}
		return true
	case ev.Key() == tcell.KeyCtrlU:
		p.taskLaunchInput = ""
		return true
	case ev.Key() == tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.taskLaunchInput) < chatMaxInputRunes {
			p.taskLaunchInput += string(r)
		}
		return true
	default:
		return true
	}
}

func (p *ChatPage) drawTaskLaunchModal(s tcell.Screen, screen Rect) {
	if !p.taskLaunchModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.taskLaunchPermission)
	if !ok {
		p.closeTaskLaunchModal()
		return
	}
	bodyWidth := maxInt(16, minInt(104, screen.W-6))
	lines := p.taskLaunchModalLines(record, bodyWidth-4)
	modalW := minInt(108, screen.W-8)
	inputRows := p.taskLaunchInputRows(maxInt(1, modalW-6))
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	bodyH := minInt(len(lines), screen.H-(inputRows+8))
	if bodyH < 4 {
		bodyH = 4
	}
	modalH := bodyH + inputRows + 7
	if modalH > screen.H-2 {
		modalH = screen.H - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{X: maxInt(1, screen.X+(screen.W-modalW)/2), Y: maxInt(1, screen.Y+(screen.H-modalH)/2), W: modalW, H: modalH}

	p.taskLaunchApproveRect = Rect{}
	p.taskLaunchDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), "Review Task Launch")
	hint := "Enter approve · Esc deny"
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W/2))
	subtitle := "Review agents, permissions, prompt, and assigned roles."
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 3, W: modal.W - 4, H: modal.H - (inputRows + 6)}
	maxScroll := maxInt(0, len(lines)-bodyRect.H)
	if p.taskLaunchScroll > maxScroll {
		p.taskLaunchScroll = maxScroll
	}
	if p.taskLaunchScroll < 0 {
		p.taskLaunchScroll = 0
	}
	for row := 0; row < bodyRect.H; row++ {
		idx := p.taskLaunchScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, bodyRect.X, bodyRect.Y+row, bodyRect.W, lines[idx])
	}
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.taskLaunchScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, modal.Y+2, 18, onPanel(p.theme.TextMuted), scrollLabel)
	}

	inputY := modal.Y + modal.H - (inputRows + 3)
	textX := modal.X + 2
	textW := modal.W - 4
	if textW > 0 {
		inputLabel := "Message to agent (optional):"
		DrawText(s, modal.X+2, inputY-1, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(inputLabel, modal.W-4))
		visibleLines := p.taskLaunchInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.taskLaunchInput) == "" {
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
	help := "PgUp/PgDn scroll • Enter approve • Esc deny"
	helpWidth := modal.W - 4
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.taskLaunchScroll+1, maxScroll+1)
		scrollWidth := utf8.RuneCountInString(scrollLabel)
		DrawTextRight(s, modal.X+modal.W-2, helpY, maxInt(scrollWidth, modal.W/2), onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, modal.W/2))
		remaining := modal.W - 4 - scrollWidth - 2
		if remaining > 12 {
			helpWidth = remaining
		}
	}
	DrawText(s, modal.X+2, helpY, helpWidth, onPanel(p.theme.TextMuted), clampEllipsis(help, helpWidth))

	actionY := modal.Y + modal.H - 2
	actionX := modal.X + 2
	p.taskLaunchApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, "Enter Approve", p.theme.Success)
	p.taskLaunchDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, "Esc Deny", p.theme.Error)
	if p.taskLaunchApproveRect.W == 0 && p.taskLaunchDenyRect.W == 0 {
		compactHint := "Enter approve · Esc deny"
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(compactHint, modal.W-4))
	}
}

type taskLaunchModalSection struct {
	Title       string
	BorderStyle tcell.Style
	TitleStyle  tcell.Style
	Lines       []chatRenderLine
}

func (p *ChatPage) taskLaunchModalLines(record ChatPermissionRecord, width int) []chatRenderLine {
	width = maxInt(16, width)
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return []chatRenderLine{{Text: "Unable to decode task launch manifest.", Style: p.theme.Error}}
	}

	goal := strings.TrimSpace(firstNonEmptyToolValue(jsonString(manifest, "goal"), jsonString(manifest, "description")))
	prompt := strings.TrimSpace(jsonString(manifest, "prompt"))
	launches := p.taskLaunchOrderedLaunches(jsonObjectSlice(manifest, "launches"))
	launchCount := maxInt(len(launches), jsonInt(manifest, "launch_count"))

	reviewLines := p.taskLaunchTopSummaryLines(manifest, launches, launchCount, prompt)
	if goal != "" && !strings.EqualFold(goal, prompt) {
		reviewLines = append(reviewLines, p.taskLaunchKeyValueLine("task", goal, p.theme.Text))
	}
	sections := []taskLaunchModalSection{
		{
			Title:       "Review",
			BorderStyle: p.theme.BorderActive,
			TitleStyle:  p.theme.Secondary.Bold(true),
			Lines:       reviewLines,
		},
	}

	out := make([]chatRenderLine, 0, 32)
	for i, section := range sections {
		if i > 0 {
			out = append(out, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		out = append(out, p.taskLaunchSectionBoxLines(section, width)...)
	}
	return out
}

func (p *ChatPage) taskLaunchTextLine(text string, style tcell.Style) chatRenderLine {
	return chatRenderLine{Text: strings.TrimSpace(text), Style: style}
}

func (p *ChatPage) taskLaunchKeyValueLine(label, value string, valueStyle tcell.Style) chatRenderLine {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if value == "" {
		value = "—"
		valueStyle = p.theme.TextMuted
	}
	spans := []chatRenderSpan{
		{Text: label + ": ", Style: p.theme.TextMuted},
		{Text: value, Style: valueStyle},
	}
	return chatRenderLine{Text: chatRenderSpansText(spans), Style: valueStyle, Spans: spans}
}

func (p *ChatPage) taskLaunchTopSummaryLines(manifest map[string]any, launches []map[string]any, launchCount int, prompt string) []chatRenderLine {
	lines := make([]chatRenderLine, 0, 4)
	if agents := p.taskLaunchAgentsSummary(manifest, launches, launchCount); agents != "" {
		lines = append(lines, p.taskLaunchKeyValueLine("agent", agents, p.theme.Text))
	}
	lines = append(lines, p.taskLaunchKeyValueLine("permissions", p.taskLaunchPermissionSummary(manifest), p.theme.Text))
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		lines = append(lines, p.taskLaunchKeyValueLine("base prompt", taskLaunchInlineSummary(prompt), p.theme.Text))
	}
	if roles := p.taskLaunchAssignedRolesSummary(launches); roles != "" {
		lines = append(lines, p.taskLaunchKeyValueLine("assigned roles", roles, p.theme.Text))
	}
	return lines
}

func (p *ChatPage) taskLaunchAgentsSummary(manifest map[string]any, launches []map[string]any, launchCount int) string {
	if len(launches) == 0 {
		return strings.TrimSpace(firstNonEmptyToolValue(
			jsonString(manifest, "subagent_type"),
			jsonString(manifest, "resolved_agent_name"),
			jsonString(manifest, "agent_type"),
			jsonString(manifest, "subagent"),
		))
	}
	counts := make(map[string]int, len(launches))
	order := make([]string, 0, len(launches))
	for _, launch := range launches {
		agent := strings.TrimSpace(p.taskLaunchLaunchAgent(launch))
		if agent == "" {
			agent = "subagent"
		}
		if _, ok := counts[agent]; !ok {
			order = append(order, agent)
		}
		counts[agent]++
	}
	parts := make([]string, 0, len(order))
	for _, agent := range order {
		count := counts[agent]
		if count > 1 || len(order) == 1 && count == launchCount && launchCount > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", agent, count))
			continue
		}
		parts = append(parts, agent)
	}
	return strings.Join(parts, ", ")
}

func (p *ChatPage) taskLaunchAssignedRolesSummary(launches []map[string]any) string {
	if len(launches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(launches))
	for _, launch := range launches {
		agent := strings.TrimSpace(p.taskLaunchLaunchAgent(launch))
		assignment := strings.TrimSpace(p.taskLaunchLaunchAssignment(launch))
		if agent == "" && assignment == "" {
			continue
		}
		if assignment == "" {
			parts = append(parts, agent)
			continue
		}
		if agent == "" {
			parts = append(parts, assignment)
			continue
		}
		parts = append(parts, agent+" — "+taskLaunchInlineSummary(assignment))
	}
	return strings.Join(parts, "; ")
}

func taskLaunchInlineSummary(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func (p *ChatPage) taskLaunchPermissionSummary(manifest map[string]any) string {
	allowBash := jsonBool(manifest, "allow_bash")
	disabled := jsonStringSlice(manifest, "disabled_tools")
	mode := strings.TrimSpace(jsonString(manifest, "effective_child_mode"))
	parts := make([]string, 0, 4)
	if allowBash {
		parts = append(parts, "bash allowed")
	} else {
		parts = append(parts, "bash disabled")
	}
	if disabled = conciseTaskLaunchDisabledTools(disabled); len(disabled) > 0 {
		parts = append(parts, "disabled: "+strings.Join(disabled, ", "))
	}
	if mode != "" {
		parts = append(parts, "mode "+mode)
	}
	if reportMaxChars := jsonInt(manifest, "report_max_chars"); reportMaxChars > 0 {
		parts = append(parts, fmt.Sprintf("report %d chars", reportMaxChars))
	}
	return strings.Join(parts, " · ")
}

func conciseTaskLaunchDisabledTools(disabled []string) []string {
	if len(disabled) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(disabled))
	out := make([]string, 0, len(disabled))
	for _, name := range disabled {
		name = strings.TrimSpace(strings.ReplaceAll(name, "-", "_"))
		switch name {
		case "", "bash":
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (p *ChatPage) taskLaunchMarkdownSectionLines(body, empty string) []chatRenderLine {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	rows := trimBlankRenderLines(p.renderMarkdownRows(body, p.theme.MarkdownText, p.theme.Text))
	if len(rows) == 0 {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	return rows
}

func trimBlankRenderLines(lines []chatRenderLine) []chatRenderLine {
	start := 0
	for start < len(lines) && chatRenderLineText(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && chatRenderLineText(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return nil
	}
	return cloneRenderLines(lines[start:end])
}

func (p *ChatPage) taskLaunchOrderedLaunches(launches []map[string]any) []map[string]any {
	if len(launches) == 0 {
		return nil
	}
	ordered := make([]map[string]any, 0, len(launches))
	for _, launch := range launches {
		if launch == nil {
			continue
		}
		ordered = append(ordered, launch)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := p.taskLaunchLaunchIndex(ordered[i], i)
		right := p.taskLaunchLaunchIndex(ordered[j], j)
		if left != right {
			return left < right
		}
		return p.taskLaunchLaunchAgent(ordered[i]) < p.taskLaunchLaunchAgent(ordered[j])
	})
	return ordered
}

func (p *ChatPage) taskLaunchLaunchIndex(launch map[string]any, fallback int) int {
	if idx := jsonInt(launch, "launch_index"); idx > 0 {
		return idx
	}
	if idx := jsonInt(launch, "index"); idx > 0 {
		return idx
	}
	return fallback + 1
}

func (p *ChatPage) taskLaunchLaunchAgent(launch map[string]any) string {
	agent := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(launch, "requested_subagent_type"),
		jsonString(launch, "resolved_agent_name"),
		jsonString(launch, "agent_type"),
		jsonString(launch, "subagent"),
		jsonString(launch, "requested_subagent"),
	))
	if agent == "" {
		return "subagent"
	}
	return agent
}

func (p *ChatPage) taskLaunchLaunchAssignment(launch map[string]any) string {
	return strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(launch, "meta_prompt"),
		jsonString(launch, "description"),
		jsonString(launch, "prompt"),
	))
}

func (p *ChatPage) taskLaunchLaunchTableLines(launches []map[string]any, width int) []chatRenderLine {
	if len(launches) == 0 {
		return []chatRenderLine{{Text: "No launches were included in the manifest.", Style: p.theme.TextMuted}}
	}
	width = maxInt(24, width)
	maxAgentRunes := utf8.RuneCountInString("Agent")
	for _, launch := range launches {
		maxAgentRunes = maxInt(maxAgentRunes, utf8.RuneCountInString(p.taskLaunchLaunchAgent(launch)))
	}
	agentWidth := minInt(maxInt(10, maxAgentRunes), maxInt(10, width/3))
	prefixWidth := utf8.RuneCountInString(fmt.Sprintf("%-4s %-*s ", "#", agentWidth, "Agent"))
	if prefixWidth >= width-8 {
		agentWidth = maxInt(8, width-12)
		prefixWidth = utf8.RuneCountInString(fmt.Sprintf("%-4s %-*s ", "#", agentWidth, "Agent"))
	}
	assignmentWidth := maxInt(8, width-prefixWidth)

	headerSpans := []chatRenderSpan{
		{Text: fmt.Sprintf("%-4s %-*s ", "#", agentWidth, "Agent"), Style: p.theme.Secondary.Bold(true)},
		{Text: "Assignment", Style: p.theme.Secondary.Bold(true)},
	}
	out := []chatRenderLine{{Text: chatRenderSpansText(headerSpans), Style: p.theme.Secondary, Spans: headerSpans}}
	out = append(out, chatRenderLine{Text: strings.Repeat("─", width), Style: p.theme.Border})

	for i, launch := range launches {
		idxLabel := fmt.Sprintf("#%d", p.taskLaunchLaunchIndex(launch, i))
		agent := clampEllipsis(p.taskLaunchLaunchAgent(launch), agentWidth)
		assignment := p.taskLaunchLaunchAssignment(launch)
		assignmentLines := p.taskLaunchMarkdownSectionLines(assignment, "No launch-specific instructions.")
		firstPrefix := fmt.Sprintf("%-4s %-*s ", idxLabel, agentWidth, agent)
		continuationPrefix := fmt.Sprintf("%-4s %-*s ", "", agentWidth, "")
		for rowIndex, row := range assignmentLines {
			prefix := continuationPrefix
			if rowIndex == 0 {
				prefix = firstPrefix
			}
			wrapped := wrapRenderLineWithCustomPrefixes(prefix, continuationPrefix, row, prefixWidth+assignmentWidth)
			out = append(out, wrapped...)
		}
		if i < len(launches)-1 {
			out = append(out, chatRenderLine{Text: strings.Repeat("·", width), Style: p.theme.TextMuted})
		}
	}
	return out
}

func (p *ChatPage) taskLaunchSectionBoxLines(section taskLaunchModalSection, width int) []chatRenderLine {
	width = maxInt(12, width)
	innerWidth := maxInt(1, width-4)
	lines := make([]chatRenderLine, 0, len(section.Lines)+2)
	lines = append(lines, p.taskLaunchSectionBorderLine('┌', '┐', section.Title, width, section.BorderStyle, section.TitleStyle))
	if len(section.Lines) == 0 {
		section.Lines = []chatRenderLine{{Text: "", Style: p.theme.Text}}
	}
	for _, line := range section.Lines {
		if chatRenderLineText(line) == "" {
			lines = append(lines, p.taskLaunchSectionContentLine(chatRenderLine{Text: "", Style: p.theme.Text}, innerWidth, section.BorderStyle))
			continue
		}
		for _, wrapped := range wrapRenderLineWithCustomPrefixes("", "", line, innerWidth) {
			lines = append(lines, p.taskLaunchSectionContentLine(wrapped, innerWidth, section.BorderStyle))
		}
	}
	lines = append(lines, p.taskLaunchSectionBorderLine('└', '┘', "", width, section.BorderStyle, section.TitleStyle))
	return lines
}

func (p *ChatPage) taskLaunchSectionBorderLine(left, right rune, title string, width int, borderStyle, titleStyle tcell.Style) chatRenderLine {
	fillWidth := maxInt(0, width-2)
	if strings.TrimSpace(title) == "" {
		text := string(left) + strings.Repeat("─", fillWidth) + string(right)
		return chatRenderLine{Text: text, Style: borderStyle}
	}
	label := "─ " + clampEllipsis(strings.TrimSpace(title), maxInt(1, fillWidth-3)) + " "
	remaining := fillWidth - utf8.RuneCountInString(label)
	if remaining < 0 {
		remaining = 0
	}
	spans := []chatRenderSpan{
		{Text: string(left), Style: borderStyle},
		{Text: label, Style: titleStyle},
		{Text: strings.Repeat("─", remaining), Style: borderStyle},
		{Text: string(right), Style: borderStyle},
	}
	return chatRenderLine{Text: chatRenderSpansText(spans), Style: borderStyle, Spans: spans}
}

func (p *ChatPage) taskLaunchSectionContentLine(line chatRenderLine, innerWidth int, borderStyle tcell.Style) chatRenderLine {
	if innerWidth <= 0 {
		innerWidth = 1
	}
	body := cloneRenderSpans(line.Spans)
	if len(body) == 0 && line.Text != "" {
		body = []chatRenderSpan{{Text: line.Text, Style: line.Style}}
	}
	if renderSpansRuneCount(body) > innerWidth {
		body, _ = splitRenderSpansByRunes(body, innerWidth)
	}
	padWidth := innerWidth - renderSpansRuneCount(body)
	if padWidth < 0 {
		padWidth = 0
	}
	padStyle := line.Style
	if padStyle == tcell.StyleDefault {
		padStyle = p.theme.Text
	}
	spans := make([]chatRenderSpan, 0, len(body)+3)
	spans = append(spans, chatRenderSpan{Text: "│ ", Style: borderStyle})
	spans = append(spans, body...)
	if padWidth > 0 {
		spans = append(spans, chatRenderSpan{Text: strings.Repeat(" ", padWidth), Style: padStyle})
	}
	spans = append(spans, chatRenderSpan{Text: " │", Style: borderStyle})
	return chatRenderLine{Text: chatRenderSpansText(spans), Style: padStyle, Spans: spans}
}

func (p *ChatPage) taskLaunchInputRows(textWidth int) int {
	lines := p.taskLaunchInputWrappedLines(textWidth)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	if height > chatPlanExitInputMaxLines {
		height = chatPlanExitInputMaxLines
	}
	return height
}

func (p *ChatPage) taskLaunchInputVisibleLines(textWidth, inputRows int) []string {
	lines := p.taskLaunchInputWrappedLines(textWidth)
	if inputRows < 1 {
		inputRows = 1
	}
	if len(lines) <= inputRows {
		return lines
	}
	return lines[len(lines)-inputRows:]
}

func (p *ChatPage) taskLaunchInputWrappedLines(textWidth int) []string {
	if textWidth <= 0 {
		return []string{""}
	}
	text := p.taskLaunchInput
	if text == "" {
		return []string{""}
	}
	lines := wrapWithCustomPrefixes("", "", text, textWidth)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (p *ChatPage) resolveTaskLaunchModal(approve bool) {
	permissionID := strings.TrimSpace(p.taskLaunchPermission)
	note := strings.TrimSpace(p.taskLaunchInput)
	p.closeTaskLaunchModal()
	if permissionID == "" {
		return
	}
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", note)
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", note)
}
