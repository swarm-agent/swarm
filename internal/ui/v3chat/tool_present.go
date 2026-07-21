package v3chat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxBashPresentationLines    = 8
	maxEditPresentationLines    = 14
	maxGenericPresentationLines = 6
	maxSearchPresentationFiles  = 10
)

type toolPresentationLine struct {
	Text string
	Tone string
}

type toolPresentation struct {
	Summary string
	Lines   []toolPresentationLine
	Kind    string
}

func buildToolPresentation(tool ToolTimelineItem) toolPresentation {
	name := normalizeToolDisplayName(tool.Name)
	arguments := parseToolObject(tool.Arguments)
	output := parseToolObject(tool.Output)

	var presentation toolPresentation
	switch name {
	case "read":
		presentation = presentReadTool(arguments, output)
	case "write":
		presentation = presentWriteTool(arguments, output)
	case "edit":
		presentation = presentEditTool(arguments, output)
	case "bash":
		presentation = presentBashTool(tool, arguments, output)
	case "search":
		presentation = presentSearchTool(arguments, output)
	case "list":
		presentation = presentListTool(arguments, output)
	case "websearch", "webfetch", "webdownload":
		presentation = presentWebTool(name, arguments, output)
	case "plan-manage":
		presentation = presentPlanManageTool(tool, arguments, output)
	default:
		presentation = presentGenericTool(name, tool.Output, arguments, output)
	}
	if strings.TrimSpace(presentation.Summary) == "" {
		presentation.Summary = name
	}
	return presentation
}

func normalizeToolDisplayName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	if name == "" {
		return "tool"
	}
	return name
}

func parseToolObject(value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "{") {
		return nil
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return nil
	}
	return decoded
}

func effectiveToolObject(arguments, output map[string]any) map[string]any {
	if output != nil {
		return output
	}
	return arguments
}

func presentReadTool(arguments, output map[string]any) toolPresentation {
	effective := effectiveToolObject(arguments, output)
	path := firstToolString(output, arguments, "path")
	summary := "read"
	if path != "" {
		summary += " " + path
	}
	count := toolInt(effective, "count")
	lineStart := toolInt(effective, "line_start")
	if lineStart <= 0 {
		lineStart = toolInt(arguments, "line_start")
	}
	if lineStart <= 0 {
		lineStart = 1
	}
	facts := make([]string, 0, 3)
	if count > 0 {
		if count == 1 {
			facts = append(facts, fmt.Sprintf("line %d", lineStart))
		} else {
			facts = append(facts, fmt.Sprintf("lines %d–%d", lineStart, lineStart+count-1))
		}
	} else if output == nil {
		if maxLines := toolInt(arguments, "max_lines"); maxLines > 0 {
			facts = append(facts, fmt.Sprintf("from line %d", lineStart))
		}
	}
	if bytes := toolInt(effective, "bytes"); bytes > 0 {
		facts = append(facts, formatToolBytes(bytes))
	}
	if toolBool(effective, "binary_suppressed") {
		facts = append(facts, "binary hidden")
	} else if toolBool(effective, "truncated") || toolBool(effective, "details_truncated") {
		facts = append(facts, "partial")
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts)}
}

func presentWriteTool(arguments, output map[string]any) toolPresentation {
	effective := effectiveToolObject(arguments, output)
	appendMode := toolBool(effective, "append") || toolBool(arguments, "append")
	action := "write"
	if appendMode {
		action = "append"
	}
	path := firstToolString(output, arguments, "path")
	summary := action
	if path != "" {
		summary += " " + path
	}
	facts := make([]string, 0, 1)
	if bytes := toolInt(effective, "bytes_written"); bytes > 0 {
		facts = append(facts, formatToolBytes(bytes))
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts)}
}

