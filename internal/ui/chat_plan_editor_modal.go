package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) openPlanEditorModal(plan ChatSessionPlan) {
	p.openPlanEditorModalWithPlans(plan, nil, strings.TrimSpace(plan.ID))
}

func (p *ChatPage) openPlanEditorModalWithPlans(plan ChatSessionPlan, revisions []ChatSessionPlan, activePlanID string) {
	activePlanID = strings.TrimSpace(activePlanID)
	if activePlanID == "" {
		activePlanID = strings.TrimSpace(plan.ID)
	}
	plan = normalizePlanEditorPlan(plan, activePlanID)
	items := normalizePlanEditorRevisions(plan, revisions, activePlanID)
	if len(items) == 0 {
		items = []ChatSessionPlan{plan}
	}

	p.planEditorVisible = true
	p.planEditorPlan = items[0]
	p.planEditorPlans = items
	p.planEditorActivePlanID = activePlanID
	p.planEditorPlanSelection = 0
	p.planEditorPlanScroll = 0
	p.planEditorRevisionFocus = false
	p.planEditorInput = planEditorDisplayText(items[0])
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectCopy
	p.planEditorScroll = 0
	p.planEditorInputScroll = 0
	p.planEditorCancelRect = Rect{}
	p.planEditorCopyRect = Rect{}
	p.planEditorSaveRect = Rect{}
	if title := strings.TrimSpace(items[0].Title); title != "" {
		p.statusLine = fmt.Sprintf("current plan: %s", title)
	} else {
		p.statusLine = "current plan: no active plan"
	}
}

func normalizePlanEditorRevisions(current ChatSessionPlan, revisions []ChatSessionPlan, activePlanID string) []ChatSessionPlan {
	current = normalizePlanEditorPlan(current, activePlanID)
	activeKey := planEditorRevisionKey(current, 0)
	if strings.TrimSpace(current.ID) == strings.TrimSpace(activePlanID) && activeKey != "" {
		current.Active = true
	}
	out := []ChatSessionPlan{current}
	seen := map[string]bool{activeKey: true}
	for _, revision := range revisions {
		normalized := normalizePlanEditorPlan(revision, activePlanID)
		if strings.TrimSpace(normalized.ID) == "" && strings.TrimSpace(normalized.Title) == "" && strings.TrimSpace(normalized.Plan) == "" && normalized.Document == nil {
			continue
		}
		key := planEditorRevisionKey(normalized, len(out))
		if seen[key] {
			continue
		}
		if strings.TrimSpace(normalized.ID) == strings.TrimSpace(activePlanID) {
			normalized.Active = key == activeKey
		}
		seen[key] = true
		out = append(out, normalized)
	}
	return out
}

func normalizePlanEditorPlans(plans []ChatSessionPlan, current ChatSessionPlan, activePlanID string) []ChatSessionPlan {
	return normalizePlanEditorRevisions(current, plans, activePlanID)
}

func normalizePlanEditorPlan(plan ChatSessionPlan, activePlanID string) ChatSessionPlan {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.Title = strings.TrimSpace(plan.Title)
	if plan.Title == "" {
		plan.Title = "Plan"
	}
	plan.Plan = strings.ReplaceAll(plan.Plan, "\r\n", "\n")
	plan.Plan = strings.ReplaceAll(plan.Plan, "\r", "\n")
	plan.Status = strings.TrimSpace(plan.Status)
	plan.ApprovalState = strings.TrimSpace(plan.ApprovalState)
	plan.UpdateSummary = strings.TrimSpace(plan.UpdateSummary)
	plan.UpdateScope = strings.TrimSpace(plan.UpdateScope)
	plan.UpdateKind = strings.TrimSpace(plan.UpdateKind)
	return plan
}

func (p *ChatPage) selectedPlanEditorPlan() ChatSessionPlan {
	if p == nil || len(p.planEditorPlans) == 0 {
		return p.planEditorPlan
	}
	idx := p.planEditorPlanSelection
	if idx < 0 {
		idx = 0
	}
	if idx >= len(p.planEditorPlans) {
		idx = len(p.planEditorPlans) - 1
	}
	return p.planEditorPlans[idx]
}

func (p *ChatPage) planEditorSelectedPlanActive() bool {
	plan := p.selectedPlanEditorPlan()
	return plan.Active
}

