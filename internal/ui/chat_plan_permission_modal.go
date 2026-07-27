package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// OpenPlanPermissionModal opens the V3 plan approval surface. This state and
// component are intentionally separate from the plan viewer/editor and the
// legacy exit-plan and plan-update dialogs.
func (p *ChatPage) OpenPlanPermissionModal(record ChatPermissionRecord) bool {
	if classifyChatPermission(record) != chatPermissionDestinationPlanModal {
		return false
	}
	payload := decodePermissionArguments(record.ToolArguments)
	document := planPermissionObject(payload["document"])
	if len(document) == 0 {
		if approved := planPermissionObject(payload["approved_arguments"]); len(approved) > 0 {
			document = planPermissionObject(approved["document"])
		}
	}

	title := strings.TrimSpace(planPermissionString(payload, "title"))
	if title == "" {
		title = strings.TrimSpace(planPermissionString(document, "title"))
	}
	if title == "" {
		title = "Plan approval"
	}
	summary := strings.TrimSpace(firstNonEmptyToolValue(
		planPermissionString(payload, "update_summary"),
		planPermissionString(payload, "summary"),
		planPermissionString(payload, "plan"),
	))
	info := planPermissionObject(document["info"])
	goal := strings.TrimSpace(planPermissionString(info, "goal"))

	approved := canonicalPermissionApprovedArguments(record)
	if p.planEditorModalActive() {
		p.closePlanEditorModal()
	}
	if p.planUpdateModalActive() {
		p.closePlanUpdateModal()
	}
	if p.planExitModalActive() {
		p.closePlanExitModal()
	}

	p.planPermission = strings.TrimSpace(record.ID)
	p.planPermissionTitle = title
	p.planPermissionSummary = summary
	p.planPermissionGoal = goal
	p.planPermissionDocument = document
	p.planPermissionApproved = approved
	p.planPermissionScroll = 0
	p.planPermissionMaxScroll = 0
	p.planPermissionManual = false
	p.planPermissionApproveRect = Rect{}
	p.planPermissionDenyRect = Rect{}
	p.statusLine = "plan permission active"
	return p.planPermission != ""
}

func (p *ChatPage) planPermissionModalActive() bool {
	return strings.TrimSpace(p.planPermission) != ""
}

func (p *ChatPage) closePlanPermissionModal() {
	p.planPermission = ""
	p.planPermissionTitle = ""
	p.planPermissionSummary = ""
	p.planPermissionGoal = ""
	p.planPermissionDocument = nil
	p.planPermissionApproved = ""
	p.planPermissionScroll = 0
	p.planPermissionMaxScroll = 0
	p.planPermissionManual = false
	p.planPermissionApproveRect = Rect{}
	p.planPermissionDenyRect = Rect{}
}

func (p *ChatPage) handlePlanPermissionModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.planPermissionModalActive() {
		return false
	}
	switch ev.Key() {
	case tcell.KeyUp:
		p.shiftPlanPermissionScroll(-1)
	case tcell.KeyDown:
		p.shiftPlanPermissionScroll(1)
	case tcell.KeyPgUp:
		p.shiftPlanPermissionScroll(-6)
	case tcell.KeyPgDn:
		p.shiftPlanPermissionScroll(6)
	case tcell.KeyHome:
		p.planPermissionScroll = 0
	case tcell.KeyEnd:
		p.planPermissionScroll = p.planPermissionMaxScroll
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'm', 'M':
			p.planPermissionManual = !p.planPermissionManual
		case 'a', 'A':
			p.resolvePlanPermissionModal(true)
		case 'd', 'D':
			p.resolvePlanPermissionModal(false)
		}
	}
	return true
}

