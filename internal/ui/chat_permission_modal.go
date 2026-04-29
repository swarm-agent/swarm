package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) permissionComposerDesiredHeight(width int) int {
	indexes := p.genericPermissionIndexes()
	if len(indexes) == 0 {
		return 0
	}
	if width < 24 {
		return 3
	}
	p.ensurePermissionSelection()
	if p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		p.permSelected = indexes[0]
		p.syncPermissionDetailTarget()
	}
	selected := p.pendingPerms[p.permSelected]
	detailRows := len(p.permissionArgumentRenderLines(selected, maxInt(1, width-6)))
	if detailRows < 4 {
		detailRows = 4
	}
	if detailRows > 14 {
		detailRows = 14
	}
	listRows := minInt(2, len(indexes))
	insideRows := 1 + listRows + 1 + detailRows + 3 + 1
	height := insideRows + 2
	if height < 10 {
		height = 10
	}
	if height > 24 {
		height = 24
	}
	return height
}

func (p *ChatPage) permissionArgumentRenderLines(record ChatPermissionRecord, width int) []chatRenderLine {
	if width <= 0 {
		width = 1
	}
	if rendered := p.structuredPermissionArgumentRenderLines(record, width); len(rendered) > 0 {
		return rendered
	}
	lines := permissionArgumentLines(record.ToolArguments, width)
	out := make([]chatRenderLine, 0, len(lines))
	for _, line := range lines {
		style := p.theme.MarkdownText
		if strings.TrimSpace(line) == "" {
			style = p.theme.TextMuted
		}
		out = append(out, chatRenderLine{Text: line, Style: style})
	}
	if len(out) == 0 {
		return []chatRenderLine{{Text: "{}", Style: p.theme.TextMuted}}
	}
	return out
}

type permissionArgumentField struct {
	Key   string
	Value any
}

func (p *ChatPage) structuredPermissionArgumentRenderLines(record ChatPermissionRecord, width int) []chatRenderLine {
	payload, ok := parsePermissionArgumentPayload(record.ToolArguments)
	if !ok {
		return nil
	}
	object, ok := payload.(map[string]any)
	if !ok || len(object) == 0 {
		return nil
	}

	toolName := normalizePermissionToolName(record.ToolName)
	if isTaskLaunchPermission(record) {
		toolName = "task_launch"
	}
	out := make([]chatRenderLine, 0, 24)

	if summary := permissionPrimaryRequestSummary(toolName, object); summary != "" {
		line := p.styleToolSummaryLine(summary, toolName, p.theme.Text)
		out = append(out, wrapRenderLineWithCustomPrefixes("request: ", "", line, width)...)
	}

	fields := permissionArgumentFields(toolName, object)
	fields = filterPermissionArgumentFields(toolName, fields, out)
	if len(fields) == 0 {
		return out
	}
	if len(out) > 0 {
		out = append(out, chatRenderLine{Text: "", Style: p.theme.TextMuted})
	}

	for _, field := range fields {
		out = append(out, p.renderPermissionArgumentField(field, toolName, width)...)
	}
	return out
}