func (p *ChatPage) selectPlanEditorPlan(delta int) bool {
	if p == nil || len(p.planEditorPlans) == 0 {
		return false
	}
	next := p.planEditorPlanSelection + delta
	if next < 0 {
		next = 0
	}
	if next >= len(p.planEditorPlans) {
		next = len(p.planEditorPlans) - 1
	}
	if next == p.planEditorPlanSelection {
		return false
	}
	p.planEditorPlanSelection = next
	p.loadSelectedPlanEditorPlan()
	p.statusLine = fmt.Sprintf("previewing %s (Enter to activate)", planEditorRevisionLabel(p.planEditorPlan, next))
	return true
}

func (p *ChatPage) loadSelectedPlanEditorPlan() {
	plan := normalizePlanEditorPlan(p.selectedPlanEditorPlan(), p.planEditorActivePlanID)
	p.planEditorPlan = plan
	p.planEditorInput = planEditorDisplayText(plan)
	p.planEditorInputScroll = 0
	p.planEditorScroll = 0
	p.planEditorSelection = chatPlanEditorSelectCopy
	p.statusLine = fmt.Sprintf("viewing %s", planEditorRevisionLabel(plan, p.planEditorPlanSelection))
}

func (p *ChatPage) activateSelectedPlanEditorPlan() {
	if p == nil || len(p.planEditorPlans) == 0 {
		return
	}
	p.loadSelectedPlanEditorPlan()
	plan := p.planEditorPlan
	if strings.TrimSpace(plan.ID) == "" {
		p.statusLine = "selected revision has no plan id"
		return
	}
	plan.RestoreRevision = p.planEditorPlanSelection > 0
	p.pendingChatAction = &ChatAction{Kind: ChatActionActivatePlan, Plan: plan}
	p.closePlanEditorModal()
	p.statusLine = fmt.Sprintf("activating %s...", planEditorRevisionLabel(plan, p.planEditorPlanSelection))
}

func (p *ChatPage) planEditorModalActive() bool {
	return p.planEditorVisible
}

func (p *ChatPage) closePlanEditorModal() {
	p.planEditorVisible = false
	p.planEditorPlan = ChatSessionPlan{}
	p.planEditorPlans = nil
	p.planEditorActivePlanID = ""
	p.planEditorPlanSelection = 0
	p.planEditorPlanScroll = 0
	p.planEditorRevisionFocus = false
	p.planEditorInput = ""
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectCopy
	p.planEditorScroll = 0
	p.planEditorInputScroll = 0
	p.planEditorCancelRect = Rect{}
	p.planEditorCopyRect = Rect{}
	p.planEditorSaveRect = Rect{}
}

func (p *ChatPage) handlePlanEditorModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.planEditorModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.planEditorCancelRect.Contains(x, y):
			p.resolvePlanEditorModal(chatPlanEditorActionCancel)
			return true
		case p.planEditorCopyRect.Contains(x, y):
			p.resolvePlanEditorModal(chatPlanEditorActionCopy)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftPlanEditorScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftPlanEditorScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handlePlanEditorModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.planEditorModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	switch {
	case p.keybinds.Match(ev, KeybindPlanExitCancel):
		p.resolvePlanEditorModal(chatPlanEditorActionCancel)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight), p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
		p.planEditorRevisionFocus = false
		if p.planEditorSelection == chatPlanEditorSelectCancel {
			p.planEditorSelection = chatPlanEditorSelectCopy
		} else {
			p.planEditorSelection = chatPlanEditorSelectCancel
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp), p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		if p.planEditorRevisionFocus {
			p.selectPlanEditorPlan(-1)
		} else {
			p.shiftPlanEditorScroll(-1)
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown), p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		if p.planEditorRevisionFocus {
			p.selectPlanEditorPlan(1)
		} else {
			p.shiftPlanEditorScroll(1)
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageUp):
		p.shiftPlanEditorScroll(-6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitPageDown):
		p.shiftPlanEditorScroll(6)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpHome):
		p.planEditorScroll = 0
		p.planEditorInputScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindPlanExitJumpEnd):
		p.planEditorScroll = 1 << 30
		p.planEditorInputScroll = 1 << 30
		return true
	case p.keybinds.Match(ev, KeybindPlanExitConfirm):
		if p.planEditorRevisionFocus {
			p.activateSelectedPlanEditorPlan()
			return true
		}
		if p.planEditorSelection == chatPlanEditorSelectCancel {
			p.resolvePlanEditorModal(chatPlanEditorActionCancel)
		} else {
			p.resolvePlanEditorModal(chatPlanEditorActionCopy)
		}
		return true
	case ev.Key() == tcell.KeyRune:
		switch strings.ToLower(string(ev.Rune())) {
		case "c":
			p.resolvePlanEditorModal(chatPlanEditorActionCopy)
			return true
		case "r":
			p.planEditorRevisionFocus = true
			p.statusLine = "revision selector focused (↑/↓ preview, Enter activate)"
			return true
		}
	}
	return true
}