func presentEditTool(arguments, output map[string]any) toolPresentation {
	path := firstToolString(output, arguments, "path")
	summary := "edit"
	if path != "" {
		summary += " " + path
	}
	facts := make([]string, 0, 2)
	if replacements := toolInt(output, "replacements"); replacements > 0 {
		facts = append(facts, toolCountLabel(replacements, "replacement", "replacements"))
	}
	if editCount := toolInt(output, "edit_count"); editCount > 1 {
		facts = append(facts, toolCountLabel(editCount, "edit", "edits"))
	}

	lines := make([]toolPresentationLine, 0, maxEditPresentationLines)
	hunks := toolObjectSlice(output, "edits")
	if len(hunks) == 0 && output != nil && (toolStringRaw(output, "old_string_preview") != "" || toolStringRaw(output, "new_string_preview") != "") {
		hunks = []map[string]any{output}
	}
	for index, hunk := range hunks {
		if len(lines) >= maxEditPresentationLines {
			break
		}
		if len(hunks) > 1 {
			lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("edit %d", index+1), Tone: "label"})
		}
		appendEditPreviewLines(&lines, toolStringRaw(hunk, "old_string_preview"), "− ", "removed")
		appendEditPreviewLines(&lines, toolStringRaw(hunk, "new_string_preview"), "+ ", "added")
	}
	if len(lines) >= maxEditPresentationLines && len(hunks) > 0 {
		lines[maxEditPresentationLines-1] = toolPresentationLine{Text: "… diff clipped", Tone: "muted"}
		lines = lines[:maxEditPresentationLines]
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines}
}

func appendEditPreviewLines(lines *[]toolPresentationLine, preview, prefix, tone string) {
	preview = normalizeToolText(preview)
	if preview == "" {
		return
	}
	for _, line := range strings.Split(preview, "\n") {
		if len(*lines) >= maxEditPresentationLines {
			return
		}
		*lines = append(*lines, toolPresentationLine{Text: prefix + line, Tone: tone})
	}
}

func presentBashTool(tool ToolTimelineItem, arguments, output map[string]any) toolPresentation {
	command := firstToolString(output, arguments, "command")
	summary := "bash"
	facts := make([]string, 0, 3)
	if output != nil && toolHasKey(output, "exit_code") {
		facts = append(facts, fmt.Sprintf("exit %d", toolInt(output, "exit_code")))
	}
	if toolBool(output, "timed_out") {
		facts = append(facts, "timed out")
	}
	if toolBool(output, "truncated") {
		facts = append(facts, "partial output")
	}
	if toolBool(output, "binary_suppressed") {
		facts = append(facts, "binary hidden")
	}

	lines := make([]toolPresentationLine, 0, maxBashPresentationLines+2)
	if command != "" {
		lines = append(lines, toolPresentationLine{Text: "$ " + command, Tone: "command"})
	}
	rawOutput := ""
	if output != nil {
		rawOutput = firstNonEmptyToolRaw(
			toolStringRaw(output, "output"),
			toolStringRaw(output, "stdout"),
			toolStringRaw(output, "stderr"),
		)
	}
	if rawOutput == "" && (output == nil || !looksLikeTerminalBashPayload(output)) {
		rawOutput = tool.Output
	}
	lines = append(lines, boundedTailToolLines(rawOutput, maxBashPresentationLines)...)
	if len(lines) == 0 && strings.EqualFold(strings.TrimSpace(tool.Status), "running") {
		lines = append(lines, toolPresentationLine{Text: "waiting for output…", Tone: "muted"})
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines}
}

func looksLikeTerminalBashPayload(payload map[string]any) bool {
	return toolHasKey(payload, "exit_code") || toolHasKey(payload, "path_id") || toolHasKey(payload, "timed_out")
}

func boundedTailToolLines(value string, limit int) []toolPresentationLine {
	value = normalizeToolText(value)
	if value == "" || limit <= 0 {
		return nil
	}
	parts := strings.Split(value, "\n")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	omitted := 0
	if len(parts) > limit {
		omitted = len(parts) - limit
		parts = parts[len(parts)-limit:]
	}
	lines := make([]toolPresentationLine, 0, len(parts)+1)
	if omitted > 0 {
		lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("… %d earlier lines", omitted), Tone: "muted"})
	}
	for _, part := range parts {
		lines = append(lines, toolPresentationLine{Text: part, Tone: "code"})
	}
	return lines
}

type searchPresentationFile struct {
	Path    string
	Lines   []int
	Matches int
	seen    map[int]struct{}
}

