package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	chatToolPreviewMaxLines     = 2
	chatToolPreviewMaxRunes     = 180
	chatBashLivePreviewMaxLines = 5
)

func (p *ChatPage) drawToolStreamComponent(s tcell.Screen, rect Rect) {
	if rect.W < 12 || rect.H < 4 {
		return
	}

	DrawBox(s, rect, p.theme.Border)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Secondary, "toolstream")

	contentW := rect.W - 4
	contentH := rect.H - 2
	if contentW <= 0 || contentH <= 0 {
		return
	}

	lines := p.buildToolStreamLines(contentW)
	if len(lines) == 0 {
		lines = []chatRenderLine{{Text: "No tool calls yet.", Style: p.theme.TextMuted}}
	}
	if len(lines) > contentH {
		lines = lines[len(lines)-contentH:]
	}

	y := rect.Y + 1
	for _, line := range lines {
		if y >= rect.Y+rect.H-1 {
			break
		}
		DrawTimelineLine(s, rect.X+2, y, contentW, line)
		y++
	}
}

func (p *ChatPage) buildToolStreamLines(width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	lines := make([]chatRenderLine, 0, 24)

	if p.effectiveRunActive() {
		running := fmt.Sprintf("%s running turn (%s)", p.pulseFrame(), p.runElapsedLabel())
		for _, wrapped := range wrapWithPrefix("", running, width) {
			lines = append(lines, chatRenderLine{Text: wrapped, Style: p.theme.Primary})
		}
		prompt := clampEllipsis(p.runPrompt, maxInt(8, width-2))
		for _, wrapped := range wrapWithPrefix("  ", prompt, width) {
			lines = append(lines, chatRenderLine{Text: wrapped, Style: p.theme.TextMuted})
		}
	}

	start := maxInt(0, len(p.toolStream)-8)
	for i := start; i < len(p.toolStream); i++ {
		entry := p.toolStream[i]
		state := normalizedToolState(entry)
		symbol := p.toolRunningSymbol
		style := p.theme.TextMuted
		switch state {
		case "pending":
			style = p.theme.TextMuted
		case "running":
			style = p.theme.TextMuted
		case "error":
			symbol = p.toolErrorSymbol
			style = p.theme.Error
		default:
			symbol = p.toolSuccessSymbol
			style = p.theme.Accent
		}

		header := toolHeadline(entry, maxInt(8, width-8))
		if duration := p.toolEntryDurationLabel(entry); duration != "" {
			header += "  ·  " + duration
		}
		headerLine := p.styleToolSummaryLine(header, entry.ToolName, style)
		for _, wrapped := range wrapRenderLineWithCustomPrefixes(symbol+" ", "", headerLine, width) {
			lines = append(lines, wrapped)
		}

		isEdit := isEditToolEntry(entry)
		previewLanguage := toolEntryPreviewLanguage(entry)
		previewLines := toolPreviewLines(entry, maxInt(8, width-2), toolPreviewLineLimit(entry))
		for _, preview := range previewLines {
			if strings.EqualFold(strings.TrimSpace(preview), strings.TrimSpace(header)) {
				continue
			}
			previewStyle := p.theme.TextMuted
			if isEdit {
				trimmed := strings.TrimSpace(preview)
				switch {
				case strings.HasPrefix(trimmed, "-"):
					previewStyle = p.theme.Error
				case strings.HasPrefix(trimmed, "+"):
					previewStyle = p.theme.Success
				}
			}
			previewLine := p.styleToolPreviewLine(preview, entry.ToolName, previewLanguage, previewStyle, isEdit)
			for _, wrapped := range wrapRenderLineWithCustomPrefixes("  ", "", previewLine, width) {
				lines = append(lines, wrapped)
			}
		}

		if strings.TrimSpace(entry.Error) != "" {
			for _, wrapped := range wrapWithPrefix("  error: ", entry.Error, width) {
				lines = append(lines, chatRenderLine{Text: wrapped, Style: p.theme.Error})
			}
		}
	}

	return lines
}

func parseToolStreamEntry(content string, createdAt int64) chatToolStreamEntry {
	raw := strings.TrimSpace(content)
	entry := chatToolStreamEntry{
		ToolName:  "tool",
		Output:    raw,
		Raw:       raw,
		CreatedAt: createdAt,
		State:     "done",
	}
	if raw == "" {
		return entry
	}
	if historyEntry, ok := parseToolHistoryStreamEntry(raw, createdAt); ok {
		return historyEntry
	}

	toolName := parseDelimitedField(raw, "tool", "call_id", "state", "status", "duration_ms", "duration", "error", "output")
	callID := parseDelimitedField(raw, "call_id", "state", "status", "duration_ms", "duration", "error", "output")
	state := parseDelimitedField(raw, "state", "status", "duration_ms", "duration", "error", "output")
	status := parseDelimitedField(raw, "status", "duration_ms", "duration", "error", "output")
	errText := parseDelimitedField(raw, "error", "output")
	output := parseOutputField(raw)
	duration := parseDurationMilliseconds(
		parseDelimitedField(raw, "duration_ms", "duration", "error", "output"),
		parseDelimitedField(raw, "duration", "error", "output"),
	)

	if strings.TrimSpace(toolName) != "" {
		entry.ToolName = strings.TrimSpace(toolName)
	}
	if strings.TrimSpace(callID) != "" {
		entry.CallID = strings.TrimSpace(callID)
	}
	if strings.TrimSpace(errText) != "" {
		entry.Error = strings.TrimSpace(errText)
		entry.State = "error"
	}
	if strings.TrimSpace(state) != "" {
		entry.State = strings.ToLower(strings.TrimSpace(state))
	}
	if strings.TrimSpace(status) != "" && strings.TrimSpace(state) == "" {
		entry.State = strings.ToLower(strings.TrimSpace(status))
	}
	if strings.TrimSpace(output) != "" {
		entry.Output = strings.TrimSpace(output)
	}
	entry.DurationMS = duration
	entry.State = normalizedToolState(entry)
	return entry
}

func parseToolHistoryStreamEntry(raw string, createdAt int64) (chatToolStreamEntry, bool) {
	payload := parseToolJSON(raw)
	if payload == nil || !strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "run.tool-history.v2") {
		return chatToolStreamEntry{}, false
	}

	toolName := strings.TrimSpace(jsonString(payload, "tool"))
	if toolName == "" {
		toolName = "tool"
	}
	output := strings.TrimSpace(jsonString(payload, "completed_output"))
	rawOutput := strings.TrimSpace(jsonString(payload, "output"))
	if output == "" {
		output = rawOutput
	}
	if rawOutput == "" {
		rawOutput = output
	}
	entry := chatToolStreamEntry{
		ToolName:   toolName,
		CallID:     strings.TrimSpace(jsonString(payload, "call_id")),
		Output:     output,
		Raw:        rawOutput,
		Error:      strings.TrimSpace(jsonString(payload, "error")),
		CreatedAt:  createdAt,
		State:      "done",
		DurationMS: int64(jsonInt(payload, "duration_ms")),
	}
	if args := strings.TrimSpace(jsonString(payload, "arguments")); args != "" {
		entry.StartedArguments = args
		entry.StartedArgsAreJSON = parseToolJSON(args) != nil
	}
	if metadata := jsonObject(payload, "metadata"); len(metadata) > 0 {
		if entry.StartedArguments == "" {
			if encoded, err := json.Marshal(metadata); err == nil {
				entry.StartedArguments = string(encoded)
				entry.StartedArgsAreJSON = true
			}
		}
	}
	if entry.Error != "" {
		entry.State = "error"
	}
	entry.State = normalizedToolState(entry)
	return entry, true
}

func parseDelimitedField(raw string, key string, nextKeys ...string) string {
	if raw == "" || key == "" {
		return ""
	}
	marker := key + "="
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := len(raw)
	for _, next := range nextKeys {
		nextMarker := " " + next + "="
		if idx := strings.Index(raw[start:], nextMarker); idx >= 0 {
			pos := start + idx
			if pos < end {
				end = pos
			}
		}
	}
	if start >= end {
		return ""
	}
	return strings.TrimSpace(raw[start:end])
}

func parseOutputField(raw string) string {
	if raw == "" {
		return ""
	}
	marker := "output="
	idx := strings.Index(raw, marker)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(raw[idx+len(marker):])
}

func formatUnifiedToolEntry(entry chatToolStreamEntry) string {
	if askUser := formatAskUserToolEntry(entry, chatToolPreviewMaxRunes); askUser != "" {
		return askUser
	}
	if edit := formatEditToolEntry(entry, 0); edit != "" {
		return edit
	}
	if search := formatSearchToolEntry(entry, maxInt(chatToolPreviewMaxRunes, 640)); search != "" {
		return search
	}
	if websearch := formatWebSearchToolEntry(entry, maxInt(chatToolPreviewMaxRunes, 640)); websearch != "" {
		return websearch
	}
	if list := formatListToolEntry(entry, maxInt(chatToolPreviewMaxRunes, 640)); list != "" {
		return list
	}
	if webfetch := formatWebFetchToolEntry(entry, maxInt(chatToolPreviewMaxRunes, 640)); webfetch != "" {
		return webfetch
	}
	if manageTodos := formatManageTodosToolEntry(entry, maxInt(chatToolPreviewMaxRunes, 640)); manageTodos != "" {
		return manageTodos
	}
	headline := toolMessageHeadline(entry, chatToolPreviewMaxRunes)
	if headline == "" {
		headline = "tool"
	}
	if errText := strings.TrimSpace(entry.Error); errText != "" {
		return clampEllipsis(headline+"  ·  error: "+errText, chatToolPreviewMaxRunes)
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "task") {
		return clampEllipsis(headline, maxInt(chatToolPreviewMaxRunes, 320))
	}
	maxPreviewRunes := maxInt(chatToolPreviewMaxRunes, 320)
	maxUnifiedRunes := maxPreviewRunes
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "bash") {
		maxPreviewRunes = maxInt(maxPreviewRunes, 320)
		maxUnifiedRunes = maxInt(maxUnifiedRunes, 960)
	}
	lines := []string{headline}
	for _, preview := range toolPreviewLines(entry, maxPreviewRunes, toolPreviewLineLimit(entry)) {
		trimmedPreview := strings.TrimSpace(preview)
		if strings.EqualFold(trimmedPreview, strings.TrimSpace(headline)) {
			continue
		}
		if isToolNoiseLine(trimmedPreview, entry.ToolName) {
			continue
		}
		lines = append(lines, preview)
	}
	joined := strings.Join(lines, "\n")
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "bash") && strings.Contains(joined, "/output") {
		return joined
	}
	return clampEllipsis(joined, maxUnifiedRunes)
}

func formatEditToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil || !isEditPayload(entry, payload) {
		return ""
	}

	path := strings.TrimSpace(jsonString(payload, "path"))
	replacements := jsonInt(payload, "replacements")
	replaceAll := jsonBool(payload, "replace_all")
	items := editPayloadPreviewItems(payload)
	editCount := jsonInt(payload, "edit_count")
	if editCount <= 0 {
		if len(items) > 0 {
			editCount = len(items)
		} else if replacements > 0 {
			editCount = 1
		}
	}

	headline := "edit"
	if path != "" {
		headline += " " + clampEllipsis(path, 180)
	}
	notes := make([]string, 0, 3)
	if editCount > 1 {
		notes = append(notes, toolCountLabel(editCount, "edit", "edits"))
	}
	if replacements > 0 {
		notes = append(notes, toolCountLabel(replacements, "replacement", "replacements"))
	}
	if replaceAll {
		if editCount > 1 {
			notes = append(notes, "contains replace-all")
		} else {
			notes = append(notes, "replace all")
		}
	}
	headline = toolSummaryWithNotes(headline, notes...)

	if len(items) == 0 {
		return clampEllipsis(headline, maxRunes)
	}

	lines := []string{headline}
	for _, item := range items {
		oldPreview := strings.TrimSpace(jsonString(item, "old_string_preview"))
		newPreview := strings.TrimSpace(jsonString(item, "new_string_preview"))
		oldTruncated := jsonBool(item, "old_string_truncated")
		newTruncated := jsonBool(item, "new_string_truncated")
		if oldPreview == "" {
			oldPreview = "(empty)"
		}
		if newPreview == "" {
			newPreview = "(empty)"
		}
		for _, line := range expandEditPreviewLines(oldPreview, oldTruncated) {
			lines = append(lines, "-"+line)
		}
		for _, line := range expandEditPreviewLines(newPreview, newTruncated) {
			lines = append(lines, "+"+line)
		}
	}
	joined := strings.Join(lines, "\n")
	if maxRunes <= 0 {
		return joined
	}
	return clampEllipsis(joined, maxRunes)
}

func formatWebSearchToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	preferred := preferredStructuredToolText("websearch", strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw))
	payload := parseToolJSON(preferred)
	if payload == nil && preferred != strings.TrimSpace(entry.Raw) {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isWebSearchPayload(entry, payload) {
		return ""
	}

	headline := summarizeWebSearchToolPayload(payload, maxRunes)
	if headline == "" {
		headline = "websearch"
	}

	lines := []string{headline}
	for _, line := range structuredWebSearchTimelineLines(payload, maxInt(maxRunes, 160), 5) {
		if strings.EqualFold(strings.TrimSpace(line), strings.TrimSpace(headline)) {
			continue
		}
		lines = append(lines, line)
	}
	if errText := strings.TrimSpace(entry.Error); errText != "" && len(lines) == 1 {
		lines = append(lines, clampEllipsis("error: "+errText, maxRunes))
	}
	return clampEllipsis(strings.Join(lines, "\n"), maxInt(maxRunes, 640))
}

func formatSearchToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	preferred := preferredStructuredToolText("search", strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw))
	payload := parseToolJSON(preferred)
	if payload == nil && preferred != strings.TrimSpace(entry.Raw) {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isSearchPayload(entry, payload) {
		return ""
	}

	headline := summarizeSearchToolPayload(payload, maxRunes)
	if headline == "" {
		headline = "search"
	}

	rows, grouped := searchToolRenderRows(payload)
	if len(rows) == 0 {
		if errText := strings.TrimSpace(entry.Error); errText != "" {
			return clampEllipsis(headline+"\nerror: "+errText, maxInt(maxRunes, 640))
		}
		return clampEllipsis(headline, maxInt(maxRunes, 640))
	}

	lines := []string{headline}
	lines = append(lines, searchToolTablePreviewLines(rows, grouped, maxInt(maxRunes, 160), 4)...)
	return clampEllipsis(strings.Join(lines, "\n"), maxInt(maxRunes, 640))
}

func formatListToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isListPayload(entry, payload) {
		return ""
	}

	headline := summarizeListToolPayload(payload, maxRunes)
	if headline == "" {
		headline = "list"
	}
	lines := []string{headline}
	for _, line := range structuredListTimelineLines(payload, maxInt(maxRunes, 160), 5) {
		if strings.EqualFold(strings.TrimSpace(line), strings.TrimSpace(headline)) {
			continue
		}
		lines = append(lines, line)
	}
	if errText := strings.TrimSpace(entry.Error); errText != "" && len(lines) == 1 {
		lines = append(lines, clampEllipsis("error: "+errText, maxRunes))
	}
	return clampEllipsis(strings.Join(lines, "\n"), maxInt(maxRunes, 640))
}

func formatWebFetchToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isWebFetchPayload(entry, payload) {
		return ""
	}

	headline := summarizeWebFetchToolPayload(payload, maxRunes)
	if headline == "" {
		headline = "webfetch"
	}
	lines := []string{headline}
	for _, line := range structuredWebFetchTimelineLines(payload, maxInt(maxRunes, 160), 5) {
		if strings.EqualFold(strings.TrimSpace(line), strings.TrimSpace(headline)) {
			continue
		}
		lines = append(lines, line)
	}
	if errText := strings.TrimSpace(entry.Error); errText != "" && len(lines) == 1 {
		lines = append(lines, clampEllipsis("error: "+errText, maxRunes))
	}
	return clampEllipsis(strings.Join(lines, "\n"), maxInt(maxRunes, 640))
}

func formatManageTodosToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isManageTodosPayload(entry, payload) {
		return ""
	}

	headline := summarizeManageTodosToolPayload(payload)
	if headline == "" {
		headline = "manage_todos"
	}
	lines := []string{headline}
	for _, line := range structuredManageTodosPreviewLines(payload, maxInt(maxRunes, 160), 6) {
		if strings.EqualFold(strings.TrimSpace(line), strings.TrimSpace(headline)) {
			continue
		}
		lines = append(lines, line)
	}
	if errText := strings.TrimSpace(entry.Error); errText != "" && len(lines) == 1 {
		lines = append(lines, clampEllipsis("error: "+errText, maxRunes))
	}
	return clampEllipsis(strings.Join(lines, "\n"), maxInt(maxRunes, 640))
}

func isListPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(entry.ToolName), "list") ||
		strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.list.v3")
}

func isWebSearchPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "websearch") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "tool")), "websearch") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.websearch.exa.v1") {
		return true
	}
	if len(webSearchResultQueryPayloads(payload)) > 0 {
		return true
	}
	return false
}

func isSearchPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "search") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "tool")), "search") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.search.v1") {
		return true
	}
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	if mode != "content" && mode != "files" {
		return false
	}
	if len(jsonObjectSlice(payload, "query_results")) > 0 {
		return true
	}
	if len(jsonObjectSlice(payload, "results")) > 0 {
		return true
	}
	if len(jsonObjectSlice(payload, "matches")) > 0 || len(jsonObjectSlice(payload, "files")) > 0 {
		return true
	}
	return jsonInt(payload, "query_count") > 0 || len(searchRequestedQueries(payload)) > 0
}

func isWebFetchPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(entry.ToolName), "webfetch") ||
		strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.webfetch.exa.v1")
}

func isManageTodosPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "manage_todos") || strings.EqualFold(strings.TrimSpace(entry.ToolName), "manage-todos") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "tool")), "manage_todos") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.manage-todos.v1") {
		return true
	}
	if action := strings.TrimSpace(jsonString(payload, "action")); action != "" {
		if len(jsonObjectSlice(payload, "items")) > 0 || len(jsonObject(payload, "item")) > 0 || len(jsonObject(payload, "summary")) > 0 || len(jsonObjectSlice(payload, "results")) > 0 {
			return true
		}
	}
	return false
}

func structuredWebSearchTimelineLines(payload map[string]any, maxRunes, maxLines int) []string {
	if maxLines <= 0 || payload == nil {
		return nil
	}
	queryResults := webSearchResultQueryPayloads(payload)
	if len(queryResults) == 0 {
		return structuredWebSearchPreviewLines(payload, maxRunes, maxLines)
	}
	if len(queryResults) == 1 {
		return structuredWebSearchPreviewLines(payload, maxRunes, maxLines)
	}

	lines := make([]string, 0, maxLines)
	shownQueries := 0
	singleQuery := webSearchPrimaryQuery(payload) != "" && len(queryResults) == 1
	for _, queryPayload := range queryResults {
		if len(lines) >= maxLines {
			break
		}
		if summary := webSearchQuerySummaryLine(queryPayload, maxRunes, singleQuery); summary != "" {
			lines = append(lines, summary)
		}
		if len(lines) >= maxLines {
			break
		}
		if hit := webSearchTopHitPreviewLine(queryPayload, maxRunes); hit != "" {
			lines = append(lines, hit)
		}
		shownQueries++
	}
	if remaining := len(queryResults) - shownQueries; remaining > 0 && len(lines) < maxLines {
		lines = append(lines, clampEllipsis(fmt.Sprintf("+%d more %s", remaining, pluralizeLabel(remaining, "query", "queries")), maxRunes))
	}
	return lines
}

func summarizeWebSearchToolPayload(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}

	queryResults := webSearchResultQueryPayloads(payload)
	queryCount := jsonInt(payload, "query_count")
	if queryCount <= 0 {
		queryCount = len(queryResults)
	}
	primaryQuery := webSearchPrimaryQuery(payload)
	failedQueries := jsonInt(payload, "failed_queries")
	if queryCount > 0 || failedQueries > 0 || len(queryResults) > 0 {
		parts := []string{"websearch"}
		if aggregate := webSearchAggregateSummaryLine(payload, maxRunes); aggregate != "" {
			parts = append(parts, aggregate)
		} else if primaryQuery != "" {
			parts = append(parts, primaryQuery)
		} else if queryCount > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", queryCount, pluralizeLabel(queryCount, "query", "queries")))
		}
		if searchType := webSearchTypeLabel(payload); searchType != "" {
			parts = append(parts, searchType)
		}
		if failedQueries > 0 && !strings.Contains(strings.Join(parts, " · "), "failed") {
			parts = append(parts, fmt.Sprintf("%d failed", failedQueries))
		}
		if (jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries")) && !strings.Contains(strings.Join(parts, " · "), "partial") {
			parts = append(parts, "partial")
		}
		return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
	}

	queries := webSearchRequestedQueries(payload)
	parts := []string{"websearch"}
	if primaryQuery != "" {
		parts = append(parts, primaryQuery)
	} else if len(queries) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(queries), pluralizeLabel(len(queries), "query", "queries")))
	}
	if searchType := webSearchTypeLabel(payload); searchType != "" {
		parts = append(parts, searchType)
	}
	if maxResults := jsonInt(payload, "max_results"); maxResults > 0 {
		parts = append(parts, fmt.Sprintf("max %d per query", maxResults))
	}
	if recencyDays := jsonInt(payload, "recency_days"); recencyDays > 0 {
		parts = append(parts, fmt.Sprintf("last %dd", recencyDays))
	}
	if domains := jsonStringSlice(payload, "include_domains"); len(domains) == 1 {
		parts = append(parts, domains[0])
	} else if len(domains) > 1 {
		parts = append(parts, fmt.Sprintf("%d domains", len(domains)))
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func summarizeSearchToolPayload(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	if mode != "content" && mode != "files" {
		return ""
	}
	queries := searchRequestedQueries(payload)
	root := strings.TrimSpace(jsonString(payload, "path"))
	count := jsonInt(payload, "count")
	queryCount := jsonInt(payload, "query_count")
	if queryCount <= 0 {
		queryCount = len(queries)
	}
	parts := []string{"search"}
	if len(queries) == 1 {
		parts = append(parts, fmt.Sprintf("%q", clampEllipsis(queries[0], maxInt(maxRunes/2, 24))))
	} else if queryCount > 1 {
		parts = append(parts, fmt.Sprintf("%d %s", queryCount, pluralizeLabel(queryCount, "query", "queries")))
	}
	if root != "" {
		parts = append(parts, root)
	}
	if count > 0 {
		label := "matches"
		if mode == "files" {
			label = "files"
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, label))
	}
	if totalMatched := jsonInt(payload, "total_matched"); totalMatched > count {
		parts = append(parts, fmt.Sprintf("%d total", totalMatched))
	}
	if merge := strings.TrimSpace(jsonString(payload, "merge_strategy")); merge != "" && queryCount > 1 {
		parts = append(parts, merge)
	}
	if jsonBool(payload, "timed_out") {
		parts = append(parts, "timed out")
	} else if jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated") || jsonBool(payload, "truncated_queries") {
		parts = append(parts, "partial")
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func summarizeListToolPayload(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	path := strings.TrimSpace(jsonString(payload, "path"))
	mode := strings.TrimSpace(jsonString(payload, "mode"))
	count := jsonInt(payload, "count")
	totalFound := jsonInt(payload, "total_found")
	label := "list"
	if path != "" {
		label += " " + path
	}
	notes := make([]string, 0, 4)
	switch {
	case totalFound > count:
		notes = append(notes, fmt.Sprintf("showing %d of %d entries", count, totalFound))
	default:
		notes = append(notes, fmt.Sprintf("%d %s", count, pluralizeLabel(count, "entry", "entries")))
	}
	if view := toolListViewLabel(mode); view != "" {
		notes = append(notes, view)
	}
	if jsonBool(payload, "truncated") {
		notes = append(notes, "partial results")
	}
	if jsonBool(payload, "scan_limited") {
		notes = append(notes, "scan limited")
	}
	return clampEllipsis(toolSummaryWithNotes(label, notes...), maxRunes)
}

func summarizeWebFetchToolPayload(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	count := jsonInt(payload, "count")
	successCount := jsonInt(payload, "success_count")
	parts := []string{"webfetch"}
	if mode := strings.TrimSpace(jsonString(payload, "retrieval_mode")); mode != "" {
		parts = append(parts, mode)
	}
	if count > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", count, pluralizeLabel(count, "URL", "URLs")))
	}
	if successCount > 0 {
		parts = append(parts, fmt.Sprintf("%d ok", successCount))
	}
	if jsonBool(payload, "timed_out") {
		parts = append(parts, "timed out")
	}
	if jsonBool(payload, "details_truncated") || jsonBool(payload, "text_truncated") || jsonBool(payload, "truncated_urls") {
		parts = append(parts, "partial")
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func webFetchResultLabel(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	title := strings.TrimSpace(jsonString(payload, "title"))
	rawURL := strings.TrimSpace(jsonString(payload, "url"))
	host := webSearchHostLabel(rawURL)
	published := webSearchPublishedDateLabel(jsonString(payload, "published_date"))
	errorText := strings.TrimSpace(jsonString(payload, "error"))

	headline := title
	if headline == "" {
		headline = host
	}
	if headline == "" {
		headline = rawURL
	}
	if headline == "" {
		return ""
	}
	parts := []string{headline}
	if host != "" && !strings.EqualFold(strings.TrimSpace(headline), host) {
		parts = append(parts, host)
	}
	if published != "" {
		parts = append(parts, published)
	}
	if errorText != "" {
		parts = append(parts, "error")
	} else if jsonBool(payload, "text_truncated") {
		parts = append(parts, "text truncated")
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func isEditPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(entry.ToolName))
	if name == "edit" {
		return true
	}
	tool := strings.ToLower(strings.TrimSpace(jsonString(payload, "tool")))
	if tool == "edit" || strings.EqualFold(strings.TrimSpace(jsonString(payload, "path_id")), "tool.edit.v3") {
		return true
	}
	if len(editPayloadPreviewItems(payload)) > 0 {
		return true
	}
	return jsonInt(payload, "edit_count") > 0
}

func formatAskUserToolEntry(entry chatToolStreamEntry, maxRunes int) string {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil {
		payload = parseToolJSON(strings.TrimSpace(entry.Raw))
	}
	if payload == nil || !isAskUserPayload(entry, payload) {
		return ""
	}

	rows := askUserSummaryRows(payload)
	if len(rows) == 0 {
		if summary := strings.TrimSpace(jsonString(payload, "summary")); summary != "" {
			return clampEllipsis(summary, maxRunes)
		}
		return "ask-user response captured"
	}

	maxRows := 4
	if len(rows) > maxRows {
		rows = append(rows[:maxRows], askUserSummaryRow{
			Question: "…",
			Answer:   fmt.Sprintf("+%d more", len(rows)-maxRows),
		})
	}

	lines := []string{
		"ask-user responses",
		"| Question | Response |",
		"| --- | --- |",
	}
	for _, row := range rows {
		question := cleanAskUserCell(row.Question, 64)
		answer := cleanAskUserCell(row.Answer, 64)
		lines = append(lines, fmt.Sprintf("| %s | %s |", question, answer))
	}
	return strings.Join(lines, "\n")
}

func isAskUserPayload(entry chatToolStreamEntry, payload map[string]any) bool {
	name := strings.ToLower(strings.TrimSpace(entry.ToolName))
	if name == "ask-user" || name == "ask_user" {
		return true
	}
	tool := strings.ToLower(strings.TrimSpace(jsonString(payload, "tool")))
	return tool == "ask-user" || tool == "ask_user"
}

type askUserSummaryRow struct {
	Question string
	Answer   string
}

func askUserSummaryRows(payload map[string]any) []askUserSummaryRow {
	if payload == nil {
		return nil
	}
	answerByID := jsonStringMap(payload, "answers")
	rows := make([]askUserSummaryRow, 0, 6)

	if rawQuestions, ok := payload["questions"].([]any); ok {
		for _, item := range rawQuestions {
			questionMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(jsonString(questionMap, "id"))
			question := strings.TrimSpace(jsonString(questionMap, "question"))
			if question == "" {
				question = strings.TrimSpace(jsonString(questionMap, "prompt"))
			}
			if question == "" {
				question = strings.TrimSpace(jsonString(questionMap, "title"))
			}
			if question == "" {
				question = id
			}
			answer := strings.TrimSpace(answerByID[id])
			if answer == "" && len(rawQuestions) == 1 {
				answer = strings.TrimSpace(jsonString(payload, "answer"))
			}
			if answer == "" {
				continue
			}
			rows = append(rows, askUserSummaryRow{Question: question, Answer: answer})
		}
	}

	if len(rows) == 0 {
		question := strings.TrimSpace(jsonString(payload, "question"))
		answer := strings.TrimSpace(jsonString(payload, "answer"))
		if question != "" && answer != "" {
			rows = append(rows, askUserSummaryRow{Question: question, Answer: answer})
		}
	}

	if len(rows) == 0 && len(answerByID) > 0 {
		keys := make([]string, 0, len(answerByID))
		for key := range answerByID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			answer := strings.TrimSpace(answerByID[key])
			if answer == "" {
				continue
			}
			rows = append(rows, askUserSummaryRow{
				Question: key,
				Answer:   answer,
			})
		}
	}
	return rows
}

func cleanAskUserCell(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "|", "/")
	if text == "" {
		return "-"
	}
	return clampEllipsis(text, maxRunes)
}

