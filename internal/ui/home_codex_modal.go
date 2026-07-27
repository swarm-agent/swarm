package ui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

type codexUsageModalState struct {
	Visible         bool
	Loading         bool
	SelectedCredit  int
	ConfirmCreditID string
	BusyCreditID    string
	Status          string
	Error           string
	RedeemKeys      map[string]string
}

func (p *HomePage) ShowCodexUsageModal() {
	if p == nil {
		return
	}
	p.codexModal.Visible = true
	p.codexModal.Loading = true
	p.codexModal.Error = ""
	p.codexModal.Status = "Loading Codex account usage..."
	p.codexModal.ConfirmCreditID = ""
	if p.codexModal.RedeemKeys == nil {
		p.codexModal.RedeemKeys = make(map[string]string)
	}
}

func (p *HomePage) HideCodexUsageModal() {
	if p == nil {
		return
	}
	p.codexModal.Visible = false
	p.codexModal.ConfirmCreditID = ""
	p.codexModalTargets = p.codexModalTargets[:0]
}

func (p *HomePage) CodexUsageModalVisible() bool {
	return p != nil && p.codexModal.Visible
}

func (p *HomePage) SetCodexUsageModalResult(usage client.CodexAccountUsage, usageErr string, credits client.CodexResetCredits, creditsErr string) {
	if p == nil {
		return
	}
	if strings.TrimSpace(usageErr) == "" {
		p.codexUsage = usage
	}
	if strings.TrimSpace(creditsErr) == "" {
		p.codexResetCredits = credits
	}
	p.codexModal.Loading = false
	p.codexModal.Error = strings.TrimSpace(strings.Join(nonEmptyStrings(usageErr, creditsErr), "; "))
	if p.codexModal.Error == "" {
		p.codexModal.Status = fmt.Sprintf("Updated • %d reset credits available", credits.AvailableCount)
	} else {
		p.codexModal.Status = "Refresh failed; press r to retry or open /auth"
	}
	p.reconcileCodexCreditSelection()
}

func (p *HomePage) SetCodexResetResult(creditID, message string, retryable bool) {
	if p == nil {
		return
	}
	p.codexModal.BusyCreditID = ""
	p.codexModal.ConfirmCreditID = ""
	p.codexModal.Status = strings.TrimSpace(message)
	if !retryable {
		delete(p.codexModal.RedeemKeys, strings.TrimSpace(creditID))
	}
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (p *HomePage) sortedCodexCredits() []client.CodexResetCredit {
	credits := append([]client.CodexResetCredit(nil), p.codexResetCredits.Credits...)
	sort.SliceStable(credits, func(i, j int) bool {
		left, right := codexCreditExpiry(credits[i]), codexCreditExpiry(credits[j])
		if left.IsZero() != right.IsZero() {
			return !left.IsZero()
		}
		return left.Before(right)
	})
	return credits
}

func codexCreditExpiry(credit client.CodexResetCredit) time.Time {
	if credit.ExpiresAt == nil {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(*credit.ExpiresAt))
	return parsed
}

func codexCreditRedeemable(credit client.CodexResetCredit) bool {
	if !strings.EqualFold(strings.TrimSpace(credit.Status), "available") {
		return false
	}
	expires := codexCreditExpiry(credit)
	return expires.IsZero() || expires.After(time.Now())
}

func (p *HomePage) reconcileCodexCreditSelection() {
	credits := p.sortedCodexCredits()
	if len(credits) == 0 {
		p.codexModal.SelectedCredit = 0
		return
	}
	p.codexModal.SelectedCredit = maxInt(0, minInt(p.codexModal.SelectedCredit, len(credits)-1))
}

func (p *HomePage) moveCodexCreditSelection(delta int) {
	credits := p.sortedCodexCredits()
	if len(credits) == 0 || delta == 0 {
		return
	}
	p.codexModal.SelectedCredit = (p.codexModal.SelectedCredit + delta + len(credits)) % len(credits)
	p.codexModal.ConfirmCreditID = ""
}

func (p *HomePage) requestSelectedCodexReset() {
	credits := p.sortedCodexCredits()
	if p == nil || p.codexModal.BusyCreditID != "" || p.codexModal.SelectedCredit < 0 || p.codexModal.SelectedCredit >= len(credits) {
		return
	}
	credit := credits[p.codexModal.SelectedCredit]
	if !codexCreditRedeemable(credit) {
		p.codexModal.Status = "Selected reset credit is not available"
		return
	}
	if p.codexModal.ConfirmCreditID != credit.ID {
		p.codexModal.ConfirmCreditID = credit.ID
		p.codexModal.Status = "Press Enter again to consume this reset credit"
		return
	}
	key := p.codexModal.RedeemKeys[credit.ID]
	if key == "" {
		key = newCodexRedeemKey()
		p.codexModal.RedeemKeys[credit.ID] = key
	}
	p.codexModal.BusyCreditID = credit.ID
	p.codexModal.Status = "Using reset credit..."
	p.QueueCodexResetCredit(credit.ID, key)
}

func newCodexRedeemKey() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("tui-%d", time.Now().UnixNano())
}

