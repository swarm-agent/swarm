package run

import (
	"encoding/json"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/tool"
)

const (
	toolHistoryPathID              = "run.tool-history.v2"
	maxBashCompletedRawOutputBytes = 512
)

type gitStatusResponseFields = gitstatus.ResponseFields

type toolHistoryRecord struct {
	PathID          string         `json:"path_id"`
	Tool            string         `json:"tool"`
	CallID          string         `json:"call_id"`
	Arguments       string         `json:"arguments,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Output          string         `json:"output,omitempty"`
	CompletedOutput string         `json:"completed_output,omitempty"`
	Error           string         `json:"error,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
}

func formatToolHistory(call tool.Call, result tool.Result) string {
	return formatToolHistoryWithMetadata(call, nil, result)
}

func liveStreamRawOutput(call tool.Call, result tool.Result) string {
	output := strings.TrimSpace(result.Output)
	if output == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(call.Name), "bash") {
		return strings.TrimSpace(truncateRunes(output, maxBashCompletedRawOutputBytes))
	}
	return output
}

func formatToolHistoryWithMetadata(call tool.Call, metadata map[string]any, result tool.Result) string {
	record := buildToolHistoryRecord(call, metadata, result)
	encoded, err := json.Marshal(record)
	if err == nil {
		return string(encoded)
	}

	record.Metadata = nil
	encoded, err = json.Marshal(record)
	if err == nil {
		return string(encoded)
	}

	summary := summarizeToolOutput(record.Tool, record.Output, maxToolPreviewChars, 3)
	if summary == "" {
		summary = "(empty)"
	}
	if record.Error != "" {
		return fmt.Sprintf("tool=%s call_id=%s error=%s output=%s", record.Tool, record.CallID, record.Error, summary)
	}
	return fmt.Sprintf("tool=%s call_id=%s output=%s", record.Tool, record.CallID, summary)
}

func buildToolHistoryRecord(call tool.Call, metadata map[string]any, result tool.Result) toolHistoryRecord {
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = strings.TrimSpace(result.Name)
	}
	if name == "" {
		name = "tool"
	}
	callID := strings.TrimSpace(result.CallID)
	if callID == "" {
		callID = strings.TrimSpace(call.CallID)
	}
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	record := toolHistoryRecord{
		PathID:          toolHistoryPathID,
		Tool:            name,
		CallID:          callID,
		Arguments:       arguments,
		Metadata:        cloneGenericMap(metadata),
		Output:          strings.TrimSpace(result.Output),
		CompletedOutput: formatToolCompletedOutput(call, result),
		Error:           strings.TrimSpace(result.Error),
		DurationMS:      result.DurationMS,
	}
	if record.CompletedOutput == "" {
		record.CompletedOutput = record.Output
	}
	return record
}

func decodeToolHistoryRecord(raw string) (toolHistoryRecord, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return toolHistoryRecord{}, false
	}

	var record toolHistoryRecord
	if err := json.Unmarshal([]byte(raw), &record); err == nil {
		record.PathID = strings.TrimSpace(record.PathID)
		record.Tool = strings.TrimSpace(record.Tool)
		record.CallID = strings.TrimSpace(record.CallID)
		record.Arguments = strings.TrimSpace(record.Arguments)
		record.Output = strings.TrimSpace(record.Output)
		record.CompletedOutput = strings.TrimSpace(record.CompletedOutput)
		record.Error = strings.TrimSpace(record.Error)
		record.Metadata = cloneGenericMap(record.Metadata)
		if strings.EqualFold(record.PathID, toolHistoryPathID) && record.Tool != "" && record.CallID != "" {
			if record.Arguments == "" {
				record.Arguments = "{}"
			}
			if record.CompletedOutput == "" {
				record.CompletedOutput = record.Output
			}
			return record, true
		}
	}
	return toolHistoryRecord{}, false
}

