package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) drawTimelineComponent(s tcell.Screen, rect Rect) {
	if rect.W < 8 || rect.H < 2 {
		return
	}

	contentW := rect.W
	contentH := rect.H
	if contentW <= 0 || contentH <= 0 {
		return
	}

	blocks := p.buildTimelineRenderBlocks(contentW)
	if len(blocks) == 0 {
		blocks = []chatTimelineRenderBlock{{
			Lines:  []chatRenderLine{{Text: "No messages yet. Type a prompt and press Enter.", Style: p.theme.TextMuted}},
			Height: 1,
		}}
	}

	totalLines := totalTimelineRenderBlockHeight(blocks)
	maxScroll := maxInt(0, totalLines-contentH)
	if p.timelineScroll > maxScroll {
		p.timelineScroll = maxScroll
	}
	if p.timelineScroll < 0 {
		p.timelineScroll = 0
	}

	skipLines := maxInt(0, totalLines-contentH-p.timelineScroll)
	y := rect.Y
	if p.timelineScroll == 0 && totalLines > 0 && totalLines < contentH {
		y = rect.Y + (contentH - totalLines)
	}
	for _, block := range blocks {
		if block.Height <= 0 || len(block.Lines) == 0 {
			continue
		}
		if skipLines >= block.Height {
			skipLines -= block.Height
			continue
		}
		for i := skipLines; i < block.Height && y < rect.Y+rect.H; i++ {
			DrawTimelineLine(s, rect.X, y, contentW, block.Lines[i])
			y++
		}
		skipLines = 0
		if y >= rect.Y+rect.H {
			break
		}
	}
}

func (p *ChatPage) buildTimelineLines(width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	lines := make([]chatRenderLine, 0, len(p.timeline)*3)
	for _, message := range p.timeline {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "user":
			lines = append(lines, p.renderUserMessageLines(message, width)...)
		case "assistant":
			lines = append(lines, p.renderAssistantMessageLines(message, width)...)
		case "reasoning":
			lines = append(lines, p.renderReasoningMessageLines(message, width)...)
		case "tool":
			lines = append(lines, p.renderToolMessageLines(message, width)...)
		case "system":
			for _, line := range wrapWithPrefix("· ", message.Text, width) {
				lines = append(lines, chatRenderLine{Text: line, Style: p.theme.Warning})
			}
		default:
			for _, line := range wrapWithPrefix("· ", message.Text, width) {
				lines = append(lines, chatRenderLine{Text: line, Style: p.theme.TextMuted})
			}
		}
		if len(lines) > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
	}

	if p.liveRunVisible() {
		liveTools := p.liveToolEntries(2)
		renderedLiveTools := 0
		for _, entry := range liveTools {
			if shouldSuppressLiveToolEntry(entry) {
				continue
			}
			lines = append(lines, p.renderLiveToolEntryLines(entry, width)...)
			renderedLiveTools++
		}
		if renderedLiveTools > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}

		liveAssistant := strings.TrimSpace(p.liveAssistant)
		if liveAssistant != "" {
			liveMessage := chatMessageItem{
				Role:      "assistant",
				Text:      liveAssistant,
				CreatedAt: time.Now().UnixMilli(),
			}
			assistantLines := p.renderAssistantMessageLines(liveMessage, width)
			if len(assistantLines) > 0 {
				last := assistantLines[len(assistantLines)-1]
				last = appendRenderLineSuffix(last, " "+p.spinnerFrame(), p.thinkingPulseStyle(), width)
				assistantLines[len(assistantLines)-1] = last
			}
			lines = append(lines, assistantLines...)
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
	}
	return lines
}

func (p *ChatPage) liveRunVisible() bool {
	if p == nil {
		return false
	}
	if p.effectiveRunActive() {
		return true
	}
	if p.streamingRun && p.runCancel != nil {
		return true
	}
	return false
}

