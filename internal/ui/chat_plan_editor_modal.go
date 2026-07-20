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
	p.planEditorRecoveryFocus = 0
	p.planEditorCheckpoint = planEditorInitialCheckpoint(items[0])
	p.planEditorManualReview = false
	p.planEditorRecoveryAction = 0
	p.planEditorInput = planEditorDisplayText(items[0])
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectCopy
	p.planEditorScroll = 0
	p.planEditorInputScroll = 0
	p.planEditorCancelRect = Rect{}
	p.planEditorCopyRect = Rect{}
	p.planEditorSaveRect = Rect{}
	p.planEditorRecoveryRects = [4]Rect{}
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
	p.planEditorRecoveryFocus = 0
	p.planEditorCheckpoint = 0
	p.planEditorManualReview = false
	p.planEditorRecoveryAction = 0
	p.planEditorInput = ""
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectCopy
	p.planEditorScroll = 0
	p.planEditorInputScroll = 0
	p.planEditorCancelRect = Rect{}
	p.planEditorCopyRect = Rect{}
	p.planEditorSaveRect = Rect{}
	p.planEditorRecoveryRects = [4]Rect{}
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
		default:
			for i, rect := range p.planEditorRecoveryRects {
				if rect.Contains(x, y) {
					p.planEditorRecoveryFocus = i + 1
					if i == 2 {
						p.planEditorManualReview = !p.planEditorManualReview
					}
					return true
				}
			}
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
		if p.planEditorRecoveryFocus > 0 {
			p.planEditorRecoveryFocus++
			if p.planEditorRecoveryFocus > 4 {
				p.planEditorRecoveryFocus = 1
			}
			return true
		}
		p.planEditorRevisionFocus = false
		if p.planEditorSelection == chatPlanEditorSelectCancel {
			p.planEditorSelection = chatPlanEditorSelectCopy
		} else {
			p.planEditorSelection = chatPlanEditorSelectCancel
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp), p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		if p.planEditorRecoveryFocus > 0 {
			p.shiftPlanEditorRecovery(-1)
		} else if p.planEditorRevisionFocus {
			p.selectPlanEditorPlan(-1)
		} else {
			p.shiftPlanEditorScroll(-1)
		}
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown), p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		if p.planEditorRecoveryFocus > 0 {
			p.shiftPlanEditorRecovery(1)
		} else if p.planEditorRevisionFocus {
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
		if p.planEditorRecoveryFocus > 0 {
			p.queuePlanEditorRecovery()
			return true
		}
		if p.planEditorRevisionFocus {
			p.loadSelectedPlanEditorPlan()
			p.planEditorRevisionFocus = false
			p.planEditorRecoveryFocus = 1
			p.statusLine = "recovery: choose checkpoint, snapshot, review behavior, and action"
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
			p.planEditorRecoveryFocus = 0
			p.statusLine = "recovery snapshot focused (↑/↓ preview, Enter choose recovery settings)"
			return true
		}
	}
	return true
}

func planEditorCheckpoints(plan ChatSessionPlan) []map[string]any {
	doc := structuredPlanDocumentMap(plan.Document)
	return mapAnyObjectSlice(doc, "checkpoints")
}

func planEditorInitialCheckpoint(plan ChatSessionPlan) int {
	checkpoints := planEditorCheckpoints(plan)
	doc := structuredPlanDocumentMap(plan.Document)
	activeID := firstNonEmptyToolValue(mapStringArg(doc, "active_checkpoint_id"), mapStringArg(doc, "activeCheckpointId"))
	for i, checkpoint := range checkpoints {
		if mapStringArg(checkpoint, "id") == activeID {
			return i
		}
	}
	return 0
}

func (p *ChatPage) shiftPlanEditorRecovery(delta int) {
	switch p.planEditorRecoveryFocus {
	case 1:
		checkpoints := planEditorCheckpoints(p.planEditorPlan)
		if len(checkpoints) > 0 {
			p.planEditorCheckpoint = (p.planEditorCheckpoint + delta + len(checkpoints)) % len(checkpoints)
		}
	case 2:
		p.selectPlanEditorPlan(delta)
		p.planEditorCheckpoint = planEditorInitialCheckpoint(p.planEditorPlan)
	case 3:
		p.planEditorManualReview = !p.planEditorManualReview
	case 4:
		if p.planEditorPlanSelection > 0 {
			p.planEditorRecoveryAction = (p.planEditorRecoveryAction + delta + 4) % 4
		}
	}
}

func (p *ChatPage) selectedPlanEditorCheckpointID(final bool) string {
	checkpoints := planEditorCheckpoints(p.planEditorPlan)
	if len(checkpoints) == 0 {
		return ""
	}
	idx := p.planEditorCheckpoint
	if final {
		idx = len(checkpoints) - 1
	}
	if idx < 0 || idx >= len(checkpoints) {
		idx = 0
	}
	return mapStringArg(checkpoints[idx], "id")
}