func buildToolHistoryInput(content string) ([]map[string]any, bool) {
	record, ok := decodeToolHistoryRecord(content)
	if !ok || record.Tool == "" || record.CallID == "" {
		return nil, false
	}

	call := tool.Call{
		CallID:    record.CallID,
		Name:      record.Tool,
		Arguments: firstNonEmptyString(record.Arguments, "{}"),
	}
	result := tool.Result{
		CallID:     record.CallID,
		Name:       record.Tool,
		Output:     firstNonEmptyString(record.Output, record.CompletedOutput),
		Error:      record.Error,
		DurationMS: record.DurationMS,
	}
	callInput := map[string]any{
		"type":      "function_call",
		"call_id":   call.CallID,
		"name":      call.Name,
		"arguments": call.Arguments,
	}
	if metadata := cloneGenericMap(record.Metadata); len(metadata) > 0 {
		callInput["metadata"] = metadata
	}
	return []map[string]any{
		callInput,
		{
			"type":    "function_call_output",
			"call_id": call.CallID,
			"output":  prepareToolOutputForModel(call, result),
		},
	}, true
}

func formatToolCompletedOutput(call tool.Call, result tool.Result) string {
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = strings.TrimSpace(call.Name)
	}
	if preview, ok := toolHistoryStructuredPayload(name, result.Output, call.Arguments); ok {
		return preview
	}
	return summarizeToolOutput(name, result.Output, maxToolPreviewChars, 2)
}