func (p *ChatPage) renderToolMessageLines(message chatMessageItem, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	if payload, ok := toolTimelinePayload(message); ok {
		if isTaskToolPayload(payload) {
			if lines := p.renderTaskToolTableLines(message, payload, width); len(lines) > 0 {
				return lines
			}
		}
		if lines := p.renderSearchToolTableLines(message, payload, width); len(lines) > 0 {
			return lines
		}
	}
	body := strings.TrimSpace(message.Text)
	if body == "" {
		return nil
	}

	state := strings.ToLower(strings.TrimSpace(message.ToolState))
	prefix := p.toolSuccessSymbol + " "
	firstStyle := p.theme.Accent
	switch state {
	case "running":
		prefix = p.toolRunningSymbol + " "
		firstStyle = p.thinkingPulseStyle()
	case "error":
		prefix = p.toolErrorSymbol + " "
		firstStyle = p.theme.Error
	}

	out := make([]chatRenderLine, 0, 6)
	parts := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	isEditPreview := false
	previewLanguage := ""
	toolNameHint := ""
	if len(parts) > 0 {
		headline := strings.TrimSpace(parts[0])
		toolNameHint = extractToolNameHintFromHeadline(headline)
		isEditPreview = toolNameHint == "edit"
		previewLanguage = inferCodeLanguageFromPath(extractPathFromToolHeadline(toolNameHint, headline))
	}
	lineIndex := 0
	for _, part := range parts {
		trimmedPart := strings.TrimSpace(part)
		if trimmedPart == "" {
			continue
		}
		linePrefix := "  "
		lineStyle := p.theme.TextMuted
		if lineIndex == 0 {
			linePrefix = prefix
			lineStyle = firstStyle
			if toolNameHint == "" {
				toolNameHint = extractToolNameHintFromHeadline(trimmedPart)
				if previewLanguage == "" {
					previewLanguage = inferCodeLanguageFromPath(extractPathFromToolHeadline(toolNameHint, trimmedPart))
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(trimmedPart), "error:") {
			lineStyle = p.theme.Error
		}
		if isEditPreview && lineIndex > 0 {
			switch {
			case strings.HasPrefix(trimmedPart, "-"):
				lineStyle = p.theme.Error
			case strings.HasPrefix(trimmedPart, "+"):
				lineStyle = p.theme.Success
			}
		}
		styled := p.styleToolPreviewLine(part, toolNameHint, previewLanguage, lineStyle, isEditPreview)
		if lineIndex == 0 {
			if markdownLine, ok := p.styleMarkdownToolPreviewLine(part, lineStyle); ok {
				styled = markdownLine
			} else {
				styled = p.styleToolSummaryLine(part, toolNameHint, lineStyle)
			}
		}
		for _, line := range wrapRenderLineWithCustomPrefixes(linePrefix, "", styled, width) {
			out = append(out, line)
		}
		lineIndex++
	}
	return out
}

func (p *ChatPage) renderReasoningMessageLines(message chatMessageItem, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	label, style := p.reasoningTimelineLabel(message, width)
	if label == "" {
		return nil
	}
	if !p.showThinkingTags {
		return []chatRenderLine{{Text: label, Style: style}}
	}
	blocks := thinkingSummaryBlocks(message.Text)
	if len(blocks) == 0 {
		return []chatRenderLine{{Text: label, Style: style}}
	}
	return renderThinkingBlockLines(label, blocks, width, style)
}

func (p *ChatPage) reasoningTimelineLabel(message chatMessageItem, width int) (string, tcell.Style) {
	if width <= 0 {
		return "", tcell.StyleDefault
	}
	state := strings.ToLower(strings.TrimSpace(message.ToolState))
	if state == "" {
		state = "done"
	}

	symbol := p.toolSuccessSymbol
	style := p.theme.Accent
	switch state {
	case "running":
		symbol = p.toolRunningSymbol
		style = p.theme.Accent
	case "error":
		symbol = p.toolErrorSymbol
		style = p.theme.Error
	}

	headline := fmt.Sprintf("%s Thinking", symbol)
	switch {
	case reasoningTimelineDurationMS(message) > 0:
		headline += "  ·  " + formatDurationCompact(time.Duration(reasoningTimelineDurationMS(message))*time.Millisecond)
	case state == "running" && reasoningTimelineMessageRunID(message) == p.runID && p.liveRunVisible():
		headline += "  ·  " + p.runElapsedLabel()
	}
	return clampEllipsis(headline, maxInt(12, width)), style
}

func (p *ChatPage) renderLiveToolEntryLines(entry chatToolStreamEntry, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	state := strings.ToLower(strings.TrimSpace(entry.State))
	if state == "" {
		state = "pending"
	}
	symbol := p.toolRunningSymbol
	headlineStyle := p.thinkingPulseStyle()
	previewStyle := p.theme.TextMuted
	switch state {
	case "pending":
		headlineStyle = p.theme.TextMuted
	case "running":
		headlineStyle = p.thinkingPulseStyle()
	case "error":
		symbol = p.toolErrorSymbol
		headlineStyle = p.theme.Error
		previewStyle = p.theme.Error
	case "done":
		symbol = p.toolSuccessSymbol
		headlineStyle = p.theme.Accent
		previewStyle = p.theme.TextMuted
	}

	headline := toolHeadline(entry, maxInt(8, width-8))
	if duration := p.toolEntryDurationLabel(entry); duration != "" {
		headline = headline + "  ·  " + duration
	}
	out := make([]chatRenderLine, 0, 4)
	headlineLine := p.styleToolSummaryLine(headline, entry.ToolName, headlineStyle)
	for _, line := range wrapRenderLineWithCustomPrefixes(symbol+" ", "", headlineLine, width) {
		out = append(out, line)
	}
	isEdit := isEditToolEntry(entry)
	previewLanguage := toolEntryPreviewLanguage(entry)
	for _, preview := range toolPreviewLines(entry, maxInt(8, width-4), toolPreviewLineLimit(entry)) {
		if strings.EqualFold(strings.TrimSpace(preview), strings.TrimSpace(headline)) {
			continue
		}
		lineStyle := previewStyle
		if isEdit {
			trimmed := strings.TrimSpace(preview)
			switch {
			case strings.HasPrefix(trimmed, "-"):
				lineStyle = p.theme.Error
			case strings.HasPrefix(trimmed, "+"):
				lineStyle = p.theme.Success
			}
		}
		previewLine := p.styleToolPreviewLine(preview, entry.ToolName, previewLanguage, lineStyle, isEdit)
		for _, line := range wrapRenderLineWithCustomPrefixes("  ", "", previewLine, width) {
			out = append(out, line)
		}
	}
	if errLine := strings.TrimSpace(entry.Error); errLine != "" {
		for _, line := range wrapWithPrefix("  error: ", errLine, width) {
			out = append(out, chatRenderLine{Text: line, Style: p.theme.Error})
		}
	}
	return out
}

func isEditToolEntry(entry chatToolStreamEntry) bool {
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "edit") {
		return true
	}
	return isEditPayload(entry, parseToolJSON(strings.TrimSpace(entry.Output)))
}

func (p *ChatPage) liveToolEntries(limit int) []chatToolStreamEntry {
	runStarted := p.effectiveRunStarted()
	if limit <= 0 || len(p.toolStream) == 0 || runStarted.IsZero() {
		return nil
	}
	runStart := runStarted.UnixMilli()
	running := make([]chatToolStreamEntry, 0, limit)

	for i := len(p.toolStream) - 1; i >= 0; i-- {
		entry := p.toolStream[i]
		if entry.CreatedAt > 0 && entry.CreatedAt+5000 < runStart {
			break
		}
		if strings.EqualFold(strings.TrimSpace(entry.State), "running") {
			running = append(running, entry)
			if len(running) >= limit {
				break
			}
		}
	}
	if len(running) == 0 {
		return nil
	}
	for i, j := 0, len(running)-1; i < j; i, j = i+1, j-1 {
		running[i], running[j] = running[j], running[i]
	}
	return running
}

func shouldSuppressLiveToolEntry(entry chatToolStreamEntry) bool {
	if isManagedTaskToolEntry(entry) {
		return true
	}
	return isLiveBashToolEntry(entry) && strings.TrimSpace(entry.Output) != ""
}

func isManagedTaskToolEntry(entry chatToolStreamEntry) bool {
	if !isTaskToolStreamEntry(entry) {
		return false
	}
	state := normalizedToolState(entry)
	return state == "pending" || state == "running" || isTerminalToolState(state)
}

func isTaskToolStreamEntry(entry chatToolStreamEntry) bool {
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "task") {
		return true
	}
	for _, candidate := range []string{entry.Output, entry.Raw, entry.StartedArguments} {
		payload := parseToolJSON(strings.TrimSpace(candidate))
		if isTaskToolPayload(payload) {
			return true
		}
	}
	return false
}

