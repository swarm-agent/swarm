package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

type bashPermissionIntent struct {
	Command     string
	Explanation []string
	Category    string
	Critical    bool
}

// permissionCardModel is tool-agnostic. Tool presenters supply content while
// the shared component owns the themed card surface and layout.
type permissionCardModel struct {
	Title      string
	Badge      string
	Meta       string
	Content    []permissionCardLine
	FooterRows int
}

type permissionCardLine struct {
	Text  string
	Style tcell.Style
}

func normalizePermissionToolName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "functions.")
	return strings.ReplaceAll(value, "-", "_")
}

func parseBashPermissionIntent(record client.PermissionRecord) (bashPermissionIntent, bool) {
	if normalizePermissionToolName(record.ToolName) != "bash" {
		return bashPermissionIntent{}, false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(record.ToolArguments)), &payload) != nil || payload == nil {
		return bashPermissionIntent{}, false
	}
	command, _ := payload["command"].(string)
	category, _ := payload["category"].(string)
	critical, hasCritical := payload["critical"].(bool)
	command = strings.TrimSpace(command)
	category = strings.ToLower(strings.TrimSpace(category))
	var explanation []string
	if raw, ok := payload["explanation"].([]any); ok {
		for _, item := range raw {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				explanation = append(explanation, strings.TrimSpace(text))
			}
		}
	}
	if command == "" || len(explanation) == 0 || !hasCritical {
		return bashPermissionIntent{}, false
	}
	switch category {
	case "read", "write", "update":
	default:
		return bashPermissionIntent{}, false
	}
	return bashPermissionIntent{Command: command, Explanation: explanation, Category: category, Critical: critical}, true
}

func permissionCardModelForRecord(record client.PermissionRecord, pendingCount, width int, styles PageStyles, prefixPreview string) permissionCardModel {
	if normalizePermissionToolName(record.ToolName) == "bash" {
		return bashPermissionCardModel(record, pendingCount, width, styles, prefixPreview)
	}
	if intent, ok := parsePlanPermissionIntent(record); ok {
		return planPermissionCardModel(record, intent, pendingCount, width, styles)
	}
	mode := strings.TrimSpace(record.Mode)
	if mode == "" {
		mode = "auto"
	}
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	meta := permissionCardMeta(record, pendingCount, mode)
	model := permissionCardModel{Title: toolName + " permission", Meta: meta, FooterRows: permissionCardFooterRows(record)}
	model.Content = append(model.Content, permissionCardLine{Text: "REQUEST", Style: styles.Muted.Bold(true)})
	arguments := strings.TrimSpace(record.ToolArguments)
	if arguments == "" {
		arguments = "{}"
	}
	for _, line := range wrapText(arguments, maxInt(1, width)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
	}
	return model
}

type planPermissionIntent struct {
	Title       string
	Summary     string
	Goal        string
	Document    map[string]any
	Checkpoints []map[string]any
}

func isPlanProposalRequirement(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "plan_new_request", "plan_revision_request", "plan_amendment_request", "plan_followup_request":
		return true
	default:
		return false
	}
}

func parsePlanPermissionIntent(record client.PermissionRecord) (planPermissionIntent, bool) {
	if normalizePermissionToolName(record.ToolName) != "plan_manage" || !isPlanProposalRequirement(record.Requirement) {
		return planPermissionIntent{}, false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(record.ToolArguments)), &payload) != nil || payload == nil {
		return planPermissionIntent{}, false
	}
	document := toolObject(payload, "document")
	if document == nil {
		if approved := toolObject(payload, "approved_arguments"); approved != nil {
			document = toolObject(approved, "document")
		}
	}
	if document == nil {
		return planPermissionIntent{}, false
	}
	info := toolObject(document, "info")
	return planPermissionIntent{
		Title:       firstNonEmptyToolRaw(toolString(payload, "title"), toolString(document, "title"), toolString(info, "goal"), "Plan proposal"),
		Summary:     firstNonEmptyToolRaw(toolString(payload, "update_summary"), toolString(payload, "summary")),
		Goal:        toolString(info, "goal"),
		Document:    document,
		Checkpoints: toolObjectSlice(document, "checkpoints"),
	}, true
}

