package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	homeAlertsModalVisibleCards = 5
	homeAlertsModalCardRows     = 2
)

type AlertModalItem struct {
	ID            string
	Title         string
	Body          string
	Status        string
	Severity      string
	Category      string
	ToolName      string
	Requirement   string
	SessionID     string
	SessionTitle  string
	SessionLabel  string
	WorkspacePath string
	WorkspaceName string
	OriginLabel   string
	UpdatedAgo    string
}

type alertsModalState struct {
	Visible   bool
	Query     string
	Selection int
	Scroll    int
	Items     []AlertModalItem
}

func (p *HomePage) OpenAlertsModal(items []AlertModalItem, query string) bool {
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
		p.keybindsModal.Visible ||
		p.sessionsModal.Visible {
		return false
	}
	p.alertsModal.Visible = true
	p.alertsModal.Items = append([]AlertModalItem(nil), items...)
	p.alertsModal.Query = strings.TrimSpace(query)
	p.alertsModal.Selection = 0
	p.alertsModal.Scroll = 0
	p.syncAlertsModalSelection()
	if len(p.alertsModal.Items) == 0 {
		p.statusLine = "no alerts"
	} else {
		p.statusLine = "alerts"
	}
	return true
}

func (p *HomePage) HideAlertsModal() {
	if p == nil {
		return
	}
	p.alertsModal.Visible = false
	p.alertsModal.Query = ""
	p.alertsModal.Selection = 0
	p.alertsModal.Scroll = 0
}

func (p *HomePage) SetAlertsModalItems(items []AlertModalItem) {
	if p == nil {
		return
	}
	p.alertsModal.Items = append([]AlertModalItem(nil), items...)
	p.syncAlertsModalSelection()
}

func (p *HomePage) alertsModalMatches() []AlertModalItem {
	if p == nil || len(p.alertsModal.Items) == 0 {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(p.alertsModal.Query))
	if query == "" {
		return append([]AlertModalItem(nil), p.alertsModal.Items...)
	}
	matches := make([]AlertModalItem, 0, len(p.alertsModal.Items))
	for _, item := range p.alertsModal.Items {
		joined := strings.ToLower(strings.Join([]string{item.Title, item.Body, item.SessionLabel, item.SessionTitle, item.WorkspaceName, item.WorkspacePath, item.OriginLabel, item.ToolName, item.SessionID}, " "))
		if strings.Contains(joined, query) {
			matches = append(matches, item)
		}
	}
	return matches
}

func (p *HomePage) syncAlertsModalSelection() []AlertModalItem {
	matches := p.alertsModalMatches()
	if len(matches) == 0 {
		p.alertsModal.Selection = 0
		p.alertsModal.Scroll = 0
		return matches
	}
	if p.alertsModal.Selection < 0 {
		p.alertsModal.Selection = 0
	}
	if p.alertsModal.Selection >= len(matches) {
		p.alertsModal.Selection = len(matches) - 1
	}
	maxScroll := maxInt(0, len(matches)-homeAlertsModalVisibleCards)
	if p.alertsModal.Scroll < 0 {
		p.alertsModal.Scroll = 0
	}
	if p.alertsModal.Scroll > maxScroll {
		p.alertsModal.Scroll = maxScroll
	}
	if p.alertsModal.Selection < p.alertsModal.Scroll {
		p.alertsModal.Scroll = p.alertsModal.Selection
	}
	if p.alertsModal.Selection >= p.alertsModal.Scroll+homeAlertsModalVisibleCards {
		p.alertsModal.Scroll = p.alertsModal.Selection - homeAlertsModalVisibleCards + 1
	}
	return matches
}

func (p *HomePage) moveAlertsModalSelection(delta int) {
	matches := p.syncAlertsModalSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	next := p.alertsModal.Selection + delta
	if next < 0 {
		next = 0
	}
	if next >= len(matches) {
		next = len(matches) - 1
	}
	p.alertsModal.Selection = next
	p.syncAlertsModalSelection()
}

func (p *HomePage) selectedAlertsModalItem() (AlertModalItem, bool) {
	matches := p.syncAlertsModalSelection()
	if len(matches) == 0 {
		return AlertModalItem{}, false
	}
	return matches[p.alertsModal.Selection], true
}

func (p *HomePage) confirmAlertsModalSelection() {
	selected, ok := p.selectedAlertsModalItem()
	if !ok {
		p.statusLine = "no alerts match search"
		return
	}
	if strings.TrimSpace(selected.SessionID) == "" {
		p.statusLine = "alert has no session"
		return
	}
	p.HideAlertsModal()
	p.pendingHomeAction = &HomeAction{
		Kind:          HomeActionOpenAlertSession,
		SessionID:     strings.TrimSpace(selected.SessionID),
		SessionTitle:  strings.TrimSpace(firstNonEmptyUI(selected.SessionTitle, selected.SessionLabel, selected.Title)),
		WorkspacePath: strings.TrimSpace(selected.WorkspacePath),
		WorkspaceName: strings.TrimSpace(selected.WorkspaceName),
	}
}

