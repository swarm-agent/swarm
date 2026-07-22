package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

const (
	homeSessionsModalVisibleCards = 5
	homeSessionsModalCardHeight   = 3
)

type sessionsModalState struct {
	Visible   bool
	Query     string
	Selection int
	Scroll    int
	Items     []ChatSessionPaletteItem
	Expanded  map[string]bool
	Filter    int
}

func sessionManagerBadge(item ChatSessionPaletteItem) string {
	label := strings.TrimSpace(item.ActivityLabel)
	if strings.EqualFold(strings.TrimSpace(item.Group), "needs_review") && strings.EqualFold(label, "REVIEW") {
		label = ""
	}
	if item.NeedsAttention && label == "" {
		label = "NEEDS APPROVAL"
	}
	if label == "" && item.Active {
		label = "ACTIVE"
	}
	if label == "" {
		return ""
	}
	return "[" + label + "]"
}

func (p *HomePage) OpenSessionsModal(items []ChatSessionPaletteItem, query string) bool {
	if p == nil {
		return false
	}
	if p.authModal.Visible ||
		p.vaultModal.Visible ||
		p.authDefaultsInfoModal.Visible ||
		p.workspaceModal.Visible ||
		p.worktreesModal.Visible ||
		p.codexModal.Visible ||
		p.modelsModal.Visible ||
		p.agentsModal.Visible ||
		p.voiceModal.Visible ||
		p.themeModal.Visible ||
		p.keybindsModal.Visible {
		return false
	}

	p.sessionsModal.Visible = true
	p.sessionsModal.Items = prepareSessionManagerItems(items)
	p.sessionsModal.Expanded = make(map[string]bool)
	p.sessionsModal.Filter = 0
	p.sessionsModal.Query = strings.TrimSpace(query)
	p.sessionsModal.Selection = 0
	p.sessionsModal.Scroll = 0
	p.syncSessionsModalSelection()
	if len(p.sessionsModalMatches()) == 0 {
		p.statusLine = "no sessions match search"
	} else {
		p.statusLine = "session manager"
	}
	return true
}

func (p *HomePage) HideSessionsModal() {
	if p == nil {
		return
	}
	p.sessionsModal.Visible = false
	p.sessionsModal.Query = ""
	p.sessionsModal.Selection = 0
	p.sessionsModal.Scroll = 0
	p.sessionsModal.Expanded = nil
	p.sessionsModal.Filter = 0
}

func (p *HomePage) SessionsModalVisible() bool {
	return p != nil && p.sessionsModal.Visible
}

func (p *HomePage) SessionsModalItems() []ChatSessionPaletteItem {
	if p == nil || len(p.sessionsModal.Items) == 0 {
		return nil
	}
	return append([]ChatSessionPaletteItem(nil), p.sessionsModal.Items...)
}

func (p *HomePage) sessionsModalMatches() []ChatSessionPaletteItem {
	if p == nil || len(p.sessionsModal.Items) == 0 {
		return nil
	}
	items := filterSessionManagerItems(p.sessionsModal.Items, p.sessionsModal.Filter)
	query := strings.ToLower(strings.TrimSpace(p.sessionsModal.Query))
	if query == "" {
		return visibleSessionManagerItems(items, p.sessionsModal.Expanded)
	}
	matches := make([]ChatSessionPaletteItem, 0, len(items))
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Title), query) ||
			strings.Contains(strings.ToLower(item.ID), query) ||
			strings.Contains(strings.ToLower(item.WorkspaceName), query) ||
			strings.Contains(strings.ToLower(item.WorkspacePath), query) {
			matches = append(matches, item)
		}
	}
	return matches
}