func permissionPrimaryRequestSummary(toolName string, payload map[string]any) string {
	if payload == nil {
		return ""
	}
	switch toolName {
	case "task_launch":
		goal := strings.TrimSpace(jsonString(payload, "goal"))
		if goal == "" {
			goal = strings.TrimSpace(jsonString(payload, "description"))
		}
		agent := strings.TrimSpace(jsonString(payload, "resolved_agent_name"))
		if goal == "" && agent == "" {
			return "task launch"
		}
		if goal == "" {
			return "task launch via " + agent
		}
		if agent == "" {
			return "task launch " + goal
		}
		return fmt.Sprintf("task launch %s via %s", goal, agent)
	case "bash":
		command := strings.TrimSpace(jsonString(payload, "command"))
		if command == "" {
			return "bash"
		}
		return "bash " + sanitizeCommandSnippetPreview(clampEllipsis(command, 120))
	case "read":
		path := strings.TrimSpace(jsonString(payload, "path"))
		if path == "" {
			return "read"
		}
		lineStart := jsonInt(payload, "line_start")
		maxLines := jsonInt(payload, "max_lines")
		if maxLines > 0 {
			if lineStart <= 0 {
				lineStart = 1
			}
			lineEnd := lineStart + maxLines - 1
			if maxLines == 1 {
				return fmt.Sprintf("read %s (request line %d)", path, lineStart)
			}
			return fmt.Sprintf("read %s (request lines %d-%d)", path, lineStart, lineEnd)
		}
		return "read " + path
	case "write":
		path := strings.TrimSpace(jsonString(payload, "path"))
		if path == "" {
			return "write"
		}
		if jsonBool(payload, "append") {
			return "append " + path
		}
		return "write " + path
	case "edit":
		path := strings.TrimSpace(jsonString(payload, "path"))
		if path == "" {
			return "edit"
		}
		return "edit " + path
	case "grep":
		pattern := strings.TrimSpace(jsonString(payload, "pattern"))
		root := strings.TrimSpace(jsonString(payload, "path"))
		switch {
		case pattern != "" && root != "":
			return fmt.Sprintf("grep %q in %s", pattern, root)
		case pattern != "":
			return fmt.Sprintf("grep %q", pattern)
		case root != "":
			return "grep in " + root
		default:
			return "grep"
		}
	case "glob":
		pattern := strings.TrimSpace(jsonString(payload, "pattern"))
		root := strings.TrimSpace(jsonString(payload, "path"))
		switch {
		case pattern != "" && root != "":
			return fmt.Sprintf("glob %q in %s", pattern, root)
		case pattern != "":
			return fmt.Sprintf("glob %q", pattern)
		case root != "":
			return "glob in " + root
		default:
			return "glob"
		}
	case "list":
		path := strings.TrimSpace(jsonString(payload, "path"))
		if path == "" {
			return "list"
		}
		return "list " + path
	case "websearch":
		query := strings.TrimSpace(jsonString(payload, "query"))
		if query != "" {
			return "websearch " + query
		}
		queries := jsonStringSlice(payload, "queries")
		if len(queries) == 1 {
			return "websearch " + queries[0]
		}
		if len(queries) > 1 {
			return fmt.Sprintf("websearch %d queries", len(queries))
		}
		return "websearch"
	case "task":
		description := strings.TrimSpace(jsonString(payload, "description"))
		if description != "" {
			return "task " + description
		}
		return "task"
	case "manage_todos":
		action := strings.TrimSpace(jsonString(payload, "action"))
		text := strings.TrimSpace(jsonString(payload, "text"))
		itemID := strings.TrimSpace(jsonString(payload, "id"))
		ownerKind := strings.TrimSpace(jsonString(payload, "owner_kind"))
		ownerSuffix := ""
		if ownerKind != "" {
			ownerSuffix = " [" + ownerKind + "]"
		}
		if action == "batch" {
			if operations := jsonObjectSlice(payload, "operations"); len(operations) > 0 {
				return fmt.Sprintf("manage_todos%s batch %d ops", ownerSuffix, len(operations))
			}
			return "manage_todos" + ownerSuffix + " batch"
		}
		switch {
		case action != "" && text != "":
			return fmt.Sprintf("manage_todos%s %s %s", ownerSuffix, action, text)
		case action != "" && itemID != "":
			return fmt.Sprintf("manage_todos%s %s %s", ownerSuffix, action, itemID)
		case action != "":
			return "manage_todos" + ownerSuffix + " " + action
		default:
			return "manage_todos" + ownerSuffix
		}
	default:
		if path := strings.TrimSpace(jsonString(payload, "path")); path != "" {
			return permissionDisplayToolName(toolName) + " " + path
		}
		return permissionDisplayToolName(toolName)
	}
}

func permissionArgumentFields(toolName string, payload map[string]any) []permissionArgumentField {
	if payload == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(payload))
	out := make([]permissionArgumentField, 0, len(payload))
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		value, ok := payload[key]
		if !ok {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, permissionArgumentField{Key: key, Value: value})
	}

	for _, key := range permissionPreferredArgumentKeys(toolName) {
		add(key)
	}

	remaining := make([]string, 0, len(payload)-len(out))
	for key := range payload {
		if _, exists := seen[key]; exists {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		add(key)
	}
	return out
}

func filterPermissionArgumentFields(toolName string, fields []permissionArgumentField, summaries []chatRenderLine) []permissionArgumentField {
	if normalizePermissionToolName(toolName) != "bash" || !permissionRenderLinesContainRequestSummary(summaries) {
		return fields
	}
	out := fields[:0]
	for _, field := range fields {
		if strings.EqualFold(strings.TrimSpace(field.Key), "command") {
			continue
		}
		out = append(out, field)
	}
	return out
}