func (p *ChatPage) drawPlanEditorModal(s tcell.Screen, screen Rect) {
	if !p.planEditorModalActive() || screen.W < 40 || screen.H < 12 {
		return
	}
	modalW := minInt(132, screen.W-4)
	if modalW < 56 {
		modalW = screen.W - 2
	}
	if modalW < 40 {
		return
	}
	modalH := minInt(40, screen.H-4)
	if modalH < 16 {
		modalH = screen.H - 2
	}
	if modalH < 12 {
		return
	}
	modal := Rect{X: maxInt(1, (screen.W-modalW)/2), Y: maxInt(1, (screen.H-modalH)/2), W: modalW, H: modalH}
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	FillRect(s, modal, p.theme.Panel)
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	revisionLabel := planEditorRevisionLabel(p.planEditorPlan, p.planEditorPlanSelection)
	if len(p.planEditorPlans) == 0 {
		revisionLabel = "Current revision"
	}
	revisionText := "R " + revisionLabel
	revisionStyle := onPanel(p.theme.TextMuted)
	if p.planEditorRevisionFocus {
		revisionStyle = onPanel(p.theme.Warning.Bold(true))
		revisionText = "› " + revisionText
	}
	if p.planEditorSelectedPlanActive() {
		revisionText += " (active)"
	}
	revisionW := minInt(maxInt(18, utf8.RuneCountInString(revisionText)+2), modal.W/2)
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, revisionW, revisionStyle, clampEllipsis(revisionText, revisionW))

	header := "Current Plan"
	if title := strings.TrimSpace(p.planEditorPlan.Title); title != "" {
		header = title
	}
	titleW := modal.W - 5 - revisionW
	if titleW < 12 {
		titleW = modal.W - 4
	}
	DrawText(s, modal.X+2, modal.Y+1, titleW, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, titleW))
	subtitle := "Ask the AI to update plans; TUI direct editing is disabled for now."
	if len(p.planEditorPlans) > 1 {
		subtitle = "Press r for revisions, ↑/↓ preview, Enter activate. Ask the AI to update plans."
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 4, W: modal.W - 4, H: modal.H - 7}
	maxScroll := p.drawPlanEditorDocument(s, bodyRect, onPanel)

	helpY := modal.Y + modal.H - 3
	helpText := "↑/↓ scroll • R revisions • Tab buttons • C copy • Esc close"
	if p.planEditorRevisionFocus {
		helpText = "Revisions focused • ↑/↓ preview • Enter activate • Tab buttons • Esc close"
	}
	helpWidth := modal.W - 4
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.planEditorInputScroll+1, maxScroll+1)
		scrollWidth := utf8.RuneCountInString(scrollLabel)
		DrawTextRight(s, modal.X+modal.W-2, helpY, maxInt(scrollWidth, modal.W/2), onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, modal.W/2))
		remaining := modal.W - 4 - scrollWidth - 2
		if remaining > 12 {
			helpWidth = remaining
		}
	}
	DrawText(s, modal.X+2, helpY, helpWidth, onPanel(p.theme.TextMuted), clampEllipsis(helpText, helpWidth))

	buttonY := modal.Y + modal.H - 2
	labels := []string{"Esc Close", "C Copy"}
	styles := []tcell.Style{p.theme.TextMuted, p.theme.Accent}
	rects := []*Rect{&p.planEditorCancelRect, &p.planEditorCopyRect}
	p.planEditorSaveRect = Rect{}
	selection := p.planEditorSelection
	if selection < 0 || selection >= len(styles) || p.planEditorRevisionFocus {
		selection = -1
	}
	for i := range styles {
		if i == selection {
			styles[i] = p.theme.Warning
		}
	}
	x := modal.X + 2
	for i, label := range labels {
		var nextX int
		*rects[i], nextX = drawPermissionActionButton(s, x, buttonY, modal.X+modal.W-2, label, styles[i])
		x = nextX
	}
	if p.planEditorCancelRect.W == 0 && p.planEditorCopyRect.W == 0 {
		DrawText(s, modal.X+2, buttonY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Esc close • C copy", modal.W-4))
	}
}