func (p *HomePage) handleCodexUsageModalKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.codexModal.Visible {
		return
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideCodexUsageModal()
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveCodexCreditSelection(-1)
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveCodexCreditSelection(1)
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.requestSelectedCodexReset()
	case ev.Key() == tcell.KeyRune && (ev.Rune() == 'r' || ev.Rune() == 'R'):
		if !p.codexModal.Loading && p.codexModal.BusyCreditID == "" {
			p.codexModal.Loading = true
			p.codexModal.Status = "Refreshing Codex account usage..."
			p.QueueCodexUsageRefresh()
		}
	}
}

func (p *HomePage) handleCodexUsageModalMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil || !p.codexModal.Visible {
		return false
	}
	if ev.Buttons()&tcell.WheelUp != 0 {
		p.moveCodexCreditSelection(-1)
		return true
	}
	if ev.Buttons()&tcell.WheelDown != 0 {
		p.moveCodexCreditSelection(1)
		return true
	}
	if ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	x, y := ev.Position()
	for _, target := range p.codexModalTargets {
		if !target.Rect.Contains(x, y) {
			continue
		}
		switch target.Action {
		case "codex-refresh":
			if !p.codexModal.Loading && p.codexModal.BusyCreditID == "" {
				p.codexModal.Loading = true
				p.QueueCodexUsageRefresh()
			}
		case "codex-credit":
			p.codexModal.SelectedCredit = target.Index
			p.requestSelectedCodexReset()
		}
		return true
	}
	return true
}