func (p *HomePage) syncSessionsModalSelection() []ChatSessionPaletteItem {
	matches := p.sessionsModalMatches()
	if len(matches) == 0 {
		p.sessionsModal.Selection = 0
		p.sessionsModal.Scroll = 0
		return matches
	}
	if p.sessionsModal.Selection < 0 {
		p.sessionsModal.Selection = 0
	}
	if p.sessionsModal.Selection >= len(matches) {
		p.sessionsModal.Selection = len(matches) - 1
	}
	maxScroll := maxInt(0, len(matches)-homeSessionsModalVisibleCards)
	if p.sessionsModal.Scroll < 0 {
		p.sessionsModal.Scroll = 0
	}
	if p.sessionsModal.Scroll > maxScroll {
		p.sessionsModal.Scroll = maxScroll
	}
	if p.sessionsModal.Selection < p.sessionsModal.Scroll {
		p.sessionsModal.Scroll = p.sessionsModal.Selection
	}
	if p.sessionsModal.Selection >= p.sessionsModal.Scroll+homeSessionsModalVisibleCards {
		p.sessionsModal.Scroll = p.sessionsModal.Selection - homeSessionsModalVisibleCards + 1
	}
	if p.sessionsModal.Scroll < 0 {
		p.sessionsModal.Scroll = 0
	}
	if p.sessionsModal.Scroll > maxScroll {
		p.sessionsModal.Scroll = maxScroll
	}
	return matches
}

func (p *HomePage) moveSessionsModalSelection(delta int) {
	matches := p.syncSessionsModalSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	next := p.sessionsModal.Selection + delta
	if next < 0 {
		next = 0
	}
	if next >= len(matches) {
		next = len(matches) - 1
	}
	p.sessionsModal.Selection = next
	p.syncSessionsModalSelection()
}

func (p *HomePage) moveSessionsModalFilter(delta int) {
	count := sessionManagerFilterCount()
	if count == 0 || delta == 0 {
		return
	}
	p.sessionsModal.Filter = (p.sessionsModal.Filter + delta + count) % count
	p.sessionsModal.Selection = 0
	p.sessionsModal.Scroll = 0
	p.syncSessionsModalSelection()
	p.statusLine = strings.ToLower(sessionManagerFilterLabel(p.sessionsModal.Filter)) + " sessions"
}

func (p *HomePage) selectedSessionsModalItem() (ChatSessionPaletteItem, bool) {
	matches := p.syncSessionsModalSelection()
	if len(matches) == 0 {
		return ChatSessionPaletteItem{}, false
	}
	return matches[p.sessionsModal.Selection], true
}

func (p *HomePage) toggleSelectedSessionsModalSubagents() {
	selected, ok := p.selectedSessionsModalItem()
	if !ok {
		return
	}
	id := strings.TrimSpace(selected.ID)
	if sessionManagerChildCount(p.sessionsModal.Items, id) == 0 {
		p.statusLine = "selected session has no subagents"
		return
	}
	if p.sessionsModal.Expanded == nil {
		p.sessionsModal.Expanded = make(map[string]bool)
	}
	p.sessionsModal.Expanded[id] = !p.sessionsModal.Expanded[id]
	p.syncSessionsModalSelection()
	if p.sessionsModal.Expanded[id] {
		p.statusLine = "subagent sessions expanded"
	} else {
		p.statusLine = "subagent sessions collapsed"
	}
}

func (p *HomePage) confirmSessionsModalSelection() {
	selected, ok := p.selectedSessionsModalItem()
	if !ok {
		p.statusLine = "no sessions match search"
		return
	}
	sessionID := strings.TrimSpace(selected.ID)
	if sessionID == "" {
		p.statusLine = "cannot open session: missing id"
		return
	}
	p.HideSessionsModal()
	p.queueOpenSessionAction(model.SessionSummary{
		ID:            sessionID,
		Title:         strings.TrimSpace(selected.Title),
		Mode:          strings.TrimSpace(selected.Mode),
		WorkspacePath: strings.TrimSpace(selected.WorkspacePath),
		WorkspaceName: strings.TrimSpace(selected.WorkspaceName),
	})
}