func permissionRenderLinesContainRequestSummary(lines []chatRenderLine) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line.Text)), "request:") {
			return true
		}
	}
	return false
}

func permissionPreferredArgumentKeys(toolName string) []string {
	switch toolName {
	case "bash":
		return []string{"command", "workdir", "justification", "sandbox_permissions", "prefix_rule", "timeout_ms", "yield_time_ms", "max_output_tokens", "shell", "login", "tty"}
	case "read":
		return []string{"path", "line_start", "max_lines"}
	case "write":
		return []string{"path", "append", "content"}
	case "edit":
		return []string{"path", "edits", "old_string", "new_string", "replace_all"}
	case "grep":
		return []string{"pattern", "path", "max_results", "timeout_ms"}
	case "glob":
		return []string{"pattern", "path", "max_results", "timeout_ms"}
	case "list":
		return []string{"path", "pattern", "max_results", "cursor"}
	case "websearch":
		return []string{"query", "queries", "search_type", "include_domains", "max_results", "recency_days"}
	case "webfetch":
		return []string{"urls", "retrieval_mode", "timeout_ms"}
	case "task":
		return []string{"description", "prompt", "subagent_type", "max_steps"}
	case "task_launch":
		return []string{"goal", "description", "prompt", "launch_count", "launches"}
	case "theme_change":
		return []string{"action", "theme", "workspace_path", "change", "summary"}
	case "manage_todos":
		return []string{"action", "owner_kind", "operations", "text", "id", "done", "priority", "group", "tags", "in_progress", "ordered_ids", "workspace_path"}
	default:
		return nil
	}
}

func (p *ChatPage) renderPermissionArgumentField(field permissionArgumentField, toolName string, width int) []chatRenderLine {
	label := strings.TrimSpace(field.Key)
	if label == "" {
		label = "value"
	}
	prefix := label + ": "
	continuationPrefix := strings.Repeat(" ", utf8.RuneCountInString(prefix))

	switch value := field.Value.(type) {
	case string:
		return p.renderPermissionArgumentText(prefix, continuationPrefix, value, toolName, label == "command", width)
	case nil:
		line := chatRenderLine{Text: "null", Style: p.theme.TextMuted}
		return wrapRenderLineWithCustomPrefixes(prefix, continuationPrefix, line, width)
	case map[string]any, []any:
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			line := chatRenderLine{Text: fmt.Sprintf("%v", value), Style: p.theme.Text}
			return wrapRenderLineWithCustomPrefixes(prefix, continuationPrefix, line, width)
		}
		return p.renderPermissionArgumentText(prefix, continuationPrefix, string(encoded), toolName, false, width)
	default:
		line := chatRenderLine{Text: fmt.Sprintf("%v", value), Style: p.theme.Text}
		return wrapRenderLineWithCustomPrefixes(prefix, continuationPrefix, line, width)
	}
}

func (p *ChatPage) renderPermissionArgumentText(prefix, continuationPrefix, value, toolName string, preferCommand bool, width int) []chatRenderLine {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	parts := strings.Split(value, "\n")
	if len(parts) == 0 {
		parts = []string{""}
	}

	out := make([]chatRenderLine, 0, len(parts))
	for i, part := range parts {
		style := p.theme.Text
		if strings.TrimSpace(part) == "" {
			style = p.theme.TextMuted
			part = "(empty)"
		}
		line := p.styleSyntaxLine(part, chatSyntaxRequest{
			Surface:            chatSyntaxSurfaceTool,
			PreferredTool:      toolName,
			PreferCommand:      preferCommand,
			AllowInlineCommand: true,
		}, style)
		currentPrefix := continuationPrefix
		if i == 0 {
			currentPrefix = prefix
		}
		out = append(out, wrapRenderLineWithCustomPrefixes(currentPrefix, continuationPrefix, line, width)...)
	}
	return out
}

