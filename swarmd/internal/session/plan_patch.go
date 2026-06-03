package session

import (
	"errors"
	"fmt"
	"strings"
)

type PlanPatch struct {
	Operation     string
	Section       string
	OldText       string
	NewText       string
	Text          string
	ChecklistItem string
	Checked       *bool
	ReplaceAll    bool
}

func (p PlanPatch) IsZero() bool {
	return strings.TrimSpace(p.OldText) == "" && strings.TrimSpace(p.NewText) == "" && strings.TrimSpace(p.Text) == "" && strings.TrimSpace(p.ChecklistItem) == "" && strings.TrimSpace(p.Section) == "" && p.Checked == nil
}

func ApplyPlanPatch(plan string, patch PlanPatch) (string, error) {
	operation := strings.ToLower(strings.TrimSpace(patch.Operation))
	operation = strings.ReplaceAll(operation, "-", "_")
	if operation == "" {
		switch {
		case strings.TrimSpace(patch.Section) != "" && strings.TrimSpace(patch.NewText) != "":
			operation = "replace_section"
		case strings.TrimSpace(patch.OldText) != "" || strings.TrimSpace(patch.NewText) != "":
			operation = "replace_text"
		case strings.TrimSpace(patch.ChecklistItem) != "":
			operation = "append_checklist_item"
		case strings.TrimSpace(patch.Text) != "":
			operation = "append_text"
		}
	}

	switch operation {
	case "replace_text", "replace", "replace_paragraph", "update_paragraph":
		return applyPlanReplaceText(plan, patch)
	case "append_text", "append":
		return appendPlanText(plan, patch.Text)
	case "replace_section", "update_section":
		return replacePlanSection(plan, patch.Section, firstNonBlank(patch.NewText, patch.Text))
	case "append_to_section", "append_section":
		return appendToPlanSection(plan, patch.Section, patch.Text)
	case "append_checklist_item", "append_checklist":
		return appendPlanChecklistItem(plan, patch)
	case "set_checkbox", "update_checkbox", "check", "uncheck":
		if patch.Checked == nil {
			checked := operation == "check"
			patch.Checked = &checked
		}
		return setPlanCheckbox(plan, patch)
	default:
		if operation == "" {
			return "", errors.New("plan patch requires an operation such as replace_text, replace_section, append_to_section, append_checklist_item, or set_checkbox")
		}
		return "", fmt.Errorf("unsupported plan patch operation %q", patch.Operation)
	}
}

func applyPlanReplaceText(plan string, patch PlanPatch) (string, error) {
	oldText := patch.OldText
	if oldText == "" {
		return "", errors.New("replace_text plan patch requires old_text")
	}
	count := strings.Count(plan, oldText)
	if count == 0 {
		return "", errors.New("replace_text plan patch old_text was not found")
	}
	if count > 1 && !patch.ReplaceAll {
		return "", fmt.Errorf("replace_text plan patch old_text matched %d times; provide a more specific old_text or set replace_all=true", count)
	}
	limit := 1
	if patch.ReplaceAll {
		limit = -1
	}
	return strings.Replace(plan, oldText, patch.NewText, limit), nil
}

func appendPlanText(plan, text string) (string, error) {
	text = strings.Trim(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", errors.New("append_text plan patch requires text")
	}
	plan = strings.TrimRight(normalizePlanNewlines(plan), "\n")
	if strings.TrimSpace(plan) == "" {
		return text, nil
	}
	return plan + "\n\n" + text, nil
}

