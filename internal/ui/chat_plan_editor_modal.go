package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) openPlanEditorModal(plan ChatSessionPlan) {
	planID := strings.TrimSpace(plan.ID)
	title := strings.TrimSpace(plan.Title)
	if title == "" {
		title = "Plan"
	}
	body := strings.ReplaceAll(plan.Plan, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	p.planEditorVisible = true
	p.planEditorPlan = ChatSessionPlan{
		ID:            planID,
		Title:         title,
		Plan:          body,
		Status:        strings.TrimSpace(plan.Status),
		ApprovalState: strings.TrimSpace(plan.ApprovalState),
	}
	p.planEditorInput = body
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectSave
	p.planEditorScroll = 0
	p.planEditorInputScroll = 0
	p.planEditorCancelRect = Rect{}
	p.planEditorCopyRect = Rect{}
	p.planEditorSaveRect = Rect{}
	if planID != "" {
		p.statusLine = fmt.Sprintf("current plan: %s", title)
	} else {
		p.statusLine = "current plan: no active plan"
	}
}

func (p *ChatPage) planEditorModalActive() bool {
	return p.planEditorVisible
}

func (p *ChatPage) closePlanEditorModal() {
	p.planEditorVisible = false
	p.planEditorPlan = ChatSessionPlan{}
	p.planEditorInput = ""
	p.planEditorEditing = false
	p.planEditorConfirmSave = false
	p.planEditorSelection = chatPlanEditorSelectSave
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
		case p.planEditorSaveRect.Contains(x, y):
			p.resolvePlanEditorModal(chatPlanEditorActionSave)
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
	if p.planEditorConfirmSave {
		switch {
		case p.keybinds.Match(ev, KeybindPlanExitCancel):
			p.planEditorConfirmSave = false
			p.statusLine = "save canceled"
			return true
		case p.keybinds.Match(ev, KeybindPlanExitConfirm):
			p.planEditorEditing = false
			p.planEditorConfirmSave = false
			p.planEditorSelection = chatPlanEditorSelectSave
			p.resolvePlanEditorModal(chatPlanEditorActionSave)
			return true
		case ev.Key() == tcell.KeyRune:
			switch unicode.ToLower(ev.Rune()) {
			case 'y':
				p.planEditorEditing = false
				p.planEditorConfirmSave = false
				p.planEditorSelection = chatPlanEditorSelectSave
				p.resolvePlanEditorModal(chatPlanEditorActionSave)
				return true
			case 'n':
				p.planEditorConfirmSave = false
				p.statusLine = "save canceled"
				return true
			}
		}
		return true
	}
	if p.planEditorEditing {
		switch {
		case p.keybinds.Match(ev, KeybindPlanExitCancel):
			p.resolvePlanEditorModal(chatPlanEditorActionCancel)
			return true
		case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight), p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
			if p.planEditorSelection == chatPlanEditorSelectCancel {
				p.planEditorSelection = chatPlanEditorSelectSave
			} else {
				p.planEditorSelection = chatPlanEditorSelectCancel
			}
			return true
		case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
			if len(p.planEditorInput) > 0 {
				_, sz := utf8.DecodeLastRuneInString(p.planEditorInput)
				if sz > 0 {
					p.planEditorInput = p.planEditorInput[:len(p.planEditorInput)-sz]
				}
			}
			p.planEditorSelection = chatPlanEditorSelectSave
			return true
		case ev.Key() == tcell.KeyCtrlU:
			p.planEditorInput = ""
			p.planEditorInputScroll = 0
			p.planEditorSelection = chatPlanEditorSelectSave
			return true
		case ev.Key() == tcell.KeyCtrlJ:
			p.planEditorInput += "\n"
			p.planEditorSelection = chatPlanEditorSelectSave
			return true
		case ev.Key() == tcell.KeyEnter:
			if p.planEditorSelection == chatPlanEditorSelectCancel {
				p.resolvePlanEditorModal(chatPlanEditorActionCancel)
				return true
			}
			p.planEditorEditing = false
			p.planEditorConfirmSave = false
			p.planEditorSelection = chatPlanEditorSelectSave
			p.resolvePlanEditorModal(chatPlanEditorActionSave)
			return true
		case ev.Key() == tcell.KeyRune:
			r := ev.Rune()
			if unicode.IsPrint(r) && utf8.RuneCountInString(p.planEditorInput) < chatMaxInputRunes {
				p.planEditorInput += string(r)
				p.planEditorSelection = chatPlanEditorSelectSave
			}
			return true
		}
		return true
	}

	switch {
	case p.keybinds.Match(ev, KeybindPlanExitCancel):
		p.resolvePlanEditorModal(chatPlanEditorActionCancel)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggle), p.keybinds.Match(ev, KeybindPlanExitToggleRight):
		p.planEditorSelection = (p.planEditorSelection + 1) % 3
		return true
	case p.keybinds.Match(ev, KeybindPlanExitToggleLeft):
		p.planEditorSelection = (p.planEditorSelection + 2) % 3
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUp):
		p.shiftPlanEditorScroll(-1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDown):
		p.shiftPlanEditorScroll(1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveUpAlt):
		p.shiftPlanEditorScroll(-1)
		return true
	case p.keybinds.Match(ev, KeybindPlanExitMoveDownAlt):
		p.shiftPlanEditorScroll(1)
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
		switch p.planEditorSelection {
		case chatPlanEditorSelectCancel:
			p.resolvePlanEditorModal(chatPlanEditorActionCancel)
		case chatPlanEditorSelectCopy:
			p.resolvePlanEditorModal(chatPlanEditorActionCopy)
		default:
			p.resolvePlanEditorModal(chatPlanEditorActionSave)
		}
		return true
	case ev.Key() == tcell.KeyRune:
		r := unicode.ToLower(ev.Rune())
		switch r {
		case 'c':
			p.resolvePlanEditorModal(chatPlanEditorActionCopy)
			return true
		case 'e':
			p.planEditorEditing = true
			p.planEditorConfirmSave = false
			p.planEditorSelection = chatPlanEditorSelectSave
			p.statusLine = "editing plan (Enter saves, Ctrl+J newline, Esc cancels)"
			return true
		}
	}
	return true
}