func wrapWithPrefix(prefix, body string, width int) []string {
	return wrapWithCustomPrefixes(prefix, "", body, width)
}

func renderThinkingBlockLines(label string, blocks []thinkingDisplayBlock, width int, style tcell.Style) []chatRenderLine {
	if width <= 0 || len(blocks) == 0 {
		return nil
	}

	out := make([]chatRenderLine, 0, len(blocks)*3+1)
	label = strings.TrimSpace(label)
	if label != "" {
		labelLine := chatRenderLine{Text: label, Style: style}
		out = append(out, wrapRenderLineWithCustomPrefixes("", "", labelLine, width)...)
		out = append(out, chatRenderLine{Text: "", Style: style})
	}
	for i, block := range blocks {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		if i > 0 && len(out) > 0 {
			out = append(out, chatRenderLine{Text: "", Style: style})
		}
		line := chatRenderLine{Text: text, Style: style}
		if block.Bold {
			headingStyle := style.Bold(true).Italic(false)
			line = chatRenderLine{
				Text:  text,
				Style: headingStyle,
				Spans: []chatRenderSpan{{Text: text, Style: headingStyle}},
			}
		}
		for _, wrapped := range wrapRenderLineWithCustomPrefixes("  ", "  ", line, width) {
			out = append(out, wrapped)
		}
	}
	return out
}

