package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

var chatSpinnerFrames = []string{"|", "/", "-", "\\"}
var chatPulseDotFrames = []string{"·", "•", "◦", "•"}

const (
	chatDefaultToolRunningSymbol = "•"
	chatDefaultToolSuccessSymbol = "✓"
	chatDefaultToolErrorSymbol   = "✕"
	chatHugePromptWarnTokens     = 25_000
)

const (
	chatCursorRune                  rune = '█'
	chatCursorBlinkOn                    = 10
	chatMouseWheelStep                   = 3
	chatMaxInputRunes                    = 8000
	chatHistoryLimit                     = 800
	chatMaxTimelineMessages              = 2000
	chatMaxToolEntries                   = 600
	chatMaxLiveToolOutputRunes           = 96 * 1024
	chatMaxBashTimelinePreviewRunes      = 12 * 1024
	chatMaxBashOutputRunes               = 512 * 1024
	chatBashTimelineRefreshMS            = 80
	chatUserVariantCount                 = 10
	chatAssistantVariantCount            = 10
)

const (
	chatReasoningTimelineObjectMetadataKey    = "swarm_ui_reasoning_object"
	chatReasoningTimelineRunIDMetadataKey     = "swarm_ui_reasoning_run_id"
	chatReasoningTimelineSegmentMetadataKey   = "swarm_ui_reasoning_segment"
	chatReasoningTimelineStartedAtMetadataKey = "swarm_ui_reasoning_started_at"
	chatReasoningTimelineDurationMetadataKey  = "swarm_ui_reasoning_duration_ms"
	chatToolTimelineObjectMetadataKey         = "swarm_ui_tool_object"
	chatToolTimelineEntryKeyMetadataKey       = "swarm_ui_tool_entry_key"
	chatToolTimelineToolNameMetadataKey       = "swarm_ui_tool_name"
	chatToolTimelinePayloadMetadataKey        = "swarm_ui_tool_payload"
	chatToolTimelineCallIDMetadataKey         = "swarm_ui_tool_call_id"
	chatToolTimelineStartedAtMetadataKey      = "swarm_ui_tool_started_at"
	chatToolTimelineDurationMetadataKey       = "swarm_ui_tool_duration_ms"
)

type ChatSessionMeta struct {
	Workspace             string
	Path                  string
	Branch                string
	Dirty                 int
	Agent                 string
	AgentExecutionSetting string
	AgentExitPlanMode     bool
	AgentRuntimeKnown     bool
	Subagents             []string
	Plan                  string
	WorktreeEnabled       bool
	BypassPermissions     bool
}

type ChatSessionTab struct {
	ID              string
	Title           string
	WorkspaceName   string
	WorkspacePath   string
	Mode            string
	UpdatedAgo      string
	Provider        string
	ModelName       string
	ServiceTier     string
	ContextMode     string
	Background      bool
	ParentSessionID string
	LineageKind     string
	LineageLabel    string
	TargetKind      string
	TargetName      string
	Depth           int
}

type ChatSessionPaletteItem struct {
	ID              string
	Title           string
	WorkspaceName   string
	WorkspacePath   string
	Mode            string
	UpdatedAgo      string
	Provider        string
	ModelName       string
	ServiceTier     string
	ContextMode     string
	Background      bool
	ParentSessionID string
	LineageKind     string
	LineageLabel    string
	TargetKind      string
	TargetName      string
	Depth           int
}

type ChatPageOptions struct {
	Backend            ChatBackend
	SessionID          string
	SessionTitle       string
	InitialPrompt      string
	Presets            []string
	SessionTabs        []ChatSessionTab
	CommandSuggestions []CommandSuggestion
	ShowHeader         bool
	ShowThinkingTags   *bool
	Meta               ChatSessionMeta
	AuthConfigured     bool
	ModelProvider      string
	ModelName          string
	AvailableModels    []ModelsModalEntry
	ThinkingLevel      string
	ServiceTier        string
	ContextMode        string
	ContextWindow      int
	SessionMode        string
	ToolStreamStyle    ChatToolStreamStyle
	SwarmingTitle      string
	SwarmingStatus     string
	SwarmName          string
	KeyBindings        *KeyBindings
	OnAsyncEvent       func()
	RequestAsyncRender func()
	CopyText           func(string) error
}

type ChatToolStreamStyle struct {
	ShowAnchor    *bool
	PulseFrames   []string
	RunningSymbol string
	SuccessSymbol string
	ErrorSymbol   string
}

type chatMessageItem struct {
	MessageID string
	Role      string
	Text      string
	CreatedAt int64
	ToolState string
	Metadata  map[string]any
}

type chatToolStreamEntry struct {
	EntryKey           string
	ToolName           string
	CallID             string
	Output             string
	Error              string
	State              string
	Raw                string
	StartedArguments   string
	StartedArgsAreJSON bool
	CreatedAt          int64
	StartedAt          int64
	DurationMS         int64
}

type chatBashOutputState struct {
	Visible             bool
	Expanded            bool
	ToolName            string
	CallID              string
	Command             string
	Output              string
	UpdatedAt           int64
	Running             bool
	Truncated           bool
	Scroll              int
	LastStatus          string
	LastTimelineRefresh int64
	LastPreviewText     string
}

type chatRenderSpan struct {
	Text  string
	Style tcell.Style
}

type chatRenderLine struct {
	Text  string
	Style tcell.Style
	Spans []chatRenderSpan
}

type chatAskUserOption struct {
	Value       string
	Label       string
	Description string
	AllowCustom bool
}

type chatAskUserQuestion struct {
	ID       string
	Header   string
	Question string
	Options  []chatAskUserOption
	Required bool
}

type chatHistoryResult struct {
	Messages []ChatMessageRecord
	Err      error
}

type chatUsageResult struct {
	Summary *ChatUsageSummary
	Err     error
}

type chatRunResult struct {
	RunID    int
	Response ChatRunResponse
	Err      error
}

type chatRunStreamResult struct {
	RunID  int
	Event  ChatRunStreamEvent
	AtUnix int64
}

type chatRunStopResult struct {
	RunID int
	Err   error
}

type chatRunStreamEnqueueResult struct {
	queued bool
	drop   bool
}

type chatPermissionLoadResult struct {
	Mode       string
	Records    []ChatPermissionRecord
	ModeErr    error
	PendingErr error
}

type chatPermissionActionResult struct {
	Action       string
	Mode         string
	Announce     bool
	Permission   ChatPermissionRecord
	Permissions  []ChatPermissionRecord
	Pending      []ChatPermissionRecord
	ResolvedMany []ChatPermissionRecord
	Err          error
}

type ChatPage struct {
	theme    Theme
	keybinds *KeyBindings
	meta     ChatSessionMeta

	backend ChatBackend

	sessionID          string
	sessionTitle       string
	input              string
	inputCursor        int
	lastPrompt         string
	presets            []string
	sessionTabs        []ChatSessionTab
	commandSuggestions []CommandSuggestion
	mentionSubagents   []string
	footerTargets      []clickTarget
	authCommandLogged  map[string]bool

	selectedPreset      int
	frameTick           int
	commandPaletteIndex int
	mentionPaletteIndex int

	timeline       []chatMessageItem
	toolStream     []chatToolStreamEntry
	timelineScroll int

	timelineRenderGeneration uint64
	timelineRenderCache      []chatTimelineCacheEntry
	liveAssistantRenderCache chatLiveAssistantCacheEntry

	userVariant            int
	userVariantPrev        Rect
	userVariantTarget      Rect
	userVariantNext        Rect
	assistantVariant       int
	assistantVariantPrev   Rect
	assistantVariantTarget Rect
	assistantVariantNext   Rect
	toolAnchorEnabled      bool
	toolPulseFrames        []string
	toolRunningSymbol      string
	toolSuccessSymbol      string
	toolErrorSymbol        string

	busy       bool
	runID      int
	runPrompt  string
	runStarted time.Time
	lifecycle  *ChatSessionLifecycle
	ownedRunID string
	runCancel  context.CancelFunc
	runResults chan chatRunResult
	runStream  chan chatRunStreamResult
	runStops   chan chatRunStopResult
	runAbort   bool

	historyLoading bool
	historyLoaded  bool
	historyResults chan chatHistoryResult
	usageLoading   bool
	usageResults   chan chatUsageResult

	permissionsLoading         bool
	permissionBackfillInFlight bool
	permissionResults          chan chatPermissionLoadResult
	permissionActions          chan chatPermissionActionResult
	onAsyncEvent               func()
	requestAsyncRenderFn       func()
	copyTextFn                 func(string) error

	initialPrompt     string
	initialPromptSent bool

	showHeader               bool
	showThinkingTags         bool
	swarmingTitle            string
	swarmingStatus           string
	swarmName                string
	swarmNotificationCount   int
	modelProvider            string
	modelName                string
	thinkingLevel            string
	serviceTier              string
	contextMode              string
	thinkingSummary          string
	liveThinking             string
	thinkingCompletedAt      time.Time
	reasoningSegment         int
	reasoningActive          bool
	reasoningStartedAt       time.Time
	activeReasoningMessageID string
	liveAssistant            string
	streamingRun             bool
	streamedTools            map[string]struct{}
	authConfigured           bool
	contextUsageSet          bool
	contextWindow            int
	contextRemain            int64
	usageSummary             *ChatUsageSummary
	sessionMode              string
	pendingPerms             []ChatPermissionRecord
	permissions              []ChatPermissionRecord
	permSelected             int
	permInput                string
	pasteActive              bool
	pasteBuffer              []rune
	lastPasteBatchSize       int
	askUserOption            int
	permRows                 []Rect
	permIndexes              []int
	permApproveRect          Rect
	permDenyRect             Rect
	alwaysAllowRect          Rect
	alwaysDenyRect           Rect
	permDetailScroll         int
	permDetailMaxScroll      int
	permDetailTargetID       string

	taskLaunchPermission  string
	taskLaunchScroll      int
	taskLaunchApproveRect Rect
	taskLaunchDenyRect    Rect

	agentChangePermission          string
	agentChangeScroll              int
	agentChangeApproveRect         Rect
	agentChangeDenyRect            Rect
	agentChangeModelOptions        []agentChangeModelOption
	agentChangeModelPickerVisible  bool
	agentChangeModelPickerSelected int
	agentChangeModelPickerProvider string
	agentChangeOverrideProvider    string
	agentChangeOverrideModel       string
	agentChangeOverrideThinking    string

	skillChangePermission  string
	skillChangeScroll      int
	skillChangeApproveRect Rect
	skillChangeDenyRect    Rect

	planUpdatePermission  string
	planUpdateTitle       string
	planUpdatePlanID      string
	planUpdatePriorTitle  string
	planUpdatePriorPlan   string
	planUpdatePlan        string
	planUpdateDiffLines   []string
	planUpdateScroll      int
	planUpdateSelection   int
	planUpdateInput       string
	planUpdateCancelRect  Rect
	planUpdateConfirmRect Rect

	planExitVisible     bool
	planExitTitle       string
	planExitBody        string
	planExitPermission  string
	planExitPlanID      string
	planExitScroll      int
	planExitSelection   int
	planExitInput       string
	planExitCancelRect  Rect
	planExitConfirmRect Rect

	planEditorVisible     bool
	planEditorPlan        ChatSessionPlan
	planEditorInput       string
	planEditorEditing     bool
	planEditorConfirmSave bool
	planEditorSelection   int
	planEditorScroll      int
	planEditorInputScroll int
	planEditorCancelRect  Rect
	planEditorCopyRect    Rect
	planEditorSaveRect    Rect

	manageTodosPermission  string
	manageTodosScroll      int
	manageTodosInput       string
	manageTodosCancelRect  Rect
	manageTodosConfirmRect Rect

	askUserVisible    bool
	askUserPermission string
	askUserTitle      string
	askUserContext    string
	askUserQuestions  []chatAskUserQuestion
	askUserCurrent    int
	askUserAnswers    map[string]string
	askUserSelections map[string]int
	askUserScroll     int
	askUserInputMode  bool
	askUserInput      string

	workspaceScopeVisible        bool
	workspaceScopePermission     string
	workspaceScopeTitle          string
	workspaceScopeSummary        string
	workspaceScopeToolName       string
	workspaceScopeAccessLabel    string
	workspaceScopeRequestedPath  string
	workspaceScopeResolvedPath   string
	workspaceScopeDirectory      string
	workspaceScopeWorkspacePath  string
	workspaceScopeWorkspaceName  string
	workspaceScopeWorkspaceSaved bool
	workspaceScopeSelection      int
	workspaceScopeScroll         int
	workspaceScopeAllowRect      Rect
	workspaceScopeAddDirRect     Rect
	workspaceScopeDenyRect       Rect

	themeChangePermission  string
	themeChangeScroll      int
	themeChangeApproveRect Rect
	themeChangeDenyRect    Rect

	sessionsPaletteVisible   bool
	sessionsPaletteQuery     string
	sessionsPaletteSelection int
	sessionsPaletteScroll    int
	sessionsPaletteItems     []ChatSessionPaletteItem
	bashOutput               chatBashOutputState
	pendingChatAction        *ChatAction
	statusLine               string
	errorLine                string
	toast                    toastState
	voiceInput               VoiceInputState
}

func NewChatPage(opts ChatPageOptions) *ChatPage {
	meta := opts.Meta
	if strings.TrimSpace(meta.Workspace) == "" {
		meta.Workspace = "workspace"
	}
	if strings.TrimSpace(meta.Path) == "" {
		meta.Path = "."
	}
	if strings.TrimSpace(meta.Branch) == "" {
		meta.Branch = "-"
	}
	if strings.TrimSpace(meta.Agent) == "" {
		meta.Agent = "swarm"
	}

	title := strings.TrimSpace(opts.SessionTitle)
	if title == "" {
		title = chatSessionTitleFromPrompt(opts.InitialPrompt)
	}

	toolStyle := normalizeChatToolStreamStyle(opts.ToolStreamStyle)

	p := &ChatPage{
		theme:                   NordTheme(),
		keybinds:                opts.KeyBindings,
		meta:                    meta,
		backend:                 opts.Backend,
		sessionID:               strings.TrimSpace(opts.SessionID),
		sessionTitle:            title,
		presets:                 append([]string(nil), opts.Presets...),
		sessionTabs:             normalizeChatSessionTabs(opts.SessionTabs, strings.TrimSpace(opts.SessionID), title),
		sessionsPaletteItems:    normalizeChatSessionPaletteItems(opts.SessionTabs),
		runResults:              make(chan chatRunResult, 4),
		runStream:               make(chan chatRunStreamResult, 2048),
		runStops:                make(chan chatRunStopResult, 4),
		historyResults:          make(chan chatHistoryResult, 1),
		usageResults:            make(chan chatUsageResult, 1),
		permissionResults:       make(chan chatPermissionLoadResult, 1),
		permissionActions:       make(chan chatPermissionActionResult, 16),
		initialPrompt:           strings.TrimSpace(opts.InitialPrompt),
		showHeader:              opts.ShowHeader,
		showThinkingTags:        true,
		swarmingTitle:           strings.TrimSpace(opts.SwarmingTitle),
		swarmingStatus:          strings.TrimSpace(opts.SwarmingStatus),
		swarmName:               strings.TrimSpace(opts.SwarmName),
		modelProvider:           strings.TrimSpace(opts.ModelProvider),
		modelName:               strings.TrimSpace(opts.ModelName),
		agentChangeModelOptions: normalizeAgentChangeModelOptions(opts.AvailableModels),
		thinkingLevel:           strings.TrimSpace(opts.ThinkingLevel),
		serviceTier:             strings.TrimSpace(opts.ServiceTier),
		contextMode:             strings.TrimSpace(opts.ContextMode),
		authConfigured:          opts.AuthConfigured,
		streamedTools:           make(map[string]struct{}, 16),
		sessionMode:             normalizeSessionMode(opts.SessionMode),
		pendingPerms:            make([]ChatPermissionRecord, 0, 16),
		permissions:             make([]ChatPermissionRecord, 0, 32),
		authCommandLogged:       make(map[string]bool),
		permRows:                make([]Rect, 0, 16),
		permIndexes:             make([]int, 0, 16),
		statusLine:              "ready",
		userVariant:             0,
		assistantVariant:        0,
		toolAnchorEnabled:       toolStyle.ShowAnchor,
		toolPulseFrames:         toolStyle.PulseFrames,
		toolRunningSymbol:       toolStyle.RunningSymbol,
		toolSuccessSymbol:       toolStyle.SuccessSymbol,
		toolErrorSymbol:         toolStyle.ErrorSymbol,
		onAsyncEvent:            opts.OnAsyncEvent,
		requestAsyncRenderFn:    opts.RequestAsyncRender,
		copyTextFn:              opts.CopyText,
	}
	p.inputCursor = utf8.RuneCountInString(p.input)
	if p.sessionMode == "plan" {
		p.statusLine = "plan mode active - draft plan then /plan exit"
	} else {
		p.statusLine = "mode: " + p.sessionMode
	}
	if opts.ShowThinkingTags != nil {
		p.showThinkingTags = *opts.ShowThinkingTags
	}
	if p.swarmingTitle == "" {
		p.swarmingTitle = "Swarming"
	}
	if p.swarmingStatus == "" {
		p.swarmingStatus = "swarming"
	}
	if p.swarmName == "" {
		p.swarmName = "Local"
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	p.setCommandSuggestions(opts.CommandSuggestions)
	p.setMentionSubagents(meta.Subagents)

	if !p.authConfigured {
		p.statusLine = "auth missing - run /auth"
		p.appendSystemMessage("Auth is missing. Run /auth before starting a turn.")
	}

	if p.backend == nil || p.sessionID == "" {
		p.historyLoaded = true
		if p.backend == nil {
			p.appendSystemMessage("Chat backend is unavailable.")
		}
		if p.sessionID == "" {
			p.appendSystemMessage("Session id is missing.")
		}
		return p
	}

	p.historyLoading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		messages, err := p.backend.LoadMessages(ctx, p.sessionID, 0, chatHistoryLimit)
		result := chatHistoryResult{Messages: messages, Err: err}
		select {
		case p.historyResults <- result:
		default:
		}
		p.notifyAsyncEvent()
	}()

	p.usageLoading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		summary, err := p.backend.GetSessionUsageSummary(ctx, p.sessionID)
		result := chatUsageResult{Summary: summary, Err: err}
		select {
		case p.usageResults <- result:
		default:
		}
		p.notifyAsyncEvent()
	}()

	p.permissionsLoading = true
	go func() {
		mode := ""
		var (
			modeErr    error
			pendingErr error
			records    []ChatPermissionRecord
		)

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		mode, modeErr = p.backend.GetSessionMode(ctx, p.sessionID)
		records, pendingErr = p.backend.ListPermissions(ctx, p.sessionID, 200)

		result := chatPermissionLoadResult{
			Mode:       mode,
			Records:    records,
			ModeErr:    modeErr,
			PendingErr: pendingErr,
		}
		select {
		case p.permissionResults <- result:
		default:
		}
		p.notifyAsyncEvent()
	}()

	return p
}

func (p *ChatPage) notifyAsyncEvent() {
	if p == nil || p.onAsyncEvent == nil {
		return
	}
	p.onAsyncEvent()
}

func (p *ChatPage) requestAsyncRender() {
	if p == nil || p.requestAsyncRenderFn == nil {
		p.notifyAsyncEvent()
		return
	}
	p.requestAsyncRenderFn()
}

func (p *ChatPage) enqueueRunStreamEvent(result chatRunStreamResult) chatRunStreamEnqueueResult {
	if p == nil {
		return chatRunStreamEnqueueResult{}
	}
	select {
	case p.runStream <- result:
		return chatRunStreamEnqueueResult{queued: true}
	default:
	}

	eventType := strings.ToLower(strings.TrimSpace(result.Event.Type))
	if eventType == "tool.delta" || eventType == "reasoning.delta" {
		return chatRunStreamEnqueueResult{drop: true}
	}

	for {
		select {
		case p.runStream <- result:
			return chatRunStreamEnqueueResult{queued: true}
		case <-time.After(10 * time.Millisecond):
			if eventType == "turn.completed" || eventType == "turn.error" {
				continue
			}
		}
	}
}

func (p *ChatPage) HandleTick() bool {
	changed := false
	if p.liveRunVisible() {
		p.frameTick++
		changed = true
	}
	if p.handleAsyncState() {
		changed = true
	}
	return changed
}

func (p *ChatPage) HandleAsync() bool {
	return p.handleAsyncState()
}

