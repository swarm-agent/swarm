package ui

import (
	"strings"
	"time"
	"unicode/utf8"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func (p *HomePage) PromptValue() string {
	return p.prompt
}

func (p *HomePage) PromptCursor() int {
	if p == nil {
		return 0
	}
	return clampRuneCursor(p.prompt, p.promptCursor)
}

func (p *HomePage) ClearPrompt() {
	if p == nil {
		return
	}
	p.prompt = ""
	p.promptCursor = 0
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
}

func (p *HomePage) SetPrompt(value string) {
	if p == nil {
		return
	}
	p.prompt = clampMultilineInput(value, homeMaxInputRunes)
	p.promptCursor = utf8.RuneCountInString(p.prompt)
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
}

func (p *HomePage) SetTheme(theme Theme) {
	p.theme = theme
}

func (p *HomePage) AcceptCommandPaletteEnter() bool {
	return p.acceptCommandPaletteEnter()
}

func (p *HomePage) SetModel(next model.HomeModel) {
	draftUsername := ""
	draftSwarmName := ""
	if p.onboarding.Visible {
		draftUsername = strings.TrimSpace(p.model.OnboardingUsername)
		draftSwarmName = strings.TrimSpace(p.model.OnboardingSwarmName)
	}
	if next.OnboardingRequired {
		if strings.TrimSpace(next.OnboardingUsername) == "" {
			next.OnboardingUsername = draftUsername
		}
		if strings.TrimSpace(next.OnboardingSwarmName) == "" {
			next.OnboardingSwarmName = draftSwarmName
		}
	}
	p.model = next
	p.sessionMode = normalizeHomeSessionMode(p.sessionMode)
	p.applySessionModeModel()
	if next.OnboardingRequired {
		p.ShowOnboardingLocked("Complete required setup before using Swarm.")
	}
}

func (p *HomePage) SetStatus(status string) {
	p.statusLine = strings.TrimSpace(status)
}

func normalizeHomeSessionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto":
		return "auto"
	case "read":
		return "read"
	case "readwrite":
		return "readwrite"
	default:
		return "plan"
	}
}

func nextHomeSessionMode(current string) string {
	switch normalizeHomeSessionMode(current) {
	case "plan":
		return "auto"
	case "auto":
		return "plan"
	default:
		return normalizeHomeSessionMode(current)
	}
}

func (p *HomePage) SetSessionMode(mode string) {
	p.sessionMode = normalizeHomeSessionMode(mode)
	p.applySessionModeModel()
}

