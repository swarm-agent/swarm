package ui

import (
	"strings"
	"time"
	"unicode/utf8"
)

func (p *ChatPage) InputValue() string {
	return p.input
}

func (p *ChatPage) InputCursor() int {
	if p == nil {
		return 0
	}
	return clampRuneCursor(p.input, p.inputCursor)
}

func (p *ChatPage) ClearInput() {
	if p == nil {
		return
	}
	p.input = ""
	p.inputCursor = 0
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.syncComposerPalettes()
}

func (p *ChatPage) SetInput(value string) {
	if p == nil {
		return
	}
	before := p.input
	p.input = clampMultilineInput(value, chatMaxInputRunes)
	p.inputCursor = utf8.RuneCountInString(p.input)
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.maybeWarnLargeInput(before, p.input)
	p.syncComposerPalettes()
}

func (p *ChatPage) SetTheme(theme Theme) {
	p.theme = theme
	p.bumpTimelineRenderGeneration()
}

func (p *ChatPage) AcceptCommandPaletteEnter() bool {
	return p.acceptCommandPaletteEnter()
}

func (p *ChatPage) SetStatus(status string) {
	p.statusLine = strings.TrimSpace(status)
	p.errorLine = ""
}

func (p *ChatPage) Status() string {
	if p == nil {
		return ""
	}
	return p.statusLine
}

func (p *ChatPage) SetSessionTitle(title string) {
	if p == nil {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	p.sessionTitle = title
	currentID := strings.TrimSpace(p.sessionID)
	for i := range p.sessionTabs {
		if strings.TrimSpace(p.sessionTabs[i].ID) != currentID {
			continue
		}
		p.sessionTabs[i].Title = title
		p.sessionsPaletteItems = normalizeChatSessionPaletteItems(p.sessionTabs)
		return
	}
	p.sessionTabs = normalizeChatSessionTabs(p.sessionTabs, currentID, title)
	p.sessionsPaletteItems = normalizeChatSessionPaletteItems(p.sessionTabs)
}

func (p *ChatPage) SetSessionBranch(branch string) {
	if p == nil {
		return
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "-"
	}
	p.meta.Branch = branch
}

func (p *ChatPage) SetAgentRuntime(agent, executionSetting string, exitPlanModeEnabled, runtimeKnown bool) {
	if p == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "swarm"
	}
	p.meta.Agent = agent
	p.meta.AgentExecutionSetting = normalizeAgentExecutionSetting(executionSetting)
	p.meta.AgentExitPlanMode = exitPlanModeEnabled
	p.meta.AgentRuntimeKnown = runtimeKnown
}

func (p *ChatPage) SetSessionPath(path string) {
	if p == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	p.meta.Path = path
}

func (p *ChatPage) SetSessionTabs(tabs []ChatSessionTab) {
	if p == nil {
		return
	}
	normalized := normalizeChatSessionTabs(tabs, p.sessionID, p.sessionTitle)
	p.sessionTabs = normalized
	p.sessionsPaletteItems = normalizeChatSessionPaletteItems(normalized)
}

func (p *ChatPage) ApplySessionTitleWarning(warning string) {
	if p == nil {
		return
	}
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return
	}
	p.statusLine = warning
	p.appendSystemMessage(warning)
}

func (p *ChatPage) SetVoiceInputState(state VoiceInputState) {
	p.voiceInput = state
}

func (p *ChatPage) PermissionModalVisible() bool {
	return p.permissionModalActive() || p.planUpdateModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive()
}

func (p *ChatPage) AgentChangeModalVisible() bool {
	return p.agentChangeModalActive()
}

func (p *ChatPage) ExitPlanModalVisible() bool {
	return p.planExitModalActive()
}

func (p *ChatPage) AskUserModalVisible() bool {
	return p.askUserModalActive()
}

func (p *ChatPage) SetSessionMode(mode string) {
	if p == nil {
		return
	}
	p.applySessionMode(mode, false)
}

func (p *ChatPage) SessionMode() string {
	return normalizeSessionMode(p.sessionMode)
}

func (p *ChatPage) SessionID() string {
	return strings.TrimSpace(p.sessionID)
}

func (p *ChatPage) OpenCurrentPlanModal(plan ChatSessionPlan) bool {
	if p == nil {
		return false
	}
	if p.planUpdateModalActive() || p.permissionModalActive() || p.askUserModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive() || p.sessionsPaletteActive() {
		return false
	}
	if p.planExitModalActive() {
		p.closePlanExitModal()
	}
	p.openPlanEditorModal(plan)
	return true
}

func (p *ChatPage) SessionPaletteItems() []ChatSessionPaletteItem {
	if p == nil {
		return nil
	}
	return append([]ChatSessionPaletteItem(nil), p.sessionsPaletteItems...)
}

func (p *ChatPage) SetModelState(modelProvider, modelName, thinkingLevel, serviceTier, contextMode string) {
	if value := strings.TrimSpace(modelProvider); value != "" {
		p.modelProvider = value
	}
	if value := strings.TrimSpace(modelName); value != "" {
		p.modelName = value
	}
	if value := strings.TrimSpace(thinkingLevel); value != "" {
		p.thinkingLevel = value
	}
	p.serviceTier = strings.TrimSpace(serviceTier)
	p.contextMode = strings.TrimSpace(contextMode)
}

func (p *ChatPage) ModelState() (string, string, string, string, string) {
	if p == nil {
		return "", "", "", "", ""
	}
	return strings.TrimSpace(p.modelProvider), strings.TrimSpace(p.modelName), strings.TrimSpace(p.thinkingLevel), strings.TrimSpace(p.serviceTier), strings.TrimSpace(p.contextMode)
}

func (p *ChatPage) ContextWindow() int {
	if p == nil {
		return 0
	}
	return p.contextWindow
}

func (p *ChatPage) SetContextWindow(window int) {
	if p == nil || window <= 0 {
		return
	}
	p.contextWindow = window
}

func (p *ChatPage) SetThinkingTagsVisible(show bool) {
	p.showThinkingTags = show
	p.bumpTimelineRenderGeneration()
}

func (p *ChatPage) SetActivePlan(plan string) {
	p.meta.Plan = strings.TrimSpace(plan)
}

func (p *ChatPage) ShowToast(level ToastLevel, message string) {
	p.toast.show(level, message, toastDefaultDuration)
}

func (p *ChatPage) ShowToastForDuration(level ToastLevel, message string, duration time.Duration) {
	p.toast.show(level, message, duration)
}

func (p *ChatPage) RunInProgress() bool {
	if p == nil {
		return false
	}
	return p.busy
}

func (p *ChatPage) AppendSystemMessage(text string) {
	p.appendSystemMessage(text)
}

func (p *ChatPage) ToggleInlineBashOutputExpanded() bool {
	if p == nil {
		return false
	}
	return p.toggleInlineBashOutputExpanded()
}

func (p *ChatPage) StartManualCompact(note string) bool {
	if p == nil {
		return false
	}
	return p.startManualCompact(note)
}

func (p *ChatPage) ConsumeQuitScrollbackJump() bool {
	if p == nil {
		return false
	}
	if p.planEditorModalActive() || p.planUpdateModalActive() || p.planExitModalActive() || p.askUserModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive() || p.permissionModalActive() {
		return false
	}
	if p.timelineScroll <= 0 {
		return false
	}
	p.timelineScroll = 0
	return true
}

func (p *ChatPage) SetKeyBindings(keybinds *KeyBindings) {
	if keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
		return
	}
	p.keybinds = keybinds
}

func (p *ChatPage) SetSwarmName(name string) {
	if p == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Local"
	}
	p.swarmName = name
}