func (p *ChatPage) queuePlanEditorRecovery() {
	plan := p.selectedPlanEditorPlan()
	if strings.TrimSpace(plan.ID) == "" {
		p.statusLine = "recovery unavailable: plan id is missing"
		return
	}
	action := "start_selected"
	if p.planEditorPlanSelection > 0 {
		actions := []string{"restart_selected", "fast_forward", "final_checkpoint", "restore_only"}
		action = actions[p.planEditorRecoveryAction]
	}
	checkpointID := p.selectedPlanEditorCheckpointID(action == "final_checkpoint")
	if action != "restore_only" && checkpointID == "" {
		p.statusLine = "recovery unavailable: checkpoint id is missing"
		return
	}
	policy := "automatic"
	if p.planEditorManualReview {
		policy = "review_each_checkpoint"
	}
	p.pendingChatAction = &ChatAction{Kind: ChatActionRecoverPlan, Plan: plan, Recovery: ChatPlanRecovery{Action: action, CheckpointID: checkpointID, ExecutionGranularity: "checkpointed", ContinuationPolicy: policy, ContinueAutomatically: !p.planEditorManualReview}}
	p.closePlanEditorModal()
	p.statusLine = "applying plan recovery action..."
}

func (p *ChatPage) drawPlanEditorRecoverySummary(s tcell.Screen, rect Rect, onPanel func(tcell.Style) tcell.Style) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	checkpointID := p.selectedPlanEditorCheckpointID(false)
	snapshot := planEditorRevisionLabel(p.planEditorPlan, p.planEditorPlanSelection)
	review := "Off (automatic)"
	if p.planEditorManualReview {
		review = "On (pause after each checkpoint)"
	}
	action := "Start selected checkpoint"
	if p.planEditorPlanSelection > 0 {
		action = []string{"Restore + restart selected", "Restore + fast-forward", "Restore + final checkpoint", "Restore only"}[p.planEditorRecoveryAction]
	}
	rows := []string{"1 Checkpoint: " + firstNonEmptyToolValue(checkpointID, "none"), "2 Snapshot: " + snapshot, "3 Pause for review: " + review, "4 Action: " + action}
	p.planEditorRecoveryRects = [4]Rect{}
	for i, row := range rows {
		style := onPanel(p.theme.TextMuted)
		field := i + 1
		if p.planEditorRecoveryFocus == field {
			style = onPanel(p.theme.Warning.Bold(true))
			row = "› " + row
		}
		DrawText(s, rect.X, rect.Y+i, rect.W, style, clampEllipsis(row, rect.W))
		p.planEditorRecoveryRects[i] = Rect{X: rect.X, Y: rect.Y + i, W: rect.W, H: 1}
	}
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
	subtitle := "Structured plan • display only in TUI • press R for recovery controls"
	if len(p.planEditorPlans) > 1 {
		subtitle = "Structured plan • R recovery • select current or saved snapshot"
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyTop := modal.Y + 4
	if p.planEditorRecoveryFocus > 0 {
		p.drawPlanEditorRecoverySummary(s, Rect{X: modal.X + 2, Y: bodyTop, W: modal.W - 4, H: 4}, onPanel)
		bodyTop += 5
	}
	bodyRect := Rect{X: modal.X + 2, Y: bodyTop, W: modal.W - 4, H: modal.Y + modal.H - 3 - bodyTop}
	maxScroll := p.drawPlanEditorDocument(s, bodyRect, onPanel)

	helpY := modal.Y + modal.H - 3
	helpText := "↑/↓ scroll • R recovery • A activate only • C copy • Esc close"
	if p.planEditorRevisionFocus {
		helpText = "Snapshots focused • ↑/↓ preview • Enter recovery • A activate only"
	} else if p.planEditorRecoveryFocus > 0 {
		helpText = "Recovery • Tab field • ↑/↓ change • Enter confirm • Esc close"
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
	p.planEditorRecoveryRects = [4]Rect{}
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
	lines := p.planEditorFallbackLines(rect.W)
	if len(doc) > 0 {
		// TUI plan documents are intentionally rendered as one vertical stack.
		// Two-column layouts are hard to scan in terminals and make wrapping brittle;
		// keep information at the top and checkpoints below it at every width.
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
	appendPlanEditorField(&lines, width, "Scope", mapStringArg(info, "scope"), p.theme.Text)
	appendPlanEditorField(&lines, width, "Context", mapStringArg(info, "context"), p.theme.Text)
	appendPlanEditorList(&lines, width, "Decisions", mapAnyStringSlice(info, "decisions"), p.theme.Text)
	appendPlanEditorList(&lines, width, "Success criteria", firstNonEmptyStringSlice(mapAnyStringSlice(info, "success_criteria"), mapAnyStringSlice(info, "successCriteria")), p.theme.Text)
	appendPlanEditorList(&lines, width, "Constraints", mapAnyStringSlice(info, "constraints"), p.theme.Text)
	appendPlanEditorList(&lines, width, "Assumptions", mapAnyStringSlice(info, "assumptions"), p.theme.Text)
	appendPlanEditorList(&lines, width, "Open questions", firstNonEmptyStringSlice(mapAnyStringSlice(info, "open_questions"), mapAnyStringSlice(info, "openQuestions")), p.theme.Text)
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