func (p *HomePage) handleAlertsModalBackspace() {
	if len(p.alertsModal.Query) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(p.alertsModal.Query)
	if size <= 0 {
		return
	}
	p.alertsModal.Query = p.alertsModal.Query[:len(p.alertsModal.Query)-size]
	p.syncAlertsModalSelection()
}

func (p *HomePage) handleAlertsModalKey(ev *tcell.EventKey) {
	if ev == nil {
		return
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideAlertsModal()
		p.statusLine = "alerts closed"
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveAlertsModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveAlertsModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalPageUp):
		p.moveAlertsModalSelection(-homeAlertsModalVisibleCards)
		return
	case p.keybinds.Match(ev, KeybindModalPageDown):
		p.moveAlertsModalSelection(homeAlertsModalVisibleCards)
		return
	case p.keybinds.Match(ev, KeybindModalJumpHome):
		p.alertsModal.Selection = 0
		p.syncAlertsModalSelection()
		return
	case p.keybinds.Match(ev, KeybindModalJumpEnd):
		matches := p.syncAlertsModalSelection()
		if len(matches) > 0 {
			p.alertsModal.Selection = len(matches) - 1
			p.syncAlertsModalSelection()
		}
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.handleAlertsModalBackspace()
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.alertsModal.Query = ""
		p.syncAlertsModalSelection()
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.confirmAlertsModalSelection()
		return
	}
	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		switch unicode.ToLower(r) {
		case 'c':
			p.pendingHomeAction = &HomeAction{Kind: HomeActionClearAlerts}
			return
		}
		if unicode.IsPrint(r) {
			p.alertsModal.Query += string(r)
			p.syncAlertsModalSelection()
		}
	}
}

func (p *HomePage) drawAlertsModal(s tcell.Screen) {
	if !p.AlertsModalVisible() {
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
	modalH := homeAlertsModalVisibleCards*homeAlertsModalCardRows + 9
	if modalH > screenH-4 {
		modalH = screenH - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{X: maxInt(1, (screenW-modalW)/2), Y: maxInt(1, (screenH-modalH)/2), W: modalW, H: modalH}
	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	matches := p.syncAlertsModalSelection()
	header := fmt.Sprintf("Alerts (%d)", len(p.alertsModal.Items))
	if strings.TrimSpace(p.alertsModal.Query) != "" {
		header = fmt.Sprintf("Alerts (%d/%d)", len(matches), len(p.alertsModal.Items))
	}
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, modal.W-4))
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("search: "+p.alertsModal.Query, modal.W-4))
	listTop := modal.Y + 4
	listH := minInt(homeAlertsModalVisibleCards*homeAlertsModalCardRows, maxInt(homeAlertsModalCardRows, modal.H-7))
	visibleCards := maxInt(1, listH/homeAlertsModalCardRows)
	compact := modal.W < 72
	if len(matches) == 0 {
		DrawText(s, modal.X+2, listTop, modal.W-4, onPanel(p.theme.Warning), "no matching alerts")
	} else {
		start := p.alertsModal.Scroll
		for row := 0; row < visibleCards && start+row < len(matches); row++ {
			idx := start + row
			item := matches[idx]
			rowY := listTop + row*homeAlertsModalCardRows
			style := onPanel(p.theme.Text)
			metaStyle := onPanel(p.theme.TextMuted)
			prefix := "  "
			if idx == p.alertsModal.Selection {
				style = onPanel(p.theme.Primary.Bold(true))
				metaStyle = onPanel(p.theme.Primary)
				prefix = "> "
			}
			session := firstNonEmptyUI(item.SessionLabel, item.SessionTitle, shortUIID(item.SessionID))
			workspace := firstNonEmptyUI(item.WorkspaceName, item.WorkspacePath)
			origin := strings.TrimSpace(item.OriginLabel)
			status := strings.TrimSpace(item.Status)
			if item.Severity != "" && item.Severity != item.Status {
				status = strings.TrimSpace(strings.Join(nonEmptyUI([]string{item.Severity, status}), "/"))
			}
			line1 := prefix + firstNonEmptyUI(item.Title, item.Body, "alert")
			line2Parts := []string{session}
			if !compact {
				line2Parts = append(line2Parts, workspace, origin)
			}
			line2Parts = append(line2Parts, item.ToolName, item.Requirement, status)
			line2 := "  " + strings.Join(nonEmptyUI(line2Parts), " · ")
			if strings.TrimSpace(line2) == "" {
				line2 = "  no session metadata"
			}
			DrawText(s, modal.X+2, rowY, modal.W-12, style, clampEllipsis(line1, modal.W-12))
			DrawTextRight(s, modal.X+modal.W-3, rowY, 8, onPanel(p.theme.TextMuted), clampEllipsis(item.UpdatedAgo, 8))
			DrawText(s, modal.X+2, rowY+1, modal.W-4, metaStyle, clampEllipsis(line2, modal.W-4))
		}
	}
	hint := "Enter open session | c clear | Esc close | type to search | Up/Down scroll"
	DrawText(s, modal.X+2, modal.Y+modal.H-2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
}

func firstNonEmptyUI(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func nonEmptyUI(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func shortUIID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
