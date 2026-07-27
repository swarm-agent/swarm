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

func (p *ChatPage) LiveAssistantText() string {
	if p == nil {
		return ""
	}
	return p.liveAssistant
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
		p.sessionsPaletteItems = prepareSessionManagerItems(normalizeChatSessionPaletteItems(p.sessionTabs))
		return
	}
	p.sessionTabs = normalizeChatSessionTabs(p.sessionTabs, currentID, title)
	p.sessionsPaletteItems = prepareSessionManagerItems(normalizeChatSessionPaletteItems(p.sessionTabs))
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

func (p *ChatPage) SetAgentTodoSummary(taskCount, openCount, inProgressCount int) {
	if p == nil {
		return
	}
	if taskCount < 0 {
		taskCount = 0
	}
	if openCount < 0 {
		openCount = 0
	}
	if inProgressCount < 0 {
		inProgressCount = 0
	}
	p.meta.AgentTodoTaskCount = taskCount
	p.meta.AgentTodoOpenCount = openCount
	p.meta.AgentTodoInProgress = inProgressCount
}

func (p *ChatPage) SetSessionTabs(tabs []ChatSessionTab) {
	if p == nil {
		return
	}
	normalized := normalizeChatSessionTabs(tabs, p.sessionID, p.sessionTitle)
	p.sessionTabs = normalized
	p.sessionsPaletteItems = prepareSessionManagerItems(normalizeChatSessionPaletteItems(normalized))
}

func (p *ChatPage) SetMessages(messages []ChatMessageRecord) {
	if p == nil {
		return
	}
	lifecycle := p.lifecycle
	restoreLiveAssistant := p.liveAssistant
	restoreLiveAssistantRunID := p.liveAssistantRunID
	restoreLiveAssistantStreamID := p.liveAssistantStreamID
	restoreLiveAssistantNextSeq := p.liveAssistantNextSeq
	restoreLiveAssistantOffset := p.liveAssistantOffset
	restoreLiveThinking := p.liveThinking
	restoreThinkingSummary := p.thinkingSummary
	restoreThinkingCompletedAt := p.thinkingCompletedAt
	restoreReasoningActive := p.reasoningActive
	restoreReasoningStartedAt := p.reasoningStartedAt
	restoreActiveReasoningMessageID := p.activeReasoningMessageID
	restoreToolStream := cloneChatToolStreamEntries(p.toolStream)
	restoreBashOutput := p.bashOutput
	restoreStreamedTools := cloneStringSet(p.streamedTools)
	restoreLiveState := lifecycle != nil && lifecycle.Active && !chatMessagesContainAssistantForRun(messages, lifecycle.RunID)
	messages = p.reconcilePendingLocalUserMessages(messages)
	p.timeline = nil
	p.toolStream = nil
	p.bashOutput = chatBashOutputState{}
	p.liveAssistant = ""
	p.resetLiveAssistantStream()
	p.liveThinking = ""
	p.thinkingCompletedAt = time.Time{}
	p.reasoningActive = false
	p.reasoningStartedAt = time.Time{}
	p.activeReasoningMessageID = ""
	p.streamedTools = make(map[string]struct{}, 16)
	p.resetTimelineRenderCache()
	p.applyHistory(messages)
	if restoreLiveState {
		p.lifecycle = lifecycle
		p.busy = true
		p.liveAssistant = restoreLiveAssistant
		p.liveAssistantRunID = restoreLiveAssistantRunID
		p.liveAssistantStreamID = restoreLiveAssistantStreamID
		p.liveAssistantNextSeq = restoreLiveAssistantNextSeq
		p.liveAssistantOffset = restoreLiveAssistantOffset
		p.liveThinking = restoreLiveThinking
		p.thinkingSummary = restoreThinkingSummary
		p.thinkingCompletedAt = restoreThinkingCompletedAt
		p.reasoningActive = restoreReasoningActive
		p.reasoningStartedAt = restoreReasoningStartedAt
		p.activeReasoningMessageID = restoreActiveReasoningMessageID
		p.toolStream = mergeChatToolStreamEntries(p.toolStream, restoreToolStream)
		p.bashOutput = restoreBashOutput
		p.streamedTools = mergeStringSets(p.streamedTools, restoreStreamedTools)
		p.rebuildToolLifecycleViews()
	}
}