func (p *ChatPage) drawPermissionComposer(s tcell.Screen, rect Rect) {
	p.permRows = p.permRows[:0]
	p.permIndexes = p.permIndexes[:0]
	p.permApproveRect = Rect{}
	p.permDenyRect = Rect{}
	p.alwaysAllowRect = Rect{}
	p.alwaysDenyRect = Rect{}

	indexes := p.genericPermissionIndexes()
	if len(indexes) == 0 {
		p.permDetailMaxScroll = 0
		p.permDetailScroll = 0
		return
	}
	if rect.W < 24 || rect.H < 9 {
		p.drawPermissionComposerCompact(s, rect, indexes)
		return
	}
	p.ensurePermissionSelection()
	p.syncPermissionDetailTarget()
	if p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		return
	}
	selectedPos := indexOfInt(indexes, p.permSelected)
	if selectedPos < 0 {
		selectedPos = 0
		p.permSelected = indexes[selectedPos]
		p.syncPermissionDetailTarget()
	}
	selected := p.pendingPerms[p.permSelected]
	preview, previewErr := p.permissionAlwaysAllowPreview(selected)

	listY := rect.Y + 2
	maxListRows := minInt(2, len(indexes))
	if rect.H <= 11 {
		maxListRows = minInt(1, len(indexes))
	}
	start := selectedPos
	if start > len(indexes)-maxListRows {
		start = len(indexes) - maxListRows
	}
	if start < 0 {
		start = 0
	}

	argsLabelY := listY + maxListRows
	inputBox := Rect{X: rect.X + 2, Y: rect.Y + rect.H - 5, W: rect.W - 4, H: 2}
	detailTop := argsLabelY + 1
	detailH := inputBox.Y - detailTop
	if detailH < 0 {
		detailH = 0
	}
	detailRect := Rect{X: rect.X + 3, Y: detailTop, W: maxInt(1, rect.W-6), H: detailH}
	detailLines := p.permissionArgumentRenderLines(selected, detailRect.W)
	p.permDetailMaxScroll = maxInt(0, len(detailLines)-detailRect.H)
	p.clampPermissionDetailScroll()

	DrawBox(s, rect, p.theme.Warning)
	header := fmt.Sprintf("permission %d/%d  ·  mode %s", selectedPos+1, len(indexes), p.sessionMode)
	if p.permDetailMaxScroll > 0 {
		current, total := p.permissionDetailScrollSummary()
		header = fmt.Sprintf("%s  ·  args %d/%d", header, current, total)
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, clampEllipsis(header, rect.W-4))

	for i := 0; i < maxListRows && start+i < len(indexes); i++ {
		idx := indexes[start+i]
		record := p.pendingPerms[idx]
		prefix := "  "
		style := p.theme.TextMuted
		if idx == p.permSelected {
			prefix = "› "
			style = p.theme.Primary
		}
		label := fmt.Sprintf("%s%s · %s", prefix, permissionDisplayToolName(record.ToolName), permissionRequirementLabel(record.Requirement))
		p.permRows = append(p.permRows, Rect{X: rect.X + 1, Y: listY, W: rect.W - 2, H: 1})
		p.permIndexes = append(p.permIndexes, idx)
		DrawText(s, rect.X+2, listY, rect.W-4, style, clampEllipsis(label, rect.W-4))
		listY++
	}

	if argsLabelY < inputBox.Y {
		DrawText(s, rect.X+2, argsLabelY, rect.W-4, p.theme.TextMuted, "arguments")
	}

	if strings.TrimSpace(preview) != "" && argsLabelY < inputBox.Y {
		style := p.theme.TextMuted
		if previewErr == nil {
			style = p.theme.Accent
		}
		DrawText(s, rect.X+2, argsLabelY, rect.W-4, style, clampEllipsis("always allow prefix: "+preview, rect.W-4))
		argsLabelY++
		detailTop = argsLabelY + 1
		detailH = inputBox.Y - detailTop
		if detailH < 0 {
			detailH = 0
		}
		detailRect = Rect{X: rect.X + 3, Y: detailTop, W: maxInt(1, rect.W-6), H: detailH}
		detailLines = p.permissionArgumentRenderLines(selected, detailRect.W)
		p.permDetailMaxScroll = maxInt(0, len(detailLines)-detailRect.H)
		p.clampPermissionDetailScroll()
	}

	if detailRect.H > 0 {
		startLine := p.permDetailScroll
		if startLine < 0 {
			startLine = 0
		}
		if startLine > len(detailLines) {
			startLine = len(detailLines)
		}
		endLine := minInt(len(detailLines), startLine+detailRect.H)
		y := detailRect.Y
		for i := startLine; i < endLine && y < detailRect.Y+detailRect.H; i++ {
			DrawTimelineLine(s, detailRect.X, y, detailRect.W, detailLines[i])
			y++
		}
	}

	if inputBox.W >= 8 && inputBox.H >= 2 {
		textX := inputBox.X + 2
		textY := inputBox.Y + 1
		textW := inputBox.W - 4
		if textW > 0 {
			prefix := "› "
			prefixW := utf8.RuneCountInString(prefix)
			if prefixW >= textW {
				prefix = ">"
				prefixW = utf8.RuneCountInString(prefix)
			}
			DrawText(s, textX, textY, textW, p.theme.Primary, prefix)
			visible := clampTail(p.permInput, maxInt(1, textW-prefixW-1))
			if visible != "" {
				DrawText(s, textX+prefixW, textY, maxInt(1, textW-prefixW), p.theme.Text, visible)
			}
			if (p.frameTick/chatCursorBlinkOn)%2 == 0 {
				cursorX := textX + prefixW + utf8.RuneCountInString(visible)
				maxX := inputBox.X + inputBox.W - 3
				if cursorX > maxX {
					cursorX = maxX
				}
				s.SetContent(cursorX, textY, chatCursorRune, nil, p.theme.Primary)
			}
		}
	}

	actionY := rect.Y + rect.H - 2
	actionX := rect.X + 2
	p.permApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, rect.X+rect.W-2, "Enter Approve", p.theme.Success)
	p.permDenyRect, actionX = drawPermissionActionButton(s, actionX, actionY, rect.X+rect.W-2, "Esc Deny", p.theme.Error)
	p.alwaysAllowRect, actionX = drawPermissionActionButton(s, actionX, actionY, rect.X+rect.W-2, "Ctrl+A Always Allow", p.theme.Accent)
	p.alwaysDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, rect.X+rect.W-2, "Ctrl+D Always Deny", p.theme.Warning)
}

