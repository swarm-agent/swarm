package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type onboardingPhase int

const (
	onboardingPhaseIdentity onboardingPhase = iota
	onboardingPhaseProvider
	onboardingPhaseWorkspace
)

type onboardingFocus int

const (
	onboardingFocusUsername onboardingFocus = iota
	onboardingFocusSwarmName
)

type onboardingState struct {
	Visible        bool
	Locked         bool
	Phase          onboardingPhase
	Focus          onboardingFocus
	Status         string
	Error          string
	Pending        bool
	WorkspacePath  string
	WorkspaceReady bool
}

func (p *HomePage) SetOnboardingRequired(required bool, username, swarmName string) {
	if p == nil {
		return
	}
	p.model.OnboardingRequired = required
	p.model.OnboardingUsername = strings.TrimSpace(username)
	p.model.OnboardingSwarmName = strings.TrimSpace(swarmName)
	if required {
		p.ShowOnboardingLocked("Complete required setup before using Swarm.")
	}
}

func (p *HomePage) ShowOnboardingLocked(status string) {
	if p == nil {
		return
	}
	wasVisible := p.onboarding.Visible
	p.onboarding.Visible = true
	p.onboarding.Locked = true
	if !wasVisible {
		p.onboarding.Phase = onboardingPhaseIdentity
		p.onboarding.Focus = onboardingFocusUsername
	}
	if p.onboarding.WorkspacePath == "" {
		p.onboarding.WorkspacePath = strings.TrimSpace(p.model.CWD)
	}
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) OnboardingVisible() bool {
	return p != nil && p.onboarding.Visible
}

func (p *HomePage) OnboardingProviderActive() bool {
	return p != nil && p.onboarding.Visible && p.onboarding.Phase == onboardingPhaseProvider
}

func (p *HomePage) OnboardingWorkspaceActive() bool {
	return p != nil && p.onboarding.Visible && p.onboarding.Phase == onboardingPhaseWorkspace
}

func (p *HomePage) SetOnboardingWorkspacePath(path string) {
	if p == nil || strings.TrimSpace(path) == "" {
		return
	}
	p.onboarding.WorkspacePath = strings.TrimSpace(path)
}

func (p *HomePage) ShowOnboardingProvider(status string) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	p.onboarding.Phase = onboardingPhaseProvider
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	p.authModal.Focus = authModalFocusProviders
	p.authModal.reconcileSelections()
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) ShowOnboardingWorkspace(status string) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	p.authModal.Editor = nil
	p.authModal.Login = nil
	p.authModal.Loading = false
	p.onboarding.Phase = onboardingPhaseWorkspace
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) CompleteOnboardingWorkspace() {
	if p == nil {
		return
	}
	p.onboarding.WorkspaceReady = true
	p.onboarding.Pending = false
	p.onboarding = onboardingState{}
	p.model.OnboardingRequired = false
}

func (p *HomePage) HideOnboarding() {
	if p == nil {
		return
	}
	if p.onboarding.Locked && !p.onboarding.WorkspaceReady {
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
	p.onboarding.Pending = false
	p.onboarding.Error = strings.TrimSpace(message)
}

func (p *HomePage) handleOnboardingKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.onboarding.Visible || p.onboarding.Pending {
		return
	}
	switch p.onboarding.Phase {
	case onboardingPhaseProvider:
		p.handleOnboardingProviderKey(ev)
	case onboardingPhaseWorkspace:
		p.handleOnboardingWorkspaceKey(ev)
	default:
		p.handleOnboardingIdentityKey(ev)
	}
}

func (p *HomePage) handleOnboardingIdentityKey(ev *tcell.EventKey) {
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
		p.submitOnboardingIdentity()
		return
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.onboarding.Error = "Finish all three setup steps before entering Swarm."
		return
	}
	if ev.Key() != tcell.KeyRune || !unicode.IsPrint(ev.Rune()) {
		return
	}
	if p.onboarding.Focus == onboardingFocusUsername {
		p.model.OnboardingUsername += string(ev.Rune())
	} else {
		p.model.OnboardingSwarmName += string(ev.Rune())
	}
	p.onboarding.Error = ""
}