func (p *ChatPage) handleAsyncState() bool {
	changed := false
	if p.toast.tick(time.Now()) {
		changed = true
	}
	if p.drainHistoryResults() {
		changed = true
	}
	if p.drainUsageResults() {
		changed = true
	}
	if p.drainRunStream() {
		changed = true
	}
	if p.drainRunResults() {
		changed = true
	}
	if p.drainRunStops() {
		changed = true
	}
	if p.drainPermissionLoads() {
		changed = true
	}
	if p.drainPermissionActions() {
		changed = true
	}

	if p.historyLoaded && !p.initialPromptSent {
		p.initialPromptSent = true
		if prompt := strings.TrimSpace(p.initialPrompt); prompt != "" {
			p.startRun(prompt)
			changed = true
		}
	}
	return changed
}

func (p *ChatPage) HandleMouse(ev *tcell.EventMouse) {
	if ev == nil {
		return
	}
	if p.handlePlanEditorModalMouse(ev) {
		return
	}
	if p.handlePlanUpdateModalMouse(ev) {
		return
	}
	if p.handlePlanExitModalMouse(ev) {
		return
	}
	if p.handleManageTodosModalMouse(ev) {
		return
	}
	if p.handleAskUserModalMouse(ev) {
		return
	}
	if p.handleWorkspaceScopeModalMouse(ev) {
		return
	}
	if p.handleTaskLaunchModalMouse(ev) {
		return
	}
	if p.handleThemeChangeModalMouse(ev) {
		return
	}
	if p.handleAgentChangeModalMouse(ev) {
		return
	}
	if p.handleSkillChangeModalMouse(ev) {
		return
	}
	if p.handleSessionsPaletteMouse(ev) {
		return
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		for _, target := range p.footerTargets {
			if target.Rect.Contains(x, y) {
				p.queueFooterAction(target.Action)
				return
			}
		}
	}
	if buttons&tcell.Button1 != 0 && p.userVariantPrev.Contains(x, y) {
		p.cycleUserVariant(-1)
		return
	}
	if buttons&tcell.Button1 != 0 && p.userVariantNext.Contains(x, y) {
		p.cycleUserVariant(1)
		return
	}
	if buttons&tcell.Button1 != 0 && p.userVariantTarget.Contains(x, y) {
		p.cycleUserVariant(1)
		return
	}
	if buttons&tcell.Button1 != 0 && p.assistantVariantPrev.Contains(x, y) {
		p.cycleAssistantVariant(-1)
		return
	}
	if buttons&tcell.Button1 != 0 && p.assistantVariantNext.Contains(x, y) {
		p.cycleAssistantVariant(1)
		return
	}
	if buttons&tcell.Button1 != 0 && p.assistantVariantTarget.Contains(x, y) {
		p.cycleAssistantVariant(1)
		return
	}
	if (buttons&tcell.Button2 != 0 || buttons&tcell.Button3 != 0) && (p.userVariantTarget.Contains(x, y) || p.userVariantPrev.Contains(x, y) || p.userVariantNext.Contains(x, y)) {
		p.cycleUserVariant(-1)
		return
	}
	if (buttons&tcell.Button2 != 0 || buttons&tcell.Button3 != 0) && (p.assistantVariantTarget.Contains(x, y) || p.assistantVariantPrev.Contains(x, y) || p.assistantVariantNext.Contains(x, y)) {
		p.cycleAssistantVariant(-1)
		return
	}
	if p.permissionModalActive() {
		if buttons&tcell.Button1 != 0 {
			p.ensurePermissionSelection()
			reason := p.permissionReason()
			switch {
			case p.permApproveRect.Contains(x, y):
				p.queueResolveSelected("allow_once", reason)
				return
			case p.permDenyRect.Contains(x, y):
				p.queueResolveSelected("deny_once", reason)
				return
			case p.alwaysAllowRect.Contains(x, y):
				p.queueResolveSelected("allow_always", reason)
				return
			case p.alwaysDenyRect.Contains(x, y):
				p.queueResolveSelected("deny_always", reason)
				return
			default:
				for i, row := range p.permRows {
					if !row.Contains(x, y) {
						continue
					}
					if i >= 0 && i < len(p.permIndexes) {
						p.permSelected = p.permIndexes[i]
						p.syncPermissionDetailTarget()
						p.statusLine = "selected pending permission"
					}
					return
				}
			}
		}
		switch {
		case buttons&tcell.WheelUp != 0:
			p.shiftPermissionDetailScroll(-1)
			return
		case buttons&tcell.WheelDown != 0:
			p.shiftPermissionDetailScroll(1)
			return
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.scrollTimeline(chatMouseWheelStep)
	case buttons&tcell.WheelDown != 0:
		p.scrollTimeline(-chatMouseWheelStep)
	}
}

func (p *ChatPage) HandleKey(ev *tcell.EventKey) {
	if ev == nil {
		return
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	if p.handlePlanEditorModalKey(ev) {
		return
	}
	if p.handlePlanUpdateModalKey(ev) {
		return
	}
	if p.handlePlanExitModalKey(ev) {
		return
	}
	if p.handleManageTodosModalKey(ev) {
		return
	}
	if p.handleAskUserModalKey(ev) {
		return
	}
	if p.handleWorkspaceScopeModalKey(ev) {
		return
	}
	if p.handleTaskLaunchModalKey(ev) {
		return
	}
	if p.handleThemeChangeModalKey(ev) {
		return
	}
	if p.handleAgentChangeModalKey(ev) {
		return
	}
	if p.handleSkillChangeModalKey(ev) {
		return
	}
	if p.handleSessionsPaletteKey(ev) {
		return
	}
	if p.handlePermissionModalKey(ev) {
		return
	}
	if p.pasteActive {
		if p.HandlePasteKey(ev) {
			p.syncComposerPalettes()
		}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindChatMoveUp):
		if p.commandPaletteActive() {
			p.moveCommandPaletteSelection(-1)
			return
		}
		if p.mentionPaletteActive() {
			p.moveMentionPaletteSelection(-1)
			return
		}
		p.scrollTimeline(1)
		return
	case p.keybinds.Match(ev, KeybindChatMoveDown):
		if p.commandPaletteActive() {
			p.moveCommandPaletteSelection(1)
			return
		}
		if p.mentionPaletteActive() {
			p.moveMentionPaletteSelection(1)
			return
		}
		p.scrollTimeline(-1)
		return
	case p.keybinds.Match(ev, KeybindChatPageUp):
		if p.bashOutputVisible() && p.bashOutput.Expanded {
			p.scrollBashOutput(6)
			return
		}
		p.scrollTimeline(6)
		return
	case p.keybinds.Match(ev, KeybindChatPageDown):
		if p.bashOutputVisible() && p.bashOutput.Expanded {
			p.scrollBashOutput(-6)
			return
		}
		p.scrollTimeline(-6)
		return
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.timelineScroll = 1 << 30
		return
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.timelineScroll = 0
		return
	case p.keybinds.Match(ev, KeybindChatBackspace):
		if len(p.pasteBuffer) > 0 {
			p.pasteBuffer = p.pasteBuffer[:len(p.pasteBuffer)-1]
			p.lastPasteBatchSize = len(p.pasteBuffer)
			p.syncComposerPalettes()
			return
		}
		before := p.input
		var changed bool
		p.input, p.inputCursor, changed = backspaceMultilineAtCursor(p.input, p.inputCursor)
		if changed {
			p.lastPasteBatchSize = 0
			p.maybeWarnLargeInput(before, p.input)
		}
		p.syncComposerPalettes()
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		if p.commandPaletteActive() || p.mentionPaletteActive() {
			return
		}
		p.inputCursor = moveRuneCursorLeft(p.input, p.inputCursor)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		if p.commandPaletteActive() || p.mentionPaletteActive() {
			return
		}
		p.inputCursor = moveRuneCursorRight(p.input, p.inputCursor)
		return
	case p.keybinds.Match(ev, KeybindChatClear):
		p.input = ""
		p.inputCursor = 0
		p.pasteBuffer = p.pasteBuffer[:0]
		p.lastPasteBatchSize = 0
		p.syncComposerPalettes()
		return
	case p.keybinds.Match(ev, KeybindChatUserVariantPrev):
		p.cycleUserVariant(-1)
		return
	case p.keybinds.Match(ev, KeybindChatUserVariantNext):
		p.cycleUserVariant(1)
		return
	case p.keybinds.Match(ev, KeybindChatAssistantVariantPrev):
		p.cycleAssistantVariant(-1)
		return
	case p.keybinds.Match(ev, KeybindChatAssistantVariantNext):
		p.cycleAssistantVariant(1)
		return
	case p.keybinds.Match(ev, KeybindChatCycleMode):
		p.queueCycleMode()
		return
	case p.keybinds.Match(ev, KeybindChatComplete):
		if p.commandPaletteActive() && p.completeCommandFromPalette() {
			return
		}
		if p.mentionPaletteActive() && p.completeMentionFromPalette() {
			return
		}
		if len(p.presets) > 0 {
			p.selectedPreset = (p.selectedPreset + 1) % len(p.presets)
		}
		return
	case p.keybinds.Match(ev, KeybindChatInsertNewline):
		before := p.input
		inserted := 0
		p.input, p.inputCursor, inserted = insertMultilineAtCursor(p.input, p.inputCursor, "\n", chatMaxInputRunes)
		if inserted > 0 {
			p.pasteBuffer = p.pasteBuffer[:0]
			p.lastPasteBatchSize = 0
		}
		p.maybeWarnLargeInput(before, p.input)
		p.syncComposerPalettes()
		return
	case p.keybinds.Match(ev, KeybindChatSubmit):
		if p.acceptCommandPaletteEnter() {
			return
		}
		if p.acceptMentionPaletteEnter() {
			return
		}
		p.submitInput()
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		switch {
		case p.keybinds.Match(ev, KeybindChatMoveUpAlt):
			if strings.TrimSpace(p.input) == "" {
				p.scrollTimeline(1)
				return
			}
		case p.keybinds.Match(ev, KeybindChatMoveDownAlt):
			if strings.TrimSpace(p.input) == "" {
				p.scrollTimeline(-1)
				return
			}
		}
		if unicode.IsPrint(r) {
			before := p.input
			inserted := 0
			p.input, p.inputCursor, inserted = insertMultilineAtCursor(p.input, p.inputCursor, string(r), chatMaxInputRunes)
			if inserted > 0 {
				p.pasteBuffer = p.pasteBuffer[:0]
				p.lastPasteBatchSize = 0
			}
			p.maybeWarnLargeInput(before, p.input)
			p.syncComposerPalettes()
		}
	}
}

func (p *ChatPage) HandleEscape() bool {
	if p.bashOutputVisible() && p.bashOutput.Expanded {
		p.bashOutput.Visible = false
		p.bashOutput.Expanded = false
		p.bashOutput.Scroll = 0
		p.statusLine = "bash output closed"
		return true
	}
	if p.planEditorModalActive() {
		p.resolvePlanEditorModal(chatPlanEditorActionCancel)
		return true
	}
	if p.planUpdateModalActive() {
		p.resolvePlanUpdateModal(false)
		return true
	}
	if p.planExitModalActive() {
		p.resolvePlanExitModal(false)
		return true
	}
	if p.askUserModalActive() {
		p.resolveAskUserModal(false)
		return true
	}
	if p.workspaceScopeModalActive() {
		p.resolveWorkspaceScopeModal(false)
		return true
	}
	if p.taskLaunchModalActive() {
		p.resolveTaskLaunchModal(false)
		return true
	}
	if p.themeChangeModalActive() {
		p.resolveThemeChangeModal(false)
		return true
	}
	if p.agentChangeModalActive() {
		if p.agentChangeModelPickerVisible {
			p.agentChangeModelPickerVisible = false
			p.statusLine = "agent model picker closed"
			return true
		}
		p.resolveAgentChangeModal(false)
		return true
	}
	if p.sessionsPaletteActive() {
		p.closeSessionsPalette()
		p.statusLine = "sessions palette closed"
		return true
	}
	if p.permissionModalActive() {
		return false
	}
	if p.effectiveRunActive() {
		p.abortAgenticLoop()
		return true
	}
	return false
}

func (p *ChatPage) abortAgenticLoop() {
	if !p.effectiveRunActive() {
		return
	}
	p.runAbort = true
	if p.backend != nil && p.lifecycle != nil && p.lifecycle.Active && strings.TrimSpace(p.lifecycle.RunID) != "" && strings.TrimSpace(p.sessionID) != "" {
		p.statusLine = "stopping run"
		runID := p.runID
		sessionID := strings.TrimSpace(p.sessionID)
		lifecycleRunID := strings.TrimSpace(p.lifecycle.RunID)
		go func() {
			err := p.backend.StopRun(context.Background(), sessionID, lifecycleRunID)
			select {
			case p.runStops <- chatRunStopResult{RunID: runID, Err: err}:
				p.notifyAsyncEvent()
			default:
			}
		}()
		return
	}
	if p.runCancel != nil {
		p.runCancel()
	}
}

func (p *ChatPage) handlePermissionModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.permissionModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	p.ensurePermissionSelection()

	downPressed := p.keybinds.Match(ev, KeybindPermissionMoveDown)
	downAltPressed := p.keybinds.Match(ev, KeybindPermissionMoveDownAlt)

	switch {
	case p.keybinds.Match(ev, KeybindPermissionCycleMode):
		p.queueCycleMode()
		return true
	case p.keybinds.Match(ev, KeybindPermissionMoveUp):
		p.shiftPermissionSelection(-1)
		return true
	case downPressed || downAltPressed:
		p.shiftPermissionSelection(1)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.shiftPermissionDetailScroll(-6)
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.shiftPermissionDetailScroll(6)
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.permDetailScroll = 0
		p.clampPermissionDetailScroll()
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.permDetailScroll = p.permDetailMaxScroll
		p.clampPermissionDetailScroll()
		return true
	case p.keybinds.Match(ev, KeybindPermissionBackspace):
		if len(p.permInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.permInput)
			if sz > 0 {
				p.permInput = p.permInput[:len(p.permInput)-sz]
			}
		}
		return true
	case p.keybinds.Match(ev, KeybindPermissionClear):
		p.permInput = ""
		return true
	case p.keybinds.Match(ev, KeybindPermissionAlwaysAllow):
		p.queueResolveSelected("allow_always", p.permissionReason())
		return true
	case p.keybinds.Match(ev, KeybindPermissionAlwaysDeny):
		p.queueResolveSelected("deny_always", p.permissionReason())
		return true
	case p.keybinds.Match(ev, KeybindPermissionDeny):
		p.queueResolveSelected("deny_once", p.permissionReason())
		return true
	case p.keybinds.Match(ev, KeybindPermissionApprove):
		p.queueResolveSelected("allow_once", p.permissionReason())
		return true
	case ev.Key() == tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.permInput) < chatMaxInputRunes {
			p.permInput += string(r)
		}
		return true
	default:
		return false
	}
}

func (p *ChatPage) shiftPermissionSelection(delta int) {
	indexes := p.genericPermissionIndexes()
	if delta == 0 || len(indexes) == 0 {
		return
	}
	p.ensurePermissionSelection()
	pos := indexOfInt(indexes, p.permSelected)
	if pos < 0 {
		pos = 0
	}
	pos += delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(indexes) {
		pos = len(indexes) - 1
	}
	p.permSelected = indexes[pos]
	p.syncAskUserOption()
	p.syncPermissionDetailTarget()
	p.statusLine = "selected pending permission"
}

func (p *ChatPage) shiftAskUserOption(delta int) {
	if delta == 0 || len(p.pendingPerms) == 0 {
		return
	}
	p.ensurePermissionSelection()
	selected := p.pendingPerms[p.permSelected]
	options := askUserOptionsFromPermission(selected)
	if len(options) == 0 {
		return
	}
	p.askUserOption += delta
	if p.askUserOption < 0 {
		p.askUserOption = 0
	}
	if p.askUserOption >= len(options) {
		p.askUserOption = len(options) - 1
	}
	p.statusLine = fmt.Sprintf("ask-user option %d/%d", p.askUserOption+1, len(options))
}

func (p *ChatPage) selectedAskUserOptionText(record ChatPermissionRecord) string {
	options := askUserOptionsFromPermission(record)
	if len(options) == 0 {
		return ""
	}
	p.syncAskUserOption()
	if p.askUserOption < 0 || p.askUserOption >= len(options) {
		return ""
	}
	return strings.TrimSpace(options[p.askUserOption])
}

func (p *ChatPage) syncAskUserOption() {
	if len(p.pendingPerms) == 0 {
		p.askUserOption = 0
		return
	}
	selected := p.pendingPerms[p.permSelected]
	options := askUserOptionsFromPermission(selected)
	if len(options) == 0 {
		p.askUserOption = 0
		return
	}
	if p.askUserOption < 0 {
		p.askUserOption = 0
	}
	if p.askUserOption >= len(options) {
		p.askUserOption = len(options) - 1
	}
}

func (p *ChatPage) syncPermissionDetailTarget() {
	if len(p.pendingPerms) == 0 || p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		p.permDetailTargetID = ""
		p.permDetailScroll = 0
		p.permDetailMaxScroll = 0
		return
	}
	target := strings.TrimSpace(p.pendingPerms[p.permSelected].ID)
	if target == p.permDetailTargetID {
		p.clampPermissionDetailScroll()
		return
	}
	p.permDetailTargetID = target
	p.permDetailScroll = 0
	p.permDetailMaxScroll = 0
}

func (p *ChatPage) clampPermissionDetailScroll() {
	if p.permDetailMaxScroll < 0 {
		p.permDetailMaxScroll = 0
	}
	if p.permDetailScroll < 0 {
		p.permDetailScroll = 0
		return
	}
	if p.permDetailScroll > p.permDetailMaxScroll {
		p.permDetailScroll = p.permDetailMaxScroll
	}
}

func (p *ChatPage) shiftPermissionDetailScroll(delta int) {
	if delta == 0 {
		return
	}
	p.permDetailScroll += delta
	p.clampPermissionDetailScroll()
}

func (p *ChatPage) permissionDetailScrollSummary() (int, int) {
	if p.permDetailMaxScroll <= 0 {
		return 1, 1
	}
	current := p.permDetailScroll + 1
	total := p.permDetailMaxScroll + 1
	if current < 1 {
		current = 1
	}
	if current > total {
		current = total
	}
	return current, total
}

func (p *ChatPage) permissionReason() string {
	return strings.TrimSpace(p.permInput)
}

func (p *ChatPage) permissionModalActive() bool {
	return !p.planUpdateModalActive() && !p.planExitModalActive() && !p.askUserModalActive() && !p.workspaceScopeModalActive() && !p.taskLaunchModalActive() && !p.agentChangeModalActive() && !p.skillChangeModalActive() && p.genericPermissionCount() > 0
}

