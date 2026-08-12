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
	Summary                 string
	Lines                   []toolPresentationLine
	Kind                    string
	TaskRows                []taskPresentationRow
	TaskSwarm               bool
	TaskSwarmAgent          string
	TaskSwarmModel          string
	TaskSwarmStrategy       string
	TaskIntegrationContract string
	TaskIntegrationRequired bool
}

type taskPresentationRow struct {
	Index         int
	Status        string
	Agent         string
	Title         string
	Model         string
	Tool          string
	Time          string
	Preview       string
	Error         string
	SwarmStrategy string
	AssemblyPart  string
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
	case "manage-sessions":
		presentation = presentManageSessionsTool(tool, arguments, output)
	case "task":
		presentation = presentTaskTool(tool, arguments, output)
	default:
		presentation = presentGenericTool(name, tool.Output, arguments, output)
	}
	if strings.TrimSpace(presentation.Summary) == "" {
		presentation.Summary = name
	}
	if isToolActive(tool.Status) && len(presentation.Lines) == 0 && presentation.Kind == "" {
		presentation.Summary = activeToolSummary(tool.Name, presentation.Summary)
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

func isToolActive(status string) bool {
	switch canonicalToolStatus(status) {
	case "constructing", "ready", "running":
		return true
	default:
		return false
	}
}

func activeToolSummary(name, fallback string) string {
	switch normalizeToolDisplayName(name) {
	case "edit":
		return "editing…"
	case "plan-manage", "exit-plan-mode":
		return "planning…"
	case "task":
		return "launching subagents…"
	default:
		if fallback = strings.TrimSpace(fallback); fallback != "" {
			return fallback + "…"
		}
		return "working…"
	}
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
	tailLines, outputClipped := boundedTailToolLines(rawOutput, maxBashPresentationLines)
	lines = append(lines, tailLines...)
	if outputClipped {
		lines = append(lines, toolPresentationLine{Text: "use /output to open full output", Tone: "muted"})
	}
	if len(lines) == 0 && strings.EqualFold(strings.TrimSpace(tool.Status), "running") {
		lines = append(lines, toolPresentationLine{Text: "waiting for output…", Tone: "muted"})
	}
	return toolPresentation{Summary: appendToolFacts(summary, facts), Lines: lines}
}

func looksLikeTerminalBashPayload(payload map[string]any) bool {
	return toolHasKey(payload, "exit_code") || toolHasKey(payload, "path_id") || toolHasKey(payload, "timed_out")
}

func boundedTailToolLines(value string, limit int) ([]toolPresentationLine, bool) {
	value = normalizeToolText(value)
	if value == "" || limit <= 0 {
		return nil, false
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
	return lines, omitted > 0
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
		summary := "plan"
		if toolStatusRank(tool.Status) < 3 {
			summary = "planning…"
		}
		return toolPresentation{Summary: summary, Kind: "plan"}
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
	if summaryLine := planToolSummaryLine(payload, plan, document, action, title); summaryLine != "" {
		lines = append(lines, toolPresentationLine{Text: summaryLine, Tone: "muted"})
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

func planToolSummaryLine(payload, plan, document map[string]any, action, title string) string {
	update := firstNonEmptyToolRaw(toolString(plan, "update_summary"), toolString(payload, "update_summary"))
	if update != "" && !strings.EqualFold(update, title) {
		return clampToolRunes(strings.Join(strings.Fields(update), " "), 180)
	}

	action = strings.ToLower(strings.TrimSpace(action))
	if strings.Contains(action, "checkpoint") || strings.Contains(action, "subtask") {
		checkpointID := firstNonEmptyToolRaw(toolString(payload, "checkpoint_id"), toolString(plan, "update_scope"), toolString(payload, "update_scope"))
		checkpoint := planCheckpointByID(document, checkpointID)
		checkpointTitle := toolString(checkpoint, "title")
		if checkpointTitle == "" && len(toolObjectSlice(document, "checkpoints")) == 1 {
			checkpointTitle = toolString(toolObjectSlice(document, "checkpoints")[0], "title")
		}
		if checkpointTitle != "" && !strings.EqualFold(checkpointTitle, title) {
			return checkpointTitle
		}
		return planLifecycleActionSummary(action)
	}

	goal := toolString(toolObject(document, "info"), "goal")
	if goal != "" && !strings.EqualFold(goal, title) {
		return clampToolRunes(strings.Join(strings.Fields(goal), " "), 180)
	}
	return ""
}

func planLifecycleActionSummary(action string) string {
	switch action {
	case "complete_checkpoint", "checkpoint_outcome":
		return "Checkpoint completed"
	case "mark_needs_review":
		return "Checkpoint ready for review"
	case "mark_blocked":
		return "Checkpoint blocked"
	case "mark_failed":
		return "Checkpoint failed"
	case "start_checkpoint", "start_session_checkpoint":
		return "Checkpoint started"
	case "continue_checkpoint", "resolve_blocked_checkpoint":
		return "Checkpoint resumed"
	case "restart_checkpoint":
		return "Checkpoint restarted"
	case "complete_subtask":
		return "Checkpoint task completed"
	default:
		return "Checkpoint status updated"
	}
}

func planCheckpointByID(document map[string]any, checkpointID string) map[string]any {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil
	}
	for _, checkpoint := range toolObjectSlice(document, "checkpoints") {
		if strings.EqualFold(toolString(checkpoint, "id"), checkpointID) {
			return checkpoint
		}
	}
	return nil
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

func presentManageSessionsTool(tool ToolTimelineItem, arguments, output map[string]any) toolPresentation {
	payload := output
	if payload == nil {
		payload = arguments
	}
	action := firstNonEmptyToolRaw(toolString(payload, "action"), toolString(arguments, "action"))
	lines := make([]toolPresentationLine, 0, 12)

	// Permission denials and failures are durable terminal envelopes. Their
	// nested tool.arguments is the original request, not display text.
	if permission := toolObject(payload, "permission"); permission != nil {
		status := firstNonEmptyToolRaw(toolString(permission, "status"), "resolved")
		reason := toolString(permission, "reason")
		if nestedTool := toolObject(payload, "tool"); nestedTool != nil {
			if nested := parseToolObject(toolStringRaw(nestedTool, "arguments")); nested != nil {
				action = firstNonEmptyToolRaw(action, toolString(nested, "action"))
				appendManageSessionsProposalLines(&lines, toolObjectSlice(nested, "proposals"))
			}
		}
		lines = append(lines, toolPresentationLine{Text: "Permission " + strings.ReplaceAll(status, "_", " "), Tone: "error"})
		if reason != "" {
			lines = append(lines, toolPresentationLine{Text: reason, Tone: "muted"})
		}
		return toolPresentation{Summary: manageSessionsSummary(action, len(lines)), Lines: lines, Kind: "manage-sessions"}
	}

	if action == "search" && toolString(payload, "search_mode") == "durable_log" {
		appendManageSessionsDurableEventLines(&lines, payload)
	}
	items := toolObjectSlice(payload, "items")
	if len(items) == 0 && action == "get" && toolString(payload, "id") != "" {
		items = []map[string]any{payload}
	}
	if len(items) > 0 {
		appendManageSessionsItemLines(&lines, items)
	}
	results := toolObjectSlice(payload, "results")
	if len(results) > 0 {
		appendManageSessionsResultLines(&lines, results)
	}
	if len(lines) == 0 {
		appendManageSessionsMutationLines(&lines, payload, action)
	}
	if len(lines) == 0 {
		appendManageSessionsProposalLines(&lines, toolObjectSlice(arguments, "proposals"))
	}
	if len(lines) == 0 && strings.TrimSpace(tool.Error) != "" {
		lines = append(lines, toolPresentationLine{Text: tool.Error, Tone: "error"})
	}
	return toolPresentation{Summary: manageSessionsSummary(action, maxInt(len(items), len(results))), Lines: lines, Kind: "manage-sessions"}
}

func appendManageSessionsDurableEventLines(lines *[]toolPresentationLine, payload map[string]any) {
	events := toolObjectSlice(payload, "events")
	*lines = append(*lines, toolPresentationLine{Text: appendToolFacts("Durable V3 event log", []string{toolCountLabel(len(events), "match", "matches")}), Tone: "label"})
	for _, event := range events {
		eventType := firstNonEmptyToolRaw(toolString(event, "event_type"), "event")
		seq := toolInt(event, "seq")
		*lines = append(*lines, toolPresentationLine{Text: appendToolFacts(eventType, []string{fmt.Sprintf("seq %d", seq)}), Tone: "path"})
		if payloadValue, ok := event["payload"]; ok {
			encoded, _ := json.Marshal(payloadValue)
			if payloadText := strings.TrimSpace(string(encoded)); payloadText != "" && payloadText != "null" {
				*lines = append(*lines, toolPresentationLine{Text: clampToolRunes(payloadText, 240), Tone: "muted"})
			}
		}
	}
	truncation := make([]string, 0, 3)
	if toolBool(payload, "scan_truncated") {
		truncation = append(truncation, "scan truncated")
	}
	if toolBool(payload, "character_truncated") {
		truncation = append(truncation, "character limit")
	}
	if toolBool(payload, "result_truncated") {
		truncation = append(truncation, "result limit")
	}
	if len(truncation) > 0 {
		*lines = append(*lines, toolPresentationLine{Text: strings.Join(truncation, " · "), Tone: "muted"})
	}
}

func manageSessionsSummary(action string, count int) string {
	action = strings.ReplaceAll(strings.TrimSpace(action), "_", " ")
	if action == "" {
		action = "activity"
	}
	summary := "sessions " + action
	if count > 0 {
		summary = appendToolFacts(summary, []string{toolCountLabel(count, "item", "items")})
	}
	return summary
}

func appendManageSessionsProposalLines(lines *[]toolPresentationLine, proposals []map[string]any) {
	for index, proposal := range proposals {
		title := firstNonEmptyToolRaw(toolString(proposal, "title"), fmt.Sprintf("Proposal %d", index+1))
		status := ""
		if toolHasKey(proposal, "selected") && !toolBool(proposal, "selected") {
			status = "not selected"
		}
		*lines = append(*lines, toolPresentationLine{Text: appendToolFacts(title, []string{status}), Tone: "label"})
		if prompt := toolString(proposal, "prompt"); prompt != "" {
			*lines = append(*lines, toolPresentationLine{Text: prompt})
		}
		identity := appendToolFacts(toolString(proposal, "agent_name"), []string{toolString(proposal, "mode")})
		if identity != "" {
			*lines = append(*lines, toolPresentationLine{Text: identity, Tone: "path"})
		}
		workspace := firstNonEmptyToolRaw(toolString(proposal, "workspace_name"), toolString(proposal, "workspace_path"))
		if workspace != "" {
			worktree := "current workspace"
			if toolBool(proposal, "managed_worktree") || toolBool(proposal, "worktree") {
				worktree = "managed worktree"
			}
			*lines = append(*lines, toolPresentationLine{Text: appendToolFacts(workspace, []string{worktree}), Tone: "muted"})
		}
	}
}

func appendManageSessionsItemLines(lines *[]toolPresentationLine, items []map[string]any) {
	for _, item := range items {
		title := firstNonEmptyToolRaw(toolString(item, "title"), toolString(item, "id"), toolString(item, "session_id"), "Untitled session")
		state := strings.ReplaceAll(firstNonEmptyToolRaw(toolString(item, "state"), toolString(item, "status")), "_", " ")
		workspace := firstNonEmptyToolRaw(toolString(item, "workspace_name"), toolString(item, "workspace_path"))
		*lines = append(*lines, toolPresentationLine{Text: appendToolFacts(title, []string{state}), Tone: manageSessionsStatusTone(state)})
		if workspace != "" {
			*lines = append(*lines, toolPresentationLine{Text: workspace, Tone: "muted"})
		}
	}
}

func appendManageSessionsResultLines(lines *[]toolPresentationLine, results []map[string]any) {
	for _, result := range results {
		title := firstNonEmptyToolRaw(toolString(result, "title"), toolString(result, "session_id"), toolString(result, "proposal_id"), "Session")
		status := strings.ReplaceAll(firstNonEmptyToolRaw(toolString(result, "status"), "completed"), "_", " ")
		*lines = append(*lines, toolPresentationLine{Text: appendToolFacts(title, []string{status}), Tone: manageSessionsStatusTone(status)})
		meta := appendToolFacts(toolString(result, "agent"), []string{toolString(result, "mode")})
		if meta != "" {
			*lines = append(*lines, toolPresentationLine{Text: meta, Tone: "path"})
		}
		if errText := toolString(result, "error"); errText != "" {
			*lines = append(*lines, toolPresentationLine{Text: errText, Tone: "error"})
		}
	}
}

func appendManageSessionsMutationLines(lines *[]toolPresentationLine, payload map[string]any, action string) {
	keys := []string{}
	switch action {
	case "archive":
		keys = []string{"archived_session_ids", "already_archived_session_ids"}
	case "unarchive":
		keys = []string{"unarchived_session_ids", "already_active_session_ids"}
	}
	for _, key := range keys {
		for _, id := range toolStringSlice(payload, key) {
			*lines = append(*lines, toolPresentationLine{Text: id, Tone: "label"})
		}
	}
}

func manageSessionsStatusTone(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(status, "fail"), strings.Contains(status, "error"), strings.Contains(status, "denied"), strings.Contains(status, "cancel"):
		return "error"
	case strings.Contains(status, "start"), strings.Contains(status, "created"), strings.Contains(status, "complete"), strings.Contains(status, "success"):
		return "added"
	default:
		return "label"
	}
}

func presentTaskTool(tool ToolTimelineItem, arguments, output map[string]any) toolPresentation {
	launches := make([]map[string]any, 0)
	launchCount := 0
	if tool.TaskStream != nil {
		launchCount = tool.TaskStream.LaunchCount
		for _, key := range tool.TaskStream.LaunchOrder {
			if launch := tool.TaskStream.LaunchesByKey[key]; launch != nil {
				launches = append(launches, launch)
			}
		}
	}
	if len(launches) == 0 {
		launches = toolObjectSlice(output, "launches")
		launchCount = maxInt(launchCount, toolInt(output, "launch_count"))
	}
	if len(launches) == 0 && toolString(output, "path_id") == "tool.task.stream.v2" {
		if launch := toolObject(output, "launch"); launch != nil {
			launches = append(launches, launch)
		}
	}
	launchCount = maxInt(launchCount, len(launches))
	swarm := taskPresentationIsSwarm(arguments, output, launches)
	swarmStrategy := taskPresentationSwarmStrategy(arguments, output, tool.TaskStream, launches, swarm)
	integrationContract := taskPresentationIntegrationContract(arguments, output, tool.TaskStream, launches)
	integrationRequired := taskPresentationIntegrationRequired(output, tool.TaskStream, launches)
	rows := make([]taskPresentationRow, 0, len(launches))
	for index, launch := range launches {
		launchIndex := toolInt(launch, "launch_index")
		if launchIndex <= 0 {
			launchIndex = index + 1
		}
		previewKind := strings.ToLower(toolString(launch, "current_preview_kind"))
		preview := ""
		if previewKind != "assistant" && previewKind != "reasoning" {
			preview = toolString(launch, "current_preview_text")
		}
		currentTool := firstNonEmptyToolRaw(toolString(launch, "current_tool_display"), toolString(launch, "current_tool"))
		if previewKind == "reasoning" {
			currentTool = "thinking"
		}
		if currentTool == "" {
			history := toolStringSlice(launch, "tool_order")
			if len(history) > 0 {
				currentTool = history[len(history)-1]
			}
		}
		status := normalizeTaskPresentationStatus(toolString(launch, "status"))
		timeMS := toolInt(launch, "elapsed_ms")
		if status == "running" {
			timeMS = toolInt(launch, "current_tool_ms")
		}
		assemblyPart := toolObject(launch, "assembly_part")
		rows = append(rows, taskPresentationRow{
			Index:         launchIndex,
			Status:        status,
			Agent:         firstNonEmptyToolRaw(toolString(launch, "resolved_agent_name"), toolString(launch, "requested_subagent_type"), toolString(launch, "agent_type"), toolString(launch, "subagent"), toolString(launch, "requested_subagent"), "subagent"),
			Title:         firstNonEmptyToolRaw(toolString(assemblyPart, "name"), toolString(launch, "assignment_label"), toolString(launch, "meta_prompt"), "subagent"),
			Model:         taskPresentationModel(launch),
			Tool:          firstNonEmptyToolRaw(currentTool, "-"),
			Time:          toolDurationLabel(int64(timeMS)),
			Preview:       preview,
			Error:         toolString(launch, "error"),
			SwarmStrategy: firstNonEmptyToolRaw(toolString(launch, "swarm_strategy"), swarmStrategy),
			AssemblyPart:  toolString(assemblyPart, "name"),
		})
	}
	summary := "subagent stream"
	swarmAgent := ""
	swarmModel := ""
	if swarm {
		summary = "Iteration Swarm"
		if swarmStrategy == "assembly" {
			summary = "Assembly Swarm"
		}
		for _, row := range rows {
			agent := strings.ToLower(strings.TrimSpace(row.Agent))
			if swarmAgent == "" {
				swarmAgent = agent
			} else if swarmAgent != agent {
				swarmAgent = "mixed"
			}
			model := strings.TrimSpace(row.Model)
			if model == "" {
				continue
			}
			if swarmModel == "" {
				swarmModel = model
			} else if swarmModel != model {
				swarmModel = ""
				break
			}
		}
	}
	if len(rows) == 0 && toolStatusRank(tool.Status) < 3 {
		if swarm {
			if swarmStrategy == "assembly" {
				summary = "hydrating Assembly Swarm…"
			} else {
				summary = "hydrating Iteration Swarm…"
			}
		} else {
			summary = "launching subagents…"
		}
	} else if launchCount > 0 {
		summary += " · " + toolCountLabel(launchCount, "subagent", "subagents")
	}
	return toolPresentation{Summary: summary, Kind: "task", TaskRows: rows, TaskSwarm: swarm, TaskSwarmAgent: swarmAgent, TaskSwarmModel: swarmModel, TaskSwarmStrategy: swarmStrategy, TaskIntegrationContract: integrationContract, TaskIntegrationRequired: integrationRequired}
}

func taskPresentationIsSwarm(arguments, output map[string]any, launches []map[string]any) bool {
	for _, payload := range []map[string]any{arguments, output} {
		if strings.EqualFold(strings.TrimSpace(toolString(payload, "mode")), "swarm") ||
			strings.EqualFold(strings.TrimSpace(toolString(payload, "task_mode")), "swarm") ||
			toolBool(payload, "swarm_mode") {
			return true
		}
	}
	for _, launch := range launches {
		if toolBool(launch, "swarm_mode") || strings.EqualFold(strings.TrimSpace(toolString(launch, "task_mode")), "swarm") {
			return true
		}
	}
	return false
}

func taskPresentationSwarmStrategy(arguments, output map[string]any, stream *TaskStreamState, launches []map[string]any, swarm bool) string {
	for _, value := range []string{toolString(output, "swarm_strategy"), toolString(arguments, "swarm_strategy")} {
		if strategy := strings.ToLower(strings.TrimSpace(value)); strategy == "assembly" || strategy == "explore" {
			return strategy
		}
	}
	if stream != nil {
		if strategy := strings.ToLower(strings.TrimSpace(stream.SwarmStrategy)); strategy == "assembly" || strategy == "explore" {
			return strategy
		}
	}
	for _, launch := range launches {
		if strategy := strings.ToLower(strings.TrimSpace(toolString(launch, "swarm_strategy"))); strategy == "assembly" || strategy == "explore" {
			return strategy
		}
	}
	if swarm {
		return "explore"
	}
	return ""
}

func taskPresentationIntegrationContract(arguments, output map[string]any, stream *TaskStreamState, launches []map[string]any) string {
	if contract := firstToolString(output, arguments, "integration_contract"); contract != "" {
		return contract
	}
	if stream != nil && strings.TrimSpace(stream.IntegrationContract) != "" {
		return strings.TrimSpace(stream.IntegrationContract)
	}
	for _, launch := range launches {
		if contract := toolString(launch, "integration_contract"); contract != "" {
			return contract
		}
	}
	return ""
}

func taskPresentationIntegrationRequired(output map[string]any, stream *TaskStreamState, launches []map[string]any) bool {
	if toolBool(output, "integration_required") || (stream != nil && stream.IntegrationRequired) {
		return true
	}
	for _, launch := range launches {
		if toolBool(launch, "integration_required") {
			return true
		}
	}
	return false
}

func normalizeTaskPresentationStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "ok", "success", "completed", "complete":
		return "done"
	case "error", "failed":
		return "error"
	case "cancelled", "canceled":
		return "cancelled"
	case "running", "active", "in_progress":
		return "running"
	case "":
		return "pending"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func taskPresentationModel(launch map[string]any) string {
	provider := toolString(launch, "subagent_provider")
	model := toolString(launch, "subagent_model")
	if provider != "" && model != "" {
		return provider + "/" + model
	}
	return firstNonEmptyToolRaw(provider, model)
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
