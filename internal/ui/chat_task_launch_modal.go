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
	p.taskLaunchPromptExpanded = false
	p.taskLaunchApproveRect = Rect{}
	p.taskLaunchDenyRect = Rect{}
	p.statusLine = "task launch permission active"
	return true
}

func (p *ChatPage) closeTaskLaunchModal() {
	p.taskLaunchPermission = ""
	p.taskLaunchScroll = 0
	p.taskLaunchInput = ""
	p.taskLaunchPromptExpanded = false
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
	case ev.Key() == tcell.KeyCtrlP:
		p.taskLaunchPromptExpanded = !p.taskLaunchPromptExpanded
		p.taskLaunchScroll = 1 << 30
		if p.taskLaunchPromptExpanded {
			p.statusLine = "task launch prompt opened"
		} else {
			p.statusLine = "task launch prompt hidden"
		}
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
	headerManifest := decodePermissionArguments(record.ToolArguments)
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(taskLaunchModalTitleFromManifest(headerManifest), modal.W-4))
	hint := "Enter launch · Esc deny"
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W/2))
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(taskLaunchModalSubtitleFromManifest(headerManifest), modal.W-4))

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
		inputLabel := "Message to agent (optional)"
		DrawText(s, modal.X+2, inputY-1, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(inputLabel, modal.W-4))
		visibleLines := p.taskLaunchInputVisibleLines(maxInt(1, textW), inputRows)
		if strings.TrimSpace(p.taskLaunchInput) == "" {
			DrawText(s, textX, inputY, textW, onPanel(p.theme.TextMuted), clampEllipsis("Add any notes for the agents before launch...", textW))
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
	help := "PgUp/PgDn scroll • Ctrl+P prompt • Enter launch • Esc deny"
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
	p.taskLaunchApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, "Enter Launch", p.theme.Success)
	p.taskLaunchDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, "Esc Deny", p.theme.Secondary)
	if p.taskLaunchApproveRect.W == 0 && p.taskLaunchDenyRect.W == 0 {
		compactHint := "Enter launch · Esc deny"
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(compactHint, modal.W-4))
	}
}

func (p *ChatPage) taskLaunchModalLines(record ChatPermissionRecord, width int) []chatRenderLine {
	width = maxInt(16, width)
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return []chatRenderLine{{Text: "Unable to decode task launch manifest.", Style: p.theme.Error}}
	}

	prompt := strings.TrimSpace(jsonString(manifest, "prompt"))
	launches := p.taskLaunchOrderedLaunches(jsonObjectSlice(manifest, "launches"))
	launchCount := maxInt(len(launches), jsonInt(manifest, "launch_count"))

	out := make([]chatRenderLine, 0, 96)
	out = append(out, p.taskLaunchSummaryChipLines(manifest, launchCount, width)...)
	out = append(out, chatRenderLine{Text: "", Style: p.theme.TextMuted})
	out = append(out, p.taskLaunchDividerLine("Subagents", width))
	out = append(out, p.taskLaunchLaunchCardLines(launches, maxInt(16, width))...)
	out = append(out, chatRenderLine{Text: "", Style: p.theme.TextMuted})
	out = append(out, p.taskLaunchDividerLine("Full prompt", width))
	out = append(out, p.taskLaunchPromptSectionLines(prompt, width)...)
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

func taskLaunchModalTitleFromManifest(manifest map[string]any) string {
	launchCount := maxInt(jsonInt(manifest, "launch_count"), len(jsonObjectSlice(manifest, "launches")))
	if launchCount < 1 {
		launchCount = 1
	}
	prefix := "Launch subagent"
	if launchCount != 1 {
		prefix = fmt.Sprintf("Launch %d subagents", launchCount)
	}
	task := taskLaunchPromptPreview(firstNonEmptyToolValue(jsonString(manifest, "description"), jsonString(manifest, "goal"), jsonString(manifest, "prompt")), 10)
	if task == "" || task == "No prompt text." {
		return prefix
	}
	return prefix + ": " + task
}