func (p *ChatPage) drawPlanEditorDocument(s tcell.Screen, rect Rect, onPanel func(tcell.Style) tcell.Style) int {
	if rect.W <= 0 || rect.H <= 0 {
		return 0
	}
	doc := structuredPlanDocumentMap(p.planEditorPlan.Document)
	wide := rect.W >= 86 && len(doc) > 0
	if wide {
		leftW := rect.W / 3
		if leftW < 28 {
			leftW = 28
		}
		if leftW > 44 {
			leftW = 44
		}
		rightW := rect.W - leftW - 3
		if rightW < 36 {
			wide = false
		} else {
			leftRect := Rect{X: rect.X, Y: rect.Y, W: leftW, H: rect.H}
			rightRect := Rect{X: rect.X + leftW + 3, Y: rect.Y, W: rightW, H: rect.H}
			for y := rect.Y; y < rect.Y+rect.H; y++ {
				s.SetContent(rect.X+leftW+1, y, '│', nil, onPanel(p.theme.Border))
			}
			leftLines := p.planEditorDetailLines(doc, maxInt(1, leftRect.W))
			rightLines := p.planEditorCheckpointLines(doc, maxInt(1, rightRect.W))
			maxScroll := maxInt(maxInt(0, len(leftLines)-leftRect.H), maxInt(0, len(rightLines)-rightRect.H))
			p.clampPlanEditorScroll(maxScroll)
			p.drawPlanEditorLines(s, leftRect, leftLines, p.planEditorInputScroll, onPanel)
			p.drawPlanEditorLines(s, rightRect, rightLines, p.planEditorInputScroll, onPanel)
			return maxScroll
		}
	}

	lines := p.planEditorFallbackLines(rect.W)
	if len(doc) > 0 {
		lines = append(p.planEditorDetailLines(doc, rect.W), planEditorRenderLine{Text: "", Style: p.theme.Text})
		lines = append(lines, p.planEditorCheckpointLines(doc, rect.W)...)
	}
	maxScroll := maxInt(0, len(lines)-rect.H)
	p.clampPlanEditorScroll(maxScroll)
	p.drawPlanEditorLines(s, rect, lines, p.planEditorInputScroll, onPanel)
	return maxScroll
}

type planEditorRenderLine struct {
	Text  string
	Style tcell.Style
}

func (p *ChatPage) drawPlanEditorLines(s tcell.Screen, rect Rect, lines []planEditorRenderLine, scroll int, onPanel func(tcell.Style) tcell.Style) {
	if scroll < 0 {
		scroll = 0
	}
	for row := 0; row < rect.H; row++ {
		idx := scroll + row
		if idx >= len(lines) {
			break
		}
		line := lines[idx]
		DrawText(s, rect.X, rect.Y+row, rect.W, onPanel(line.Style), clampEllipsis(line.Text, rect.W))
	}
	if len(lines) == 0 {
		DrawText(s, rect.X, rect.Y, rect.W, onPanel(p.theme.TextMuted), "No active plan yet.")
	}
}

func (p *ChatPage) planEditorDetailLines(doc map[string]any, width int) []planEditorRenderLine {
	lines := []planEditorRenderLine{{Text: "Plan Details", Style: p.theme.Secondary.Bold(true)}}
	add := func(text string, style tcell.Style) {
		lines = appendWrappedPlanEditorLine(lines, text, width, style)
	}
	if title := firstNonEmptyToolValue(mapStringArg(doc, "title"), mapStringArg(doc, "id")); title != "" {
		add(title, p.theme.Text.Bold(true))
	}
	if status := mapStringArg(doc, "status"); status != "" {
		add("Status: "+quietPlanStatus(status), p.theme.TextMuted)
	}
	info, _ := doc["info"].(map[string]any)
	appendPlanEditorField(&lines, width, "Goal", mapStringArg(info, "goal"), p.theme.Text)
	appendPlanEditorField(&lines, width, "Scope", firstNonEmptyToolValue(mapStringArg(info, "scope"), mapStringArg(info, "context")), p.theme.Text)
	appendPlanEditorList(&lines, width, "Decisions", mapAnyStringSlice(info, "decisions"), p.theme.Text)
	appendPlanEditorList(&lines, width, "Files", firstNonEmptyStringSlice(mapAnyStringSlice(info, "relevant_files"), mapAnyStringSlice(info, "relevantFiles"), mapAnyStringSlice(info, "files")), p.theme.Text)
	appendPlanEditorField(&lines, width, "Validation", firstNonEmptyToolValue(mapStringArg(info, "validation_strategy"), mapStringArg(info, "validationStrategy"), mapStringArg(info, "validation"), strings.Join(mapAnyStringSlice(info, "validation"), "; ")), p.theme.Text)
	if len(lines) == 1 {
		lines = appendWrappedPlanEditorLine(lines, strings.TrimSpace(p.planEditorPlan.Plan), width, p.theme.Text)
	}
	return lines
}