func (p *HomePage) handleSessionsModalBackspace() {
	if len(p.sessionsModal.Query) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(p.sessionsModal.Query)
	if size <= 0 {
		return
	}
	p.sessionsModal.Query = p.sessionsModal.Query[:len(p.sessionsModal.Query)-size]
	p.syncSessionsModalSelection()
}

func (p *HomePage) handleSessionsModalKey(ev *tcell.EventKey) {
	if ev == nil {
		return
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideSessionsModal()
		p.statusLine = "session manager closed"
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveSessionsModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveSessionsModalSelection(1)
		return
	case ev.Key() == tcell.KeyLeft:
		p.moveSessionsModalFilter(-1)
		return
	case ev.Key() == tcell.KeyRight:
		p.moveSessionsModalFilter(1)
		return
	case p.keybinds.Match(ev, KeybindModalPageUp):
		p.moveSessionsModalSelection(-homeSessionsModalVisibleCards)
		return
	case p.keybinds.Match(ev, KeybindModalPageDown):
		p.moveSessionsModalSelection(homeSessionsModalVisibleCards)
		return
	case p.keybinds.Match(ev, KeybindModalJumpHome):
		p.sessionsModal.Selection = 0
		p.syncSessionsModalSelection()
		return
	case p.keybinds.Match(ev, KeybindModalJumpEnd):
		matches := p.syncSessionsModalSelection()
		if len(matches) > 0 {
			p.sessionsModal.Selection = len(matches) - 1
			p.syncSessionsModalSelection()
		}
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.handleSessionsModalBackspace()
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.sessionsModal.Query = ""
		p.syncSessionsModalSelection()
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.confirmSessionsModalSelection()
		return
	case ev.Key() == tcell.KeyRune && (ev.Rune() == 's' || ev.Rune() == 'S'):
		p.toggleSelectedSessionsModalSubagents()
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if unicode.IsPrint(r) {
			p.sessionsModal.Query += string(r)
			p.syncSessionsModalSelection()
		}
	}
}