func (p *HomePage) drawCodexUsageModal(s tcell.Screen) {
	p.codexModalTargets = p.codexModalTargets[:0]
	if p == nil || !p.codexModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := minInt(84, w-2)
	modalH := minInt(24, h-2)
	if modalW < 36 || modalH < 14 {
		return
	}
	rect := Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Codex usage")
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, "ChatGPT plan: "+emptyValue(strings.TrimSpace(p.codexUsage.PlanType), "Unavailable"))
	refresh := "[ r: refresh ]"
	DrawText(s, rect.X+rect.W-len([]rune(refresh))-2, rect.Y+1, len([]rune(refresh)), p.theme.Secondary, refresh)
	p.codexModalTargets = append(p.codexModalTargets, clickTarget{Rect: Rect{X: rect.X + rect.W - len([]rune(refresh)) - 2, Y: rect.Y + 1, W: len([]rune(refresh)), H: 1}, Action: "codex-refresh"})

	contentW := rect.W - 4
	creditsY := rect.Y + 8
	if modalW < 64 {
		p.drawCodexUsageWindow(s, Rect{X: rect.X + 2, Y: rect.Y + 3, W: contentW, H: 3}, "Primary", codexPrimaryWindow(p.codexUsage))
		p.drawCodexUsageWindow(s, Rect{X: rect.X + 2, Y: rect.Y + 6, W: contentW, H: 3}, "Secondary", codexSecondaryWindow(p.codexUsage))
		creditsY = rect.Y + 9
	} else {
		halfW := (contentW - 2) / 2
		p.drawCodexUsageWindow(s, Rect{X: rect.X + 2, Y: rect.Y + 3, W: halfW, H: 4}, "Primary", codexPrimaryWindow(p.codexUsage))
		p.drawCodexUsageWindow(s, Rect{X: rect.X + 4 + halfW, Y: rect.Y + 3, W: contentW - halfW - 2, H: 4}, "Secondary", codexSecondaryWindow(p.codexUsage))
	}

	credits := p.sortedCodexCredits()
	available := p.codexResetCredits.AvailableCount
	if available == 0 && p.codexUsage.ResetCredits != nil {
		available = p.codexUsage.ResetCredits.AvailableCount
	}
	DrawText(s, rect.X+2, creditsY, rect.W-4, p.theme.Text, fmt.Sprintf("Usage-limit resets • %d available", available))
	rowY := creditsY + 2
	maxRows := rect.Y + rect.H - 3 - rowY
	if len(credits) == 0 {
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, "No reset credits are available for this account.")
	} else {
		start := 0
		if p.codexModal.SelectedCredit >= maxRows {
			start = p.codexModal.SelectedCredit - maxRows + 1
		}
		for i := start; i < len(credits) && rowY < rect.Y+rect.H-3; i++ {
			credit := credits[i]
			style, prefix := p.theme.Text, "  "
			if i == p.codexModal.SelectedCredit {
				style, prefix = p.theme.Primary.Bold(true), "> "
			}
			title := "Usage-limit reset"
			if credit.Title != nil && strings.TrimSpace(*credit.Title) != "" {
				title = strings.TrimSpace(*credit.Title)
			}
			expiry := "no expiry"
			if expires := codexCreditExpiry(credit); !expires.IsZero() {
				expiry = "expires " + expires.Local().Format("2006-01-02 15:04")
			}
			label := fmt.Sprintf("%s%s • %s • %s", prefix, title, emptyValue(strings.TrimSpace(credit.Status), "unknown"), expiry)
			DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(label, rect.W-4))
			p.codexModalTargets = append(p.codexModalTargets, clickTarget{Rect: Rect{X: rect.X + 2, Y: rowY, W: rect.W - 4, H: 1}, Action: "codex-credit", Index: i})
			rowY++
		}
	}
	statusStyle := p.theme.TextMuted
	if p.codexModal.Error != "" {
		statusStyle = p.theme.Error
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, statusStyle, clampEllipsis(p.codexModal.Status+" • Enter: use selected (confirmation required) • Esc: close", rect.W-4))
}

func codexPrimaryWindow(usage client.CodexAccountUsage) *client.CodexUsageWindow {
	if usage.RateLimit == nil {
		return nil
	}
	return usage.RateLimit.PrimaryWindow
}

func codexSecondaryWindow(usage client.CodexAccountUsage) *client.CodexUsageWindow {
	if usage.RateLimit == nil {
		return nil
	}
	return usage.RateLimit.SecondaryWindow
}

func (p *HomePage) drawCodexUsageWindow(s tcell.Screen, rect Rect, fallback string, window *client.CodexUsageWindow) {
	if window == nil {
		DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, fallback+": unavailable")
		return
	}
	remaining := 100 - window.UsedPercent
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	label := fallback
	if window.LimitWindowSeconds > 0 {
		if window.LimitWindowSeconds%86400 == 0 {
			label = fmt.Sprintf("%d day", window.LimitWindowSeconds/86400)
		} else if window.LimitWindowSeconds%3600 == 0 {
			label = fmt.Sprintf("%d hour", window.LimitWindowSeconds/3600)
		}
	}
	DrawText(s, rect.X, rect.Y, rect.W, p.theme.Text, fmt.Sprintf("%s: %.0f%% remaining", label, remaining))
	reset := "Unavailable"
	if window.ResetAt > 0 {
		reset = time.Unix(window.ResetAt, 0).Local().Format("2006-01-02 15:04 MST")
	}
	DrawText(s, rect.X, rect.Y+1, rect.W, p.theme.TextMuted, "Exact reset: "+reset)
}