func (p *ChatPage) Draw(s tcell.Screen) {
	w, h := s.Size()
	FillRect(s, Rect{X: 0, Y: 0, W: w, H: h}, p.theme.Background)
	s.HideCursor()

	if w <= 0 || h <= 0 {
		return
	}
	if w < 18 || h < 5 {
		DrawText(s, 0, 0, w, p.theme.Warning, clampEllipsis("swarm chat", w))
		return
	}

	contentX := 0
	mainW := w

	renderHeader := p.showHeader && w >= 56 && h >= 10
	headerH := 0
	if renderHeader {
		headerH = 1
	}

	footerH := 2
	if w < 54 || h < 12 {
		footerH = 1
	}
	if footerH > h-1 {
		footerH = maxInt(0, h-1)
	}

	availableMainH := h - headerH - footerH
	if availableMainH < 2 && renderHeader {
		renderHeader = false
		headerH = 0
		availableMainH = h - footerH
	}
	if availableMainH < 2 && footerH > 1 {
		footerH = 1
		availableMainH = h - headerH - footerH
	}
	if availableMainH < 1 {
		availableMainH = 1
	}

	permissionModal := p.permissionModalActive()
	runIndicatorH := 0
	if !permissionModal && availableMainH >= 6 && p.liveRunVisible() {
		runIndicatorH = 1
	}
	bashOutputH := 0
	if !permissionModal {
		bashOutputH = p.inlineBashOutputHeight(mainW, availableMainH-runIndicatorH)
	}
	permH := 0
	if !permissionModal && w >= 44 && availableMainH >= 8 {
		permH = p.permissionPanelHeight(mainW)
	}

	composerH := p.desiredComposerHeight(mainW)
	if permissionModal {
		composerH = p.permissionComposerHeight(mainW, availableMainH-runIndicatorH-permH)
	}
	if composerH < 1 {
		composerH = 1
	}

	timelineMinH := 1
	if w >= 48 && h >= 10 {
		timelineMinH = 2
	}
	for {
		timelineH := availableMainH - composerH - runIndicatorH - permH - bashOutputH
		if timelineH >= timelineMinH {
			break
		}
		if permH > 0 {
			permH--
			continue
		}
		if bashOutputH > 0 {
			bashOutputH--
			continue
		}
		if runIndicatorH > 0 {
			runIndicatorH = 0
			continue
		}
		if composerH > 1 {
			composerH--
			continue
		}
		if timelineMinH > 1 {
			timelineMinH = 1
			continue
		}
		break
	}

	headerRect := Rect{X: contentX, Y: 0, W: mainW, H: headerH}
	footerRect := Rect{X: contentX, Y: h - footerH, W: mainW, H: footerH}
	inputRect := Rect{X: contentX, Y: h - footerH - composerH, W: mainW, H: composerH}
	runIndicatorRect := Rect{X: contentX, Y: inputRect.Y - runIndicatorH, W: mainW, H: runIndicatorH}
	baseY := runIndicatorRect.Y
	if runIndicatorH <= 0 {
		baseY = inputRect.Y
	}
	permRect := Rect{X: contentX, Y: baseY - permH, W: mainW, H: permH}
	bashOutputRect := Rect{X: contentX, Y: permRect.Y - bashOutputH, W: mainW, H: bashOutputH}
	timelineRect := Rect{X: contentX, Y: headerH, W: mainW, H: bashOutputRect.Y - headerH}
	if bashOutputH <= 0 {
		bashOutputRect = Rect{}
		timelineRect = Rect{X: contentX, Y: headerH, W: mainW, H: permRect.Y - headerH}
	}
	if permH <= 0 {
		permRect = Rect{}
		anchorY := baseY
		if bashOutputH > 0 {
			anchorY = bashOutputRect.Y
		}
		timelineRect = Rect{X: contentX, Y: headerH, W: mainW, H: anchorY - headerH}
	}
	if timelineRect.H < 1 {
		timelineRect.H = 1
	}

	if renderHeader {
		p.drawHeader(s, headerRect)
	} else {
		p.userVariantPrev = Rect{}
		p.userVariantTarget = Rect{}
		p.userVariantNext = Rect{}
		p.assistantVariantPrev = Rect{}
		p.assistantVariantTarget = Rect{}
		p.assistantVariantNext = Rect{}
	}
	timelineTextRect := timelineRect
	timelineInset := 0
	switch {
	case timelineTextRect.W >= 120:
		timelineInset = 2
	case timelineTextRect.W >= 84:
		timelineInset = 2
	case timelineTextRect.W >= 52:
		timelineInset = 1
	case timelineTextRect.W >= 40:
		timelineInset = 1
	}
	if timelineInset > 0 && timelineTextRect.W > timelineInset*2 {
		timelineTextRect.X += timelineInset
		timelineTextRect.W -= timelineInset * 2
	}
	p.drawTimelineComponent(s, timelineTextRect)
	p.drawBashOutputComponent(s, bashOutputRect)
	p.drawPermissionPanel(s, permRect)
	if permissionModal {
		p.drawPermissionComposer(s, inputRect)
	} else {
		p.drawFooterRunIndicator(s, runIndicatorRect)
		p.drawComposer(s, inputRect)
		p.drawCommandPalette(s, inputRect, headerH, footerRect.Y)
		p.drawMentionPalette(s, inputRect, headerH, footerRect.Y)
	}
	p.drawFooterBar(s, footerRect)
	if p.planEditorModalActive() {
		p.drawPlanEditorModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.planUpdateModalActive() {
		p.drawPlanUpdateModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.planExitModalActive() {
		p.drawPlanExitModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.manageTodosModalActive() {
		p.drawManageTodosModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.askUserModalActive() {
		p.drawAskUserModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.workspaceScopeModalActive() {
		p.drawWorkspaceScopeModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.taskLaunchModalActive() {
		p.drawTaskLaunchModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.themeChangeModalActive() {
		p.drawThemeChangeModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.agentChangeModalActive() {
		p.drawAgentChangeModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.skillChangeModalActive() {
		p.drawSkillChangeModal(s, Rect{X: 0, Y: 0, W: w, H: h})
	} else if p.sessionsPaletteActive() {
		p.drawSessionsPalette(s, Rect{X: 0, Y: 0, W: w, H: h})
	}
	toastInset := 1
	drawToastOverlay(s, p.theme, &p.toast, Rect{X: 0, Y: 0, W: w, H: h}, toastInset)
}

func (p *ChatPage) submitInput() {
	trimmed := strings.TrimSpace(p.input)
	if trimmed == "" {
		return
	}
	if !p.canSubmitPrompt(trimmed) {
		return
	}
	p.input = ""
	p.inputCursor = 0
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.startRun(trimmed)
}

func (p *ChatPage) startRun(prompt string) {
	p.startRunRequest(p.runRequestForPrompt(prompt), prompt, true, "running turn")
}

func (p *ChatPage) runRequestForPrompt(prompt string) ChatRunRequest {
	if targetName, task, ok := parseTargetedSubagentPrompt(prompt, p.mentionSubagents); ok {
		return ChatRunRequest{
			Prompt:     task,
			TargetKind: "subagent",
			TargetName: targetName,
		}
	}
	return ChatRunRequest{
		Prompt:    strings.TrimSpace(prompt),
		AgentName: strings.TrimSpace(p.meta.Agent),
	}
}

func (p *ChatPage) startManualCompact(note string) bool {
	note = strings.TrimSpace(note)
	displayPrompt := "/compact"
	if note != "" {
		displayPrompt = displayPrompt + " " + note
	}
	return p.startRunRequest(ChatRunRequest{
		Prompt:    note,
		AgentName: strings.TrimSpace(p.meta.Agent),
		Compact:   true,
	}, displayPrompt, false, "compacting context")
}

func (p *ChatPage) startRunRequest(req ChatRunRequest, displayPrompt string, appendUserMessage bool, runningStatus string) bool {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.AgentName == "" && strings.TrimSpace(req.TargetKind) == "" && strings.TrimSpace(req.TargetName) == "" {
		req.AgentName = strings.TrimSpace(p.meta.Agent)
	}
	displayPrompt = strings.TrimSpace(displayPrompt)
	if displayPrompt == "" {
		displayPrompt = req.Prompt
	}
	if displayPrompt == "" && req.Compact {
		displayPrompt = "/compact"
	}
	if req.Prompt == "" && !req.Compact {
		return false
	}
	if p.effectiveRunActive() {
		p.statusLine = "run already in progress"
		return false
	}
	if p.backend == nil {
		p.appendSystemMessage("Cannot run turn: backend is unavailable.")
		p.statusLine = "run blocked"
		return false
	}
	if p.sessionID == "" {
		p.appendSystemMessage("Cannot run turn: session id is missing.")
		p.statusLine = "run blocked"
		return false
	}

	if appendUserMessage {
		p.appendMessage("user", displayPrompt, time.Now().UnixMilli())
		p.lastPrompt = displayPrompt
	}
	p.busy = true
	p.runID++
	p.runPrompt = displayPrompt
	p.thinkingSummary = p.defaultThinkingSummary(displayPrompt)
	p.liveThinking = ""
	p.thinkingCompletedAt = time.Time{}
	p.reasoningSegment = 0
	p.reasoningActive = false
	p.reasoningStartedAt = time.Time{}
	p.activeReasoningMessageID = ""
	p.liveAssistant = ""
	p.streamingRun = false
	p.streamedTools = make(map[string]struct{}, 16)
	p.runStarted = time.Time{}
	p.lifecycle = nil
	p.ownedRunID = ""
	runningStatus = strings.TrimSpace(runningStatus)
	if runningStatus == "" {
		runningStatus = "running turn"
	}
	p.statusLine = runningStatus
	p.errorLine = ""
	p.runAbort = false

	runID := p.runID
	// Keep streaming runs user-cancelable without forcing an 8-minute hard stop.
	runCtx, cancel := context.WithCancel(context.Background())
	p.runCancel = cancel
	go func() {
		defer cancel()
		resp, err := p.backend.RunTurnStream(runCtx, p.sessionID, ChatRunRequest{
			Prompt:       req.Prompt,
			AgentName:    req.AgentName,
			Instructions: req.Instructions,
			Compact:      req.Compact,
			TargetKind:   req.TargetKind,
			TargetName:   req.TargetName,
		}, func(event ChatRunStreamEvent) {
			queued := p.enqueueRunStreamEvent(chatRunStreamResult{RunID: runID, Event: event, AtUnix: time.Now().UnixMilli()})
			if queued.queued {
				eventType := strings.ToLower(strings.TrimSpace(event.Type))
				switch eventType {
				case "assistant.delta", "assistant.commentary", "message.updated", "reasoning.delta", "tool.delta":
					p.requestAsyncRender()
				default:
					p.notifyAsyncEvent()
				}
			}
		})
		result := chatRunResult{RunID: runID, Response: resp, Err: err}
		select {
		case p.runResults <- result:
			p.notifyAsyncEvent()
		default:
		}
	}()
	return true
}

func (p *ChatPage) drainHistoryResults() bool {
	if !p.historyLoading {
		return false
	}
	select {
	case result := <-p.historyResults:
		p.historyLoading = false
		p.historyLoaded = true
		if result.Err != nil {
			p.appendSystemMessage("Unable to load session history: " + strings.TrimSpace(result.Err.Error()))
			p.statusLine = "history load failed"
			return true
		}
		p.applyHistory(result.Messages)
		if len(result.Messages) > 0 {
			p.statusLine = fmt.Sprintf("loaded %d history messages", len(result.Messages))
		}
		return true
	default:
		return false
	}
}

func (p *ChatPage) drainUsageResults() bool {
	if !p.usageLoading {
		return false
	}
	select {
	case result := <-p.usageResults:
		p.usageLoading = false
		if result.Err != nil {
			return true
		}
		p.applyContextUsageSummary(result.Summary)
		return true
	default:
		return false
	}
}

func (p *ChatPage) drainRunStream() bool {
	changed := false
	for {
		select {
		case streamed := <-p.runStream:
			if streamed.RunID != p.runID {
				continue
			}
			p.trackOwnedRunStreamEvent(streamed.Event)
			p.applyRunStreamEvent(streamed.Event, streamed.AtUnix)
			changed = true
		default:
			return changed
		}
	}
}

func (p *ChatPage) applyRunStreamEvent(event ChatRunStreamEvent, atUnix int64) {
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if eventType == "" {
		return
	}
	p.streamingRun = true
	p.maybeCompleteThinkingBeforeEvent(event, eventType, atUnix)

	switch eventType {
	case "session.lifecycle.updated":
		if event.Lifecycle != nil {
			p.ApplySessionLifecycle(*event.Lifecycle)
		}
		return
	case "turn.started":
		if agent := strings.TrimSpace(event.Agent); agent != "" {
			p.meta.Agent = agent
		}
		if strings.TrimSpace(p.thinkingSummary) == "" {
			p.thinkingSummary = p.defaultThinkingSummary(p.runPrompt)
		}
		p.statusLine = fmt.Sprintf("winding up %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "step.started":
		if strings.TrimSpace(p.statusLine) == "" {
			p.statusLine = fmt.Sprintf("working %s", p.runElapsedLabel())
		}
	case "reasoning.started":
		p.startReasoningSegment(atUnix)
		p.statusLine = fmt.Sprintf("thinking %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "reasoning.delta":
		p.startReasoningSegment(atUnix)
		snapshot := canonicalThinkingText(event.Delta)
		if snapshot == "" {
			return
		}
		p.liveThinking = snapshot
		if summary := defaultSummaryFromText(snapshot); summary != "" {
			p.thinkingSummary = summary
		}
		p.updateThinkingTimelineMessage(snapshot, atUnix)
		p.statusLine = fmt.Sprintf("thinking %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "reasoning.summary":
		summary := canonicalThinkingText(firstNonEmptyStringUI(strings.TrimSpace(event.Summary), strings.TrimSpace(event.Delta)))
		if summary == "" {
			return
		}
		p.startReasoningSegment(atUnix)
		p.liveThinking = summary
		p.thinkingSummary = normalizeThinkingSummary(summary)
		p.updateThinkingTimelineMessage(summary, atUnix)
		p.statusLine = fmt.Sprintf("thinking %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "reasoning.completed":
		summary := canonicalThinkingText(firstNonEmptyStringUI(strings.TrimSpace(event.Summary), strings.TrimSpace(event.Delta), strings.TrimSpace(p.liveThinking)))
		if summary != "" {
			p.liveThinking = summary
			p.thinkingSummary = normalizeThinkingSummary(summary)
		}
		p.completeThinkingTimeline("done", atUnix, summary)
		p.statusLine = fmt.Sprintf("working %s", p.runElapsedLabel())
	case "assistant.delta":
		p.completeThinkingTimeline("done", atUnix, "")
		p.liveAssistant = mergeAssistantStream(p.liveAssistant, event.Delta)
		p.statusLine = fmt.Sprintf("streaming response %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "assistant.commentary":
		p.completeThinkingTimeline("done", atUnix, "")
		p.liveAssistant = mergeAssistantStream(p.liveAssistant, event.Delta)
		p.statusLine = fmt.Sprintf("working %s %s", p.spinnerFrame(), p.runElapsedLabel())
	case "usage.updated":
		p.applyContextUsageSummary(event.UsageSummary)
	case "tool.started":
		// Flush any accumulated assistant/commentary text to the timeline before
		// the tool entry so it appears above the tool call, not pinned at the bottom.
		p.flushLiveAssistantToTimeline(atUnix)

		startedToolName := strings.TrimSpace(event.ToolName)
		startedArgs := strings.TrimSpace(event.Arguments)
		startedArgsAreJSON := parseToolJSON(startedArgs) != nil
		startedOutput := startedArgs
		startedState := "pending"
		if startedArgsAreJSON {
			// tool.started carries input arguments. Suppress raw JSON blobs in the
			// live tool stream and let canonical tool.completed summaries replace them.
			startedOutput = ""
		}
		if strings.TrimSpace(event.Output) != "" {
			startedOutput = strings.TrimSpace(event.Output)
			startedState = "running"
		}
		entry := chatToolStreamEntry{
			ToolName:           startedToolName,
			CallID:             strings.TrimSpace(event.CallID),
			Output:             startedOutput,
			Raw:                startedOutput,
			StartedArguments:   startedArgs,
			StartedArgsAreJSON: startedArgsAreJSON,
			CreatedAt:          atUnix,
			StartedAt:          atUnix,
			State:              startedState,
		}
		p.upsertToolStreamEntry(entry)
		p.statusLine = fmt.Sprintf("running tool %s", emptyValue(entry.ToolName, "tool"))
		p.maybeStartInlineBashOutput(entry)
		if isBashToolName(entry.ToolName) {
			p.upsertManagedToolTimelineMessage(entry, atUnix)
		}
	case "tool.delta":
		toolName := strings.TrimSpace(event.ToolName)
		callID := strings.TrimSpace(event.CallID)
		p.appendToolDelta(callID, toolName, event.Output, atUnix)
		entry := p.latestToolStreamEntry(callID, toolName)
		p.updateInlineBashOutput(entry, event.Output, atUnix)
		if p.shouldRefreshManagedToolTimeline(entry, event.Output, atUnix) {
			p.upsertManagedToolTimelineMessage(entry, atUnix)
		}
		if toolName == "" {
			toolName = "tool"
		}
		p.statusLine = fmt.Sprintf("streaming tool %s", toolName)
	case "tool.completed":
		rawOutput := strings.TrimSpace(event.RawOutput)
		if rawOutput == "" {
			rawOutput = strings.TrimSpace(event.Output)
		}
		entry := chatToolStreamEntry{
			ToolName:   strings.TrimSpace(event.ToolName),
			CallID:     strings.TrimSpace(event.CallID),
			Output:     event.Output,
			Raw:        rawOutput,
			Error:      strings.TrimSpace(event.Error),
			CreatedAt:  atUnix,
			State:      "done",
			DurationMS: event.DurationMS,
		}
		if entry.Error != "" {
			entry.State = "error"
		}
		p.upsertToolStreamEntry(entry)
		entry = p.latestToolStreamEntryForEntry(entry)
		p.finishInlineBashOutput(entry)
		if strings.EqualFold(strings.TrimSpace(entry.ToolName), "exit_plan_mode") {
			if payload := parseToolJSON(strings.TrimSpace(entry.Raw)); payload != nil {
				if jsonBool(payload, "mode_changed") {
					if targetMode := strings.TrimSpace(jsonString(payload, "target_mode")); targetMode != "" {
						p.applySessionMode(targetMode, false)
					}
				}
			}
		}
		callKey := toolReplayDedupKey(entry)
		if _, seen := p.streamedTools[callKey]; !seen {
			p.appendToolMessage(entry, atUnix)
			p.streamedTools[callKey] = struct{}{}
		}
	case "message.stored":
		if event.Message == nil {
			return
		}
		msg := *event.Message
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "user":
			if strings.TrimSpace(event.RunID) == strings.TrimSpace(p.ownedRunID) && strings.TrimSpace(p.ownedRunID) != "" {
				return
			}
			p.appendStoredMessageWithMetadata(msg.ID, role, msg.Content, msg.Metadata, msg.CreatedAt)
		case "assistant":
			if text := strings.TrimSpace(msg.Content); text != "" {
				if isCommentaryMetadata(msg.Metadata) {
					p.completeThinkingTimeline("done", msg.CreatedAt, "")
					p.liveAssistant = mergeAssistantStream(p.liveAssistant, text)
					return
				}
				if strings.TrimSpace(event.RunID) == strings.TrimSpace(p.ownedRunID) && strings.TrimSpace(p.ownedRunID) != "" {
					p.completeThinkingTimeline("done", msg.CreatedAt, "")
					p.liveAssistant = text
					return
				}
				p.completeThinkingTimeline("done", msg.CreatedAt, "")
				p.liveAssistant = ""
				p.appendStoredMessageWithMetadata(msg.ID, role, text, msg.Metadata, msg.CreatedAt)
			}
		case "reasoning":
			rawSummary := canonicalThinkingText(msg.Content)
			summary := normalizeThinkingSummary(rawSummary)
			if summary == "" {
				return
			}
			p.thinkingSummary = summary
			if strings.TrimSpace(event.RunID) == strings.TrimSpace(p.ownedRunID) && strings.TrimSpace(p.ownedRunID) != "" && p.busy {
				return
			}
			p.upsertStoredMessageWithMetadata(msg.ID, role, rawSummary, msg.Metadata, msg.CreatedAt)
		case "tool":
			entry := parseToolStreamEntry(msg.Content, msg.CreatedAt)
			p.upsertToolStreamEntry(entry)
			callKey := toolReplayDedupKey(entry)
			p.streamedTools[callKey] = struct{}{}
		}
	case "message.updated":
		if event.Message == nil {
			return
		}
		msg := *event.Message
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "assistant":
			if text := strings.TrimSpace(msg.Content); text != "" {
				p.upsertStoredMessageWithMetadata(msg.ID, role, text, msg.Metadata, msg.CreatedAt)
			}
		case "reasoning":
			rawSummary := canonicalThinkingText(msg.Content)
			summary := normalizeThinkingSummary(rawSummary)
			if summary == "" {
				return
			}
			p.thinkingSummary = summary
			if strings.TrimSpace(event.RunID) == strings.TrimSpace(p.ownedRunID) && strings.TrimSpace(p.ownedRunID) != "" && p.busy {
				return
			}
			p.upsertStoredMessageWithMetadata(msg.ID, role, rawSummary, msg.Metadata, msg.CreatedAt)
		case "tool":
			p.upsertStoredMessageWithMetadata(msg.ID, role, msg.Content, msg.Metadata, msg.CreatedAt)
		}
	case "turn.error":
		msg := strings.TrimSpace(event.Error)
		if msg != "" {
			p.errorLine = msg
		}
		p.completeThinkingTimeline("error", atUnix, "")
	case "permission.requested":
		record, ok := permissionRecordFromStreamEvent(event)
		if !ok {
			return
		}
		p.permissions = mergePermissionHistory(p.permissions, []ChatPermissionRecord{record})
		p.rebuildToolLifecycleViews()
		if permissionArgumentsNeedBackfill(record.ToolArguments) {
			p.queueRefreshPendingPermissions("permission.requested")
		}
		p.statusLine = fmt.Sprintf("permission requested: %s", permissionDisplayToolName(record.ToolName))
	case "permission.updated":
		record, ok := permissionRecordFromStreamEvent(event)
		if !ok {
			return
		}
		p.permissions = mergePermissionHistory(p.permissions, []ChatPermissionRecord{record})
		p.rebuildToolLifecycleViews()
		if permissionArgumentsNeedBackfill(record.ToolArguments) {
			p.queueRefreshPendingPermissions("permission.updated")
		}
		status := strings.ToLower(strings.TrimSpace(record.Status))
		switch status {
		case "approved":
			if isExitPlanPermission(record) {
				p.statusLine = "exit plan mode approved"
				p.queueRefreshSessionMode()
				p.SetAgentRuntime(p.meta.Agent, p.meta.AgentExecutionSetting, true, true)
			} else if isPlanUpdatePermission(record) {
				p.statusLine = "plan update approved"
			} else if isManageTodosPermission(record) {
				p.statusLine = "todo changes approved"
			} else if isWorkspaceScopePermission(record) {
				p.statusLine = workspaceScopeApprovedStatus(record.Reason)
			} else {
				p.statusLine = "permission approved"
			}
		case "denied":
			p.statusLine = "permission denied"
		case "cancelled":
			p.statusLine = "permission cancelled"
		}
	case "session.title.updated":
		title := strings.TrimSpace(event.Title)
		if title == "" {
			return
		}
		p.SetSessionTitle(title)
		stage := strings.TrimSpace(event.TitleStage)
		if stage == "" {
			stage = "update"
		}
		p.statusLine = fmt.Sprintf("session title updated (%s)", stage)
	case "session.title.warning":
		warning := strings.TrimSpace(event.Warning)
		if warning == "" {
			warning = "session title update fallback: memory title generation failed"
		}
		p.ApplySessionTitleWarning(warning)
	}
}

func (p *ChatPage) maybeCompleteThinkingBeforeEvent(event ChatRunStreamEvent, eventType string, atUnix int64) {
	if p == nil || !p.reasoningActive || p.currentThinkingTimelineIndex() < 0 {
		return
	}
	switch eventType {
	case "turn.started", "step.started", "usage.updated", "session.lifecycle.updated", "session.title.updated", "session.title.warning":
		return
	case "reasoning.started", "reasoning.delta", "reasoning.summary":
		return
	case "message.stored", "message.updated":
		if event.Message == nil {
			return
		}
		switch strings.ToLower(strings.TrimSpace(event.Message.Role)) {
		case "assistant", "tool", "system":
			p.completeThinkingTimeline("done", atUnix, "")
		}
		return
	case "assistant.delta", "assistant.commentary", "tool.started", "tool.delta", "tool.completed", "permission.requested", "permission.updated", "turn.error", "reasoning.completed":
		p.completeThinkingTimeline("done", atUnix, "")
	}
}

func (p *ChatPage) ApplySharedStreamEvent(event ChatRunStreamEvent, atUnix int64) bool {
	if p == nil {
		return false
	}
	if p.shouldIgnoreSharedStreamEvent(event) {
		return false
	}
	p.applyRunStreamEvent(event, atUnix)
	return true
}

func (p *ChatPage) shouldIgnoreSharedStreamEvent(event ChatRunStreamEvent) bool {
	if p == nil {
		return true
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID != "" && strings.TrimSpace(p.sessionID) != "" && sessionID != strings.TrimSpace(p.sessionID) {
		return true
	}
	runID := strings.TrimSpace(event.RunID)
	if runID == "" {
		switch {
		case event.Lifecycle != nil:
			runID = strings.TrimSpace(event.Lifecycle.RunID)
		case event.Permission != nil:
			runID = strings.TrimSpace(event.Permission.RunID)
		}
	}
	if runID == "" {
		return false
	}
	if p.runCancel != nil {
		if strings.TrimSpace(p.ownedRunID) == "" {
			return true
		}
		return runID == strings.TrimSpace(p.ownedRunID)
	}
	return runID != "" && strings.TrimSpace(p.ownedRunID) != "" && runID == strings.TrimSpace(p.ownedRunID)
}

func (p *ChatPage) trackOwnedRunStreamEvent(event ChatRunStreamEvent) {
	if p == nil || strings.TrimSpace(p.ownedRunID) != "" {
		return
	}
	runID := strings.TrimSpace(event.RunID)
	if runID == "" {
		if event.Lifecycle != nil {
			runID = strings.TrimSpace(event.Lifecycle.RunID)
		} else if event.Permission != nil {
			runID = strings.TrimSpace(event.Permission.RunID)
		}
	}
	if runID == "" {
		return
	}
	if p.runCancel != nil || p.streamingRun || p.runAbort {
		p.ownedRunID = runID
	}
}

func (p *ChatPage) ApplySessionLifecycle(lifecycle ChatSessionLifecycle) {
	lifecycle.SessionID = strings.TrimSpace(lifecycle.SessionID)
	if lifecycle.SessionID != "" && strings.TrimSpace(p.sessionID) != "" && lifecycle.SessionID != strings.TrimSpace(p.sessionID) {
		return
	}
	lifecycle.RunID = strings.TrimSpace(lifecycle.RunID)
	lifecycle.Phase = strings.TrimSpace(lifecycle.Phase)
	lifecycle.StopReason = strings.TrimSpace(lifecycle.StopReason)
	lifecycle.Error = strings.TrimSpace(lifecycle.Error)
	lifecycle.OwnerTransport = strings.TrimSpace(lifecycle.OwnerTransport)

	copy := lifecycle
	p.lifecycle = &copy
	p.runAbort = false

	if lifecycle.Active {
		p.busy = true
		if lifecycle.StartedAt > 0 {
			p.runStarted = time.UnixMilli(lifecycle.StartedAt)
		}
		switch strings.ToLower(lifecycle.Phase) {
		case "blocked":
			p.statusLine = "waiting for permission"
		case "starting":
			p.statusLine = "starting run"
		case "running":
			if strings.TrimSpace(p.statusLine) == "" || strings.EqualFold(strings.TrimSpace(p.statusLine), "starting run") {
				p.statusLine = "running turn"
			}
		}
		return
	}

	switch strings.ToLower(lifecycle.Phase) {
	case "errored":
		p.completeThinkingTimeline("error", time.Now().UnixMilli(), "")
	default:
		p.completeThinkingTimeline("done", time.Now().UnixMilli(), "")
	}
	p.busy = false
	p.runStarted = time.Time{}
	p.runCancel = nil
	p.streamingRun = false
	p.liveAssistant = ""
	p.liveThinking = ""
	p.thinkingCompletedAt = time.Time{}
	p.reasoningActive = false
	p.reasoningStartedAt = time.Time{}
	p.activeReasoningMessageID = ""
	p.ownedRunID = ""
	switch strings.ToLower(lifecycle.Phase) {
	case "errored":
		if lifecycle.Error != "" {
			p.errorLine = lifecycle.Error
		}
		p.statusLine = "run failed"
	case "cancelled":
		if lifecycle.StopReason != "" {
			p.statusLine = lifecycle.StopReason
		} else {
			p.statusLine = "run stopped"
		}
		p.errorLine = ""
	case "interrupted":
		if lifecycle.StopReason != "" {
			p.statusLine = lifecycle.StopReason
		} else {
			p.statusLine = "run interrupted"
		}
		p.errorLine = ""
	default:
		p.statusLine = "ready"
		p.errorLine = ""
	}
}

func permissionRecordFromStreamEvent(event ChatRunStreamEvent) (ChatPermissionRecord, bool) {
	if event.Permission == nil {
		permissionParserDebugf("permission stream event missing permission payload type=%s run=%s", strings.TrimSpace(event.Type), strings.TrimSpace(event.RunID))
		return ChatPermissionRecord{}, false
	}
	record := *event.Permission
	if strings.TrimSpace(record.ToolArguments) == "" {
		record.ToolArguments = strings.TrimSpace(event.Arguments)
	}
	if strings.TrimSpace(record.ToolName) == "" {
		record.ToolName = strings.TrimSpace(event.ToolName)
	}
	if strings.TrimSpace(record.CallID) == "" {
		record.CallID = strings.TrimSpace(event.CallID)
	}
	if strings.TrimSpace(record.ToolArguments) == "" {
		permissionParserDebugf("permission stream event has empty args type=%s tool=%s call=%s run=%s", strings.TrimSpace(event.Type), strings.TrimSpace(record.ToolName), strings.TrimSpace(record.CallID), strings.TrimSpace(record.RunID))
	}
	return record, true
}

func permissionArgumentsNeedBackfill(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "", "{}", "null", `""`:
		return true
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return true
	}
	switch typed := payload.(type) {
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case nil:
		return true
	default:
		return false
	}
}

func mergePermissionRecord(existing, incoming ChatPermissionRecord) ChatPermissionRecord {
	preferIncoming := permissionFreshnessTimestamp(incoming) >= permissionFreshnessTimestamp(existing)
	merged := existing
	other := incoming
	if preferIncoming {
		merged = incoming
		other = existing
	}
	if strings.TrimSpace(merged.ToolArguments) == "" {
		merged.ToolArguments = other.ToolArguments
	}
	if strings.TrimSpace(merged.ToolName) == "" {
		merged.ToolName = other.ToolName
	}
	if strings.TrimSpace(merged.CallID) == "" {
		merged.CallID = other.CallID
	}
	if strings.TrimSpace(merged.RunID) == "" {
		merged.RunID = other.RunID
	}
	if strings.TrimSpace(merged.Requirement) == "" {
		merged.Requirement = other.Requirement
	}
	if strings.TrimSpace(merged.Mode) == "" {
		merged.Mode = other.Mode
	}
	if merged.Step == 0 {
		merged.Step = other.Step
	}
	if merged.PermissionRequestedAt == 0 {
		merged.PermissionRequestedAt = other.PermissionRequestedAt
	}
	if strings.TrimSpace(merged.ExecutionStatus) == "" {
		merged.ExecutionStatus = other.ExecutionStatus
	}
	if strings.TrimSpace(merged.Output) == "" {
		merged.Output = other.Output
	}
	if strings.TrimSpace(merged.Error) == "" {
		merged.Error = other.Error
	}
	if merged.DurationMS == 0 {
		merged.DurationMS = other.DurationMS
	}
	if merged.StartedAt == 0 {
		merged.StartedAt = other.StartedAt
	}
	if merged.CompletedAt == 0 {
		merged.CompletedAt = other.CompletedAt
	}
	if merged.CreatedAt == 0 {
		merged.CreatedAt = other.CreatedAt
	}
	if merged.UpdatedAt == 0 {
		merged.UpdatedAt = other.UpdatedAt
	}
	if merged.ResolvedAt == 0 {
		merged.ResolvedAt = other.ResolvedAt
	}
	if strings.TrimSpace(merged.Status) == "" {
		merged.Status = other.Status
	}
	if strings.TrimSpace(merged.Decision) == "" {
		merged.Decision = other.Decision
	}
	if strings.TrimSpace(merged.Reason) == "" {
		merged.Reason = other.Reason
	}
	return merged
}

func permissionFreshnessTimestamp(record ChatPermissionRecord) int64 {
	for _, value := range []int64{record.UpdatedAt, record.CompletedAt, record.ResolvedAt, record.StartedAt, record.PermissionRequestedAt, record.CreatedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (p *ChatPage) drainRunResults() bool {
	changed := false
	for {
		select {
		case result := <-p.runResults:
			changed = true
			if result.RunID != p.runID {
				continue
			}
			p.runCancel = nil
			if result.Err != nil {
				if p.lifecycle != nil && !p.lifecycle.Active {
					switch strings.ToLower(strings.TrimSpace(p.lifecycle.Phase)) {
					case "cancelled", "interrupted", "errored":
						continue
					}
				}
				if p.runAbort && isCancelledRunError(result.Err) {
					if p.lifecycle == nil || !p.lifecycle.Active {
						p.statusLine = "run aborted"
						p.errorLine = ""
						p.appendSystemMessage("Agentic loop aborted, double escape for home.")
						p.completeThinkingTimeline("done", time.Now().UnixMilli(), "")
						p.liveAssistant = ""
						p.liveThinking = ""
						p.thinkingCompletedAt = time.Time{}
						p.reasoningActive = false
						p.reasoningStartedAt = time.Time{}
						p.streamingRun = false
						p.runAbort = false
					}
					continue
				}
				if p.lifecycle != nil && p.lifecycle.Active {
					continue
				}
				p.busy = false
				p.runAbort = false
				friendly := p.friendlyRunError(result.Err)
				p.errorLine = friendly
				p.statusLine = "run failed"
				p.appendSystemMessage(friendly)
				p.completeThinkingTimeline("error", time.Now().UnixMilli(), "")
				p.liveAssistant = ""
				p.liveThinking = ""
				p.thinkingCompletedAt = time.Time{}
				p.reasoningActive = false
				p.reasoningStartedAt = time.Time{}
				p.streamingRun = false
				continue
			}
			p.runAbort = false
			if p.lifecycle == nil || !p.lifecycle.Active {
				p.busy = false
				p.runStarted = time.Time{}
			}
			p.applyRunSuccess(result.Response)
		default:
			return changed
		}
	}
}

func (p *ChatPage) drainRunStops() bool {
	changed := false
	for {
		select {
		case result := <-p.runStops:
			changed = true
			if result.RunID != p.runID {
				continue
			}
			if result.Err != nil {
				p.runAbort = false
				p.errorLine = strings.TrimSpace(result.Err.Error())
				p.statusLine = "stop failed"
				continue
			}
			p.statusLine = "stopping run"
		default:
			return changed
		}
	}
}

func (p *ChatPage) drainPermissionLoads() bool {
	if !p.permissionsLoading {
		return false
	}
	select {
	case result := <-p.permissionResults:
		p.permissionsLoading = false
		if result.ModeErr == nil {
			p.applySessionMode(result.Mode, len(p.timeline) == 0 && normalizeSessionMode(result.Mode) == "plan")
		}
		if result.PendingErr == nil {
			p.permissions = mergePermissionHistory(nil, result.Records)
			p.rebuildToolLifecycleViews()
		}
		if result.ModeErr != nil && result.PendingErr != nil {
			p.statusLine = "permission state unavailable"
		}
		return true
	default:
		return false
	}
}

func (p *ChatPage) drainPermissionActions() bool {
	changed := false
	for {
		select {
		case result := <-p.permissionActions:
			changed = true
			if result.Action == "permission.refresh_pending" {
				p.permissionBackfillInFlight = false
				if result.Err != nil {
					permissionParserDebugf("permission backfill refresh failed err=%v", result.Err)
					continue
				}
				p.permissions = mergePermissionHistory(p.permissions, result.Pending)
				p.rebuildToolLifecycleViews()
				permissionParserDebugf("permission backfill refresh merged pending=%d", len(filterPendingPermissions(result.Pending)))
				continue
			}
			if result.Err != nil {
				p.errorLine = result.Err.Error()
				p.statusLine = "permission action failed"
				continue
			}
			switch result.Action {
			case "mode.set":
				p.applySessionMode(result.Mode, result.Announce)
			case "mode.refresh":
				p.applySessionMode(result.Mode, false)
			case "permission.resolve":
				p.permissions = mergePermissionHistory(p.permissions, []ChatPermissionRecord{result.Permission})
				p.rebuildToolLifecycleViews()
				p.applySessionMode(result.Mode, false)
				status := strings.ToLower(strings.TrimSpace(result.Permission.Status))
				switch status {
				case "approved":
					if isExitPlanPermission(result.Permission) {
						p.statusLine = "exit plan mode approved -> mode " + p.sessionMode
					} else if isPlanUpdatePermission(result.Permission) {
						p.statusLine = "plan update approved"
					} else if isManageTodosPermission(result.Permission) {
						p.statusLine = "todo changes approved"
					} else if isAskUserPermission(result.Permission) {
						p.statusLine = "ask-user response submitted"
					} else if isWorkspaceScopePermission(result.Permission) {
						p.statusLine = workspaceScopeApprovedStatus(result.Permission.Reason)
					} else if isTaskLaunchPermission(result.Permission) {
						p.statusLine = "task launch approved"
					} else if strings.EqualFold(strings.TrimSpace(result.Permission.Decision), "allow_always") || strings.TrimSpace(result.Permission.SavedRulePreview) != "" {
						preview := strings.TrimSpace(result.Permission.SavedRulePreview)
						if preview == "" {
							p.statusLine = "permission always allowed"
						} else {
							p.statusLine = "always allowed: " + preview
						}
					} else {
						p.statusLine = "permission approved"
					}
				case "denied":
					if strings.EqualFold(strings.TrimSpace(result.Permission.Decision), "deny_always") || strings.TrimSpace(result.Permission.SavedRulePreview) != "" {
						preview := strings.TrimSpace(result.Permission.SavedRulePreview)
						if preview == "" {
							p.statusLine = "permission always denied"
						} else {
							p.statusLine = "always denied: " + preview
						}
					} else {
						p.statusLine = "permission denied"
					}
				case "cancelled":
					p.statusLine = "permission cancelled"
				default:
					p.statusLine = "permission updated"
				}
				p.permInput = ""
			case "permission.resolve_all":
				p.permissions = mergePermissionHistory(p.permissions, result.ResolvedMany)
				p.rebuildToolLifecycleViews()
				p.applySessionMode(result.Mode, false)
				p.statusLine = fmt.Sprintf("approved %d pending permissions", len(result.ResolvedMany))
				p.permInput = ""
			}
		default:
			return changed
		}
	}
}

func (p *ChatPage) queueRefreshPendingPermissions(trigger string) {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	if p.permissionBackfillInFlight {
		return
	}
	p.permissionBackfillInFlight = true
	permissionParserDebugf("permission backfill refresh queued trigger=%s", strings.TrimSpace(trigger))
	go func(trigger string) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		pending, err := p.backend.ListPendingPermissions(ctx, p.sessionID, 200)
		p.permissionActions <- chatPermissionActionResult{
			Action:  "permission.refresh_pending",
			Pending: pending,
			Err:     err,
		}
		p.notifyAsyncEvent()
	}(trigger)
}

func (p *ChatPage) queueCycleMode() {
	p.queueSetMode(nextSessionMode(p.sessionMode), false)
}

func (p *ChatPage) queueSetMode(target string, announce bool) {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	next := normalizeSessionMode(target)
	p.statusLine = "switching mode..."
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		mode, err := p.backend.SetSessionMode(ctx, p.sessionID, next)
		select {
		case p.permissionActions <- chatPermissionActionResult{
			Action:   "mode.set",
			Mode:     mode,
			Announce: announce,
			Err:      err,
		}:
			p.notifyAsyncEvent()
		default:
		}
	}()
}

func (p *ChatPage) applySessionMode(mode string, announce bool) {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" {
		return
	}
	next := normalizeSessionMode(trimmed)
	prev := normalizeSessionMode(p.sessionMode)
	p.sessionMode = next
	if prev != next {
		p.statusLine = "mode: " + next
		if announce {
			p.appendSystemMessage(sessionModeGuidance(next))
		}
	}
}

func sessionModeGuidance(mode string) string {
	switch normalizeSessionMode(mode) {
	case "plan":
		return "Plan mode active: build/refine the active plan with plan_manage, use ask-user for decisions, then run /plan exit to submit it for approval. After approval, auto continues on the same plan/checklist."
	case "read":
		return "Read mode active: inspect and analyze within read-only capability and the configured tool scope."
	case "readwrite":
		return "Readwrite mode active: continue execution with read/write capability, but bash remains unavailable unless the configured tool scope explicitly permits it."
	default:
		return "Auto mode active: continue execution with normal approval gates. The active plan/checklist remains available via plan_manage; exit_plan_mode is only for leaving plan mode."
	}
}

func (p *ChatPage) queueRefreshSessionMode() {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		mode, err := p.backend.GetSessionMode(ctx, p.sessionID)
		if err != nil {
			return
		}
		select {
		case p.permissionActions <- chatPermissionActionResult{
			Action: "mode.refresh",
			Mode:   mode,
		}:
			p.notifyAsyncEvent()
		default:
		}
	}()
}

func (p *ChatPage) queueResolveAll(action, reason string) {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	indexes := p.genericPermissionIndexes()
	if len(indexes) == 0 {
		p.statusLine = "no pending permissions"
		return
	}
	targets := make([]ChatPermissionRecord, 0, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(p.pendingPerms) {
			continue
		}
		targets = append(targets, p.pendingPerms[idx])
	}
	if len(targets) == 0 {
		p.statusLine = "no pending permissions"
		return
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "allow_once"
	}
	reason = strings.TrimSpace(reason)
	p.statusLine = "resolving permissions..."
	go func(targets []ChatPermissionRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolved := make([]ChatPermissionRecord, 0, len(targets))
		for _, target := range targets {
			record, err := p.backend.ResolvePermission(ctx, p.sessionID, target.ID, action, reason)
			if err != nil {
				select {
				case p.permissionActions <- chatPermissionActionResult{
					Action: "permission.resolve_all",
					Err:    err,
				}:
					p.notifyAsyncEvent()
				default:
				}
				return
			}
			resolved = append(resolved, record)
		}
		mode := ""
		if refreshed, modeErr := p.backend.GetSessionMode(ctx, p.sessionID); modeErr == nil {
			mode = refreshed
		}
		select {
		case p.permissionActions <- chatPermissionActionResult{
			Action:       "permission.resolve_all",
			Mode:         mode,
			ResolvedMany: resolved,
		}:
			p.notifyAsyncEvent()
		default:
		}
	}(targets)
}

func (p *ChatPage) queueResolveSelected(action, reason string) {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	if p.genericPermissionCount() == 0 {
		p.statusLine = "no pending permissions"
		return
	}
	p.ensurePermissionSelection()
	if p.permSelected < 0 || p.permSelected >= len(p.pendingPerms) {
		p.statusLine = "no pending permissions"
		return
	}
	selected := p.pendingPerms[p.permSelected]
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "allow_once"
	}
	reason = strings.TrimSpace(reason)
	p.statusLine = "resolving permission..."
	go func(selected ChatPermissionRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		record, err := p.backend.ResolvePermission(ctx, p.sessionID, selected.ID, action, reason)
		mode := ""
		if err == nil {
			if refreshed, modeErr := p.backend.GetSessionMode(ctx, p.sessionID); modeErr == nil {
				mode = refreshed
			}
		}
		select {
		case p.permissionActions <- chatPermissionActionResult{
			Action:     "permission.resolve",
			Mode:       mode,
			Permission: record,
			Err:        err,
		}:
			p.notifyAsyncEvent()
		default:
		}
	}(selected)
}

func (p *ChatPage) queueResolvePermissionByID(permissionID, action, reason string, approvedArguments ...string) {
	if p.backend == nil || strings.TrimSpace(p.sessionID) == "" {
		return
	}
	permissionID = strings.TrimSpace(permissionID)
	if permissionID == "" {
		return
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "allow_once"
	}
	reason = strings.TrimSpace(reason)
	approved := ""
	if len(approvedArguments) > 0 {
		approved = strings.TrimSpace(approvedArguments[0])
	}
	p.statusLine = "resolving permission..."
	go func(permissionID, action, reason, approvedArguments string) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		record, err := p.backend.ResolvePermissionWithArguments(ctx, p.sessionID, permissionID, action, reason, approvedArguments)
		mode := ""
		if err == nil {
			if refreshed, modeErr := p.backend.GetSessionMode(ctx, p.sessionID); modeErr == nil {
				mode = refreshed
			}
		}
		select {
		case p.permissionActions <- chatPermissionActionResult{
			Action:     "permission.resolve",
			Mode:       mode,
			Permission: record,
			Err:        err,
		}:
			p.notifyAsyncEvent()
		default:
		}
	}(permissionID, action, reason, approved)
}

func (p *ChatPage) upsertPendingPermission(record ChatPermissionRecord) {
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	if strings.TrimSpace(record.ID) == "" {
		return
	}
	for i := range p.pendingPerms {
		if strings.TrimSpace(p.pendingPerms[i].ID) != strings.TrimSpace(record.ID) {
			continue
		}
		p.pendingPerms[i] = mergePermissionRecord(p.pendingPerms[i], record)
		p.ensurePermissionSelection()
		p.syncSpecialPermissionModals()
		return
	}
	if record.Status == "pending" {
		p.pendingPerms = append(p.pendingPerms, record)
		p.ensurePermissionSelection()
		p.syncSpecialPermissionModals()
	}
}

func (p *ChatPage) applyPermissionUpdate(record ChatPermissionRecord) {
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	if strings.TrimSpace(record.ID) == "" {
		return
	}
	next := p.pendingPerms[:0]
	existingRecord := ChatPermissionRecord{}
	existingFound := false
	for _, existing := range p.pendingPerms {
		if strings.TrimSpace(existing.ID) == strings.TrimSpace(record.ID) {
			existingRecord = existing
			existingFound = true
			continue
		}
		next = append(next, existing)
	}
	if existingFound {
		record = mergePermissionRecord(existingRecord, record)
	}
	p.pendingPerms = append([]ChatPermissionRecord(nil), next...)
	if record.Status == "pending" {
		p.pendingPerms = append(p.pendingPerms, record)
	}
	p.ensurePermissionSelection()
	p.syncSpecialPermissionModals()
}

func (p *ChatPage) applyHistory(messages []ChatMessageRecord) {
	for _, message := range messages {
		p.ingestMessageRecord(message)
	}
	p.rebuildToolLifecycleViews()
}

func (p *ChatPage) applyRunSuccess(resp ChatRunResponse) {
	p.applyContextUsageSummary(resp.UsageSummary)

	if strings.TrimSpace(resp.Model) != "" {
		p.modelName = strings.TrimSpace(resp.Model)
	}
	if strings.TrimSpace(resp.Thinking) != "" {
		p.thinkingLevel = strings.TrimSpace(resp.Thinking)
	}
	summaryFromResp := ""
	rawSummaryFromResp := ""
	if p.showThinkingTags {
		if p.isCodexModel() {
			rawSummaryFromResp = strings.TrimSpace(p.liveThinking)
			if rawSummaryFromResp == "" {
				rawSummaryFromResp = canonicalThinkingText(resp.ReasoningSummary)
			}
			if rawSummaryFromResp == "" {
				rawSummaryFromResp = canonicalThinkingText(p.thinkingSummary)
			}
		} else {
			rawSummaryFromResp = canonicalThinkingText(resp.ReasoningSummary)
		}
		summaryFromResp = normalizeThinkingSummary(rawSummaryFromResp)
		if summaryFromResp == "" {
			summaryFromResp = defaultSummaryFromText(p.liveThinking)
		}
		if summaryFromResp != "" {
			p.thinkingSummary = summaryFromResp
		}
	}

	if !p.streamingRun {
		for _, toolMessage := range resp.ToolMessages {
			p.ingestMessageRecord(toolMessage)
		}
		for _, commentaryMessage := range resp.Commentary {
			p.ingestMessageRecord(commentaryMessage)
		}
	}

	if p.lifecycle != nil && !p.lifecycle.Active {
		p.streamingRun = false
	}

	assistantText := strings.TrimSpace(resp.AssistantMessage.Content)
	if assistantText == "" {
		assistantText = strings.TrimSpace(p.liveAssistant)
	}
	if assistantText == "" {
		assistantText = "No assistant response text was returned."
	}
	createdAt := resp.AssistantMessage.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	reasoningToPersist := ""
	reasoningToPersist = rawSummaryFromResp
	if summary := normalizeThinkingSummary(reasoningToPersist); summary != "" {
		p.thinkingSummary = summary
	}
	p.completeThinkingTimeline("done", createdAt, reasoningToPersist)
	p.reasoningActive = false
	p.appendMessageWithMetadata("assistant", assistantText, resp.AssistantMessage.Metadata, createdAt)
	p.liveAssistant = ""
	p.liveThinking = ""
	p.thinkingCompletedAt = time.Time{}
	p.reasoningStartedAt = time.Time{}
	p.activeReasoningMessageID = ""
	p.streamingRun = false

	duration := time.Duration(0)
	if started := p.effectiveRunStarted(); !started.IsZero() {
		duration = time.Since(started)
	}
	p.statusLine = fmt.Sprintf("turn complete in %s", formatDurationCompact(duration))
	p.errorLine = ""
}

func (p *ChatPage) applyContextUsageSummary(summary *ChatUsageSummary) {
	if summary == nil {
		return
	}
	p.usageSummary = cloneChatUsageSummary(summary)
	if summary.ContextWindow <= 0 {
		return
	}
	window := summary.ContextWindow
	remaining := summary.RemainingTokens

	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(window) {
		remaining = int64(window)
	}
	p.contextUsageSet = true
	p.contextWindow = window
	p.contextRemain = remaining
}

func cloneChatUsageSummary(summary *ChatUsageSummary) *ChatUsageSummary {
	if summary == nil {
		return nil
	}
	out := *summary
	if summary.LastConnectedViaWS != nil {
		connected := *summary.LastConnectedViaWS
		out.LastConnectedViaWS = &connected
	}
	return &out
}

func (p *ChatPage) ingestMessageRecord(message ChatMessageRecord) {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	createdAt := message.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}

	switch role {
	case "assistant", "user":
		p.appendStoredMessageWithMetadata(message.ID, role, message.Content, message.Metadata, createdAt)
	case "system":
		if isToolDBDebugMessage(message.Content) {
			return
		}
		p.appendStoredMessageWithMetadata(message.ID, role, message.Content, nil, createdAt)
	case "reasoning":
		if summary := canonicalThinkingText(message.Content); summary != "" {
			p.thinkingSummary = normalizeThinkingSummary(summary)
			p.upsertStoredMessageWithMetadata(message.ID, role, summary, message.Metadata, createdAt)
		}
	case "tool":
		entry := parseToolStreamEntry(message.Content, createdAt)
		if shouldSuppressHistoricalToolEntry(entry) {
			return
		}
		entry.EntryKey = historicalToolEntryKey(message, entry)
		p.upsertToolStreamEntry(entry)
		entry = p.latestToolStreamEntryForEntry(entry)
		if isBashToolName(entry.ToolName) {
			p.restoreInlineBashOutput(entry)
		}
		if p.streamedTools == nil {
			p.streamedTools = make(map[string]struct{}, 16)
		}
		callKey := toolReplayDedupKey(entry)
		if _, seen := p.streamedTools[callKey]; !seen {
			p.appendToolMessage(entry, entry.CreatedAt)
		}
		p.streamedTools[callKey] = struct{}{}
		return
	default:
		p.appendStoredMessageWithMetadata(message.ID, "system", message.Content, nil, createdAt)
	}
}

func isToolDBDebugMessage(content string) bool {
	return false
}

func (p *ChatPage) SetHeaderVisible(show bool) {
	p.showHeader = show
}

func (p *ChatPage) appendMessage(role, text string, createdAt int64) {
	p.appendMessageWithMetadata(role, text, nil, createdAt)
}

func (p *ChatPage) appendMessageWithMetadata(role, text string, metadata map[string]any, createdAt int64) {
	p.appendStoredMessageWithMetadata("", role, text, metadata, createdAt)
}

func (p *ChatPage) upsertStoredMessageWithMetadata(messageID, role, text string, metadata map[string]any, createdAt int64) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID != "" {
		for i := len(p.timeline) - 1; i >= 0; i-- {
			if strings.TrimSpace(p.timeline[i].MessageID) != messageID {
				continue
			}
			role = strings.ToLower(strings.TrimSpace(role))
			if role == "" {
				role = "system"
			}
			p.timeline[i] = chatMessageItem{
				MessageID: messageID,
				Role:      role,
				Text:      text,
				CreatedAt: createdAt,
				Metadata:  cloneMetadataMap(metadata),
			}
			p.bumpTimelineRenderGeneration()
			return
		}
	}
	p.appendStoredMessageWithMetadata(messageID, role, text, metadata, createdAt)
}

func (p *ChatPage) appendStoredMessageWithMetadata(messageID, role, text string, metadata map[string]any, createdAt int64) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID != "" && p.hasTimelineMessageID(messageID) {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "system"
	}

	p.timeline = append(p.timeline, chatMessageItem{
		MessageID: messageID,
		Role:      role,
		Text:      text,
		CreatedAt: createdAt,
		Metadata:  cloneMetadataMap(metadata),
	})
	if len(p.timeline) > chatMaxTimelineMessages {
		drop := len(p.timeline) - chatMaxTimelineMessages
		p.timeline = append([]chatMessageItem(nil), p.timeline[drop:]...)
		p.resetTimelineRenderCache()
		return
	}
	p.ensureTimelineRenderCacheLen()
}

func (p *ChatPage) hasTimelineMessageID(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	for i := len(p.timeline) - 1; i >= 0; i-- {
		if strings.TrimSpace(p.timeline[i].MessageID) == messageID {
			return true
		}
	}
	return false
}

func cloneMetadataMap(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func isCommentaryMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	value, _ := metadata["phase"].(string)
	return strings.EqualFold(strings.TrimSpace(value), "commentary")
}

func reasoningTimelineMetadata(runID int, segment int, startedAt int64, durationMS int64) map[string]any {
	metadata := map[string]any{
		chatReasoningTimelineObjectMetadataKey:  true,
		chatReasoningTimelineRunIDMetadataKey:   runID,
		chatReasoningTimelineSegmentMetadataKey: segment,
	}
	if startedAt > 0 {
		metadata[chatReasoningTimelineStartedAtMetadataKey] = startedAt
	}
	if durationMS > 0 {
		metadata[chatReasoningTimelineDurationMetadataKey] = durationMS
	}
	return metadata
}

func toolTimelineMetadata(entry chatToolStreamEntry) map[string]any {
	metadata := map[string]any{
		chatToolTimelineObjectMetadataKey:   true,
		chatToolTimelineToolNameMetadataKey: strings.TrimSpace(entry.ToolName),
		chatToolTimelineCallIDMetadataKey:   strings.TrimSpace(entry.CallID),
	}
	if entryKey := strings.TrimSpace(entry.EntryKey); entryKey != "" {
		metadata[chatToolTimelineEntryKeyMetadataKey] = entryKey
	}
	payload := strings.TrimSpace(entry.Output)
	if payload == "" {
		payload = strings.TrimSpace(entry.Raw)
	}
	if payload == "" && entry.StartedArgsAreJSON {
		payload = strings.TrimSpace(entry.StartedArguments)
	}
	if payload != "" {
		if !isBashToolName(entry.ToolName) {
			metadata[chatToolTimelinePayloadMetadataKey] = payload
		}
	}
	startedAt := entry.StartedAt
	if startedAt <= 0 && strings.EqualFold(strings.TrimSpace(entry.State), "running") {
		startedAt = entry.CreatedAt
	}
	if startedAt > 0 {
		metadata[chatToolTimelineStartedAtMetadataKey] = startedAt
	}
	if entry.DurationMS > 0 {
		metadata[chatToolTimelineDurationMetadataKey] = entry.DurationMS
	}
	return metadata
}

func isTerminalToolState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "error":
		return true
	default:
		return false
	}
}

func toolTimelinePayload(message chatMessageItem) (map[string]any, bool) {
	if !isManagedToolTimelineMessage(message) {
		return nil, false
	}
	raw, _ := message.Metadata[chatToolTimelinePayloadMetadataKey].(string)
	payload := parseToolJSON(strings.TrimSpace(raw))
	if payload == nil {
		return nil, false
	}
	return payload, true
}

func toolTimelineMessageToolName(message chatMessageItem) string {
	if len(message.Metadata) == 0 {
		return ""
	}
	value, _ := message.Metadata[chatToolTimelineToolNameMetadataKey].(string)
	return strings.TrimSpace(value)
}

func toolTimelineMessageCallID(message chatMessageItem) string {
	if len(message.Metadata) == 0 {
		return ""
	}
	value, _ := message.Metadata[chatToolTimelineCallIDMetadataKey].(string)
	return strings.TrimSpace(value)
}

func toolTimelineMessageEntryKey(message chatMessageItem) string {
	if len(message.Metadata) == 0 {
		return ""
	}
	value, _ := message.Metadata[chatToolTimelineEntryKeyMetadataKey].(string)
	return strings.TrimSpace(value)
}

func toolTimelineMessageStartedAt(message chatMessageItem) int64 {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatToolTimelineStartedAtMetadataKey].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func timelineMetadataHash(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Sprintf("%v", metadata)
	}
	return string(encoded)
}

func (p *ChatPage) upsertManagedToolTimelineMessage(entry chatToolStreamEntry, createdAt int64) {
	if p == nil {
		return
	}
	text := formatUnifiedToolEntry(entry)
	if strings.TrimSpace(text) == "" {
		return
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	state := strings.ToLower(strings.TrimSpace(entry.State))
	if state == "" {
		state = "pending"
	}
	message := chatMessageItem{
		Role:      "tool",
		Text:      text,
		CreatedAt: createdAt,
		ToolState: state,
		Metadata:  toolTimelineMetadata(entry),
	}
	if isBashToolName(entry.ToolName) {
		for i := len(p.timeline) - 1; i >= 0; i-- {
			item := p.timeline[i]
			if !isManagedToolTimelineMessage(item) {
				continue
			}
			if entryKey := strings.TrimSpace(entry.EntryKey); entryKey != "" {
				if toolTimelineMessageEntryKey(item) != entryKey {
					continue
				}
			} else if callID := strings.TrimSpace(entry.CallID); callID != "" {
				if toolTimelineMessageCallID(item) != callID {
					continue
				}
			} else if toolName := strings.TrimSpace(entry.ToolName); toolName != "" {
				if !strings.EqualFold(toolTimelineMessageToolName(item), toolName) {
					continue
				}
			} else {
				continue
			}
			if item.Text == message.Text && item.ToolState == message.ToolState && toolTimelineMessageDurationMS(item) == entry.DurationMS {
				return
			}
			break
		}
	}
	callID := strings.TrimSpace(entry.CallID)
	entryKey := strings.TrimSpace(entry.EntryKey)
	toolName := strings.TrimSpace(entry.ToolName)
	for i := len(p.timeline) - 1; i >= 0; i-- {
		item := p.timeline[i]
		if !isManagedToolTimelineMessage(item) {
			continue
		}
		if entryKey != "" {
			if toolTimelineMessageEntryKey(item) != entryKey {
				continue
			}
		}
		if callID != "" && toolTimelineMessageCallID(item) != callID {
			continue
		}
		if callID == "" && toolName != "" && !strings.EqualFold(toolTimelineMessageToolName(item), toolName) {
			continue
		}
		p.timeline[i] = message
		p.bumpTimelineRenderGeneration()
		return
	}
	p.timeline = append(p.timeline, message)
	if len(p.timeline) > chatMaxTimelineMessages {
		drop := len(p.timeline) - chatMaxTimelineMessages
		p.timeline = append([]chatMessageItem(nil), p.timeline[drop:]...)
		p.resetTimelineRenderCache()
		return
	}
	p.ensureTimelineRenderCacheLen()
	p.bumpTimelineRenderGeneration()
}

func toolTimelineMessageDurationMS(message chatMessageItem) int64 {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatToolTimelineDurationMetadataKey].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func isManagedToolTimelineMessage(message chatMessageItem) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") || len(message.Metadata) == 0 {
		return false
	}
	live, _ := message.Metadata[chatToolTimelineObjectMetadataKey].(bool)
	return live
}

func reasoningTimelineMessageRunID(message chatMessageItem) int {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatReasoningTimelineRunIDMetadataKey].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func reasoningTimelineMessageSegment(message chatMessageItem) int {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatReasoningTimelineSegmentMetadataKey].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func reasoningTimelineMessageStartedAt(message chatMessageItem) int64 {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatReasoningTimelineStartedAtMetadataKey].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func reasoningTimelineDurationMS(message chatMessageItem) int64 {
	if len(message.Metadata) == 0 {
		return 0
	}
	switch value := message.Metadata[chatReasoningTimelineDurationMetadataKey].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func isManagedReasoningTimelineMessage(message chatMessageItem) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "reasoning") || len(message.Metadata) == 0 {
		return false
	}
	live, _ := message.Metadata[chatReasoningTimelineObjectMetadataKey].(bool)
	return live
}