func taskLaunchModalSubtitleFromManifest(manifest map[string]any) string {
	parts := []string{"Review before launch"}
	return strings.Join(parts, " · ")
}

func (p *ChatPage) taskLaunchSummaryChipLines(manifest map[string]any, launchCount, width int) []chatRenderLine {
	launchNoun := "launch"
	if launchCount != 1 {
		launchNoun = "launches"
	}
	launchLabel := fmt.Sprintf("^ %d %s", launchCount, launchNoun)
	chips := []string{"[" + launchLabel + "]"}
	if toolsLabel := taskLaunchToolsSummary(jsonObject(manifest, "resolved_tools")); toolsLabel != "" {
		chips = append(chips, "[! "+toolsLabel+"]")
	}
	if resolvedAgent := strings.TrimSpace(jsonString(manifest, "resolved_agent_name")); resolvedAgent != "" {
		chips = append(chips, "[# Router: "+resolvedAgent+"]")
	}
	used, remaining := jsonInt(manifest, "automatic_budget_used"), jsonInt(manifest, "automatic_budget_remaining")
	if used > 0 || remaining > 0 {
		chips = append(chips, fmt.Sprintf("[Budget: %d used · %d remaining]", used, remaining))
	}

	lines := make([]chatRenderLine, 0, len(chips))
	current := ""
	for _, chip := range chips {
		if current == "" {
			current = chip
			continue
		}
		candidate := current + "  " + chip
		if utf8.RuneCountInString(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, chatRenderLine{Text: current, Style: p.theme.TextMuted})
		current = chip
	}
	if current != "" {
		lines = append(lines, chatRenderLine{Text: current, Style: p.theme.TextMuted})
	}
	return lines
}

func taskLaunchToolsSummary(tools map[string]any) string {
	execution := strings.TrimSpace(firstNonEmptyToolValue(jsonString(tools, "effective_execution_mode"), jsonString(tools, "runtime_mode")))
	if execution != "" {
		if execution == "read" {
			return "read-only tools"
		}
		return execution + " tools"
	}
	if preset := strings.TrimSpace(jsonString(tools, "preset")); preset != "" {
		return strings.ReplaceAll(preset, "_", " ") + " tools"
	}
	if allowed := jsonStringSlice(tools, "allowed_tools"); len(allowed) > 0 {
		return fmt.Sprintf("%d allowed tool%s", len(allowed), pluralSuffix(len(allowed)))
	}
	return ""
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func (p *ChatPage) taskLaunchDividerLine(title string, width int) chatRenderLine {
	label := strings.ToUpper(strings.TrimSpace(title))
	if label == "" {
		label = "SECTION"
	}
	prefix := "─ " + label + " "
	remaining := maxInt(0, width-utf8.RuneCountInString(prefix))
	spans := []chatRenderSpan{
		{Text: prefix, Style: p.theme.TextMuted.Bold(true)},
		{Text: strings.Repeat("─", remaining), Style: p.theme.Border},
	}
	return chatRenderLine{Text: chatRenderSpansText(spans), Style: p.theme.Border, Spans: spans}
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

func (p *ChatPage) taskLaunchPromptToggleLine() chatRenderLine {
	label := "Show full prompt"
	if p.taskLaunchPromptExpanded {
		label = "Hide full prompt"
	}
	return chatRenderLine{Text: "[Ctrl+P] " + label, Style: p.theme.Primary}
}

func (p *ChatPage) taskLaunchPromptSectionLines(prompt string, width int) []chatRenderLine {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n"))
	if prompt == "" {
		return []chatRenderLine{{Text: "No prompt was included in the manifest.", Style: p.theme.TextMuted}}
	}
	width = maxInt(24, width)
	innerWidth := maxInt(1, width-4)
	preview := taskLaunchPromptPreview(prompt, 42)
	summary := fmt.Sprintf("%d words", taskLaunchPromptWordCount(prompt))
	body := []chatRenderLine{{Text: preview, Style: p.theme.TextMuted}}
	if p.taskLaunchPromptExpanded {
		body = p.taskLaunchMarkdownSectionLines(prompt, "No prompt was included in the manifest.")
	}

	lines := []chatRenderLine{p.taskLaunchPromptToggleLine()}
	lines = append(lines, p.taskLaunchSectionBorderLine('┌', '┐', "Prompt · "+summary, width, p.theme.Border, p.theme.Accent.Bold(true)))
	for _, row := range body {
		for _, wrapped := range wrapRenderLineWithCustomPrefixes("", "", row, innerWidth) {
			lines = append(lines, p.taskLaunchSectionContentLine(wrapped, innerWidth, p.theme.Border))
		}
	}
	lines = append(lines, p.taskLaunchSectionBorderLine('└', '┘', "", width, p.theme.Border, p.theme.Accent.Bold(true)))
	return lines
}

func taskLaunchPromptWordCount(prompt string) int {
	return len(strings.Fields(strings.TrimSpace(prompt)))
}

func taskLaunchPromptPreview(prompt string, maxWords int) string {
	words := strings.Fields(strings.TrimSpace(prompt))
	if len(words) == 0 {
		return "No prompt text."
	}
	if maxWords < 1 {
		maxWords = 1
	}
	if len(words) <= maxWords {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:maxWords], " ") + "..."
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
		jsonString(launch, "resolved_agent_name"),
		jsonString(launch, "agent_type"),
		jsonString(launch, "subagent"),
		jsonString(launch, "requested_subagent_type"),
		jsonString(launch, "requested_subagent"),
	))
	if agent == "" {
		return "subagent"
	}
	return agent
}