func presentSearchTool(arguments, output map[string]any) toolPresentation {
	files := collectSearchPresentationFiles(output)
	query := toolString(arguments, "query")
	queries := toolStringSlice(arguments, "queries")
	queryCount := len(queries)
	if queryCount == 0 && query != "" {
		queryCount = 1
	}
	if count := len(toolObjectSlice(output, "query_results")); count > queryCount {
		queryCount = count
	}

	summary := "search"
	if queryCount == 1 {
		if query == "" && len(queries) == 1 {
			query = queries[0]
		}
		if query != "" {
			summary += " " + strconv.Quote(clampToolRunes(query, 72))
		}
	}
	facts := make([]string, 0, 4)
	if queryCount > 1 {
		facts = append(facts, toolCountLabel(queryCount, "query", "queries"))
	}
	count := toolInt(output, "count")
	mode := strings.ToLower(toolString(output, "search_mode"))
	if count > 0 || toolHasKey(output, "count") {
		if mode == "files" {
			facts = append(facts, toolCountLabel(count, "file", "files"))
		} else {
			facts = append(facts, toolCountLabel(count, "match", "matches"))
		}
	}
	if len(files) > 0 && mode != "files" {
		facts = append(facts, fmt.Sprintf("%d %s", len(files), map[bool]string{true: "file", false: "files"}[len(files) == 1]))
	}
	if toolBool(output, "timed_out") {
		facts = append(facts, "timed out")
	} else if toolBool(output, "truncated") || toolBool(output, "details_truncated") {
		facts = append(facts, "partial")
	}

	lines := make([]toolPresentationLine, 0, minInt(len(files), maxSearchPresentationFiles)+1)
	visible := files
	if len(visible) > maxSearchPresentationFiles {
		visible = visible[:maxSearchPresentationFiles]
	}
	for _, file := range visible {
		fileFacts := make([]string, 0, 2)
		if len(file.Lines) > 0 {
			fileFacts = append(fileFacts, "lines "+compactSearchLineNumbers(file.Lines, 6))
		}
		if file.Matches > 0 {
			fileFacts = append(fileFacts, toolCountLabel(file.Matches, "match", "matches"))
		}
		lines = append(lines, toolPresentationLine{Text: appendToolFacts(file.Path, fileFacts), Tone: "path"})
	}
	if remaining := len(files) - len(visible); remaining > 0 {
		lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("… %d more files", remaining), Tone: "muted"})
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines}
}

func collectSearchPresentationFiles(output map[string]any) []searchPresentationFile {
	if output == nil {
		return nil
	}
	order := make([]string, 0)
	byPath := make(map[string]*searchPresentationFile)
	add := func(path string, line int, matches int) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		file := byPath[path]
		if file == nil {
			file = &searchPresentationFile{Path: path, seen: make(map[int]struct{})}
			byPath[path] = file
			order = append(order, path)
		}
		if line > 0 {
			if _, ok := file.seen[line]; !ok {
				file.seen[line] = struct{}{}
				file.Lines = append(file.Lines, line)
			}
		}
		if matches > 0 {
			file.Matches += matches
		} else {
			file.Matches++
		}
	}
	for _, group := range toolObjectSlice(output, "results") {
		path := firstNonEmptyToolRaw(toolString(group, "relative_path"), toolString(group, "path"))
		items := toolObjectSlice(group, "items")
		if len(items) == 0 {
			add(path, 0, maxInt(1, toolInt(group, "count")))
			continue
		}
		for _, item := range items {
			add(path, toolInt(item, "line"), 1)
		}
	}
	for _, item := range toolObjectSlice(output, "matches") {
		path := firstNonEmptyToolRaw(toolString(item, "relative_path"), toolString(item, "path"))
		add(path, toolInt(item, "line"), 1)
	}
	for _, item := range toolObjectSlice(output, "files") {
		path := firstNonEmptyToolRaw(toolString(item, "relative_path"), toolString(item, "path"))
		add(path, 0, maxInt(1, toolInt(item, "count")))
	}
	files := make([]searchPresentationFile, 0, len(order))
	for _, path := range order {
		file := byPath[path]
		sort.Ints(file.Lines)
		files = append(files, *file)
	}
	return files
}

func compactSearchLineNumbers(lines []int, limit int) string {
	if len(lines) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 1
	}
	visible := lines
	if len(visible) > limit {
		visible = visible[:limit]
	}
	parts := make([]string, 0, len(visible)+1)
	for _, line := range visible {
		parts = append(parts, strconv.Itoa(line))
	}
	if remaining := len(lines) - len(visible); remaining > 0 {
		parts = append(parts, fmt.Sprintf("+%d", remaining))
	}
	return strings.Join(parts, ", ")
}

