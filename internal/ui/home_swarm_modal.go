package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

type SwarmModalActionKind string

const (
	SwarmModalActionCopyDashboardLink SwarmModalActionKind = "copy-dashboard-link"
)

type SwarmModalAction struct {
	Kind SwarmModalActionKind
	Text string
}

type swarmModalState struct {
	Visible       bool
	Title         string
	Lines         []string
	DashboardLink string
	Status        string
}

func (p *HomePage) ShowSwarmModal(title string, lines []string, dashboardLink string) {
	p.swarmModal = swarmModalState{
		Visible:       true,
		Title:         emptyValue(strings.TrimSpace(title), "Swarm"),
		Lines:         append([]string(nil), lines...),
		DashboardLink: strings.TrimSpace(dashboardLink),
		Status:        "Esc close • c copy dashboard link",
	}
}

func (p *HomePage) SwarmModalVisible() bool {
	return p != nil && p.swarmModal.Visible
}

func (p *HomePage) HideSwarmModal() {
	p.swarmModal = swarmModalState{}
	p.pendingSwarmAction = nil
}

func (p *HomePage) PopSwarmModalAction() (SwarmModalAction, bool) {
	if p == nil || p.pendingSwarmAction == nil {
		return SwarmModalAction{}, false
	}
	action := *p.pendingSwarmAction
	p.pendingSwarmAction = nil
	return action, true
}

func (p *HomePage) SetSwarmModalStatus(status string) {
	if p == nil || !p.swarmModal.Visible {
		return
	}
	p.swarmModal.Status = strings.TrimSpace(status)
}

func (p *HomePage) handleSwarmModalKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.swarmModal.Visible {
		return
	}
	if p.keybinds.Match(ev, KeybindModalClose) {
		p.HideSwarmModal()
		return
	}
	if ev.Key() == tcell.KeyRune && ev.Rune() == 'c' {
		link := strings.TrimSpace(p.swarmModal.DashboardLink)
		if link == "" {
			p.swarmModal.Status = "dashboard link unavailable"
			return
		}
		p.pendingSwarmAction = &SwarmModalAction{Kind: SwarmModalActionCopyDashboardLink, Text: link}
		p.swarmModal.Status = "copying dashboard link..."
	}
}

func (p *HomePage) drawSwarmModal(s tcell.Screen) {
	if p == nil || !p.swarmModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := minInt(maxInt(w-8, 48), 88)
	modalH := minInt(maxInt(h-6, 12), 22)
	rect := Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Primary.Bold(true), "─"+emptyValue(strings.TrimSpace(p.swarmModal.Title), "Swarm")+"─")
	y := rect.Y + 2
	maxRows := rect.H - 5
	for i := 0; i < maxRows && i < len(p.swarmModal.Lines); i++ {
		DrawText(s, rect.X+2, y+i, rect.W-4, p.theme.Text, clampEllipsis(p.swarmModal.Lines[i], rect.W-4))
	}
	status := emptyValue(strings.TrimSpace(p.swarmModal.Status), "Esc close • c copy dashboard link")
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(status, rect.W-4))
}