func (p *ChatPage) taskLaunchLaunchModelLine(launch map[string]any) string {
	parts := []string{p.taskLaunchLaunchAgent(launch)}
	provider := strings.TrimSpace(jsonString(launch, "subagent_provider"))
	model := strings.TrimSpace(jsonString(launch, "subagent_model"))
	switch {
	case provider != "" && model != "":
		parts = append(parts, provider+"/"+model)
	case model != "":
		parts = append(parts, model)
	case provider != "":
		parts = append(parts, provider)
	}
	if mode := strings.TrimSpace(firstNonEmptyToolValue(jsonString(launch, "effective_child_mode"), jsonString(launch, "child_mode"), jsonString(launch, "mode"))); mode != "" {
		parts = append(parts, mode)
	}
	return strings.Join(parts, " · ")
}

func (p *ChatPage) taskLaunchLaunchAssignment(launch map[string]any) string {
	return strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(launch, "meta_prompt"),
		jsonString(launch, "role"),
		jsonString(launch, "description"),
		jsonString(launch, "prompt"),
		jsonString(launch, "assignment_label"),
	))
}

func (p *ChatPage) taskLaunchLaunchCardLines(launches []map[string]any, width int) []chatRenderLine {
	if len(launches) == 0 {
		return []chatRenderLine{{Text: "No launches were included in the manifest.", Style: p.theme.TextMuted}}
	}
	width = maxInt(24, width)
	innerWidth := maxInt(1, width-4)
	out := make([]chatRenderLine, 0, len(launches)*8)
	for i, launch := range launches {
		idx := p.taskLaunchLaunchIndex(launch, i)
		agentName := p.taskLaunchLaunchAgent(launch)
		requested := strings.TrimSpace(firstNonEmptyToolValue(jsonString(launch, "requested_subagent_type"), jsonString(launch, "requested_subagent"), jsonString(launch, "subagent_type")))
		requestedLabel := ""
		if requested != "" && requested != agentName {
			requestedLabel = "via " + requested
		}
		modelLabel := p.taskLaunchLaunchModelLine(launch)
		if strings.HasPrefix(modelLabel, agentName+" · ") {
			modelLabel = strings.TrimPrefix(modelLabel, agentName+" · ")
		} else if modelLabel == agentName {
			modelLabel = ""
		}
		metaParts := make([]string, 0, 3)
		if requestedLabel != "" {
			metaParts = append(metaParts, requestedLabel)
		}
		if modelLabel != "" {
			metaParts = append(metaParts, modelLabel)
		}
		toolLabel := taskLaunchToolsSummary(jsonObject(launch, "resolved_tools"))

		out = append(out, p.taskLaunchCardBorderLine('┌', '┐', width))
		headerSpans := []chatRenderSpan{
			{Text: fmt.Sprintf("(%d) ", idx), Style: p.theme.TextMuted.Bold(true)},
			{Text: agentName, Style: p.theme.Text.Bold(true)},
		}
		if toolLabel != "" {
			headerSpans = append(headerSpans, chatRenderSpan{Text: " · " + toolLabel, Style: p.theme.TextMuted})
		}
		out = append(out, p.taskLaunchCardContentLine(chatRenderLine{Text: chatRenderSpansText(headerSpans), Style: p.theme.Text, Spans: headerSpans}, innerWidth))
		if len(metaParts) > 0 {
			out = append(out, p.taskLaunchCardContentLine(chatRenderLine{Text: strings.Join(metaParts, " · "), Style: p.theme.TextMuted}, innerWidth))
		}
		details := []struct{ label, value string }{
			{"Coder source", jsonString(launch, "source_agent_name")},
			{"Profile mode", jsonString(launch, "source_profile_mode")},
			{"Inherited runtime", jsonString(launch, "inherited_runtime_mode")},
			{"Deliverable", jsonString(launch, "deliverable")},
			{"Owned scope", strings.Join(jsonStringSlice(launch, "owned_scope"), ", ")},
			{"Dependency evidence", jsonString(launch, "dependency_evidence")},
			{"Isolation", firstNonEmptyToolValue(jsonString(launch, "isolation"), jsonString(launch, "worktree_isolation"))},
			{"Tools", strings.Join(jsonStringSlice(jsonObject(launch, "resolved_tools"), "allowed_tools"), ", ")},
		}
		for _, detail := range details {
			if strings.TrimSpace(detail.value) != "" {
				out = append(out, p.taskLaunchCardContentLine(p.taskLaunchKeyValueLine(detail.label, detail.value, p.theme.Text), innerWidth))
			}
		}
		assignment := p.taskLaunchMarkdownSectionLines(p.taskLaunchLaunchAssignment(launch), "No launch-specific instructions.")
		if len(assignment) > 0 {
			out = append(out, p.taskLaunchCardContentLine(chatRenderLine{Text: "", Style: p.theme.TextMuted}, innerWidth))
		}
		for _, row := range assignment {
			for _, wrapped := range wrapRenderLineWithCustomPrefixes("", "", row, innerWidth) {
				out = append(out, p.taskLaunchCardContentLine(wrapped, innerWidth))
			}
		}
		out = append(out, p.taskLaunchCardBorderLine('└', '┘', width))
		if i < len(launches)-1 {
			out = append(out, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
	}
	return out
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

func (p *ChatPage) taskLaunchCardBorderLine(left, right rune, width int) chatRenderLine {
	width = maxInt(4, width)
	text := string(left) + strings.Repeat("─", maxInt(0, width-2)) + string(right)
	return chatRenderLine{Text: text, Style: p.theme.Border}
}

func (p *ChatPage) taskLaunchCardContentLine(line chatRenderLine, innerWidth int) chatRenderLine {
	if innerWidth < 1 {
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
	spans = append(spans, chatRenderSpan{Text: "│ ", Style: p.theme.Border})
	spans = append(spans, body...)
	if padWidth > 0 {
		spans = append(spans, chatRenderSpan{Text: strings.Repeat(" ", padWidth), Style: padStyle})
	}
	spans = append(spans, chatRenderSpan{Text: " │", Style: p.theme.Border})
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