func (p *HomePage) drawSessionsModal(s tcell.Screen) {
	if !p.SessionsModalVisible() {
		return
	}
	screenW, screenH := s.Size()
	if screenW < 40 || screenH < 12 {
		return
	}
	modalW := minInt(132, screenW-6)
	if modalW < 56 {
		modalW = screenW - 2
	}
	if modalW < 40 {
		return
	}
	modalH := homeSessionsModalVisibleCards*homeSessionsModalCardHeight + 9
	if modalH > screenH-4 {
		modalH = screenH - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{
		X: maxInt(1, (screenW-modalW)/2),
		Y: maxInt(1, (screenH-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	matches := p.syncSessionsModalSelection()
	header := fmt.Sprintf("Sessions (%d)", len(p.sessionsModal.Items))
	if strings.TrimSpace(p.sessionsModal.Query) != "" {
		header = fmt.Sprintf("Sessions (%d/%d)", len(matches), len(p.sessionsModal.Items))
	}
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, modal.W-4))
	counts := sessionManagerFilterCounts(p.sessionsModal.Items)
	filterParts := make([]string, 0, sessionManagerFilterCount())
	for index := 0; index < sessionManagerFilterCount(); index++ {
		label := fmt.Sprintf("%s %d", sessionManagerFilterLabel(index), counts[index])
		if index == p.sessionsModal.Filter {
			label = "[" + label + "]"
		}
		filterParts = append(filterParts, label)
	}
	filterLine := "‹ " + strings.Join(filterParts, "  ") + " ›"
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.Primary.Bold(true)), clampEllipsis(filterLine, modal.W-4))
	searchLine := "search: " + p.sessionsModal.Query
	DrawText(s, modal.X+2, modal.Y+3, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(searchLine, modal.W-4))

	listTop := modal.Y + 5
	listH := modal.H - 8
	visibleCards := listH / homeSessionsModalCardHeight
	if visibleCards > homeSessionsModalVisibleCards {
		visibleCards = homeSessionsModalVisibleCards
	}
	if visibleCards < 1 {
		visibleCards = 1
	}
	if listH < 1 {
		listH = 1
	}
	compact := modal.W < 72

	if len(matches) == 0 {
		DrawText(s, modal.X+2, listTop, modal.W-4, onPanel(p.theme.Warning), "no matching sessions")
	} else {
		start := p.sessionsModal.Scroll
		for row := 0; row < visibleCards && start+row < len(matches); row++ {
			idx := start + row
			item := matches[idx]
			style := onPanel(p.theme.Text)
			prefix := "  "
			if idx == p.sessionsModal.Selection {
				style = onPanel(p.theme.Primary.Bold(true))
				prefix = "> "
			}
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = strings.TrimSpace(item.ID)
			}
			meta := strings.TrimSpace(item.UpdatedAgo)
			if meta == "" {
				meta = strings.TrimSpace(item.Mode)
			}
			workspace := sessionManagerWorkspaceLabel(item)

			modelLabel := model.DisplayModelLabel(item.Provider, item.ModelName, item.ServiceTier, item.ContextMode)
			line := sessionListPrimaryLine(prefix+SessionIndentedPrefix(item.Depth), sessionDisplayTitle(item.Title, item.ID), SessionLineageDisplay(SessionLineageFromPaletteItem(item)), workspace, modelLabel, compact)
			childCount := sessionManagerChildCount(p.sessionsModal.Items, item.ID)
			if suffix := sessionManagerItemSuffix(item, childCount, p.sessionsModal.Expanded[strings.TrimSpace(item.ID)]); suffix != "" {
				line += " · " + suffix
			}
			if !compact && isBackgroundSessionPaletteItem(item) && strings.TrimSpace(item.LineageLabel) == "" {
				line += " | background"
			}
			cardY := listTop + row*homeSessionsModalCardHeight
			card := Rect{X: modal.X + 2, Y: cardY, W: modal.W - 4, H: homeSessionsModalCardHeight}
			cardBorder := onPanel(p.theme.Border)
			if idx == p.sessionsModal.Selection {
				cardBorder = onPanel(p.theme.BorderActive)
			}
			DrawBox(s, card, cardBorder)

			badge := sessionManagerBadge(item)
			badgeStyle := onPanel(p.theme.Success.Bold(true))
			if item.NeedsAttention || strings.EqualFold(strings.TrimSpace(item.Group), "needs_review") {
				badgeStyle = onPanel(p.theme.Warning.Bold(true))
			}
			if badge != "" {
				line = badge + "  " + line
			}
			textY := card.Y + 1
			if compact {
				DrawText(s, card.X+2, textY, card.W-4, style, clampEllipsis(line, card.W-4))
				continue
			}
			if badge != "" {
				DrawText(s, card.X+2, textY, len([]rune(badge)), badgeStyle, badge)
				contentW := maxInt(1, card.W-14-len([]rune(badge)))
				DrawText(s, card.X+2+len([]rune(badge))+2, textY, contentW, style, clampEllipsis(strings.TrimPrefix(line, badge+"  "), contentW))
			} else {
				DrawText(s, card.X+2, textY, card.W-14, style, clampEllipsis(line, card.W-14))
			}
			DrawTextRight(s, card.X+card.W-2, textY, 8, onPanel(p.theme.TextMuted), clampEllipsis(meta, 8))
		}
	}

	hint := "←/→ filter • Enter open • s subagents • Esc close • type to search • ↑/↓ cards"
	DrawText(s, modal.X+2, modal.Y+modal.H-2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
}

func (p *HomePage) SelectedSessionsModalItem() (ChatSessionPaletteItem, bool) {
	return p.selectedSessionsModalItem()
}

func isBackgroundSessionPaletteItem(item ChatSessionPaletteItem) bool {
	return strings.EqualFold(strings.TrimSpace(item.Mode), "background")
}