func (p *HomePage) handleOnboardingProviderKey(ev *tcell.EventKey) {
	if p.authModal.Editor != nil {
		p.handleAuthModalEditorKey(ev)
		return
	}
	if p.keybinds.Match(ev, KeybindEditorClose) {
		p.ShowOnboardingWorkspace("Provider skipped. Confirm your launch workspace to finish setup.")
		return
	}
	if ev.Key() == tcell.KeyRune && (ev.Rune() == 's' || ev.Rune() == 'S') {
		p.ShowOnboardingWorkspace("Provider skipped. Confirm your launch workspace to finish setup.")
		return
	}
	switch {
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown), ev.Key() == tcell.KeyRight:
		p.authModal.Focus = authModalFocusProviders
		p.moveAuthModalSelection(1)
		p.onboarding.Error = ""
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp), ev.Key() == tcell.KeyLeft:
		p.authModal.Focus = authModalFocusProviders
		p.moveAuthModalSelection(-1)
		p.onboarding.Error = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		providerID := p.selectedAuthProviderID()
		if providerID == "" {
			p.onboarding.Error = "No provider is available yet. Press s to continue without one."
			return
		}
		p.triggerProviderLogin(providerID)
		return
	}
}

func (p *HomePage) handleOnboardingWorkspaceKey(ev *tcell.EventKey) {
	if p.keybinds.Match(ev, KeybindEditorClose) {
		p.onboarding.Error = "A workspace is required before entering Swarm. Press Enter to create it."
		return
	}
	if !p.keybinds.Match(ev, KeybindEditorSubmit) {
		return
	}
	path := strings.TrimSpace(p.onboarding.WorkspacePath)
	if path == "" {
		p.onboarding.Error = "The launch directory is unavailable; restart Swarm from the workspace you want to use."
		return
	}
	p.pendingHomeAction = &HomeAction{Kind: HomeActionCreateOnboardingWorkspace, WorkspacePath: path}
	p.onboarding.Pending = true
	p.onboarding.Status = "Creating workspace and loading Swarm..."
	p.onboarding.Error = ""
}

func (p *HomePage) advanceOnboardingFocus(delta int) {
	if delta == 0 {
		return
	}
	if p.onboarding.Focus == onboardingFocusUsername {
		p.onboarding.Focus = onboardingFocusSwarmName
	} else {
		p.onboarding.Focus = onboardingFocusUsername
	}
	p.onboarding.Error = ""
}

func (p *HomePage) deleteOnboardingRune() {
	field := p.onboardingField()
	if field == nil || len(*field) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(*field)
	if size > 0 {
		*field = (*field)[:len(*field)-size]
	}
}

func (p *HomePage) clearOnboardingField() {
	if field := p.onboardingField(); field != nil {
		*field = ""
	}
}

func (p *HomePage) onboardingField() *string {
	if p.onboarding.Focus == onboardingFocusUsername {
		return &p.model.OnboardingUsername
	}
	return &p.model.OnboardingSwarmName
}

func (p *HomePage) identityOnboardingComplete() bool {
	return strings.TrimSpace(p.model.OnboardingUsername) != "" && strings.TrimSpace(p.model.OnboardingSwarmName) != ""
}

func (p *HomePage) submitOnboardingIdentity() {
	if strings.TrimSpace(p.model.OnboardingUsername) == "" {
		p.onboarding.Focus = onboardingFocusUsername
		p.onboarding.Error = "Your name is required."
		return
	}
	if strings.TrimSpace(p.model.OnboardingSwarmName) == "" {
		p.onboarding.Focus = onboardingFocusSwarmName
		p.onboarding.Error = "Swarm name is required."
		return
	}
	p.pendingHomeAction = &HomeAction{
		Kind:      HomeActionSaveOnboarding,
		Username:  strings.TrimSpace(p.model.OnboardingUsername),
		SwarmName: strings.TrimSpace(p.model.OnboardingSwarmName),
	}
	p.onboarding.Pending = true
	p.onboarding.Status = "Saving identity..."
	p.onboarding.Error = ""
}