func (p *ChatPage) handlePlanPermissionModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.planPermissionModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.planPermissionApproveRect.Contains(x, y):
			p.resolvePlanPermissionModal(true)
		case p.planPermissionDenyRect.Contains(x, y):
			p.resolvePlanPermissionModal(false)
		}
		return true
	}
	if buttons&tcell.WheelUp != 0 {
		p.shiftPlanPermissionScroll(-1)
	} else if buttons&tcell.WheelDown != 0 {
		p.shiftPlanPermissionScroll(1)
	}
	return true
}

func (p *ChatPage) shiftPlanPermissionScroll(delta int) {
	p.planPermissionScroll += delta
	if p.planPermissionScroll < 0 {
		p.planPermissionScroll = 0
	}
	if p.planPermissionScroll > p.planPermissionMaxScroll {
		p.planPermissionScroll = p.planPermissionMaxScroll
	}
}

func (p *ChatPage) drawPlanPermissionModal(s tcell.Screen, screen Rect) {
	if !p.planPermissionModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	modalW := screen.W - 8
	if modalW > 112 {
		modalW = 112
	}
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	lines := p.planPermissionModalLines(modalW - 4)
	modalH := len(lines) + 8
	if modalH < 14 {
		modalH = 14
	}
	if maxH := screen.H - 4; modalH > maxH {
		modalH = maxH
	}
	if modalH < 12 {
		return
	}
	modal := Rect{X: maxInt(1, (screen.W-modalW)/2), Y: maxInt(1, (screen.H-modalH)/2), W: modalW, H: modalH}
	p.planPermissionApproveRect = Rect{}
	p.planPermissionDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis("Plan approval: "+p.planPermissionTitle, modal.W-4))

	mode := "automatic continuation"
	if p.planPermissionManual {
		mode = "manual checkpoint review"
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.Secondary.Bold(true)), clampEllipsis("m  Continuation: "+mode, modal.W-4))

	contentTop := modal.Y + 3
	contentH := modal.H - 7
	if contentH < 1 {
		contentH = 1
	}
	p.planPermissionMaxScroll = maxInt(0, len(lines)-contentH)
	p.shiftPlanPermissionScroll(0)
	for row := 0; row < contentH; row++ {
		idx := p.planPermissionScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, modal.X+2, contentTop+row, modal.W-4, lines[idx])
	}

	helpY := modal.Y + modal.H - 3
	help := "m toggle continuation  •  ↑/↓ scroll"
	if p.planPermissionMaxScroll > 0 {
		help += fmt.Sprintf("  •  %d/%d", p.planPermissionScroll+1, p.planPermissionMaxScroll+1)
	}
	DrawText(s, modal.X+2, helpY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(help, modal.W-4))

	denyLabel := "d Deny"
	approveLabel := "a Approve"
	denyW := utf8.RuneCountInString(denyLabel) + 2
	approveW := utf8.RuneCountInString(approveLabel) + 2
	gap := 2
	startX := modal.X + (modal.W-denyW-gap-approveW)/2
	buttonY := modal.Y + modal.H - 2
	denyStyle := filledButtonStyle(p.theme.Warning)
	approveStyle := filledButtonStyle(p.theme.Success)
	FillRect(s, Rect{X: startX, Y: buttonY, W: denyW, H: 1}, denyStyle)
	DrawCenteredText(s, startX, buttonY, denyW, denyStyle, denyLabel)
	approveX := startX + denyW + gap
	FillRect(s, Rect{X: approveX, Y: buttonY, W: approveW, H: 1}, approveStyle)
	DrawCenteredText(s, approveX, buttonY, approveW, approveStyle, approveLabel)
	p.planPermissionDenyRect = Rect{X: startX, Y: buttonY, W: denyW, H: 1}
	p.planPermissionApproveRect = Rect{X: approveX, Y: buttonY, W: approveW, H: 1}
}