func toolMessageHeadline(entry chatToolStreamEntry, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = chatToolPreviewMaxRunes
	}
	headline := strings.TrimSpace(toolHeadline(entry, maxRunes))
	toolName := strings.TrimSpace(entry.ToolName)
	if headline == "" {
		headline = toolName
	}
	if strings.EqualFold(toolName, "bash") {
		return clampEllipsis(headline, maxRunes)
	}

	informative := ""
	for _, line := range toolPreviewLines(entry, maxRunes, chatToolPreviewMaxLines) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isToolNoiseLine(trimmed, toolName) {
			continue
		}
		informative = trimmed
		break
	}
	if informative != "" && isToolNoiseLine(headline, toolName) {
		return clampEllipsis(informative, maxRunes)
	}
	if headline == "" && informative != "" {
		return clampEllipsis(informative, maxRunes)
	}
	return clampEllipsis(headline, maxRunes)
}

func isToolNoiseLine(line, toolName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(line))
	if normalized == "" {
		return true
	}
	switch normalized {
	case "ok", "done", "success", "completed", "complete", "running":
		return true
	}
	if tool := strings.ToLower(strings.TrimSpace(toolName)); tool != "" && normalized == tool {
		return true
	}
	return false
}

func toolHeadline(entry chatToolStreamEntry, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = chatToolPreviewMaxRunes
	}
	toolName := strings.TrimSpace(entry.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	preferred := preferredStructuredToolText(strings.ToLower(toolName), strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw))
	if summary := summarizeStructuredToolPreview(strings.ToLower(toolName), preferred); summary != "" {
		return clampEllipsis(summary, maxRunes)
	}
	if !strings.EqualFold(toolName, "edit") && preferred != strings.TrimSpace(entry.Raw) {
		if summary := summarizeStructuredToolPreview(strings.ToLower(toolName), strings.TrimSpace(entry.Raw)); summary != "" {
			return clampEllipsis(summary, maxRunes)
		}
	}
	return clampEllipsis(toolName, maxRunes)
}

func toolMetaLine(entry chatToolStreamEntry) string {
	status := "ok"
	switch normalizedToolState(entry) {
	case "running":
		status = "running"
	case "error":
		status = "error"
	}
	if duration := toolDurationLabel(entry.DurationMS); duration != "" {
		return status + "  ·  " + duration
	}
	return status
}

func normalizedToolState(entry chatToolStreamEntry) string {
	state := strings.ToLower(strings.TrimSpace(entry.State))
	if strings.TrimSpace(entry.Error) != "" {
		return "error"
	}
	switch state {
	case "waiting_approval", "queued", "pending":
		return "pending"
	case "running":
		return "running"
	case "failed", "cancelled", "canceled", "skipped", "error":
		return "error"
	case "completed", "done", "ok", "success":
		return "done"
	case "":
		return "pending"
	default:
		return "done"
	}
}

func (p *ChatPage) toolEntryDurationLabel(entry chatToolStreamEntry) string {
	if entry.DurationMS > 0 {
		return toolDurationLabel(entry.DurationMS)
	}
	startAt := entry.StartedAt
	if startAt <= 0 {
		startAt = entry.CreatedAt
	}
	if startAt <= 0 {
		return ""
	}
	state := strings.ToLower(strings.TrimSpace(entry.State))
	if state == "running" {
		d := time.Since(time.UnixMilli(startAt))
		if d < 0 {
			d = 0
		}
		return formatDurationCompact(d)
	}
	endAt := entry.CreatedAt
	if endAt <= startAt {
		return ""
	}
	return formatDurationCompact(time.Duration(endAt-startAt) * time.Millisecond)
}

func toolDurationLabel(durationMS int64) string {
	if durationMS <= 0 {
		return ""
	}
	return formatDurationCompact(time.Duration(durationMS) * time.Millisecond)
}

func parseDurationMilliseconds(values ...string) int64 {
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		value = strings.TrimSuffix(value, "ms")
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func toolPreviewLines(entry chatToolStreamEntry, maxRunes, maxLines int) []string {
	if maxRunes <= 0 {
		maxRunes = chatToolPreviewMaxRunes
	}
	if maxLines <= 0 {
		maxLines = chatToolPreviewMaxLines
	}

	toolName := strings.ToLower(strings.TrimSpace(entry.ToolName))
	if toolName == "read" {
		return nil
	}
	if isLiveBashToolEntry(entry) {
		return bashPreviewLinesWithHint(entry, maxRunes, maxLines)
	}

	if editLines := editToolPreviewLines(entry, maxRunes, maxLines); len(editLines) > 0 {
		return editLines
	}

	preview := preferredStructuredToolText(toolName, strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw))
	if preview == "" {
		if toolName == "edit" {
			return nil
		}
		preview = strings.TrimSpace(entry.Raw)
	}
	if preview == "" {
		return nil
	}
	if toolName == "bash" {
		if lines := bashPreviewLinesWithHint(entry, maxRunes, maxLines); len(lines) > 0 {
			return lines
		}
	}
	if structured := structuredToolPreviewLines(toolName, preview, maxRunes, maxLines); len(structured) > 0 {
		return structured
	}
	if summary := summarizeStructuredToolPreview(toolName, preview); summary != "" {
		return []string{clampEllipsis(summary, maxRunes)}
	}
	if (toolName == "websearch" || toolName == "search") && preview != strings.TrimSpace(entry.Raw) {
		rawPreview := strings.TrimSpace(entry.Raw)
		if rawPreview != "" && rawPreview != preview {
			if structured := structuredToolPreviewLines(toolName, rawPreview, maxRunes, maxLines); len(structured) > 0 {
				return structured
			}
			if summary := summarizeStructuredToolPreview(toolName, rawPreview); summary != "" {
				return []string{clampEllipsis(summary, maxRunes)}
			}
		}
	}

	lines := make([]string, 0, maxLines)
	for _, line := range strings.Split(strings.ReplaceAll(preview, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, clampEllipsis(line, maxRunes))
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func isLiveBashToolEntry(entry chatToolStreamEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.ToolName), "bash") && normalizedToolState(entry) == "running"
}

func liveBashPreviewLines(entry chatToolStreamEntry, maxRunes, maxLines int) []string {
	lines, _ := bashPreviewLinesAndTruncated(entry, maxRunes, maxLines)
	return lines
}

func bashPreviewLinesWithHint(entry chatToolStreamEntry, maxRunes, maxLines int) []string {
	lines, truncated := bashPreviewLinesAndTruncated(entry, maxRunes, maxLines)
	if len(lines) == 0 || !truncated {
		return lines
	}
	hint := clampEllipsis("write /output to see full output", maxRunes)
	lines[len(lines)-1] = hint
	return lines
}

func bashPreviewLinesAndTruncated(entry chatToolStreamEntry, maxRunes, maxLines int) ([]string, bool) {
	if maxRunes <= 0 {
		maxRunes = chatToolPreviewMaxRunes
	}
	if maxLines <= 0 {
		maxLines = chatBashLivePreviewMaxLines
	}
	text, payloadTruncated := bashPreviewSource(entry)
	lines, previewTruncated := tailPreviewLinesWithTruncation(text, maxRunes, maxLines)
	return lines, payloadTruncated || previewTruncated
}

func structuredToolPreviewLines(toolName, raw string, maxRunes, maxLines int) []string {
	payload := parseToolJSON(strings.TrimSpace(raw))
	if payload == nil || maxLines <= 0 {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read":
		return structuredReadPreviewLines(payload, maxRunes, maxLines)
	case "grep":
		return structuredGrepPreviewLines(payload, maxRunes, maxLines)
	case "bash":
		return structuredBashPreviewLines(payload, maxRunes, maxLines)
	case "websearch":
		return structuredWebSearchPreviewLines(payload, maxRunes, maxLines)
	case "search":
		return structuredSearchPreviewLines(payload, maxRunes, maxLines)
	case "list":
		return structuredListPreviewLines(payload, maxRunes, maxLines)
	case "webfetch":
		return structuredWebFetchPreviewLines(payload, maxRunes, maxLines)
	case "manage_todos":
		return structuredManageTodosPreviewLines(payload, maxRunes, maxLines)
	case "task":
		return structuredTaskPreviewLines(payload, maxRunes, maxLines)
	case "exit-plan-mode", "exit_plan_mode", "permission":
		return structuredExitPlanPreviewLines(toolName, payload, maxRunes, maxLines)
	default:
		return nil
	}
}

func structuredReadPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	rawLines, ok := payload["lines"].([]any)
	if !ok || len(rawLines) == 0 {
		if jsonInt(payload, "count") == 0 {
			return []string{clampEllipsis("0 lines", maxRunes)}
		}
		return nil
	}
	lines := make([]string, 0, minInt(maxLines, len(rawLines)))
	for i := 0; i < len(rawLines) && len(lines) < maxLines; i++ {
		item, ok := rawLines[i].(map[string]any)
		if !ok {
			continue
		}
		lineNo := jsonInt(item, "line")
		text := strings.TrimSpace(jsonString(item, "text"))
		if text == "" {
			text = "(blank)"
		}
		if lineNo > 0 {
			lines = append(lines, clampEllipsis(fmt.Sprintf("%d: %s", lineNo, text), maxRunes))
			continue
		}
		lines = append(lines, clampEllipsis(text, maxRunes))
	}
	if len(lines) == 0 && jsonInt(payload, "count") == 0 {
		return []string{clampEllipsis("0 lines", maxRunes)}
	}
	return lines
}

func structuredGrepPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	rawMatches, ok := payload["matches"].([]any)
	if !ok || len(rawMatches) == 0 {
		if jsonInt(payload, "count") == 0 {
			return []string{clampEllipsis("0 matches", maxRunes)}
		}
		return nil
	}
	lines := make([]string, 0, minInt(maxLines, len(rawMatches)))
	for i := 0; i < len(rawMatches) && len(lines) < maxLines; i++ {
		item, ok := rawMatches[i].(map[string]any)
		if !ok {
			continue
		}
		path := strings.TrimSpace(jsonString(item, "path"))
		lineNo := jsonInt(item, "line")
		text := strings.TrimSpace(jsonString(item, "text"))
		if text == "" {
			text = "(blank)"
		}
		summary := text
		if path != "" && lineNo > 0 {
			summary = fmt.Sprintf("%s:%d: %s", path, lineNo, text)
		} else if path != "" {
			summary = fmt.Sprintf("%s: %s", path, text)
		} else if lineNo > 0 {
			summary = fmt.Sprintf("%d: %s", lineNo, text)
		}
		lines = append(lines, clampEllipsis(summary, maxRunes))
	}
	if len(lines) == 0 && jsonInt(payload, "count") == 0 {
		return []string{clampEllipsis("0 matches", maxRunes)}
	}
	return lines
}

func structuredBashPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil {
		return nil
	}
	if summary := summarizeBashPermissionPayload("bash", payload); summary != "" {
		return []string{clampEllipsis(summary, maxRunes)}
	}
	output := strings.TrimSpace(jsonString(payload, "output"))
	if output == "" {
		return nil
	}
	lines, _ := tailPreviewLinesWithTruncation(output, maxRunes, maxLines)
	return lines
}

func bashPreviewSource(entry chatToolStreamEntry) (string, bool) {
	for _, candidate := range []string{
		preferredStructuredToolText("bash", strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw)),
		strings.TrimSpace(entry.Output),
		strings.TrimSpace(entry.Raw),
	} {
		if candidate == "" {
			continue
		}
		if payload := parseToolJSON(candidate); payload != nil {
			output := strings.TrimSpace(jsonString(payload, "output"))
			if output == "" {
				continue
			}
			return normalizePreviewNewlines(output), jsonBool(payload, "truncated")
		}
		return normalizePreviewNewlines(candidate), false
	}
	return "", false
}

func tailPreviewLines(text string, maxRunes, maxLines int) []string {
	lines, _ := tailPreviewLinesWithTruncation(text, maxRunes, maxLines)
	return lines
}

func tailPreviewLinesWithTruncation(text string, maxRunes, maxLines int) ([]string, bool) {
	if maxRunes <= 0 {
		maxRunes = chatToolPreviewMaxRunes
	}
	if maxLines <= 0 {
		maxLines = chatToolPreviewMaxLines
	}
	text = normalizePreviewNewlines(text)
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	parts := strings.Split(text, "\n")
	totalNonEmpty := 0
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			totalNonEmpty++
		}
	}
	lines := make([]string, 0, minInt(maxLines, len(parts)))
	lineTruncated := false
	for i := len(parts) - 1; i >= 0 && len(lines) < maxLines; i-- {
		line := strings.TrimRight(parts[i], "\t ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		clamped := clampEllipsis(line, maxRunes)
		if clamped != line {
			lineTruncated = true
		}
		lines = append(lines, clamped)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, lineTruncated || totalNonEmpty > len(lines)
}

func countNonEmptyPreviewLines(text string) int {
	count := 0
	for _, part := range strings.Split(normalizePreviewNewlines(text), "\n") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	return count
}

func normalizePreviewNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func structuredListPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	entries := jsonObjectSlice(payload, "entries")
	if len(entries) == 0 {
		return nil
	}
	lines := make([]string, 0, minInt(maxLines, len(entries)))
	for i := 0; i < len(entries) && len(lines) < maxLines; i++ {
		entry := entries[i]
		path := strings.TrimSpace(jsonString(entry, "path"))
		entryType := strings.TrimSpace(jsonString(entry, "type"))
		depth := jsonInt(entry, "depth")
		line := path
		if line == "" {
			continue
		}
		if entryType != "" {
			line += "  ·  " + entryType
		}
		if depth > 0 {
			line += fmt.Sprintf("  ·  depth %d", depth)
		}
		lines = append(lines, clampEllipsis(line, maxRunes))
	}
	return lines
}

func structuredWebFetchPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	results := jsonObjectSlice(payload, "results")
	if len(results) == 0 {
		return nil
	}
	lines := make([]string, 0, minInt(maxLines, len(results)))
	for i := 0; i < len(results) && len(lines) < maxLines; i++ {
		result := results[i]
		if line := webFetchResultLabel(result, maxRunes); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func structuredListTimelineLines(payload map[string]any, maxRunes, maxLines int) []string {
	if maxLines <= 0 || payload == nil {
		return nil
	}
	lines := make([]string, 0, maxLines)
	if mode := strings.TrimSpace(jsonString(payload, "mode")); mode != "" {
		lines = append(lines, clampEllipsis("mode: "+mode, maxRunes))
		if len(lines) >= maxLines {
			return lines
		}
	}
	if nextCursor := jsonInt(payload, "next_cursor"); nextCursor > 0 {
		lines = append(lines, clampEllipsis(fmt.Sprintf("next_cursor: %d", nextCursor), maxRunes))
		if len(lines) >= maxLines {
			return lines
		}
	}
	for _, line := range structuredListPreviewLines(payload, maxRunes, maxLines-len(lines)) {
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func structuredManageTodosPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(jsonString(payload, "action")))
	lines := make([]string, 0, maxLines)
	if shouldShowManageTodosSummaryLines(action) {
		if summaryPayload := jsonObject(payload, "summary"); len(summaryPayload) > 0 {
			for _, line := range manageTodosSummaryPreviewLines(summaryPayload, maxRunes, maxLines-len(lines)) {
				if line == "" {
					continue
				}
				lines = append(lines, line)
				if len(lines) >= maxLines {
					return lines
				}
			}
		}
	}
	for _, item := range prioritizeManageTodosPreviewItems(payload) {
		if len(lines) >= maxLines {
			break
		}
		for _, line := range manageTodosPreviewItemLines(item, maxRunes) {
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) >= maxLines {
				break
			}
		}
	}
	for _, line := range manageTodosStatusPreviewLines(payload, maxRunes, maxLines-len(lines)) {
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	if len(lines) > 0 {
		return lines
	}
	if empty := manageTodosEmptyPreviewLine(payload); empty != "" {
		return []string{clampEllipsis(empty, maxRunes)}
	}
	return nil
}

func shouldShowManageTodosSummaryLines(action string) bool {
	switch action {
	case "summary":
		return true
	default:
		return false
	}
}

func prioritizeManageTodosPreviewItems(payload map[string]any) []map[string]any {
	return manageTodosPreviewItems(payload)
}

func manageTodosPreviewItems(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(jsonString(payload, "action"))) {
	case "batch":
		return manageTodosPreviewItemsFromResults(payload)
	case "create", "update", "in_progress":
		if item := jsonObject(payload, "item"); len(item) > 0 {
			return []map[string]any{item}
		}
		return nil
	case "list":
		return manageTodosListPreviewItems(payload)
	default:
		return nil
	}
}

