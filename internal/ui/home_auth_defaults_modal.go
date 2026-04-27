package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

type AuthDefaultsInfo struct {
	Provider        string
	PrimaryModel    string
	PrimaryThinking string
	UtilityProvider string
	UtilityModel    string
	UtilityThinking string
	Subagents       []string
}

type authDefaultsInfoModalState struct {
	Visible bool
	Info    AuthDefaultsInfo
}

func (p *HomePage) ShowAuthDefaultsInfo(info *AuthDefaultsInfo) {
	if p == nil || info == nil {
		return
	}
	provider := strings.TrimSpace(info.Provider)
	primaryModel := strings.TrimSpace(info.PrimaryModel)
	if provider == "" || primaryModel == "" {
		return
	}
	utilityModel := strings.TrimSpace(info.UtilityModel)
	subagents := uniqueInfoValues(info.Subagents)
	if utilityModel == "" || len(subagents) == 0 {
		return
	}
	utilityProvider := strings.TrimSpace(info.UtilityProvider)
	if utilityProvider == "" {
		utilityProvider = provider
	}
	p.authDefaultsInfoModal.Visible = true
	p.authDefaultsInfoModal.Info = AuthDefaultsInfo{
		Provider:        provider,
		PrimaryModel:    primaryModel,
		PrimaryThinking: strings.ToLower(strings.TrimSpace(info.PrimaryThinking)),
		UtilityProvider: utilityProvider,
		UtilityModel:    utilityModel,
		UtilityThinking: strings.ToLower(strings.TrimSpace(info.UtilityThinking)),
		Subagents:       subagents,
	}
}

func (p *HomePage) HideAuthDefaultsInfo() {
	if p == nil {
		return
	}
	p.authDefaultsInfoModal = authDefaultsInfoModalState{}
}

func (p *HomePage) AuthDefaultsInfoVisible() bool {
	if p == nil {
		return false
	}
	return p.authDefaultsInfoModal.Visible
}

func (p *HomePage) handleAuthDefaultsInfoKey(ev *tcell.EventKey) {
	if !p.AuthDefaultsInfoVisible() {
		return
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose), p.keybinds.Match(ev, KeybindModalEnter):
		p.HideAuthDefaultsInfo()
		return
	}
	if ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyEnter {
		p.HideAuthDefaultsInfo()
	}
}

func (p *HomePage) drawAuthDefaultsInfoModal(s tcell.Screen) {
	if !p.AuthDefaultsInfoVisible() {
		return
	}
	w, h := s.Size()
	if w < 40 || h < 10 {
		return
	}

	modalW := minInt(92, w-8)
	if modalW < 56 {
		modalW = w - 2
	}
	if modalW < 40 {
		return
	}
	modalH := minInt(18, h-4)
	if modalH < 14 {
		modalH = h - 2
	}
	if modalH < 10 {
		return
	}
	rect := Rect{X: maxInt(1, (w-modalW)/2), Y: maxInt(1, (h-modalH)/2), W: modalW, H: modalH}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Recommended agent defaults applied")

	info := p.authDefaultsInfoModal.Info
	lines := make([]string, 0, 6)
	primary := fmt.Sprintf("Primary default: %s/%s", info.Provider, model.DisplayModelName(info.Provider, info.PrimaryModel))
	if info.PrimaryThinking != "" {
		primary += " (thinking: " + info.PrimaryThinking + ")"
	}
	utility := fmt.Sprintf("Utility default: %s/%s", info.UtilityProvider, model.DisplayModelName(info.UtilityProvider, info.UtilityModel))
	if info.UtilityThinking != "" {
		utility += " (thinking: " + info.UtilityThinking + ")"
	}
	lines = append(lines,
		primary,
		utility,
		"Applied to subagents: "+strings.Join(info.Subagents, ", "),
		"These are our recommended startup modes.",
		"Change anytime in /agents.",
	)

	bodyY := rect.Y + 2
	maxLines := rect.H - 5
	for i := 0; i < len(lines) && i < maxLines; i++ {
		style := p.theme.Text
		if i >= len(lines)-2 {
			style = p.theme.TextMuted
		}
		DrawText(s, rect.X+2, bodyY+i, rect.W-4, style, clampEllipsis(lines[i], rect.W-4))
	}

	hint := "Press Enter or Esc to continue"
	if rect.W < 52 {
		hint = "Enter/Esc continue"
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(hint, rect.W-4))
}

func uniqueInfoValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