func replacePlanSection(plan, section, content string) (string, error) {
	section = strings.TrimSpace(section)
	if section == "" {
		return "", errors.New("replace_section plan patch requires section")
	}
	lines := splitPlanLines(normalizePlanNewlines(plan))
	start, end, headingLine, _, ok := findPlanSection(lines, section)
	if !ok {
		return "", fmt.Errorf("replace_section plan patch section %q was not found", section)
	}
	content = strings.Trim(content, "\n")
	var replacement []string
	if strings.HasPrefix(strings.TrimSpace(content), "#") {
		replacement = splitPlanLines(content)
	} else {
		replacement = []string{headingLine}
		if content != "" {
			replacement = append(replacement, "")
			replacement = append(replacement, splitPlanLines(content)...)
		}
	}
	out := make([]string, 0, len(lines)-(end-start)+len(replacement))
	out = append(out, lines[:start]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}

func appendToPlanSection(plan, section, text string) (string, error) {
	text = strings.Trim(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", errors.New("append_to_section plan patch requires text")
	}
	if strings.TrimSpace(section) == "" {
		return appendPlanText(plan, text)
	}
	lines := splitPlanLines(normalizePlanNewlines(plan))
	_, end, _, _, ok := findPlanSection(lines, section)
	if !ok {
		return "", fmt.Errorf("append_to_section plan patch section %q was not found", section)
	}
	insert := splitPlanLines(text)
	out := make([]string, 0, len(lines)+len(insert)+1)
	out = append(out, lines[:end]...)
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, insert...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}

func appendPlanChecklistItem(plan string, patch PlanPatch) (string, error) {
	item := strings.TrimSpace(firstNonBlank(patch.ChecklistItem, patch.Text))
	if item == "" {
		return "", errors.New("append_checklist_item plan patch requires checklist_item or text")
	}
	item = strings.TrimPrefix(item, "- [ ]")
	item = strings.TrimPrefix(item, "- [x]")
	item = strings.TrimPrefix(item, "- [X]")
	item = strings.TrimSpace(item)
	if item == "" {
		return "", errors.New("append_checklist_item plan patch requires a non-empty checklist item")
	}
	checked := false
	if patch.Checked != nil {
		checked = *patch.Checked
	}
	marker := "[ ]"
	if checked {
		marker = "[x]"
	}
	return appendToPlanSection(plan, patch.Section, "- "+marker+" "+item)
}

func setPlanCheckbox(plan string, patch PlanPatch) (string, error) {
	if patch.Checked == nil {
		return "", errors.New("set_checkbox plan patch requires checked")
	}
	target := strings.TrimSpace(firstNonBlank(patch.ChecklistItem, patch.Text, patch.OldText))
	if target == "" {
		return "", errors.New("set_checkbox plan patch requires checklist_item or text")
	}
	lines := splitPlanLines(normalizePlanNewlines(plan))
	match := -1
	for i, line := range lines {
		if !lineHasMarkdownCheckbox(line) {
			continue
		}
		if strings.Contains(line, target) || strings.Contains(stripMarkdownCheckbox(line), target) {
			if match >= 0 {
				return "", fmt.Errorf("set_checkbox plan patch target %q matched multiple checklist items", target)
			}
			match = i
		}
	}
	if match < 0 {
		return "", fmt.Errorf("set_checkbox plan patch target %q was not found", target)
	}
	lines[match] = replaceCheckboxMarker(lines[match], *patch.Checked)
	return strings.Join(lines, "\n"), nil
}

func findPlanSection(lines []string, target string) (start, end int, headingLine string, level int, ok bool) {
	normalizedTarget := normalizeSectionTarget(target)
	for i, line := range lines {
		candidateLevel, text, isHeading := parsePlanHeading(line)
		if !isHeading {
			continue
		}
		if strings.EqualFold(normalizeSectionTarget(text), normalizedTarget) || strings.EqualFold(normalizeSectionTarget(line), normalizedTarget) {
			end = len(lines)
			for j := i + 1; j < len(lines); j++ {
				nextLevel, _, nextHeading := parsePlanHeading(lines[j])
				if nextHeading && nextLevel <= candidateLevel {
					end = j
					break
				}
			}
			return i, end, line, candidateLevel, true
		}
	}
	return 0, 0, "", 0, false
}

func parsePlanHeading(line string) (int, string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) || trimmed[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(trimmed[level+1:]), true
}

func normalizeSectionTarget(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "#") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	}
	return value
}

func normalizePlanNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func lineHasMarkdownCheckbox(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "[ ]") || strings.Contains(lower, "[x]")
}

func stripMarkdownCheckbox(line string) string {
	line = strings.Replace(line, "[ ]", "", 1)
	line = strings.Replace(line, "[x]", "", 1)
	line = strings.Replace(line, "[X]", "", 1)
	line = strings.TrimPrefix(strings.TrimSpace(line), "-")
	return strings.TrimSpace(line)
}

func replaceCheckboxMarker(line string, checked bool) string {
	marker := "[ ]"
	if checked {
		marker = "[x]"
	}
	lower := strings.ToLower(line)
	if idx := strings.Index(lower, "[ ]"); idx >= 0 {
		return line[:idx] + marker + line[idx+3:]
	}
	if idx := strings.Index(lower, "[x]"); idx >= 0 {
		return line[:idx] + marker + line[idx+3:]
	}
	return line
}