func manageTodosPreviewItemsFromResults(payload map[string]any) []map[string]any {
	results := jsonObjectSlice(payload, "results")
	if len(results) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		item := jsonObject(result, "item")
		if len(item) == 0 {
			continue
		}
		key := strings.TrimSpace(jsonString(item, "id"))
		if key == "" {
			key = strings.TrimSpace(jsonString(item, "text"))
		}
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func manageTodosListPreviewItems(payload map[string]any) []map[string]any {
	items := jsonObjectSlice(payload, "items")
	if len(items) == 0 {
		return nil
	}
	ownerKind := strings.ToLower(strings.TrimSpace(jsonString(payload, "owner_kind")))
	sessionID := strings.TrimSpace(jsonString(payload, "session_id"))
	if ownerKind != "agent" || sessionID == "" {
		return items
	}
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(jsonString(item, "session_id")) != sessionID {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func manageTodosStatusPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	appendLine := func(line string) {
		if len(lines) >= maxLines {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		lines = append(lines, clampEllipsis(line, maxRunes))
	}
	switch strings.ToLower(strings.TrimSpace(jsonString(payload, "action"))) {
	case "delete":
		if id := strings.TrimSpace(jsonString(payload, "id")); id != "" {
			appendLine("Deleted " + id + ".")
		} else {
			appendLine("Deleted todo.")
		}
	case "delete_done":
		appendLine("Deleted completed todos.")
	case "delete_all":
		appendLine("Deleted todos.")
	case "reorder":
		appendLine("Reordered todos.")
	case "batch":
		for _, line := range manageTodosBatchStatusPreviewLines(payload) {
			appendLine(line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}

func manageTodosBatchStatusPreviewLines(payload map[string]any) []string {
	results := jsonObjectSlice(payload, "results")
	if len(results) == 0 {
		return nil
	}
	lines := make([]string, 0, len(results))
	for _, result := range results {
		if line := manageTodosBatchResultStatusLine(result); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}

func manageTodosBatchResultStatusLine(result map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(jsonString(result, "action"))) {
	case "delete":
		if id := strings.TrimSpace(jsonString(result, "id")); id != "" {
			return "Deleted " + id + "."
		}
		return "Deleted todo."
	case "delete_done":
		count := manageTodosDeletedCount(result)
		if count > 0 {
			return fmt.Sprintf("Deleted %d completed %s.", count, pluralizeLabel(count, "todo", "todos"))
		}
		return "Deleted completed todos."
	case "delete_all":
		count := manageTodosDeletedCount(result)
		if count > 0 {
			return fmt.Sprintf("Deleted %d %s.", count, pluralizeLabel(count, "todo", "todos"))
		}
		return "Deleted todos."
	case "reorder":
		return "Reordered todos."
	default:
		return ""
	}
}

func manageTodosDeletedCount(payload map[string]any) int {
	id := strings.TrimSpace(jsonString(payload, "id"))
	if !strings.HasPrefix(id, "deleted:") {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(id, "deleted:")))
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func manageTodosEmptyPreviewLine(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if strings.ToLower(strings.TrimSpace(jsonString(payload, "action"))) != "list" {
		return ""
	}
	ownerKind := strings.ToLower(strings.TrimSpace(jsonString(payload, "owner_kind")))
	sessionID := strings.TrimSpace(jsonString(payload, "session_id"))
	if ownerKind == "agent" && sessionID != "" {
		return "No agent todos for this session."
	}
	return "No todos."
}

func manageTodosPreviewItemLines(item map[string]any, maxRunes int) []string {
	if item == nil {
		return nil
	}
	checkbox := "[ ]"
	if jsonBool(item, "done") {
		checkbox = "[x]"
	}
	prefix := checkbox
	if !jsonBool(item, "done") && jsonBool(item, "in_progress") {
		prefix = "> " + checkbox
	}
	text := strings.TrimSpace(jsonString(item, "text"))
	if text == "" {
		text = firstNonEmptyToolValue(strings.TrimSpace(jsonString(item, "id")), "Todo")
	}
	metadata := make([]string, 0, 2)
	if group := strings.TrimSpace(jsonString(item, "group")); group != "" {
		metadata = append(metadata, group)
	}
	if tags := jsonStringSlice(item, "tags"); len(tags) > 0 {
		metadata = append(metadata, "#"+strings.Join(tags, " #"))
	}
	body := prefix + " " + text
	if priority := strings.TrimSpace(jsonString(item, "priority")); priority != "" {
		body += "  ·  " + priority
	}
	lines := make([]string, 0, 2)
	if len(metadata) > 0 {
		lines = append(lines, clampEllipsis(strings.Join(metadata, "  ·  "), maxRunes))
	}
	lines = append(lines, clampEllipsis(body, maxRunes))
	return lines
}

func manageTodosSummaryPreviewLines(summaryPayload map[string]any, maxRunes, maxLines int) []string {
	if summaryPayload == nil || maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	appendSummaryLine := func(label string, payload map[string]any) {
		if len(lines) >= maxLines || payload == nil {
			return
		}
		total := jsonInt(payload, "task_count")
		open := jsonInt(payload, "open_count")
		inProgress := jsonInt(payload, "in_progress_count")
		parts := []string{fmt.Sprintf("%s: %d open · %d total", label, open, total)}
		if inProgress > 0 {
			parts = append(parts, fmt.Sprintf("%d in progress", inProgress))
		}
		lines = append(lines, clampEllipsis(strings.Join(parts, "  ·  "), maxRunes))
	}
	appendSummaryLine("All Todos", summaryPayload)
	appendSummaryLine("User Todos", jsonObject(summaryPayload, "user"))
	appendSummaryLine("Agent Checklist", jsonObject(summaryPayload, "agent"))
	return lines
}

func structuredWebFetchTimelineLines(payload map[string]any, maxRunes, maxLines int) []string {
	if maxLines <= 0 || payload == nil {
		return nil
	}
	lines := make([]string, 0, maxLines)
	if mode := strings.TrimSpace(jsonString(payload, "retrieval_mode")); mode != "" {
		lines = append(lines, clampEllipsis("mode: "+mode, maxRunes))
		if len(lines) >= maxLines {
			return lines
		}
	}
	for _, line := range structuredWebFetchPreviewLines(payload, maxRunes, maxLines-len(lines)) {
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func structuredSearchTimelineLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	rows, grouped := searchToolRenderRows(payload)
	return searchToolTablePreviewLines(rows, grouped, maxRunes, maxLines)
}

func structuredWebSearchPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}

	queryResults := webSearchResultQueryPayloads(payload)
	singleQuery := webSearchPrimaryQuery(payload) != ""
	if len(queryResults) > 0 {
		if len(queryResults) == 1 {
			if hit := webSearchTopHitPreviewLine(queryResults[0], maxRunes); hit != "" {
				return []string{hit}
			}
			if summary := webSearchQuerySummaryLine(queryResults[0], maxRunes, true); summary != "" {
				return []string{summary}
			}
		}
		lines := make([]string, 0, maxLines)
		multiQuery := len(queryResults) > 1
		for _, queryPayload := range queryResults {
			if len(lines) >= maxLines {
				break
			}
			if summary := webSearchQuerySummaryLine(queryPayload, maxRunes, singleQuery); summary != "" {
				lines = append(lines, summary)
			}
			if len(lines) >= maxLines {
				break
			}
			if !multiQuery {
				if hit := webSearchTopHitPreviewLine(queryPayload, maxRunes); hit != "" {
					lines = append(lines, hit)
				}
			}
		}
		if len(lines) == 0 {
			return []string{clampEllipsis(fmt.Sprintf("%d %s", jsonInt(payload, "total_results"), pluralizeLabel(jsonInt(payload, "total_results"), "result", "results")), maxRunes)}
		}
		return lines
	}

	queries := webSearchRequestedQueries(payload)
	if len(queries) == 0 {
		return nil
	}
	if singleQuery {
		return nil
	}

	lines := make([]string, 0, maxLines)
	for idx, query := range queries {
		if len(lines) >= maxLines {
			break
		}
		if idx == maxLines-1 && len(queries) > idx+1 {
			lines = append(lines, clampEllipsis(fmt.Sprintf("%s  ·  +%d more", query, len(queries)-idx-1), maxRunes))
			break
		}
		lines = append(lines, clampEllipsis(query, maxRunes))
	}
	return lines
}

func structuredSearchPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	rows, grouped := searchToolRenderRows(payload)
	return searchToolTablePreviewLines(rows, grouped, maxRunes, maxLines)
}

func webSearchRequestedQueries(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	queries := make([]string, 0, 4)
	if single := strings.TrimSpace(jsonString(payload, "query")); single != "" {
		queries = append(queries, single)
	}
	queries = append(queries, jsonStringSlice(payload, "queries")...)
	if len(queries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(queries))
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, query)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func searchRequestedQueries(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	queries := make([]string, 0, 6)
	if single := strings.TrimSpace(jsonString(payload, "query")); single != "" {
		queries = append(queries, single)
	}
	if legacy := strings.TrimSpace(jsonString(payload, "pattern")); legacy != "" {
		queries = append(queries, legacy)
	}
	queries = append(queries, jsonStringSlice(payload, "queries")...)
	if len(queries) == 0 {
		for _, queryPayload := range searchResultQueryPayloads(payload) {
			if query := strings.TrimSpace(jsonString(queryPayload, "query")); query != "" {
				queries = append(queries, query)
			}
		}
		if len(queries) == 0 {
			for _, group := range jsonObjectSlice(payload, "results") {
				for _, item := range jsonObjectSlice(group, "items") {
					if query := strings.TrimSpace(jsonString(item, "query")); query != "" {
						queries = append(queries, query)
					}
				}
			}
		}
	}
	if len(queries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(queries))
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, query)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func searchToolTablePreviewLines(rows []searchToolRenderRow, grouped bool, maxRunes, maxLines int) []string {
	if len(rows) == 0 || maxLines <= 0 {
		return nil
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

	available := maxInt(maxRunes, 48)
	fixed := pathWidth + lineWidth + 3
	if showQuery {
		fixed += queryWidth + 3
	}
	if showInfo {
		fixed += infoWidth + 3
	}
	for fixed > available {
		shrank := false
		if showInfo && infoWidth > 6 {
			infoWidth--
			fixed--
			shrank = true
		}
		if fixed <= available {
			break
		}
		if pathWidth > 12 {
			pathWidth--
			fixed--
			shrank = true
		}
		if fixed <= available {
			break
		}
		if showQuery && queryWidth > 6 {
			queryWidth--
			fixed--
			shrank = true
		}
		if fixed <= available {
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

	lines := make([]string, 0, minInt(maxLines, len(rows)+1))
	headerParts := make([]string, 0, 4)
	if showQuery {
		headerParts = append(headerParts, fitLeft("Query", queryWidth))
	}
	headerParts = append(headerParts, fitLeft("Path", pathWidth), fitRight("Ln", lineWidth))
	if showInfo {
		headerParts = append(headerParts, fitLeft("Info", infoWidth))
	}
	lines = append(lines, clampEllipsis(strings.Join(headerParts, " │ "), available))
	for _, row := range rows {
		if len(lines) >= maxLines {
			break
		}
		parts := make([]string, 0, 4)
		if showQuery {
			parts = append(parts, fitLeft(emptyValue(row.Query, "-"), queryWidth))
		}
		parts = append(parts,
			fitLeft(emptyValue(row.Path, "-"), pathWidth),
			fitRight(emptyValue(row.Line, "-"), lineWidth),
		)
		if showInfo {
			parts = append(parts, fitLeft(emptyValue(strings.TrimSpace(row.Result), "-"), infoWidth))
		}
		lines = append(lines, clampEllipsis(strings.Join(parts, " │ "), available))
	}
	return lines
}

func searchAggregateSummaryLine(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	if mode != "content" && mode != "files" {
		return ""
	}
	queryCount := jsonInt(payload, "query_count")
	if queryCount <= 0 {
		queryCount = len(searchRequestedQueries(payload))
	}
	count := jsonInt(payload, "count")
	totalMatched := jsonInt(payload, "total_matched")
	parts := make([]string, 0, 5)
	if count > 0 {
		label := "matches"
		if mode == "files" {
			label = "files"
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, label))
	}
	if totalMatched > count {
		parts = append(parts, fmt.Sprintf("%d total", totalMatched))
	}
	if queryCount > 1 {
		parts = append(parts, fmt.Sprintf("across %d %s", queryCount, pluralizeLabel(queryCount, "query", "queries")))
	}
	if root := strings.TrimSpace(jsonString(payload, "path")); root != "" {
		parts = append(parts, root)
	}
	if jsonBool(payload, "timed_out") {
		parts = append(parts, "timed out")
	} else if jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") || jsonBool(payload, "truncated") {
		parts = append(parts, "partial")
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func searchResultQueryPayloads(payload map[string]any) []map[string]any {
	results := jsonObjectSlice(payload, "query_results")
	if len(results) > 0 {
		return results
	}
	return buildSearchQueryResultsFromGroupedResults(payload)
}

func searchQuerySummaryLine(payload map[string]any, maxRunes int, mode string, omitQuery bool) string {
	if payload == nil {
		return ""
	}
	query := strings.TrimSpace(jsonString(payload, "query"))
	count := jsonInt(payload, "count")
	parts := make([]string, 0, 4)
	if !omitQuery && query != "" {
		parts = append(parts, query)
	}
	if errText := strings.TrimSpace(jsonString(payload, "error")); errText != "" {
		parts = append(parts, "failed")
	} else if jsonBool(payload, "timed_out") {
		parts = append(parts, "timed out")
	} else {
		label := "matches"
		if mode == "files" {
			label = "files"
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, label))
	}
	if totalMatched := jsonInt(payload, "total_matched"); totalMatched > count {
		parts = append(parts, fmt.Sprintf("%d total", totalMatched))
	}
	if jsonBool(payload, "truncated") {
		parts = append(parts, "partial")
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func searchTopHitPreviewLine(payload, queryPayload map[string]any, maxRunes int) string {
	if queryPayload == nil {
		return ""
	}
	query := strings.TrimSpace(jsonString(queryPayload, "query"))
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	if len(jsonObjectSlice(payload, "results")) > 0 {
		for _, group := range jsonObjectSlice(payload, "results") {
			relPath := strings.TrimSpace(jsonString(group, "path"))
			if relPath == "" {
				continue
			}
			items := jsonObjectSlice(group, "items")
			if mode == "files" {
				for _, item := range items {
					if query != "" && !strings.EqualFold(strings.TrimSpace(jsonString(item, "query")), query) {
						continue
					}
					parts := []string{relPath}
					if score := jsonInt(item, "score"); score > 0 {
						parts = append(parts, fmt.Sprintf("score %d", score))
					}
					return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
				}
				continue
			}
			for _, item := range items {
				if query != "" && !strings.EqualFold(strings.TrimSpace(jsonString(item, "query")), query) {
					continue
				}
				text := strings.TrimSpace(jsonString(item, "text"))
				line := jsonInt(item, "line")
				label := relPath
				if line > 0 {
					label += fmt.Sprintf(":%d", line)
				}
				if text != "" {
					label += "  ·  " + text
				}
				return clampEllipsis(label, maxRunes)
			}
		}
	}
	if mode == "files" {
		for _, item := range jsonObjectSlice(payload, "files") {
			if query != "" && !strings.EqualFold(strings.TrimSpace(jsonString(item, "query")), query) {
				continue
			}
			relPath := strings.TrimSpace(jsonString(item, "relative_path"))
			if relPath == "" {
				relPath = strings.TrimSpace(jsonString(item, "path"))
			}
			if relPath == "" {
				continue
			}
			parts := []string{relPath}
			if score := jsonInt(item, "score"); score > 0 {
				parts = append(parts, fmt.Sprintf("score %d", score))
			}
			return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
		}
		return ""
	}
	for _, item := range jsonObjectSlice(payload, "matches") {
		if query != "" && !strings.EqualFold(strings.TrimSpace(jsonString(item, "query")), query) {
			continue
		}
		relPath := strings.TrimSpace(jsonString(item, "relative_path"))
		if relPath == "" {
			relPath = strings.TrimSpace(jsonString(item, "path"))
		}
		text := strings.TrimSpace(jsonString(item, "text"))
		line := jsonInt(item, "line")
		if relPath == "" && text == "" {
			continue
		}
		label := relPath
		if line > 0 {
			label += fmt.Sprintf(":%d", line)
		}
		if text != "" {
			label += "  ·  " + text
		}
		return clampEllipsis(label, maxRunes)
	}
	return ""
}

func searchPreviewQueryGroups(payload map[string]any, mode string) []searchPreviewQueryGroup {
	if payload == nil {
		return nil
	}

	queryResults := searchResultQueryPayloads(payload)
	queries := searchRequestedQueries(payload)
	groups := make([]searchPreviewQueryGroup, 0, maxInt(len(queryResults), len(queries)))
	seen := make(map[string]struct{}, maxInt(len(queryResults), len(queries)))

	appendGroup := func(queryPayload map[string]any, query string) {
		query = strings.TrimSpace(query)
		key := strings.ToLower(query)
		if query != "" {
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
		}

		group := searchPreviewQueryGroup{
			Query:       query,
			Mode:        mode,
			FileGroups:  searchFilePreviewGroups(payload, query, mode),
			SampleLines: searchSamplePreviewLines(payload, query, mode, 2),
		}
		if queryPayload != nil {
			group.Count = jsonInt(queryPayload, "count")
			group.TotalMatched = jsonInt(queryPayload, "total_matched")
			group.Failed = strings.TrimSpace(jsonString(queryPayload, "error")) != ""
			group.TimedOut = jsonBool(queryPayload, "timed_out")
			group.Partial = jsonBool(queryPayload, "truncated")
		}
		if group.Count <= 0 && mode == "files" {
			group.Count = len(group.FileGroups)
		}
		if group.TotalMatched < group.Count {
			group.TotalMatched = group.Count
		}
		groups = append(groups, group)
	}

	for _, queryPayload := range queryResults {
		appendGroup(queryPayload, jsonString(queryPayload, "query"))
	}
	for _, query := range queries {
		appendGroup(nil, query)
	}

	if len(groups) == 1 && len(queryResults) == 0 {
		if groups[0].Count <= 0 {
			groups[0].Count = jsonInt(payload, "count")
		}
		if groups[0].TotalMatched <= 0 {
			groups[0].TotalMatched = maxInt(groups[0].Count, jsonInt(payload, "total_matched"))
		}
		groups[0].TimedOut = groups[0].TimedOut || jsonBool(payload, "timed_out")
		groups[0].Partial = groups[0].Partial || jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") || jsonBool(payload, "truncated")
	}

	if len(groups) == 0 {
		group := searchPreviewQueryGroup{
			Mode:         mode,
			Count:        jsonInt(payload, "count"),
			TotalMatched: jsonInt(payload, "total_matched"),
			TimedOut:     jsonBool(payload, "timed_out"),
			Partial:      jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") || jsonBool(payload, "truncated"),
			FileGroups:   searchFilePreviewGroups(payload, "", mode),
			SampleLines:  searchSamplePreviewLines(payload, "", mode, 2),
		}
		if group.Count <= 0 && mode == "files" {
			group.Count = len(group.FileGroups)
		}
		if group.TotalMatched < group.Count {
			group.TotalMatched = group.Count
		}
		if group.Count > 0 || group.TotalMatched > 0 || len(group.FileGroups) > 0 || len(group.SampleLines) > 0 || group.TimedOut || group.Partial {
			groups = append(groups, group)
		}
	}

	return groups
}

func structuredSingleSearchQueryPreviewLines(group searchPreviewQueryGroup, maxRunes, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	if fileLine := searchPreviewGroupFilesLine(group, maxRunes); fileLine != "" {
		lines = append(lines, fileLine)
	}
	for _, sample := range group.SampleLines {
		if len(lines) >= maxLines {
			break
		}
		lines = append(lines, clampEllipsis("sample: "+sample, maxRunes))
	}
	if len(lines) == 0 {
		if summary := searchPreviewGroupSummaryLine(group, maxRunes, true); summary != "" {
			lines = append(lines, summary)
		}
	}
	return lines
}

func structuredMultiSearchQueryPreviewLines(groups []searchPreviewQueryGroup, maxRunes, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	shownGroups := 0
	for _, group := range groups {
		if len(lines) >= maxLines {
			break
		}
		if summary := searchPreviewGroupSummaryLine(group, maxRunes, false); summary != "" {
			lines = append(lines, summary)
		} else if group.Query != "" {
			lines = append(lines, clampEllipsis(group.Query, maxRunes))
		}
		shownGroups++
		if len(lines) >= maxLines {
			break
		}
		remainingGroups := len(groups) - shownGroups
		remainingSlots := maxLines - len(lines)
		if remainingSlots <= remainingGroups {
			continue
		}
		if fileLine := searchPreviewGroupFilesLine(group, maxRunes); fileLine != "" {
			lines = append(lines, fileLine)
			continue
		}
		if len(group.SampleLines) > 0 {
			lines = append(lines, clampEllipsis("sample: "+group.SampleLines[0], maxRunes))
		}
	}
	if remaining := len(groups) - shownGroups; remaining > 0 {
		extra := clampEllipsis(fmt.Sprintf("+%d more %s", remaining, pluralizeLabel(remaining, "query", "queries")), maxRunes)
		if len(lines) >= maxLines {
			lines[maxLines-1] = extra
		} else {
			lines = append(lines, extra)
		}
	}
	return lines
}

func searchPreviewGroupSummaryLine(group searchPreviewQueryGroup, maxRunes int, omitQuery bool) string {
	parts := make([]string, 0, 5)
	if !omitQuery && strings.TrimSpace(group.Query) != "" {
		parts = append(parts, strings.TrimSpace(group.Query))
	}
	switch {
	case group.Failed:
		parts = append(parts, "failed")
	case group.TimedOut:
		parts = append(parts, "timed out")
	case group.Count > 0:
		singular, plural := "match", "matches"
		if group.Mode == "files" {
			singular, plural = "file", "files"
		}
		parts = append(parts, toolCountLabel(group.Count, singular, plural))
	}
	if group.TotalMatched > group.Count {
		parts = append(parts, fmt.Sprintf("%d total", group.TotalMatched))
	}
	if fileCount := len(group.FileGroups); fileCount > 0 && group.Mode != "files" {
		parts = append(parts, toolCountLabel(fileCount, "file", "files"))
	}
	if group.Partial {
		parts = append(parts, "partial")
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func searchPreviewGroupFilesLine(group searchPreviewQueryGroup, maxRunes int) string {
	if len(group.FileGroups) == 0 {
		return ""
	}
	parts := make([]string, 0, minInt(4, len(group.FileGroups)))
	for i, fileGroup := range group.FileGroups {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(group.FileGroups)-i))
			break
		}
		label := fileGroup.Path
		if fileGroup.Count > 1 {
			label += fmt.Sprintf(" (%d)", fileGroup.Count)
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis("files: "+strings.Join(parts, ", "), maxRunes)
}

func searchFilePreviewGroups(payload map[string]any, query, mode string) []searchPreviewFileGroup {
	if payload == nil {
		return nil
	}
	if len(jsonObjectSlice(payload, "results")) > 0 {
		return searchFilePreviewGroupsFromGroupedResults(payload, query, mode)
	}
	items := jsonObjectSlice(payload, "matches")
	if mode == "files" {
		items = jsonObjectSlice(payload, "files")
	}
	if len(items) == 0 {
		return nil
	}

	query = strings.TrimSpace(query)
	multiQuery := len(searchRequestedQueries(payload)) > 1 || len(searchResultQueryPayloads(payload)) > 1
	groups := make([]searchPreviewFileGroup, 0, len(items))
	indexByPath := make(map[string]int, len(items))
	for _, item := range items {
		itemQuery := strings.TrimSpace(jsonString(item, "query"))
		if query != "" {
			switch {
			case itemQuery != "" && !strings.EqualFold(itemQuery, query):
				continue
			case itemQuery == "" && multiQuery:
				continue
			}
		}
		relPath := strings.TrimSpace(jsonString(item, "relative_path"))
		if relPath == "" {
			relPath = strings.TrimSpace(jsonString(item, "path"))
		}
		if relPath == "" {
			continue
		}
		key := strings.ToLower(relPath)
		increment := maxInt(1, jsonInt(item, "count"))
		if idx, ok := indexByPath[key]; ok {
			groups[idx].Count += increment
			continue
		}
		indexByPath[key] = len(groups)
		groups = append(groups, searchPreviewFileGroup{Path: relPath, Count: increment})
	}
	if len(groups) == 0 {
		return nil
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Count == groups[j].Count {
			return groups[i].Path < groups[j].Path
		}
		return groups[i].Count > groups[j].Count
	})
	return groups
}

func searchSamplePreviewLines(payload map[string]any, query, mode string, maxLines int) []string {
	if payload == nil || maxLines <= 0 || mode != "content" {
		return nil
	}
	if len(jsonObjectSlice(payload, "results")) > 0 {
		return searchSamplePreviewLinesFromGroupedResults(payload, query, maxLines)
	}
	matches := jsonObjectSlice(payload, "matches")
	if len(matches) == 0 {
		return nil
	}

	query = strings.TrimSpace(query)
	multiQuery := len(searchRequestedQueries(payload)) > 1 || len(searchResultQueryPayloads(payload)) > 1
	lines := make([]string, 0, maxLines)
	seen := make(map[string]struct{}, maxLines)
	for _, item := range matches {
		itemQuery := strings.TrimSpace(jsonString(item, "query"))
		if query != "" {
			switch {
			case itemQuery != "" && !strings.EqualFold(itemQuery, query):
				continue
			case itemQuery == "" && multiQuery:
				continue
			}
		}
		line := searchMatchPreviewLine(item, chatToolPreviewMaxRunes)
		if line == "" {
			continue
		}
		key := strings.ToLower(line)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func searchMatchPreviewLine(item map[string]any, maxRunes int) string {
	if item == nil {
		return ""
	}
	relPath := strings.TrimSpace(jsonString(item, "relative_path"))
	if relPath == "" {
		relPath = strings.TrimSpace(jsonString(item, "path"))
	}
	text := strings.TrimSpace(jsonString(item, "text"))
	lineNo := jsonInt(item, "line")
	if relPath == "" && text == "" {
		return ""
	}
	label := relPath
	if lineNo > 0 {
		label += fmt.Sprintf(":%d", lineNo)
	}
	if text != "" {
		if label != "" {
			label += "  ·  "
		}
		label += text
	}
	return clampEllipsis(label, maxRunes)
}

type searchPreviewQueryGroup struct {
	Query        string
	Mode         string
	Count        int
	TotalMatched int
	Failed       bool
	TimedOut     bool
	Partial      bool
	FileGroups   []searchPreviewFileGroup
	SampleLines  []string
}

type searchPreviewFileGroup struct {
	Path  string
	Count int
}

func buildSearchQueryResultsFromGroupedResults(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	groups := jsonObjectSlice(payload, "results")
	if len(groups) == 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(jsonString(payload, "search_mode")))
	totals := make(map[string]map[string]any)
	order := make([]string, 0, 4)
	for _, group := range groups {
		for _, item := range jsonObjectSlice(group, "items") {
			query := strings.TrimSpace(jsonString(item, "query"))
			if query == "" {
				continue
			}
			key := strings.ToLower(query)
			entry, ok := totals[key]
			if !ok {
				entry = map[string]any{"query": query, "mode": mode}
				totals[key] = entry
				order = append(order, key)
			}
			entry["count"] = jsonInt(entry, "count") + 1
			entry["total_matched"] = jsonInt(entry, "total_matched") + maxInt(1, jsonInt(item, "count"))
		}
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		out = append(out, totals[key])
	}
	return out
}

func searchFilePreviewGroupsFromGroupedResults(payload map[string]any, query, mode string) []searchPreviewFileGroup {
	groupsPayload := jsonObjectSlice(payload, "results")
	if len(groupsPayload) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	multiQuery := len(searchRequestedQueries(payload)) > 1 || len(searchResultQueryPayloads(payload)) > 1
	groups := make([]searchPreviewFileGroup, 0, len(groupsPayload))
	for _, group := range groupsPayload {
		relPath := strings.TrimSpace(jsonString(group, "path"))
		if relPath == "" {
			continue
		}
		count := 0
		for _, item := range jsonObjectSlice(group, "items") {
			itemQuery := strings.TrimSpace(jsonString(item, "query"))
			if query != "" {
				switch {
				case itemQuery != "" && !strings.EqualFold(itemQuery, query):
					continue
				case itemQuery == "" && multiQuery:
					continue
				}
			}
			count += maxInt(1, jsonInt(item, "count"))
			if mode == "files" && count > 0 {
				count = 1
				break
			}
		}
		if count <= 0 {
			continue
		}
		groups = append(groups, searchPreviewFileGroup{Path: relPath, Count: count})
	}
	if len(groups) == 0 {
		return nil
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Count == groups[j].Count {
			return groups[i].Path < groups[j].Path
		}
		return groups[i].Count > groups[j].Count
	})
	return groups
}

func searchSamplePreviewLinesFromGroupedResults(payload map[string]any, query string, maxLines int) []string {
	groups := jsonObjectSlice(payload, "results")
	if len(groups) == 0 || maxLines <= 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	multiQuery := len(searchRequestedQueries(payload)) > 1 || len(searchResultQueryPayloads(payload)) > 1
	lines := make([]string, 0, maxLines)
	seen := make(map[string]struct{}, maxLines)
	for _, group := range groups {
		relPath := strings.TrimSpace(jsonString(group, "path"))
		for _, item := range jsonObjectSlice(group, "items") {
			itemQuery := strings.TrimSpace(jsonString(item, "query"))
			if query != "" {
				switch {
				case itemQuery != "" && !strings.EqualFold(itemQuery, query):
					continue
				case itemQuery == "" && multiQuery:
					continue
				}
			}
			label := relPath
			if lineNo := jsonInt(item, "line"); lineNo > 0 {
				label += fmt.Sprintf(":%d", lineNo)
			}
			if text := strings.TrimSpace(jsonString(item, "text")); text != "" {
				label += "  ·  " + text
			}
			label = clampEllipsis(label, chatToolPreviewMaxRunes)
			if strings.TrimSpace(label) == "" {
				continue
			}
			key := strings.ToLower(label)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			lines = append(lines, label)
			if len(lines) >= maxLines {
				return lines
			}
		}
	}
	return lines
}

func webSearchAggregateSummaryLine(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	totalResults := jsonInt(payload, "total_results")
	queryCount := jsonInt(payload, "query_count")
	if queryCount <= 0 {
		queryCount = len(webSearchResultQueryPayloads(payload))
	}
	parts := make([]string, 0, 4)
	if totalResults > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", totalResults, pluralizeLabel(totalResults, "result", "results")))
	}
	if queryCount > 1 {
		parts = append(parts, fmt.Sprintf("across %d %s", queryCount, pluralizeLabel(queryCount, "query", "queries")))
	}
	if searchType := webSearchTypeLabel(payload); searchType != "" {
		parts = append(parts, searchType)
	}
	if failedQueries := jsonInt(payload, "failed_queries"); failedQueries > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failedQueries))
	}
	if jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") {
		parts = append(parts, "partial")
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func webSearchTypeLabel(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	requested := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "requested_search_type"),
		jsonString(payload, "search_type"),
	))
	resolved := strings.TrimSpace(jsonString(payload, "resolved_search_type"))
	if resolved == "" {
		resolvedTypes := jsonStringSlice(payload, "resolved_search_types")
		if len(resolvedTypes) == 1 {
			resolved = strings.TrimSpace(resolvedTypes[0])
		}
	}
	switch {
	case requested != "" && resolved != "" && !strings.EqualFold(requested, resolved):
		return requested + " -> " + resolved
	case resolved != "":
		return resolved
	default:
		return requested
	}
}

func webSearchResultQueryPayloads(payload map[string]any) []map[string]any {
	results := jsonObjectSlice(payload, "results")
	if len(results) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(results))
	for _, item := range results {
		if isWebSearchQueryPayload(item) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isWebSearchQueryPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return strings.TrimSpace(jsonString(payload, "query")) != ""
}

func webSearchPrimaryQuery(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	queryResults := webSearchResultQueryPayloads(payload)
	if len(queryResults) == 1 {
		if query := strings.TrimSpace(jsonString(queryResults[0], "query")); query != "" {
			return query
		}
	}
	queries := webSearchRequestedQueries(payload)
	if len(queries) == 1 {
		return queries[0]
	}
	return ""
}

func webSearchQuerySummaryLine(payload map[string]any, maxRunes int, omitQuery bool) string {
	if payload == nil {
		return ""
	}
	query := strings.TrimSpace(jsonString(payload, "query"))
	errorText := strings.TrimSpace(jsonString(payload, "error"))
	count := jsonInt(payload, "count")
	parts := make([]string, 0, 3)
	if !omitQuery && query != "" {
		parts = append(parts, query)
	}
	switch {
	case errorText != "":
		parts = append(parts, "failed")
	case jsonBool(payload, "timed_out"):
		parts = append(parts, "timed out")
	default:
		parts = append(parts, fmt.Sprintf("%d %s", count, pluralizeLabel(count, "result", "results")))
	}
	if len(parts) == 0 {
		return ""
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxRunes)
}

func webSearchTopHitPreviewLine(payload map[string]any, maxRunes int) string {
	for _, hit := range jsonObjectSlice(payload, "results") {
		label := webSearchHitLabel(hit)
		if label == "" {
			continue
		}
		return clampEllipsis(label, maxRunes)
	}
	return ""
}

func webSearchHitLabel(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	title := strings.TrimSpace(jsonString(payload, "title"))
	rawURL := strings.TrimSpace(jsonString(payload, "url"))
	host := webSearchHostLabel(rawURL)
	published := webSearchPublishedDateLabel(jsonString(payload, "published_date"))

	headline := title
	if headline == "" {
		headline = host
	}
	if headline == "" {
		headline = rawURL
	}
	if headline == "" {
		return ""
	}

	parts := []string{headline}
	if host != "" && !strings.EqualFold(strings.TrimSpace(headline), host) {
		parts = append(parts, host)
	}
	if published != "" {
		parts = append(parts, published)
	}
	return strings.Join(parts, "  ·  ")
}

func webSearchHostLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	host = strings.TrimPrefix(host, "www.")
	return host
}

func webSearchPublishedDateLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) >= 10 && raw[4] == '-' && raw[7] == '-' {
		return raw[:10]
	}
	return raw
}