func wrapSingleLine(prefix, body string, width int) []string {
	return wrapSingleLineWithCustomPrefixes(prefix, strings.Repeat(" ", len([]rune(prefix))), body, width)
}

func (p *ChatPage) renderTaskToolTableLines(message chatMessageItem, payload map[string]any, width int) []chatRenderLine {
	rows := taskToolTableRows(payload, toolTimelineMessageStartedAt(message), message.ToolState)
	if len(rows) == 0 {
		return nil
	}
	if width < 30 {
		width = 30
	}
	agentWidth := 8
	statusWidth := 2
	toolWidth := 8
	timeWidth := 5
	for _, row := range rows {
		agentWidth = maxInt(agentWidth, minInt(18, utf8.RuneCountInString(strings.TrimSpace(row.Agent))))
		toolWidth = maxInt(toolWidth, minInt(18, utf8.RuneCountInString(strings.TrimSpace(row.Tool))))
		timeWidth = maxInt(timeWidth, minInt(8, utf8.RuneCountInString(strings.TrimSpace(row.Time))))
	}
	fixed := agentWidth + statusWidth + toolWidth + timeWidth + 9
	if fixed >= width {
		over := fixed - width + 1
		for over > 0 && agentWidth > 6 {
			agentWidth--
			over--
		}
		for over > 0 && toolWidth > 6 {
			toolWidth--
			over--
		}
		for over > 0 && timeWidth > 4 {
			timeWidth--
			over--
		}
	}
	headerSpans := []chatRenderSpan{
		{Text: p.toolTableHeaderCell("Agent", agentWidth), Style: p.theme.Secondary.Bold(true)},
		{Text: " │ ", Style: p.theme.Border},
		{Text: p.toolTableHeaderCell("St", statusWidth), Style: p.theme.Secondary.Bold(true)},
		{Text: " │ ", Style: p.theme.Border},
		{Text: p.toolTableHeaderCell("Tool", toolWidth), Style: p.theme.Secondary.Bold(true)},
		{Text: " │ ", Style: p.theme.Border},
		{Text: p.toolTableHeaderCell("Time", timeWidth), Style: p.theme.Secondary.Bold(true)},
	}
	out := []chatRenderLine{{Text: chatRenderSpansText(headerSpans), Style: p.theme.Secondary, Spans: headerSpans}}
	out = append(out, chatRenderLine{Text: strings.Repeat("─", minInt(width, agentWidth+statusWidth+toolWidth+timeWidth+9)), Style: p.theme.Border})
	for _, row := range rows {
		statusStyle := p.toolTableStatusStyle(row.Status)
		spans := []chatRenderSpan{
			{Text: fitLeft(emptyValue(row.Agent, "-"), agentWidth), Style: p.theme.Text},
			{Text: " │ ", Style: p.theme.Border},
			{Text: fitLeft(taskToolStatusLabel(row.Status), statusWidth), Style: statusStyle},
			{Text: " │ ", Style: p.theme.Border},
			{Text: fitLeft(emptyValue(row.Tool, "-"), toolWidth), Style: p.theme.Text},
			{Text: " │ ", Style: p.theme.Border},
			{Text: fitRight(emptyValue(row.Time, "-"), timeWidth), Style: p.theme.TextMuted},
		}
		out = append(out, chatRenderLine{Text: chatRenderSpansText(spans), Style: p.theme.TextMuted, Spans: spans})
		if preview := strings.TrimSpace(row.PreviewText); preview != "" {
			previewLabel := strings.TrimSpace(row.PreviewKind)
			if previewLabel == "" {
				previewLabel = "live"
			}
			labelStyle := p.theme.Secondary
			switch strings.ToLower(previewLabel) {
			case "assistant":
				labelStyle = p.theme.Accent
			case "thinking":
				labelStyle = p.theme.Warning
			case "tool":
				labelStyle = p.theme.TextMuted
			}
			prefix := fmt.Sprintf("  %s: ", previewLabel)
			for _, line := range wrapRenderLineWithCustomPrefixes(
				prefix,
				strings.Repeat(" ", len([]rune(prefix))),
				chatRenderLine{Style: p.theme.TextMuted, Spans: []chatRenderSpan{{Text: preview, Style: p.theme.TextMuted}}},
				maxInt(24, width),
			) {
				if len(line.Spans) > 0 {
					line.Spans[0].Style = labelStyle
					line.Text = chatRenderSpansText(line.Spans)
				}
				out = append(out, line)
			}
		}
	}
	return out
}