func planPermissionCardModel(record client.PermissionRecord, intent planPermissionIntent, pendingCount, width int, styles PageStyles) permissionCardModel {
	mode := firstNonEmpty(strings.TrimSpace(record.Mode), "auto")
	model := permissionCardModel{Title: "Plan approval", Badge: "PLAN", Meta: planPermissionCardMeta(record, pendingCount, mode), FooterRows: permissionCardFooterRows(record)}
	model.Content = append(model.Content, permissionCardLine{Text: intent.Title, Style: styles.Text.Bold(true)})
	if intent.Goal != "" && !strings.EqualFold(intent.Goal, intent.Title) {
		for _, line := range wrapText(intent.Goal, maxInt(1, width)) {
			model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
		}
	} else if intent.Summary != "" && !strings.EqualFold(intent.Summary, intent.Title) {
		for _, line := range wrapText(intent.Summary, maxInt(1, width)) {
			model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Muted})
		}
	}
	if len(intent.Checkpoints) > 0 {
		model.Content = append(model.Content, permissionCardLine{
			Text:  "CHECKPOINTS  ·  " + toolCountLabel(len(intent.Checkpoints), "checkpoint", "checkpoints"),
			Style: styles.Muted.Bold(true),
		})
		for index, checkpoint := range intent.Checkpoints {
			order := toolInt(checkpoint, "order")
			if order <= 0 {
				order = index + 1
			}
			title := firstNonEmptyToolRaw(toolString(checkpoint, "title"), toolString(checkpoint, "id"), "Untitled checkpoint")
			status := humanizePlanStatus(firstNonEmptyToolRaw(toolString(checkpoint, "status"), "pending"))
			for _, line := range wrapText(fmt.Sprintf("%d. %s  ·  %s", order, title, status), maxInt(1, width)) {
				model.Content = append(model.Content, permissionCardLine{Text: line, Style: planCheckpointCardStyle(status, styles)})
			}
		}
		model.Content = append(model.Content, permissionCardLine{Text: "/plan  Open full plan", Style: styles.Muted})
	}
	return model
}

func planCheckpointCardStyle(status string, styles PageStyles) tcell.Style {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "approved":
		return styles.Success
	case "blocked", "paused", "waiting review", "needs review":
		return styles.Warning
	case "failed", "cancelled", "canceled":
		return styles.Error
	case "in progress", "running":
		return styles.Accent.Bold(true)
	default:
		return styles.Text
	}
}

func bashPermissionCardModel(record client.PermissionRecord, pendingCount, width int, styles PageStyles, prefixPreview string) permissionCardModel {
	mode := strings.TrimSpace(record.Mode)
	if mode == "" {
		mode = "auto"
	}
	meta := permissionCardMeta(record, pendingCount, mode)
	model := permissionCardModel{Title: "Bash permission", Meta: meta, FooterRows: permissionCardFooterRows(record)}
	intent, ok := parseBashPermissionIntent(record)
	if !ok {
		model.Content = []permissionCardLine{{
			Text:  "Invalid Bash request: explanation, read/write/update category, and critical flag are required.",
			Style: styles.Error,
		}}
		return model
	}
	model.Badge = intent.Category
	if intent.Critical {
		model.Content = append(model.Content,
			permissionCardLine{Text: "! PAY ATTENTION BEFORE APPROVING", Style: styles.Warning.Bold(true)},
			permissionCardLine{Text: "The AI marked this command as critical.", Style: styles.Warning},
			permissionCardLine{Text: "", Style: styles.Muted},
		)
	}
	model.Content = append(model.Content, permissionCardLine{Text: "WHAT THIS COMMAND WILL DO", Style: styles.Muted.Bold(true)})
	for _, item := range intent.Explanation {
		for index, line := range wrapText(item, maxInt(1, width-2)) {
			prefix := "  "
			if index == 0 {
				prefix = "• "
			}
			model.Content = append(model.Content, permissionCardLine{Text: prefix + line, Style: styles.Text})
		}
	}
	model.Content = append(model.Content,
		permissionCardLine{Text: "", Style: styles.Muted},
		permissionCardLine{Text: "COMMAND", Style: styles.Muted.Bold(true)},
	)
	for _, line := range wrapText(intent.Command, maxInt(1, width)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
	}
	if strings.TrimSpace(prefixPreview) == "" {
		prefixPreview = record.SavedRulePreview
	}
	model.Content = append(model.Content,
		permissionCardLine{Text: "", Style: styles.Muted},
		permissionCardLine{Text: "ALWAYS ALLOW PREFIX", Style: styles.Muted.Bold(true)},
		permissionCardLine{Text: bashPermissionPreviewPrefix(prefixPreview), Style: styles.Accent},
		permissionCardLine{Text: "Future Bash commands starting with this prefix will be approved automatically.", Style: styles.Muted},
	)
	if !permissionPending(record) {
		model.Content = append(model.Content,
			permissionCardLine{Text: "", Style: styles.Muted},
			permissionCardLine{Text: "RESOLVED", Style: styles.Muted.Bold(true)},
			permissionCardLine{Text: permissionResolvedLabel(record), Style: permissionResolvedStyle(record, styles).Bold(true)},
		)
		if reason := strings.TrimSpace(record.Reason); reason != "" {
			model.Content = append(model.Content, permissionCardLine{Text: "Note: " + reason, Style: styles.Muted})
		}
	}
	return model
}