func (p *HomePage) drawOnboarding(s tcell.Screen) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	w, h := s.Size()
	boxW := minInt(92, w-4)
	if boxW < 48 {
		boxW = w - 2
	}
	boxH := minInt(24, h-2)
	if boxW <= 8 || boxH <= 10 {
		return
	}
	rect := Rect{X: (w - boxW) / 2, Y: (h - boxH) / 2, W: boxW, H: boxH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	p.drawOnboardingHeader(s, rect)

	content := Rect{X: rect.X + 3, Y: rect.Y + 6, W: rect.W - 6, H: rect.H - 10}
	switch p.onboarding.Phase {
	case onboardingPhaseProvider:
		p.drawOnboardingProvider(s, content)
	case onboardingPhaseWorkspace:
		p.drawOnboardingWorkspace(s, content)
	default:
		p.drawOnboardingIdentity(s, content)
	}

	status := strings.TrimSpace(p.onboarding.Status)
	statusStyle := p.theme.TextMuted
	if p.onboarding.Phase == onboardingPhaseProvider {
		if authStatus := strings.TrimSpace(p.authModal.Status); authStatus != "" {
			status = authStatus
		}
		if authError := strings.TrimSpace(p.authModal.Error); authError != "" {
			status = authError
			statusStyle = p.theme.Error
		}
	}
	if errText := strings.TrimSpace(p.onboarding.Error); errText != "" {
		status = errText
		statusStyle = p.theme.Error
	}
	if status != "" {
		lines := Wrap(status, rect.W-6)
		for i, line := range lines {
			if i >= 2 {
				break
			}
			DrawText(s, rect.X+3, rect.Y+rect.H-4+i, rect.W-6, statusStyle, line)
		}
	}
	help := "Tab/↑/↓ move • Enter continue"
	if p.onboarding.Phase == onboardingPhaseProvider {
		help = "←/→ select provider • Enter connect • s/Esc skip to workspace"
	} else if p.onboarding.Phase == onboardingPhaseWorkspace {
		help = "Enter create workspace • setup cannot be skipped"
	}
	DrawText(s, rect.X+3, rect.Y+rect.H-2, rect.W-6, p.theme.TextMuted, clampEllipsis(help, rect.W-6))
}

func (p *HomePage) drawOnboardingHeader(s tcell.Screen, rect Rect) {
	step := int(p.onboarding.Phase) + 1
	labels := []string{"Identity", "Provider", "Workspace"}
	DrawText(s, rect.X+3, rect.Y+1, rect.W-6, p.theme.Text, "SWARM  ·  FIRST LAUNCH")
	DrawText(s, rect.X+3, rect.Y+2, rect.W-6, p.theme.TextMuted, fmt.Sprintf("STEP %d OF 3  ·  %s", step, labels[step-1]))
	barW := maxInt(3, (rect.W-8)/3)
	for i := 0; i < 3; i++ {
		style := p.theme.Border
		marker := strings.Repeat("─", barW)
		if i == step-1 {
			style = p.theme.Primary
			marker = strings.Repeat("━", barW)
		}
		DrawText(s, rect.X+3+i*(barW+1), rect.Y+3, barW, style, marker)
	}
	titles := []string{"Name your Swarm.", "Connect your AI provider.", "Create your first workspace."}
	subtitles := []string{
		"Start with your name and the name of this Swarm.",
		"Connect now, or skip ahead. Your workspace is still required.",
		"Swarm will register the directory where you launched the TUI.",
	}
	DrawText(s, rect.X+3, rect.Y+4, rect.W-6, p.theme.Text, titles[step-1])
	DrawText(s, rect.X+3, rect.Y+5, rect.W-6, p.theme.TextMuted, clampEllipsis(subtitles[step-1], rect.W-6))
}