func (p *ChatPage) toolTableHeaderCell(label string, width int) string {
	return fitLeft(label, width)
}

func (p *ChatPage) toolTableStatusStyle(status string) tcell.Style {
	switch normalizeTaskToolStatus(status) {
	case "done":
		return p.theme.Success
	case "error":
		return p.theme.Error
	case "running":
		return p.thinkingPulseStyle()
	default:
		return p.theme.TextMuted
	}
}

type searchToolRenderRow struct {
	Query  string
	Path   string
	Line   string
	Result string
}

func (p *ChatPage) renderSearchToolTableLines(message chatMessageItem, payload map[string]any, width int) []chatRenderLine {
	if !isSearchPayload(chatToolStreamEntry{ToolName: toolTimelineMessageToolName(message)}, payload) {
		return nil
	}
	if width < 36 {
		width = 36
	}
	rows, grouped := searchToolRenderRows(payload)
	if len(rows) == 0 {
		if summary := summarizeSearchToolPayload(payload, maxInt(24, width)); summary != "" {
			return []chatRenderLine{{Text: summary, Style: p.theme.Accent}}
		}
		return nil
	}
	if width < 52 {
		return p.renderSearchToolStackedLines(payload, rows, grouped, width)
	}
	return p.renderSearchToolAlignedLines(payload, rows, grouped, width)
}

func searchToolHasInfoColumn(rows []searchToolRenderRow) bool {
	for _, row := range rows {
		if strings.TrimSpace(row.Result) != "" {
			return true
		}
	}
	return false
}