func permissionPending(record client.PermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Status), "pending")
}

func permissionCardMeta(record client.PermissionRecord, pendingCount int, mode string) string {
	state := "Resolved · " + permissionResolvedLabel(record)
	if permissionPending(record) {
		state = "Approval required"
	}
	meta := fmt.Sprintf("%s  ·  %s  ·  mode %s", state, permissionRequirementLabel(record.Requirement), mode)
	if permissionPending(record) && pendingCount > 1 {
		meta += fmt.Sprintf("  ·  %d pending", pendingCount)
	}
	return meta
}

func planPermissionCardMeta(record client.PermissionRecord, pendingCount int, mode string) string {
	if permissionPending(record) {
		return permissionCardMeta(record, pendingCount, mode)
	}
	state := "Resolved · " + permissionResolvedLabel(record)
	switch strings.ToLower(strings.TrimSpace(record.ExecutionStatus)) {
	case "running":
		state = "Approved · Running"
	case "completed":
		state = "Approved · Completed"
	case "failed":
		state = "Approved · Failed"
	case "cancelled", "canceled":
		state = "Approved · Cancelled"
	case "skipped":
		state = "Resolved · Skipped"
	}
	return fmt.Sprintf("%s  ·  %s  ·  mode %s", state, permissionRequirementLabel(record.Requirement), mode)
}

func permissionCardFooterRows(record client.PermissionRecord) int {
	if permissionPending(record) {
		return 4
	}
	return 1
}

func permissionResolvedLabel(record client.PermissionRecord) string {
	decision := strings.ToLower(strings.TrimSpace(record.Decision))
	switch decision {
	case "allow_once":
		return "Approved once"
	case "allow_always":
		return "Always allowed"
	case "deny_once":
		return "Denied once"
	case "deny_always":
		return "Always denied"
	}
	status := strings.TrimSpace(record.Status)
	if status == "" {
		return "Resolved"
	}
	return strings.ToUpper(status[:1]) + status[1:]
}

func permissionResolvedStyle(record client.PermissionRecord, styles PageStyles) tcell.Style {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(record.Decision)), "deny") {
		return styles.Error
	}
	return styles.Success
}

type permissionCardAction struct {
	Label  string
	Action string
	Tone   tcell.Style
}

type permissionCardView struct {
	Model     permissionCardModel
	Selected  bool
	Pending   bool
	Note      string
	Busy      bool
	ErrorText string
	Actions   []permissionCardAction
}

func inlinePermissionCardRows(record client.PermissionRecord, pendingCount, width int, styles PageStyles, prefixPreview string, selected bool, note []rune, busy bool, errorText string) []renderRow {
	view := permissionCardView{
		Model:     permissionCardModelForRecord(record, pendingCount, maxInt(1, width-4), styles, prefixPreview),
		Selected:  selected,
		Pending:   permissionPending(record),
		Note:      string(note),
		Busy:      busy,
		ErrorText: errorText,
		Actions: []permissionCardAction{
			{Label: "Enter Approve", Action: "allow_once", Tone: styles.Success},
			{Label: "Esc Deny", Action: "deny_once", Tone: styles.Error},
			{Label: "Ctrl+A Always Allow", Action: "allow_always", Tone: styles.Accent},
			{Label: "Ctrl+D Always Deny", Action: "deny_always", Tone: styles.Warning},
		},
	}
	return permissionCardRows(view, width, styles)
}