func (p *ChatPage) planPermissionModalLines(width int) []chatRenderLine {
	if width < 8 {
		width = 8
	}
	lines := make([]chatRenderLine, 0, 48)
	appendText := func(text string, style tcell.Style) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, row := range wrapWithCustomPrefixes("", "", text, width) {
			lines = append(lines, chatRenderLine{Text: row, Style: styleForCurrentCellBackground(style)})
		}
	}
	appendList := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		appendText(label+":", p.theme.Secondary.Bold(true))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			for _, row := range wrapWithCustomPrefixes("  • ", "    ", value, width) {
				lines = append(lines, chatRenderLine{Text: row, Style: styleForCurrentCellBackground(p.theme.Text)})
			}
		}
	}

	if p.planPermissionSummary != "" {
		appendText("Summary: "+p.planPermissionSummary, p.theme.Text)
	}
	if p.planPermissionGoal != "" {
		appendText("Goal: "+p.planPermissionGoal, p.theme.Text.Bold(true))
	}
	checkpoints := planPermissionObjectSlice(p.planPermissionDocument["checkpoints"])
	sort.SliceStable(checkpoints, func(i, j int) bool {
		left, right := planPermissionInt(checkpoints[i], "order"), planPermissionInt(checkpoints[j], "order")
		if left <= 0 {
			left = i + 1
		}
		if right <= 0 {
			right = j + 1
		}
		return left < right
	})
	for i, checkpoint := range checkpoints {
		if len(lines) > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.TextMuted)})
		}
		order := planPermissionInt(checkpoint, "order")
		if order <= 0 {
			order = i + 1
		}
		title := strings.TrimSpace(firstNonEmptyToolValue(planPermissionString(checkpoint, "title"), planPermissionString(checkpoint, "id"), "Untitled checkpoint"))
		appendText(fmt.Sprintf("Checkpoint %d: %s", order, title), p.theme.Primary.Bold(true))
		if objective := strings.TrimSpace(planPermissionString(checkpoint, "objective")); objective != "" {
			appendText("Objective: "+objective, p.theme.Text)
		}
		appendList("Tasks", planPermissionStrings(checkpoint, "tasks"))
		criteria := planPermissionStrings(checkpoint, "acceptance_criteria")
		if len(criteria) == 0 {
			criteria = planPermissionStrings(checkpoint, "acceptanceCriteria")
		}
		appendList("Acceptance criteria", criteria)
		if notes := strings.TrimSpace(planPermissionString(checkpoint, "notes")); notes != "" {
			appendText("Notes: "+notes, p.theme.Text)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, chatRenderLine{Text: "No structured plan content was provided.", Style: styleForCurrentCellBackground(p.theme.TextMuted)})
	}
	return lines
}

func (p *ChatPage) resolvePlanPermissionModal(approve bool) {
	permissionID := strings.TrimSpace(p.planPermission)
	approvedArguments := ""
	if approve {
		approvedArguments = p.planPermissionApprovedArguments()
		if approvedArguments == "" {
			p.statusLine = "plan approval arguments unavailable"
			return
		}
	}
	p.closePlanPermissionModal()
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", "", approvedArguments)
		p.statusLine = "plan approved"
	} else {
		p.queueResolvePermissionByID(permissionID, "deny", "")
		p.statusLine = "plan denied"
	}
}

func (p *ChatPage) planPermissionApprovedArguments() string {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(p.planPermissionApproved)), &args); err != nil || args == nil {
		return ""
	}
	args["execution_granularity"] = "checkpointed"
	if p.planPermissionManual {
		args["continuation_policy"] = "review_each_checkpoint"
		args["continue_automatically"] = false
	} else {
		args["continuation_policy"] = "automatic"
		args["continue_automatically"] = true
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(raw)
}

func planPermissionObject(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		var object map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &object) == nil {
			return object
		}
	}
	return nil
}

func planPermissionObjectSlice(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object := planPermissionObject(item); len(object) > 0 {
			out = append(out, object)
		}
	}
	return out
}

func planPermissionString(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

func planPermissionStrings(object map[string]any, key string) []string {
	items, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func planPermissionInt(object map[string]any, key string) int {
	switch value := object[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}
