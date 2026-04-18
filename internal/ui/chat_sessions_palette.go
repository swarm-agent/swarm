package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

const chatSessionsPaletteVisibleRows = 10

func (p *ChatPage) OpenSessionsPalette(items []ChatSessionPaletteItem, query string) bool {
	if p == nil {
		return false
	}
	if p.planEditorModalActive() || p.planUpdateModalActive() || p.planExitModalActive() || p.askUserModalActive() || p.workspaceScopeModalActive() || p.permissionModalActive() || p.skillChangeModalActive() {
		return false
	}
	p.sessionsPaletteItems = append([]ChatSessionPaletteItem(nil), items...)
	p.sessionsPaletteVisible = true
	p.sessionsPaletteQuery = strings.TrimSpace(query)
	p.sessionsPaletteSelection = 0
	p.sessionsPaletteScroll = 0
	p.syncSessionsPaletteSelection()
	if len(p.sessionsPaletteMatches()) == 0 {
		p.statusLine = "no sessions match search"
	} else {
		p.statusLine = "sessions palette"
	}
	return true
}

func (p *ChatPage) sessionsPaletteActive() bool {
	return p != nil && p.sessionsPaletteVisible
}

func (p *ChatPage) closeSessionsPalette() {
	if p == nil {
		return
	}
	p.sessionsPaletteVisible = false
	p.sessionsPaletteScroll = 0
	p.sessionsPaletteSelection = 0
	p.sessionsPaletteQuery = ""
}

func (p *ChatPage) sessionsPaletteMatches() []ChatSessionPaletteItem {
	if p == nil {
		return nil
	}
	if len(p.sessionsPaletteItems) == 0 {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(p.sessionsPaletteQuery))
	if query == "" {
		return append([]ChatSessionPaletteItem(nil), p.sessionsPaletteItems...)
	}
	matches := make([]ChatSessionPaletteItem, 0, len(p.sessionsPaletteItems))
	for _, item := range p.sessionsPaletteItems {
		if strings.Contains(strings.ToLower(item.Title), query) ||
			strings.Contains(strings.ToLower(item.ID), query) ||
			strings.Contains(strings.ToLower(item.WorkspaceName), query) ||
			strings.Contains(strings.ToLower(item.WorkspacePath), query) {
			matches = append(matches, item)
		}
	}
	return matches
}

func (p *ChatPage) syncSessionsPaletteSelection() []ChatSessionPaletteItem {
	matches := p.sessionsPaletteMatches()
	if len(matches) == 0 {
		p.sessionsPaletteSelection = 0
		p.sessionsPaletteScroll = 0
		return matches
	}
	if p.sessionsPaletteSelection < 0 {
		p.sessionsPaletteSelection = 0
	}
	if p.sessionsPaletteSelection >= len(matches) {
		p.sessionsPaletteSelection = len(matches) - 1
	}
	rows := chatSessionsPaletteVisibleRows
	maxScroll := maxInt(0, len(matches)-rows)
	if p.sessionsPaletteScroll < 0 {
		p.sessionsPaletteScroll = 0
	}
	if p.sessionsPaletteScroll > maxScroll {
		p.sessionsPaletteScroll = maxScroll
	}
	if p.sessionsPaletteSelection < p.sessionsPaletteScroll {
		p.sessionsPaletteScroll = p.sessionsPaletteSelection
	}
	if p.sessionsPaletteSelection >= p.sessionsPaletteScroll+rows {
		p.sessionsPaletteScroll = p.sessionsPaletteSelection - rows + 1
	}
	if p.sessionsPaletteScroll < 0 {
		p.sessionsPaletteScroll = 0
	}
	if p.sessionsPaletteScroll > maxScroll {
		p.sessionsPaletteScroll = maxScroll
	}
	return matches
}

func (p *ChatPage) moveSessionsPaletteSelection(delta int) {
	matches := p.syncSessionsPaletteSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	next := p.sessionsPaletteSelection + delta
	if next < 0 {
		next = 0
	}
	if next >= len(matches) {
		next = len(matches) - 1
	}
	p.sessionsPaletteSelection = next
	p.syncSessionsPaletteSelection()
}

func (p *ChatPage) pageSessionsPalette(delta int) {
	if delta == 0 {
		return
	}
	p.moveSessionsPaletteSelection(delta * chatSessionsPaletteVisibleRows)
}

func (p *ChatPage) sessionsPaletteBackspace() {
	if len(p.sessionsPaletteQuery) == 0 {
		return
	}
	_, sz := utf8.DecodeLastRuneInString(p.sessionsPaletteQuery)
	if sz <= 0 {
		return
	}
	p.sessionsPaletteQuery = p.sessionsPaletteQuery[:len(p.sessionsPaletteQuery)-sz]
	p.syncSessionsPaletteSelection()
}

func (p *ChatPage) sessionsPaletteClearQuery() {
	p.sessionsPaletteQuery = ""
	p.syncSessionsPaletteSelection()
}

func (p *ChatPage) selectedSessionsPaletteItem() (ChatSessionPaletteItem, bool) {
	matches := p.syncSessionsPaletteSelection()
	if len(matches) == 0 {
		return ChatSessionPaletteItem{}, false
	}
	return matches[p.sessionsPaletteSelection], true
}

