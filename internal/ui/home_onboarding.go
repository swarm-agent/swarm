package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type onboardingFocus int

const (
	onboardingFocusUsername onboardingFocus = iota
	onboardingFocusSwarmName
	onboardingFocusProviders
)

type onboardingState struct {
	Visible bool
	Locked  bool
	Focus   onboardingFocus
	Status  string
	Error   string
}

func (p *HomePage) SetOnboardingRequired(required bool, username, swarmName string) {
	if p == nil {
		return
	}
	p.model.OnboardingRequired = required
	p.model.OnboardingUsername = strings.TrimSpace(username)
	p.model.OnboardingSwarmName = strings.TrimSpace(swarmName)
	if required {
		p.ShowOnboardingLocked("Complete required identity setup before using Swarm.")
		return
	}
	if p.onboarding.Visible && !p.identityOnboardingComplete() {
		p.onboarding = onboardingState{}
	}
}

func (p *HomePage) ShowOnboardingLocked(status string) {
	if p == nil {
		return
	}
	p.onboarding.Visible = true
	p.onboarding.Locked = true
	if p.onboarding.Focus < onboardingFocusUsername || p.onboarding.Focus > onboardingFocusProviders {
		p.onboarding.Focus = onboardingFocusUsername
	}
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) OnboardingVisible() bool {
	return p != nil && p.onboarding.Visible
}

func (p *HomePage) HideOnboarding() {
	if p == nil {
		return
	}
	if p.onboarding.Locked && !p.identityOnboardingComplete() {
		return
	}
	p.onboarding = onboardingState{}
}

func (p *HomePage) SetOnboardingStatus(status string) {
	if p == nil {
		return
	}
	p.onboarding.Status = strings.TrimSpace(status)
	p.onboarding.Error = ""
}

func (p *HomePage) SetOnboardingError(message string) {
	if p == nil {
		return
	}
	p.onboarding.Error = strings.TrimSpace(message)
}

func (p *HomePage) handleOnboardingKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.onboarding.Visible {
		return
	}
	switch {
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown):
		p.advanceOnboardingFocus(1)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp):
		p.advanceOnboardingFocus(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		p.deleteOnboardingRune()
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		p.clearOnboardingField()
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		p.submitOnboardingStep()
		return
	case p.keybinds.Match(ev, KeybindEditorClose):
		if p.identityOnboardingComplete() {
			p.HideOnboarding()
			p.statusLine = "Provider auth skipped. Needs auth remains until /auth is completed."
		} else {
			p.onboarding.Error = "Username and swarm name are required before leaving onboarding."
		}
		return
	}
	if ev.Key() != tcell.KeyRune {
		return
	}
	r := ev.Rune()
	if !unicode.IsPrint(r) {
		return
	}
	switch p.onboarding.Focus {
	case onboardingFocusUsername:
		p.model.OnboardingUsername += string(r)
	case onboardingFocusSwarmName:
		p.model.OnboardingSwarmName += string(r)
	case onboardingFocusProviders:
		switch r {
		case 'a', 'A', 'l', 'L':
			p.ShowAuthModal()
			p.openSelectedOnboardingProviderAuth()
		case 's', 'S':
			p.HideOnboarding()
			p.statusLine = "Provider auth skipped. Needs auth remains until /auth is completed."
		}
	}
}

func (p *HomePage) advanceOnboardingFocus(delta int) {
	maxFocus := onboardingFocusSwarmName
	if p.identityOnboardingComplete() {
		maxFocus = onboardingFocusProviders
	}
	if delta == 0 {
		return
	}
	idx := int(p.onboarding.Focus)
	if delta > 0 {
		idx++
	} else {
		idx--
	}
	if idx < 0 {
		idx = int(maxFocus)
	}
	if idx > int(maxFocus) {
		idx = 0
	}
	p.onboarding.Focus = onboardingFocus(idx)
	p.onboarding.Error = ""
}

func (p *HomePage) deleteOnboardingRune() {
	field := p.onboardingField()
	if field == nil || len(*field) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(*field)
	if size <= 0 {
		return
	}
	*field = (*field)[:len(*field)-size]
}

func (p *HomePage) clearOnboardingField() {
	field := p.onboardingField()
	if field != nil {
		*field = ""
	}
}

func (p *HomePage) onboardingField() *string {
	switch p.onboarding.Focus {
	case onboardingFocusUsername:
		return &p.model.OnboardingUsername
	case onboardingFocusSwarmName:
		return &p.model.OnboardingSwarmName
	default:
		return nil
	}
}