func (p *ChatPage) currentThinkingTimelineIndex() int {
	if p == nil || len(p.timeline) == 0 || p.reasoningSegment <= 0 {
		return -1
	}
	for i := len(p.timeline) - 1; i >= 0; i-- {
		message := p.timeline[i]
		if !isManagedReasoningTimelineMessage(message) {
			continue
		}
		if reasoningTimelineMessageRunID(message) == p.runID && reasoningTimelineMessageSegment(message) == p.reasoningSegment {
			return i
		}
	}
	return -1
}

func (p *ChatPage) currentThinkingTimelineState() string {
	if idx := p.currentThinkingTimelineIndex(); idx >= 0 {
		if state := strings.ToLower(strings.TrimSpace(p.timeline[idx].ToolState)); state != "" {
			return state
		}
	}
	if p.reasoningActive {
		return "running"
	}
	if !p.thinkingCompletedAt.IsZero() {
		return "done"
	}
	return "running"
}

func (p *ChatPage) currentThinkingTimelineDurationMS() int64 {
	if p == nil {
		return 0
	}
	if idx := p.currentThinkingTimelineIndex(); idx >= 0 {
		if durationMS := reasoningTimelineDurationMS(p.timeline[idx]); durationMS > 0 {
			return durationMS
		}
		startedAt := reasoningTimelineMessageStartedAt(p.timeline[idx])
		if startedAt > 0 {
			endedAt := time.Now().UnixMilli()
			if !p.thinkingCompletedAt.IsZero() {
				endedAt = p.thinkingCompletedAt.UnixMilli()
			}
			if endedAt > startedAt {
				return endedAt - startedAt
			}
		}
	}
	if p.reasoningStartedAt.IsZero() {
		return 0
	}
	endedAt := time.Now()
	if !p.thinkingCompletedAt.IsZero() {
		endedAt = p.thinkingCompletedAt
	}
	durationMS := endedAt.Sub(p.reasoningStartedAt).Milliseconds()
	if durationMS < 0 {
		return 0
	}
	return durationMS
}

