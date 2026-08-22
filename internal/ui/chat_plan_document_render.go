package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func StructuredPlanDocumentTextFromValue(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return StructuredPlanDocumentTextFromJSON(raw)
}

func StructuredPlanDocumentTextFromJSON(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload) == 0 {
		return ""
	}
	return structuredPlanDocumentText(payload)
}

func structuredPlanDocumentText(doc map[string]any) string {
	var lines []string
	title := firstNonEmptyToolValue(mapStringArg(doc, "title"), mapStringArg(doc, "id"), "Structured plan")
	lines = append(lines, "Structured plan: "+title)
	if id := mapStringArg(doc, "id"); id != "" {
		lines = append(lines, "ID: "+id)
	}
	if status := mapStringArg(doc, "status"); status != "" {
		lines = append(lines, "Status: "+status)
	}
	if revision := firstNonEmptyToolValue(mapStringArg(doc, "revision_id"), mapStringArg(doc, "revisionId")); revision != "" {
		lines = append(lines, "Revision: "+revision)
	}
	if active := firstNonEmptyToolValue(mapStringArg(doc, "active_checkpoint_id"), mapStringArg(doc, "activeCheckpointId")); active != "" {
		if label := planManageCheckpointDisplay(doc, map[string]any{"checkpoint_id": active}); label != "" {
			lines = append(lines, strings.Title(label)+" active")
		}
	}
	info, _ := doc["info"].(map[string]any)
	if len(info) > 0 {
		lines = append(lines, "", "Plan info:")
		appendPlanDocumentTextField(&lines, "Goal", mapStringArg(info, "goal"))
		appendPlanDocumentTextField(&lines, "Scope", mapStringArg(info, "scope"))
		appendPlanDocumentTextField(&lines, "Context", mapStringArg(info, "context"))
		appendPlanDocumentListField(&lines, "Decisions", mapAnyStringSlice(info, "decisions"))
		appendPlanDocumentListField(&lines, "Success criteria", firstNonEmptyStringSlice(mapAnyStringSlice(info, "success_criteria"), mapAnyStringSlice(info, "successCriteria")))
		appendPlanDocumentListField(&lines, "Constraints", mapAnyStringSlice(info, "constraints"))
		appendPlanDocumentListField(&lines, "Assumptions", mapAnyStringSlice(info, "assumptions"))
		appendPlanDocumentListField(&lines, "Open questions", firstNonEmptyStringSlice(mapAnyStringSlice(info, "open_questions"), mapAnyStringSlice(info, "openQuestions")))
		appendPlanDocumentListField(&lines, "Files", firstNonEmptyStringSlice(mapAnyStringSlice(info, "relevant_files"), mapAnyStringSlice(info, "relevantFiles"), mapAnyStringSlice(info, "files")))
		appendPlanDocumentTextField(&lines, "Validation", firstNonEmptyToolValue(mapStringArg(info, "validation_strategy"), mapStringArg(info, "validationStrategy"), mapStringArg(info, "validation"), strings.Join(mapAnyStringSlice(info, "validation"), "; ")))
	}
	checkpoints := mapAnyObjectSlice(doc, "checkpoints")
	if len(checkpoints) > 0 {
		sort.SliceStable(checkpoints, func(i, j int) bool { return mapAnyInt(checkpoints[i], "order") < mapAnyInt(checkpoints[j], "order") })
		lines = append(lines, "", fmt.Sprintf("Checkpoints (%d):", len(checkpoints)))
		for idx, checkpoint := range checkpoints {
			order := mapAnyInt(checkpoint, "order")
			if order <= 0 {
				order = idx + 1
			}
			heading := fmt.Sprintf("%d. %s", order, firstNonEmptyToolValue(mapStringArg(checkpoint, "title"), fmt.Sprintf("Checkpoint %d", order)))
			if status := mapStringArg(checkpoint, "status"); status != "" {
				if strings.EqualFold(mapStringArg(doc, "active_checkpoint_id"), mapStringArg(checkpoint, "id")) && (strings.EqualFold(status, "pending") || strings.EqualFold(status, "queued") || strings.EqualFold(status, "approved")) {
					status = "in_progress"
				}
				heading += " [" + status + "]"
			}
			lines = append(lines, heading)
			appendPlanDocumentIndentedField(&lines, "Objective", mapStringArg(checkpoint, "objective"))
			appendPlanDocumentIndentedList(&lines, "Tasks", mapAnyStringSlice(checkpoint, "tasks"))
			appendPlanDocumentIndentedList(&lines, "Acceptance", firstNonEmptyStringSlice(mapAnyStringSlice(checkpoint, "acceptance_criteria"), mapAnyStringSlice(checkpoint, "acceptanceCriteria")))
			appendPlanDocumentIndentedField(&lines, "Notes", mapStringArg(checkpoint, "notes"))
			appendPlanDocumentIndentedField(&lines, "Report", mapStringArg(checkpoint, "report"))
			appendPlanDocumentIndentedField(&lines, "Result", mapStringArg(checkpoint, "result"))
			appendPlanDocumentIndentedList(&lines, "Changed files", firstNonEmptyStringSlice(mapAnyStringSlice(checkpoint, "changed_files"), mapAnyStringSlice(checkpoint, "changedFiles")))
			appendPlanDocumentIndentedList(&lines, "Validation", mapAnyStringSlice(checkpoint, "validation"))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func appendPlanDocumentTextField(lines *[]string, label, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		*lines = append(*lines, label+": "+value)
	}
}

func appendPlanDocumentListField(lines *[]string, label string, values []string) {
	if len(values) == 0 {
		return
	}
	*lines = append(*lines, label+":")
	for _, value := range values {
		*lines = append(*lines, "- "+value)
	}
}

func appendPlanDocumentIndentedField(lines *[]string, label, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		*lines = append(*lines, "   "+label+": "+value)
	}
}

func appendPlanDocumentIndentedList(lines *[]string, label string, values []string) {
	if len(values) == 0 {
		return
	}
	*lines = append(*lines, "   "+label+":")
	for _, value := range values {
		*lines = append(*lines, "   - "+value)
	}
}

func firstNonEmptyStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func mapAnyStringSlice(payload map[string]any, key string) []string {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func mapAnyObjectSlice(payload map[string]any, key string) []map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}

func mapAnyInt(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}