func (p *ChatPage) renderSearchToolAlignedLines(payload map[string]any, rows []searchToolRenderRow, grouped bool, width int) []chatRenderLine {
	out := make([]chatRenderLine, 0, len(rows)+4)
	if summary := summarizeSearchToolPayload(payload, maxInt(24, width)); summary != "" {
		out = append(out, chatRenderLine{Text: summary, Style: p.theme.Accent})
	}

	showQuery := grouped
	showInfo := searchToolHasInfoColumn(rows)
	queryWidth := 0
	if showQuery {
		queryWidth = 8
		for _, row := range rows {
			queryWidth = maxInt(queryWidth, minInt(28, utf8.RuneCountInString(strings.TrimSpace(row.Query))))
		}
	}
	pathWidth := 16
	for _, row := range rows {
		pathWidth = maxInt(pathWidth, minInt(40, utf8.RuneCountInString(strings.TrimSpace(row.Path))))
	}
	lineWidth := 2
	for _, row := range rows {
		lineWidth = maxInt(lineWidth, minInt(28, utf8.RuneCountInString(strings.TrimSpace(row.Line))))
	}
	infoWidth := 0
	if showInfo {
		infoWidth = 6
		for _, row := range rows {
			infoWidth = maxInt(infoWidth, minInt(18, utf8.RuneCountInString(strings.TrimSpace(row.Result))))
		}
	}

	fixed := pathWidth + lineWidth + 3
	if showQuery {
		fixed += queryWidth + 3
	}
	if showInfo {
		fixed += infoWidth + 3
	}
	for fixed > width {
		shrank := false
		if showInfo && infoWidth > 6 {
			infoWidth--
			fixed--
			shrank = true
		}
		if fixed <= width {
			break
		}
		if pathWidth > 12 {
			pathWidth--
			fixed--
			shrank = true
		}
		if fixed <= width {
			break
		}
		if showQuery && queryWidth > 6 {
			queryWidth--
			fixed--
			shrank = true
		}
		if fixed <= width {
			break
		}
		if lineWidth > 4 {
			lineWidth--
			fixed--
			shrank = true
		}
		if !shrank {
			break
		}
	}
	if fixed > width {
		return p.renderSearchToolStackedLines(payload, rows, grouped, width)
	}

	headerSpans := make([]chatRenderSpan, 0, 10)
	if showQuery {
		headerSpans = append(headerSpans,
			chatRenderSpan{Text: fitLeft("Query", queryWidth), Style: p.theme.Secondary.Bold(true)},
			chatRenderSpan{Text: " │ ", Style: p.theme.Border},
		)
	}
	headerSpans = append(headerSpans,
		chatRenderSpan{Text: fitLeft("Path", pathWidth), Style: p.theme.Secondary.Bold(true)},
		chatRenderSpan{Text: " │ ", Style: p.theme.Border},
		chatRenderSpan{Text: fitRight("Ln", lineWidth), Style: p.theme.Secondary.Bold(true)},
	)
	if showInfo {
		headerSpans = append(headerSpans,
			chatRenderSpan{Text: " │ ", Style: p.theme.Border},
			chatRenderSpan{Text: fitLeft("Info", infoWidth), Style: p.theme.Secondary.Bold(true)},
		)
	}
	headerText := chatRenderSpansText(headerSpans)
	out = append(out, chatRenderLine{Text: headerText, Style: p.theme.Secondary, Spans: headerSpans})
	out = append(out, chatRenderLine{Text: strings.Repeat("─", minInt(width, utf8.RuneCountInString(headerText))), Style: p.theme.Border})

	for _, row := range rows {
		rowSpans := make([]chatRenderSpan, 0, 10)
		if showQuery {
			rowSpans = append(rowSpans,
				chatRenderSpan{Text: fitLeft(emptyValue(row.Query, "-"), queryWidth), Style: p.theme.TextMuted},
				chatRenderSpan{Text: " │ ", Style: p.theme.Border},
			)
		}
		rowSpans = append(rowSpans,
			chatRenderSpan{Text: fitLeft(emptyValue(row.Path, "-"), pathWidth), Style: p.theme.Text},
			chatRenderSpan{Text: " │ ", Style: p.theme.Border},
			chatRenderSpan{Text: fitRight(emptyValue(row.Line, "-"), lineWidth), Style: p.theme.TextMuted},
		)
		if showInfo {
			rowSpans = append(rowSpans,
				chatRenderSpan{Text: " │ ", Style: p.theme.Border},
				chatRenderSpan{Text: fitLeft(emptyValue(row.Result, "-"), infoWidth), Style: p.theme.TextMuted},
			)
		}
		out = append(out, chatRenderLine{Text: chatRenderSpansText(rowSpans), Style: p.theme.Text, Spans: rowSpans})
	}
	return out
}