func (p *ChatPage) drawPlanEditorModal(s tcell.Screen, screen Rect) {
	if !p.planEditorModalActive() || screen.W < 40 || screen.H < 12 {
		return
	}
	modalW := minInt(120, screen.W-6)
	if modalW < 56 {
		modalW = screen.W - 2
	}
	if modalW < 40 {
		return
	}
	modalH := minInt(38, screen.H-4)
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
	header := "Current Plan"
	if title := strings.TrimSpace(p.planEditorPlan.Title); title != "" {
		header = "Current Plan: " + title
	}
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, modal.W-4))
	subtitle := "Review, copy, edit, and save the active plan for this session."
	if planID := strings.TrimSpace(p.planEditorPlan.ID); planID != "" {
		subtitle = fmt.Sprintf("Plan %s • copy/paste friendly editor", clampEllipsis(planID, 24))
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 4, W: modal.W - 4, H: modal.H - 8}
	compact := bodyRect.W < 52
	if compact {
		DrawText(s, bodyRect.X, bodyRect.Y, bodyRect.W, onPanel(p.theme.Secondary.Bold(true)), "Plan")
		editorRect := Rect{X: bodyRect.X, Y: bodyRect.Y + 1, W: bodyRect.W, H: maxInt(3, bodyRect.H-3)}
		DrawBox(s, editorRect, onPanel(p.theme.Border))
		textRect := Rect{X: editorRect.X + 1, Y: editorRect.Y + 1, W: maxInt(1, editorRect.W-2), H: maxInt(1, editorRect.H-2)}
		visibleLines, maxScroll := p.planEditorVisibleLines(textRect.W, textRect.H)
		if maxScroll < 0 {
			maxScroll = 0
		}
		if p.planEditorInputScroll > maxScroll {
			p.planEditorInputScroll = maxScroll
		}
		for i := 0; i < len(visibleLines) && i < textRect.H; i++ {
			DrawText(s, textRect.X, textRect.Y+i, textRect.W, onPanel(p.theme.Text), visibleLines[i])
		}
		if len(visibleLines) == 0 {
			DrawText(s, textRect.X, textRect.Y, textRect.W, onPanel(p.theme.TextMuted), "No active plan yet.")
		}
		if maxScroll > 0 {
			scrollLabel := fmt.Sprintf("scroll %d/%d", p.planEditorInputScroll+1, maxScroll+1)
			DrawTextRight(s, editorRect.X+editorRect.W-1, editorRect.Y, editorRect.W/2, onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, editorRect.W/2))
		}
	} else {
		bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 4, W: modal.W - 4, H: modal.H - 8}
		leftW := maxInt(24, bodyRect.W/3)
		if leftW > 34 {
			leftW = 34
		}
		rightW := bodyRect.W - leftW - 2
		if rightW < 20 {
			rightW = 20
			leftW = bodyRect.W - rightW - 2
		}
		leftRect := Rect{X: bodyRect.X, Y: bodyRect.Y, W: leftW, H: bodyRect.H}
		rightRect := Rect{X: leftRect.X + leftRect.W + 2, Y: bodyRect.Y, W: rightW, H: bodyRect.H}

		DrawText(s, leftRect.X, leftRect.Y, leftRect.W, onPanel(p.theme.Secondary.Bold(true)), "Plan details")
		title := strings.TrimSpace(p.planEditorPlan.Title)
		if title == "" {
			title = "Plan"
		}
		detailLines := []string{
			fmt.Sprintf("Title: %s", title),
			fmt.Sprintf("Plan ID: %s", planEditorFallback(strings.TrimSpace(p.planEditorPlan.ID), "none")),
			fmt.Sprintf("Status: %s", planEditorFallback(strings.TrimSpace(p.planEditorPlan.Status), "draft")),
			fmt.Sprintf("Approval: %s", planEditorFallback(strings.TrimSpace(p.planEditorPlan.ApprovalState), "pending")),
			"",
			"Tips:",
			"- Edit directly in the right pane",
			"- Use Copy to grab the full plan",
			"- Use Save to persist it to the session",
		}
		for i, line := range detailLines {
			if leftRect.Y+2+i >= leftRect.Y+leftRect.H {
				break
			}
			DrawText(s, leftRect.X, leftRect.Y+2+i, leftRect.W, onPanel(p.theme.Text), clampEllipsis(line, leftRect.W))
		}

		DrawText(s, rightRect.X, rightRect.Y, rightRect.W, onPanel(p.theme.Secondary.Bold(true)), "Plan editor")
		editorRect := Rect{X: rightRect.X, Y: rightRect.Y + 1, W: rightRect.W, H: rightRect.H - 2}
		DrawBox(s, editorRect, onPanel(p.theme.Border))
		textRect := Rect{X: editorRect.X + 1, Y: editorRect.Y + 1, W: editorRect.W - 2, H: editorRect.H - 2}
		visibleLines, maxScroll := p.planEditorVisibleLines(maxInt(1, textRect.W), maxInt(1, textRect.H))
		if maxScroll < 0 {
			maxScroll = 0
		}
		if p.planEditorInputScroll > maxScroll {
			p.planEditorInputScroll = maxScroll
		}
		for i := 0; i < len(visibleLines) && i < textRect.H; i++ {
			DrawText(s, textRect.X, textRect.Y+i, textRect.W, onPanel(p.theme.Text), visibleLines[i])
		}
		if p.planEditorEditing {
			DrawTextRight(s, textRect.X+textRect.W-1, textRect.Y, textRect.W, onPanel(p.theme.Warning), clampEllipsis("EDITING", textRect.W))
		}
		if len(visibleLines) == 0 {
			DrawText(s, textRect.X, textRect.Y, textRect.W, onPanel(p.theme.TextMuted), "No active plan yet. Save to create one.")
		}
		if (p.frameTick/chatCursorBlinkOn)%2 == 0 {
			cursorLine, cursorCol := p.planEditorCursor(maxInt(1, textRect.W), maxInt(1, textRect.H))
			cursorX := minInt(textRect.X+cursorCol, textRect.X+textRect.W-1)
			cursorY := minInt(textRect.Y+cursorLine, textRect.Y+textRect.H-1)
			s.SetContent(cursorX, cursorY, chatCursorRune, nil, onPanel(p.theme.Primary))
		}
		if maxScroll > 0 {
			scrollLabel := fmt.Sprintf("scroll %d/%d", p.planEditorInputScroll+1, maxScroll+1)
			DrawTextRight(s, editorRect.X+editorRect.W-1, editorRect.Y, editorRect.W/2, onPanel(p.theme.TextMuted), clampEllipsis(scrollLabel, editorRect.W/2))
		}
	}

	helpY := modal.Y + modal.H - 3
	helpText := "↑/↓ scroll • Tab/←/→ buttons • C copy • E edit"
	if p.planEditorEditing {
		helpText = "Editing plan • Enter saves • Ctrl+J newline • Esc cancels"
	}
	if p.planEditorConfirmSave {
		helpText = "Confirm save • Enter saves • Esc or N cancels"
	}
	DrawText(s, modal.X+2, helpY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(helpText, modal.W-4))
	buttonY := modal.Y + modal.H - 2
	labels := []string{"Esc Cancel", "C Copy", "Enter Save"}
	styles := []tcell.Style{p.theme.TextMuted, p.theme.Accent, p.theme.Success}
	rects := []*Rect{&p.planEditorCancelRect, &p.planEditorCopyRect, &p.planEditorSaveRect}
	selection := p.planEditorSelection
	if p.planEditorEditing {
		labels = []string{"Esc Cancel", "Enter Save"}
		styles = []tcell.Style{p.theme.TextMuted, p.theme.Success}
		rects = []*Rect{&p.planEditorCancelRect, &p.planEditorSaveRect}
		p.planEditorCopyRect = Rect{}
		selection = chatPlanEditorSelectSave
	} else if p.planEditorConfirmSave {
		selection = chatPlanEditorSelectSave
	}
	if selection >= 0 && selection < len(styles) {
		for i := range styles {
			if i == selection {
				styles[i] = p.theme.Warning
			}
		}
	}
	x := modal.X + 2
	for i, label := range labels {
		var nextX int
		*rects[i], nextX = drawPermissionActionButton(s, x, buttonY, modal.X+modal.W-2, label, styles[i])
		x = nextX
	}
	if p.planEditorCancelRect.W == 0 && p.planEditorCopyRect.W == 0 && p.planEditorSaveRect.W == 0 {
		DrawText(s, modal.X+2, buttonY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Esc cancel • Enter save", modal.W-4))
	}
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
	text := strings.ReplaceAll(p.planEditorInput, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
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
	case chatPlanEditorActionSave:
		updated := ChatSessionPlan{
			ID:            strings.TrimSpace(plan.ID),
			Title:         planEditorFallback(strings.TrimSpace(plan.Title), "Plan"),
			Plan:          text,
			Status:        planEditorFallback(strings.TrimSpace(plan.Status), "draft"),
			ApprovalState: strings.TrimSpace(plan.ApprovalState),
		}
		p.pendingChatAction = &ChatAction{Kind: ChatActionSavePlan, Plan: updated}
		p.statusLine = "saving current plan..."
	default:
		p.statusLine = "current plan closed"
	}
}

func planEditorFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (p *ChatPage) planEditorDirty() bool {
	if p == nil {
		return false
	}
	current := strings.ReplaceAll(p.planEditorInput, "\r\n", "\n")
	current = strings.ReplaceAll(current, "\r", "\n")
	original := strings.ReplaceAll(p.planEditorPlan.Plan, "\r\n", "\n")
	original = strings.ReplaceAll(original, "\r", "\n")
	return current != original
}

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

func (p *ChatPage) planEditorVisibleLines(width, height int) ([]string, int) {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	text := strings.ReplaceAll(p.planEditorInput, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := wrapWithCustomPrefixes("", "", text, width)
	if len(lines) == 0 {
		lines = []string{""}
	}
	maxScroll := len(lines) - height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if p.planEditorInputScroll > maxScroll {
		p.planEditorInputScroll = maxScroll
	}
	start := p.planEditorInputScroll
	end := minInt(len(lines), start+height)
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		start = maxInt(0, len(lines)-height)
		end = len(lines)
	}
	return lines[start:end], maxScroll
}

func (p *ChatPage) planEditorCursor(width, height int) (int, int) {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	text := strings.ReplaceAll(p.planEditorInput, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	wrapped := wrapWithCustomPrefixes("", "", text, width)
	if len(wrapped) == 0 {
		return 0, 0
	}
	cursorIndex := len(wrapped) - 1 - p.planEditorInputScroll
	if cursorIndex < 0 {
		cursorIndex = 0
	}
	if cursorIndex >= height {
		cursorIndex = height - 1
	}
	cursorText := wrapped[len(wrapped)-1]
	if p.planEditorInputScroll > 0 {
		cursorText = wrapped[minInt(len(wrapped)-1, p.planEditorInputScroll+cursorIndex)]
	}
	return cursorIndex, utf8.RuneCountInString(cursorText)
}