func (p *HomePage) applySessionModeModel() {
	if p == nil || !p.model.ActiveAgentExitPlanMode {
		return
	}
	var provider, modelName, thinking, serviceTier, contextMode string
	if normalizeHomeSessionMode(p.sessionMode) == "auto" {
		provider, modelName = p.model.AutoModelProvider, p.model.AutoModelName
		thinking, serviceTier, contextMode = p.model.AutoThinkingLevel, p.model.AutoServiceTier, p.model.AutoContextMode
	} else {
		provider, modelName = p.model.PlanModelProvider, p.model.PlanModelName
		thinking, serviceTier, contextMode = p.model.PlanThinkingLevel, p.model.PlanServiceTier, p.model.PlanContextMode
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(modelName) == "" {
		return
	}
	p.model.ModelProvider = strings.TrimSpace(provider)
	p.model.ModelName = strings.TrimSpace(modelName)
	p.model.ThinkingLevel = strings.TrimSpace(thinking)
	p.model.ServiceTier = strings.TrimSpace(serviceTier)
	p.model.ContextMode = strings.TrimSpace(contextMode)
	p.model.QuickActions = homeProfileQuickActions(p.model)
}

func homeProfileQuickActions(m model.HomeModel) []string {
	if !m.AuthConfigured {
		return []string{"Auth: missing", "Run /auth"}
	}
	profile := "Agent model default"
	if strings.EqualFold(strings.TrimSpace(m.ActiveModelProfile.Source), "saved") {
		profile = emptyValue(strings.TrimSpace(m.ActiveModelProfile.Name), "Saved profile")
	} else if strings.EqualFold(strings.TrimSpace(m.ActiveModelProfile.Source), "temporary") {
		profile = "Temporary/customized"
	}
	modelLabel := model.DisplayModelLabel(m.ModelProvider, m.ModelName, m.ServiceTier, m.ContextMode)
	setup := strings.Join([]string{profile, modelLabel, emptyValue(m.ThinkingLevel, "unset"), emptyValue(m.ServiceTier, "default")}, " · ")
	return []string{"Profile: " + setup}
}

func (p *HomePage) SessionMode() string {
	return normalizeHomeSessionMode(p.sessionMode)
}

func (p *HomePage) ModelState() (provider, modelName, thinking, serviceTier, contextMode string) {
	if p == nil {
		return "", "", "", "", ""
	}
	return effectiveHomeModelState(p.model, p.sessionMode)
}

func effectiveHomeModelState(m model.HomeModel, sessionMode string) (provider, modelName, thinking, serviceTier, contextMode string) {
	provider, modelName = m.ModelProvider, m.ModelName
	thinking, serviceTier, contextMode = m.ThinkingLevel, m.ServiceTier, m.ContextMode
	if !m.ActiveAgentExitPlanMode {
		return
	}
	if normalizeHomeSessionMode(sessionMode) == "auto" {
		if strings.TrimSpace(m.AutoModelProvider) != "" && strings.TrimSpace(m.AutoModelName) != "" {
			return m.AutoModelProvider, m.AutoModelName, m.AutoThinkingLevel, m.AutoServiceTier, m.AutoContextMode
		}
		return
	}
	if strings.TrimSpace(m.PlanModelProvider) != "" && strings.TrimSpace(m.PlanModelName) != "" {
		return m.PlanModelProvider, m.PlanModelName, m.PlanThinkingLevel, m.PlanServiceTier, m.PlanContextMode
	}
	return
}

func (p *HomePage) ModelProfiles() []client.ModelProfile {
	if p == nil {
		return nil
	}
	return append([]client.ModelProfile(nil), p.model.ModelProfiles...)
}

func (p *HomePage) ActiveModelProfile() model.ActiveModelProfile {
	if p == nil {
		return model.ActiveModelProfile{}
	}
	return p.model.ActiveModelProfile
}

func (p *HomePage) ProfileLabel() string {
	if p == nil {
		return "Agent model default"
	}
	profile := p.model.ActiveModelProfile
	switch strings.ToLower(strings.TrimSpace(profile.Source)) {
	case "saved":
		if name := strings.TrimSpace(profile.Name); name != "" {
			return name
		}
		return "Saved profile"
	case "temporary":
		return "Temporary/customized"
	default:
		return "Agent model default"
	}
}

func homeDisplayedMode(m model.HomeModel, sessionMode string) string {
	if m.ActiveAgentRuntimeKnown {
		if m.ActiveAgentExitPlanMode {
			return normalizeHomeSessionMode(sessionMode)
		}
		switch strings.ToLower(strings.TrimSpace(m.ActiveAgentExecutionSetting)) {
		case "read":
			return "read"
		case "readwrite":
			return "readwrite"
		}
	}
	return normalizeHomeSessionMode(sessionMode)
}

func currentDisplayedHomeSessionMode(page *HomePage) string {
	if page == nil {
		return "on"
	}
	if normalizeHomeSessionMode(page.sessionMode) == "plan" {
		return "on"
	}
	return "off"
}

func homeAgentModeCapability(m model.HomeModel, sessionMode string) string {
	if !m.ActiveAgentRuntimeKnown {
		return normalizeHomeSessionMode(sessionMode)
	}
	if m.ActiveAgentExitPlanMode {
		return "Plan on/off"
	}
	return homeDisplayedMode(m, sessionMode)
}

func currentHomeAgentModeCapability(page *HomePage) string {
	if page == nil {
		return "plan"
	}
	return homeAgentModeCapability(page.model, page.sessionMode)
}

func (p *HomePage) CanCycleSessionMode() bool {
	return p != nil && p.model.ActiveAgentExitPlanMode &&
		strings.TrimSpace(p.model.PlanModelProvider) != "" && strings.TrimSpace(p.model.PlanModelName) != "" &&
		strings.TrimSpace(p.model.AutoModelProvider) != "" && strings.TrimSpace(p.model.AutoModelName) != ""
}

func (p *HomePage) SetVoiceInputState(state VoiceInputState) {
	p.voiceInput = state
}

func (p *HomePage) ShowToast(level ToastLevel, message string) {
	p.toast.show(level, message, toastDefaultDuration)
}

func (p *HomePage) ShowToastForDuration(level ToastLevel, message string, duration time.Duration) {
	p.toast.show(level, message, duration)
}

func (p *HomePage) Status() string {
	return p.statusLine
}

func (p *HomePage) SetCommandOverlay(lines []string) {
	p.commandOverlay = append([]string(nil), lines...)
}

func (p *HomePage) ClearCommandOverlay() {
	p.commandOverlay = nil
}

func (p *HomePage) CommandOverlayLines() []string {
	return append([]string(nil), p.commandOverlay...)
}

func (p *HomePage) ModelPresets() []string {
	out := make([]string, 0, len(p.model.QuickActions))
	for _, item := range p.model.QuickActions {
		if strings.HasPrefix(item, "Model: ") {
			out = append(out, item[len("Model: "):])
		}
	}
	if len(out) == 0 && p.model.AuthConfigured {
		if label := model.DisplayModelLabel(p.model.ModelProvider, p.model.ModelName, p.model.ServiceTier, p.model.ContextMode); label != "unset" {
			out = append(out, label)
		}
	}
	return out
}

func (p *HomePage) ActiveWorkspaceName() string {
	return p.activeWorkspaceName()
}

func (p *HomePage) ActiveDirectory() model.DirectoryItem {
	return p.primaryDirectory()
}

func (p *HomePage) ActivePlanName() string {
	return p.activePlanName()
}

func (p *HomePage) SetKeyBindings(keybinds *KeyBindings) {
	if keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
		return
	}
	p.keybinds = keybinds
}

func (p *HomePage) KeyBindings() *KeyBindings {
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	return p.keybinds
}

func (p *HomePage) SetSwarmName(name string) {
	if p == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Local"
	}
	p.swarmName = name
}

func (p *HomePage) SetSwarmNotificationCount(count int) {
	if p == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	p.swarmNotificationCount = count
}