func (p *ChatPage) renderSearchToolStackedLines(payload map[string]any, rows []searchToolRenderRow, grouped bool, width int) []chatRenderLine {
	out := make([]chatRenderLine, 0, len(rows)*3+8)
	if summary := summarizeSearchToolPayload(payload, maxInt(24, width)); summary != "" {
		out = append(out, chatRenderLine{Text: summary, Style: p.theme.Accent})
	}
	lastQuery := ""
	for _, row := range rows {
		if grouped && row.Query != "" && !strings.EqualFold(row.Query, lastQuery) {
			lastQuery = row.Query
			out = append(out, chatRenderLine{Text: clampEllipsis("query: "+row.Query, width), Style: p.theme.Secondary.Bold(true)})
		}
		location := row.Path
		if row.Line != "" {
			location += ":" + row.Line
		}
		if location == "" && row.Query != "" {
			location = row.Query
		}
		if location != "" {
			out = append(out, chatRenderLine{Text: clampEllipsis(location, width), Style: p.theme.Text})
		}
		if info := strings.TrimSpace(row.Result); info != "" {
			for _, line := range wrapWithCustomPrefixes("  info: ", "        ", info, width) {
				out = append(out, chatRenderLine{Text: line, Style: p.theme.TextMuted})
			}
		}
	}
	return out
}