func (p *ChatPage) planEditorCheckpointLines(doc map[string]any, width int) []planEditorRenderLine {
	checkpoints := mapAnyObjectSlice(doc, "checkpoints")
	sort.SliceStable(checkpoints, func(i, j int) bool { return mapAnyInt(checkpoints[i], "order") < mapAnyInt(checkpoints[j], "order") })
	lines := []planEditorRenderLine{{Text: fmt.Sprintf("Checkpoints (%d)", len(checkpoints)), Style: p.theme.Secondary.Bold(true)}}
	if len(checkpoints) == 0 {
		return append(lines, planEditorRenderLine{Text: "No checkpoints yet.", Style: p.theme.TextMuted})
	}
	activeID := firstNonEmptyToolValue(mapStringArg(doc, "active_checkpoint_id"), mapStringArg(doc, "activeCheckpointId"))
	for idx, checkpoint := range checkpoints {
		order := mapAnyInt(checkpoint, "order")
		if order <= 0 {
			order = idx + 1
		}
		title := firstNonEmptyToolValue(mapStringArg(checkpoint, "title"), mapStringArg(checkpoint, "id"), "Untitled checkpoint")
		status := quietPlanStatus(mapStringArg(checkpoint, "status"))
		heading := fmt.Sprintf("%d. %s", order, title)
		if status != "" {
			heading += " · " + status
		}
		style := p.theme.Text.Bold(true)
		if activeID != "" && activeID == mapStringArg(checkpoint, "id") {
			heading = "› " + heading
			style = p.theme.Warning.Bold(true)
		}
		if idx > 0 {
			lines = append(lines, planEditorRenderLine{Text: "", Style: p.theme.Text})
		}
		lines = appendWrappedPlanEditorLine(lines, heading, width, style)
		appendPlanEditorField(&lines, width, "Objective", mapStringArg(checkpoint, "objective"), p.theme.Text)
		appendPlanEditorList(&lines, width, "Tasks", mapAnyStringSlice(checkpoint, "tasks"), p.theme.Text)
		appendPlanEditorList(&lines, width, "Acceptance", firstNonEmptyStringSlice(mapAnyStringSlice(checkpoint, "acceptance_criteria"), mapAnyStringSlice(checkpoint, "acceptanceCriteria")), p.theme.Text)
		appendPlanEditorField(&lines, width, "Notes", mapStringArg(checkpoint, "notes"), p.theme.Text)
		appendPlanEditorField(&lines, width, "Report", mapStringArg(checkpoint, "report"), p.theme.Text)
		appendPlanEditorField(&lines, width, "Result", mapStringArg(checkpoint, "result"), p.theme.Text)
		appendPlanEditorList(&lines, width, "Changed files", firstNonEmptyStringSlice(mapAnyStringSlice(checkpoint, "changed_files"), mapAnyStringSlice(checkpoint, "changedFiles")), p.theme.Text)
		appendPlanEditorList(&lines, width, "Validation", mapAnyStringSlice(checkpoint, "validation"), p.theme.Text)
	}
	return lines
}

func (p *ChatPage) planEditorFallbackLines(width int) []planEditorRenderLine {
	lines := []planEditorRenderLine{{Text: "Plan", Style: p.theme.Secondary.Bold(true)}}
	text := strings.TrimSpace(p.planEditorInput)
	if text == "" {
		return append(lines, planEditorRenderLine{Text: "No active plan yet.", Style: p.theme.TextMuted})
	}
	return appendWrappedPlanEditorLine(lines, text, width, p.theme.Text)
}

func appendPlanEditorField(lines *[]planEditorRenderLine, width int, label, value string, style tcell.Style) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*lines = appendWrappedPlanEditorLine(*lines, label+": "+value, width, style)
}

func appendPlanEditorList(lines *[]planEditorRenderLine, width int, label string, values []string, style tcell.Style) {
	if len(values) == 0 {
		return
	}
	*lines = appendWrappedPlanEditorLine(*lines, label+":", width, style.Bold(true))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			*lines = appendWrappedPlanEditorLine(*lines, "- "+value, width, style)
		}
	}
}