func prepareToolOutputForModel(call tool.Call, result tool.Result) string {
	output := strings.TrimSpace(result.Output)
	errorText := strings.TrimSpace(result.Error)
	if errorText == "" && (output == "" || len(output) <= maxToolInputBytes) {
		return output
	}

	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = strings.TrimSpace(result.Name)
	}
	if name == "" {
		name = "tool"
	}

	payload := map[string]any{
		"path_id": "run.tool-output.v2",
		"tool":    strings.ToLower(name),
		"call_id": strings.TrimSpace(result.CallID),
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	if output != "" && len(output) <= maxToolInputBytes {
		payload["output"] = output
	}
	if len(output) > maxToolInputBytes {
		payload["truncated_for_model"] = true
		payload["original_bytes"] = len(output)
		payload["retained_bytes"] = maxToolInputBytes
		payload["summary"] = summarizeToolOutput(name, output, maxToolPreviewChars, 3)
		payload["hint"] = "Rerun with narrower tool scope if more detail is needed."
		if structured := decodeToolPayload(output); structured != nil {
			if pathID := mapString(structured, "path_id"); pathID != "" {
				payload["tool_path_id"] = pathID
			}
			if toolSummary := mapString(structured, "summary"); toolSummary != "" {
				payload["tool_summary"] = toolSummary
			}
			if count := mapInt(structured, "count"); count > 0 {
				payload["count"] = count
			}
			if totalMatches := mapInt(structured, "total_matches"); totalMatches > 0 {
				payload["total_matches"] = totalMatches
			}
			if hasMore, ok := structured["has_more_queries"].(bool); ok {
				payload["has_more_queries"] = hasMore
			}
			if nextCursor := mapInt(structured, "next_query_cursor"); nextCursor > 0 {
				payload["next_query_cursor"] = nextCursor
			}
		}
		if preview := truncateRunes(output, maxToolInputPreview); preview != "" {
			payload["preview"] = preview
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		switch {
		case errorText != "" && output != "":
			return errorText + "\n\n" + truncateRunes(output, maxToolInputBytes)
		case errorText != "":
			return errorText
		default:
			return truncateRunes(output, maxToolInputBytes)
		}
	}
	return string(encoded)
}

func toolHistoryStructuredPayload(name, output, arguments string) (string, bool) {
	trimmedOutput := strings.TrimSpace(output)
	payload := decodeToolPayload(trimmedOutput)
	if isPermissionGatePayload(payload) {
		encoded, err := json.Marshal(payload)
		if err == nil {
			return string(encoded), true
		}
		if trimmedOutput != "" {
			return trimmedOutput, true
		}
		return "", false
	}

	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "edit":
		return toolHistoryStructuredEditPayload(output, arguments)
	case "websearch", "search":
		if payload == nil {
			return "", false
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	case "task":
		if payload == nil {
			return "", false
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	case "permission", "ask-user", "ask_user", "exit-plan-mode", "exit_plan_mode":
		if payload == nil {
			return "", false
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	default:
		return "", false
	}
}

func isPermissionGatePayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	rawPermission, ok := payload["permission"]
	if !ok {
		return false
	}
	permission, ok := rawPermission.(map[string]any)
	if !ok || len(permission) == 0 {
		return false
	}
	if _, hasApproved := permission["approved"]; !hasApproved {
		if _, hasStatus := permission["status"]; !hasStatus {
			return false
		}
	}
	rawTool, ok := payload["tool"]
	if !ok {
		return false
	}
	toolPayload, ok := rawTool.(map[string]any)
	if !ok || len(toolPayload) == 0 {
		return false
	}
	if _, hasName := toolPayload["name"]; hasName {
		return true
	}
	_, hasArguments := toolPayload["arguments"]
	return hasArguments
}

func toolHistoryStructuredEditPayload(output, arguments string) (string, bool) {
	resultPayload := decodeToolPayload(strings.TrimSpace(output))
	argsPayload := decodeToolPayload(strings.TrimSpace(arguments))
	if resultPayload == nil && argsPayload == nil {
		return "", false
	}

	path := firstNonEmptyString(mapString(resultPayload, "path"), mapString(argsPayload, "path"))
	matches := firstPositiveInt(mapInt(resultPayload, "matches"))
	replacements := firstPositiveInt(mapInt(resultPayload, "replacements"))
	replaceAll := mapBool(resultPayload, "replace_all") || mapBool(argsPayload, "replace_all")
	resultEdits := mapSlice(resultPayload, "edits")
	argsEdits := mapSlice(argsPayload, "edits")
	editCount := firstPositiveInt(mapInt(resultPayload, "edit_count"), len(resultEdits), len(argsEdits))

	oldPreview := mapString(resultPayload, "old_string_preview")
	newPreview := mapString(resultPayload, "new_string_preview")
	oldTruncated := mapBool(resultPayload, "old_string_truncated")
	newTruncated := mapBool(resultPayload, "new_string_truncated")

	if len(resultEdits) == 0 {
		if oldPreview == "" {
			oldPreview, oldTruncated = compactEditHistoryPreview(firstNonEmptyString(
				mapString(resultPayload, "old_string"),
				mapString(argsPayload, "old_string"),
			))
		}
		if newPreview == "" {
			newPreview, newTruncated = compactEditHistoryPreview(firstNonEmptyString(
				mapString(resultPayload, "new_string"),
				mapString(argsPayload, "new_string"),
			))
		}
	}

	summary := firstNonEmptyString(
		mapString(resultPayload, "summary"),
		mapString(argsPayload, "summary"),
		summarizePlainToolOutput(strings.TrimSpace(output), maxToolPreviewChars, 1),
	)

	if path == "" && matches <= 0 && replacements <= 0 && editCount <= 0 && oldPreview == "" && newPreview == "" && summary == "" {
		return "", false
	}

	preview := map[string]any{
		"tool":         "edit",
		"path":         path,
		"matches":      matches,
		"replacements": replacements,
		"replace_all":  replaceAll,
		"path_id":      firstNonEmptyString(mapString(resultPayload, "path_id"), mapString(argsPayload, "path_id")),
		"summary":      summary,
	}
	if len(resultEdits) > 0 {
		preview["edit_count"] = firstPositiveInt(editCount, len(resultEdits))
		preview["edits"] = resultEdits
	} else {
		if editCount > 1 {
			preview["edit_count"] = editCount
		}
		if oldPreview != "" || newPreview != "" {
			preview["old_string_preview"] = oldPreview
			preview["new_string_preview"] = newPreview
			preview["old_string_truncated"] = oldTruncated
			preview["new_string_truncated"] = newTruncated
		}
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func mapSlice(source map[string]any, key string) []any {
	if source == nil {
		return nil
	}
	value, ok := source[key]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	return items
}

func jsonObjectSlice(payload map[string]any, key string) []map[string]any {
	items := mapSlice(payload, key)
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		typed, ok := item.(map[string]any)
		if !ok || len(typed) == 0 {
			continue
		}
		out = append(out, typed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactEditHistoryPreview(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	if value == "" {
		return "", false
	}
	value = strings.ReplaceAll(value, "\n", "\\n")
	const maxRunes = 240
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return truncateRunes(value, maxRunes), true
}

func summarizeToolOutput(name, output string, maxChars, maxLines int) string {
	name = strings.ToLower(strings.TrimSpace(name))
	raw := strings.TrimSpace(output)
	if raw == "" {
		return ""
	}
	if payload := decodeToolPayload(raw); payload != nil {
		if name == "ask-user" || name == "ask_user" || name == "permission" || name == "exit-plan-mode" || name == "exit_plan_mode" || name == "task" || name == "websearch" {
			// Keep structured decision payloads intact so UI and later model turns can consume rich context.
			encoded, err := json.Marshal(payload)
			if err == nil {
				return string(encoded)
			}
			return raw
		}
		if name == "edit" {
			if preview := mapString(payload, "old_string_preview"); preview != "" || len(mapSlice(payload, "edits")) > 0 || mapInt(payload, "edit_count") > 0 {
				encoded, err := json.Marshal(payload)
				if err == nil {
					return string(encoded)
				}
				return raw
			}
		}
		if name == "read" {
			if summary := summarizeReadToolPayload(payload); summary != "" {
				return summary
			}
		}
		if summary := mapString(payload, "summary"); summary != "" {
			return summary
		}
	}

	switch name {
	case "read":
		if summary := summarizeReadToolOutput(raw); summary != "" {
			return summary
		}
	case "write":
		if summary := summarizeWriteToolOutput(raw); summary != "" {
			return summary
		}
	case "bash":
		if summary := summarizeBashToolOutput(raw); summary != "" {
			return summary
		}
	case "glob":
		if summary := summarizeGlobToolOutput(raw); summary != "" {
			return summary
		}
	case "search":
		if summary := summarizeSearchToolOutput(raw); summary != "" {
			return summary
		}
	}

	return summarizePlainToolOutput(raw, maxChars, maxLines)
}

func summarizeReadToolOutput(raw string) string {
	payload := decodeToolPayload(raw)
	if payload == nil {
		return ""
	}
	return summarizeReadToolPayload(payload)
}

func summarizeReadToolPayload(payload map[string]any) string {
	path := mapString(payload, "path")
	lineStart := mapInt(payload, "line_start")
	count := mapInt(payload, "count")
	bytes := mapInt(payload, "bytes")
	truncated := mapBool(payload, "truncated")
	binarySuppressed := mapBool(payload, "binary_suppressed")

	if path == "" && lineStart <= 0 && count <= 0 && bytes <= 0 && !truncated && !binarySuppressed {
		return ""
	}

	label := "read"
	if path != "" {
		label += " " + path
	}

	if count > 0 {
		if lineStart <= 0 {
			lineStart = 1
		}
		lineEnd := lineStart + count - 1
		if count == 1 {
			label += fmt.Sprintf(" (line %d", lineStart)
		} else {
			label += fmt.Sprintf(" (lines %d-%d", lineStart, lineEnd)
		}
		if truncated {
			label += ", partial"
		}
		if binarySuppressed {
			label += ", binary output hidden"
		}
		return label + ")"
	}

	if count == 0 {
		label += " 0 lines"
		flags := make([]string, 0, 2)
		if truncated {
			flags = append(flags, "partial")
		}
		if binarySuppressed {
			flags = append(flags, "binary output hidden")
		}
		if len(flags) > 0 {
			label += " (" + strings.Join(flags, ", ") + ")"
		}
		return label
	}

	if bytes > 0 {
		label += fmt.Sprintf(" (%d bytes", bytes)
		if truncated {
			label += ", partial"
		}
		if binarySuppressed {
			label += ", binary output hidden"
		}
		return label + ")"
	}

	if truncated || binarySuppressed {
		flags := make([]string, 0, 2)
		if truncated {
			flags = append(flags, "partial")
		}
		if binarySuppressed {
			flags = append(flags, "binary output hidden")
		}
		label += " (" + strings.Join(flags, ", ") + ")"
	}

	return label
}

func summarizeWriteToolOutput(raw string) string {
	payload := decodeToolPayload(raw)
	if payload == nil {
		return ""
	}
	path := mapString(payload, "path")
	written := mapInt(payload, "bytes_written")
	appendMode := mapBool(payload, "append")

	if path == "" && written <= 0 {
		return ""
	}

	label := "write"
	if appendMode {
		label = "append"
	}
	if path != "" {
		label += " " + path
	}
	if written > 0 {
		label += fmt.Sprintf(" (%d bytes)", written)
	}
	return label
}

func summarizeBashToolOutput(raw string) string {
	payload := decodeToolPayload(raw)
	if payload == nil {
		return ""
	}
	command := mapString(payload, "command")
	exitCode := mapInt(payload, "exit_code")
	timedOut := mapBool(payload, "timed_out")
	truncated := mapBool(payload, "truncated")
	output := strings.TrimSpace(mapString(payload, "output"))

	if command == "" && output == "" && !timedOut && exitCode == 0 {
		return ""
	}

	label := "bash"
	if command != "" {
		label += " " + truncateRunes(command, 80)
	}
	notes := make([]string, 0, 3)
	switch {
	case timedOut:
		notes = append(notes, "timed out")
	case exitCode != 0:
		notes = append(notes, "failed")
	}
	if truncated {
		notes = append(notes, "partial output")
	}
	if output != "" {
		notes = append(notes, summarizePlainToolOutput(output, 120, 1))
	}
	return summarizeWithNotes(label, notes...)
}

func summarizeGlobToolOutput(raw string) string {
	payload := decodeToolPayload(raw)
	if payload == nil {
		return ""
	}
	pattern := mapString(payload, "pattern")
	root := mapString(payload, "path")
	count := mapInt(payload, "count")
	truncated := mapBool(payload, "truncated")
	timedOut := mapBool(payload, "timed_out")

	if pattern == "" && root == "" && count <= 0 {
		return ""
	}
	label := "glob"
	if pattern != "" {
		label += " " + fmt.Sprintf("%q", truncateRunes(pattern, 80))
	}
	if root != "" {
		label += " in " + root
	}
	notes := []string{countSummaryLabel(count, "file", "files")}
	if timedOut {
		notes = append(notes, "timed out")
	} else if truncated {
		notes = append(notes, "partial results")
	}
	return summarizeWithNotes(label, notes...)
}

func summarizeSearchToolOutput(raw string) string {
	payload := decodeToolPayload(raw)
	if payload == nil {
		return ""
	}
	pattern := mapString(payload, "pattern")
	queries := mapStringSlice(payload, "queries")
	root := mapString(payload, "path")
	count := mapInt(payload, "count")
	truncated := mapBool(payload, "truncated")
	timedOut := mapBool(payload, "timed_out")
	mode := strings.ToLower(strings.TrimSpace(mapString(payload, "search_mode")))
	if pattern == "" && len(queries) == 0 && root == "" && count <= 0 {
		return ""
	}
	label := "search"
	if len(queries) > 1 {
		label += " [" + countSummaryLabel(len(queries), "query", "queries") + "]"
	} else {
		query := pattern
		if len(queries) == 1 {
			query = strings.TrimSpace(queries[0])
		}
		if query != "" {
			label += " " + fmt.Sprintf("%q", truncateRunes(query, 80))
		}
	}
	if root != "" {
		label += " in " + root
	}
	flags := []string{countSummaryLabel(count, "file", "files")}
	if mode == "content" {
		flags[0] = countSummaryLabel(count, "match", "matches")
	}
	if timedOut {
		flags = append(flags, "timed out")
	} else if truncated {
		flags = append(flags, "partial results")
	}
	return summarizeWithNotes(label, flags...)
}

func summarizeGrepToolOutput(raw string) string {
	return summarizeSearchToolOutput(raw)
}

func countSummaryLabel(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func summarizeWithNotes(label string, notes ...string) string {
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

func summarizePlainToolOutput(raw string, maxChars, maxLines int) string {
	if maxChars <= 0 {
		maxChars = maxToolPreviewChars
	}
	if maxLines <= 0 {
		maxLines = 2
	}

	parts := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	lines := make([]string, 0, maxLines)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines = append(lines, truncateRunes(part, maxChars))
		if len(lines) >= maxLines {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, " | ")
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func decodeToolPayload(raw string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func mapString(payload map[string]any, key string) string {
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

func mapStringSlice(payload map[string]any, key string) []string {
	value, ok := payload[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			out = append(out, entry)
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			str := strings.TrimSpace(fmt.Sprint(entry))
			if str == "" {
				continue
			}
			out = append(out, str)
		}
		return out
	default:
		return nil
	}
}

func mapBool(payload map[string]any, key string) bool {
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

func mapInt(payload map[string]any, key string) int {
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

func cloneGenericMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = cloneGenericValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneGenericValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneGenericMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneGenericValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneGenericMap(item))
		}
		return out
	default:
		return value
	}
}

func sessionGitMetadata(existing map[string]any) map[string]any {
	metadata := cloneGenericMap(existing)
	if metadata == nil {
		metadata = make(map[string]any, 4)
	}
	gitMeta, _ := metadata["git"].(map[string]any)
	gitMeta = cloneGenericMap(gitMeta)
	if gitMeta == nil {
		gitMeta = make(map[string]any, 8)
	}
	metadata["git"] = gitMeta
	return metadata
}

func detectGitCommit(call tool.Call, result tool.Result) (map[string]any, bool) {
	toolName := strings.ToLower(strings.TrimSpace(call.Name))
	if toolName == "git_commit" {
		payload := decodeToolPayload(result.Output)
		if payload == nil {
			payload = decodeToolPayload(result.Error)
		}
		if payload == nil || mapInt(payload, "exit_code") != 0 {
			return nil, false
		}
		argv := mapStringSlice(payload, "argv")
		return map[string]any{
			"detected": true,
			"command":  strings.Join(argv, " "),
		}, true
	}
	if toolName != "bash" {
		return nil, false
	}
	record := buildToolHistoryRecord(call, nil, result)
	command := bashCommandFromArguments(record.Arguments)
	if !bashCommandIncludesGitCommit(command) {
		return nil, false
	}
	payload := decodeToolPayload(record.Output)
	if payload == nil {
		payload = decodeToolPayload(record.CompletedOutput)
	}
	if payload == nil || mapInt(payload, "exit_code") != 0 {
		return nil, false
	}
	return map[string]any{
		"detected": true,
		"command":  command,
	}, true
}

func bashCommandFromArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(mapString(payload, "command"))
}

func bashCommandIncludesGitCommit(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return false
	}
	return strings.Contains(command, "git commit")
}

func sessionGitCommitCount(metadata map[string]any) int {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return 0
	}
	return mapInt(gitMeta, "commit_count")
}

func SessionGitCommitCount(metadata map[string]any) int {
	return sessionGitCommitCount(metadata)
}

func metadataGitCommitDetected(metadata map[string]any) bool {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return false
	}
	return mapBool(gitMeta, "commit_detected")
}

func MetadataGitCommitDetected(metadata map[string]any) bool {
	return metadataGitCommitDetected(metadata)
}

func metadataGitStatusFields(metadata map[string]any) (gitStatusResponseFields, bool) {
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return gitStatusResponseFields{}, false
	}
	statusMeta, _ := gitMeta["status"].(map[string]any)
	if statusMeta == nil {
		return gitStatusResponseFields{}, false
	}
	branch := strings.TrimSpace(mapString(statusMeta, "branch"))
	if branch == "-" {
		branch = ""
	}
	return gitStatusResponseFields{
		GitBranch:             branch,
		GitHasGit:             mapBool(statusMeta, "has_git"),
		GitClean:              mapBool(statusMeta, "clean"),
		GitDirtyCount:         mapInt(statusMeta, "dirty_count"),
		GitStagedCount:        mapInt(statusMeta, "staged_count"),
		GitModifiedCount:      mapInt(statusMeta, "modified_count"),
		GitUntrackedCount:     mapInt(statusMeta, "untracked_count"),
		GitConflictCount:      mapInt(statusMeta, "conflict_count"),
		GitAheadCount:         mapInt(statusMeta, "ahead_count"),
		GitBehindCount:        mapInt(statusMeta, "behind_count"),
		GitCommittedFileCount: mapInt(statusMeta, "committed_file_count"),
		GitCommittedAdditions: mapInt(statusMeta, "committed_additions"),
		GitCommittedDeletions: mapInt(statusMeta, "committed_deletions"),
	}, true
}

func MetadataGitStatusFields(metadata map[string]any) (gitStatusResponseFields, bool) {
	return metadataGitStatusFields(metadata)
}

func buildGitStatusMetadata(workspacePath, baseBranch string) (map[string]any, bool) {
	status := gitstatus.ForWorktreePath(workspacePath, baseBranch)
	if !status.HasGit {
		return nil, false
	}
	branch := strings.TrimSpace(status.Branch)
	if branch == "-" {
		branch = ""
	}
	return map[string]any{
		"branch":               branch,
		"has_git":              status.HasGit,
		"clean":                status.DirtyCount == 0,
		"dirty_count":          status.DirtyCount,
		"staged_count":         status.StagedCount,
		"modified_count":       status.ModifiedCount,
		"untracked_count":      status.UntrackedCount,
		"conflict_count":       status.ConflictCount,
		"ahead_count":          status.AheadCount,
		"behind_count":         status.BehindCount,
		"committed_file_count": status.CommittedFileCount,
		"committed_additions":  status.CommittedAdditions,
		"committed_deletions":  status.CommittedDeletions,
	}, true
}