func searchToolRenderRows(payload map[string]any) ([]searchToolRenderRow, bool) {
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	grouped := len(searchRequestedQueries(payload)) > 1 || len(searchResultQueryPayloads(payload)) > 1
	groupedResults := jsonObjectSlice(payload, "results")
	rows := make([]searchToolRenderRow, 0, maxInt(len(groupedResults), maxInt(len(jsonObjectSlice(payload, "matches")), len(jsonObjectSlice(payload, "files")))))
	if len(groupedResults) > 0 {
		for _, group := range groupedResults {
			path := strings.TrimSpace(jsonString(group, "path"))
			if path == "" {
				continue
			}
			items := jsonObjectSlice(group, "items")
			if mode == "files" {
				queryParts := make([]string, 0, minInt(3, len(items)))
				infoParts := make([]string, 0, minInt(3, len(items)))
				for i, item := range items {
					if i >= 3 {
						break
					}
					if grouped {
						queryParts = append(queryParts, emptyValue(strings.TrimSpace(jsonString(item, "query")), "-"))
					}
					if score := jsonInt(item, "score"); score > 0 {
						infoParts = append(infoParts, fmt.Sprintf("score %d", score))
					}
				}
				row := searchToolRenderRow{Path: path}
				if grouped && len(queryParts) > 0 {
					row.Query = strings.Join(queryParts, " | ")
				}
				if len(infoParts) > 0 {
					row.Result = strings.Join(infoParts, " | ")
				}
				rows = append(rows, row)
				continue
			}
			lineParts := make([]string, 0, minInt(4, len(items)))
			queryParts := make([]string, 0, minInt(4, len(items)))
			for i, item := range items {
				if i >= 4 {
					break
				}
				if grouped {
					queryParts = append(queryParts, emptyValue(strings.TrimSpace(jsonString(item, "query")), "-"))
				}
				if line := jsonInt(item, "line"); line > 0 {
					lineParts = append(lineParts, strconv.Itoa(line))
				}
			}
			row := searchToolRenderRow{Path: path}
			if grouped && len(queryParts) > 0 {
				row.Query = strings.Join(queryParts, " | ")
			}
			if len(lineParts) > 0 {
				row.Line = strings.Join(lineParts, ", ")
			}
			rows = append(rows, row)
		}
		return rows, grouped
	}
	if mode == "files" {
		for _, item := range jsonObjectSlice(payload, "files") {
			query := strings.TrimSpace(jsonString(item, "query"))
			path := firstNonEmptyToolValue(strings.TrimSpace(jsonString(item, "relative_path")), strings.TrimSpace(jsonString(item, "path")))
			if path == "" {
				continue
			}
			info := ""
			if count := jsonInt(item, "count"); count > 1 {
				info = toolCountLabel(count, "hit", "hits")
			} else if score := jsonInt(item, "score"); score > 0 {
				info = fmt.Sprintf("score %d", score)
			}
			rows = append(rows, searchToolRenderRow{Query: query, Path: path, Result: info})
		}
		return rows, grouped
	}

	type searchToolFileQueryGroup struct {
		label string
		lines []int
		seen  map[int]struct{}
	}
	type searchToolFileGroup struct {
		path       string
		queryOrder []string
		queries    map[string]*searchToolFileQueryGroup
	}

	fileOrder := make([]string, 0, len(jsonObjectSlice(payload, "matches")))
	fileGroups := make(map[string]*searchToolFileGroup)
	pathlessRows := make([]searchToolRenderRow, 0)
	for _, item := range jsonObjectSlice(payload, "matches") {
		query := strings.TrimSpace(jsonString(item, "query"))
		path := firstNonEmptyToolValue(strings.TrimSpace(jsonString(item, "relative_path")), strings.TrimSpace(jsonString(item, "path")))
		lineNumber := jsonInt(item, "line")
		line := ""
		if lineNumber > 0 {
			line = strconv.Itoa(lineNumber)
		}
		if path == "" && line == "" {
			continue
		}
		if path == "" {
			pathlessRows = append(pathlessRows, searchToolRenderRow{Query: query, Line: line})
			continue
		}
		group, ok := fileGroups[path]
		if !ok {
			group = &searchToolFileGroup{path: path, queries: make(map[string]*searchToolFileQueryGroup)}
			fileGroups[path] = group
			fileOrder = append(fileOrder, path)
		}
		queryKey := strings.ToLower(query)
		queryGroup, ok := group.queries[queryKey]
		if !ok {
			queryGroup = &searchToolFileQueryGroup{label: query, seen: make(map[int]struct{})}
			group.queries[queryKey] = queryGroup
			group.queryOrder = append(group.queryOrder, queryKey)
		}
		if queryGroup.label == "" && query != "" {
			queryGroup.label = query
		}
		if lineNumber > 0 {
			if _, seen := queryGroup.seen[lineNumber]; !seen {
				queryGroup.seen[lineNumber] = struct{}{}
				queryGroup.lines = append(queryGroup.lines, lineNumber)
			}
		}
	}

	for _, path := range fileOrder {
		group := fileGroups[path]
		if group == nil {
			continue
		}
		row := searchToolRenderRow{Path: group.path}
		displayQueries := group.queryOrder
		if len(displayQueries) > 3 {
			displayQueries = displayQueries[:3]
		}
		queryParts := make([]string, 0, len(displayQueries)+1)
		lineParts := make([]string, 0, len(displayQueries)+1)
		for _, queryKey := range displayQueries {
			queryGroup := group.queries[queryKey]
			if queryGroup == nil {
				continue
			}
			if grouped {
				queryParts = append(queryParts, emptyValue(strings.TrimSpace(queryGroup.label), "-"))
			}
			lineParts = append(lineParts, searchToolCompactLineList(queryGroup.lines, 4))
		}
		if remaining := len(group.queryOrder) - len(displayQueries); remaining > 0 {
			if grouped {
				queryParts = append(queryParts, fmt.Sprintf("+%d more", remaining))
			}
			lineParts = append(lineParts, fmt.Sprintf("+%d queries", remaining))
		}
		if grouped && len(queryParts) > 0 {
			row.Query = strings.Join(queryParts, " | ")
		}
		if len(lineParts) > 0 {
			separator := ", "
			if grouped {
				separator = " | "
			}
			row.Line = strings.Join(lineParts, separator)
		}
		rows = append(rows, row)
	}
	rows = append(rows, pathlessRows...)
	return rows, grouped
}

func searchToolCompactLineList(lines []int, maxParts int) string {
	if len(lines) == 0 {
		return "-"
	}
	if maxParts <= 0 {
		maxParts = 1
	}
	parts := make([]string, 0, minInt(len(lines), maxParts)+1)
	for _, line := range lines {
		if len(parts) >= maxParts {
			break
		}
		parts = append(parts, strconv.Itoa(line))
	}
	if remaining := len(lines) - len(parts); remaining > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", remaining))
	}
	return strings.Join(parts, ", ")
}