// ApplyPermissionRecords merges authoritative V3 permission records into the
// chat page and immediately synchronizes the visible approval surface. This is
// used by the TUI hydration/realtime store in addition to ChatPage's direct
// permission backfill so a missed legacy event cannot hide a durable pending
// permission.
func (p *ChatPage) ApplyPermissionRecords(records []ChatPermissionRecord) {
	if p == nil || len(records) == 0 {
		return
	}
	p.permissions = mergePermissionHistory(p.permissions, records)
	p.rebuildToolLifecycleViews()
}

func (p *ChatPage) SetUsageSummary(summary *ChatUsageSummary) {
	if p == nil {
		return
	}
	p.applyContextUsageSummary(summary)
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
	return p.ordinaryPermissionComposerActive() || p.planPermissionModalActive() || p.manageSessionsPermissionModalActive() || p.planUpdateModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive()
}

func (p *ChatPage) OrdinaryPermissionComposerVisible() bool {
	return p.ordinaryPermissionComposerActive()
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

func (p *ChatPage) Meta() ChatSessionMeta {
	if p == nil {
		return ChatSessionMeta{}
	}
	return p.meta
}

func (p *ChatPage) SetMeta(meta ChatSessionMeta) {
	if p == nil {
		return
	}
	p.meta = meta
}

func (p *ChatPage) SessionMode() string {
	return normalizeSessionMode(p.sessionMode)
}

func (p *ChatPage) SessionID() string {
	return strings.TrimSpace(p.sessionID)
}

func (p *ChatPage) CurrentPlanModalVisible() bool {
	return p != nil && p.planEditorModalActive()
}

func (p *ChatPage) CloseCurrentPlanModal() {
	if p == nil {
		return
	}
	p.closePlanEditorModal()
}

func (p *ChatPage) OpenCurrentPlanModal(plan ChatSessionPlan) bool {
	return p.OpenCurrentPlanModalWithPlans(plan, nil, strings.TrimSpace(plan.ID))
}

func (p *ChatPage) OpenCurrentPlanModalWithPlans(plan ChatSessionPlan, plans []ChatSessionPlan, activePlanID string) bool {
	if p == nil {
		return false
	}
	if p.planPermissionModalActive() || p.manageSessionsPermissionModalActive() || p.planUpdateModalActive() || p.ordinaryPermissionComposerActive() || p.askUserModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive() || p.sessionsPaletteActive() {
		return false
	}
	if p.planExitModalActive() {
		p.closePlanExitModal()
	}
	p.openPlanEditorModalWithPlans(plan, plans, activePlanID)
	return true
}

func (p *ChatPage) SessionPaletteItems() []ChatSessionPaletteItem {
	if p == nil {
		return nil
	}
	return append([]ChatSessionPaletteItem(nil), p.sessionsPaletteItems...)
}

func (p *ChatPage) SetModelState(modelProvider, modelName, thinkingLevel, serviceTier, contextMode string) {
	if p == nil {
		return
	}
	p.modelProvider = strings.TrimSpace(modelProvider)
	p.modelName = strings.TrimSpace(modelName)
	p.thinkingLevel = strings.TrimSpace(thinkingLevel)
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

func (p *ChatPage) SetPlanExecutionState(plan ChatSessionPlan, revisions []ChatSessionPlan, runID, runStatus string) {
	if p == nil {
		return
	}
	p.planExecutionPlan = plan
	p.planExecutionRevisions = append([]ChatSessionPlan(nil), revisions...)
	p.planExecutionRunID = strings.TrimSpace(runID)
	p.planExecutionRunStatus = strings.TrimSpace(runStatus)
}

func (p *ChatPage) PlanExecutionState() (ChatSessionPlan, []ChatSessionPlan, string, string) {
	if p == nil {
		return ChatSessionPlan{}, nil, "", ""
	}
	return p.planExecutionPlan, append([]ChatSessionPlan(nil), p.planExecutionRevisions...), p.planExecutionRunID, p.planExecutionRunStatus
}

func (p *ChatPage) SetActivePlan(plan string) {
	p.meta.Plan = strings.TrimSpace(plan)
}

func (p *ChatPage) ShowToast(level ToastLevel, message string) {
	p.toast.show(level, message, toastDefaultDuration)
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

func (p *ChatPage) ConsumeQuitScrollbackJump() bool {
	if p == nil {
		return false
	}
	if p.planPermissionModalActive() || p.manageSessionsPermissionModalActive() || p.planEditorModalActive() || p.planUpdateModalActive() || p.planExitModalActive() || p.askUserModalActive() || p.workspaceScopeModalActive() || p.taskLaunchModalActive() || p.themeChangeModalActive() || p.agentChangeModalActive() || p.skillChangeModalActive() || p.ordinaryPermissionComposerActive() {
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