func (p *ChatPage) drawPermissionComposerCompact(s tcell.Screen, rect Rect, indexes []int) {
	if rect.W < 16 || rect.H < 1 {
		return
	}
	p.permDetailMaxScroll = 0
	p.permDetailScroll = 0
	p.ensurePermissionSelection()
	if p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		return
	}
	selectedPos := indexOfInt(indexes, p.permSelected)
	if selectedPos < 0 {
		selectedPos = 0
		p.permSelected = indexes[0]
		p.syncPermissionDetailTarget()
	}
	selected := p.pendingPerms[p.permSelected]
	tool := permissionDisplayToolName(selected.ToolName)
	label := fmt.Sprintf("perm %d/%d · %s", selectedPos+1, len(indexes), tool)
	DrawText(s, rect.X, rect.Y, rect.W, p.theme.Warning, clampEllipsis(label, rect.W))
	if rect.H < 2 {
		return
	}
	hint := "Enter approve · Esc deny · Ctrl+A always allow · Ctrl+D always deny"
	DrawText(s, rect.X, rect.Y+1, rect.W, p.theme.TextMuted, clampEllipsis(hint, rect.W))
}

func drawPermissionActionButton(s tcell.Screen, x, y, maxX int, label string, style tcell.Style) (Rect, int) {
	text := " " + strings.TrimSpace(label) + " "
	w := utf8.RuneCountInString(text)
	if w <= 0 || x+w > maxX {
		return Rect{}, x
	}
	buttonStyle := filledButtonStyle(style)
	FillRect(s, Rect{X: x, Y: y, W: w, H: 1}, buttonStyle)
	DrawText(s, x, y, w, buttonStyle, text)
	next := x + w + 1
	return Rect{X: x, Y: y, W: w, H: 1}, next
}

func normalizePermissionToolName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if dot := strings.LastIndex(name, "."); dot >= 0 && dot+1 < len(name) {
		name = name[dot+1:]
	}
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuser":
		return "ask_user"
	case "exitplanmode":
		return "exit_plan_mode"
	case "managetodos":
		return "manage_todos"
	case "managetheme":
		return "manage_theme"
	}
	return name
}

func permissionDisplayToolName(raw string) string {
	name := normalizePermissionToolName(raw)
	if name == "" {
		return "tool"
	}
	switch name {
	case "ask_user":
		return "ask-user"
	case "exit_plan_mode":
		return "exit_plan_mode"
	case "manage_todos":
		return "manage_todos"
	case "manage_theme":
		return "manage-theme"
	default:
		return name
	}
}

func bashPermissionPreviewPrefix(preview string) string {
	preview = strings.TrimSpace(preview)
	if preview == "" {
		return ""
	}
	lower := strings.ToLower(preview)
	for _, prefix := range []string{
		"allow bash command prefix:",
		"allow bash prefix:",
		"deny bash command prefix:",
		"deny bash prefix:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(preview[len(prefix):])
		}
	}
	return preview
}

func permissionRequirementLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "permission"
	case "task_launch":
		return "task launch"
	case "workspace_scope":
		return "workspace access"
	case "skill_change":
		return "skill change"
	case "agent_change":
		return "agent change"
	case "theme_change":
		return "theme change"
	default:
		return strings.TrimSpace(raw)
	}
}

func isAskUserPermission(record ChatPermissionRecord) bool {
	return normalizePermissionToolName(record.ToolName) == "ask_user"
}

func isThemeChangePermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "theme_change")
}

func decodePermissionArguments(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func mapStringArg(payload map[string]any, key string) string {
	v, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

var permissionParserDebugSeen sync.Map

func permissionArgumentLines(raw string, width int) []string {
	if width <= 0 {
		width = 1
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		permissionParserDebugf("permission args empty: rendering {}")
		return []string{"{}"}
	}
	payloadText := raw
	if payload, ok := parsePermissionArgumentPayload(raw); ok {
		if pretty, err := json.MarshalIndent(payload, "", "  "); err == nil {
			payloadText = string(pretty)
			permissionParserDebugf("permission args parsed raw_len=%d payload_type=%T", len(raw), payload)
		} else {
			permissionParserDebugf("permission args marshal failed raw_len=%d err=%v", len(raw), err)
		}
	} else {
		permissionParserDebugf("permission args parse failed raw_len=%d preview=%q", len(raw), permissionDebugPreview(raw, 180))
	}
	out := make([]string, 0, 32)
	for _, line := range strings.Split(payloadText, "\n") {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, Wrap(line, width)...)
	}
	if len(out) == 0 {
		return []string{"{}"}
	}
	return out
}

func parsePermissionArgumentPayload(raw string) (any, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, false
	}
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		permissionParserDebugf("permission args top-level json decode failed err=%v", err)
		return nil, false
	}
	normalized, _ := normalizePermissionPayload(payload, 0)
	return normalized, true
}

func normalizePermissionPayload(payload any, depth int) (any, bool) {
	if depth > 8 {
		return payload, false
	}
	switch typed := payload.(type) {
	case map[string]any:
		changed := false
		for key, value := range typed {
			normalized, wasChanged := normalizePermissionPayload(value, depth+1)
			if !wasChanged {
				continue
			}
			typed[key] = normalized
			changed = true
		}
		return typed, changed
	case []any:
		changed := false
		for i := range typed {
			normalized, wasChanged := normalizePermissionPayload(typed[i], depth+1)
			if !wasChanged {
				continue
			}
			typed[i] = normalized
			changed = true
		}
		return typed, changed
	case string:
		trimmed := strings.TrimSpace(typed)
		if !permissionJSONCandidate(trimmed) {
			return payload, false
		}
		var nested any
		if err := json.Unmarshal([]byte(trimmed), &nested); err != nil {
			permissionParserDebugf("permission args nested json decode failed err=%v", err)
			return payload, false
		}
		normalized, _ := normalizePermissionPayload(nested, depth+1)
		return normalized, true
	default:
		return payload, false
	}
}

func permissionJSONCandidate(text string) bool {
	if text == "" {
		return false
	}
	switch text[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

func permissionDebugPreview(text string, max int) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " "))
	if max <= 0 || len(clean) <= max {
		return clean
	}
	if max <= 3 {
		return clean[:max]
	}
	return clean[:max-3] + "..."
}

func permissionParserDebugEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("SWARM_PERMISSION_DEBUG"))
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func permissionParserDebugf(format string, args ...any) {
	if !permissionParserDebugEnabled() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if _, loaded := permissionParserDebugSeen.LoadOrStore(msg, struct{}{}); loaded {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmtui.permission] %s\n", msg)
}

func (p *ChatPage) permissionPanelHeight(width int) int {
	count := p.genericPermissionCount()
	if width < 24 || count == 0 {
		return 0
	}
	rows := count
	if rows > 2 {
		rows = 2
	}
	// Header + rows + footer/action line + borders.
	return rows + 3
}