// permissionCardRows is the shared themed permission-card renderer. Tool
// presenters provide a model and actions; this component owns the surface,
// focus border, badge, footer layout, and mouse targets.
func permissionCardRows(view permissionCardView, width int, styles PageStyles) []renderRow {
	if width < 24 {
		return nil
	}
	surface := styles.Background
	borderTone := styles.Border
	if view.Selected {
		borderTone = styles.BorderActive
	}
	border := styleOnPermissionCard(borderTone, surface)
	text := styleOnPermissionCard(styles.Text, surface)
	rows := make([]renderRow, 0, len(view.Model.Content)+10)

	appendEdge := func(left, middle, right string) {
		rows = append(rows, renderRow{
			text:  left + strings.Repeat(middle, maxInt(0, width-2)) + right,
			style: border,
		})
	}
	appendBody := func(spans []renderSpan, actions []renderActionTarget) {
		used := 2
		body := []renderSpan{{text: "│ ", style: border}}
		var rowText strings.Builder
		rowText.WriteString("│ ")
		for _, span := range spans {
			remaining := maxInt(0, width-1-used)
			if remaining == 0 {
				break
			}
			span.text = truncateRunes(span.text, remaining)
			if !span.keepBackground {
				span.style = styleOnPermissionCard(span.style, surface)
			}
			body = append(body, span)
			rowText.WriteString(span.text)
			used += utf8.RuneCountInString(span.text)
		}
		padding := strings.Repeat(" ", maxInt(0, width-1-used))
		body = append(body, renderSpan{text: padding + "│", style: border})
		rowText.WriteString(padding + "│")
		rows = append(rows, renderRow{text: rowText.String(), style: surface, spans: body, actions: actions})
	}
	appendText := func(value string, style tcell.Style) {
		appendBody([]renderSpan{{text: truncateRunes(value, maxInt(0, width-3)), style: style}}, nil)
	}

	appendEdge("┌", "─", "┐")
	title := strings.TrimSpace(view.Model.Title)
	badge := strings.ToUpper(strings.TrimSpace(view.Model.Badge))
	headerSpans := []renderSpan{{text: truncateRunes(title, maxInt(1, width-4)), style: styles.Text.Bold(true)}}
	if badge != "" {
		badgeText := " " + badge + " "
		innerWidth := maxInt(1, width-3)
		title = truncateRunes(title, maxInt(1, innerWidth-utf8.RuneCountInString(badgeText)-1))
		gap := maxInt(1, innerWidth-utf8.RuneCountInString(title)-utf8.RuneCountInString(badgeText))
		headerSpans = []renderSpan{
			{text: title, style: styles.Text.Bold(true)},
			{text: strings.Repeat(" ", gap), style: surface},
			{text: badgeText, style: permissionButtonStyle(styles.Secondary), keepBackground: true},
		}
	}
	appendBody(headerSpans, nil)
	appendText(strings.TrimSpace(view.Model.Meta), styles.Muted)
	appendEdge("├", "─", "┤")
	for _, line := range view.Model.Content {
		appendText(line.Text, line.Style)
	}

	if view.Pending && view.Selected {
		appendEdge("├", "─", "┤")
		if strings.TrimSpace(view.ErrorText) != "" {
			appendText("error · "+strings.TrimSpace(view.ErrorText), styles.Error)
		} else {
			appendBody([]renderSpan{
				{text: "note › ", style: styles.Muted},
				{text: view.Note, style: styles.Text},
			}, nil)
		}
		if view.Busy {
			appendText("Resolving permission…", styles.Muted)
		} else {
			var spans []renderSpan
			var targets []renderActionTarget
			x := 2
			for _, action := range view.Actions {
				label := " " + strings.TrimSpace(action.Label) + " "
				labelWidth := utf8.RuneCountInString(label)
				if x+labelWidth >= width-1 {
					break
				}
				spans = append(spans, renderSpan{text: label, style: permissionButtonStyle(action.Tone), keepBackground: true})
				targets = append(targets, renderActionTarget{x: x, width: labelWidth, action: action.Action})
				x += labelWidth
				if x+1 < width-1 {
					spans = append(spans, renderSpan{text: " ", style: surface})
					x++
				}
			}
			appendBody(spans, targets)
		}
	}
	appendEdge("└", "─", "┘")
	rows = append(rows, renderRow{text: "", style: text})
	return rows
}

func permissionButtonStyle(tone tcell.Style) tcell.Style {
	background, _, attributes := tone.Decompose()
	if !background.Valid() || background == tcell.ColorDefault {
		background = tcell.ColorWhite
	}
	r, g, b := background.TrueColor().RGB()
	foreground := tcell.ColorWhite
	if (299*r+587*g+114*b)/1000 >= 160 {
		foreground = tcell.ColorBlack
	}
	return tcell.StyleDefault.Foreground(foreground).Background(background).Attributes(attributes | tcell.AttrBold)
}

func styleOnPermissionCard(style, surface tcell.Style) tcell.Style {
	foreground, _, attributes := style.Decompose()
	_, background, _ := surface.Decompose()
	return tcell.StyleDefault.Foreground(foreground).Background(background).Attributes(attributes)
}

func permissionRequirementLabel(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), "-", " "))
	if value == "" || strings.EqualFold(value, "tool") || strings.EqualFold(value, "permission") {
		return "tool permission"
	}
	return value
}

func bashPermissionPreviewPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "available after approval"
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"allow bash command prefix:", "deny bash command prefix:", "allow bash prefix:", "deny bash prefix:"} {
		if index := strings.Index(lower, marker); index >= 0 {
			return strings.TrimSpace(value[index+len(marker):])
		}
	}
	return value
}