func (p *ChatPage) thinkingTimelineText(text string) string {
	text = canonicalThinkingText(text)
	switch {
	case text != "":
		return text
	case strings.TrimSpace(p.liveThinking) != "":
		return canonicalThinkingText(p.liveThinking)
	case strings.TrimSpace(p.thinkingSummary) != "":
		return canonicalThinkingText(p.thinkingSummary)
	default:
		return "Thinking"
	}
}

func (p *ChatPage) setThinkingTimelineMessage(text string, createdAt int64, state string, durationMS int64) {
	if p == nil || p.reasoningSegment <= 0 {
		return
	}
	text = p.thinkingTimelineText(text)
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		state = "running"
	}

	startedAt := createdAt
	if !p.reasoningStartedAt.IsZero() {
		startedAt = p.reasoningStartedAt.UnixMilli()
	}
	messageID := strings.TrimSpace(p.activeReasoningMessageID)
	if idx := p.currentThinkingTimelineIndex(); idx >= 0 {
		if existingStartedAt := reasoningTimelineMessageStartedAt(p.timeline[idx]); existingStartedAt > 0 {
			startedAt = existingStartedAt
		}
		if messageID == "" {
			messageID = strings.TrimSpace(p.timeline[idx].MessageID)
		}
	}

	p.bumpTimelineRenderGeneration()
	metadata := reasoningTimelineMetadata(p.runID, p.reasoningSegment, startedAt, durationMS)
	if idx := p.currentThinkingTimelineIndex(); idx >= 0 {
		p.timeline[idx].MessageID = messageID
		p.timeline[idx].Text = text
		p.timeline[idx].CreatedAt = createdAt
		p.timeline[idx].ToolState = state
		p.timeline[idx].Metadata = metadata
		return
	}

	p.timeline = append(p.timeline, chatMessageItem{
		MessageID: messageID,
		Role:      "reasoning",
		Text:      text,
		CreatedAt: createdAt,
		ToolState: state,
		Metadata:  metadata,
	})
	if len(p.timeline) > chatMaxTimelineMessages {
		drop := len(p.timeline) - chatMaxTimelineMessages
		p.timeline = append([]chatMessageItem(nil), p.timeline[drop:]...)
		p.resetTimelineRenderCache()
		return
	}
	p.ensureTimelineRenderCacheLen()
}