func presentListTool(arguments, output map[string]any) toolPresentation {
	effective := effectiveToolObject(arguments, output)
	path := firstToolString(output, arguments, "path")
	summary := "list"
	if path != "" {
		summary += " " + path
	}
	facts := make([]string, 0, 3)
	if count := toolInt(effective, "count"); count > 0 || toolHasKey(effective, "count") {
		facts = append(facts, toolCountLabel(count, "entry", "entries"))
	}
	if toolBool(effective, "truncated") {
		facts = append(facts, "partial")
	}
	entries := toolObjectSlice(output, "entries")
	lines := make([]toolPresentationLine, 0, minInt(len(entries), maxGenericPresentationLines)+1)
	visible := entries
	if len(visible) > maxGenericPresentationLines {
		visible = visible[:maxGenericPresentationLines]
	}
	for _, entry := range visible {
		entryPath := firstNonEmptyToolRaw(toolString(entry, "relative_path"), toolString(entry, "path"))
		if toolString(entry, "type") == "dir" && entryPath != "" && !strings.HasSuffix(entryPath, "/") {
			entryPath += "/"
		}
		if entryPath != "" {
			lines = append(lines, toolPresentationLine{Text: entryPath, Tone: "path"})
		}
	}
	if remaining := len(entries) - len(visible); remaining > 0 {
		lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("… %d more entries", remaining), Tone: "muted"})
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines}
}

func presentWebTool(name string, arguments, output map[string]any) toolPresentation {
	summary := toolString(output, "summary")
	if summary == "" {
		summary = name
		if query := firstNonEmptyToolRaw(toolString(arguments, "query"), firstToolString(output, arguments, "url")); query != "" {
			summary += " " + strconv.Quote(clampToolRunes(query, 72))
		}
	}
	lines := make([]toolPresentationLine, 0, maxGenericPresentationLines)
	appendResult := func(item map[string]any) {
		if len(lines) >= maxGenericPresentationLines {
			return
		}
		title := firstNonEmptyToolRaw(toolString(item, "title"), toolString(item, "name"), toolString(item, "url"))
		url := toolString(item, "url")
		if title == "" {
			return
		}
		if url != "" && url != title {
			title += " · " + url
		}
		lines = append(lines, toolPresentationLine{Text: title, Tone: "path"})
	}
	for _, group := range toolObjectSlice(output, "results") {
		nested := toolObjectSlice(group, "results")
		if len(nested) == 0 {
			appendResult(group)
			continue
		}
		for _, item := range nested {
			appendResult(item)
		}
	}
	return toolPresentation{Summary: summary, Lines: lines}
}

func presentPlanManageTool(tool ToolTimelineItem, arguments, output map[string]any) toolPresentation {
	payload := planToolPayload(tool, arguments, output)
	if payload == nil {
		return toolPresentation{Summary: "plan", Kind: "plan"}
	}
	document := planDocumentFromToolPayload(payload)
	plan := toolObject(payload, "plan")
	action := firstNonEmptyToolRaw(
		toolString(payload, "action"),
		toolString(payload, "document_operation"),
		toolString(payload, "update_kind"),
	)
	summary := "plan"
	if action != "" {
		summary += " " + strings.ReplaceAll(action, "_", " ")
	}
	facts := make([]string, 0, 2)
	if checkpoints := toolObjectSlice(document, "checkpoints"); len(checkpoints) > 0 {
		facts = append(facts, toolCountLabel(len(checkpoints), "checkpoint", "checkpoints"))
	}
	if status := firstNonEmptyToolRaw(toolString(document, "status"), toolString(plan, "status"), toolString(payload, "status")); status != "" && !strings.EqualFold(status, "ok") {
		facts = append(facts, strings.ReplaceAll(status, "_", " "))
	}
	lines := make([]toolPresentationLine, 0, 4+len(toolObjectSlice(document, "checkpoints")))
	info := toolObject(document, "info")
	title := firstNonEmptyToolRaw(
		toolString(plan, "title"),
		toolString(document, "title"),
		toolString(payload, "title"),
		toolString(info, "goal"),
	)
	if title != "" {
		lines = append(lines, toolPresentationLine{Text: title, Tone: "label"})
	}
	if goal := toolString(info, "goal"); goal != "" && !strings.EqualFold(goal, title) {
		lines = append(lines, toolPresentationLine{Text: goal})
	} else if update := firstNonEmptyToolRaw(toolString(plan, "update_summary"), toolString(payload, "update_summary")); update != "" && !strings.EqualFold(update, title) {
		lines = append(lines, toolPresentationLine{Text: update, Tone: "muted"})
	}
	checkpoints := toolObjectSlice(document, "checkpoints")
	if len(checkpoints) > 0 {
		lines = append(lines, toolPresentationLine{Text: "CHECKPOINTS  ·  " + toolCountLabel(len(checkpoints), "checkpoint", "checkpoints"), Tone: "muted"})
		for index, checkpoint := range checkpoints {
			order := toolInt(checkpoint, "order")
			if order <= 0 {
				order = index + 1
			}
			title := firstNonEmptyToolRaw(toolString(checkpoint, "title"), toolString(checkpoint, "id"), "Untitled checkpoint")
			status := humanizePlanStatus(firstNonEmptyToolRaw(toolString(checkpoint, "status"), "pending"))
			lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("%d. %s  ·  %s", order, title, status), Tone: "checkpoint:" + strings.ToLower(status)})
		}
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines, Kind: "plan"}
}