func (p *HomePage) identityOnboardingComplete() bool {
	return strings.TrimSpace(p.model.OnboardingUsername) != "" && strings.TrimSpace(p.model.OnboardingSwarmName) != ""
}

func (p *HomePage) submitOnboardingStep() {
	if p.identityOnboardingComplete() {
		if p.onboarding.Focus == onboardingFocusProviders {
			p.ShowAuthModal()
			p.openSelectedOnboardingProviderAuth()
			return
		}
		p.pendingHomeAction = &HomeAction{
			Kind:      HomeActionSaveOnboarding,
			Username:  strings.TrimSpace(p.model.OnboardingUsername),
			SwarmName: strings.TrimSpace(p.model.OnboardingSwarmName),
		}
		p.onboarding.Status = "Saving identity setup..."
		p.onboarding.Error = ""
		return
	}
	if strings.TrimSpace(p.model.OnboardingUsername) == "" {
		p.onboarding.Focus = onboardingFocusUsername
		p.onboarding.Error = "Username is required."
		return
	}
	if strings.TrimSpace(p.model.OnboardingSwarmName) == "" {
		p.onboarding.Focus = onboardingFocusSwarmName
		p.onboarding.Error = "Swarm name is required."
		return
	}
}

func (p *HomePage) openSelectedOnboardingProviderAuth() {
	if len(p.authModal.Providers) == 0 {
		p.authModal.Status = "Auth manager loading. Select a provider, then press Enter to add credentials."
		return
	}
	p.authModal.Focus = authModalFocusProviders
	p.authModal.reconcileSelections()
	providerID := p.selectedAuthProviderID()
	if providerID == "" {
		p.authModal.Status = "Select a provider, then press Enter to add credentials."
		return
	}
	p.triggerProviderLogin(providerID)
}

func (p *HomePage) drawOnboarding(s tcell.Screen) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	w, h := s.Size()
	boxW := minInt(86, w-4)
	if boxW < 48 {
		boxW = w - 2
	}
	boxH := 17
	if boxH > h-2 {
		boxH = h - 2
	}
	if boxW <= 8 || boxH <= 8 {
		return
	}
	rect := Rect{X: (w - boxW) / 2, Y: (h - boxH) / 2, W: boxW, H: boxH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Warning, "Required Swarm setup")
	lines := []struct {
		label string
		value string
		focus onboardingFocus
	}{
		{label: "Username", value: p.model.OnboardingUsername, focus: onboardingFocusUsername},
		{label: "Swarm name", value: p.model.OnboardingSwarmName, focus: onboardingFocusSwarmName},
	}
	y := rect.Y + 2
	DrawText(s, rect.X+2, y, rect.W-4, p.theme.TextMuted, "Create the local product identity before using protected actions.")
	y += 2
	for _, line := range lines {
		style := p.theme.Text
		prefix := "  "
		if p.onboarding.Focus == line.focus {
			style = p.theme.Primary
			prefix = "> "
		}
		value := strings.TrimSpace(line.value)
		if value == "" {
			value = "<required>"
		}
		DrawText(s, rect.X+2, y, rect.W-4, style, clampEllipsis(prefix+line.label+": "+value, rect.W-4))
		y++
	}
	y++
	identityComplete := p.identityOnboardingComplete()
	providerStyle := p.theme.TextMuted
	providerText := "Provider auth: locked until username and swarm name are filled"
	if identityComplete {
		providerStyle = p.theme.Warning
		if p.onboarding.Focus == onboardingFocusProviders {
			providerStyle = p.theme.Primary
		}
		providerText = "Provider auth: Enter adds credentials, s skips for now (Needs auth remains; resolve with /auth)"
	}
	for _, line := range Wrap(providerText, rect.W-4) {
		if y >= rect.Y+rect.H-5 {
			break
		}
		DrawText(s, rect.X+2, y, rect.W-4, providerStyle, line)
		y++
	}
	status := strings.TrimSpace(p.onboarding.Status)
	statusStyle := p.theme.TextMuted
	if errText := strings.TrimSpace(p.onboarding.Error); errText != "" {
		status = errText
		statusStyle = p.theme.Error
	}
	if status != "" {
		for _, line := range Wrap(status, rect.W-4) {
			if y >= rect.Y+rect.H-3 {
				break
			}
			DrawText(s, rect.X+2, y, rect.W-4, statusStyle, line)
			y++
		}
	}
	help := "Tab/↑/↓ move • type to edit • Enter continue/save • Ctrl+U clear"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(help, rect.W-4))
}