func pluralizeLabel(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func toolCountLabel(count int, singular, plural string) string {
	return fmt.Sprintf("%d %s", count, pluralizeLabel(count, singular, plural))
}

func toolSummaryWithNotes(label string, notes ...string) string {
	filtered := make([]string, 0, len(notes))
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note == "" {
			continue
		}
		filtered = append(filtered, note)
	}
	if len(filtered) == 0 {
		return label
	}
	return label + " (" + strings.Join(filtered, ", ") + ")"
}

func toolListViewLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tree":
		return "tree view"
	case "flat":
		return "flat view"
	case "":
		return ""
	default:
		return mode + " view"
	}
}

func structuredTaskPreviewLines(payload map[string]any, maxRunes, maxLines int) []string {
	if payload == nil || maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	appendLine := func(prefix, value string) {
		if len(lines) >= maxLines {
			return
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if prefix != "" {
			lines = append(lines, clampEllipsis(prefix+value, maxRunes))
			return
		}
		lines = append(lines, clampEllipsis(value, maxRunes))
	}

	launches := jsonObjectSlice(payload, "launches")
	if len(launches) > 0 {
		for _, launch := range launches {
			if len(lines) >= maxLines {
				break
			}
			if line := taskLaunchPreviewLine(launch, maxRunes); line != "" {
				lines = append(lines, clampEllipsis(line, maxRunes))
			}
		}
		if len(lines) == 0 {
			return nil
		}
		return lines
	}

	if strings.EqualFold(strings.TrimSpace(jsonString(payload, "status")), "running") {
		appendLine("current_tool: ", jsonString(payload, "current_tool"))
		if currentToolDuration := toolDurationLabel(int64(jsonInt(payload, "current_tool_ms"))); currentToolDuration != "" {
			appendLine("current_tool_time: ", currentToolDuration)
		}
		if elapsed := toolDurationLabel(int64(jsonInt(payload, "elapsed_ms"))); elapsed != "" {
			appendLine("elapsed: ", elapsed)
		}
	}
	appendLine("status: ", jsonString(payload, "status"))

	started := jsonInt(payload, "tool_started")
	completed := jsonInt(payload, "tool_completed")
	failed := jsonInt(payload, "tool_failed")
	if (started > 0 || completed > 0 || failed > 0) && len(lines) < maxLines {
		lines = append(lines, clampEllipsis(
			fmt.Sprintf("tools: %d started, %d completed, %d failed", started, completed, failed),
			maxRunes,
		))
	}

	if len(lines) == 0 {
		return nil
	}
	return lines
}

func taskLaunchPreviewLine(payload map[string]any, maxRunes int) string {
	if payload == nil {
		return ""
	}
	idx := jsonInt(payload, "launch_index")
	if idx <= 0 {
		idx = jsonInt(payload, "index")
	}
	status := strings.TrimSpace(jsonString(payload, "status"))
	agent := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "resolved_agent_name"),
		jsonString(payload, "agent_type"),
		jsonString(payload, "subagent"),
		jsonString(payload, "requested_subagent"),
	))
	metaPrompt := strings.TrimSpace(jsonString(payload, "meta_prompt"))
	phase := strings.TrimSpace(jsonString(payload, "phase"))
	currentTool := strings.TrimSpace(jsonString(payload, "current_tool"))
	elapsed := toolDurationLabel(int64(jsonInt(payload, "elapsed_ms")))
	currentToolElapsed := toolDurationLabel(int64(jsonInt(payload, "current_tool_ms")))
	parts := make([]string, 0, 8)
	label := "-"
	if idx > 0 {
		label = fmt.Sprintf("%d.", idx)
	}
	parts = append(parts, label)
	if agent != "" {
		parts = append(parts, agent)
	}
	line := strings.Join(parts, " ")
	if metaPrompt != "" {
		line += " — " + metaPrompt
	}
	if status != "" {
		line += " [" + status + "]"
	}
	switch {
	case phase != "":
		line += " · " + phase
	case currentTool != "":
		toolPart := currentTool
		if currentToolElapsed != "" {
			toolPart += " (" + currentToolElapsed + ")"
		}
		line += " · " + toolPart
	case elapsed != "":
		line += " · total " + elapsed
	}
	return clampEllipsis(line, maxRunes)
}

type taskToolTableRow struct {
	Status             string
	Agent              string
	Tool               string
	Time               string
	PreviewKind        string
	PreviewText        string
	LaunchStartedAt    int64
	CurrentToolStarted int64
}

func taskToolTableRows(payload map[string]any, startedAt int64, state string) []taskToolTableRow {
	if payload == nil {
		return nil
	}
	launches := jsonObjectSlice(payload, "launches")
	if len(launches) > 0 {
		rows := make([]taskToolTableRow, 0, len(launches))
		for _, launch := range launches {
			row := taskToolLaunchRow(launch)
			if row == (taskToolTableRow{}) {
				continue
			}
			rows = append(rows, row)
		}
		if len(rows) > 0 {
			return rows
		}
	}
	row := taskToolPayloadRow(payload, startedAt, state)
	if row == (taskToolTableRow{}) {
		return nil
	}
	return []taskToolTableRow{row}
}

func taskToolLaunchRow(payload map[string]any) taskToolTableRow {
	if payload == nil {
		return taskToolTableRow{}
	}
	status := normalizeTaskToolStatus(jsonString(payload, "status"))
	agent := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "resolved_agent_name"),
		jsonString(payload, "requested_subagent_type"),
		jsonString(payload, "agent_type"),
		jsonString(payload, "subagent"),
		jsonString(payload, "requested_subagent"),
	))
	rawPreviewKind := strings.TrimSpace(jsonString(payload, "current_preview_kind"))
	toolName := strings.TrimSpace(jsonString(payload, "current_tool"))
	if toolName == "" && !strings.EqualFold(rawPreviewKind, "reasoning") {
		if history := jsonStringSlice(payload, "tool_order"); len(history) > 0 {
			toolName = strings.TrimSpace(history[len(history)-1])
		}
	}
	timeLabel := toolDurationLabel(int64(jsonInt(payload, "current_tool_ms")))
	if timeLabel == "" {
		timeLabel = toolDurationLabel(int64(jsonInt(payload, "elapsed_ms")))
	}
	previewText := strings.TrimSpace(jsonString(payload, "current_preview_text"))
	toolName, previewKind, previewText := normalizeTaskToolDisplay(toolName, rawPreviewKind, previewText)
	return taskToolTableRow{
		Status:             status,
		Agent:              emptyValue(agent, "subagent"),
		Tool:               emptyValue(toolName, "-"),
		Time:               timeLabel,
		PreviewKind:        previewKind,
		PreviewText:        previewText,
		LaunchStartedAt:    int64(jsonInt(payload, "launch_started_at_ms")),
		CurrentToolStarted: int64(jsonInt(payload, "current_tool_started_at_ms")),
	}
}

func taskToolPayloadRow(payload map[string]any, startedAt int64, state string) taskToolTableRow {
	if payload == nil {
		return taskToolTableRow{}
	}
	status := normalizeTaskToolStatus(firstNonEmptyToolValue(jsonString(payload, "status"), state))
	agent := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "resolved_agent_name"),
		jsonString(payload, "requested_subagent_type"),
		jsonString(payload, "agent_type"),
		jsonString(payload, "subagent"),
		jsonString(payload, "requested_subagent"),
	))
	rawPreviewKind := strings.TrimSpace(jsonString(payload, "current_preview_kind"))
	toolName := strings.TrimSpace(jsonString(payload, "current_tool"))
	timeLabel := toolDurationLabel(int64(jsonInt(payload, "current_tool_ms")))
	if timeLabel == "" {
		timeLabel = toolDurationLabel(int64(jsonInt(payload, "elapsed_ms")))
	}
	if timeLabel == "" && strings.EqualFold(status, "running") && startedAt > 0 {
		d := time.Since(time.UnixMilli(startedAt))
		if d < 0 {
			d = 0
		}
		timeLabel = formatDurationCompact(d)
	}
	previewText := strings.TrimSpace(jsonString(payload, "current_preview_text"))
	toolName, previewKind, previewText := normalizeTaskToolDisplay(toolName, rawPreviewKind, previewText)
	launchStartedAt := int64(jsonInt(payload, "launch_started_at_ms"))
	currentToolStarted := int64(jsonInt(payload, "current_tool_started_at_ms"))
	return taskToolTableRow{
		Status:             status,
		Agent:              emptyValue(agent, "subagent"),
		Tool:               emptyValue(toolName, "-"),
		Time:               timeLabel,
		PreviewKind:        previewKind,
		PreviewText:        previewText,
		LaunchStartedAt:    launchStartedAt,
		CurrentToolStarted: currentToolStarted,
	}
}