func planToolPayload(tool ToolTimelineItem, arguments, output map[string]any) map[string]any {
	for _, payload := range []map[string]any{output, arguments} {
		if planDocumentFromToolPayload(payload) != nil {
			return payload
		}
	}
	for _, candidate := range []string{tool.Output, tool.Arguments} {
		var envelope map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(candidate)), &envelope) != nil {
			continue
		}
		for _, key := range []string{"completed_output", "raw_output", "output", "arguments"} {
			if nested := parseToolObject(toolStringRaw(envelope, key)); planDocumentFromToolPayload(nested) != nil {
				return nested
			}
		}
	}
	if output != nil {
		return output
	}
	return arguments
}

func planDocumentFromToolPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if plan := toolObject(payload, "plan"); plan != nil {
		if document := toolObject(plan, "document"); document != nil {
			return document
		}
	}
	return toolObject(payload, "document")
}

func presentGenericTool(name, rawOutput string, arguments, output map[string]any) toolPresentation {
	summary := toolString(output, "summary")
	if summary == "" {
		summary = name
		action := firstToolString(output, arguments, "action")
		if action != "" {
			summary += " " + strings.ReplaceAll(action, "_", " ")
		}
		facts := make([]string, 0, 3)
		if status := toolString(output, "status"); status != "" && !strings.EqualFold(status, "ok") {
			facts = append(facts, status)
		}
		if count := toolInt(output, "count"); count > 0 {
			facts = append(facts, fmt.Sprintf("%d results", count))
		}
		if target := firstToolTarget(output, arguments); target != "" && !strings.Contains(summary, target) {
			facts = append(facts, target)
		}
		summary = appendToolFacts(summary, facts)
	}
	if output != nil {
		return toolPresentation{Summary: summary}
	}
	return toolPresentation{Summary: summary, Lines: boundedGenericToolLines(rawOutput)}
}

func boundedGenericToolLines(value string) []toolPresentationLine {
	value = normalizeToolText(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "\n")
	visible := parts
	if len(visible) > maxGenericPresentationLines {
		visible = visible[:maxGenericPresentationLines]
	}
	lines := make([]toolPresentationLine, 0, len(visible)+1)
	for _, line := range visible {
		lines = append(lines, toolPresentationLine{Text: line, Tone: "code"})
	}
	if remaining := len(parts) - len(visible); remaining > 0 {
		lines = append(lines, toolPresentationLine{Text: fmt.Sprintf("… %d more lines", remaining), Tone: "muted"})
	}
	return lines
}

func firstToolTarget(objects ...map[string]any) string {
	for _, key := range []string{"path", "url", "thread_id", "session_id", "cwd"} {
		for _, object := range objects {
			if value := toolString(object, key); value != "" {
				return value
			}
		}
	}
	return ""
}

func appendToolFacts(summary string, facts []string) string {
	filtered := make([]string, 0, len(facts))
	for _, fact := range facts {
		if fact = strings.TrimSpace(fact); fact != "" {
			filtered = append(filtered, fact)
		}
	}
	if len(filtered) == 0 {
		return summary
	}
	return summary + " · " + strings.Join(filtered, " · ")
}

func firstToolString(primary, secondary map[string]any, key string) string {
	return firstNonEmptyToolRaw(toolString(primary, key), toolString(secondary, key))
}

func firstNonEmptyToolRaw(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func toolString(object map[string]any, key string) string {
	return strings.TrimSpace(toolStringRaw(object, key))
}

func toolStringRaw(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

func toolObject(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	value, _ := object[key].(map[string]any)
	return value
}

func toolStringSlice(object map[string]any, key string) []string {
	if object == nil {
		return nil
	}
	values, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func toolInt(object map[string]any, key string) int {
	if object == nil {
		return 0
	}
	switch value := object[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func toolBool(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	value, _ := object[key].(bool)
	return value
}

func toolHasKey(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func toolObjectSlice(object map[string]any, key string) []map[string]any {
	if object == nil {
		return nil
	}
	values, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func toolCountLabel(count int, singular, plural string) string {
	label := plural
	if count == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", count, label)
}

func formatToolBytes(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}

func normalizeToolText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func clampToolRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