func (p *ChatPage) startReasoningSegment(createdAt int64) {
	if p == nil {
		return
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	if p.reasoningActive && p.currentThinkingTimelineIndex() >= 0 {
		return
	}
	p.reasoningSegment++
	p.reasoningActive = true
	p.thinkingCompletedAt = time.Time{}
	p.reasoningStartedAt = time.UnixMilli(createdAt)
	p.activeReasoningMessageID = ""
	p.liveThinking = ""
	p.setThinkingTimelineMessage("", createdAt, "running", 0)
}

func (p *ChatPage) updateThinkingTimelineMessage(text string, createdAt int64) {
	p.setThinkingTimelineMessage(text, createdAt, p.currentThinkingTimelineState(), p.currentThinkingTimelineDurationMS())
}

func (p *ChatPage) completeThinkingTimeline(state string, atUnix int64, text string) {
	if p == nil {
		return
	}
	if idx := p.currentThinkingTimelineIndex(); idx < 0 && strings.TrimSpace(text) == "" && strings.TrimSpace(p.liveThinking) == "" && strings.TrimSpace(p.thinkingSummary) == "" {
		return
	}
	if atUnix <= 0 {
		atUnix = time.Now().UnixMilli()
	}
	if p.thinkingCompletedAt.IsZero() {
		p.thinkingCompletedAt = time.UnixMilli(atUnix)
	}
	if current := p.currentThinkingTimelineState(); current == "error" && !strings.EqualFold(strings.TrimSpace(state), "error") {
		state = current
	}
	p.reasoningActive = false
	p.setThinkingTimelineMessage(text, atUnix, state, p.currentThinkingTimelineDurationMS())
}

func (p *ChatPage) applyOwnedReasoningMessage(messageID, text string, createdAt int64) {
	if p == nil {
		return
	}
	text = canonicalThinkingText(text)
	if strings.TrimSpace(text) == "" {
		return
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}

	messageID = strings.TrimSpace(messageID)
	currentID := strings.TrimSpace(p.activeReasoningMessageID)
	currentIdx := p.currentThinkingTimelineIndex()

	switch {
	case messageID == "":
		if !p.reasoningActive || currentIdx < 0 {
			p.startReasoningSegment(createdAt)
		}
	case currentID == "":
		if !p.reasoningActive || currentIdx < 0 {
			p.startReasoningSegment(createdAt)
		}
		p.activeReasoningMessageID = messageID
	case currentID != messageID:
		if p.reasoningActive || currentIdx >= 0 {
			p.completeThinkingTimeline("done", createdAt, "")
		}
		p.startReasoningSegment(createdAt)
		p.activeReasoningMessageID = messageID
	default:
		if !p.reasoningActive || currentIdx < 0 {
			p.startReasoningSegment(createdAt)
		}
	}

	p.liveThinking = text
	p.updateThinkingTimelineMessage(text, createdAt)
}

func (p *ChatPage) upsertReasoningMessage(summary string, createdAt int64) {
	summary = canonicalThinkingText(summary)
	if summary == "" {
		return
	}
	p.thinkingSummary = normalizeThinkingSummary(summary)
	if p.reasoningActive && p.currentThinkingTimelineIndex() >= 0 {
		p.completeThinkingTimeline("done", createdAt, summary)
		return
	}

	if !p.timelineHasReasoningSummary(summary) {
		p.timeline = append(p.timeline, chatMessageItem{
			Role:      "reasoning",
			Text:      summary,
			CreatedAt: createdAt,
			ToolState: "done",
		})
		if len(p.timeline) > chatMaxTimelineMessages {
			drop := len(p.timeline) - chatMaxTimelineMessages
			p.timeline = append([]chatMessageItem(nil), p.timeline[drop:]...)
			p.resetTimelineRenderCache()
			return
		}
		p.ensureTimelineRenderCacheLen()
		p.bumpTimelineRenderGeneration()
	}
}

func (p *ChatPage) timelineHasReasoningSummary(summary string) bool {
	summary = thinkingSummaryKey(summary)
	if summary == "" || len(p.timeline) == 0 {
		return false
	}
	checked := 0
	for i := len(p.timeline) - 1; i >= 0 && checked < 24; i-- {
		checked++
		item := p.timeline[i]
		if strings.ToLower(strings.TrimSpace(item.Role)) != "reasoning" {
			continue
		}
		if thinkingSummaryKey(item.Text) == summary {
			return true
		}
	}
	return false
}

func (p *ChatPage) appendSystemMessage(text string) {
	p.appendMessage("system", text, time.Now().UnixMilli())
}

// flushLiveAssistantToTimeline commits any accumulated live assistant text
// into the timeline so it flows above subsequent tool calls instead of staying
// pinned at the bottom of the view.
func (p *ChatPage) flushLiveAssistantToTimeline(atUnix int64) {
	text := strings.TrimSpace(p.liveAssistant)
	if text == "" {
		return
	}
	createdAt := atUnix
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	p.appendMessage("assistant", text, createdAt)
	p.liveAssistant = ""
}

func (p *ChatPage) appendToolMessage(entry chatToolStreamEntry, createdAt int64) {
	if isBashToolName(entry.ToolName) {
		p.bashOutput.Visible = false
		p.bashOutput.Expanded = false
		p.bashOutput.Scroll = 0
	}
	p.upsertManagedToolTimelineMessage(entry, createdAt)
}

func (p *ChatPage) addToolStreamEntry(entry chatToolStreamEntry) {
	if strings.TrimSpace(entry.ToolName) == "" {
		entry.ToolName = "tool"
	}
	if entry.CreatedAt <= 0 {
		entry.CreatedAt = time.Now().UnixMilli()
	}
	if strings.EqualFold(strings.TrimSpace(entry.State), "running") && entry.StartedAt <= 0 {
		entry.StartedAt = entry.CreatedAt
	}
	if strings.TrimSpace(entry.State) == "" {
		entry.State = "done"
	}
	p.toolStream = append(p.toolStream, entry)
	if len(p.toolStream) > chatMaxToolEntries {
		drop := len(p.toolStream) - chatMaxToolEntries
		p.toolStream = append([]chatToolStreamEntry(nil), p.toolStream[drop:]...)
	}
}

func historicalToolEntryKey(message ChatMessageRecord, entry chatToolStreamEntry) string {
	if messageID := strings.TrimSpace(message.ID); messageID != "" {
		return "message:" + messageID
	}
	if message.GlobalSeq > 0 {
		return fmt.Sprintf("seq:%d", message.GlobalSeq)
	}
	toolName := strings.ToLower(strings.TrimSpace(entry.ToolName))
	callID := strings.TrimSpace(entry.CallID)
	return fmt.Sprintf("history:%d:%s:%s", message.CreatedAt, toolName, callID)
}

func toolStreamEntryKey(entry chatToolStreamEntry) string {
	if entryKey := strings.TrimSpace(entry.EntryKey); entryKey != "" {
		return entryKey
	}
	return streamToolKey(entry.CallID, entry.ToolName)
}

func toolReplayDedupKey(entry chatToolStreamEntry) string {
	if strings.TrimSpace(entry.CallID) != "" {
		return streamToolKey(entry.CallID, entry.ToolName)
	}
	return toolStreamEntryKey(entry)
}

func (p *ChatPage) latestToolStreamEntry(callID, toolName string) chatToolStreamEntry {
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(toolName)
	for i := len(p.toolStream) - 1; i >= 0; i-- {
		entry := p.toolStream[i]
		if callID != "" && strings.TrimSpace(entry.CallID) == callID {
			return entry
		}
	}
	for i := len(p.toolStream) - 1; i >= 0; i-- {
		entry := p.toolStream[i]
		if toolName != "" && strings.EqualFold(strings.TrimSpace(entry.ToolName), toolName) {
			return entry
		}
	}
	return chatToolStreamEntry{
		ToolName: toolName,
		CallID:   callID,
	}
}

func (p *ChatPage) latestToolStreamEntryForEntry(entry chatToolStreamEntry) chatToolStreamEntry {
	targetKey := toolStreamEntryKey(entry)
	if targetKey != "" {
		for i := len(p.toolStream) - 1; i >= 0; i-- {
			if toolStreamEntryKey(p.toolStream[i]) != targetKey {
				continue
			}
			return p.toolStream[i]
		}
	}
	return p.latestToolStreamEntry(entry.CallID, entry.ToolName)
}

func (p *ChatPage) upsertToolStreamEntry(entry chatToolStreamEntry) {
	if strings.TrimSpace(entry.ToolName) == "" {
		entry.ToolName = "tool"
	}
	if entry.CreatedAt <= 0 {
		entry.CreatedAt = time.Now().UnixMilli()
	}
	if strings.TrimSpace(entry.State) == "" {
		entry.State = "done"
	}
	state := strings.ToLower(strings.TrimSpace(entry.State))
	if state == "" {
		state = "done"
	}
	providedStartedAt := entry.StartedAt
	targetEntryKey := strings.TrimSpace(entry.EntryKey)

	if targetEntryKey != "" {
		for i := len(p.toolStream) - 1; i >= 0; i-- {
			if toolStreamEntryKey(p.toolStream[i]) != targetEntryKey {
				continue
			}
			if strings.TrimSpace(entry.ToolName) != "" {
				p.toolStream[i].ToolName = entry.ToolName
			}
			if strings.TrimSpace(entry.Output) != "" {
				p.toolStream[i].Output = mergeToolStreamText(p.toolStream[i].Output, entry.Output)
			}
			if strings.TrimSpace(entry.Raw) != "" {
				p.toolStream[i].Raw = mergeToolStreamText(p.toolStream[i].Raw, entry.Raw)
			}
			if strings.TrimSpace(entry.StartedArguments) != "" {
				p.toolStream[i].StartedArguments = strings.TrimSpace(entry.StartedArguments)
			}
			if entry.StartedArgsAreJSON {
				p.toolStream[i].StartedArgsAreJSON = true
			}
			incomingError := strings.TrimSpace(entry.Error)
			if incomingError != "" || strings.TrimSpace(p.toolStream[i].Error) == "" {
				p.toolStream[i].Error = incomingError
			}
			if strings.TrimSpace(entry.State) != "" {
				if strings.EqualFold(strings.TrimSpace(p.toolStream[i].State), "error") &&
					!strings.EqualFold(strings.TrimSpace(entry.State), "error") &&
					incomingError == "" {
				} else {
					p.toolStream[i].State = entry.State
				}
			}
			p.toolStream[i].CreatedAt = entry.CreatedAt
			p.toolStream[i].EntryKey = targetEntryKey

			switch state {
			case "running":
				if p.toolStream[i].StartedAt <= 0 {
					if providedStartedAt > 0 {
						p.toolStream[i].StartedAt = providedStartedAt
					} else {
						p.toolStream[i].StartedAt = entry.CreatedAt
					}
				}
				p.toolStream[i].DurationMS = 0
			default:
				if p.toolStream[i].StartedAt <= 0 {
					switch {
					case providedStartedAt > 0:
						p.toolStream[i].StartedAt = providedStartedAt
					case entry.DurationMS > 0:
						startedAt := entry.CreatedAt - entry.DurationMS
						if startedAt < 0 {
							startedAt = 0
						}
						p.toolStream[i].StartedAt = startedAt
					default:
						p.toolStream[i].StartedAt = entry.CreatedAt
					}
				}
				if entry.DurationMS > 0 {
					p.toolStream[i].DurationMS = entry.DurationMS
				} else if p.toolStream[i].DurationMS <= 0 &&
					p.toolStream[i].StartedAt > 0 &&
					p.toolStream[i].CreatedAt > p.toolStream[i].StartedAt {
					p.toolStream[i].DurationMS = p.toolStream[i].CreatedAt - p.toolStream[i].StartedAt
				}
			}
			return
		}
	}

	targetCallID := strings.TrimSpace(entry.CallID)
	if targetEntryKey == "" && targetCallID != "" {
		for i := len(p.toolStream) - 1; i >= 0; i-- {
			if strings.TrimSpace(p.toolStream[i].CallID) != targetCallID {
				continue
			}
			if strings.TrimSpace(entry.ToolName) != "" {
				p.toolStream[i].ToolName = entry.ToolName
			}
			if strings.TrimSpace(entry.Output) != "" {
				p.toolStream[i].Output = mergeToolStreamText(p.toolStream[i].Output, entry.Output)
			}
			if strings.TrimSpace(entry.Raw) != "" {
				p.toolStream[i].Raw = mergeToolStreamText(p.toolStream[i].Raw, entry.Raw)
			}
			if strings.TrimSpace(entry.StartedArguments) != "" {
				p.toolStream[i].StartedArguments = strings.TrimSpace(entry.StartedArguments)
			}
			if entry.StartedArgsAreJSON {
				p.toolStream[i].StartedArgsAreJSON = true
			}
			incomingError := strings.TrimSpace(entry.Error)
			if incomingError != "" || strings.TrimSpace(p.toolStream[i].Error) == "" {
				p.toolStream[i].Error = incomingError
			}
			if strings.TrimSpace(entry.State) != "" {
				if strings.EqualFold(strings.TrimSpace(p.toolStream[i].State), "error") &&
					!strings.EqualFold(strings.TrimSpace(entry.State), "error") &&
					incomingError == "" {
					// Keep terminal error state when later compact history entries omit error details.
				} else {
					p.toolStream[i].State = entry.State
				}
			}
			p.toolStream[i].CreatedAt = entry.CreatedAt

			switch state {
			case "running":
				if p.toolStream[i].StartedAt <= 0 {
					if providedStartedAt > 0 {
						p.toolStream[i].StartedAt = providedStartedAt
					} else {
						p.toolStream[i].StartedAt = entry.CreatedAt
					}
				}
				p.toolStream[i].DurationMS = 0
			default:
				if p.toolStream[i].StartedAt <= 0 {
					switch {
					case providedStartedAt > 0:
						p.toolStream[i].StartedAt = providedStartedAt
					case entry.DurationMS > 0:
						startedAt := entry.CreatedAt - entry.DurationMS
						if startedAt < 0 {
							startedAt = 0
						}
						p.toolStream[i].StartedAt = startedAt
					default:
						p.toolStream[i].StartedAt = entry.CreatedAt
					}
				}
				if entry.DurationMS > 0 {
					p.toolStream[i].DurationMS = entry.DurationMS
				} else if p.toolStream[i].DurationMS <= 0 &&
					p.toolStream[i].StartedAt > 0 &&
					p.toolStream[i].CreatedAt > p.toolStream[i].StartedAt {
					p.toolStream[i].DurationMS = p.toolStream[i].CreatedAt - p.toolStream[i].StartedAt
				}
			}
			return
		}
	}

	if entry.StartedAt <= 0 {
		switch {
		case entry.DurationMS > 0:
			startedAt := entry.CreatedAt - entry.DurationMS
			if startedAt < 0 {
				startedAt = 0
			}
			entry.StartedAt = startedAt
		default:
			entry.StartedAt = entry.CreatedAt
		}
	}
	if entry.StartedArguments = strings.TrimSpace(entry.StartedArguments); entry.StartedArguments != "" {
		entry.StartedArgsAreJSON = entry.StartedArgsAreJSON || parseToolJSON(entry.StartedArguments) != nil
	}
	if state != "running" && entry.DurationMS <= 0 && entry.StartedAt > 0 && entry.CreatedAt > entry.StartedAt {
		entry.DurationMS = entry.CreatedAt - entry.StartedAt
	}
	p.addToolStreamEntry(entry)
}

func mergeToolStreamText(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	switch {
	case incoming == "":
		return existing
	case existing == "":
		return incoming
	}

	mergedJSON := mergeStructuredTaskToolPayloads(existing, incoming)
	if mergedJSON != "" {
		return mergedJSON
	}

	// Stored tool history messages are compact UI summaries and may already be
	// truncated. Keep richer stream payload text when both are available.
	if isCompactToolHistoryText(incoming) && !isCompactToolHistoryText(existing) {
		return existing
	}
	if toolTextLooksTruncated(existing) && !toolTextLooksTruncated(incoming) {
		return incoming
	}
	if utf8.RuneCountInString(incoming) > utf8.RuneCountInString(existing) {
		return incoming
	}
	return existing
}

func mergeStructuredTaskToolPayloads(existing, incoming string) string {
	existingPayload := parseToolJSON(existing)
	incomingPayload := parseToolJSON(incoming)
	if existingPayload == nil || incomingPayload == nil {
		return ""
	}
	if !isTaskToolPayload(existingPayload) || !isTaskToolPayload(incomingPayload) {
		return ""
	}
	existingLaunches := jsonObjectSlice(existingPayload, "launches")
	incomingLaunches := jsonObjectSlice(incomingPayload, "launches")
	if len(existingLaunches) == 0 && len(incomingLaunches) == 0 {
		return ""
	}
	merged := cloneGenericJSONMap(existingPayload)
	for key, value := range incomingPayload {
		merged[key] = value
	}
	merged["launches"] = mergeTaskLaunchPayloads(existingLaunches, incomingLaunches)
	if launchCount := maxInt(len(jsonObjectSlice(merged, "launches")), jsonInt(merged, "launch_count")); launchCount > 0 {
		merged["launch_count"] = launchCount
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func mergeTaskLaunchPayloads(existing, incoming []map[string]any) []map[string]any {
	if len(existing) == 0 {
		merged := cloneGenericJSONSlice(incoming)
		sortTaskLaunchPayloads(merged)
		return merged
	}
	if len(incoming) == 0 {
		merged := cloneGenericJSONSlice(existing)
		sortTaskLaunchPayloads(merged)
		return merged
	}
	merged := cloneGenericJSONSlice(existing)
	indexByLaunch := map[int]int{}
	for i, launch := range merged {
		idx := jsonInt(launch, "launch_index")
		if idx > 0 {
			indexByLaunch[idx] = i
		}
	}
	for _, launch := range incoming {
		idx := jsonInt(launch, "launch_index")
		if idx > 0 {
			if pos, ok := indexByLaunch[idx]; ok {
				row := cloneGenericJSONMap(merged[pos])
				for key, value := range launch {
					if key == "current_tool" && strings.TrimSpace(jsonString(launch, key)) == "" {
						if history := appendTaskToolHistory(nil, jsonStringSlice(row, "tool_order"), strings.TrimSpace(jsonString(row, "current_tool"))); len(history) > 0 {
							row["tool_order"] = history
						}
						continue
					}
					row[key] = value
				}
				if history := appendTaskToolHistory(jsonStringSlice(row, "tool_order"), jsonStringSlice(launch, "tool_order"), strings.TrimSpace(jsonString(launch, "current_tool"))); len(history) > 0 {
					row["tool_order"] = history
				}
				merged[pos] = row
				continue
			}
			indexByLaunch[idx] = len(merged)
		}
		row := cloneGenericJSONMap(launch)
		if history := appendTaskToolHistory(nil, jsonStringSlice(row, "tool_order"), strings.TrimSpace(jsonString(row, "current_tool"))); len(history) > 0 {
			row["tool_order"] = history
		}
		merged = append(merged, row)
	}
	sortTaskLaunchPayloads(merged)
	return merged
}

func sortTaskLaunchPayloads(launches []map[string]any) {
	if len(launches) < 2 {
		return
	}
	sort.SliceStable(launches, func(i, j int) bool {
		left := jsonInt(launches[i], "launch_index")
		right := jsonInt(launches[j], "launch_index")
		switch {
		case left <= 0 && right <= 0:
			return i < j
		case left <= 0:
			return false
		case right <= 0:
			return true
		default:
			return left < right
		}
	})
}

func appendTaskToolHistory(base []string, extras []string, current string) []string {
	groups := make([][]string, 0, 3)
	if len(base) > 0 {
		groups = append(groups, base)
	}
	if len(extras) > 0 {
		groups = append(groups, extras)
	}
	if current = strings.TrimSpace(current); current != "" {
		groups = append(groups, []string{current})
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isTaskToolPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	pathID := strings.TrimSpace(jsonString(payload, "path_id"))
	return pathID == "tool.task.stream.v1" || pathID == "tool.task.v1"
}

func cloneGenericJSONMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneGenericJSONValue(value)
	}
	return out
}

func cloneGenericJSONSlice(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, cloneGenericJSONMap(item))
	}
	return out
}

func cloneGenericJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneGenericJSONMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneGenericJSONValue(item))
		}
		return out
	default:
		return typed
	}
}

func isCompactToolHistoryText(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "tool=") && strings.Contains(value, " output=")
}

func toolTextLooksTruncated(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, "...[history-truncated]") || strings.Contains(value, "...[truncated") {
		return true
	}
	return strings.HasSuffix(value, "...") || strings.Contains(value, "...")
}

func (p *ChatPage) appendToolDelta(callID, toolName, chunk string, createdAt int64) {
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(toolName)
	if chunk == "" {
		return
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}

	if callID != "" {
		for i := len(p.toolStream) - 1; i >= 0; i-- {
			if strings.TrimSpace(p.toolStream[i].CallID) != callID {
				continue
			}
			if toolName != "" {
				p.toolStream[i].ToolName = toolName
			}
			p.toolStream[i].CreatedAt = createdAt
			if strings.TrimSpace(p.toolStream[i].State) == "" || strings.EqualFold(strings.TrimSpace(p.toolStream[i].State), "pending") {
				p.toolStream[i].State = "running"
			}
			if p.toolStream[i].StartedAt <= 0 {
				p.toolStream[i].StartedAt = createdAt
			}
			p.toolStream[i].DurationMS = 0
			if shouldReplaceToolDeltaOutput(emptyValue(toolName, p.toolStream[i].ToolName), chunk) {
				merged := mergeToolStreamText(p.toolStream[i].Output, chunk)
				if strings.TrimSpace(merged) == "" {
					merged = chunk
				}
				p.toolStream[i].Output = merged
				p.toolStream[i].Raw = merged
			} else {
				maxRunes := chatMaxLiveToolOutputRunes
				if isBashToolName(emptyValue(toolName, p.toolStream[i].ToolName)) {
					maxRunes = chatMaxBashTimelinePreviewRunes
				}
				p.toolStream[i].Output = appendToolDeltaText(p.toolStream[i].Output, chunk, maxRunes)
				p.toolStream[i].Raw = appendToolDeltaText(p.toolStream[i].Raw, chunk, maxRunes)
			}
			return
		}
	}

	p.addToolStreamEntry(chatToolStreamEntry{
		ToolName:   emptyValue(toolName, "tool"),
		CallID:     callID,
		Output:     chunk,
		Raw:        chunk,
		State:      "running",
		CreatedAt:  createdAt,
		StartedAt:  createdAt,
		DurationMS: 0,
	})
}

func appendToolDeltaText(current, chunk string, maxRunes int) string {
	if chunk == "" {
		return current
	}
	merged := chunk
	if current != "" {
		if strings.HasSuffix(current, "\n") || strings.HasPrefix(chunk, "\n") {
			merged = current + chunk
		} else {
			merged = current + "\n" + chunk
		}
	}
	if maxRunes <= 0 {
		return merged
	}
	runes := []rune(merged)
	if len(runes) <= maxRunes {
		return merged
	}
	return "..." + string(runes[len(runes)-maxRunes:])
}

func isBashToolName(toolName string) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "bash")
}

func bashCommandFromEntry(entry chatToolStreamEntry) string {
	for _, candidate := range []string{entry.StartedArguments, entry.Raw, entry.Output} {
		payload := parseToolJSON(strings.TrimSpace(candidate))
		if payload == nil {
			continue
		}
		if command := strings.TrimSpace(jsonString(payload, "command")); command != "" {
			return sanitizeCommandSnippetPreview(command)
		}
	}
	return ""
}

func (p *ChatPage) bashOutputAvailable() bool {
	if p == nil {
		return false
	}
	if !isBashToolName(p.bashOutput.ToolName) {
		return false
	}
	return strings.TrimSpace(p.bashOutput.Output) != "" || strings.TrimSpace(p.bashOutput.Command) != "" || p.bashOutput.Running
}

func (p *ChatPage) bashOutputVisible() bool {
	if p == nil {
		return false
	}
	if !p.bashOutput.Visible {
		return false
	}
	return p.bashOutputAvailable()
}

func (p *ChatPage) inlineBashOutputHeight(width, available int) int {
	if p == nil || !p.bashOutputVisible() {
		return 0
	}
	if width < 36 || available < 6 {
		return 0
	}
	if p.bashOutput.Expanded {
		height := maxInt(6, available/2)
		if available >= 14 {
			height = maxInt(height, 8)
		}
		return minInt(available-1, height)
	}
	if available < 7 {
		return 0
	}
	return minInt(available-1, 7)
}

func (p *ChatPage) maybeStartInlineBashOutput(entry chatToolStreamEntry) {
	if p == nil || !isBashToolName(entry.ToolName) {
		return
	}
	p.bashOutput.ToolName = strings.TrimSpace(entry.ToolName)
	p.bashOutput.CallID = strings.TrimSpace(entry.CallID)
	p.bashOutput.Command = bashCommandFromEntry(entry)
	p.bashOutput.UpdatedAt = entry.CreatedAt
	p.bashOutput.Running = !isTerminalToolState(strings.TrimSpace(entry.State))
	if strings.TrimSpace(entry.Output) != "" && strings.TrimSpace(p.bashOutput.Output) == "" {
		p.bashOutput.Output = appendToolDeltaText("", entry.Output, 0)
	}
	p.bashOutput.Truncated = strings.HasPrefix(p.bashOutput.Output, "...")
	if !p.bashOutput.Expanded {
		p.bashOutput.Scroll = 0
	}
	if !p.bashOutput.Running {
		p.bashOutput.LastTimelineRefresh = entry.CreatedAt
		p.bashOutput.LastPreviewText = strings.Join(toolPreviewLines(entry, chatToolPreviewMaxRunes, toolPreviewLineLimit(entry)), "\n")
	}
}

func (p *ChatPage) updateInlineBashOutput(entry chatToolStreamEntry, chunk string, createdAt int64) {
	if p == nil || !isBashToolName(entry.ToolName) {
		return
	}
	p.maybeStartInlineBashOutput(entry)
	if strings.TrimSpace(chunk) != "" {
		p.bashOutput.Output = appendToolDeltaText(p.bashOutput.Output, chunk, 0)
	}
	if createdAt > 0 {
		p.bashOutput.UpdatedAt = createdAt
	}
	p.bashOutput.Running = true
	p.bashOutput.Truncated = strings.HasPrefix(p.bashOutput.Output, "...")
	if !p.bashOutput.Expanded {
		p.bashOutput.Scroll = 0
	}
}

func mergeBashSessionOutput(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	switch {
	case incoming == "":
		return existing
	case existing == "":
		return incoming
	}
	if toolTextLooksTruncated(existing) && !toolTextLooksTruncated(incoming) {
		return incoming
	}
	if toolTextLooksTruncated(incoming) && !toolTextLooksTruncated(existing) {
		return existing
	}
	if utf8.RuneCountInString(incoming) > utf8.RuneCountInString(existing) {
		return incoming
	}
	return existing
}

func preferredBashOutputText(entry chatToolStreamEntry) string {
	return strings.TrimSpace(preferredStructuredToolText(entry.ToolName, entry.Output, entry.Raw))
}

func (p *ChatPage) restoreInlineBashOutput(entry chatToolStreamEntry) {
	if p == nil || !isBashToolName(entry.ToolName) {
		return
	}
	p.bashOutput.ToolName = strings.TrimSpace(entry.ToolName)
	p.bashOutput.CallID = strings.TrimSpace(entry.CallID)
	p.bashOutput.Command = bashCommandFromEntry(entry)
	p.bashOutput.Output = preferredBashOutputText(entry)
	p.bashOutput.UpdatedAt = entry.CreatedAt
	p.bashOutput.Running = !isTerminalToolState(strings.TrimSpace(entry.State))
	p.bashOutput.Truncated = strings.HasPrefix(p.bashOutput.Output, "...")
	p.bashOutput.LastTimelineRefresh = entry.CreatedAt
	p.bashOutput.LastPreviewText = strings.Join(toolPreviewLines(entry, chatToolPreviewMaxRunes, toolPreviewLineLimit(entry)), "\n")
	if !p.bashOutput.Expanded {
		p.bashOutput.Scroll = 0
	}
}

func (p *ChatPage) shouldRefreshManagedToolTimeline(entry chatToolStreamEntry, chunk string, createdAt int64) bool {
	if p == nil {
		return false
	}
	if !isBashToolName(entry.ToolName) {
		return true
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	trimmedChunk := strings.TrimSpace(chunk)
	forcePreviewCheck := p.bashOutput.LastTimelineRefresh <= 0 ||
		createdAt-p.bashOutput.LastTimelineRefresh >= chatBashTimelineRefreshMS ||
		strings.Contains(chunk, "\n") ||
		strings.Contains(chunk, "\r")
	if !forcePreviewCheck {
		return false
	}
	preview := strings.Join(toolPreviewLines(entry, chatToolPreviewMaxRunes, toolPreviewLineLimit(entry)), "\n")
	if preview == p.bashOutput.LastPreviewText {
		return false
	}
	if preview == "" && trimmedChunk == "" {
		return false
	}
	p.bashOutput.LastPreviewText = preview
	p.bashOutput.LastTimelineRefresh = createdAt
	return true
}

func (p *ChatPage) finishInlineBashOutput(entry chatToolStreamEntry) {
	if p == nil || !isBashToolName(entry.ToolName) {
		return
	}
	p.maybeStartInlineBashOutput(entry)
	if preferred := preferredBashOutputText(entry); preferred != "" {
		p.bashOutput.Output = mergeBashSessionOutput(p.bashOutput.Output, preferred)
	}
	p.bashOutput.UpdatedAt = entry.CreatedAt
	p.bashOutput.Running = !isTerminalToolState(strings.TrimSpace(entry.State))
	p.bashOutput.Truncated = strings.HasPrefix(p.bashOutput.Output, "...")
	p.bashOutput.LastTimelineRefresh = entry.CreatedAt
	p.bashOutput.LastPreviewText = strings.Join(toolPreviewLines(entry, chatToolPreviewMaxRunes, toolPreviewLineLimit(entry)), "\n")
	if !p.bashOutput.Expanded {
		p.bashOutput.Scroll = 0
	}
}

func (p *ChatPage) toggleInlineBashOutputExpanded() bool {
	if p == nil || !p.bashOutputAvailable() {
		return false
	}
	if !p.bashOutput.Visible {
		p.bashOutput.Visible = true
		p.bashOutput.Expanded = true
		p.bashOutput.Scroll = 0
		p.statusLine = "full bash output"
		return true
	}
	p.bashOutput.Expanded = !p.bashOutput.Expanded
	if !p.bashOutput.Expanded {
		p.bashOutput.Visible = false
		p.bashOutput.Scroll = 0
		p.statusLine = "bash output closed"
		return true
	}
	p.bashOutput.Scroll = 0
	p.statusLine = "full bash output"
	return true
}

func (p *ChatPage) scrollBashOutput(step int) {
	if p == nil || !p.bashOutputVisible() || !p.bashOutput.Expanded || step == 0 {
		return
	}
	p.bashOutput.Scroll = maxInt(0, p.bashOutput.Scroll+step)
}

func (p *ChatPage) bashOutputViewportLines(width, height int) []string {
	if p == nil || width <= 0 || height <= 0 {
		return nil
	}
	lines := Wrap(strings.TrimSpace(p.bashOutput.Output), width)
	if len(lines) == 0 {
		lines = []string{""}
	}
	maxScroll := maxInt(0, len(lines)-height)
	if p.bashOutput.Scroll > maxScroll {
		p.bashOutput.Scroll = maxScroll
	}
	start := maxInt(0, len(lines)-height-p.bashOutput.Scroll)
	end := minInt(len(lines), start+height)
	return lines[start:end]
}

func (p *ChatPage) drawBashOutputComponent(s tcell.Screen, rect Rect) {
	if p == nil || rect.W < 8 || rect.H < 3 || !p.bashOutputVisible() {
		return
	}
	DrawBox(s, rect, p.theme.Border)
	innerW := rect.W - 4
	if innerW < 1 {
		innerW = 1
	}
	header := "bash output"
	if p.bashOutput.Expanded {
		header = "bash output · full"
	} else {
		header = "bash output"
	}
	if p.bashOutput.Running {
		header += " · running"
	}
	DrawText(s, rect.X+2, rect.Y+1, innerW, p.theme.TextMuted, clampEllipsis(header, innerW))
	bodyTop := rect.Y + 2
	bodyBottom := rect.Y + rect.H - 2
	if bodyBottom < bodyTop {
		bodyBottom = bodyTop
	}
	if rect.H >= 5 {
		hint := "write /output to open full output"
		if p.bashOutput.Expanded {
			hint = "/output closes viewer · PgUp/PgDn scroll · Esc closes"
		}
		DrawText(s, rect.X+2, rect.Y+rect.H-2, innerW, p.theme.TextMuted, clampEllipsis(hint, innerW))
		bodyBottom = rect.Y + rect.H - 3
	}
	rowY := bodyTop
	if command := strings.TrimSpace(p.bashOutput.Command); command != "" && rowY <= bodyBottom {
		for _, line := range wrapWithCustomPrefixes("$ ", "  ", command, innerW) {
			if rowY > bodyBottom {
				break
			}
			DrawText(s, rect.X+2, rowY, innerW, p.theme.Accent, line)
			rowY++
		}
	}
	if rowY > bodyBottom {
		return
	}
	for _, line := range p.bashOutputViewportLines(innerW, bodyBottom-rowY+1) {
		if rowY > bodyBottom {
			break
		}
		DrawText(s, rect.X+2, rowY, innerW, p.theme.Text, line)
		rowY++
	}
}

func shouldReplaceToolDeltaOutput(toolName, chunk string) bool {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return false
	}
	if toolName != "task" {
		return false
	}
	payload := parseToolJSON(chunk)
	if payload == nil {
		return false
	}
	pathID := strings.TrimSpace(jsonString(payload, "path_id"))
	return pathID == "tool.task.stream.v1" || pathID == "tool.task.v1"
}

func (p *ChatPage) scrollTimeline(step int) {
	if step == 0 {
		return
	}
	p.timelineScroll += step
	if p.timelineScroll < 0 {
		p.timelineScroll = 0
	}
}

func (p *ChatPage) cycleAssistantVariant(step int) {
	if step == 0 {
		return
	}
	if chatAssistantVariantCount <= 0 {
		p.assistantVariant = 0
		return
	}
	next := p.assistantVariant + step
	for next < 0 {
		next += chatAssistantVariantCount
	}
	p.assistantVariant = next % chatAssistantVariantCount
	p.bumpTimelineRenderGeneration()
	p.statusLine = fmt.Sprintf(
		"assistant view %d/%d: %s",
		p.assistantVariant+1,
		chatAssistantVariantCount,
		p.assistantVariantName(),
	)
}

func (p *ChatPage) cycleUserVariant(step int) {
	if step == 0 {
		return
	}
	if chatUserVariantCount <= 0 {
		p.userVariant = 0
		return
	}
	next := p.userVariant + step
	for next < 0 {
		next += chatUserVariantCount
	}
	p.userVariant = next % chatUserVariantCount
	p.bumpTimelineRenderGeneration()
	p.statusLine = fmt.Sprintf(
		"user view %d/%d: %s",
		p.userVariant+1,
		chatUserVariantCount,
		p.userVariantName(),
	)
}

func (p *ChatPage) spinnerFrame() string {
	if len(chatSpinnerFrames) == 0 {
		return "."
	}
	return chatSpinnerFrames[p.frameTick%len(chatSpinnerFrames)]
}

func (p *ChatPage) isCodexModel() bool {
	return strings.EqualFold(strings.TrimSpace(p.modelProvider), "codex")
}

func (p *ChatPage) defaultThinkingSummary(prompt string) string {
	_ = prompt
	return ""
}

func (p *ChatPage) effectiveRunActive() bool {
	if p == nil {
		return false
	}
	if p.lifecycle != nil {
		return p.lifecycle.Active
	}
	return p.busy
}

func (p *ChatPage) effectiveRunStarted() time.Time {
	if p == nil {
		return time.Time{}
	}
	if p.lifecycle != nil && p.lifecycle.Active && p.lifecycle.StartedAt > 0 {
		return time.UnixMilli(p.lifecycle.StartedAt)
	}
	return p.runStarted
}

func (p *ChatPage) runElapsedLabel() string {
	started := p.effectiveRunStarted()
	if started.IsZero() {
		return "0ms"
	}
	return formatDurationCompact(time.Since(started))
}

func (p *ChatPage) friendlyRunError(err error) string {
	if err == nil {
		return "run failed"
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "run failed"
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "manual compact failed") && strings.Contains(lower, "context deadline exceeded"):
		return "Run failed: " + text
	case strings.Contains(lower, "manual compact failed") && strings.Contains(lower, "context canceled"):
		return "Context compaction canceled."
	case strings.Contains(lower, "resolved model provider is empty"):
		return "No active model is configured. Open /models, set a provider/model, then retry."
	case strings.Contains(lower, "auth") || strings.Contains(lower, "unauthorized"):
		return "Auth is missing or invalid. Run /auth, then retry."
	case strings.Contains(lower, "unsupported provider") || strings.Contains(lower, "not runnable yet"):
		return "Selected provider is not runnable yet. Open /models, choose a supported model, then retry."
	default:
		return "Run failed: " + text
	}
}

func isCancelledRunError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lower, "context canceled") || strings.Contains(lower, "context cancelled")
}

func formatDurationCompact(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func chatSessionTitleFromPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "New Session"
	}
	return clampEllipsis(trimmed, 52)
}

func chatFormatTokenCount(v int) string {
	if v >= 1_000_000 {
		return fmt.Sprintf("%.1fm", float64(v)/1_000_000)
	}
	if v >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(v)/1_000)
	}
	return fmt.Sprintf("%d", v)
}

func chatTokenEstimateFromText(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := utf8.RuneCountInString(trimmed)
	if runes <= 0 {
		return 0
	}
	return (runes + 3) / 4
}