func (p *ChatPage) confirmSessionsPaletteSelection() bool {
	selected, ok := p.selectedSessionsPaletteItem()
	if !ok {
		return false
	}
	if strings.TrimSpace(selected.ID) == "" {
		return false
	}
	p.pendingChatAction = &ChatAction{
		Kind:    ChatActionOpenSession,
		Session: selected,
	}
	p.closeSessionsPalette()
	p.statusLine = "opening session..."
	return true
}

func (p *ChatPage) handleSessionsPaletteMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.sessionsPaletteActive() {
		return false
	}
	buttons := ev.Buttons()
	switch {
	case buttons&tcell.WheelUp != 0:
		p.moveSessionsPaletteSelection(-1)
	case buttons&tcell.WheelDown != 0:
		p.moveSessionsPaletteSelection(1)
	}
	return true
}

func (p *ChatPage) handleSessionsPaletteKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.sessionsPaletteActive() {
		return false
	}
	switch {
	case p.keybinds.Match(ev, KeybindChatEscape):
		p.closeSessionsPalette()
		p.statusLine = "sessions palette closed"
		return true
	case p.keybinds.Match(ev, KeybindChatMoveUp), p.keybinds.Match(ev, KeybindChatMoveUpAlt):
		p.moveSessionsPaletteSelection(-1)
		return true
	case p.keybinds.Match(ev, KeybindChatMoveDown), p.keybinds.Match(ev, KeybindChatMoveDownAlt):
		p.moveSessionsPaletteSelection(1)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.pageSessionsPalette(-1)
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.pageSessionsPalette(1)
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.sessionsPaletteSelection = 0
		p.syncSessionsPaletteSelection()
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		matches := p.syncSessionsPaletteSelection()
		if len(matches) > 0 {
			p.sessionsPaletteSelection = len(matches) - 1
			p.syncSessionsPaletteSelection()
		}
		return true
	case p.keybinds.Match(ev, KeybindChatSubmit):
		p.confirmSessionsPaletteSelection()
		return true
	case p.keybinds.Match(ev, KeybindChatBackspace):
		p.sessionsPaletteBackspace()
		return true
	case p.keybinds.Match(ev, KeybindChatClear):
		p.sessionsPaletteClearQuery()
		return true
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if unicode.IsPrint(r) {
			p.sessionsPaletteQuery += string(r)
			p.syncSessionsPaletteSelection()
		}
		return true
	}

	return true
}

func (p *ChatPage) drawSessionsPalette(s tcell.Screen, screen Rect) {
	if !p.sessionsPaletteActive() || screen.W < 40 || screen.H < 12 {
		return
	}
	modalW := minInt(132, screen.W-6)
	if modalW < 56 {
		modalW = screen.W - 2
	}
	if modalW < 40 {
		return
	}
	rows := chatSessionsPaletteVisibleRows
	modalH := rows + 9
	if modalH > screen.H-4 {
		modalH = screen.H - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{
		X: maxInt(1, (screen.W-modalW)/2),
		Y: maxInt(1, (screen.H-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	matches := p.syncSessionsPaletteSelection()
	header := fmt.Sprintf("Sessions (%d)", len(p.sessionsPaletteItems))
	if strings.TrimSpace(p.sessionsPaletteQuery) != "" {
		header = fmt.Sprintf("Sessions (%d/%d)", len(matches), len(p.sessionsPaletteItems))
	}
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, modal.W-4))
	searchLine := "search: " + p.sessionsPaletteQuery
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(searchLine, modal.W-4))

	listTop := modal.Y + 4
	listH := modal.H - 7
	if listH > chatSessionsPaletteVisibleRows {
		listH = chatSessionsPaletteVisibleRows
	}
	if listH < 1 {
		listH = 1
	}
	compact := modal.W < 72

	if len(matches) == 0 {
		DrawText(s, modal.X+2, listTop, modal.W-4, onPanel(p.theme.Warning), "no matching sessions")
	} else {
		start := p.sessionsPaletteScroll
		for row := 0; row < listH && start+row < len(matches); row++ {
			idx := start + row
			item := matches[idx]
			style := onPanel(p.theme.Text)
			prefix := "  "
			if idx == p.sessionsPaletteSelection {
				style = onPanel(p.theme.Primary.Bold(true))
				prefix = "› "
			}
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = strings.TrimSpace(item.ID)
			}
			meta := strings.TrimSpace(item.UpdatedAgo)
			if meta == "" {
				meta = strings.TrimSpace(item.Mode)
			}
			ws := strings.TrimSpace(item.WorkspaceName)
			if ws == "" {
				ws = strings.TrimSpace(item.WorkspacePath)
			}
			modelLabel := model.DisplayModelLabel(item.Provider, item.ModelName, item.ServiceTier, item.ContextMode)
			line := sessionListPrimaryLine(prefix+SessionIndentedPrefix(item.Depth), sessionDisplayTitle(item.Title, item.ID), SessionLineageDisplay(SessionLineageFromPaletteItem(item)), ws, modelLabel, compact)
			if compact {
				DrawText(s, modal.X+2, listTop+row, modal.W-4, style, clampEllipsis(line, modal.W-4))
				continue
			}
			DrawText(s, modal.X+2, listTop+row, modal.W-12, style, clampEllipsis(line, modal.W-12))
			DrawTextRight(s, modal.X+modal.W-3, listTop+row, 8, onPanel(p.theme.TextMuted), clampEllipsis(meta, 8))
		}
	}

	hint := "Enter open • Esc close • type to search • ↑/↓ scroll"
	DrawText(s, modal.X+2, modal.Y+modal.H-2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
}