func normalizeTaskToolDisplay(toolName, previewKind, previewText string) (string, string, string) {
	toolName = strings.TrimSpace(toolName)
	previewKind = strings.ToLower(strings.TrimSpace(previewKind))
	previewText = strings.TrimSpace(previewText)
	switch previewKind {
	case "reasoning":
		return "thinking", "thinking", ""
	case "assistant":
		return toolName, previewKind, ""
	default:
		return toolName, previewKind, previewText
	}
}

func normalizeTaskToolStatus(value string) string {
	status := strings.ToLower(strings.TrimSpace(value))
	switch status {
	case "done", "ok", "success", "completed", "complete":
		return "done"
	case "error", "failed":
		return "error"
	case "running", "active", "in_progress":
		return "running"
	case "":
		return "pending"
	default:
		return status
	}
}

func taskToolStatusLabel(status string) string {
	switch normalizeTaskToolStatus(status) {
	case "done":
		return "OK"
	case "error":
		return "ER"
	case "running":
		return "RN"
	case "pending":
		return ".."
	default:
		status = strings.ToUpper(strings.TrimSpace(status))
		if utf8.RuneCountInString(status) > 2 {
			status = string([]rune(status)[:2])
		}
		return emptyValue(status, "..")
	}
}

func fitLeft(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = clampEllipsis(strings.TrimSpace(text), width)
	pad := width - utf8.RuneCountInString(text)
	if pad <= 0 {
		return text
	}
	return text + strings.Repeat(" ", pad)
}

func fitRight(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = clampEllipsis(strings.TrimSpace(text), width)
	pad := width - utf8.RuneCountInString(text)
	if pad <= 0 {
		return text
	}
	return strings.Repeat(" ", pad) + text
}

func toolPreviewLineLimit(entry chatToolStreamEntry) int {
	if isLiveBashToolEntry(entry) || strings.EqualFold(strings.TrimSpace(entry.ToolName), "bash") {
		return chatBashLivePreviewMaxLines
	}
	if lines := editToolPreviewLineCount(entry); lines > 0 {
		return lines
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "search") {
		return 4
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "manage_todos") {
		return 6
	}
	if isExitPlanToolEntry(entry) {
		return 5
	}
	if strings.EqualFold(strings.TrimSpace(entry.ToolName), "task") {
		payload := parseToolJSON(strings.TrimSpace(entry.Output))
		if payload == nil {
			payload = parseToolJSON(strings.TrimSpace(entry.Raw))
		}
		if launches := jsonObjectSlice(payload, "launches"); len(launches) > 0 {
			return maxInt(len(launches), jsonInt(payload, "launch_count"))
		}
	}
	return chatToolPreviewMaxLines
}

func structuredExitPlanPreviewLines(toolName string, payload map[string]any, maxRunes, maxLines int) []string {
	details, ok := parseExitPlanToolDetails(toolName, payload)
	if !ok || maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	pushExitPlanPreviewLine := func(text string) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || len(lines) >= maxLines {
			return
		}
		lines = append(lines, clampEllipsis(trimmed, maxRunes))
	}
	if title := strings.TrimSpace(details.Title); title != "" {
		pushExitPlanPreviewLine("title: " + title)
	}
	if planID := strings.TrimSpace(details.PlanID); planID != "" {
		pushExitPlanPreviewLine("plan: " + planID)
	}
	if targetMode := strings.TrimSpace(details.TargetMode); targetMode != "" {
		pushExitPlanPreviewLine("next mode: " + targetMode)
	}
	if message := strings.TrimSpace(details.UserMessage); message != "" {
		pushExitPlanPreviewLine("feedback: " + message)
	}
	if modification := strings.TrimSpace(details.RequestedModification); modification != "" {
		pushExitPlanPreviewLine("requested: " + modification)
	}
	return lines
}

type exitPlanToolDetails struct {
	Action                string
	Title                 string
	PlanID                string
	TargetMode            string
	UserMessage           string
	ApprovalState         string
	RequestedModification string
}

func parseExitPlanToolDetails(toolName string, payload map[string]any) (exitPlanToolDetails, bool) {
	if payload == nil {
		return exitPlanToolDetails{}, false
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	action := normalizeExitPlanAction(
		jsonString(payload, "status"),
		jsonString(payload, "approval_state"),
	)
	title := strings.TrimSpace(jsonString(payload, "title"))
	planID := strings.TrimSpace(jsonString(payload, "plan_id"))
	targetMode := strings.TrimSpace(jsonString(payload, "target_mode"))
	approvalState := normalizeExitPlanApprovalState(jsonString(payload, "approval_state"))
	message := normalizeExitPlanUserMessage(jsonString(payload, "user_message"))
	requestedModification := firstExitPlanRequestedModification(payload)

	if (name == "permission" || action == "") && isExitPlanPermissionPayload(payload) {
		permissionPayload := jsonObject(payload, "permission")
		toolPayload := jsonObject(payload, "tool")
		if action == "" {
			action = normalizeExitPlanAction(
				jsonString(permissionPayload, "status"),
				jsonString(permissionPayload, "decision"),
			)
		}
		if message == "" {
			message = normalizeExitPlanUserMessage(jsonString(permissionPayload, "reason"))
		}
		if args := parseToolJSON(jsonString(toolPayload, "arguments")); args != nil {
			if title == "" {
				title = strings.TrimSpace(jsonString(args, "title"))
			}
			if planID == "" {
				planID = strings.TrimSpace(firstNonEmptyToolValue(
					jsonString(args, "plan_id"),
					jsonString(args, "planID"),
				))
			}
		}
		approvalState = normalizeExitPlanApprovalState(firstNonEmptyToolValue(
			approvalState,
			jsonString(permissionPayload, "status"),
		))
	}

	if action == "" && title == "" && planID == "" && targetMode == "" && message == "" && approvalState == "" && requestedModification == "" {
		return exitPlanToolDetails{}, false
	}
	return exitPlanToolDetails{
		Action:                action,
		Title:                 title,
		PlanID:                planID,
		TargetMode:            targetMode,
		UserMessage:           message,
		ApprovalState:         approvalState,
		RequestedModification: requestedModification,
	}, true
}

func summarizeExitPlanToolPayload(toolName string, payload map[string]any) string {
	details, ok := parseExitPlanToolDetails(toolName, payload)
	if !ok {
		return ""
	}
	action := strings.TrimSpace(details.Action)
	if action == "" {
		action = "updated"
	}
	if title := strings.TrimSpace(details.Title); title != "" {
		return "exit_plan_mode " + action + " · " + title
	}
	return "exit_plan_mode " + action
}

func isExitPlanPermissionPayload(payload map[string]any) bool {
	toolPayload := jsonObject(payload, "tool")
	toolName := strings.ToLower(strings.TrimSpace(jsonString(toolPayload, "name")))
	return toolName == "exit_plan_mode" || toolName == "exit-plan-mode"
}

func isExitPlanToolEntry(entry chatToolStreamEntry) bool {
	toolName := strings.ToLower(strings.TrimSpace(entry.ToolName))
	if toolName == "exit_plan_mode" || toolName == "exit-plan-mode" {
		return true
	}
	if toolName != "permission" {
		return false
	}
	for _, raw := range []string{strings.TrimSpace(entry.Output), strings.TrimSpace(entry.Raw)} {
		if payload := parseToolJSON(raw); isExitPlanPermissionPayload(payload) {
			return true
		}
	}
	return false
}

func firstExitPlanRequestedModification(payload map[string]any) string {
	items := jsonStringSlice(payload, "requested_modifications")
	for _, item := range items {
		if text := strings.TrimSpace(item); text != "" {
			return text
		}
	}
	return ""
}

func normalizeExitPlanAction(values ...string) string {
	for _, raw := range values {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		switch normalized {
		case "approved", "approve", "allow", "allowed", "yes":
			return "approved"
		case "denied", "deny", "rejected", "reject", "no", "not_in_plan_mode":
			return "denied"
		case "cancelled", "canceled", "cancel":
			return "cancelled"
		case "submitted", "pending_review":
			return "pending_review"
		case "error", "failed", "failure":
			return "error"
		}
	}
	return ""
}

func normalizeExitPlanApprovalState(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return value
}

func normalizeExitPlanUserMessage(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "approved by user", "approved", "allow", "allowed", "yes", "denied by user", "denied", "deny", "rejected", "reject", "no", "cancelled", "canceled", "not in plan mode":
		return ""
	default:
		return trimmed
	}
}

func jsonObject(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok {
		return nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return typed
}

func jsonObjectSlice(payload map[string]any, key string) []map[string]any {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		typed, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, typed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func jsonStringSlice(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func editPayloadPreviewItems(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	if items := jsonObjectSlice(payload, "edits"); len(items) > 0 {
		return items
	}
	oldPreview := strings.TrimSpace(jsonString(payload, "old_string_preview"))
	newPreview := strings.TrimSpace(jsonString(payload, "new_string_preview"))
	if oldPreview == "" && newPreview == "" {
		return nil
	}
	return []map[string]any{{
		"old_string_preview":   oldPreview,
		"new_string_preview":   newPreview,
		"old_string_truncated": jsonBool(payload, "old_string_truncated"),
		"new_string_truncated": jsonBool(payload, "new_string_truncated"),
	}}
}

func firstNonEmptyToolValue(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func editToolPreviewLines(entry chatToolStreamEntry, maxRunes, maxLines int) []string {
	_ = maxRunes
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil || !isEditPayload(entry, payload) {
		return nil
	}

	items := editPayloadPreviewItems(payload)
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, editPayloadPreviewLineCount(payload))
	for _, item := range items {
		oldPreview := strings.TrimSpace(jsonString(item, "old_string_preview"))
		newPreview := strings.TrimSpace(jsonString(item, "new_string_preview"))
		oldTruncated := jsonBool(item, "old_string_truncated")
		newTruncated := jsonBool(item, "new_string_truncated")
		if oldPreview == "" {
			oldPreview = "(empty)"
		}
		if newPreview == "" {
			newPreview = "(empty)"
		}
		for _, line := range expandEditPreviewLines(oldPreview, oldTruncated) {
			lines = append(lines, "-"+line)
			if maxLines > 0 && len(lines) >= maxLines {
				return lines[:maxLines]
			}
		}
		for _, line := range expandEditPreviewLines(newPreview, newTruncated) {
			lines = append(lines, "+"+line)
			if maxLines > 0 && len(lines) >= maxLines {
				return lines[:maxLines]
			}
		}
	}
	return lines
}

func expandEditPreviewLines(value string, truncated bool) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	// Edit payload previews may contain literal escape sequences from transport.
	value = strings.ReplaceAll(value, "\\n", "\n")
	value = strings.ReplaceAll(value, "\\t", "\t")
	value = strings.TrimRight(value, "\n")
	if value == "" {
		value = "(empty)"
	}
	lines := strings.Split(value, "\n")
	if len(lines) == 0 {
		lines = []string{"(empty)"}
	}
	if truncated {
		last := len(lines) - 1
		lines[last] = lines[last] + " ..."
	}
	return lines
}

func summarizeEditPreviewLine(lines []string) string {
	if len(lines) == 0 {
		return "(empty)"
	}
	if len(lines) == 1 {
		return lines[0]
	}
	first := lines[0]
	if strings.TrimSpace(first) == "" {
		first = "(blank)"
	}
	return first + " ..."
}

func editToolPreviewLineCount(entry chatToolStreamEntry) int {
	payload := parseToolJSON(strings.TrimSpace(entry.Output))
	if payload == nil || !isEditPayload(entry, payload) {
		return 0
	}
	return editPayloadPreviewLineCount(payload)
}

func editPayloadPreviewLineCount(payload map[string]any) int {
	items := editPayloadPreviewItems(payload)
	if len(items) == 0 {
		return 0
	}
	count := 0
	for _, item := range items {
		oldPreview := strings.TrimSpace(jsonString(item, "old_string_preview"))
		newPreview := strings.TrimSpace(jsonString(item, "new_string_preview"))
		oldTruncated := jsonBool(item, "old_string_truncated")
		newTruncated := jsonBool(item, "new_string_truncated")
		if oldPreview == "" {
			oldPreview = "(empty)"
		}
		if newPreview == "" {
			newPreview = "(empty)"
		}
		count += len(expandEditPreviewLines(oldPreview, oldTruncated))
		count += len(expandEditPreviewLines(newPreview, newTruncated))
	}
	return count
}

func summarizeStructuredToolPreview(toolName, raw string) string {
	payload := parseToolJSON(raw)
	if payload == nil {
		return ""
	}

	switch toolName {
	case "read":
		return summarizeReadToolPayload(payload)
	case "write":
		path := jsonString(payload, "path")
		written := jsonInt(payload, "bytes_written")
		appendMode := jsonBool(payload, "append")
		if path == "" && written <= 0 {
			return ""
		}
		action := "write"
		if appendMode {
			action = "append"
		}
		if written > 0 {
			return fmt.Sprintf("%s %s (%d bytes)", action, path, written)
		}
		return fmt.Sprintf("%s %s", action, path)
	case "bash":
		if summary := summarizeBashPermissionPayload(toolName, payload); summary != "" {
			return summary
		}
		command := jsonString(payload, "command")
		exitCode := jsonInt(payload, "exit_code")
		timedOut := jsonBool(payload, "timed_out")
		truncated := jsonBool(payload, "truncated")
		summary := "bash"
		if command != "" {
			summary += " " + sanitizeCommandSnippetPreview(clampEllipsis(command, 80))
		}
		notes := []string{"output in timeline"}
		switch {
		case timedOut:
			notes = append(notes, "timed out")
		case exitCode != 0:
			notes = append(notes, "failed")
		}
		if truncated {
			notes = append(notes, "partial output")
		}
		return toolSummaryWithNotes(summary, notes...)
	case "websearch":
		return summarizeWebSearchToolPayload(payload, chatToolPreviewMaxRunes)
	case "search":
		return summarizeSearchToolPayload(payload, chatToolPreviewMaxRunes)
	case "list":
		return summarizeListToolPayload(payload, chatToolPreviewMaxRunes)
	case "webfetch":
		return summarizeWebFetchToolPayload(payload, chatToolPreviewMaxRunes)
	case "glob":
		pattern := jsonString(payload, "pattern")
		root := jsonString(payload, "path")
		count := jsonInt(payload, "count")
		truncated := jsonBool(payload, "truncated")
		timedOut := jsonBool(payload, "timed_out")
		if pattern == "" && count <= 0 {
			return ""
		}
		summary := "glob"
		if pattern != "" {
			summary += " " + fmt.Sprintf("%q", clampEllipsis(pattern, 80))
		}
		if root != "" {
			summary += " in " + root
		}
		notes := []string{toolCountLabel(count, "file", "files")}
		if timedOut {
			notes = append(notes, "timed out")
		} else if truncated {
			notes = append(notes, "partial results")
		}
		return toolSummaryWithNotes(summary, notes...)
	case "grep":
		pattern := jsonString(payload, "pattern")
		root := jsonString(payload, "path")
		count := jsonInt(payload, "count")
		truncated := jsonBool(payload, "truncated")
		timedOut := jsonBool(payload, "timed_out")
		if pattern == "" && count <= 0 {
			return ""
		}
		summary := "grep"
		if pattern != "" {
			summary += " " + fmt.Sprintf("%q", clampEllipsis(pattern, 80))
		}
		if root != "" {
			summary += " in " + root
		}
		flags := []string{toolCountLabel(count, "match", "matches")}
		if timedOut {
			flags = append(flags, "timed out")
		} else if truncated {
			flags = append(flags, "partial results")
		}
		return toolSummaryWithNotes(summary, flags...)
	case "task":
		return summarizeTaskToolPayload(payload)
	case "manage_todos":
		return summarizeManageTodosToolPayload(payload)
	case "ask-user", "ask_user":
		rows := askUserSummaryRows(payload)
		if len(rows) > 0 {
			return fmt.Sprintf("ask-user responses (%d)", len(rows))
		}
		question := jsonString(payload, "question")
		if question != "" {
			return "ask-user " + clampEllipsis(question, 80)
		}
		summary := jsonString(payload, "summary")
		if summary != "" {
			return summary
		}
		return "ask-user"
	case "exit-plan-mode", "exit_plan_mode", "permission":
		if summary := summarizeExitPlanToolPayload(toolName, payload); summary != "" {
			return summary
		}
		return ""
	default:
		return ""
	}
}

func summarizeBashPermissionPayload(toolName string, payload map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(toolName), "bash") || payload == nil {
		return ""
	}
	permissionPayload := jsonObject(payload, "permission")
	if len(permissionPayload) == 0 {
		return ""
	}
	toolPayload := jsonObject(payload, "tool")
	if len(toolPayload) == 0 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(jsonString(toolPayload, "name")), "bash") {
		return ""
	}
	action := normalizePermissionAction(
		jsonString(permissionPayload, "status"),
		jsonString(permissionPayload, "decision"),
	)
	if action == "" {
		action = "updated"
	}
	command := ""
	if args := parseToolJSON(jsonString(toolPayload, "arguments")); args != nil {
		command = strings.TrimSpace(jsonString(args, "command"))
	}
	summary := "bash " + action
	if command != "" {
		summary += " · " + sanitizeCommandSnippetPreview(clampEllipsis(command, 80))
	}
	return summary
}