func (p *ChatPage) desiredComposerHeight(width int) int {
	if width < 10 {
		return 1
	}
	innerW := width - 2
	if innerW <= 1 {
		innerW = 1
	}
	contentLines := len(wrapWithCustomPrefixes("› ", "", p.input, innerW))
	if contentLines < 1 {
		contentLines = 1
	}
	height := contentLines + 2
	if height < 3 {
		height = 3
	}
	return height
}

func (p *ChatPage) maybeWarnLargeInput(before, after string) {
	beforeTokens := chatTokenEstimateFromText(before)
	afterTokens := chatTokenEstimateFromText(after)
	if beforeTokens >= chatHugePromptWarnTokens || afterTokens < chatHugePromptWarnTokens {
		return
	}
	message := fmt.Sprintf("Large prompt (~%s tokens). Consider trimming to fit context.", chatFormatTokenCount(afterTokens))
	p.ShowToast(ToastWarning, message)
}

func (p *ChatPage) canSubmitPrompt(prompt string) bool {
	promptTokens := chatTokenEstimateFromText(prompt)
	if promptTokens >= chatHugePromptWarnTokens {
		p.ShowToast(ToastWarning, fmt.Sprintf("Submitting a large prompt (~%s tokens).", chatFormatTokenCount(promptTokens)))
	}
	return true
}

type normalizedChatToolStreamStyle struct {
	ShowAnchor    bool
	PulseFrames   []string
	RunningSymbol string
	SuccessSymbol string
	ErrorSymbol   string
}

func normalizeChatToolStreamStyle(style ChatToolStreamStyle) normalizedChatToolStreamStyle {
	out := normalizedChatToolStreamStyle{
		ShowAnchor:    true,
		PulseFrames:   append([]string(nil), chatPulseDotFrames...),
		RunningSymbol: chatDefaultToolRunningSymbol,
		SuccessSymbol: chatDefaultToolSuccessSymbol,
		ErrorSymbol:   chatDefaultToolErrorSymbol,
	}
	if style.ShowAnchor != nil {
		out.ShowAnchor = *style.ShowAnchor
	}
	if frames := sanitizePulseFrames(style.PulseFrames); len(frames) > 0 {
		out.PulseFrames = frames
	}
	if symbol := strings.TrimSpace(style.RunningSymbol); symbol != "" {
		out.RunningSymbol = symbol
	}
	if symbol := strings.TrimSpace(style.SuccessSymbol); symbol != "" {
		out.SuccessSymbol = symbol
	}
	if symbol := strings.TrimSpace(style.ErrorSymbol); symbol != "" {
		out.ErrorSymbol = symbol
	}
	return out
}

func sanitizePulseFrames(frames []string) []string {
	if len(frames) == 0 {
		return nil
	}
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		if utf8.RuneCountInString(frame) > 4 {
			continue
		}
		out = append(out, frame)
		if len(out) >= 12 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeChatSessionTabs(tabs []ChatSessionTab, currentID, currentTitle string) []ChatSessionTab {
	next := make([]ChatSessionTab, 0, len(tabs)+1)
	seen := make(map[string]struct{}, len(tabs)+1)

	for _, tab := range tabs {
		id := strings.TrimSpace(tab.ID)
		title := strings.TrimSpace(tab.Title)
		if id == "" && title == "" {
			continue
		}
		if id == "" {
			id = title
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if title == "" {
			title = id
		}
		next = append(next, ChatSessionTab{
			ID:              id,
			Title:           title,
			WorkspaceName:   strings.TrimSpace(tab.WorkspaceName),
			WorkspacePath:   strings.TrimSpace(tab.WorkspacePath),
			Mode:            strings.TrimSpace(tab.Mode),
			UpdatedAgo:      strings.TrimSpace(tab.UpdatedAgo),
			Provider:        strings.TrimSpace(tab.Provider),
			ModelName:       strings.TrimSpace(tab.ModelName),
			ServiceTier:     strings.TrimSpace(tab.ServiceTier),
			ContextMode:     strings.TrimSpace(tab.ContextMode),
			Background:      tab.Background,
			ParentSessionID: strings.TrimSpace(tab.ParentSessionID),
			LineageKind:     strings.TrimSpace(tab.LineageKind),
			LineageLabel:    normalizeSessionLineageLabel(tab.LineageLabel),
			TargetKind:      strings.TrimSpace(tab.TargetKind),
			TargetName:      strings.TrimSpace(tab.TargetName),
			Depth:           tab.Depth,
		})
	}

	currentID = strings.TrimSpace(currentID)
	currentTitle = strings.TrimSpace(currentTitle)
	if currentID == "" {
		currentID = currentTitle
	}
	if currentTitle == "" {
		currentTitle = currentID
	}
	if currentID != "" {
		if _, ok := seen[currentID]; !ok {
			next = append([]ChatSessionTab{{
				ID:    currentID,
				Title: currentTitle,
			}}, next...)
		}
	}

	return next
}

func normalizeSessionMode(mode string) string {
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

func nextSessionMode(current string) string {
	switch normalizeSessionMode(current) {
	case "plan":
		return "auto"
	case "auto":
		return "plan"
	default:
		return normalizeSessionMode(current)
	}
}

func currentDisplayedSessionMode(page *ChatPage) string {
	if page == nil {
		return "plan"
	}
	return chatDisplayedMode(page.meta, page.sessionMode)
}

func filterPendingPermissions(records []ChatPermissionRecord) []ChatPermissionRecord {
	out := make([]ChatPermissionRecord, 0, len(records))
	for _, record := range records {
		if strings.ToLower(strings.TrimSpace(record.Status)) != "pending" {
			continue
		}
		out = append(out, record)
	}
	return out
}

func mergePermissionHistory(existing, incoming []ChatPermissionRecord) []ChatPermissionRecord {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	merged := make(map[string]ChatPermissionRecord, len(existing)+len(incoming))
	for _, record := range existing {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			continue
		}
		merged[id] = record
	}
	for _, record := range incoming {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			continue
		}
		if current, ok := merged[id]; ok {
			record = mergePermissionRecord(current, record)
		}
		merged[id] = record
	}
	out := make([]ChatPermissionRecord, 0, len(merged))
	for _, record := range merged {
		out = append(out, record)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := permissionSortTimestamp(out[i])
		right := permissionSortTimestamp(out[j])
		if left == right {
			return strings.TrimSpace(out[i].ID) < strings.TrimSpace(out[j].ID)
		}
		return left < right
	})
	return out
}

func permissionSortTimestamp(record ChatPermissionRecord) int64 {
	for _, value := range []int64{record.PermissionRequestedAt, record.CreatedAt, record.StartedAt, record.CompletedAt, record.UpdatedAt, record.ResolvedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}

func permissionRecordCreatedAt(record ChatPermissionRecord) int64 {
	for _, value := range []int64{record.CreatedAt, record.PermissionRequestedAt, record.StartedAt, record.CompletedAt, record.UpdatedAt, record.ResolvedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}

func permissionRecordStartedAt(record ChatPermissionRecord) int64 {
	for _, value := range []int64{record.StartedAt, record.CompletedAt, record.UpdatedAt, record.ResolvedAt, record.CreatedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}

func permissionRecordCompletedAt(record ChatPermissionRecord) int64 {
	for _, value := range []int64{record.CompletedAt, record.ResolvedAt, record.UpdatedAt, record.CreatedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}

func permissionLifecycleState(record ChatPermissionRecord) string {
	if strings.TrimSpace(record.Error) != "" {
		return "error"
	}
	switch strings.ToLower(strings.TrimSpace(record.ExecutionStatus)) {
	case "waiting_approval", "queued":
		return "pending"
	case "running":
		return "running"
	case "failed", "cancelled", "skipped":
		return "error"
	case "completed":
		return "done"
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case "pending", "approved":
		return "pending"
	case "denied", "cancelled":
		return "error"
	default:
		return "done"
	}
}

func permissionLifecycleOutput(record ChatPermissionRecord) string {
	execStatus := strings.ToLower(strings.TrimSpace(record.ExecutionStatus))
	status := strings.ToLower(strings.TrimSpace(record.Status))
	if text := strings.TrimSpace(record.Output); text != "" {
		switch execStatus {
		case "completed", "failed", "skipped", "cancelled":
			if !isPermissionPrivacyPlaceholder(record.ToolName, text) {
				return text
			}
		case "":
			if status == "denied" || status == "cancelled" {
				return text
			}
		}
	}
	if status == "denied" || status == "cancelled" || strings.TrimSpace(record.Error) != "" {
		return strings.TrimSpace(record.Reason)
	}
	return ""
}

func isPermissionPrivacyPlaceholder(toolName, text string) bool {
	toolName = strings.TrimSpace(toolName)
	text = strings.TrimSpace(text)
	if toolName == "" || text == "" {
		return false
	}
	return text == fmt.Sprintf("%s executed; detailed output omitted for privacy", toolName)
}

func permissionToolEntry(record ChatPermissionRecord) chatToolStreamEntry {
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	output := permissionLifecycleOutput(record)
	entry := chatToolStreamEntry{
		ToolName:           toolName,
		CallID:             strings.TrimSpace(record.CallID),
		Output:             output,
		Raw:                output,
		Error:              strings.TrimSpace(record.Error),
		State:              permissionLifecycleState(record),
		StartedArguments:   strings.TrimSpace(record.ToolArguments),
		StartedArgsAreJSON: parseToolJSON(strings.TrimSpace(record.ToolArguments)) != nil,
		CreatedAt:          permissionRecordCompletedAt(record),
		StartedAt:          permissionRecordStartedAt(record),
		DurationMS:         record.DurationMS,
	}
	if entry.CreatedAt <= 0 {
		entry.CreatedAt = permissionRecordCreatedAt(record)
	}
	if entry.StartedAt <= 0 {
		entry.StartedAt = permissionRecordCreatedAt(record)
	}
	if entry.DurationMS <= 0 && entry.StartedAt > 0 && entry.CreatedAt > entry.StartedAt {
		entry.DurationMS = entry.CreatedAt - entry.StartedAt
	}
	entry.State = normalizedToolState(entry)
	return entry
}

func permissionShouldRenderInToolViews(record ChatPermissionRecord) bool {
	if strings.TrimSpace(record.ID) == "" {
		return false
	}
	if strings.TrimSpace(record.ToolName) == "" {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(record.Status))
	execStatus := strings.ToLower(strings.TrimSpace(record.ExecutionStatus))
	if status == "pending" || execStatus == "waiting_approval" || execStatus == "queued" || execStatus == "running" {
		return true
	}
	if strings.TrimSpace(record.Error) != "" {
		return true
	}
	if output := strings.TrimSpace(permissionLifecycleOutput(record)); output != "" {
		return true
	}
	switch status {
	case "denied", "cancelled":
		return true
	}
	switch execStatus {
	case "failed", "cancelled", "skipped":
		return true
	default:
		return false
	}
}

func (p *ChatPage) rebuildToolLifecycleViews() {
	p.pendingPerms = filterPendingPermissions(p.permissions)
	p.ensurePermissionSelection()
	p.syncSpecialPermissionModals()

	seen := make(map[string]struct{}, len(p.permissions))
	for _, record := range p.permissions {
		if !permissionShouldRenderInToolViews(record) {
			continue
		}
		entry := permissionToolEntry(record)
		key := streamToolKey(entry.CallID, entry.ToolName)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		p.upsertToolStreamEntry(entry)
	}
	sort.SliceStable(p.toolStream, func(i, j int) bool {
		if p.toolStream[i].CreatedAt == p.toolStream[j].CreatedAt {
			return toolStreamEntryKey(p.toolStream[i]) < toolStreamEntryKey(p.toolStream[j])
		}
		return p.toolStream[i].CreatedAt < p.toolStream[j].CreatedAt
	})
	if len(p.toolStream) > chatMaxToolEntries {
		drop := len(p.toolStream) - chatMaxToolEntries
		p.toolStream = append([]chatToolStreamEntry(nil), p.toolStream[drop:]...)
	}

	nextTimeline := make([]chatMessageItem, 0, len(p.timeline)+len(p.toolStream))
	for _, item := range p.timeline {
		if strings.EqualFold(strings.TrimSpace(item.Role), "tool") {
			continue
		}
		nextTimeline = append(nextTimeline, item)
	}
	for _, entry := range p.toolStream {
		text := formatUnifiedToolEntry(entry)
		if strings.TrimSpace(text) == "" {
			continue
		}
		nextTimeline = append(nextTimeline, chatMessageItem{
			Role:      "tool",
			Text:      text,
			CreatedAt: entry.CreatedAt,
			ToolState: normalizedToolState(entry),
			Metadata:  toolTimelineMetadata(entry),
		})
	}
	sort.SliceStable(nextTimeline, func(i, j int) bool {
		if nextTimeline[i].CreatedAt == nextTimeline[j].CreatedAt {
			return timelineSortOrder(nextTimeline[i].Role) < timelineSortOrder(nextTimeline[j].Role)
		}
		return nextTimeline[i].CreatedAt < nextTimeline[j].CreatedAt
	})
	if len(nextTimeline) > chatMaxTimelineMessages {
		drop := len(nextTimeline) - chatMaxTimelineMessages
		nextTimeline = append([]chatMessageItem(nil), nextTimeline[drop:]...)
	}
	p.timeline = nextTimeline
	p.resetTimelineRenderCache()
}

func timelineSortOrder(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return 0
	case "assistant":
		return 1
	case "reasoning":
		return 2
	case "tool":
		return 3
	case "system":
		return 4
	default:
		return 5
	}
}

func (p *ChatPage) ensurePermissionSelection() {
	if len(p.pendingPerms) == 0 {
		p.permSelected = 0
		p.askUserOption = 0
		p.syncPermissionDetailTarget()
		return
	}
	if p.permSelected < 0 {
		p.permSelected = 0
	}
	if p.permSelected >= len(p.pendingPerms) {
		p.permSelected = len(p.pendingPerms) - 1
	}
	indexes := p.genericPermissionIndexes()
	if len(indexes) == 0 {
		p.askUserOption = 0
		p.syncPermissionDetailTarget()
		return
	}
	if indexOfInt(indexes, p.permSelected) < 0 {
		p.permSelected = indexes[0]
	}
	p.syncAskUserOption()
	p.syncPermissionDetailTarget()
}

func (p *ChatPage) genericPermissionCount() int {
	count := 0
	for i := range p.pendingPerms {
		if isPlanUpdatePermission(p.pendingPerms[i]) || isExitPlanPermission(p.pendingPerms[i]) || isManageTodosPermission(p.pendingPerms[i]) || isAskUserPermission(p.pendingPerms[i]) || isWorkspaceScopePermission(p.pendingPerms[i]) || isTaskLaunchPermission(p.pendingPerms[i]) || isThemeChangePermission(p.pendingPerms[i]) || isAgentChangePermission(p.pendingPerms[i]) || isSkillChangePermission(p.pendingPerms[i]) {
			continue
		}
		count++
	}
	return count
}

func (p *ChatPage) genericPermissionIndexes() []int {
	if len(p.pendingPerms) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(p.pendingPerms))
	for i := range p.pendingPerms {
		if isPlanUpdatePermission(p.pendingPerms[i]) || isExitPlanPermission(p.pendingPerms[i]) || isManageTodosPermission(p.pendingPerms[i]) || isAskUserPermission(p.pendingPerms[i]) || isWorkspaceScopePermission(p.pendingPerms[i]) || isTaskLaunchPermission(p.pendingPerms[i]) || isThemeChangePermission(p.pendingPerms[i]) || isSkillChangePermission(p.pendingPerms[i]) {
			continue
		}
		indexes = append(indexes, i)
	}
	return indexes
}

func indexOfInt(items []int, target int) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}
	return -1
}

func (p *ChatPage) syncSpecialPermissionModals() {
	if strings.TrimSpace(p.planUpdatePermission) != "" {
		if _, ok := p.pendingPermissionByID(p.planUpdatePermission); !ok {
			p.closePlanUpdateModal()
		}
	}
	if strings.TrimSpace(p.planExitPermission) != "" {
		if _, ok := p.pendingPermissionByID(p.planExitPermission); !ok {
			p.closePlanExitModal()
		}
	}
	if strings.TrimSpace(p.manageTodosPermission) != "" {
		if _, ok := p.pendingPermissionByID(p.manageTodosPermission); !ok {
			p.closeManageTodosModal()
		}
	}
	if strings.TrimSpace(p.askUserPermission) != "" {
		if _, ok := p.pendingPermissionByID(p.askUserPermission); !ok {
			p.closeAskUserModal()
		}
	}
	if strings.TrimSpace(p.workspaceScopePermission) != "" {
		if _, ok := p.pendingPermissionByID(p.workspaceScopePermission); !ok {
			p.closeWorkspaceScopeModal()
		}
	}
	if strings.TrimSpace(p.taskLaunchPermission) != "" {
		if _, ok := p.pendingPermissionByID(p.taskLaunchPermission); !ok {
			p.closeTaskLaunchModal()
		}
	}
	if strings.TrimSpace(p.agentChangePermission) != "" {
		if _, ok := p.pendingPermissionByID(p.agentChangePermission); !ok {
			p.closeAgentChangeModal()
		}
	}
	if strings.TrimSpace(p.skillChangePermission) != "" {
		if _, ok := p.pendingPermissionByID(p.skillChangePermission); !ok {
			p.closeSkillChangeModal()
		}
	}
	if strings.TrimSpace(p.themeChangePermission) != "" {
		if _, ok := p.pendingPermissionByID(p.themeChangePermission); !ok {
			p.closeThemeChangeModal()
		}
	}
	if p.planEditorModalActive() {
		return
	}
	if p.planUpdateModalActive() {
		return
	}
	if p.planExitModalActive() {
		return
	}
	if p.manageTodosModalActive() {
		return
	}
	if p.askUserModalActive() {
		return
	}
	if p.workspaceScopeModalActive() {
		return
	}
	if p.taskLaunchModalActive() {
		return
	}
	if p.themeChangeModalActive() {
		return
	}
	if p.agentChangeModalActive() {
		return
	}
	if p.skillChangeModalActive() {
		return
	}
	if p.sessionsPaletteActive() {
		return
	}
	p.ensurePermissionSelection()
	if len(p.pendingPerms) == 0 {
		return
	}
	selected := ChatPermissionRecord{}
	found := false
	for i := range p.pendingPerms {
		if !isPlanUpdatePermission(p.pendingPerms[i]) {
			continue
		}
		p.OpenPlanUpdatePermissionModal(p.pendingPerms[i])
		return
	}
	for i := range p.pendingPerms {
		if !isExitPlanPermission(p.pendingPerms[i]) {
			continue
		}
		p.permSelected = i
		selected = p.pendingPerms[i]
		found = true
		break
	}
	if !found {
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isManageTodosPermission(record) {
				continue
			}
			p.OpenManageTodosPermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isAskUserPermission(record) {
				continue
			}
			p.OpenAskUserPermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isWorkspaceScopePermission(record) {
				continue
			}
			p.OpenWorkspaceScopePermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isTaskLaunchPermission(record) {
				continue
			}
			p.OpenTaskLaunchPermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isThemeChangePermission(record) {
				continue
			}
			p.OpenThemeChangePermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isAgentChangePermission(record) {
				continue
			}
			p.OpenAgentChangePermissionModal(record)
			return
		}
		for i := range p.pendingPerms {
			record := p.pendingPerms[i]
			if !isSkillChangePermission(record) {
				continue
			}
			p.OpenSkillChangePermissionModal(record)
			return
		}
		return
	}
	title, body, planID := exitPlanPermissionPayload(selected)
	p.OpenExitPlanModePermissionModal(selected.ID, planID, title, body)
}

func (p *ChatPage) pendingPermissionByID(permissionID string) (ChatPermissionRecord, bool) {
	permissionID = strings.TrimSpace(permissionID)
	if permissionID == "" {
		return ChatPermissionRecord{}, false
	}
	for i := range p.pendingPerms {
		if strings.TrimSpace(p.pendingPerms[i].ID) != permissionID {
			continue
		}
		return p.pendingPerms[i], true
	}
	return ChatPermissionRecord{}, false
}

func streamToolKey(callID, toolName string) string {
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if callID == "" {
		return "tool:" + toolName
	}
	return callID
}

func (p *ChatPage) AppendUserAuthCommandMessage(provider string) {
	if p == nil {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "provider"
	}
	if p.authCommandLogged == nil {
		p.authCommandLogged = make(map[string]bool)
	}
	if p.authCommandLogged[provider] {
		return
	}
	p.authCommandLogged[provider] = true
	p.timeline = append(p.timeline, chatMessageItem{
		Role:      "user",
		Text:      "/auth key " + provider + " [redacted]",
		CreatedAt: time.Now().UnixMilli(),
	})
	p.bumpTimelineRenderGeneration()
	p.resetTimelineRenderCache()
}