func appendWrappedPlanEditorLine(lines []planEditorRenderLine, text string, width int, style tcell.Style) []planEditorRenderLine {
	if width <= 0 {
		width = 1
	}
	if strings.TrimSpace(text) == "" {
		return append(lines, planEditorRenderLine{Text: "", Style: style})
	}
	wrapped := wrapWithCustomPrefixes("", "  ", text, width)
	if len(wrapped) == 0 {
		return append(lines, planEditorRenderLine{Text: "", Style: style})
	}
	for _, line := range wrapped {
		lines = append(lines, planEditorRenderLine{Text: line, Style: style})
	}
	return lines
}

func structuredPlanDocumentMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func quietPlanStatus(status string) string {
	status = strings.TrimSpace(strings.ReplaceAll(status, "_", " "))
	if status == "" {
		return ""
	}
	parts := strings.Fields(strings.ToLower(status))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func planEditorDisplayText(plan ChatSessionPlan) string {
	if documentText := StructuredPlanDocumentTextFromValue(plan.Document); strings.TrimSpace(documentText) != "" {
		return documentText
	}
	return strings.TrimSpace(plan.Plan)
}

func planEditorRevisionKey(plan ChatSessionPlan, idx int) string {
	if doc := structuredPlanDocumentMap(plan.Document); len(doc) > 0 {
		if revision := firstNonEmptyToolValue(mapStringArg(doc, "revision_id"), mapStringArg(doc, "revisionId")); revision != "" {
			return revision
		}
	}
	if plan.Version > 0 {
		return fmt.Sprintf("%s:v%d", strings.TrimSpace(plan.ID), plan.Version)
	}
	if id := strings.TrimSpace(plan.ID); id != "" {
		return fmt.Sprintf("%s:%d", id, idx)
	}
	return fmt.Sprintf("revision:%d", idx)
}

func planEditorRevisionLabel(plan ChatSessionPlan, idx int) string {
	label := "Current revision"
	if idx > 0 {
		if plan.Version > 0 {
			label = fmt.Sprintf("Revision %d", plan.Version)
		} else {
			label = fmt.Sprintf("Revision %d", idx)
		}
	}
	summary := firstNonEmptyToolValue(plan.UpdateSummary, plan.UpdateKind, plan.UpdateScope)
	if summary != "" {
		label += " — " + summary
	}
	return label
}

func (p *ChatPage) planEditorDirty() bool { return false }

func (p *ChatPage) shiftPlanEditorScroll(delta int) {
	p.planEditorScroll += delta
	if p.planEditorScroll < 0 {
		p.planEditorScroll = 0
	}
	p.planEditorInputScroll += delta
	if p.planEditorInputScroll < 0 {
		p.planEditorInputScroll = 0
	}
}

func (p *ChatPage) clampPlanEditorScroll(maxScroll int) {
	if p.planEditorInputScroll < 0 {
		p.planEditorInputScroll = 0
		p.planEditorScroll = 0
		return
	}
	if p.planEditorInputScroll > maxScroll {
		p.planEditorInputScroll = maxScroll
	}
	p.planEditorScroll = p.planEditorInputScroll
}

type chatPlanEditorAction string

const (
	chatPlanEditorSelectCancel = 0
	chatPlanEditorSelectCopy   = 1
	chatPlanEditorSelectSave   = 2
)

const (
	chatPlanEditorActionCancel chatPlanEditorAction = "cancel"
	chatPlanEditorActionCopy   chatPlanEditorAction = "copy"
	chatPlanEditorActionSave   chatPlanEditorAction = "save"
)

func (p *ChatPage) resolvePlanEditorModal(action chatPlanEditorAction) {
	plan := p.planEditorPlan
	text := planEditorDisplayText(plan)
	p.closePlanEditorModal()
	switch action {
	case chatPlanEditorActionCopy:
		content := strings.TrimRight(text, "\n")
		if content == "" {
			content = strings.TrimSpace(plan.Plan)
		}
		if content == "" {
			p.statusLine = "no current plan to copy"
			return
		}
		if p.copyTextFn == nil {
			p.statusLine = "copy unavailable"
			return
		}
		if err := p.copyTextFn(content); err != nil {
			p.statusLine = fmt.Sprintf("copy failed: %v", err)
			p.ShowToast(ToastError, fmt.Sprintf("copy failed: %v", err))
			return
		}
		p.statusLine = "copied current plan to clipboard"
		p.ShowToast(ToastSuccess, "copied current plan to clipboard")
	default:
		p.statusLine = "current plan closed"
	}
}