func (p *HomePage) drawOnboardingIdentity(s tcell.Screen, content Rect) {
	fields := []struct {
		label string
		value string
		focus onboardingFocus
	}{
		{label: "Your name", value: p.model.OnboardingUsername, focus: onboardingFocusUsername},
		{label: "Swarm name", value: p.model.OnboardingSwarmName, focus: onboardingFocusSwarmName},
	}
	y := content.Y + 1
	for _, field := range fields {
		DrawText(s, content.X+1, y, content.W-2, p.theme.TextMuted, field.label)
		fieldRect := Rect{X: content.X, Y: y + 1, W: content.W, H: 3}
		border := p.theme.Border
		valueStyle := p.theme.Text
		if p.onboarding.Focus == field.focus {
			border = p.theme.BorderActive
			valueStyle = p.theme.Primary
		}
		DrawBox(s, fieldRect, border)
		value := field.value
		if value == "" {
			value = "Type here"
			valueStyle = p.theme.TextMuted
		}
		DrawText(s, fieldRect.X+2, fieldRect.Y+1, fieldRect.W-4, valueStyle, clampTail(value, fieldRect.W-4))
		y += 5
	}
}

func (p *HomePage) drawOnboardingProvider(s tcell.Screen, content Rect) {
	if p.authModal.Editor != nil {
		p.drawAuthModalEditor(s, Rect{X: content.X - 2, Y: content.Y - 1, W: content.W + 4, H: content.H + 3})
		return
	}
	providers := p.authModal.Providers
	if len(providers) == 0 {
		DrawText(s, content.X, content.Y+2, content.W, p.theme.TextMuted, "No providers loaded yet.")
		DrawText(s, content.X, content.Y+4, content.W, p.theme.Warning, "Press s to continue without a provider.")
		return
	}
	selected := p.authModal.SelectedProvider
	if selected < 0 || selected >= len(providers) {
		selected = 0
	}

	const columns = 2
	const gutter = 2
	const cardHeight = 3
	const rowGap = 1
	cardW := (content.W - gutter) / columns
	maxRows := maxInt(1, (content.H-1)/(cardHeight+rowGap))
	totalRows := (len(providers) + columns - 1) / columns
	selectedRow := selected / columns
	startRow := maxInt(0, selectedRow-maxRows/2)
	startRow = minInt(startRow, maxInt(0, totalRows-maxRows))
	start := startRow * columns
	end := minInt(len(providers), start+maxRows*columns)

	for i := start; i < end; i++ {
		visibleIndex := i - start
		row := visibleIndex / columns
		column := visibleIndex % columns
		provider := providers[i]
		card := Rect{
			X: content.X + column*(cardW+gutter),
			Y: content.Y + 1 + row*(cardHeight+rowGap),
			W: cardW,
			H: cardHeight,
		}
		style := p.theme.Border
		textStyle := p.theme.Text
		prefix := "  "
		if i == selected {
			style = p.theme.BorderActive
			textStyle = p.theme.Primary
			prefix = "› "
		}
		DrawBox(s, card, style)
		state := "needs auth"
		if provider.Ready {
			state = "connected"
		}
		label := fmt.Sprintf("%s%s  ·  %s", prefix, provider.ID, state)
		DrawText(s, card.X+1, card.Y+1, card.W-2, textStyle, clampEllipsis(label, card.W-2))
	}
}

func (p *HomePage) drawOnboardingWorkspace(s tcell.Screen, content Rect) {
	path := strings.TrimSpace(p.onboarding.WorkspacePath)
	if path == "" {
		path = "launch directory unavailable"
	}
	card := Rect{X: content.X, Y: content.Y + 2, W: content.W, H: 5}
	DrawBox(s, card, p.theme.BorderActive)
	DrawText(s, card.X+2, card.Y+1, card.W-4, p.theme.TextMuted, "Creating workspace in")
	DrawText(s, card.X+2, card.Y+2, card.W-4, p.theme.Primary, clampTail(path, card.W-4))
	if p.onboarding.Pending {
		DrawText(s, card.X+2, card.Y+3, card.W-4, p.theme.Warning, "Please wait — confirming API completion and loading workspace state...")
	} else {
		DrawText(s, card.X+2, card.Y+3, card.W-4, p.theme.Text, "Press Enter to accept")
	}
}
