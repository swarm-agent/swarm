package ui

import (
	"strings"
	"time"
	"unicode/utf8"

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
	p.model = next
	p.sessionMode = normalizeHomeSessionMode(p.sessionMode)
	total := len(p.model.RecentSessions)
	if total == 0 {
		p.selectedIndex = 0
		p.recentPage = 0
		p.sessionsFocused = false
		p.pendingHomeAction = nil
	} else {
		selectedID := ""
		if p.selectedIndex >= 0 && p.selectedIndex < len(p.model.RecentSessions) {
			selectedID = strings.TrimSpace(p.model.RecentSessions[p.selectedIndex].ID)
		}
		if selectedID != "" {
			for idx := range next.RecentSessions {
				if strings.TrimSpace(next.RecentSessions[idx].ID) == selectedID {
					p.selectedIndex = idx
					selectedID = ""
					break
				}
			}
		}
		if p.selectedIndex >= total {
			p.selectedIndex = total - 1
		}
		if p.selectedIndex < 0 {
			p.selectedIndex = 0
		}
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
}

func (p *HomePage) SessionMode() string {
	return normalizeHomeSessionMode(p.sessionMode)
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
		return "plan"
	}
	return homeDisplayedMode(page.model, page.sessionMode)
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

func (p *HomePage) SessionsFocused() bool {
	return p.sessionsFocused
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