func normalizePermissionAction(values ...string) string {
	for _, raw := range values {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		switch normalized {
		case "approved", "approve", "allow", "allowed", "allow_once", "allow_always", "yes":
			return "approved"
		case "denied", "deny", "deny_once", "deny_always", "blocked", "rejected", "reject", "no":
			return "denied"
		case "cancelled", "canceled", "cancel":
			return "cancelled"
		case "error", "failed", "failure":
			return "error"
		}
	}
	return ""
}

func summarizeManageTodosToolPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	action := strings.TrimSpace(jsonString(payload, "action"))
	ownerKind := strings.TrimSpace(jsonString(payload, "owner_kind"))
	ownerSuffix := ""
	if ownerKind != "" {
		ownerSuffix = " [" + ownerKind + "]"
	}
	summary := "manage_todos" + ownerSuffix
	switch action {
	case "list", "summary", "create", "update", "delete", "delete_done", "delete_all", "reorder", "in_progress", "batch":
		summary += " " + action
	}
	notes := make([]string, 0, 3)
	if summaryPayload := jsonObject(payload, "summary"); len(summaryPayload) > 0 {
		openCount := jsonInt(summaryPayload, "open_count")
		taskCount := jsonInt(summaryPayload, "task_count")
		inProgressCount := jsonInt(summaryPayload, "in_progress_count")
		if taskCount > 0 || openCount > 0 || inProgressCount > 0 {
			notes = append(notes, fmt.Sprintf("%d open · %d total", openCount, taskCount))
			if inProgressCount > 0 {
				notes = append(notes, fmt.Sprintf("%d in progress", inProgressCount))
			}
		}
	}
	if action == "batch" {
		count := maxInt(len(jsonObjectSlice(payload, "operations")), jsonInt(payload, "operation_count"))
		if count > 0 {
			notes = append([]string{fmt.Sprintf("%d ops", count)}, notes...)
		}
		if len(notes) > 0 {
			return toolSummaryWithNotes(summary, notes...)
		}
		return summary
	}
	if item := jsonObject(payload, "item"); len(item) > 0 {
		text := strings.TrimSpace(jsonString(item, "text"))
		if text != "" {
			notes = append(notes, clampEllipsis(text, 80))
			return toolSummaryWithNotes(summary, notes...)
		}
	}
	if id := strings.TrimSpace(jsonString(payload, "id")); id != "" {
		notes = append(notes, id)
		return toolSummaryWithNotes(summary, notes...)
	}
	if len(notes) > 0 {
		return toolSummaryWithNotes(summary, notes...)
	}
	return summary
}

func summarizeTaskToolPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	status := strings.TrimSpace(jsonString(payload, "status"))
	description := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "goal"),
		jsonString(payload, "description"),
	))
	launches := jsonObjectSlice(payload, "launches")
	launchCount := maxInt(len(launches), jsonInt(payload, "launch_count"))
	successCount := jsonInt(payload, "success_count")
	failedCount := jsonInt(payload, "failed_count")
	if launchCount > 1 {
		parts := make([]string, 0, 4)
		if description != "" {
			parts = append(parts, description)
		}
		parts = append(parts, fmt.Sprintf("(%d launches)", launchCount))
		if successCount > 0 || failedCount > 0 {
			parts = append(parts, fmt.Sprintf("(%d ok", successCount)+func() string {
				if failedCount > 0 {
					return fmt.Sprintf(", %d failed)", failedCount)
				}
				return ")"
			}())
		} else if status != "" {
			parts = append(parts, "("+status+")")
		}
		return clampEllipsis("task "+strings.Join(parts, " "), chatToolPreviewMaxRunes)
	}
	agentType := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(payload, "resolved_agent_name"),
		jsonString(payload, "agent_type"),
		jsonString(payload, "subagent"),
	))
	if status == "" && agentType == "" && description == "" {
		return "task"
	}
	parts := make([]string, 0, 3)
	if description != "" {
		parts = append(parts, description)
	}
	if agentType != "" {
		parts = append(parts, "@"+agentType)
	}
	if status != "" {
		parts = append(parts, "("+status+")")
	}
	return clampEllipsis("task "+strings.Join(parts, " "), chatToolPreviewMaxRunes)
}

func summarizeReadToolPayload(payload map[string]any) string {
	path := jsonString(payload, "path")
	lineStart := jsonInt(payload, "line_start")
	count := jsonInt(payload, "count")
	bytes := jsonInt(payload, "bytes")
	truncated := jsonBool(payload, "truncated")
	binarySuppressed := jsonBool(payload, "binary_suppressed")

	if path == "" && lineStart <= 0 && count <= 0 && bytes <= 0 && !truncated && !binarySuppressed {
		return ""
	}

	summary := "read"
	if path != "" {
		summary += " " + path
	}

	if count > 0 {
		if lineStart <= 0 {
			lineStart = 1
		}
		lineEnd := lineStart + count - 1
		if count == 1 {
			summary += fmt.Sprintf(" (line %d", lineStart)
		} else {
			summary += fmt.Sprintf(" (lines %d-%d", lineStart, lineEnd)
		}
		if truncated {
			summary += ", partial"
		}
		if binarySuppressed {
			summary += ", binary output hidden"
		}
		return summary + ")"
	}

	if count == 0 {
		summary += " 0 lines"
		flags := make([]string, 0, 2)
		if truncated {
			flags = append(flags, "partial")
		}
		if binarySuppressed {
			flags = append(flags, "binary output hidden")
		}
		if len(flags) > 0 {
			summary += " (" + strings.Join(flags, ", ") + ")"
		}
		return summary
	}

	if bytes > 0 {
		summary += fmt.Sprintf(" (%d bytes", bytes)
		if truncated {
			summary += ", partial"
		}
		if binarySuppressed {
			summary += ", binary output hidden"
		}
		return summary + ")"
	}

	if truncated || binarySuppressed {
		flags := make([]string, 0, 2)
		if truncated {
			flags = append(flags, "partial")
		}
		if binarySuppressed {
			flags = append(flags, "binary output hidden")
		}
		summary += " (" + strings.Join(flags, ", ") + ")"
	}

	return summary
}

func sanitizeCommandSnippetPreview(snippet string) string {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return ""
	}
	if !strings.HasSuffix(snippet, "...") {
		return snippet
	}
	for _, quote := range []string{"'", "\"", "`"} {
		if strings.Count(snippet, quote)%2 != 0 {
			if idx := strings.LastIndex(snippet, quote); idx >= 0 {
				snippet = strings.TrimSpace(snippet[:idx] + snippet[idx+len(quote):])
			}
		}
	}
	return strings.TrimSpace(snippet)
}

func jsonStringMap(payload map[string]any, key string) map[string]string {
	value, ok := payload[key]
	if !ok {
		return nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(typed))
	for rawKey, rawValue := range typed {
		mapKey := strings.TrimSpace(rawKey)
		if mapKey == "" {
			continue
		}
		switch current := rawValue.(type) {
		case string:
			if text := strings.TrimSpace(current); text != "" {
				out[mapKey] = text
			}
		case fmt.Stringer:
			if text := strings.TrimSpace(current.String()); text != "" {
				out[mapKey] = text
			}
		default:
			text := strings.TrimSpace(fmt.Sprintf("%v", current))
			if text != "" {
				out[mapKey] = text
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseToolJSON(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func preferredStructuredToolText(toolName, output, raw string) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	output = strings.TrimSpace(output)
	raw = strings.TrimSpace(raw)
	switch toolName {
	case "websearch", "search", "manage_todos":
		switch {
		case output == "":
			return raw
		case raw == "":
			return output
		}
		outputPayload := parseToolJSON(output)
		rawPayload := parseToolJSON(raw)
		switch {
		case outputPayload == nil:
			return raw
		case rawPayload == nil:
			return output
		}
		if structuredPayloadRichness(toolName, rawPayload) > structuredPayloadRichness(toolName, outputPayload) {
			return raw
		}
		return output
	default:
		if toolName == "bash" {
			switch {
			case raw == "":
				return output
			case output == "":
				return raw
			}
			if rawPayload := parseToolJSON(raw); rawPayload != nil {
				return raw
			}
			if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
				return output
			}
			return raw
		}
		if output != "" {
			return output
		}
		return raw
	}
}

func structuredPayloadRichness(toolName string, payload map[string]any) int {
	if payload == nil {
		return -1
	}
	score := 0
	switch toolName {
	case "websearch":
		score += jsonInt(payload, "query_count") * 10
		score += jsonInt(payload, "total_results") * 4
		for _, queryPayload := range webSearchResultQueryPayloads(payload) {
			score += 10
			score += jsonInt(queryPayload, "count") * 4
			score += len(jsonObjectSlice(queryPayload, "results")) * 2
			if strings.TrimSpace(jsonString(queryPayload, "error")) != "" {
				score++
			}
		}
		if jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") {
			score -= 25
		}
	case "search":
		score += jsonInt(payload, "query_count") * 10
		score += jsonInt(payload, "count") * 4
		score += jsonInt(payload, "total_matched") * 2
		score += len(searchResultQueryPayloads(payload)) * 10
		score += len(jsonObjectSlice(payload, "matches")) * 2
		score += len(jsonObjectSlice(payload, "files")) * 2
		if jsonBool(payload, "details_truncated") || jsonBool(payload, "truncated_queries") || jsonBool(payload, "truncated") {
			score -= 25
		}
	case "manage_todos":
		score += len(jsonObjectSlice(payload, "items")) * 8
		score += len(jsonObjectSlice(payload, "results")) * 4
		if item := jsonObject(payload, "item"); len(item) > 0 {
			score += 8
		}
		if summary := jsonObject(payload, "summary"); len(summary) > 0 {
			score += 4
			score += jsonInt(summary, "task_count")
		}
	}
	if score == 0 {
		score = len([]rune(strings.TrimSpace(firstNonEmptyToolValue(
			jsonString(payload, "summary"),
			jsonString(payload, "path_id"),
		))))
	}
	return score
}

func jsonString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	typed, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(typed)
}

func jsonBool(payload map[string]any, key string) bool {
	value, ok := payload[key]
	if !ok {
		return false
	}
	typed, ok := value.(bool)
	if !ok {
		return false
	}
	return typed
}

func jsonInt(payload map[string]any, key string) int {
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	default:
		return 0
	}
}

func shouldSuppressHistoricalToolEntry(entry chatToolStreamEntry) bool {
	if payload := parseToolJSON(strings.TrimSpace(entry.Output)); isHistoricalPermissionGatePayload(payload) {
		return true
	}
	if payload := parseToolJSON(strings.TrimSpace(entry.Raw)); isHistoricalPermissionGatePayload(payload) {
		return true
	}
	return false
}

func isHistoricalPermissionGatePayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	permissionPayload := jsonObject(payload, "permission")
	if len(permissionPayload) == 0 {
		return false
	}
	if _, hasApproved := permissionPayload["approved"]; !hasApproved {
		if strings.TrimSpace(jsonString(permissionPayload, "status")) == "" &&
			strings.TrimSpace(jsonString(permissionPayload, "decision")) == "" {
			return false
		}
	}
	toolPayload := jsonObject(payload, "tool")
	if len(toolPayload) == 0 {
		return false
	}
	toolName := strings.TrimSpace(jsonString(toolPayload, "name"))
	if toolName == "" {
		return strings.TrimSpace(jsonString(toolPayload, "arguments")) != ""
	}
	if strings.EqualFold(toolName, "bash") {
		return false
	}
	return true
}