func (p *ChatPage) drawPermissionPanel(s tcell.Screen, rect Rect) {
	p.permRows = p.permRows[:0]
	p.permIndexes = p.permIndexes[:0]
	p.alwaysAllowRect = Rect{}
	p.alwaysDenyRect = Rect{}
	indexes := p.genericPermissionIndexes()
	if rect.W < 18 || rect.H < 3 || len(indexes) == 0 {
		return
	}

	p.ensurePermissionSelection()
	if p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		return
	}
	selectedPos := indexOfInt(indexes, p.permSelected)
	if selectedPos < 0 {
		selectedPos = 0
		p.permSelected = indexes[selectedPos]
	}

	DrawBox(s, rect, p.theme.Warning)
	header := fmt.Sprintf("permissions pending: %d  ·  mode %s", len(indexes), p.sessionMode)
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, clampEllipsis(header, rect.W-4))

	maxRows := rect.H - 4
	if maxRows < 1 {
		maxRows = 1
	}
	if maxRows > len(indexes) {
		maxRows = len(indexes)
	}
	start := 0
	if selectedPos >= maxRows {
		start = selectedPos - (maxRows - 1)
	}
	if start < 0 {
		start = 0
	}
	if start > len(indexes)-maxRows {
		start = len(indexes) - maxRows
	}
	if start < 0 {
		start = 0
	}

	rowY := rect.Y + 2
	for i := 0; i < maxRows && rowY < rect.Y+rect.H-2; i++ {
		idx := indexes[start+i]
		record := p.pendingPerms[idx]
		style := p.theme.Text
		prefix := "  "
		if idx == p.permSelected {
			style = p.theme.Primary
			prefix = "› "
		}
		label := fmt.Sprintf("%s%s · %s", prefix, permissionDisplayToolName(record.ToolName), permissionRequirementLabel(record.Requirement))
		p.permRows = append(p.permRows, Rect{X: rect.X + 1, Y: rowY, W: rect.W - 2, H: 1})
		p.permIndexes = append(p.permIndexes, idx)
		DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(label, rect.W-4))
		rowY++
	}

	denyLabel := "[Always Deny]"
	denyW := utf8.RuneCountInString(denyLabel)
	acceptLabel := "[Always Allow]"
	acceptW := utf8.RuneCountInString(acceptLabel)
	actionY := rect.Y + rect.H - 2
	preview, _ := p.permissionAlwaysAllowPreview(p.pendingPerms[p.permSelected])
	if strings.TrimSpace(preview) != "" && actionY > rect.Y+2 {
		DrawText(s, rect.X+2, actionY-1, rect.W-4, p.theme.Accent, clampEllipsis("always allow prefix: "+preview, rect.W-4))
	}
	denyX := rect.X + rect.W - acceptW - denyW - 5
	allowX := rect.X + rect.W - acceptW - 3
	if denyX >= rect.X+2 {
		p.alwaysDenyRect = Rect{
			X: denyX,
			Y: actionY,
			W: denyW,
			H: 1,
		}
		p.alwaysAllowRect = Rect{
			X: allowX,
			Y: actionY,
			W: acceptW,
			H: 1,
		}
		DrawText(s, p.alwaysDenyRect.X, p.alwaysDenyRect.Y, denyW, p.theme.Warning, denyLabel)
		DrawText(s, p.alwaysAllowRect.X, p.alwaysAllowRect.Y, acceptW, p.theme.Primary, acceptLabel)
	}
	help := "Enter approve · Esc deny"
	helpW := rect.W - 4
	if p.alwaysDenyRect.X > 0 {
		helpW = maxInt(1, p.alwaysDenyRect.X-rect.X-4)
	}
	DrawText(s, rect.X+2, actionY, helpW, p.theme.TextMuted, clampEllipsis(help, helpW))
}

func (p *ChatPage) permissionAlwaysAllowPreview(record ChatPermissionRecord) (string, error) {
	if p == nil || p.backend == nil {
		return "", fmt.Errorf("permission backend unavailable")
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(p.sessionID) == "" {
		return "", fmt.Errorf("permission id is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	explain, err := p.backend.ExplainPermission(ctx, p.sessionMode, record.ToolName, record.ToolArguments)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(explain.RulePreview) != "" {
		if normalizePermissionToolName(record.ToolName) == "bash" {
			return bashPermissionPreviewPrefix(explain.RulePreview), nil
		}
		return explain.RulePreview, nil
	}
	if normalizePermissionToolName(record.ToolName) == "bash" {
		return "bash", nil
	}
	return "allow tool: " + permissionDisplayToolName(record.ToolName), nil
}
