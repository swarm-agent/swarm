package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// renderPlanToolCardLines gives plan lifecycle tools a quiet, dedicated card in
// the transcript. The full structured document stays in the existing plan
// modal; the timeline only carries the summary needed to decide whether to
// open it with Ctrl+P or /plan.
func (p *ChatPage) renderPlanToolCardLines(message chatMessageItem, payload map[string]any, width int) []chatRenderLine {
	if p == nil || payload == nil || width < 12 {
		return nil
	}
	innerWidth := width - 4
	if innerWidth < 8 {
		return nil
	}

	state := strings.ToLower(strings.TrimSpace(message.ToolState))
	borderStyle := p.theme.Border
	headerStyle := p.theme.Secondary.Bold(true)
	if state == "running" {
		borderStyle = p.thinkingPulseStyle()
		headerStyle = p.thinkingPulseStyle().Bold(true)
	} else if state == "error" {
		borderStyle = p.theme.Error
		headerStyle = p.theme.Error.Bold(true)
	}

	plan := jsonObject(payload, "plan")
	document := planManageDocument(payload)
	info := jsonObject(document, "info")
	title := firstNonEmptyToolValue(
		jsonString(plan, "title"),
		jsonString(document, "title"),
		jsonString(info, "goal"),
		jsonString(payload, "title"),
		"Plan",
	)
	action := firstNonEmptyToolValue(
		jsonString(payload, "action"),
		jsonString(payload, "document_operation"),
		jsonString(payload, "update_kind"),
	)
	status := planManageDisplayStatus(action, payload, plan, document)
	checkpointCount := len(jsonObjectSlice(document, "checkpoints"))
	goal := strings.TrimSpace(jsonString(info, "goal"))
	checkpointDisplay := planManageCheckpointDisplay(document, payload)
	updateSummary := firstNonEmptyToolValue(
		jsonString(plan, "update_summary"),
		jsonString(plan, "updateSummary"),
		jsonString(payload, "update_summary"),
		jsonString(payload, "summary"),
	)

	headerParts := make([]string, 0, 4)
	if action != "" {
		headerParts = append(headerParts, planManageLifecycleActionDisplay(action, payload, plan, document))
	}
	if checkpointDisplay != "" {
		headerParts = append(headerParts, checkpointDisplay)
	}
	if checkpointCount > 0 {
		headerParts = append(headerParts, toolCountLabel(checkpointCount, "checkpoint", "checkpoints"))
	}
	if status != "" && !strings.EqualFold(status, "ok") {
		headerParts = append(headerParts, quietPlanStatus(status))
	}
	header := "PLAN"
	if len(headerParts) > 0 {
		header += "  ·  " + strings.Join(headerParts, "  ·  ")
	}

	content := make([]chatRenderLine, 0, 7)
	content = append(content, chatRenderLine{Text: clampEllipsis(header, innerWidth), Style: headerStyle})
	content = append(content, chatRenderLine{Text: clampEllipsis(title, innerWidth), Style: p.theme.Text.Bold(true)})
	if goal != "" && !strings.EqualFold(goal, title) {
		content = appendPlanCardWrappedLines(content, goal, innerWidth, p.theme.Text)
	} else if updateSummary != "" && !strings.EqualFold(updateSummary, title) {
		content = appendPlanCardWrappedLines(content, updateSummary, innerWidth, p.theme.TextMuted)
	}
	if len(document) > 0 {
		content = append(content, chatRenderLine{Text: "Ctrl+P or /plan  Open full plan", Style: p.theme.TextMuted})
	}

	return planCardBoxLines(content, width, borderStyle)
}

func appendPlanCardWrappedLines(lines []chatRenderLine, text string, width int, style tcell.Style) []chatRenderLine {
	for _, row := range wrapWithCustomPrefixes("", "", strings.TrimSpace(text), width) {
		lines = append(lines, chatRenderLine{Text: row, Style: style})
		if len(lines) >= 6 {
			break
		}
	}
	return lines
}

func planCardBoxLines(content []chatRenderLine, width int, borderStyle tcell.Style) []chatRenderLine {
	if width < 4 {
		return content
	}
	innerWidth := width - 2
	out := make([]chatRenderLine, 0, len(content)+2)
	out = append(out, chatRenderLine{Text: "┌" + strings.Repeat("─", innerWidth) + "┐", Style: borderStyle})
	for _, line := range content {
		text := fitLeft(line.Text, innerWidth)
		out = append(out, chatRenderLine{
			Text:  fmt.Sprintf("│%s│", text),
			Style: line.Style,
			Spans: []chatRenderSpan{
				{Text: "│", Style: borderStyle},
				{Text: text, Style: line.Style},
				{Text: "│", Style: borderStyle},
			},
		})
	}
	out = append(out, chatRenderLine{Text: "└" + strings.Repeat("─", innerWidth) + "┘", Style: borderStyle})
	return out
}
