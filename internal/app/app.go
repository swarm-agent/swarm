package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/term"

	"swarm-refactor/swarmtui/internal/buildinfo"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/copyblock"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
	"swarm-refactor/swarmtui/internal/updatehandoff"
)

const (
	interruptTick              = "tick"
	interruptChatAsync         = "chat-async"
	interruptReloadReady       = "reload-ready"
	interruptAuthReady         = "auth-ready"
	interruptOnboardingReady   = "onboarding-ready"
	interruptVoiceReady        = "voice-ready"
	interruptStreamReady       = "stream-ready"
	interruptGitStatusReady    = "git-status-ready"
	interruptNotificationReady = "notification-ready"
	interruptTaskCommandReady  = "task-command-ready"
	interruptV3Chat            = "v3-chat-ready"
	interruptQuit              = "quit"
	defaultDaemonURL           = "http://127.0.0.1:7781"
	reloadInterval             = 3 * time.Second
	streamRenderMinInterval    = 66 * time.Millisecond
	vaultExportDirName         = "Swarm"
	vaultExportFileExt         = ".swarmvault"
	commitBackgroundAgentName  = "memory"
	commitBackgroundLineageTag = "@memory"
)

func buildHomeCommandSuggestions(devMode bool) []ui.CommandSuggestion {
	updateQuickTips := []string{"/update"}
	updateHint := "Update Swarm"
	if devMode {
		updateQuickTips = append(updateQuickTips, "/update dev")
		updateHint = "Update Swarm"
	}
	items := []ui.CommandSuggestion{
		{Command: "/add-dir", Hint: "Open linked-directory flow in the workspace manager"},
		{Command: "/alerts", Hint: "Open alerts / notifications (c clears all, Enter opens session)"},
		{Command: "/agents", Hint: "Open agent cards and model setup"},
		{Command: "/profiles", Hint: "Quick-switch the saved model profile used by new sessions"},
		{Command: "/notifications", Hint: "Alias for /alerts"},
		{Command: "/auth", Hint: "Auth status or key setup", QuickTips: []string{"/auth status", "/auth key <provider> <api_key>"}},
		{Command: "/codex", Hint: "Show Codex account usage and reset credits", QuickTips: []string{"/codex", "/codex refresh"}},
		{Command: "/commit", Hint: "Launch the memory agent in background to review diffs and commit changes", QuickTips: []string{"/commit [instructions]"}},
		{Command: "/copy", Hint: "Copy chat snapshot or /copy N block to clipboard"},
		{Command: "/header", Hint: "Toggle chat header visibility", QuickTips: []string{"/header toggle"}},
		{Command: "/help", Hint: "Show command help"},
		{Command: "/home", Hint: "Return to home without ending the chat session"},
		{Command: "/keybinds", Hint: "Open keybindings modal", QuickTips: []string{"/keybinds list", "/keybinds reset [all]"}},
		{Command: "/mode", Hint: "Toggle Plan behavior for new chats", QuickTips: []string{"/mode plan", "/mode action", "/mode status"}},
		{Command: "/mouse", Hint: "Toggle mouse click capture", QuickTips: []string{"/mouse toggle", "/mouse status"}},
		{Command: "/new", Hint: "Open a local session draft; explicit worktree forms route", QuickTips: []string{"/new [<prompt>]", "/new worktree [<prompt>]", "/new plan [<prompt>]", "/new wp [<prompt>]"}},
		{Command: "/permissions", Hint: "Show global permission policy", QuickTips: []string{"/permissions show", "/permissions allow tool <name>", "/permissions allow bash-prefix <command>", "/permissions deny phrase <text>"}},
		{Command: "/plan", Hint: "Show or close the existing session plan"},
		{Command: "/quit", Hint: "Exit swarmtui"},
		{Command: "/rebuild", Hint: "Rebuild the current lane and exit swarmtui"},
		{Command: "/sessions", Hint: "Open the card-style session manager (active conversations first)"},
		{Command: "/task", Hint: "Queue a durable AI task for Swarm", QuickTips: []string{"/task <request>", "/task plan <request>"}},
		{Command: "/update", Hint: updateHint, QuickTips: updateQuickTips},
		{Command: "/themes", Hint: "Open theme modal with live preview", QuickTips: []string{"/themes list", "/themes set <id>", "/themes create <id> from <base>", "/themes edit <id> <slot> <#RRGGBB>", "/themes delete <id>"}},
		{Command: "/thinking", Hint: "Use /thinking on, /thinking off, or /thinking status", QuickTips: []string{"/thinking on", "/thinking off", "/thinking status"}},
		{Command: "/workspace", Hint: "Open workspace manager", QuickTips: []string{"/workspaces", "/workspace save", "/workspace scan [query]"}},
		{Command: "/worktree", Hint: "Switch a local draft between direct and routed worktree start", QuickTips: []string{"/worktree on", "/worktree off"}},
		{Command: "/worktrees new", Hint: "Create a new session in its own worktree"},
		{Command: "/wt", Hint: "Prime the local draft or manage worktrees", QuickTips: []string{"/wt on", "/wt off", "/wt new"}},
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Command) < strings.ToLower(items[j].Command)
	})
	return items
}

func buildChatCommandSuggestions(devMode bool) []ui.CommandSuggestion {
	items := append([]ui.CommandSuggestion(nil), buildHomeCommandSuggestions(devMode)...)
	items = append(items,
		ui.CommandSuggestion{
			Command: archiveCommandUsage,
			Hint:    "Archive this session and return home",
		},
		ui.CommandSuggestion{
			Command: compactCommandUsage,
			Hint:    "Compact current chat context",
		},
	)
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Command) < strings.ToLower(items[j].Command)
	})
	return items
}

type onboardingWorkspaceResult struct {
	model model.HomeModel
	path  string
	err   error
}

type homeReloadResult struct {
	model            model.HomeModel
	hydrated         *client.SessionV3Hydrated
	sessionSnapshot  *client.SessionV3SyncSnapshot
	sessionQuery     string
	sessionOpenRoute string
	sessionID        string
	err              error
	silent           bool
}

type gitStatusRefreshResult struct {
	generation uint64
	path       string
	status     gitRepoStatus
	ok         bool
}

type gitWatcherStartResult struct {
	generation uint64
	path       string
	watcher    *repoGitWatcher
	err        error
}

type notificationCountResult struct {
	count int
	err   error
}

type repoGitWatcher struct {
	path      string
	repoRoot  string
	gitDir    string
	commonDir string
	watched   map[string]struct{}
	stop      chan struct{}
	stopped   chan struct{}
	debounce  chan struct{}
	watcher   *fsnotify.Watcher
}

type authLoginResult struct {
	err               error
	status            string
	toastLevel        ui.ToastLevel
	toast             string
	autoDefaults      *client.AutoDefaultsStatus
	clearCodexPending bool
	hideAuthModal     bool
}

type codexOAuthLoginSession struct {
	Provider        string
	Label           string
	Active          bool
	Method          string
	SessionID       string
	AuthURL         string
	VerificationURL string
	UserCode        string
}

type codexCodeLoginState struct {
	Provider     string
	Label        string
	Active       bool
	CodeVerifier string
	State        string
	AuthURL      string
}

type voiceCapturePhase string

const (
	voiceCapturePhaseIdle       voiceCapturePhase = ""
	voiceCapturePhaseRecording  voiceCapturePhase = "recording"
	voiceCapturePhaseProcessing voiceCapturePhase = "processing"
)

type activeVoiceCapture struct {
	ID        int64
	Phase     voiceCapturePhase
	Since     time.Time
	Route     string
	SessionID string
	DeviceID  string
	Profile   string
	Provider  string
	Model     string
	Language  string
	cancel    context.CancelFunc
}

type voiceCaptureEventKind string

const (
	voiceCaptureEventKindRecorded    voiceCaptureEventKind = "recorded"
	voiceCaptureEventKindTranscribed voiceCaptureEventKind = "transcribed"
)

type voiceCaptureEvent struct {
	CaptureID int64
	Kind      voiceCaptureEventKind
	Audio     []byte
	Backend   string
	Result    client.STTTranscribeResult
	Err       error
}

type App struct {
	screen tcell.Screen
	home   *ui.HomePage
	chat   *ui.ChatPage
	v3Chat *v3chat.Page
	route  string

	api                 *client.API
	startupCWD          string
	activePath          string
	workspacePath       string
	selectedChatRouteID string
	homeModel           model.HomeModel
	agentState          client.AgentState
	updateStatus        client.UpdateStatus
	config              AppConfig
	themePreviewID      string
	settingsLabel       string
	keybinds            *ui.KeyBindings

	lastReloadAt              time.Time
	reloadCh                  chan homeReloadResult
	reloading                 atomic.Bool
	homeWorkspaceBootstrapped atomic.Bool
	authLoginCh               chan authLoginResult
	authLogging               atomic.Bool
	onboardingWorkspaceCh     chan onboardingWorkspaceResult
	codexPending              *codexCodeLoginState

	voiceCaptureSeq        int64
	voiceCapture           activeVoiceCapture
	voiceCaptureCh         chan voiceCaptureEvent
	pasteActive            bool
	permissionsBypassModal permissionsBypassModalState
	permissionsPolicyModal permissionsPolicyModalState

	streamEvents            chan client.StreamEventEnvelope
	streamCancel            context.CancelFunc
	streamSeq               atomic.Uint64
	streamRenderPending     bool
	lastStreamRenderAt      time.Time
	streamRenderWakePending atomic.Bool

	tuiSessionStore        *tuiSessionStore
	tuiRealtime            *tuiRealtimeController
	tuiRealtimeFrames      chan client.V3RealtimeFrame
	tuiRealtimeStatuses    chan tuiRealtimeStatus
	tuiRealtimeWorkset     tuiRealtimeWorksetState
	tuiRealtimeClientID    string
	tuiRealtimeScopeSerial atomic.Uint64

	gitStatusCh            chan gitStatusRefreshResult
	gitWatcherReady        chan gitWatcherStartResult
	gitWatcher             *repoGitWatcher
	gitWatcherStartingPath string
	gitWatchGeneration     atomic.Uint64

	notificationCountCh    chan notificationCountResult
	taskCommandCh          chan taskCommandResult
	swarmNotificationCount int

	pendingChatRender   chan struct{}
	pendingV3ChatRender chan struct{}
	pendingStreamReady  chan struct{}

	workspaceCandidates []workspaceCandidate
	mouseHintShown      bool
	vault               client.VaultStatus

	quitRequested bool

	startupNetworkWarningModal startupNetworkWarningModalState

	devUpdateRequested     bool
	releaseUpdateRequested bool
	startupUpdateAnnounced bool

	sessionWorksetPagination tuiSessionWorksetPagination
}

type tuiSessionWorksetPagination struct {
	NextBeforeUpdatedAt *int64
	NextBeforeSessionID string
	HasMore             bool
	LoadedAt            int64
}

type tuiRealtimeWorksetState struct {
	ScopeKey       string
	WorkspacePaths []string
	CWDPath        string
}

func New() (*App, error) {
	s, err := tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("create screen: %w", err)
	}
	if err := s.Init(); err != nil {
		return nil, fmt.Errorf("init screen: %w", err)
	}
	// Force a known baseline: if a prior crash/session left terminal mouse
	// tracking enabled, explicitly disable it before applying config.
	s.DisableMouse()
	s.EnablePaste()
	s.Clear()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cwd = normalizePath(cwd)
	if cwd == "" {
		cwd = "."
	}

	apiURL := strings.TrimSpace(os.Getenv("SWARMD_URL"))
	if apiURL == "" {
		apiURL = defaultDaemonURL
	}
	api := client.New(apiURL)
	if token := strings.TrimSpace(os.Getenv("SWARMD_TOKEN")); token != "" {
		api.SetToken(token)
	}

	initial := model.EmptyHome()
	initial.CWD = cwd
	initial.ServerURL = api.BaseURL()
	initial.HintLine = "Connecting to swarmd..."
	initial.TipLine = "/vault  •  /auth  •  /workspace"

	cfg, cfgErr := loadAppConfig(api)

	app := &App{
		screen:                s,
		home:                  ui.NewHomePage(initial),
		route:                 "home",
		api:                   api,
		startupCWD:            cwd,
		activePath:            cwd,
		workspacePath:         "",
		selectedChatRouteID:   "",
		homeModel:             initial,
		config:                cfg,
		settingsLabel:         settingsBackendLabel,
		keybinds:              ui.NewDefaultKeyBindings(),
		lastReloadAt:          time.Now(),
		reloadCh:              make(chan homeReloadResult, 1),
		authLoginCh:           make(chan authLoginResult, 1),
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
		voiceCaptureCh:        make(chan voiceCaptureEvent, 4),
		streamEvents:          make(chan client.StreamEventEnvelope, 256),
		gitStatusCh:           make(chan gitStatusRefreshResult, 8),
		gitWatcherReady:       make(chan gitWatcherStartResult, 1),
		notificationCountCh:   make(chan notificationCountResult, 1),
		taskCommandCh:         make(chan taskCommandResult, 8),
		pendingChatRender:     make(chan struct{}, 1),
		pendingV3ChatRender:   make(chan struct{}, 1),
		pendingStreamReady:    make(chan struct{}, 1),
		tuiSessionStore:       newTUISessionStore(),
		tuiRealtimeFrames:     make(chan client.V3RealtimeFrame, 256),
		tuiRealtimeStatuses:   make(chan tuiRealtimeStatus, 32),
		tuiRealtimeClientID:   fmt.Sprintf("tui:%d", time.Now().UnixNano()),
		workspaceCandidates:   make([]workspaceCandidate, 0, 128),
	}
	app.keybinds.ApplyOverrides(cfg.Input.Keybinds)
	app.home.SetKeyBindings(app.keybinds)
	app.home.SetSessionMode(app.config.Chat.DefaultNewSessionMode)
	app.home.SetPasteActive(app.pasteActive)
	mouseEnabled := cfg.Input.MouseEnabled
	app.config.Input.MouseEnabled = mouseEnabled
	app.config.Swarming = cfg.Swarming
	app.config.Swarm = cfg.Swarm
	app.home.SetSwarmName(app.config.Swarm.Name)
	app.swarmNotificationCount = 0
	app.setMouseCapture(mouseEnabled)
	themeID := strings.TrimSpace(cfg.UI.Theme)
	app.syncConfiguredCustomThemes()
	app.bootstrapTheme(themeID)
	app.home.SetCommandSuggestions(buildHomeCommandSuggestions(cfg.Startup.DevMode))

	// The screen is ready now. Do not hold the first frame behind workspace,
	// topology, notification, provider, agent, update, context, or Git status
	// enrichment. Load those through the existing canonical refresh paths and
	// surface any failures after Run has rendered the usable shell.
	if cfgErr != nil {
		app.home.SetStatus(fmt.Sprintf("settings warning: %v", cfgErr))
	}
	app.queueReload(false)
	app.queueNotificationCount()
	app.announceAppliedUpdate()
	app.openStartupNetworkWarningModal()
	return app, nil
}

func (a *App) Close() {
	if a.streamCancel != nil {
		a.streamCancel()
		a.streamCancel = nil
	}
	if a.tuiRealtime != nil {
		a.tuiRealtime.Stop()
	}
	a.closeV3Chat()
	a.stopGitRealtimeWatcher()
	if a.voiceCapture.cancel != nil {
		a.voiceCapture.cancel()
		a.voiceCapture.cancel = nil
	}
	if a.screen != nil {
		a.screen.Fini()
		a.screen = nil
	}
}

func (a *App) Run() error {
	dirty := true
	for {
		if dirty {
			if a.route == "v3chat" && a.v3Chat != nil {
				a.v3Chat.Draw(a.screen)
				if a.home != nil {
					a.home.DrawChatOverlay(a.screen)
				}
			} else if a.route == "chat" && a.chat != nil {
				a.chat.Draw(a.screen)
				if a.home != nil {
					a.home.DrawChatOverlay(a.screen)
				}
			} else {
				a.home.Draw(a.screen)
			}
			if a.permissionsPolicyModalActive() {
				a.drawPermissionsPolicyModal()
			}
			if a.permissionsBypassModalActive() {
				a.drawPermissionsBypassModal()
			}
			if a.startupNetworkWarningModalActive() {
				a.drawStartupNetworkWarningModal()
			}
			a.screen.Show()
			a.noteStreamRenderDrawn(time.Now())
			dirty = false
		}

		ev := a.screen.PollEvent()
		switch e := ev.(type) {
		case *tcell.EventResize:
			a.screen.Sync()
			dirty = true
		case *tcell.EventInterrupt:
			key, _ := e.Data().(string)
			switch key {
			case interruptTick:
				if a.handleTick() {
					dirty = true
				}
			case interruptChatAsync:
				requestedRender := a.consumePendingChatRender()
				if a.handleChatAsync() || requestedRender {
					dirty = true
				}
			case interruptReloadReady:
				a.consumeReloadResult()
				dirty = true
			case interruptAuthReady:
				a.consumeAuthLoginResult()
				dirty = true
			case interruptOnboardingReady:
				a.consumeOnboardingWorkspaceResult()
				dirty = true
			case interruptVoiceReady:
				a.consumeVoiceCaptureEvents()
				dirty = true
			case interruptStreamReady:
				if a.consumeStreamReadyForRender(time.Now(), true) {
					dirty = true
				}
			case interruptGitStatusReady:
				if a.consumeGitStatusRefreshResults() {
					dirty = true
				}
			case interruptNotificationReady:
				a.consumeNotificationCountResult()
				dirty = true
			case interruptTaskCommandReady:
				a.consumeTaskCommandResults()
				dirty = true
			case interruptV3Chat:
				a.consumeV3ChatRender()
				dirty = true
			case interruptQuit:
				if a.devUpdateRequested {
					return updatehandoff.ErrDevUpdateRequested
				}
				if a.releaseUpdateRequested {
					return updatehandoff.ErrReleaseUpdateRequested
				}
				return nil
			}
		case *tcell.EventMouse:
			if a.startupNetworkWarningModalActive() {
				if a.handleStartupNetworkWarningModalMouse(e) {
					dirty = true
					continue
				}
			}
			if a.permissionsBypassModalActive() {
				if a.handlePermissionsBypassModalMouse(e) {
					dirty = true
					continue
				}
			}
			if a.permissionsPolicyModalActive() {
				if a.handlePermissionsPolicyModalMouse(e) {
					dirty = true
					continue
				}
			}
			if a.quitRequested {
				continue
			}
			if (a.route == "chat" || a.route == "v3chat") && a.home != nil && a.home.ChatOverlayVisible() {
				if a.home.HandleChatOverlayMouse(e) {
					a.consumeHomeOverlayActions()
				}
				dirty = true
				continue
			}
			if a.config.Input.MouseEnabled && !a.mouseHintShown {
				a.mouseHintShown = true
				message := "mouse capture on: use /mouse off (or F8) to disable; Shift+drag to select/copy"
				if a.route == "chat" && a.chat != nil {
					a.chat.SetStatus(message)
				} else {
					a.home.SetStatus(message)
				}
				a.showToast(ui.ToastInfo, message)
			}
			if a.route == "v3chat" && a.v3Chat != nil {
				a.v3Chat.HandleMouse(e)
				if a.v3Chat.ConsumeOpenAgentsRequest() {
					a.openAgentsModal()
				}
				dirty = true
				continue
			}
			if a.route == "chat" && a.chat != nil {
				a.chat.HandleMouse(e)
				a.consumeChatActions()
				dirty = true
				continue
			}
			if a.route == "home" {
				a.home.HandleMouse(e)
				a.consumeHomeActions()
				dirty = true
			}
		case *tcell.EventPaste:
			if a.quitRequested {
				continue
			}
			a.setPasteActive(e.Start())
			dirty = true
		case *tcell.EventKey:
			if a.startupNetworkWarningModalActive() {
				if a.handleStartupNetworkWarningModalKey(e) {
					dirty = true
					continue
				}
			}
			if a.permissionsBypassModalActive() {
				if a.handlePermissionsBypassModalKey(e) {
					dirty = true
					continue
				}
			}
			if a.permissionsPolicyModalActive() {
				if a.handlePermissionsPolicyModalKey(e) {
					dirty = true
					continue
				}
			}
			if a.quitRequested {
				continue
			}
			if a.pasteActive {
				if a.route == "chat" && a.home != nil && a.home.ChatOverlayVisible() {
					if a.home.HandlePasteKey(e) {
						a.consumeHomeOverlayActions()
						dirty = true
					}
					continue
				}
				if a.route == "chat" && a.chat != nil {
					if a.chat.HandlePasteKey(e) {
						dirty = true
					}
					continue
				}
				if a.route == "home" && a.home != nil {
					if a.home.HandlePasteKey(e) {
						dirty = true
					}
					a.consumeHomeActions()
					continue
				}
				if a.route == "v3chat" && a.v3Chat != nil {
					if a.v3Chat.HandlePasteKey(e) {
						dirty = true
					}
					continue
				}
				a.setPasteActive(false)
				dirty = true
			}
			if (a.route == "chat" || a.route == "v3chat") && a.home != nil {
				if a.home.HandleChatOverlayKey(e) {
					a.consumeHomeOverlayActions()
					a.consumeHomeActions()
					dirty = true
					continue
				}
			}
			if handled := a.handleGlobalKey(e); handled {
				dirty = true
				continue
			}
			if (a.route == "chat" || a.route == "v3chat") && a.home != nil && a.home.ChatOverlayVisible() {
				dirty = true
				continue
			}
			if a.voiceInputLocked() {
				dirty = true
				continue
			}
			if a.route == "v3chat" && a.v3Chat != nil {
				switch a.v3Chat.HandleKey(e) {
				case v3chat.PageActionHome:
					a.closeV3Chat()
					a.route = "home"
					a.home.SetStatus("home")
				case v3chat.PageActionCommand:
					a.handleV3ChatCommand()
				case v3chat.PageActionOpenCurrentPlan:
					a.showV3CurrentPlan()
				}
				dirty = true
				continue
			}
			if a.route == "chat" && a.chat != nil {
				if handled := a.handleChatKey(e); handled {
					a.consumeChatActions()
					dirty = true
					continue
				}
				a.chat.HandleKey(e)
				a.consumeChatActions()
				dirty = true
				continue
			}
			if handled := a.handleHomeKey(e); handled {
				a.consumeHomeActions()
				dirty = true
				continue
			}
			a.home.HandleKey(e)
			a.consumeHomeActions()
			dirty = true
		}
	}
}

func (a *App) setPasteActive(active bool) {
	a.pasteActive = active
	if a.home != nil {
		a.home.SetPasteActive(active)
	}
	if a.chat != nil {
		a.chat.SetPasteActive(active)
	}
	if a.v3Chat != nil {
		a.v3Chat.SetPasteActive(active)
	}
}

func (a *App) handleTick() bool {
	changed := false
	if a.route == "chat" && a.chat != nil {
		if a.chat.HandleTick() {
			changed = true
		}
		a.consumeChatActions()
		if a.voiceCapture.Phase != voiceCapturePhaseIdle {
			changed = true
		}
		return changed
	}
	if a.route == "home" && a.home != nil {
		if a.home.HandleTick() {
			changed = true
		}
	}
	if a.voiceCapture.Phase != voiceCapturePhaseIdle {
		changed = true
	}
	if time.Since(a.lastReloadAt) >= reloadInterval {
		a.lastReloadAt = time.Now()
		a.queueReload(true)
	}
	return changed
}

func (a *App) handleChatAsync() bool {
	if a.route != "chat" || a.chat == nil {
		return false
	}
	changed := a.chat.HandleAsync()
	a.consumeChatActions()
	return changed
}

func (a *App) requestChatRender() {
	if a == nil || a.screen == nil {
		return
	}
	select {
	case a.pendingChatRender <- struct{}{}:
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptChatAsync))
	default:
	}
}

func (a *App) consumePendingChatRender() bool {
	if a == nil {
		return false
	}
	select {
	case <-a.pendingChatRender:
		return true
	default:
		return false
	}
}

func (a *App) requestStreamReadyInterrupt() {
	if a == nil || a.screen == nil {
		return
	}
	select {
	case a.pendingStreamReady <- struct{}{}:
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptStreamReady))
	default:
	}
}

func (a *App) consumePendingStreamReady() {
	if a == nil {
		return
	}
	select {
	case <-a.pendingStreamReady:
	default:
	}
}

func (a *App) consumeStreamReadyForRender(now time.Time, scheduleWake bool) bool {
	if a == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	a.consumePendingStreamReady()
	if a.consumeSessionStreamEvents() {
		a.streamRenderPending = true
	}
	if a.consumeTUIRealtimeEvents() {
		a.streamRenderPending = true
	}
	if !a.streamRenderPending {
		return false
	}
	if a.lastStreamRenderAt.IsZero() || !now.Before(a.lastStreamRenderAt.Add(streamRenderMinInterval)) {
		return true
	}
	if scheduleWake {
		a.scheduleStreamRenderWake(now)
	}
	return false
}

func (a *App) noteStreamRenderDrawn(now time.Time) {
	if a == nil || !a.streamRenderPending {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	a.lastStreamRenderAt = now
	a.streamRenderPending = false
}

func (a *App) scheduleStreamRenderWake(now time.Time) {
	if a == nil || a.screen == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	wait := time.Duration(0)
	if !a.lastStreamRenderAt.IsZero() {
		wait = a.lastStreamRenderAt.Add(streamRenderMinInterval).Sub(now)
		if wait < 0 {
			wait = 0
		}
	}
	if !a.streamRenderWakePending.CompareAndSwap(false, true) {
		return
	}
	go func(delay time.Duration) {
		if delay > 0 {
			time.Sleep(delay)
		}
		a.streamRenderWakePending.Store(false)
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptStreamReady))
		}
	}(wait)
}

func (a *App) startSessionEventStream() {
	if a == nil || a.api == nil {
		return
	}
	if a.streamCancel != nil {
		a.streamCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.streamCancel = cancel
	go a.runSessionEventStream(ctx)
}

func (a *App) runSessionEventStream(ctx context.Context) {
	if a == nil || a.api == nil {
		return
	}
	lastSeen := a.streamSeq.Load()
	channels := []string{"session:*", "ui:*", "workspace:*", "system:agent"}
	for {
		if ctx.Err() != nil {
			return
		}
		err := a.api.StreamEvents(ctx, lastSeen, channels, func(event client.StreamEventEnvelope) {
			if event.GlobalSeq > lastSeen {
				lastSeen = event.GlobalSeq
				a.streamSeq.Store(lastSeen)
			}
			if !a.enqueueSessionStreamEvent(ctx, event) {
				return
			}
			a.requestStreamReadyInterrupt()
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		return
	}
}

func (a *App) enqueueSessionStreamEvent(ctx context.Context, event client.StreamEventEnvelope) bool {
	if a == nil {
		return false
	}
	select {
	case a.streamEvents <- event:
		return true
	default:
	}
	a.requestStreamReadyInterrupt()
	select {
	case a.streamEvents <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *App) consumeSessionStreamEvents() bool {
	changed := false
	for {
		select {
		case event := <-a.streamEvents:
			if a.applySessionStreamEvent(event) {
				changed = true
			}
		default:
			return changed
		}
	}
}

func (a *App) applySessionStreamEvent(event client.StreamEventEnvelope) bool {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	switch eventType {
	case "notification.created", "notification.updated":
		return a.applySwarmStreamEvent(event)
	case "agent.profile.created", "agent.profile.updated", "agent.profile.deleted", "agent.active.updated", "agent.defaults.restored", "agent.defaults.reset", "agent.state.synced", "agent.custom_tool.created", "agent.custom_tool.updated", "agent.custom_tool.deleted", "agent.custom_tool.assigned", "agent.custom_tool.unassigned", "agent.active_subagent.updated", "agent.active_subagent.deleted":
		return a.applyAgentStreamEvent(event)
	case "session.created":
		var session client.SessionSummary
		if err := json.Unmarshal(event.Payload, &session); err != nil {
			return false
		}
		if strings.TrimSpace(session.ID) == "" {
			return false
		}
		session.SessionAPI = "v3"
		a.upsertHomeSessionSummary(modelSessionSummaryFromClient(session))
		return true
	case "session.deleted", "session.closed":
		sessionID := strings.TrimSpace(event.EntityID)
		if sessionID == "" {
			var payload struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return false
			}
			sessionID = firstNonEmpty(strings.TrimSpace(payload.SessionID), strings.TrimSpace(payload.ID))
		}
		return a.removeHomeSessionSummary(sessionID)
	case "session.title.updated":
		var payload struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		title := strings.TrimSpace(payload.Title)
		if sessionID == "" || title == "" {
			return false
		}
		changed := a.updateHomeSessionTitle(sessionID, title)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.SetSessionTitle(title)
			changed = true
		}
		return changed
	case "session.title.warning":
		var payload struct {
			SessionID string `json:"session_id"`
			Warning   string `json:"warning"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		warning := strings.TrimSpace(payload.Warning)
		if sessionID == "" || warning == "" {
			return false
		}
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.ApplySessionTitleWarning(warning)
			return true
		}
		return false
	case "session.metadata.updated":
		var payload struct {
			SessionID string         `json:"session_id"`
			Metadata  map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		if sessionID == "" {
			return false
		}
		metadata := cloneMetadataMap(payload.Metadata)
		changed := false
		next := a.homeModel
		for i := range next.RecentSessions {
			if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
				continue
			}
			if len(metadata) > 0 {
				next.RecentSessions[i].Metadata = metadata
			} else {
				next.RecentSessions[i].Metadata = nil
			}
			changed = true
			break
		}
		if changed {
			a.applyHomeModel(next)
		}
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			taskCount, openCount, inProgressCount := agentTodoCountsFromMetadata(metadata)
			a.chat.SetAgentTodoSummary(taskCount, openCount, inProgressCount)
			changed = true
		}
		return changed
	case "session.mode.updated":
		var payload struct {
			SessionID string `json:"session_id"`
			Mode      string `json:"mode"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		mode := strings.TrimSpace(payload.Mode)
		if sessionID == "" || mode == "" {
			return false
		}
		changed := a.updateHomeSessionMode(sessionID, mode)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.SetSessionMode(mode)
			a.syncChatAgentRuntime()
			changed = true
		}
		return changed
	case "session.preference.updated":
		var payload struct {
			SessionID     string                 `json:"session_id"`
			Preference    client.ModelPreference `json:"preference"`
			ContextWindow int                    `json:"context_window"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		if sessionID == "" {
			return false
		}
		changed := a.updateHomeSessionPreference(sessionID, payload.Preference)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.SetModelState(
				strings.TrimSpace(payload.Preference.Provider),
				strings.TrimSpace(payload.Preference.Model),
				strings.TrimSpace(payload.Preference.Thinking),
				strings.TrimSpace(payload.Preference.ServiceTier),
				strings.TrimSpace(payload.Preference.ContextMode),
			)
			a.syncChatAgentRuntime()
			changed = true
		}
		return changed
	case "session.branch.updated":
		var payload struct {
			SessionID string `json:"session_id"`
			Branch    string `json:"branch"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		branch := strings.TrimSpace(payload.Branch)
		if sessionID == "" || branch == "" {
			return false
		}
		changed := a.updateHomeSessionBranch(sessionID, branch)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.SetSessionBranch(branch)
			a.syncChatAgentRuntime()
			changed = true
		}
		return changed
	case "session.workspace.updated":
		var payload struct {
			SessionID     string `json:"session_id"`
			WorkspacePath string `json:"workspace_path"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		workspacePath := strings.TrimSpace(payload.WorkspacePath)
		if sessionID == "" || workspacePath == "" {
			return false
		}
		changed := a.updateHomeSessionWorkspacePath(sessionID, workspacePath)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			worktreeEnabled := false
			worktreeRootPath := ""
			if summary, ok := a.sessionSummaryByID(sessionID); ok {
				worktreeEnabled = summary.WorktreeEnabled
				worktreeRootPath = strings.TrimSpace(summary.WorktreeRootPath)
			}
			a.chat.SetSessionPath(a.userFacingSessionPath(workspacePath, worktreeEnabled, worktreeRootPath))
			a.syncChatAgentRuntime()
			changed = true
		}
		return changed
	case "ui.settings.updated":
		var settings client.UISettings
		if err := json.Unmarshal(event.Payload, &settings); err != nil {
			return false
		}
		return a.applyRemoteUISettings(settings)
	case "workspace.theme.updated":
		var resolution client.WorkspaceResolution
		if err := json.Unmarshal(event.Payload, &resolution); err != nil {
			return false
		}
		return a.applyRemoteWorkspaceTheme(resolution)
	case "permission.summary.updated":
		var payload struct {
			SessionID    string `json:"session_id"`
			PendingCount int    `json:"pending_count"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		sessionID := strings.TrimSpace(payload.SessionID)
		if sessionID == "" {
			return false
		}
		return a.updateHomeSessionPendingPermissions(sessionID, payload.PendingCount)
	case "permission.requested", "permission.updated",
		"session.status",
		"run.turn.started", "run.turn.error",
		"run.step.started",
		"run.assistant.delta", "run.assistant.commentary",
		"run.tool.started", "run.tool.delta", "run.tool.completed",
		"run.usage.updated",
		"run.message.stored", "run.message.updated",
		"run.session.title.updated", "run.session.warning":
		return a.applySharedChatRuntimeEvent(event)
	case "session.lifecycle.updated":
		var payload struct {
			SessionID      string                          `json:"session_id"`
			RunID          string                          `json:"run_id"`
			Active         bool                            `json:"active"`
			Phase          string                          `json:"phase"`
			StartedAt      int64                           `json:"started_at"`
			EndedAt        int64                           `json:"ended_at"`
			UpdatedAt      int64                           `json:"updated_at"`
			Generation     uint64                          `json:"generation"`
			StopReason     string                          `json:"stop_reason"`
			Error          string                          `json:"error"`
			OwnerTransport string                          `json:"owner_transport"`
			Lifecycle      client.SessionLifecycleSnapshot `json:"lifecycle"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false
		}
		lifecycle := payload.Lifecycle
		if strings.TrimSpace(lifecycle.SessionID) == "" {
			lifecycle = client.SessionLifecycleSnapshot{
				SessionID:      strings.TrimSpace(payload.SessionID),
				RunID:          strings.TrimSpace(payload.RunID),
				Active:         payload.Active,
				Phase:          strings.TrimSpace(payload.Phase),
				StartedAt:      payload.StartedAt,
				EndedAt:        payload.EndedAt,
				UpdatedAt:      payload.UpdatedAt,
				Generation:     payload.Generation,
				StopReason:     strings.TrimSpace(payload.StopReason),
				Error:          strings.TrimSpace(payload.Error),
				OwnerTransport: strings.TrimSpace(payload.OwnerTransport),
			}
		}
		sessionID := strings.TrimSpace(lifecycle.SessionID)
		if sessionID == "" {
			return false
		}
		changed := a.updateHomeSessionLifecycle(sessionID, lifecycle)
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.ApplySessionLifecycle(ui.ChatSessionLifecycle{
				SessionID:      lifecycle.SessionID,
				RunID:          lifecycle.RunID,
				Active:         lifecycle.Active,
				Phase:          lifecycle.Phase,
				StartedAt:      lifecycle.StartedAt,
				EndedAt:        lifecycle.EndedAt,
				UpdatedAt:      lifecycle.UpdatedAt,
				Generation:     lifecycle.Generation,
				StopReason:     lifecycle.StopReason,
				Error:          lifecycle.Error,
				OwnerTransport: lifecycle.OwnerTransport,
			})
			a.syncChatAgentRuntime()
			changed = true
		}
		return changed
	default:
		return false
	}
}

func (a *App) applyRemoteUISettings(settings client.UISettings) bool {
	if a == nil {
		return false
	}
	a.applyLoadedAppConfig(appConfigFromUISettings(settings))
	if a.home != nil {
		a.home.SetModel(a.homeModel)
	}
	return true
}

func (a *App) applyRemoteWorkspaceTheme(resolution client.WorkspaceResolution) bool {
	if a == nil {
		return false
	}
	a.syncActiveWorkspaceSelection(resolution)
	return true
}

func (a *App) applySwarmStreamEvent(event client.StreamEventEnvelope) bool {
	if a == nil {
		return false
	}
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	switch eventType {
	case "notification.created", "notification.updated":
		count, err := a.loadSwarmNotificationCount(context.Background())
		if err != nil {
			return false
		}
		a.setSwarmNotificationCount(count)
		return true
	default:
		return false
	}
}

func (a *App) applyAgentStreamEvent(event client.StreamEventEnvelope) bool {
	if a == nil {
		return false
	}
	state := decodeAgentStateFromStreamEvent(event)
	if len(state.Profiles) == 0 {
		if a.api == nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		fetched, err := a.api.ListAgents(ctx, 500)
		if err != nil {
			return false
		}
		state = fetched
	}
	return a.applyAgentStateToRuntime(state)
}

func decodeAgentStateFromStreamEvent(event client.StreamEventEnvelope) client.AgentState {
	if len(event.Payload) == 0 {
		return client.AgentState{}
	}
	var payload struct {
		State client.AgentState `json:"state"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return client.AgentState{}
	}
	return payload.State
}

func (a *App) applyAgentStateToRuntime(state client.AgentState) bool {
	a.agentState = state
	activeAgent, executionSetting, exitPlanMode, runtimeKnown := activeAgentRuntime(state)
	subagents := chatMentionSubagentNames(state)
	next := a.currentHomeModel()
	next.ActiveAgent = activeAgent
	next.ActiveAgentExecutionSetting = executionSetting
	next.ActiveAgentExitPlanMode = exitPlanMode
	next.ActiveAgentRuntimeKnown = runtimeKnown
	next.Subagents = append([]string(nil), subagents...)
	next = applyActiveAgentModels(next, state)
	changed := false
	if strings.TrimSpace(a.homeModel.ActiveAgent) != strings.TrimSpace(activeAgent) ||
		strings.TrimSpace(a.homeModel.ActiveAgentExecutionSetting) != strings.TrimSpace(executionSetting) ||
		a.homeModel.ActiveAgentExitPlanMode != exitPlanMode ||
		a.homeModel.ActiveAgentRuntimeKnown != runtimeKnown ||
		!sameStringSet(a.homeModel.Subagents, subagents) ||
		a.homeModel.PlanModelProvider != next.PlanModelProvider || a.homeModel.PlanModelName != next.PlanModelName ||
		a.homeModel.PlanThinkingLevel != next.PlanThinkingLevel || a.homeModel.PlanServiceTier != next.PlanServiceTier ||
		a.homeModel.AutoModelProvider != next.AutoModelProvider || a.homeModel.AutoModelName != next.AutoModelName ||
		a.homeModel.AutoThinkingLevel != next.AutoThinkingLevel || a.homeModel.AutoServiceTier != next.AutoServiceTier {
		a.homeModel = next
		a.home.SetModel(next)
		changed = true
	}
	if a.chat != nil {
		meta := a.chat.Meta()
		if !sameStringSet(meta.Subagents, subagents) {
			meta.Subagents = append([]string(nil), subagents...)
			a.chat.SetMeta(meta)
			changed = true
		}
		a.syncChatAgentRuntime()
	}
	return changed
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[strings.ToLower(strings.TrimSpace(value))]++
	}
	for _, value := range right {
		key := strings.ToLower(strings.TrimSpace(value))
		seen[key]--
		if seen[key] < 0 {
			return false
		}
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func (a *App) applySharedChatRuntimeEvent(event client.StreamEventEnvelope) bool {
	if a == nil || a.chat == nil {
		return false
	}
	sharedEvent, ok := decodeSharedChatRuntimeEvent(event)
	if !ok {
		return false
	}
	sessionID := strings.TrimSpace(sharedEvent.SessionID)
	if sessionID == "" || strings.TrimSpace(a.chat.SessionID()) != sessionID {
		return false
	}
	atUnix := event.TsUnixMs
	if atUnix <= 0 {
		atUnix = time.Now().UnixMilli()
	}
	return a.chat.ApplySharedStreamEvent(sharedEvent, atUnix)
}

func decodeSharedChatRuntimeEvent(event client.StreamEventEnvelope) (ui.ChatRunStreamEvent, bool) {
	var raw sharedChatRuntimeEventPayload
	if err := json.Unmarshal(event.Payload, &raw); err != nil {
		return ui.ChatRunStreamEvent{}, false
	}
	raw.Type = normalizeSharedChatRuntimeEventType(strings.TrimSpace(event.EventType), strings.TrimSpace(raw.Type))
	if strings.TrimSpace(raw.SessionID) == "" {
		raw.SessionID = strings.TrimSpace(event.EntityID)
	}
	if strings.TrimSpace(raw.SessionID) == "" {
		switch {
		case raw.Message != nil:
			raw.SessionID = strings.TrimSpace(raw.Message.SessionID)
		case raw.Permission != nil:
			raw.SessionID = strings.TrimSpace(raw.Permission.SessionID)
		case raw.Lifecycle != nil:
			raw.SessionID = strings.TrimSpace(raw.Lifecycle.SessionID)
		}
	}
	if strings.TrimSpace(raw.RunID) == "" {
		switch {
		case raw.Permission != nil:
			raw.RunID = strings.TrimSpace(raw.Permission.RunID)
		case raw.Lifecycle != nil:
			raw.RunID = strings.TrimSpace(raw.Lifecycle.RunID)
		}
	}
	if strings.TrimSpace(raw.Type) == "" || strings.TrimSpace(raw.SessionID) == "" {
		return ui.ChatRunStreamEvent{}, false
	}
	return convertClientRunStreamEvent(raw), true
}

func normalizeSharedChatRuntimeEventType(envelopeType, payloadType string) string {
	if strings.TrimSpace(payloadType) != "" {
		return strings.TrimSpace(payloadType)
	}
	switch strings.ToLower(strings.TrimSpace(envelopeType)) {
	case "session.status":
		return "session.status"
	case "run.turn.started":
		return "turn.started"
	case "run.turn.completed":
		return "turn.completed"
	case "run.turn.error":
		return "turn.error"
	case "run.step.started":
		return "step.started"
	case "run.assistant.delta":
		return "assistant.delta"
	case "run.assistant.commentary":
		return "assistant.commentary"
	case "run.reasoning.started":
		return "reasoning.started"
	case "run.reasoning.delta":
		return "reasoning.delta"
	case "run.reasoning.completed":
		return "reasoning.completed"
	case "run.reasoning.summary":
		return "reasoning.summary"
	case "run.tool.started":
		return "tool.started"
	case "run.tool.delta":
		return "tool.delta"
	case "run.tool.completed":
		return "tool.completed"
	case "run.usage.updated":
		return "usage.updated"
	case "run.message.stored":
		return "message.stored"
	case "run.message.updated":
		return "message.updated"
	case "run.session.title.updated":
		return "session.title.updated"
	case "run.session.warning":
		return "session.title.warning"
	default:
		return strings.TrimSpace(envelopeType)
	}
}

func (a *App) updateHomeSessionTitle(sessionID, title string) bool {
	sessionID = strings.TrimSpace(sessionID)
	title = strings.TrimSpace(title)
	if sessionID == "" || title == "" {
		return false
	}
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		if strings.TrimSpace(next.RecentSessions[i].Title) == title {
			break
		}
		next.RecentSessions[i].Title = title
		changed = true
		break
	}
	if !changed {
		next.RecentSessions = append([]model.SessionSummary{{ID: sessionID, Title: title}}, next.RecentSessions...)
		changed = true
	}
	for i := range next.BackgroundSessions {
		if strings.TrimSpace(next.BackgroundSessions[i].ChildSessionID) != sessionID {
			continue
		}
		if strings.TrimSpace(next.BackgroundSessions[i].ChildTitle) == title {
			break
		}
		next.BackgroundSessions[i].ChildTitle = title
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.applyHomeModel(next)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionMode(sessionID, mode string) bool {
	sessionID = strings.TrimSpace(sessionID)
	mode = strings.TrimSpace(mode)
	if sessionID == "" || mode == "" {
		return false
	}
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		normalized := normalizeAppSessionMode(mode)
		if strings.TrimSpace(next.RecentSessions[i].Mode) == normalized {
			break
		}
		next.RecentSessions[i].Mode = normalized
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.homeModel = next
	if a.home != nil {
		a.home.SetModel(next)
	}
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionPreference(sessionID string, preference client.ModelPreference) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	preference.Provider = strings.TrimSpace(preference.Provider)
	preference.Model = strings.TrimSpace(preference.Model)
	preference.Thinking = strings.TrimSpace(preference.Thinking)
	preference.ServiceTier = strings.TrimSpace(preference.ServiceTier)
	preference.ContextMode = strings.TrimSpace(preference.ContextMode)
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		current := next.RecentSessions[i].Preference
		if strings.TrimSpace(current.Provider) == preference.Provider &&
			strings.TrimSpace(current.Model) == preference.Model &&
			strings.TrimSpace(current.Thinking) == preference.Thinking &&
			strings.TrimSpace(current.ServiceTier) == preference.ServiceTier &&
			strings.TrimSpace(current.ContextMode) == preference.ContextMode {
			break
		}
		next.RecentSessions[i].Preference = preference
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.homeModel = next
	if a.home != nil {
		a.home.SetModel(next)
	}
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionBranch(sessionID, branch string) bool {
	sessionID = strings.TrimSpace(sessionID)
	branch = strings.TrimSpace(branch)
	if sessionID == "" || branch == "" {
		return false
	}
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		if strings.TrimSpace(next.RecentSessions[i].WorktreeBranch) == branch {
			break
		}
		next.RecentSessions[i].WorktreeBranch = branch
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.homeModel = next
	if a.home != nil {
		a.home.SetModel(next)
	}
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionWorkspacePath(sessionID, workspacePath string) bool {
	sessionID = strings.TrimSpace(sessionID)
	workspacePath = strings.TrimSpace(workspacePath)
	if sessionID == "" || workspacePath == "" {
		return false
	}
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		if strings.TrimSpace(next.RecentSessions[i].WorkspacePath) == workspacePath {
			break
		}
		next.RecentSessions[i].WorkspacePath = workspacePath
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.homeModel = next
	if a.home != nil {
		a.home.SetModel(next)
	}
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionPendingPermissions(sessionID string, pendingCount int) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if pendingCount < 0 {
		pendingCount = 0
	}
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		if next.RecentSessions[i].PendingPermissionCount == pendingCount {
			break
		}
		next.RecentSessions[i].PendingPermissionCount = pendingCount
		changed = true
		break
	}
	if !changed {
		for i := range next.BackgroundSessions {
			if strings.TrimSpace(next.BackgroundSessions[i].ChildSessionID) != sessionID {
				continue
			}
			if next.BackgroundSessions[i].PendingPermissions == pendingCount {
				break
			}
			next.BackgroundSessions[i].PendingPermissions = pendingCount
			changed = true
			break
		}
	}
	if !changed {
		return false
	}
	a.applyHomeModel(next)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) updateHomeSessionLifecycle(sessionID string, lifecycle client.SessionLifecycleSnapshot) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	lifecycle.SessionID = sessionID
	next := a.homeModel
	changed := false
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != sessionID {
			continue
		}
		if sameClientSessionLifecycle(next.RecentSessions[i].Lifecycle, &lifecycle) {
			break
		}
		copy := lifecycle
		next.RecentSessions[i].Lifecycle = &copy
		changed = true
		break
	}
	for i := range next.BackgroundSessions {
		if strings.TrimSpace(next.BackgroundSessions[i].ChildSessionID) != sessionID {
			continue
		}
		next.BackgroundSessions[i].Status = strings.TrimSpace(lifecycle.Phase)
		next.BackgroundSessions[i].LastUpdatedAtUnixMS = lifecycle.UpdatedAt
		next.BackgroundSessions[i].StartedAtUnixMS = lifecycle.StartedAt
		if lifecycle.Active && next.BackgroundSessions[i].Status == "" {
			next.BackgroundSessions[i].Status = "running"
		}
		if !lifecycle.Active && next.BackgroundSessions[i].Status == "" {
			next.BackgroundSessions[i].Status = emptyFallback(strings.TrimSpace(lifecycle.StopReason), "idle")
		}
		changed = true
		break
	}
	if !changed {
		return false
	}
	a.applyHomeModel(next)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func sameClientSessionLifecycle(left, right *client.SessionLifecycleSnapshot) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.TrimSpace(left.SessionID) == strings.TrimSpace(right.SessionID) &&
		strings.TrimSpace(left.RunID) == strings.TrimSpace(right.RunID) &&
		left.Active == right.Active &&
		strings.TrimSpace(left.Phase) == strings.TrimSpace(right.Phase) &&
		left.StartedAt == right.StartedAt &&
		left.EndedAt == right.EndedAt &&
		left.UpdatedAt == right.UpdatedAt &&
		left.Generation == right.Generation &&
		strings.TrimSpace(left.StopReason) == strings.TrimSpace(right.StopReason) &&
		strings.TrimSpace(left.Error) == strings.TrimSpace(right.Error) &&
		strings.TrimSpace(left.OwnerTransport) == strings.TrimSpace(right.OwnerTransport)
}

func (a *App) sessionSummaryByID(sessionID string) (model.SessionSummary, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return model.SessionSummary{}, false
	}
	for _, summary := range a.homeModel.RecentSessions {
		if strings.TrimSpace(summary.ID) != sessionID {
			continue
		}
		return summary, true
	}
	for _, summary := range a.homeModel.BackgroundSessions {
		if strings.TrimSpace(summary.ChildSessionID) != sessionID {
			continue
		}
		return model.SessionSummary{
			ID:                 strings.TrimSpace(summary.ChildSessionID),
			WorkspacePath:      strings.TrimSpace(summary.WorkspacePath),
			WorkspaceName:      strings.TrimSpace(summary.WorkspaceName),
			Title:              strings.TrimSpace(summary.ChildTitle),
			Mode:               strings.TrimSpace(summary.Status),
			WorktreeEnabled:    strings.TrimSpace(summary.WorktreeRootPath) != "",
			WorktreeRootPath:   strings.TrimSpace(summary.WorktreeRootPath),
			WorktreeBranch:     strings.TrimSpace(summary.WorktreeBranch),
			WorktreeBaseBranch: strings.TrimSpace(summary.WorktreeBaseBranch),
		}, true
	}
	return model.SessionSummary{}, false
}

func (a *App) loadSessionSummary(ctx context.Context, sessionID string) (model.SessionSummary, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return model.SessionSummary{}, errors.New("session id is required")
	}
	cached, hasCached := a.sessionSummaryByID(sessionID)
	if a.api == nil {
		if hasCached {
			return cached, nil
		}
		return model.SessionSummary{}, errors.New("api client is not configured")
	}
	live, err := a.api.GetSession(ctx, sessionID)
	if err != nil {
		if hasCached {
			return cached, nil
		}
		return model.SessionSummary{}, err
	}
	summary := modelSessionSummaryFromClient(live)
	if hasCached {
		summary = mergeHomeSessionSummary(cached, summary)
	}
	return summary, nil
}

func (a *App) handleGlobalKey(ev *tcell.EventKey) bool {
	keybinds := a.activeKeyBindings()
	if a.route == "chat" && a.chat != nil && a.chat.PermissionModalVisible() {
		return false
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		if a.v3Chat.PendingPermissionVisible() {
			return false
		}
		if a.v3Chat.PlanModalVisible() && ev != nil && ev.Key() == tcell.KeyCtrlC {
			return false
		}
	}
	if a.home != nil && a.home.OnboardingVisible() {
		return false
	}
	if keybinds.Match(ev, ui.KeybindGlobalOpenAgents) {
		if a.route == "chat" && a.chat != nil && a.chat.PermissionModalVisible() {
			return false
		}
		if a.route == "chat" && a.chat != nil {
			a.openAgentsModal()
			return false
		}
		if a.route == "v3chat" && a.v3Chat != nil {
			a.openAgentsModal()
			return false
		}
		if a.route == "chat" {
			a.route = "home"
			a.chat = nil
		}
		if a.route == "home" {
			a.home.SetPasteActive(a.pasteActive)
			a.openAgentsModal()
			return false
		}
	}
	if keybinds.Match(ev, ui.KeybindHomeOpenSessions) {
		if a.route == "chat" || a.route == "v3chat" {
			a.handleSessionsCommand(nil)
			return true
		}
		if a.route == "home" && a.home != nil {
			if a.home.SessionsModalVisible() {
				a.home.HideSessionsModal()
				a.home.SetStatus("session manager closed")
				return true
			}
			if a.home.AuthModalVisible() ||
				a.home.VaultModalVisible() ||
				a.home.WorkspaceModalVisible() ||
				a.home.WorktreesModalVisible() ||
				a.home.ModelsModalVisible() ||
				a.home.AgentsModalVisible() ||
				a.home.VoiceModalVisible() ||
				a.home.ThemeModalVisible() ||
				a.home.KeybindsModalVisible() {
				return true
			}
			a.openHomeSessionsModal("")
			return true
		}
	}
	if keybinds.Match(ev, ui.KeybindGlobalWorkspaceSelect) {
		if a.homeInteractionActive() {
			return true
		}
		if a.workspaceSwitchHotkeyBlocked() {
			return true
		}
		a.showWorkspaceSelector()
		return true
	}
	for slot := 1; slot <= ui.WorkspaceSlotCount; slot++ {
		id, ok := ui.WorkspaceSlotKeybindID(slot)
		if !ok || !keybinds.Match(ev, id) {
			continue
		}
		if a.homeInteractionActive() {
			return true
		}
		if a.workspaceSwitchHotkeyBlocked() {
			return true
		}
		a.activateWorkspaceSlot(slot)
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalCycleProfiles) {
		if a.homeInteractionActive() {
			return false
		}
		if a.route != "home" && a.route != "v3chat" {
			return false
		}
		a.cycleHomeModelProfile()
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalCycleThinking) {
		if a.route == "home" && a.homeInteractionActive() {
			return true
		}
		a.cycleThinkingLevel()
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalVoiceInput) {
		if a.route == "home" &&
			(a.home.AuthModalVisible() ||
				a.home.VaultModalVisible() ||
				a.home.WorkspaceModalVisible() ||
				a.home.WorktreesModalVisible() ||
				a.home.ModelsModalVisible() ||
				a.home.AgentsModalVisible() ||
				a.home.VoiceModalVisible() ||
				a.home.ThemeModalVisible() ||
				a.home.KeybindsModalVisible()) {
			return true
		}
		if a.route == "chat" && a.chat != nil && a.chat.PermissionModalVisible() {
			return true
		}
		a.captureVoiceInput()
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalShowBackground) {
		if a.route == "chat" {
			a.route = "home"
			a.chat = nil
			return true
		}
		if a.route == "v3chat" {
			a.closeV3Chat()
			a.route = "home"
			return true
		}
	}

	if keybinds.Match(ev, ui.KeybindGlobalQuit) {
		if a.route == "v3chat" && a.v3Chat != nil && a.v3Chat.ConsumeQuitScrollbackJump() {
			return true
		}
		if a.route == "v3chat" && a.v3Chat != nil && strings.TrimSpace(a.v3Chat.InputValue()) != "" {
			a.v3Chat.ClearInput()
			return true
		}
		if a.route == "chat" && a.chat != nil && strings.TrimSpace(a.chat.InputValue()) != "" {
			a.chat.ClearInput()
			return true
		}
		if a.route == "home" && a.home != nil && strings.TrimSpace(a.home.PromptValue()) != "" {
			a.home.ClearPrompt()
			return true
		}
		if a.route == "chat" && a.chat != nil && a.chat.ConsumeQuitScrollbackJump() {
			return true
		}
		a.requestQuit()
		return true
	}

	if keybinds.Match(ev, ui.KeybindChatEscape) {
		if a.route == "chat" {
			if a.chat != nil && a.chat.HandleEscape() {
				return true
			}
		}
	}

	if keybinds.Match(ev, ui.KeybindGlobalReloadHome) && a.route == "home" {
		a.home.SetStatus("reloading from swarmd...")
		a.queueReload(false)
		return true
	}

	if keybinds.Match(ev, ui.KeybindGlobalToggleMouse) {
		a.applyMouseSetting(!a.config.Input.MouseEnabled)
		return true
	}
	return false
}

func (a *App) handleHomeKey(ev *tcell.EventKey) bool {
	if a.home.OnboardingVisible() {
		return false
	}
	if a.home.AlertsModalVisible() ||
		a.home.SessionsModalVisible() ||
		a.home.AuthModalVisible() ||
		a.home.VaultModalVisible() ||
		a.home.WorkspaceModalVisible() ||
		a.home.WorktreesModalVisible() ||
		a.home.ModelsModalVisible() ||
		a.home.AgentsModalVisible() ||
		a.home.VoiceModalVisible() ||
		a.home.ThemeModalVisible() ||
		a.home.KeybindsModalVisible() {
		return false
	}
	if !a.activeKeyBindings().Match(ev, ui.KeybindHomePromptSubmit) {
		return false
	}

	prompt := strings.TrimSpace(a.home.PromptValue())
	if prompt == "" {
		return false
	}

	if strings.HasPrefix(prompt, "/") {
		if a.home.AcceptCommandPaletteEnter() {
			prompt = strings.TrimSpace(a.home.PromptValue())
		}
		if prompt == "" || !strings.HasPrefix(prompt, "/") {
			return true
		}
		a.executeCommand(prompt)
		a.home.ClearPrompt()
		return true
	}

	a.home.ClearCommandOverlay()
	if err := a.openChatSession("", prompt); err != nil {
		a.home.SetStatus(fmt.Sprintf("open chat failed: %v", err))
		return true
	}
	return true
}

func (a *App) handleChatKey(ev *tcell.EventKey) bool {
	if a.chat == nil {
		return false
	}
	if ev != nil && ev.Key() == tcell.KeyCtrlP && strings.TrimSpace(a.chat.InputValue()) == "" {
		a.handlePlanCommand(nil)
		if status := strings.TrimSpace(a.home.Status()); status != "" {
			a.chat.SetStatus(status)
		}
		return true
	}
	if !a.activeKeyBindings().Match(ev, ui.KeybindChatSubmit) {
		return false
	}

	prompt := strings.TrimSpace(a.chat.InputValue())
	if prompt == "" || !strings.HasPrefix(prompt, "/") {
		return false
	}
	if a.chat.AcceptCommandPaletteEnter() {
		prompt = strings.TrimSpace(a.chat.InputValue())
		if prompt == "" || !strings.HasPrefix(prompt, "/") {
			return true
		}
	}

	a.executeCommand(prompt)
	if a.route != "chat" || a.chat == nil {
		return true
	}

	overlayLines := a.home.CommandOverlayLines()
	if len(overlayLines) > 0 {
		a.chat.AppendSystemMessage(strings.Join(overlayLines, "\n"))
	}

	if status := strings.TrimSpace(a.home.Status()); status != "" {
		a.chat.SetStatus(status)
	}
	a.chat.ClearInput()
	return true
}

func (a *App) executeCommand(raw string) {
	line := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		a.home.SetStatus("type /help for commands")
		return
	}
	cmd := strings.ToLower(fields[0])
	args := fields[1:]
	if a.home != nil && a.home.OnboardingVisible() {
		switch cmd {
		case "auth", "quit", "exit", "help":
		default:
			a.home.ClearCommandOverlay()
			a.home.SetStatus("Complete required onboarding before using other commands.")
			return
		}
	}
	if a.vault.Enabled && !a.vault.Unlocked {
		switch cmd {
		case "help", "quit", "exit", "vault":
		default:
			a.home.ClearCommandOverlay()
			a.home.SetStatus("Vault is locked. Unlock it with /vault before using other commands.")
			return
		}
	}

	switch cmd {
	case "help":
		a.showHelp()
	case "home":
		a.home.ClearCommandOverlay()
		if a.route == "v3chat" && a.v3Chat != nil {
			a.closeV3Chat()
			a.route = "home"
			a.home.SetStatus("home")
			return
		}
		if a.route != "chat" || a.chat == nil {
			a.home.SetStatus("/home is available in chat only")
			return
		}
		a.route = "home"
		a.chat = nil
		a.home.SetStatus("home")
	case "reload":
		a.home.ClearCommandOverlay()
		a.home.SetStatus("reloading from swarmd...")
		a.queueReload(false)
	case "rebuild":
		a.handleRebuildCommand()
	case "quit", "exit":
		a.requestQuit()
		a.home.ClearCommandOverlay()
		a.home.SetStatus("exiting swarmtui")
	case "sessions":
		a.handleSessionsCommand(args)
	case "alerts", "notifications":
		a.handleAlertsCommand(args)
	case "new":
		a.handleNewCommand(raw)
	case "plan":
		a.handlePlanCommand(args)
	case "task":
		a.handleTaskCommand(args)
	case "archive":
		a.handleArchiveCommand(args)
	case "compact":
		if (a.route == "chat" && a.chat != nil) || (a.route == "v3chat" && a.v3Chat != nil) {
			a.handleCompactCommand(args)
			break
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus("unknown command: /compact")
	case "commit":
		a.handleCommitCommand(args)
	case "codex":
		a.handleCodexCommand(args)
	case "workspace":
		a.handleWorkspaceCommand(args)
	case "workspaces":
		a.handleWorkspaceCommand(args)
	case "add-dir":
		a.handleAddDirectoryCommand(args)
	case "permissions":
		a.handlePermissionsCommand(args)
	case "output":
		a.handleOutputCommand(args)
	case "worktree":
		a.handleWorktreePrimerCommand(raw)
	case "worktrees":
		a.handleWorktreesCommand(args)
	case "wt":
		if _, matched, err := v3chat.ParseWorktreeCommand(raw); matched && err == nil {
			a.handleWorktreePrimerCommand(raw)
		} else {
			a.handleWorktreesCommand(args)
		}
	case "mode":
		a.handleModeCommand(args)
	case "profiles":
		a.openProfilesModal()
	case "agents", "agent":
		a.handleAgentsCommand(args)
	case "auth":
		a.handleAuthCommand(args)
	case "vault":
		a.handleVaultCommand(args)
	case "header":
		a.handleHeaderCommand(args)
	case "thinking":
		a.handleThinkingCommand(args)
	case "theme", "themes":
		a.handleThemesCommand(args)
	case "mouse":
		a.handleMouseCommand(args)
	case "voice":
		a.handleVoiceCommand(args)
	case "swarm":
		a.handleSwarmCommand(args)
	case "update":
		a.handleUpdateCommand(args)
	case "keybinds", "keys":
		a.handleKeybindsCommand(args)
	case "copy":
		a.handleCopyCommand(args)
	default:
		a.home.ClearCommandOverlay()
		if suggestion := suggestKnownCommand(cmd, a.startupDevMode()); suggestion != "" {
			a.home.SetStatus(fmt.Sprintf("unknown command: /%s (did you mean %s?)", cmd, suggestion))
			return
		}
		a.home.SetStatus(fmt.Sprintf("unknown command: /%s", cmd))
	}
}

func (a *App) showHelp() {
	keybinds := a.activeKeyBindings()
	lines := []string{
		fmt.Sprintf("/sessions   (open session manager; shortcut %s)", keybinds.Label(ui.KeybindHomeOpenSessions)),
		"/new [<prompt>]   (open a local session draft; a prompt starts immediately)",
		"/new plan [<prompt>]   (open a direct Plan-mode draft or start)",
		"/new worktree|wp [<prompt>]   (route an explicit worktree start)",
		"/home   (return to home from chat)",
		"/plan   (show or close the existing session plan)",
		"/task <request>   (queue a durable AI task in automatic mode)",
		"/task plan <request>   (queue a durable AI task in plan mode)",
		"/commit [instructions]   (launch memory agent in background to review diffs and commit)",
		"/git   (show authoritative Git status for the active workspace)",
		"/codex [refresh]   (Codex account usage and reset credits)",
		"/workspace   (open workspace manager)",
		"/workspaces   (alias for /workspace)",
		"/workspace save [path|#n]   (open workspace setup)",
		"/add-dir [path]   (open workspace linked-directory flow)",
		"/workspace scan [query]",
		"/output   (open full bash output viewer)",
		"/permissions [on|off]   (toggle global permission prompts)",
		"/permissions show   (show global permission policy)",
		"/permissions allow tool <name>",
		"/permissions allow bash-prefix <command>",
		"/permissions deny phrase <text>",
		"/permissions ask tool <name>",
		"/permissions remove <rule-id>",
		"/permissions reset",
		"/permissions explain <tool> [arguments json or text]",
		"Permissions modal: b toggles global permissions (OFF requires confirmation)",
		"/worktree on|off   (switch the current local draft between direct and routed worktree start)",
		"/wt on|off   (short local draft control; other forms use /worktrees management)",
		"/worktrees   (open worktrees menu)",
		"/worktrees new   (create a worktree session with title and editable branch)",
		"/worktrees [new|open|off|status|branch <name>]",
		"/agents   (open agent cards and model setup)",

		"/mode [plan|action|status]   (Plan on/off for new chats)",
		"/profiles   (quick-switch the saved model profile used by new sessions)",
		fmt.Sprintf("%s   (open agents manager modal)", keybinds.Label(ui.KeybindGlobalOpenAgents)),
		fmt.Sprintf("%s   (cycle saved model profiles)", keybinds.Label(ui.KeybindGlobalCycleProfiles)),
		"/themes   (open theme modal with live preview)",
		"/themes [open|list|set|next|prev|status|create|edit|delete|slots]",
		"/themes create <id> [from <theme>]",
		"/themes edit <id> <slot> <#RRGGBB>",
		"/themes delete <id>",
		"/header [on|off|toggle|status]   (chat header visibility)",
		"/swarm [set <name>|name <name>|<name>]   (change primary swarm display name)",
		updateHelpLine(a.startupDevMode()),
		"/thinking [on|off|toggle|status]   (show or hide reasoning/thinking tags)",
		"/mouse [on|off|toggle|status]   (mouse click capture)",
		fmt.Sprintf("%s   (toggle mouse click capture)", keybinds.Label(ui.KeybindGlobalToggleMouse)),
		"/voice [open|device <id>|stt [provider] [model]|profile [list|use <id>|upsert <id> <adapter> [model]|whisper [id] [model]|delete <id>]|tts [provider] [voice]|test [seconds]]",
		fmt.Sprintf("%s   (record voice + transcribe into input)", keybinds.Label(ui.KeybindGlobalVoiceInput)),
		"/keybinds   (open keybind manager modal)",
		"/keybinds list",
		"/keybinds reset [all]",
		"/copy [n]   (copy chat snapshot or assistant tagged copy block)",
		"/auth   (open auth manager modal)",
		"/vault   (status, unlock, export, or import the local vault credentials)",
		"/auth status",
		"/auth key <provider> <api_key>",
		"/reload   (hot reload home state)",
		"/rebuild   (run scripts/rebuild.sh for active lane, then exit)",
		"/quit   (exit swarmtui)",
	}
	a.home.SetCommandOverlay(lines)
	a.home.SetStatus("command palette loaded")
}

func (a *App) handleCopyCommand(args []string) {
	a.home.ClearCommandOverlay()
	isV3Chat := a.route == "v3chat" && a.v3Chat != nil
	if !isV3Chat && a.chat == nil {
		a.home.SetStatus("/copy is available in chat sessions only")
		return
	}
	payload := ""
	successStatus := "copied chat snapshot to clipboard"
	if len(args) > 0 {
		index, ok := copyblock.ParseIndexArg(args)
		if !ok {
			a.home.SetStatus("usage: /copy [number]")
			return
		}
		copyText := ""
		if isV3Chat {
			copyText, ok = a.v3Chat.CopyBlockText(index)
		} else {
			copyText, ok = a.chat.CopyBlockText(index)
		}
		if !ok {
			a.home.SetStatus(fmt.Sprintf("/copy %d not found", index))
			return
		}
		payload = copyText
		successStatus = copyblock.PreviewStatus(index, copyText)
	} else if isV3Chat {
		payload = a.v3Chat.ClipboardText()
	} else {
		payload = a.chat.ClipboardText()
	}

	if err := copyTextToClipboard(payload); err != nil {
		a.home.SetStatus(fmt.Sprintf("copy failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("copy failed: %v", err))
		return
	}

	a.home.SetStatus(successStatus)
	if isV3Chat {
		a.v3Chat.SetStatus(successStatus)
	}
	a.showToast(ui.ToastSuccess, successStatus)
}

func (a *App) handleOutputCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) > 0 {
		a.home.SetStatus("usage: /output")
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		if !a.v3Chat.ToggleLatestBashOutput() {
			a.home.SetStatus("no bash output available")
			return
		}
		if status := strings.TrimSpace(a.v3Chat.Status()); status != "" {
			a.home.SetStatus(status)
		}
		return
	}
	if a.chat == nil {
		a.home.SetStatus("/output is available in chat sessions only")
		return
	}
	if !a.chat.ToggleInlineBashOutputExpanded() {
		a.home.SetStatus("no bash output available")
		return
	}
	if status := strings.TrimSpace(a.chat.Status()); status != "" {
		a.home.SetStatus(status)
	}
}

func (a *App) handleRebuildCommand() {
	a.home.ClearCommandOverlay()
	lane := strings.TrimSpace(os.Getenv("SWARM_LANE"))
	if lane == "" {
		lane = "main"
	}
	a.home.SetStatus(fmt.Sprintf("rebuilding swarmtui (lane=%s)...", lane))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rebuildPath, err := resolveRebuildBinaryPath()
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("rebuild failed: %v", err))
		return
	}
	cmd := exec.CommandContext(ctx, rebuildPath, lane)
	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			a.home.SetStatus(fmt.Sprintf("rebuild failed: %v", err))
			return
		}
		lines := strings.Split(out, "\n")
		if len(lines) > 4 {
			lines = lines[len(lines)-4:]
		}
		a.home.SetStatus(fmt.Sprintf("rebuild failed: %v (%s)", err, strings.Join(lines, " | ")))
		return
	}

	a.home.SetStatus(fmt.Sprintf("rebuild complete for lane=%s; exiting swarmtui", lane))
	a.requestQuit()
}

func (a *App) handleSessionsCommand(args []string) {
	query := strings.TrimSpace(strings.Join(args, " "))
	if a.route == "chat" && a.chat != nil {
		a.home.ClearCommandOverlay()
		if err := a.openChatSessionsPalette(query); err != nil {
			a.home.SetStatus(fmt.Sprintf("/sessions failed: %v", err))
		}
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		a.home.ClearCommandOverlay()
		if err := a.queueSessionManagerOpen(query, "v3chat"); err != nil {
			a.home.SetStatus(fmt.Sprintf("/sessions failed: %v", err))
		}
		return
	}
	a.openHomeSessionsModal(query)
}

func (a *App) handleAlertsCommand(args []string) {
	query := strings.TrimSpace(strings.Join(args, " "))
	a.home.ClearCommandOverlay()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	notifications, err := a.api.ListNotifications(ctx, 200, "")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/alerts failed: %v", err))
		return
	}
	items := alertModalItemsFromNotifications(notifications)
	if !a.home.OpenAlertsModal(items, query) {
		a.home.SetStatus("alerts unavailable while another modal is open")
		return
	}
	a.setSwarmNotificationCount(unreadNotificationCount(notifications))
}

func (a *App) clearAlertsFromModal() {
	if a == nil || a.api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	deleted, err := a.api.ClearNotifications(ctx, "")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("clear alerts failed: %v", err))
		return
	}
	a.home.SetAlertsModalItems(nil)
	a.setSwarmNotificationCount(0)
	a.home.SetStatus(fmt.Sprintf("cleared %d alerts", deleted))
}

func alertModalItemsFromNotifications(records []client.NotificationRecord) []ui.AlertModalItem {
	items := make([]ui.AlertModalItem, 0, len(records))
	for _, record := range records {
		items = append(items, ui.AlertModalItem{
			ID:            strings.TrimSpace(record.ID),
			Title:         strings.TrimSpace(record.Title),
			Body:          strings.TrimSpace(record.Body),
			Status:        strings.TrimSpace(record.Status),
			Severity:      strings.TrimSpace(record.Severity),
			Category:      strings.TrimSpace(record.Category),
			ToolName:      strings.TrimSpace(record.ToolName),
			Requirement:   strings.TrimSpace(record.Requirement),
			SessionID:     strings.TrimSpace(record.SessionID),
			SessionTitle:  strings.TrimSpace(record.SessionTitle),
			SessionLabel:  strings.TrimSpace(record.SessionLabel),
			WorkspacePath: strings.TrimSpace(record.WorkspacePath),
			WorkspaceName: strings.TrimSpace(record.WorkspaceName),
			OriginLabel:   strings.TrimSpace(record.OriginLabel),
			UpdatedAgo:    formatAgo(firstNonZeroInt64(record.UpdatedAt, record.CreatedAt)),
		})
	}
	return items
}

func unreadNotificationCount(records []client.NotificationRecord) int {
	count := 0
	for _, record := range records {
		if record.ReadAt <= 0 {
			count++
		}
	}
	return count
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (a *App) handleNewCommand(raw string) {
	command, matched := v3chat.ParseNewCommand(raw)
	if !matched {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /new [worktree|plan|wp] [<prompt>]")
		return
	}
	intent := a.home.SessionIntent()
	intent.InitialPrompt = strings.TrimSpace(command.Prompt)
	intent.Mode = map[bool]string{true: "plan", false: "auto"}[command.PlanModeRequested]
	intent.WorktreeRequested = command.ManagedWorktreeRequested
	route := a.selectedChatRouteForWorkspace(a.activeContextPath())
	if err := a.openNewV3Chat(intent, route, ""); err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("/new failed: %v", err))
	}
}

func (a *App) handleWorktreePrimerCommand(raw string) {
	command, matched, err := v3chat.ParseWorktreeCommand(raw)
	if !matched {
		return
	}
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(err.Error())
		return
	}
	if a.route == "home" {
		a.home.ClearCommandOverlay()
		a.home.SetWorktreeRequested(command.Enabled)
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil && a.v3Chat.Runtime() != nil && a.v3Chat.Runtime().Store() != nil {
		state := a.v3Chat.Runtime().Store().Snapshot()
		if strings.TrimSpace(state.Session.ID) != "" {
			a.home.SetStatus("worktree priming is available only on home or a new session draft")
			a.v3Chat.SetStatus(a.home.Status())
			return
		}
		intent := a.home.SessionIntent()
		intent.InitialPrompt = ""
		intent.Mode = emptyFallback(strings.TrimSpace(state.Session.Mode), intent.Mode)
		intent.WorktreeRequested = command.Enabled
		route := a.selectedChatRouteForWorkspace(a.activeContextPath())
		if err = a.openNewV3Chat(intent, route, ""); err != nil {
			a.home.SetStatus(err.Error())
			a.v3Chat.SetStatus(a.home.Status())
			return
		}
		status := "Worktree: " + map[bool]string{true: "on", false: "off"}[command.Enabled]
		a.home.SetStatus(status)
		a.v3Chat.SetStatus(status)
		return
	}
	a.home.ClearCommandOverlay()
	a.home.SetStatus("worktree priming is available only on home or a new session draft")
}

func (a *App) handlePlanCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) != 0 {
		a.home.SetStatus("usage: /plan")
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		if a.v3Chat.PlanModalVisible() {
			a.v3Chat.ClosePlanModal()
			a.v3Chat.SetStatus("current plan closed")
			return
		}
		a.showV3CurrentPlan()
		return
	}
	if a.route != "chat" || a.chat == nil {
		a.home.SetStatus("/plan is available in chat only")
		return
	}
	if a.chat.CurrentPlanModalVisible() {
		a.chat.CloseCurrentPlanModal()
		a.home.SetStatus("current plan closed")
		return
	}
	sessionID := strings.TrimSpace(a.chat.SessionID())
	if sessionID == "" {
		a.home.SetStatus("session id is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	plan, ok, err := a.api.GetActiveSessionPlan(ctx, sessionID)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/plan failed: %v", err))
		return
	}
	if !ok {
		a.home.SetStatus("current plan: no active plan")
		return
	}
	if !a.chat.OpenCurrentPlanModal(ui.ChatSessionPlan{
		ID:            strings.TrimSpace(plan.ID),
		Title:         strings.TrimSpace(plan.Title),
		Plan:          plan.Plan,
		Document:      plan.Document,
		Status:        strings.TrimSpace(plan.Status),
		ApprovalState: strings.TrimSpace(plan.ApprovalState),
		Active:        true,
		CreatedAt:     plan.CreatedAt,
		UpdatedAt:     plan.UpdatedAt,
		Version:       plan.Version,
	}) {
		a.home.SetStatus("current plan modal is unavailable while another modal is open")
		return
	}
	a.home.SetStatus(fmt.Sprintf("current plan: %s · %s", emptyFallback(strings.TrimSpace(plan.Title), "untitled"), strings.TrimSpace(plan.ID)))
}

func (a *App) handleArchiveCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) != 0 {
		a.home.SetStatus("usage: " + archiveCommandUsage)
		return
	}
	if a.api == nil {
		a.home.SetStatus("/archive failed: api client is not configured")
		return
	}
	var sessionID string
	v3Page, legacyPage := a.v3Chat, a.chat
	switch {
	case a.route == "v3chat" && v3Page != nil && v3Page.Runtime() != nil && v3Page.Runtime().Store() != nil:
		state := v3Page.Runtime().Store().Snapshot()
		if _, active := v3chat.SelectActiveRun(state); active {
			a.home.SetStatus("/archive unavailable while a run is active")
			return
		}
		sessionID = strings.TrimSpace(state.Session.ID)
		v3Page.SetStatus("archiving session…")
	case a.route == "chat" && legacyPage != nil:
		if legacyPage.RunInProgress() {
			a.home.SetStatus("/archive unavailable while a run is active")
			return
		}
		sessionID = strings.TrimSpace(legacyPage.SessionID())
		legacyPage.SetStatus("archiving session…")
	default:
		a.home.SetStatus("unknown command: " + archiveCommandUsage)
		return
	}
	if sessionID == "" {
		a.home.SetStatus("/archive failed: session id is unavailable")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := a.api.ArchiveSessionV3(ctx, sessionID); err != nil {
			message := fmt.Sprintf("/archive failed: %v", err)
			switch {
			case a.route == "v3chat" && a.v3Chat == v3Page:
				v3Page.SetStatus(message)
				a.requestV3ChatRender()
			case a.route == "chat" && a.chat == legacyPage:
				legacyPage.SetStatus(message)
				a.requestChatRender()
			}
			return
		}
		switch {
		case a.route == "v3chat" && a.v3Chat == v3Page:
			a.closeV3Chat()
		case a.route == "chat" && a.chat == legacyPage:
			a.chat = nil
		default:
			return
		}
		a.route = "home"
		a.home.SetStatus("session archived")
		a.requestV3ChatRender()
	}()
}

func (a *App) handleCompactCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) != 0 {
		a.home.SetStatus("usage: " + compactCommandUsage)
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		a.home.SetStatus("")
		a.v3Chat.Compact()
		return
	}
	if a.route != "chat" || a.chat == nil {
		a.home.SetStatus("compact command is available in chat: " + compactCommandUsage)
		return
	}
	if a.api == nil {
		a.home.SetStatus("/compact failed: api client is not configured")
		return
	}
	sessionID := strings.TrimSpace(a.chat.SessionID())
	if sessionID == "" {
		a.home.SetStatus("/compact failed: session id is unavailable")
		return
	}
	if a.chat.RunInProgress() {
		a.home.SetStatus("/compact ignored (run already active)")
		return
	}
	a.chat.SetStatus("compacting context")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := a.api.CompactSessionV3(ctx, sessionID, client.SessionV3CompactOptions{})
		if err != nil {
			a.chat.SetStatus(fmt.Sprintf("/compact failed: %v", err))
			a.requestChatRender()
			return
		}
		a.chat.SetStatus("context compacted")
		a.requestChatRender()
	}()
	a.home.SetStatus("compacting session context")
}

func (a *App) handleCommitCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.route != "chat" || a.chat == nil {
		a.home.SetStatus("commit command is available in chat: /commit [instructions]")
		return
	}
	if a.api == nil {
		a.home.SetStatus("/commit failed: api client is not configured")
		return
	}
	parentSessionID := strings.TrimSpace(a.chat.SessionID())
	if parentSessionID == "" {
		a.home.SetStatus("/commit failed: session id is unavailable")
		return
	}

	instructions := strings.TrimSpace(strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	parentSummary, err := a.loadSessionSummary(ctx, parentSessionID)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/commit failed: load parent session: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("/commit failed: load parent session: %v", err))
		return
	}
	a.upsertHomeSessionSummary(parentSummary)

	childSummary, err := a.createBackgroundCommitSession(ctx, parentSessionID, parentSummary, instructions)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/commit failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("/commit failed: %v", err))
		return
	}
	launch, err := a.startBackgroundCommitRun(ctx, childSummary, instructions)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/commit failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("/commit failed: %v", err))
		return
	}

	execCtx := a.commitExecutionContext(childSummary)
	launchRecord := model.BackgroundSessionSummary{
		ChildSessionID:      strings.TrimSpace(childSummary.ID),
		ParentSessionID:     parentSessionID,
		ParentTitle:         strings.TrimSpace(parentSummary.Title),
		ChildTitle:          strings.TrimSpace(childSummary.Title),
		TargetKind:          "background",
		TargetName:          commitBackgroundAgentName,
		Status:              "running",
		PendingPermissions:  0,
		WorkspacePath:       strings.TrimSpace(execCtx.WorkspacePath),
		WorkspaceName:       strings.TrimSpace(childSummary.WorkspaceName),
		CWD:                 strings.TrimSpace(execCtx.CWD),
		WorktreeMode:        strings.TrimSpace(execCtx.WorktreeMode),
		WorktreeRootPath:    strings.TrimSpace(execCtx.WorktreeRootPath),
		WorktreeBranch:      strings.TrimSpace(execCtx.WorktreeBranch),
		WorktreeBaseBranch:  strings.TrimSpace(execCtx.WorktreeBaseBranch),
		LaunchMode:          "background",
		Instructions:        instructions,
		Background:          true,
		StartedAtUnixMS:     time.Now().UnixMilli(),
		LastUpdatedAtUnixMS: time.Now().UnixMilli(),
	}
	childSummary.Metadata = mergeMetadataMaps(childSummary.Metadata, map[string]any{
		"launch_mode": "background",
		"background":  true,
		"target_kind": launchRecord.TargetKind,
		"target_name": launchRecord.TargetName,
	})
	a.setBackgroundSessionSummary(launchRecord)
	a.upsertHomeSessionSummary(childSummary)
	a.updateHomeSessionLifecycle(childSummary.ID, client.SessionLifecycleSnapshot{
		SessionID:      childSummary.ID,
		RunID:          strings.TrimSpace(launch.RunID),
		Active:         true,
		Phase:          "running",
		UpdatedAt:      time.Now().UnixMilli(),
		StartedAt:      time.Now().UnixMilli(),
		OwnerTransport: strings.TrimSpace(launch.OwnerTransport),
	})
	a.refreshBackgroundSessions()

	status := fmt.Sprintf("background /commit launched: %s", emptyFallback(strings.TrimSpace(childSummary.Title), childSummary.ID))
	a.home.SetStatus(status)
	a.chat.SetStatus(status)
	a.chat.ShowToast(ui.ToastSuccess, status)
}

const tuiRetiredSessionAPIMessage = "TUI v1/v2 session APIs are retired; use a v3 session"

func errTUIRetiredSessionAPI(operation string) error {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return errors.New(tuiRetiredSessionAPIMessage)
	}
	return fmt.Errorf("%s: %s", operation, tuiRetiredSessionAPIMessage)
}

func requireTUIV3SessionAPI(sessionAPI, operation string) error {
	if strings.EqualFold(strings.TrimSpace(sessionAPI), "v3") {
		return nil
	}
	label := strings.TrimSpace(sessionAPI)
	if label == "" {
		label = "legacy"
	}
	return fmt.Errorf("%s (session_api=%s)", errTUIRetiredSessionAPI(operation), label)
}

func (a *App) openChatSession(titleSeed, initialPrompt string) error {
	return a.openChatSessionWithWorktree(titleSeed, initialPrompt, "")
}

func (a *App) openChatSessionWithWorktree(titleSeed, initialPrompt, worktreeBranchSuffix string) error {
	if a.api == nil {
		return errors.New("api client is not configured")
	}
	intent := a.home.SessionIntent()
	intent.Title = strings.TrimSpace(titleSeed)
	intent.InitialPrompt = strings.TrimSpace(initialPrompt)
	route := a.selectedChatRouteForWorkspace(a.activeContextPath())
	return a.openNewV3Chat(intent, route, worktreeBranchSuffix)
}

func (a *App) openExistingSession(summary model.SessionSummary) error {
	return a.openExistingV3Chat(summary)
}

func (a *App) openSessionSummary(summary model.SessionSummary, initialPrompt string) error {
	sessionID := strings.TrimSpace(summary.ID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	summary.WorkspaceName = a.contextDisplayNameForPath(summary.WorkspacePath, summary.WorkspaceName)
	title := strings.TrimSpace(summary.Title)
	if title == "" {
		title = chatTitleFromPrompt(initialPrompt)
	}
	if title == "" {
		title = sessionID
	}
	modelProvider := ""
	modelName := ""
	thinkingLevel := ""
	serviceTier := ""
	contextMode := ""
	contextWindow := 0
	var initialUsageSummary *ui.ChatUsageSummary
	var hydratedSession *client.SessionV3Hydrated
	workspaceScope := ""
	cwdScope := ""
	if a.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		scopePath := strings.TrimSpace(summary.WorkspacePath)
		if scopePath == "" {
			scopePath = strings.TrimSpace(a.activeContextPath())
		}
		if scopePath == "" {
			scopePath = strings.TrimSpace(a.startupCWD)
		}
		if strings.TrimSpace(a.activeWorkspacePath()) != "" {
			workspaceScope = scopePath
		} else {
			cwdScope = scopePath
		}
		hydrated, err := a.api.GetSessionV3TUI(ctx, sessionID, workspaceScope, cwdScope)
		cancel()
		if err != nil {
			return fmt.Errorf("hydrate v3 session: %w", err)
		}
		if strings.TrimSpace(hydrated.Session.ID) != "" {
			hydratedSession = &hydrated
			summary = mergeHomeSessionSummary(summary, modelSessionSummaryFromClient(hydrated.Session))
		}
		if err := requireTUIV3SessionAPI(summary.SessionAPI, "open hydrated session"); err != nil {
			return err
		}
		if len(hydrated.PendingPermissions) > 0 {
			summary.PendingPermissionCount = len(hydrated.PendingPermissions)
		}
		if hydrated.ActiveRunIntent != nil {
			summary.ActiveRunIntent = cloneClientSessionV3RunIntent(hydrated.ActiveRunIntent)
			if lifecycle := v3RunIntentSessionLifecycle(summary.ID, hydrated.ActiveRunIntent); lifecycle != nil {
				summary.Lifecycle = lifecycle
			}
		}
		modelProvider = strings.TrimSpace(hydrated.Preference.Provider)
		modelName = strings.TrimSpace(hydrated.Preference.Model)
		thinkingLevel = strings.TrimSpace(hydrated.Preference.Thinking)
		serviceTier = strings.TrimSpace(hydrated.Preference.ServiceTier)
		contextMode = strings.TrimSpace(hydrated.Preference.ContextMode)
		contextWindow = hydrated.ContextWindow
		initialUsageSummary = convertClientUsageSummary(hydrated.UsageSummary)
	} else if err := requireTUIV3SessionAPI(summary.SessionAPI, "open session"); err != nil {
		return err
	}
	if modelProvider == "" && modelName == "" {
		modelProvider = strings.TrimSpace(summary.Preference.Provider)
		modelName = strings.TrimSpace(summary.Preference.Model)
		thinkingLevel = strings.TrimSpace(summary.Preference.Thinking)
		serviceTier = strings.TrimSpace(summary.Preference.ServiceTier)
		contextMode = strings.TrimSpace(summary.Preference.ContextMode)
	}
	if hydratedTitle := strings.TrimSpace(summary.Title); hydratedTitle != "" {
		title = hydratedTitle
	}
	openedSummary := model.SessionSummary{
		ID:                         sessionID,
		WorkspacePath:              strings.TrimSpace(summary.WorkspacePath),
		WorkspaceName:              strings.TrimSpace(summary.WorkspaceName),
		Title:                      title,
		Mode:                       strings.TrimSpace(summary.Mode),
		Metadata:                   cloneMetadataMap(summary.Metadata),
		PendingPermissionCount:     summary.PendingPermissionCount,
		HasActivePlan:              summary.HasActivePlan,
		ActivePlan:                 summary.ActivePlan,
		Lifecycle:                  cloneClientSessionLifecycle(summary.Lifecycle),
		SessionAPI:                 strings.TrimSpace(summary.SessionAPI),
		LastEventSeq:               summary.LastEventSeq,
		ProjectionHighWatermarkSeq: summary.ProjectionHighWatermarkSeq,
		Preference: client.ModelPreference{
			Provider:    strings.TrimSpace(modelProvider),
			Model:       strings.TrimSpace(modelName),
			Thinking:    strings.TrimSpace(thinkingLevel),
			ServiceTier: strings.TrimSpace(serviceTier),
			ContextMode: strings.TrimSpace(contextMode),
		},
		WorktreeEnabled:    summary.WorktreeEnabled,
		WorktreeRootPath:   strings.TrimSpace(summary.WorktreeRootPath),
		WorktreeBaseBranch: strings.TrimSpace(summary.WorktreeBaseBranch),
		WorktreeBranch:     strings.TrimSpace(summary.WorktreeBranch),
		UpdatedAgo:         strings.TrimSpace(summary.UpdatedAgo),
	}
	if hydratedSession != nil {
		if a.tuiSessionStore == nil {
			a.tuiSessionStore = newTUISessionStore()
		}
		if strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey) == "" {
			state, err := tuiRealtimeWorksetStateFromOptions(tuiSessionWorksetLoadOptions{WorkspacePaths: []string{workspaceScope}, CWDPath: cwdScope})
			if err != nil {
				return err
			}
			a.tuiRealtimeWorkset = state
		}
		a.tuiSessionStore.MergeHydrated(*hydratedSession)
		a.applyTUISessionStoreToHome()
		if a.tuiSessionStore.EndpointCursor() != "" {
			if err := a.reconcileTUIRealtime(); err != nil {
				return fmt.Errorf("reconcile v3 realtime session subscription: %w", err)
			}
		}
	} else {
		a.upsertHomeSessionSummary(openedSummary)
	}
	if merged, ok := a.sessionSummaryByID(sessionID); ok {
		summary = merged
	} else {
		summary = openedSummary
	}
	return a.openChatView(
		sessionID,
		emptyFallback(strings.TrimSpace(summary.Title), title),
		summary.WorkspacePath,
		summary.WorkspaceName,
		summary.Mode,
		summary.WorktreeBranch,
		summary.WorktreeEnabled,
		summary.WorktreeRootPath,
		initialPrompt,
		modelProvider,
		modelName,
		thinkingLevel,
		serviceTier,
		contextMode,
		contextWindow,
		initialUsageSummary,
		summary.SessionAPI,
		summary.Metadata,
	)
}

func summaryBackgroundMetadata(summary model.SessionSummary) (bool, string, string) {
	metadata := summary.Metadata
	if len(metadata) == 0 {
		return false, "", ""
	}
	background := metadataBool(metadata, "background") || strings.EqualFold(consumeStringMetadata(metadata, "launch_mode"), "background")
	return background, consumeStringMetadata(metadata, "target_kind"), consumeStringMetadata(metadata, "target_name")
}

func lineageAgentName(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return ""
	}
	candidate := strings.TrimPrefix(trimmed, "@")
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || strings.Contains(candidate, " ") {
		return ""
	}
	return candidate
}

func agentProfileRuntime(profile client.AgentProfile) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(profile.RuntimeMode)) {
	case "plan_auto":
		return "", true
	case "read":
		return "read", false
	case "readwrite":
		return "readwrite", false
	}

	executionSetting := strings.ToLower(strings.TrimSpace(profile.ExecutionSetting))
	if executionSetting == "read" || executionSetting == "readwrite" {
		if profile.ExitPlanModeEnabled == nil || !*profile.ExitPlanModeEnabled {
			return executionSetting, false
		}
	}
	exitPlanMode := true
	if profile.ExitPlanModeEnabled != nil {
		exitPlanMode = *profile.ExitPlanModeEnabled
	}
	return executionSetting, exitPlanMode
}

func activeAgentProfile(state client.AgentState) (client.AgentProfile, bool) {
	active := strings.TrimSpace(state.ActivePrimary)
	if active == "" {
		active = "swarm"
	}
	for _, profile := range state.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), active) {
			return profile, true
		}
	}
	return client.AgentProfile{}, false
}

func applyActiveAgentModels(next model.HomeModel, state client.AgentState) model.HomeModel {
	if strings.EqualFold(strings.TrimSpace(next.ActiveModelProfile.Source), "saved") {
		return next
	}
	next.PlanModelProvider, next.PlanModelName, next.PlanThinkingLevel, next.PlanServiceTier, next.PlanContextMode = "", "", "", "", ""
	next.AutoModelProvider, next.AutoModelName, next.AutoThinkingLevel, next.AutoServiceTier, next.AutoContextMode = "", "", "", "", ""
	profile, ok := activeAgentProfile(state)
	if !ok || !strings.EqualFold(strings.TrimSpace(profile.RuntimeMode), "plan_auto") {
		return next
	}
	if !strings.EqualFold(strings.TrimSpace(profile.ModelMode), "split") {
		provider := strings.TrimSpace(profile.Provider)
		modelName := strings.TrimSpace(profile.Model)
		if provider == "" || modelName == "" {
			return next
		}
		next.PlanModelProvider, next.AutoModelProvider = provider, provider
		next.PlanModelName, next.AutoModelName = modelName, modelName
		next.PlanThinkingLevel, next.AutoThinkingLevel = strings.TrimSpace(profile.Thinking), strings.TrimSpace(profile.Thinking)
		next.PlanServiceTier, next.AutoServiceTier = next.ServiceTier, next.ServiceTier
		next.PlanContextMode, next.AutoContextMode = next.ContextMode, next.ContextMode
		return next
	}
	baseProvider := strings.TrimSpace(profile.Provider)
	next.PlanModelProvider = emptyFallback(strings.TrimSpace(profile.PlanProvider), baseProvider)
	next.PlanModelName = strings.TrimSpace(profile.PlanModel)
	next.PlanThinkingLevel = strings.TrimSpace(profile.PlanThinking)
	next.PlanServiceTier = strings.TrimSpace(profile.PlanServiceTier)
	next.AutoModelProvider = emptyFallback(strings.TrimSpace(profile.AutoProvider), baseProvider)
	next.AutoModelName = strings.TrimSpace(profile.AutoModel)
	next.AutoThinkingLevel = strings.TrimSpace(profile.AutoThinking)
	next.AutoServiceTier = strings.TrimSpace(profile.AutoServiceTier)
	return next
}

func agentRuntimeForName(state client.AgentState, agent string) (string, bool, bool) {
	agent = strings.TrimSpace(agent)
	for _, profile := range state.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.Name), agent) {
			continue
		}
		executionSetting, exitPlanMode := agentProfileRuntime(profile)
		return executionSetting, exitPlanMode, true
	}
	return "", strings.EqualFold(agent, "swarm"), false
}

func genuineLineageAgent(summary model.SessionSummary) string {
	metadata := summary.Metadata
	isChild := strings.TrimSpace(consumeStringMetadata(metadata, "parent_session_id")) != ""
	isBackground := metadataBool(metadata, "background") || strings.EqualFold(consumeStringMetadata(metadata, "launch_mode"), "background")
	if !isChild && !isBackground {
		return ""
	}
	for _, candidate := range []string{
		consumeStringMetadata(metadata, "subagent"),
		lineageAgentName(consumeStringMetadata(metadata, "lineage_label")),
		consumeStringMetadata(metadata, "background_agent"),
		consumeStringMetadata(metadata, "target_name"),
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func resolveSessionEffectiveAgent(summary model.SessionSummary, policy client.SessionV3AgentModelPolicy, state client.AgentState, fallbackAgent, fallbackExecution string, fallbackExitPlanMode, fallbackRuntimeKnown bool) (string, string, bool, bool) {
	resolved := strings.TrimSpace(policy.ResolvedAgent)
	if resolved == "" {
		resolved = strings.TrimSpace(policy.AgentName)
	}
	if resolved == "" {
		resolved = genuineLineageAgent(summary)
	}
	if resolved == "" {
		resolved = emptyFallback(strings.TrimSpace(fallbackAgent), "swarm")
	}
	if execution, exitPlanMode, known := agentRuntimeForName(state, resolved); known {
		return resolved, execution, exitPlanMode, true
	}
	if strings.EqualFold(resolved, strings.TrimSpace(fallbackAgent)) {
		return resolved, strings.TrimSpace(fallbackExecution), fallbackExitPlanMode, fallbackRuntimeKnown
	}
	return resolved, "", strings.EqualFold(resolved, "swarm"), false
}

func (a *App) currentChatAgentRuntime() (string, string, bool, bool) {
	fallbackAgent := emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm")
	fallbackExecution := strings.TrimSpace(a.homeModel.ActiveAgentExecutionSetting)
	fallbackExitPlanMode := a.homeModel.ActiveAgentExitPlanMode
	fallbackRuntimeKnown := a.homeModel.ActiveAgentRuntimeKnown
	if a == nil || a.chat == nil {
		return fallbackAgent, fallbackExecution, fallbackExitPlanMode, fallbackRuntimeKnown
	}
	if summary, ok := a.sessionSummaryByID(strings.TrimSpace(a.chat.SessionID())); ok {
		policy := client.SessionV3AgentModelPolicy{}
		if a.tuiSessionStore != nil {
			if snapshot, found := a.tuiSessionStore.ChatSnapshot(strings.TrimSpace(a.chat.SessionID())); found {
				policy = snapshot.AgentModelPolicy
			}
		}
		return resolveSessionEffectiveAgent(summary, policy, a.agentState, fallbackAgent, fallbackExecution, fallbackExitPlanMode, fallbackRuntimeKnown)
	}
	return fallbackAgent, fallbackExecution, fallbackExitPlanMode, fallbackRuntimeKnown
}

func normalizeAppSessionMode(mode string) string {
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

func (a *App) openChatView(sessionID, sessionTitle, workspacePath, workspaceName, sessionMode, worktreeBranch string, worktreeEnabled bool, worktreeRootPath, initialPrompt, modelProvider, modelName, thinkingLevel, serviceTier, contextMode string, contextWindow int, initialUsageSummary *ui.ChatUsageSummary, sessionAPI string, sessionMetadata map[string]any) error {
	dir := a.home.ActiveDirectory()
	chatWorkspace := strings.TrimSpace(workspaceName)
	chatPath := strings.TrimSpace(workspacePath)
	chatBranch := strings.TrimSpace(worktreeBranch)
	chatDirty := dir.DirtyCount

	if chatPath == "" {
		chatPath = strings.TrimSpace(dir.ResolvedPath)
	}
	for _, item := range a.homeModel.Directories {
		if !pathsEqual(item.ResolvedPath, chatPath) {
			continue
		}
		chatDirty = item.DirtyCount
		if !worktreeEnabled && chatBranch == "" {
			chatBranch = item.Branch
		}
		if chatWorkspace == "" && item.IsWorkspace {
			chatWorkspace = item.Name
		}
		break
	}
	chatDisplayPath := a.userFacingSessionPath(chatPath, worktreeEnabled, worktreeRootPath)
	if chatPath != "" {
		a.syncKnownWorkspaceSelectionForPath(chatPath)
	}
	chatWorkspace = a.contextDisplayNameForPath(chatPath, chatWorkspace)
	if chatBranch == "" {
		chatBranch = "-"
	}
	if chatWorkspace == "" {
		chatWorkspace = "directory"
	}

	chatBackend := newAPIChatBackend(a.api, sessionAPI, targetSwarmIDForV3Session(sessionMetadata, a.homeModel.CurrentSwarmTarget))
	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		Backend: chatBackend,
		Send: func(ctx context.Context, sessionID string, req ui.ChatSendRequest) error {
			_, err := a.sendTUIV3ChatMessage(ctx, sessionID, req)
			return err
		},
		SessionID:           strings.TrimSpace(sessionID),
		SessionTitle:        strings.TrimSpace(sessionTitle),
		InitialPrompt:       strings.TrimSpace(initialPrompt),
		Presets:             a.home.ModelPresets(),
		SessionTabs:         chatSessionTabsFromSummaries(a.homeModel.RecentSessions),
		CommandSuggestions:  buildChatCommandSuggestions(a.startupDevMode()),
		ShowHeader:          a.config.Chat.ShowHeader,
		AuthConfigured:      a.homeModel.AuthConfigured,
		ShowThinkingTags:    boolPtr(a.config.Chat.ThinkingTags),
		ModelProvider:       modelProvider,
		ModelName:           modelName,
		AvailableModels:     a.chatAvailableModels(modelProvider),
		ThinkingLevel:       thinkingLevel,
		ServiceTier:         serviceTier,
		ContextMode:         contextMode,
		ContextWindow:       contextWindow,
		InitialUsageSummary: initialUsageSummary,
		SessionMode:         sessionMode,
		ToolStreamStyle: ui.ChatToolStreamStyle{
			ShowAnchor:    boolPtr(a.config.Chat.ToolStream.ShowAnchor),
			PulseFrames:   append([]string(nil), a.config.Chat.ToolStream.PulseFrames...),
			RunningSymbol: a.config.Chat.ToolStream.RunningSymbol,
			SuccessSymbol: a.config.Chat.ToolStream.SuccessSymbol,
			ErrorSymbol:   a.config.Chat.ToolStream.ErrorSymbol,
		},
		SwarmingTitle:  a.config.Swarming.Title,
		SwarmingStatus: a.config.Swarming.Status,
		SwarmName:      a.config.Swarm.Name,
		Meta: ui.ChatSessionMeta{
			Workspace:             chatWorkspace,
			Path:                  chatDisplayPath,
			Route:                 a.sessionRouteLabelForWorkspace(chatPath, sessionMetadata),
			Branch:                chatBranch,
			Dirty:                 chatDirty,
			Version:               strings.TrimSpace(a.homeModel.Version),
			UpdateVersionHint:     homeUpdateVersionHint(a.homeModel.UpdateStatus),
			Agent:                 emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm"),
			AgentExecutionSetting: strings.TrimSpace(a.homeModel.ActiveAgentExecutionSetting),
			AgentExitPlanMode:     a.homeModel.ActiveAgentExitPlanMode,
			AgentRuntimeKnown:     a.homeModel.ActiveAgentRuntimeKnown,
			Subagents:             append([]string(nil), a.homeModel.Subagents...),
			Plan:                  a.home.ActivePlanName(),
			WorktreeEnabled:       worktreeEnabled,
			BypassPermissions:     a.homeModel.BypassPermissions,
			AgentTodoTaskCount:    0,
			AgentTodoOpenCount:    0,
			AgentTodoInProgress:   0,
		},
		KeyBindings: a.keybinds,
		CopyText:    copyTextToClipboard,
		OnAsyncEvent: func() {
			if a == nil || a.screen == nil {
				return
			}
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptChatAsync))
		},
		OnSessionModeChanged: func(mode, provider, modelName, thinking, serviceTier, contextMode string, contextWindow int) {
			if a == nil || a.tuiSessionStore == nil {
				return
			}
			a.tuiSessionStore.ApplyModePreference(strings.TrimSpace(sessionID), mode, client.ModelPreference{
				Provider:    provider,
				Model:       modelName,
				Thinking:    thinking,
				ServiceTier: serviceTier,
				ContextMode: contextMode,
			}, contextWindow)
		},
		RequestAsyncRender: func() {
			if a == nil || a.screen == nil {
				return
			}
			select {
			case a.pendingChatRender <- struct{}{}:
				a.screen.PostEventWait(tcell.NewEventInterrupt(interruptChatAsync))
			default:
			}
		},
	})
	if summary, ok := a.sessionSummaryByID(strings.TrimSpace(sessionID)); ok {
		policy := client.SessionV3AgentModelPolicy{}
		if a.tuiSessionStore != nil {
			if snapshot, found := a.tuiSessionStore.ChatSnapshot(strings.TrimSpace(sessionID)); found {
				policy = snapshot.AgentModelPolicy
			}
		}
		resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown := resolveSessionEffectiveAgent(summary, policy, a.agentState,
			emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm"),
			strings.TrimSpace(a.homeModel.ActiveAgentExecutionSetting),
			a.homeModel.ActiveAgentExitPlanMode,
			a.homeModel.ActiveAgentRuntimeKnown,
		)
		a.chat.SetAgentRuntime(resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown)
		taskCount, openCount, inProgressCount := agentTodoCountsFromMetadata(summary.Metadata)
		a.chat.SetAgentTodoSummary(taskCount, openCount, inProgressCount)
	}
	if summary, ok := a.sessionSummaryByID(strings.TrimSpace(sessionID)); ok && summary.Lifecycle != nil {
		a.chat.ApplySessionLifecycle(ui.ChatSessionLifecycle{
			SessionID:      summary.Lifecycle.SessionID,
			RunID:          summary.Lifecycle.RunID,
			Active:         summary.Lifecycle.Active,
			Phase:          summary.Lifecycle.Phase,
			StartedAt:      summary.Lifecycle.StartedAt,
			EndedAt:        summary.Lifecycle.EndedAt,
			UpdatedAt:      summary.Lifecycle.UpdatedAt,
			Generation:     summary.Lifecycle.Generation,
			StopReason:     summary.Lifecycle.StopReason,
			Error:          summary.Lifecycle.Error,
			OwnerTransport: summary.Lifecycle.OwnerTransport,
		})
	}
	a.chat.SetPasteActive(a.pasteActive)
	a.setSwarmNotificationCount(a.swarmNotificationCount)
	a.applyThemeToChat()
	a.home.ClearPrompt()
	a.startSessionEventStream()
	a.route = "chat"
	a.syncVoiceInputState()
	return nil
}

func chatTitleFromPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "New Session"
	}
	runes := []rune(trimmed)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return trimmed
}

func sessionSummaryActive(summary model.SessionSummary) bool {
	if summary.PendingPermissionCount > 0 {
		return true
	}
	if summary.ActiveRunIntent != nil {
		status := strings.ToLower(strings.TrimSpace(summary.ActiveRunIntent.Status))
		if strings.TrimSpace(summary.ActiveRunIntent.RunID) != "" && (status == "pending_executor" || status == "running") {
			return true
		}
	}
	return summary.Lifecycle != nil && summary.Lifecycle.Active
}

func sessionSummaryActiveStartedAt(summary model.SessionSummary) int64 {
	if summary.ActiveRunIntent != nil {
		if summary.ActiveRunIntent.StartedAt > 0 {
			return summary.ActiveRunIntent.StartedAt
		}
		if summary.ActiveRunIntent.CreatedAt > 0 {
			return summary.ActiveRunIntent.CreatedAt
		}
	}
	if summary.Lifecycle != nil && summary.Lifecycle.StartedAt > 0 {
		return summary.Lifecycle.StartedAt
	}
	if summary.CreatedAt > 0 {
		return summary.CreatedAt
	}
	return summary.UpdatedAt
}

func sessionSummaryActivityLabel(summary model.SessionSummary) string {
	if summary.PendingPermissionCount > 0 {
		return "NEEDS APPROVAL"
	}
	if group := sessionSummarySidebarGroup(summary); group == "needs_review" {
		return "REVIEW"
	} else if group == "in_progress" {
		if status := sessionSummaryPlanStatus(summary); status != "" {
			return status
		}
	}
	if sessionSummaryActive(summary) {
		if summary.Lifecycle != nil {
			if phase := strings.TrimSpace(summary.Lifecycle.Phase); phase != "" {
				return strings.ToUpper(strings.ReplaceAll(phase, "_", " "))
			}
		}
		return "ACTIVE"
	}
	return ""
}

func sessionSummarySidebarGroup(summary model.SessionSummary) string {
	plan := summary.ActivePlan
	if plan == nil || plan.Document == nil {
		return "active_chats"
	}
	document := plan.Document
	status := strings.ToLower(strings.TrimSpace(document.Status))
	if document.ExecutionState != nil && strings.TrimSpace(document.ExecutionState.Status) != "" {
		status = strings.ToLower(strings.TrimSpace(document.ExecutionState.Status))
	}
	checkpoint := sessionSummaryActiveCheckpoint(document)
	checkpointStatus := ""
	if checkpoint != nil {
		checkpointStatus = strings.ToLower(strings.TrimSpace(checkpoint.Status))
		if checkpointStatus == "needs_review" || (checkpoint.Review != nil && strings.EqualFold(strings.TrimSpace(checkpoint.Review.Status), "pending")) {
			return "needs_review"
		}
	}
	if status == "waiting_review" {
		return "needs_review"
	}
	if sessionSummaryPlanComplete(document, status) {
		return "active_chats"
	}
	return "in_progress"
}

func sessionSummaryPlanStatus(summary model.SessionSummary) string {
	plan := summary.ActivePlan
	if plan == nil || plan.Document == nil {
		return ""
	}
	document := plan.Document
	status := strings.ToLower(strings.TrimSpace(document.Status))
	if document.ExecutionState != nil && strings.TrimSpace(document.ExecutionState.Status) != "" {
		status = strings.ToLower(strings.TrimSpace(document.ExecutionState.Status))
	}
	checkpoint := sessionSummaryActiveCheckpoint(document)
	checkpointStatus := ""
	if checkpoint != nil {
		checkpointStatus = strings.ToLower(strings.TrimSpace(checkpoint.Status))
	}
	switch {
	case status == "waiting_review" || checkpointStatus == "needs_review":
		return "REVIEW"
	case status == "blocked" || status == "failed" || checkpointStatus == "blocked" || checkpointStatus == "failed":
		return "BLOCKED"
	case status == "queued" || status == "pending" || checkpointStatus == "pending":
		return "QUEUED"
	default:
		return "RUNNING"
	}
}

func sessionSummaryPlanProgress(summary model.SessionSummary) string {
	if summary.ActivePlan == nil || summary.ActivePlan.Document == nil {
		return ""
	}
	document := summary.ActivePlan.Document
	checkpoints := append([]client.SessionPlanCheckpoint(nil), document.Checkpoints...)
	sort.SliceStable(checkpoints, func(i, j int) bool {
		leftOrder, rightOrder := checkpoints[i].Order, checkpoints[j].Order
		if leftOrder <= 0 {
			leftOrder = int(^uint(0) >> 1)
		}
		if rightOrder <= 0 {
			rightOrder = int(^uint(0) >> 1)
		}
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return checkpoints[i].ID < checkpoints[j].ID
	})
	if len(checkpoints) == 0 {
		return ""
	}
	activeID := strings.TrimSpace(document.ActiveCheckpointID)
	if activeID == "" && document.ExecutionState != nil {
		activeID = strings.TrimSpace(document.ExecutionState.LastCheckpointID)
	}
	activeIndex, completed := 0, 0
	for index, checkpoint := range checkpoints {
		if strings.EqualFold(strings.TrimSpace(checkpoint.Status), "completed") {
			completed++
		}
		if checkpoint.ID == activeID || (activeID == "" && strings.EqualFold(strings.TrimSpace(checkpoint.Status), "in_progress")) {
			activeIndex = index + 1
			if activeID == "" {
				activeID = checkpoint.ID
			}
		}
	}
	if activeIndex == 0 {
		activeIndex = completed
	}
	return fmt.Sprintf("%d/%d", activeIndex, len(checkpoints))
}

func sessionSummaryActiveCheckpoint(document *client.SessionPlanDocument) *client.SessionPlanCheckpoint {
	if document == nil {
		return nil
	}
	activeID := strings.TrimSpace(document.ActiveCheckpointID)
	if activeID == "" && document.ExecutionState != nil {
		activeID = strings.TrimSpace(document.ExecutionState.LastCheckpointID)
	}
	for index := range document.Checkpoints {
		checkpoint := &document.Checkpoints[index]
		if checkpoint.ID == activeID || (activeID == "" && strings.EqualFold(strings.TrimSpace(checkpoint.Status), "in_progress")) {
			return checkpoint
		}
	}
	return nil
}

func sessionSummaryPlanComplete(document *client.SessionPlanDocument, normalizedStatus string) bool {
	if normalizedStatus == "completed" {
		return true
	}
	if document == nil || len(document.Checkpoints) == 0 {
		return false
	}
	for _, checkpoint := range document.Checkpoints {
		if !strings.EqualFold(strings.TrimSpace(checkpoint.Status), "completed") {
			return false
		}
	}
	return true
}

func chatSessionTabsFromSummaries(summaries []model.SessionSummary) []ui.ChatSessionTab {
	tabs := make([]ui.ChatSessionTab, 0, len(summaries))
	seen := make(map[string]struct{}, len(summaries))

	for _, summary := range summaries {
		id := strings.TrimSpace(summary.ID)
		title := strings.TrimSpace(summary.Title)
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
		lineage := ui.SessionLineageFromSummary(summary)
		tabs = append(tabs, ui.ChatSessionTab{
			ID:              id,
			Title:           title,
			WorkspaceName:   strings.TrimSpace(summary.WorkspaceName),
			WorkspacePath:   strings.TrimSpace(summary.WorkspacePath),
			WorktreeEnabled: summary.WorktreeEnabled,
			WorktreeBranch:  strings.TrimSpace(summary.WorktreeBranch),
			Mode:            strings.TrimSpace(summary.Mode),
			CreatedAt:       summary.CreatedAt,
			UpdatedAt:       summary.UpdatedAt,
			ActiveStartedAt: sessionSummaryActiveStartedAt(summary),
			UpdatedAgo:      strings.TrimSpace(summary.UpdatedAgo),
			Active:          sessionSummaryActive(summary),
			NeedsAttention:  summary.PendingPermissionCount > 0,
			ActivityLabel:   sessionSummaryActivityLabel(summary),
			Group:           sessionSummarySidebarGroup(summary),
			ProgressLabel:   sessionSummaryPlanProgress(summary),
			Provider:        strings.TrimSpace(summary.Preference.Provider),
			ModelName:       strings.TrimSpace(summary.Preference.Model),
			ServiceTier:     strings.TrimSpace(summary.Preference.ServiceTier),
			ContextMode:     strings.TrimSpace(summary.Preference.ContextMode),
			Background:      lineage.Background,
			ParentSessionID: strings.TrimSpace(lineage.ParentSessionID),
			LineageKind:     strings.TrimSpace(lineage.LineageKind),
			LineageLabel:    strings.TrimSpace(lineage.LineageLabel),
			AssignmentLabel: strings.TrimSpace(lineage.AssignmentLabel),
			TargetKind:      strings.TrimSpace(lineage.TargetKind),
			TargetName:      strings.TrimSpace(lineage.TargetName),
			Depth:           summary.Depth,
		})
	}
	for _, background := range summariesBackgroundTabs(summaries) {
		id := strings.TrimSpace(background.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		tabs = append(tabs, background)
	}
	return tabs
}

func summariesBackgroundTabs(summaries []model.SessionSummary) []ui.ChatSessionTab {
	backgroundTabs := make([]ui.ChatSessionTab, 0)
	for _, summary := range summaries {
		background, targetKind, targetName := summaryBackgroundMetadata(summary)
		if !background {
			continue
		}
		id := strings.TrimSpace(summary.ID)
		if id == "" {
			continue
		}
		title := strings.TrimSpace(summary.Title)
		if title == "" {
			title = id
		}
		mode := strings.TrimSpace(summary.Mode)
		if mode == "" {
			mode = emptyFallback(targetName, targetKind)
		}
		lineage := ui.SessionLineageFromSummary(summary)
		backgroundTabs = append(backgroundTabs, ui.ChatSessionTab{
			ID:              id,
			Title:           title,
			WorkspaceName:   strings.TrimSpace(summary.WorkspaceName),
			WorkspacePath:   strings.TrimSpace(summary.WorkspacePath),
			WorktreeEnabled: summary.WorktreeEnabled,
			WorktreeBranch:  strings.TrimSpace(summary.WorktreeBranch),
			Mode:            mode,
			CreatedAt:       summary.CreatedAt,
			UpdatedAt:       summary.UpdatedAt,
			ActiveStartedAt: sessionSummaryActiveStartedAt(summary),
			UpdatedAgo:      strings.TrimSpace(summary.UpdatedAgo),
			Active:          sessionSummaryActive(summary),
			NeedsAttention:  summary.PendingPermissionCount > 0,
			ActivityLabel:   sessionSummaryActivityLabel(summary),
			Group:           sessionSummarySidebarGroup(summary),
			ProgressLabel:   sessionSummaryPlanProgress(summary),
			Provider:        strings.TrimSpace(summary.Preference.Provider),
			ModelName:       strings.TrimSpace(summary.Preference.Model),
			ServiceTier:     strings.TrimSpace(summary.Preference.ServiceTier),
			ContextMode:     strings.TrimSpace(summary.Preference.ContextMode),
			Background:      lineage.Background,
			ParentSessionID: strings.TrimSpace(lineage.ParentSessionID),
			LineageKind:     strings.TrimSpace(lineage.LineageKind),
			LineageLabel:    strings.TrimSpace(lineage.LineageLabel),
			AssignmentLabel: strings.TrimSpace(lineage.AssignmentLabel),
			TargetKind:      strings.TrimSpace(lineage.TargetKind),
			TargetName:      strings.TrimSpace(lineage.TargetName),
			Depth:           summary.Depth,
		})
	}
	return backgroundTabs
}

func cloneMetadataMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneMetadataValue(value)
	}
	return out
}

func metadataIntValue(payload map[string]any, key string) int {
	if len(payload) == 0 {
		return 0
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	default:
		return 0
	}
}

func agentTodoCountsFromMetadata(metadata map[string]any) (int, int, int) {
	summary := cloneMetadataMap(metadataObject(metadata, "agent_todo_summary"))
	if len(summary) == 0 {
		return 0, 0, 0
	}
	agent := metadataObject(summary, "agent")
	if len(agent) == 0 {
		agent = summary
	}
	return metadataIntValue(agent, "task_count"), metadataIntValue(agent, "open_count"), metadataIntValue(agent, "in_progress_count")
}

func metadataObject(metadata map[string]any, key string) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return typed
}

func mergeMetadataMaps(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := cloneMetadataMap(base)
	if out == nil {
		out = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		out[key] = cloneMetadataValue(value)
	}
	return out
}

func cloneMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadataMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneMetadataValue(item))
		}
		return out
	default:
		return typed
	}
}

func consumeStringMetadata(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func metadataMap(metadata map[string]any, key string) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	mapped, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return mapped
}

func (a *App) backgroundSessionSummaries() []model.BackgroundSessionSummary {
	if a == nil {
		return nil
	}
	return backgroundSessionSummariesForSessions(a.homeModel.RecentSessions, a.homeModel.BackgroundSessions)
}

func backgroundSessionSummariesForSessions(summaries []model.SessionSummary, existing []model.BackgroundSessionSummary) []model.BackgroundSessionSummary {
	records := make([]model.BackgroundSessionSummary, 0)
	for _, summary := range summaries {
		metadata := summary.Metadata
		if len(metadata) == 0 {
			continue
		}
		if !metadataBool(metadata, "background") && !strings.EqualFold(consumeStringMetadata(metadata, "launch_mode"), "background") {
			continue
		}
		ctx := metadataMap(metadata, "execution_context")
		record := model.BackgroundSessionSummary{
			ChildSessionID:     strings.TrimSpace(summary.ID),
			ParentSessionID:    consumeStringMetadata(metadata, "parent_session_id"),
			ParentTitle:        consumeStringMetadata(metadata, "parent_title"),
			ChildTitle:         strings.TrimSpace(summary.Title),
			TargetKind:         consumeStringMetadata(metadata, "target_kind"),
			TargetName:         consumeStringMetadata(metadata, "target_name"),
			PendingPermissions: summary.PendingPermissionCount,
			WorkspacePath:      strings.TrimSpace(summary.WorkspacePath),
			WorkspaceName:      strings.TrimSpace(summary.WorkspaceName),
			LaunchMode:         consumeStringMetadata(metadata, "launch_mode"),
			Instructions:       consumeStringMetadata(metadata, "commit_instructions"),
			Background:         metadataBool(metadata, "background"),
		}
		if record.LaunchMode == "" {
			record.LaunchMode = "background"
		}
		if record.TargetName == "" {
			record.TargetName = consumeStringMetadata(metadata, "agent_name")
		}
		if ctx != nil {
			record.CWD = consumeStringMetadata(ctx, "cwd")
			record.WorktreeMode = consumeStringMetadata(ctx, "worktree_mode")
			record.WorktreeRootPath = consumeStringMetadata(ctx, "worktree_root_path")
			record.WorktreeBranch = consumeStringMetadata(ctx, "worktree_branch")
			record.WorktreeBaseBranch = consumeStringMetadata(ctx, "worktree_base_branch")
			if path := consumeStringMetadata(ctx, "workspace_path"); path != "" {
				record.WorkspacePath = path
			}
		}
		if record.CWD == "" {
			record.CWD = record.WorkspacePath
		}
		if summary.Lifecycle != nil {
			record.StartedAtUnixMS = summary.Lifecycle.StartedAt
			record.LastUpdatedAtUnixMS = summary.Lifecycle.UpdatedAt
			record.Status = strings.TrimSpace(summary.Lifecycle.Phase)
			if summary.Lifecycle.Active {
				if record.PendingPermissions > 0 {
					record.Status = "blocked"
				} else if record.Status == "" {
					record.Status = "running"
				}
			} else if record.Status == "" {
				record.Status = emptyFallback(strings.TrimSpace(summary.Lifecycle.StopReason), "idle")
			}
		}
		if record.Status == "" {
			if record.PendingPermissions > 0 {
				record.Status = "blocked"
			} else {
				record.Status = "idle"
			}
		}
		records = append(records, record)
	}
	for _, record := range existing {
		if strings.TrimSpace(record.ChildSessionID) == "" {
			continue
		}
		found := false
		for _, existingRecord := range records {
			if strings.TrimSpace(existingRecord.ChildSessionID) == strings.TrimSpace(record.ChildSessionID) {
				found = true
				break
			}
		}
		if !found {
			records = append(records, record)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		left := records[i].LastUpdatedAtUnixMS
		right := records[j].LastUpdatedAtUnixMS
		if left == right {
			return strings.TrimSpace(records[i].ChildTitle) < strings.TrimSpace(records[j].ChildTitle)
		}
		return left > right
	})
	return records
}

func computeSessionDepths(summaries []model.SessionSummary) map[string]int {
	depths := make(map[string]int, len(summaries))
	if len(summaries) == 0 {
		return depths
	}
	byID := make(map[string]model.SessionSummary, len(summaries))
	for _, summary := range summaries {
		id := strings.TrimSpace(summary.ID)
		if id == "" {
			continue
		}
		byID[id] = summary
	}
	visiting := make(map[string]bool, len(byID))
	var walk func(string) int
	walk = func(id string) int {
		id = strings.TrimSpace(id)
		if id == "" {
			return 0
		}
		if depth, ok := depths[id]; ok {
			return depth
		}
		if visiting[id] {
			return 0
		}
		summary, ok := byID[id]
		if !ok {
			return 0
		}
		visiting[id] = true
		lineage := ui.SessionLineageFromSummary(summary)
		parentID := strings.TrimSpace(lineage.ParentSessionID)
		depth := 0
		if parentID != "" && !strings.EqualFold(strings.TrimSpace(lineage.LineageKind), "session_deploy") {
			depth = walk(parentID) + 1
		}
		visiting[id] = false
		depths[id] = depth
		return depth
	}
	for id := range byID {
		walk(id)
	}
	return depths
}

func applySessionDepths(summaries []model.SessionSummary) []model.SessionSummary {
	if len(summaries) == 0 {
		return nil
	}
	depths := computeSessionDepths(summaries)
	out := make([]model.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		copy := summary
		id := strings.TrimSpace(copy.ID)
		copy.Depth = depths[id]
		metadata := cloneMetadataMap(copy.Metadata)
		if metadata == nil {
			metadata = make(map[string]any, 1)
		}
		metadata["ui_depth"] = copy.Depth
		copy.Metadata = metadata
		out = append(out, copy)
	}
	return out
}

func (a *App) refreshBackgroundSessions() {
	if a == nil {
		return
	}
	next := a.homeModel
	next.BackgroundSessions = a.backgroundSessionSummaries()
	a.applyHomeModel(next)
}

func (a *App) setBackgroundSessionSummary(summary model.BackgroundSessionSummary) {
	if a == nil || strings.TrimSpace(summary.ChildSessionID) == "" {
		return
	}
	next := a.homeModel
	updated := false
	for i := range next.BackgroundSessions {
		if strings.TrimSpace(next.BackgroundSessions[i].ChildSessionID) != strings.TrimSpace(summary.ChildSessionID) {
			continue
		}
		next.BackgroundSessions[i] = summary
		updated = true
		break
	}
	if !updated {
		next.BackgroundSessions = append([]model.BackgroundSessionSummary{summary}, next.BackgroundSessions...)
	}
	a.applyHomeModel(next)
}

func (a *App) upsertHomeSessionSummary(summary model.SessionSummary) {
	if a == nil {
		return
	}
	summary.ID = strings.TrimSpace(summary.ID)
	if summary.ID == "" {
		return
	}
	next := a.homeModel
	for i := range next.RecentSessions {
		if strings.TrimSpace(next.RecentSessions[i].ID) != summary.ID {
			continue
		}
		next.RecentSessions[i] = mergeHomeSessionSummary(next.RecentSessions[i], summary)
		a.applyHomeModel(next)
		if a.chat != nil {
			a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
		}
		return
	}
	next.RecentSessions = append([]model.SessionSummary{summary}, next.RecentSessions...)
	a.applyHomeModel(next)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
}

func (a *App) removeHomeSessionSummary(sessionID string) bool {
	if a == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	next := a.homeModel
	changed := false
	if len(next.RecentSessions) > 0 {
		filtered := next.RecentSessions[:0]
		for _, summary := range next.RecentSessions {
			if strings.TrimSpace(summary.ID) == sessionID {
				changed = true
				continue
			}
			filtered = append(filtered, summary)
		}
		next.RecentSessions = filtered
	}
	if len(next.BackgroundSessions) > 0 {
		filtered := next.BackgroundSessions[:0]
		for _, summary := range next.BackgroundSessions {
			if strings.TrimSpace(summary.ChildSessionID) == sessionID {
				changed = true
				continue
			}
			filtered = append(filtered, summary)
		}
		next.BackgroundSessions = filtered
	}
	if !changed {
		return false
	}
	a.applyHomeModel(next)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(next.RecentSessions))
	}
	return true
}

func (a *App) commitRunInstructions(userInstructions string) string {
	instructions := []string{
		"You are the memory agent handling /commit from the TUI.",
		"Inspect git status and diffs in the scoped current working directory before making changes.",
		"Understand the changed work, stage the appropriate files, and create one commit with a concise, accurate message.",
		"Use git add and git commit only when needed and only inside the granted workspace scope.",
		"Only run git push if the user explicitly requested push.",
		"If permissions are required, rely on the existing backend permission system and wait for approval.",
	}
	if text := strings.TrimSpace(userInstructions); text != "" {
		instructions = append(instructions, "Additional user instructions: "+text)
	}
	return strings.Join(instructions, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func commitUsesCurrentWorktreePath(summary model.SessionSummary, ctx *client.RunExecutionContext) bool {
	worktreeRootPath := strings.TrimSpace(summary.WorktreeRootPath)
	if worktreeRootPath == "" && ctx != nil {
		worktreeRootPath = strings.TrimSpace(ctx.WorktreeRootPath)
	}
	if worktreeRootPath == "" {
		return false
	}
	if summary.WorktreeEnabled {
		return true
	}
	if strings.TrimSpace(summary.WorktreeBranch) != "" || strings.TrimSpace(summary.WorktreeBaseBranch) != "" {
		return true
	}
	if ctx != nil {
		if strings.TrimSpace(ctx.WorktreeBranch) != "" || strings.TrimSpace(ctx.WorktreeBaseBranch) != "" {
			return true
		}
		workspacePath := firstNonEmpty(strings.TrimSpace(ctx.WorkspacePath), strings.TrimSpace(ctx.CWD), strings.TrimSpace(summary.WorkspacePath))
		if workspacePath != "" && !pathsEqual(workspacePath, worktreeRootPath) {
			return true
		}
	}
	return false
}

func (a *App) commitExecutionContext(summary model.SessionSummary) *client.RunExecutionContext {
	ctx := &client.RunExecutionContext{
		WorkspacePath: strings.TrimSpace(summary.WorkspacePath),
		CWD:           strings.TrimSpace(summary.WorkspacePath),
		WorktreeMode:  "inherit",
	}
	if metadata := summary.Metadata; len(metadata) > 0 {
		if execCtx := metadataMap(metadata, "execution_context"); execCtx != nil {
			if path := consumeStringMetadata(execCtx, "workspace_path"); path != "" {
				ctx.WorkspacePath = path
			}
			if cwd := consumeStringMetadata(execCtx, "cwd"); cwd != "" {
				ctx.CWD = cwd
			}
			if mode := consumeStringMetadata(execCtx, "worktree_mode"); mode != "" {
				ctx.WorktreeMode = mode
			}
			ctx.WorktreeRootPath = consumeStringMetadata(execCtx, "worktree_root_path")
			ctx.WorktreeBranch = consumeStringMetadata(execCtx, "worktree_branch")
			ctx.WorktreeBaseBranch = consumeStringMetadata(execCtx, "worktree_base_branch")
		}
	}
	if ctx.WorkspacePath == "" {
		ctx.WorkspacePath = strings.TrimSpace(summary.WorkspacePath)
	}
	if ctx.CWD == "" {
		ctx.CWD = ctx.WorkspacePath
	}
	if ctx.WorktreeRootPath == "" {
		ctx.WorktreeRootPath = strings.TrimSpace(summary.WorktreeRootPath)
	}
	if ctx.WorktreeBranch == "" {
		ctx.WorktreeBranch = strings.TrimSpace(summary.WorktreeBranch)
	}
	if ctx.WorktreeBaseBranch == "" {
		ctx.WorktreeBaseBranch = strings.TrimSpace(summary.WorktreeBaseBranch)
	}
	if ctx.WorkspacePath == "" {
		ctx.WorkspacePath = strings.TrimSpace(a.activeContextPath())
	}
	if ctx.CWD == "" {
		ctx.CWD = ctx.WorkspacePath
	}
	if ctx.CWD == "" {
		ctx.CWD = strings.TrimSpace(a.startupCWD)
	}
	if ctx.WorkspacePath == "" {
		ctx.WorkspacePath = ctx.CWD
	}
	if commitUsesCurrentWorktreePath(summary, ctx) {
		ctx.WorktreeMode = "off"
	} else if ctx.WorktreeMode == "" {
		ctx.WorktreeMode = "inherit"
	}
	return ctx
}

func (a *App) createBackgroundCommitSession(ctx context.Context, parentSessionID string, parentSummary model.SessionSummary, instructions string) (model.SessionSummary, error) {
	workspaceBindingID := consumeStringMetadata(parentSummary.Metadata, "swarm_v3_workspace_binding_id")
	swarmID := consumeStringMetadata(parentSummary.Metadata, "swarm_v3_runtime_swarm_id")
	if workspaceBindingID == "" {
		return model.SessionSummary{}, errors.New("workspace binding id is required")
	}
	if swarmID == "" {
		return model.SessionSummary{}, errors.New("swarm id is required")
	}
	return model.SessionSummary{}, errTUIRetiredSessionAPI("create background commit session")
}

func (a *App) startBackgroundCommitRun(ctx context.Context, childSummary model.SessionSummary, instructions string) (client.BackgroundRunAccepted, error) {
	prompt := "Review the git diff in scope, prepare the right staged set, and create the commit now."
	return a.api.StartBackgroundSessionRun(ctx, strings.TrimSpace(childSummary.ID), prompt, "", a.commitRunInstructions(instructions), client.RunSessionOptions{
		Compact:          false,
		Background:       true,
		TargetKind:       "background",
		TargetName:       commitBackgroundAgentName,
		ExecutionContext: a.commitExecutionContext(childSummary),
	})
}

func (a *App) consumeHomeOverlayActions() {
	if a.home == nil {
		return
	}
	for {
		processed := false

		if action, ok := a.home.PopHomeAction(); ok {
			a.handleHomeAction(action)
			processed = true
		}
		if action, ok := a.home.PopAuthModalAction(); ok {
			a.handleAuthModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopWorkspaceModalAction(); ok {
			a.handleWorkspaceModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopWorktreesModalAction(); ok {
			a.handleWorktreesModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopModelsModalAction(); ok {
			a.handleModelsModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopAgentsModalAction(); ok {
			a.handleAgentsModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopVoiceModalAction(); ok {
			a.handleVoiceModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopThemeModalAction(); ok {
			a.handleThemeModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopKeybindsModalAction(); ok {
			a.handleKeybindsModalAction(action)
			processed = true
		}
		if !processed {
			return
		}
	}
}

func (a *App) consumeHomeActions() {
	if a.route == "chat" && a.home != nil && a.home.AlertsModalVisible() {
		for {
			action, ok := a.home.PopHomeAction()
			if !ok {
				return
			}
			a.handleHomeAction(action)
			if !a.home.AlertsModalVisible() {
				return
			}
		}
	}
	if a.route != "home" || a.home == nil {
		return
	}
	for {
		processed := false

		if action, ok := a.home.PopHomeAction(); ok {
			a.handleHomeAction(action)
			processed = true
			if a.route != "home" {
				return
			}
		}
		if a.consumeBackgroundSessionsModalSelection() {
			processed = true
			if a.route != "home" {
				return
			}
		}
		if action, ok := a.home.PopAuthModalAction(); ok {
			a.handleAuthModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopVaultModalAction(); ok {
			a.handleVaultModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopWorkspaceModalAction(); ok {
			a.handleWorkspaceModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopWorktreesModalAction(); ok {
			a.handleWorktreesModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopModelsModalAction(); ok {
			a.handleModelsModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopAgentsModalAction(); ok {
			a.handleAgentsModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopVoiceModalAction(); ok {
			a.handleVoiceModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopThemeModalAction(); ok {
			a.handleThemeModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopKeybindsModalAction(); ok {
			a.handleKeybindsModalAction(action)
			processed = true
		}
		if !processed {
			return
		}
	}
}

func cloneClientSessionLifecycle(lifecycle *client.SessionLifecycleSnapshot) *client.SessionLifecycleSnapshot {
	if lifecycle == nil {
		return nil
	}
	copy := *lifecycle
	return &copy
}

func cloneClientSessionV3RunIntent(intent *client.SessionV3RunIntent) *client.SessionV3RunIntent {
	if intent == nil {
		return nil
	}
	copy := *intent
	return &copy
}

func v3RunIntentSessionLifecycle(sessionID string, intent *client.SessionV3RunIntent) *client.SessionLifecycleSnapshot {
	lifecycle := activeRunIntentLifecycle(sessionID, intent)
	if lifecycle == nil {
		return nil
	}
	return &client.SessionLifecycleSnapshot{
		SessionID:      lifecycle.SessionID,
		RunID:          lifecycle.RunID,
		Active:         lifecycle.Active,
		Phase:          lifecycle.Phase,
		StartedAt:      lifecycle.StartedAt,
		EndedAt:        lifecycle.EndedAt,
		UpdatedAt:      lifecycle.UpdatedAt,
		Generation:     lifecycle.Generation,
		StopReason:     lifecycle.StopReason,
		Error:          lifecycle.Error,
		OwnerTransport: lifecycle.OwnerTransport,
	}
}

func targetSwarmIDForV3Session(metadata map[string]any, target *model.SwarmTarget) string {
	if value := consumeStringMetadata(metadata, "swarm_v3_runtime_swarm_id"); value != "" {
		return value
	}
	if value := consumeStringMetadata(metadata, "swarm_v3_authority_host_swarm_id"); value != "" {
		return value
	}
	if isPrimaryHostSwarmTarget(target) {
		return strings.TrimSpace(target.SwarmID)
	}
	return ""
}

func mergeClientModelPreference(current, incoming client.ModelPreference) client.ModelPreference {
	merged := current
	merged.Provider = strings.TrimSpace(incoming.Provider)
	merged.Model = strings.TrimSpace(incoming.Model)
	merged.Thinking = strings.TrimSpace(incoming.Thinking)
	merged.ServiceTier = strings.TrimSpace(incoming.ServiceTier)
	merged.ContextMode = strings.TrimSpace(incoming.ContextMode)
	return merged
}

func mergeHomeSessionSummary(current, incoming model.SessionSummary) model.SessionSummary {
	merged := current
	if value := strings.TrimSpace(incoming.ID); value != "" {
		merged.ID = value
	}
	if value := strings.TrimSpace(incoming.WorkspacePath); value != "" {
		merged.WorkspacePath = value
	}
	if value := strings.TrimSpace(incoming.WorkspaceName); value != "" {
		merged.WorkspaceName = value
	}
	if value := strings.TrimSpace(incoming.Title); value != "" {
		merged.Title = value
	}
	if value := strings.TrimSpace(incoming.Mode); value != "" {
		merged.Mode = value
	}
	if len(incoming.Metadata) > 0 {
		merged.Metadata = cloneMetadataMap(incoming.Metadata)
	}
	merged.PendingPermissionCount = incoming.PendingPermissionCount
	merged.HasActivePlan = incoming.HasActivePlan
	if incoming.ActivePlan != nil {
		activePlan := *incoming.ActivePlan
		merged.ActivePlan = &activePlan
	} else if !incoming.HasActivePlan {
		merged.ActivePlan = nil
	}
	merged.Preference = mergeClientModelPreference(merged.Preference, incoming.Preference)
	merged.WorktreeEnabled = incoming.WorktreeEnabled
	if value := strings.TrimSpace(incoming.WorktreeRootPath); value != "" || !merged.WorktreeEnabled {
		merged.WorktreeRootPath = value
	}
	if value := strings.TrimSpace(incoming.WorktreeBaseBranch); value != "" || !merged.WorktreeEnabled {
		merged.WorktreeBaseBranch = value
	}
	if value := strings.TrimSpace(incoming.WorktreeBranch); value != "" || !merged.WorktreeEnabled {
		merged.WorktreeBranch = value
	}
	if incoming.CreatedAt > 0 {
		merged.CreatedAt = incoming.CreatedAt
	}
	if incoming.UpdatedAt > 0 {
		merged.UpdatedAt = incoming.UpdatedAt
	}
	if value := strings.TrimSpace(incoming.UpdatedAgo); value != "" {
		merged.UpdatedAgo = value
	}
	if incoming.Lifecycle != nil {
		merged.Lifecycle = cloneClientSessionLifecycle(incoming.Lifecycle)
	}
	if incoming.ActiveRunIntent != nil {
		merged.ActiveRunIntent = cloneClientSessionV3RunIntent(incoming.ActiveRunIntent)
		if lifecycle := v3RunIntentSessionLifecycle(merged.ID, incoming.ActiveRunIntent); lifecycle != nil {
			merged.Lifecycle = lifecycle
		}
	}
	if value := strings.TrimSpace(incoming.SessionAPI); value != "" {
		merged.SessionAPI = value
	}
	if incoming.LastEventSeq != 0 {
		merged.LastEventSeq = incoming.LastEventSeq
	}
	if incoming.ProjectionHighWatermarkSeq != 0 {
		merged.ProjectionHighWatermarkSeq = incoming.ProjectionHighWatermarkSeq
	}
	return merged
}

func modelSessionSummaryFromClient(record client.SessionSummary) model.SessionSummary {
	title := strings.TrimSpace(record.Title)
	if title == "" {
		title = strings.TrimSpace(record.ID)
	}
	return model.SessionSummary{
		ID:                         strings.TrimSpace(record.ID),
		WorkspacePath:              strings.TrimSpace(record.WorkspacePath),
		WorkspaceName:              strings.TrimSpace(record.WorkspaceName),
		Title:                      title,
		Mode:                       strings.TrimSpace(record.Mode),
		Metadata:                   cloneMetadataMap(record.Metadata),
		PendingPermissionCount:     record.PendingPermissionCount,
		Lifecycle:                  cloneClientSessionLifecycle(record.Lifecycle),
		Preference:                 mergeClientModelPreference(client.ModelPreference{}, record.Preference),
		WorktreeEnabled:            record.WorktreeEnabled,
		WorktreeRootPath:           strings.TrimSpace(record.WorktreeRootPath),
		WorktreeBaseBranch:         strings.TrimSpace(record.WorktreeBaseBranch),
		WorktreeBranch:             strings.TrimSpace(record.WorktreeBranch),
		CreatedAt:                  record.CreatedAt,
		UpdatedAt:                  record.UpdatedAt,
		UpdatedAgo:                 formatAgo(record.UpdatedAt),
		SessionAPI:                 strings.TrimSpace(record.SessionAPI),
		LastEventSeq:               record.LastEventSeq,
		ProjectionHighWatermarkSeq: record.ProjectionHighWatermarkSeq,
	}
}

func modelSessionSummariesFromTUIWorkset(workset client.SessionV3Workset) []model.SessionSummary {
	clientSummaries := sessionSummariesFromTUIWorkset(workset)
	modelSummaries := make([]model.SessionSummary, 0, len(clientSummaries))
	for _, session := range clientSummaries {
		summary := modelSessionSummaryFromClient(session)
		metadata := cloneMetadataMap(summary.Metadata)
		if preference, ok := workset.PreferencesBySession[session.ID]; ok {
			summary.Preference = mergeClientModelPreference(summary.Preference, preference)
		}
		if permissions, ok := workset.PermissionsBySession[session.ID]; ok {
			summary.PendingPermissionCount = len(permissions)
			metadata = putSessionWorksetMetadata(metadata, "v3_pending_permissions", permissions)
		}
		if usage, ok := workset.UsageBySession[session.ID]; ok {
			metadata = putSessionWorksetMetadata(metadata, "v3_usage_summary", usage)
		}
		if plans, ok := workset.PlansBySession[session.ID]; ok {
			metadata = putSessionWorksetMetadata(metadata, "v3_plans", plans)
		}
		if revisions, ok := workset.PlanRevisionsBySession[session.ID]; ok {
			metadata = putSessionWorksetMetadata(metadata, "v3_plan_revisions", revisions)
		}
		if policy, ok := workset.AgentModelPolicyBySession[session.ID]; ok {
			metadata = putSessionWorksetMetadata(metadata, "v3_agent_model_policy", policy)
		}
		if intents := workset.RunIntentsBySession[session.ID]; len(intents) > 0 {
			intent := intents[0]
			summary.ActiveRunIntent = cloneClientSessionV3RunIntent(&intent)
			metadata = putSessionWorksetMetadata(metadata, "v3_run_intents", intents)
			if lifecycle := v3RunIntentSessionLifecycle(summary.ID, &intent); lifecycle != nil {
				summary.Lifecycle = lifecycle
			}
		}
		if projection, ok := workset.ProjectionsBySession[session.ID]; ok {
			summary.LastEventSeq = projection.LastEventSeq
			summary.ProjectionHighWatermarkSeq = projection.ProjectionHighWatermarkSeq
		}
		summary.Metadata = metadata
		modelSummaries = append(modelSummaries, summary)
	}
	return applySessionDepths(modelSummaries)
}

func putSessionWorksetMetadata(metadata map[string]any, key string, value any) map[string]any {
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[key] = value
	return metadata
}

func chatSessionTabsWithExtras(summaries []model.SessionSummary, extras []client.SessionSummary) []ui.ChatSessionTab {
	merged := make([]model.SessionSummary, 0, len(summaries)+len(extras))
	indexByID := make(map[string]int, len(summaries)+len(extras))
	appendSummary := func(summary model.SessionSummary) {
		id := strings.TrimSpace(summary.ID)
		title := strings.TrimSpace(summary.Title)
		if id == "" && title == "" {
			return
		}
		if id == "" {
			id = title
		}
		summary.ID = id
		if idx, ok := indexByID[id]; ok {
			merged[idx] = mergeHomeSessionSummary(merged[idx], summary)
			return
		}
		indexByID[id] = len(merged)
		merged = append(merged, summary)
	}
	for _, summary := range summaries {
		appendSummary(summary)
	}
	for _, extra := range extras {
		appendSummary(modelSessionSummaryFromClient(extra))
	}
	return chatSessionTabsFromSummaries(merged)
}

func filterSessionSummariesForExactPath(summaries []model.SessionSummary, path string) []model.SessionSummary {
	normalizedPath := normalizePath(path)
	if normalizedPath == "" {
		return nil
	}
	filtered := make([]model.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !pathsEqual(summary.WorkspacePath, normalizedPath) {
			continue
		}
		filtered = append(filtered, summary)
	}
	return filtered
}

func scopedSessionTabsForPath(path string, summaries []model.SessionSummary, extras []client.SessionSummary) []ui.ChatSessionTab {
	return chatSessionTabsWithExtras(filterSessionSummariesForExactPath(summaries, path), extras)
}

func localWorkspaceBindingIDForActiveWorkspace(home model.HomeModel, activeWorkspacePath string) string {
	for _, ws := range home.Workspaces {
		if ws.Active {
			return strings.TrimSpace(ws.LocalWorkspaceBindingID)
		}
	}
	activeWorkspacePath = normalizePath(activeWorkspacePath)
	if activeWorkspacePath == "" {
		return ""
	}
	for _, ws := range home.Workspaces {
		if pathsEqual(ws.Path, activeWorkspacePath) {
			return strings.TrimSpace(ws.LocalWorkspaceBindingID)
		}
	}
	return ""
}

func (a *App) activeLocalWorkspaceBindingID() string {
	if a == nil {
		return ""
	}
	return localWorkspaceBindingIDForActiveWorkspace(a.homeModel, a.activeWorkspacePath())
}

func (a *App) listSessionsForActiveContext(ctx context.Context, limit int, workspacePath string) ([]client.SessionSummary, error) {
	workset, err := a.loadTUISessionWorksetForPath(ctx, limit, workspacePath)
	if err != nil {
		return nil, err
	}
	return sessionSummariesFromTUIWorkset(workset), nil
}

func (a *App) loadTUISessionWorksetForPath(ctx context.Context, limit int, path string) (client.SessionV3Workset, error) {
	path = normalizePath(strings.TrimSpace(path))
	if path == "" {
		return client.SessionV3Workset{}, errors.New("workspace path is required")
	}
	return a.loadTUISessionWorkset(ctx, tuiSessionWorksetLoadOptions{Limit: limit, WorkspacePaths: []string{path}})
}

type tuiSessionWorksetLoadOptions struct {
	Limit           int
	SessionIDs      []string
	WorkspacePaths  []string
	CWDPath         string
	BeforeUpdatedAt *int64
	BeforeSessionID string
}

func (a *App) loadTUISessionWorkset(ctx context.Context, opts tuiSessionWorksetLoadOptions) (client.SessionV3Workset, error) {
	if a == nil || a.api == nil {
		return client.SessionV3Workset{}, errors.New("api is unavailable")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = homeRecentSessionLimit
	}
	state, err := tuiRealtimeWorksetStateFromOptions(opts)
	if err != nil {
		return client.SessionV3Workset{}, err
	}
	return a.api.GetSessionV3TUIWorkset(ctx, client.SessionV3TUIWorksetRequest{
		SessionIDs: trimTUIRealtimeStrings(opts.SessionIDs),
		Scope: client.SessionV3TUIWorksetScope{
			WorkspacePaths: append([]string(nil), state.WorkspacePaths...),
			CWDPath:        state.CWDPath,
		},
		Recent: client.SessionV3WorksetRecent{
			Limit:           limit,
			BeforeUpdatedAt: opts.BeforeUpdatedAt,
			BeforeSessionID: strings.TrimSpace(opts.BeforeSessionID),
		},
		History: client.SessionV3WorksetHistory{
			Mode:                  "tail",
			MaxMessagesPerSession: 20,
			MaxEventsPerSession:   50,
			ManifestPolicy:        "manifest",
			IncludeEvents:         true,
		},
	})
}

func canonicalUniquePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = normalizePath(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func sessionSummariesFromTUIWorkset(workset client.SessionV3Workset) []client.SessionSummary {
	out := make([]client.SessionSummary, 0, len(workset.SessionOrder))
	seen := make(map[string]struct{}, len(workset.SessionsByID))
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		summary, ok := workset.SessionsByID[id]
		if !ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, summary)
	}
	for _, id := range workset.SessionOrder {
		appendID(id)
	}
	if len(seen) < len(workset.SessionsByID) {
		ids := make([]string, 0, len(workset.SessionsByID)-len(seen))
		for id := range workset.SessionsByID {
			if _, ok := seen[id]; !ok {
				ids = append(ids, id)
			}
		}
		sort.SliceStable(ids, func(i, j int) bool {
			left := workset.SessionsByID[ids[i]]
			right := workset.SessionsByID[ids[j]]
			if left.UpdatedAt == right.UpdatedAt {
				return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
			}
			return left.UpdatedAt > right.UpdatedAt
		})
		for _, id := range ids {
			appendID(id)
		}
	}
	return out
}

const (
	workspaceOverviewDesktopSessionLimit = 200
	homeRecentSessionLimit               = 50
)

func chatSessionPaletteItemsFromTabs(tabs []ui.ChatSessionTab) []ui.ChatSessionPaletteItem {
	items := make([]ui.ChatSessionPaletteItem, 0, len(tabs))
	seen := make(map[string]struct{}, len(tabs))

	for _, tab := range tabs {
		id := strings.TrimSpace(tab.ID)
		title := strings.TrimSpace(tab.Title)
		if id == "" && title == "" {
			continue
		}
		if id == "" {
			id = title
		}
		if title == "" {
			title = id
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, ui.ChatSessionPaletteItem{
			ID:              id,
			Title:           title,
			WorkspaceName:   strings.TrimSpace(tab.WorkspaceName),
			WorkspacePath:   strings.TrimSpace(tab.WorkspacePath),
			WorktreeEnabled: tab.WorktreeEnabled,
			WorktreeBranch:  strings.TrimSpace(tab.WorktreeBranch),
			Mode:            strings.TrimSpace(tab.Mode),
			CreatedAt:       tab.CreatedAt,
			UpdatedAt:       tab.UpdatedAt,
			ActiveStartedAt: tab.ActiveStartedAt,
			UpdatedAgo:      strings.TrimSpace(tab.UpdatedAgo),
			Active:          tab.Active,
			NeedsAttention:  tab.NeedsAttention,
			ActivityLabel:   strings.TrimSpace(tab.ActivityLabel),
			Group:           strings.TrimSpace(tab.Group),
			ProgressLabel:   strings.TrimSpace(tab.ProgressLabel),
			Provider:        strings.TrimSpace(tab.Provider),
			ModelName:       strings.TrimSpace(tab.ModelName),
			ServiceTier:     strings.TrimSpace(tab.ServiceTier),
			ContextMode:     strings.TrimSpace(tab.ContextMode),
			Background:      tab.Background,
			ParentSessionID: strings.TrimSpace(tab.ParentSessionID),
			LineageKind:     strings.TrimSpace(tab.LineageKind),
			LineageLabel:    strings.TrimSpace(tab.LineageLabel),
			AssignmentLabel: strings.TrimSpace(tab.AssignmentLabel),
			TargetKind:      strings.TrimSpace(tab.TargetKind),
			TargetName:      strings.TrimSpace(tab.TargetName),
			Depth:           tab.Depth,
		})
	}

	return items
}

func modelSessionSummariesFromV3SyncSnapshot(snapshot client.SessionV3SyncSnapshot) []model.SessionSummary {
	ordered := make([]model.SessionSummary, 0, len(snapshot.SessionsByID))
	seen := make(map[string]struct{}, len(snapshot.SessionsByID))
	activeIDs := make(map[string]struct{}, len(snapshot.ActiveSessionIDs))
	for _, id := range snapshot.ActiveSessionIDs {
		if id = strings.TrimSpace(id); id != "" {
			activeIDs[id] = struct{}{}
		}
	}
	appendSession := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		session, ok := snapshot.SessionsByID[id]
		if !ok {
			return
		}
		seen[id] = struct{}{}
		if projection, ok := snapshot.ProjectionsBySession[id]; ok {
			session.SessionAPI = "v3"
			session.LastEventSeq = projection.LastEventSeq
			session.ProjectionHighWatermarkSeq = projection.ProjectionHighWatermarkSeq
		}
		summary := modelSessionSummaryFromClient(session)
		if permission, ok := snapshot.PermissionSummariesBySession[id]; ok {
			summary.PendingPermissionCount = permission.PendingApprovalCount
		}
		if view, ok := snapshot.SessionViewsByID[id]; ok {
			summary.HasActivePlan = view.HasActivePlan != nil && *view.HasActivePlan
			if view.ActivePlan != nil {
				activePlan := *view.ActivePlan
				summary.ActivePlan = &activePlan
				summary.HasActivePlan = true
			}
		}
		if runState, ok := snapshot.CurrentRunStateBySession[id]; ok {
			intent := client.SessionV3RunIntent{
				SessionID:     id,
				RunID:         strings.TrimSpace(runState.RunID),
				Status:        strings.TrimSpace(runState.Status),
				BlockedReason: strings.TrimSpace(runState.BlockedReason),
				CreatedAt:     runState.CreatedAt,
				StartedAt:     runState.StartedAt,
				CompletedAt:   runState.CompletedAt,
				DurationMS:    runState.DurationMS,
				UpdatedAt:     runState.UpdatedAt,
				EventSeq:      runState.EventSeq,
			}
			summary.ActiveRunIntent = &intent
			summary.Lifecycle = &client.SessionLifecycleSnapshot{
				SessionID: id,
				RunID:     intent.RunID,
				Active:    runState.Active,
				Phase:     intent.Status,
				StartedAt: intent.StartedAt,
				EndedAt:   intent.CompletedAt,
				UpdatedAt: intent.UpdatedAt,
				Error:     intent.BlockedReason,
			}
		} else if _, active := activeIDs[id]; active {
			summary.Lifecycle = &client.SessionLifecycleSnapshot{SessionID: id, Active: true, Phase: "active", UpdatedAt: summary.UpdatedAt}
		}
		ordered = append(ordered, summary)
	}
	for _, id := range snapshot.SessionOrder {
		appendSession(id)
	}
	if len(seen) < len(snapshot.SessionsByID) {
		ids := make([]string, 0, len(snapshot.SessionsByID)-len(seen))
		for id := range snapshot.SessionsByID {
			if _, ok := seen[id]; !ok {
				ids = append(ids, id)
			}
		}
		sort.SliceStable(ids, func(i, j int) bool {
			left, right := snapshot.SessionsByID[ids[i]], snapshot.SessionsByID[ids[j]]
			if left.UpdatedAt != right.UpdatedAt {
				return left.UpdatedAt > right.UpdatedAt
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			appendSession(id)
		}
	}
	return applySessionDepths(ordered)
}

func (a *App) queueSessionManagerOpen(query, openRoute string) error {
	if a == nil || a.api == nil {
		return errors.New("api is unavailable")
	}
	if !a.reloading.CompareAndSwap(false, true) {
		return errors.New("session data is already loading")
	}
	query = strings.TrimSpace(query)
	openRoute = strings.TrimSpace(openRoute)
	if a.home != nil {
		a.home.SetStatus("loading V3 sessions...")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		snapshot, err := a.api.GetSessionV3SyncBootstrap(ctx, client.SessionV3SyncBootstrapRequest{
			Surface: "tui",
			Selector: client.SessionV3SyncSelector{
				Kind:      "recent",
				Global:    true,
				Recent:    client.SessionV3WorksetRecent{Limit: workspaceOverviewDesktopSessionLimit},
				Attention: client.SessionV3SyncAttention{PendingPermissions: true},
			},
			History: client.SessionV3WorksetHistory{Mode: "none"},
			Resources: client.SessionV3SyncResources{
				CurrentRunState:     true,
				PermissionSummaries: true,
				ActivePlan:          true,
			},
			IncludeActive: true,
		})
		result := homeReloadResult{sessionQuery: query, sessionOpenRoute: openRoute, err: err}
		if err == nil {
			result.sessionSnapshot = &snapshot
		}
		select {
		case a.reloadCh <- result:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptReloadReady))
		}
	}()
	return nil
}

func (a *App) openLoadedHomeSessionsModal(query string) {
	items := chatSessionPaletteItemsFromTabs(chatSessionTabsFromSummaries(a.homeModel.RecentSessions))
	if !a.home.OpenSessionsModal(items, strings.TrimSpace(query)) {
		a.home.SetStatus("session manager unavailable while another modal is open")
		return
	}
	a.home.SetStatus("session manager")
}

func (a *App) openHomeSessionsModal(query string) {
	a.home.ClearCommandOverlay()
	if err := a.queueSessionManagerOpen(query, "home"); err != nil {
		a.home.SetStatus(fmt.Sprintf("/sessions failed: %v", err))
	}
}

func (a *App) openLoadedChatSessionsPalette(query string) {
	if a.chat == nil {
		return
	}
	a.chat.SetSessionTabs(chatSessionTabsFromSummaries(a.homeModel.RecentSessions))
	if !a.chat.OpenSessionsPalette(a.chat.SessionPaletteItems(), strings.TrimSpace(query)) {
		a.home.SetStatus("sessions palette unavailable while another modal is open")
		return
	}
	a.home.SetStatus("sessions palette")
}

func (a *App) openChatSessionsPalette(query string) error {
	if a.chat == nil {
		return errors.New("chat is unavailable")
	}
	return a.queueSessionManagerOpen(query, "chat")
}

func (a *App) consumeChatActions() {
	if a.route != "chat" || a.chat == nil {
		return
	}
	for {
		action, ok := a.chat.PopChatAction()
		if !ok {
			return
		}
		a.handleChatAction(action)
		if a.route != "chat" {
			return
		}
	}
}

func (a *App) handleChatAction(action ui.ChatAction) {
	switch action.Kind {
	case ui.ChatActionOpenSession:
		sessionID := strings.TrimSpace(action.Session.ID)
		if sessionID == "" {
			a.home.SetStatus("open session failed: missing session id")
			return
		}
		err := a.openExistingSession(model.SessionSummary{
			ID:               sessionID,
			WorkspacePath:    strings.TrimSpace(action.Session.WorkspacePath),
			WorkspaceName:    strings.TrimSpace(action.Session.WorkspaceName),
			Title:            strings.TrimSpace(action.Session.Title),
			Mode:             strings.TrimSpace(action.Session.Mode),
			WorktreeEnabled:  false,
			WorktreeRootPath: "",
			WorktreeBranch:   "",
		})
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("open session failed: %v", err))
		}
	case ui.ChatActionActivatePlan:
		if a.api == nil {
			a.home.SetStatus("activate plan failed: api client is not configured")
			return
		}
		if a.chat == nil {
			a.home.SetStatus("activate plan failed: chat is unavailable")
			return
		}
		sessionID := strings.TrimSpace(a.chat.SessionID())
		if sessionID == "" {
			a.home.SetStatus("activate plan failed: session id is unavailable")
			return
		}
		planID := strings.TrimSpace(action.Plan.ID)
		if planID == "" {
			a.home.SetStatus("activate plan failed: plan id is unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		plan, err := a.api.SetActiveSessionPlan(ctx, sessionID, planID)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("activate plan failed: %v", err))
			return
		}
		a.chat.SetActivePlan(chatPlanLabel(plan))
		a.home.SetStatus(fmt.Sprintf("active plan: %s", plan.ID))
		a.chat.AppendSystemMessage(fmt.Sprintf("Active plan set to %s (%s).", plan.ID, emptyFallback(plan.Title, "untitled")))
	case ui.ChatActionRecoverPlan:
		if a.api == nil || a.chat == nil {
			a.home.SetStatus("plan recovery failed: API or chat is unavailable")
			return
		}
		sessionID := strings.TrimSpace(a.chat.SessionID())
		planID := strings.TrimSpace(action.Plan.ID)
		if sessionID == "" || planID == "" {
			a.home.SetStatus("plan recovery failed: session id or plan id is unavailable")
			return
		}
		automatic := action.Recovery.ContinueAutomatically
		req := client.SessionPlanRevisionRequest{PlanID: planID, Version: action.Plan.Version, RevisionVersion: action.Plan.Version, CheckpointID: strings.TrimSpace(action.Recovery.CheckpointID), ContinuationPolicy: strings.TrimSpace(action.Recovery.ContinuationPolicy), ContinueAutomatically: &automatic}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var result client.SessionPlanLifecycleResult
		var err error
		switch action.Recovery.Action {
		case "restore_only":
			result, err = a.api.RestoreSessionV3PlanRevision(ctx, sessionID, req)
		case "fast_forward", "final_checkpoint":
			req.Restart, req.Start, req.SkipPrior = true, true, true
			result, err = a.api.JumpSessionV3PlanToCheckpoint(ctx, sessionID, req)
		case "restart_selected":
			req.Restart, req.Start = true, true
			result, err = a.api.RestartSessionV3PlanFromRevision(ctx, sessionID, req)
		default:
			options := client.SessionPlanExecutionOptions{CheckpointID: req.CheckpointID, ContinuationPolicy: req.ContinuationPolicy, ContinueAutomatically: req.ContinueAutomatically}
			result, err = a.api.StartSessionV3PlanCheckpointed(ctx, sessionID, planID, options)
		}
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("plan recovery failed: %v", err))
			return
		}
		if strings.TrimSpace(result.Plan.ID) != "" {
			a.chat.SetActivePlan(chatPlanLabel(result.Plan))
		}
		a.home.SetStatus(fmt.Sprintf("plan recovery applied: %s", action.Recovery.Action))
		a.chat.AppendSystemMessage(fmt.Sprintf("Plan recovery action %s applied through the V3 lifecycle.", action.Recovery.Action))
	case ui.ChatActionPlanExecution:
		if a.api == nil || a.chat == nil {
			a.home.SetStatus("plan action failed: API or chat is unavailable")
			return
		}
		sessionID, planID := strings.TrimSpace(a.chat.SessionID()), strings.TrimSpace(action.Plan.ID)
		checkpointID := strings.TrimSpace(action.PlanExecution.CheckpointID)
		if sessionID == "" || planID == "" {
			a.home.SetStatus("plan action failed: session id or plan id is unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var result client.SessionPlanLifecycleResult
		var err error
		switch action.PlanExecution.Operation {
		case "stop":
			result, err = a.api.StopSessionV3PlanRun(ctx, sessionID, client.SessionPlanCurrentRunRequest{PlanID: planID})
		case "accept":
			if checkpointID == "" {
				err = errors.New("checkpoint id is unavailable")
				break
			}
			result, err = a.api.AcceptSessionV3PlanCheckpoint(ctx, sessionID, checkpointID, client.SessionPlanCheckpointAcceptRequest{PlanID: planID, Result: "accepted"})
		case "resolve":
			if checkpointID == "" {
				err = errors.New("checkpoint id is unavailable")
				break
			}
			result, err = a.api.ResolveSessionV3BlockedCheckpoint(ctx, sessionID, checkpointID, client.SessionPlanCheckpointResolveRequest{PlanID: planID, Result: "resolved", StartNext: action.PlanExecution.StartNext, ContinueNext: action.PlanExecution.StartNext})
		case "restart":
			if checkpointID == "" {
				err = errors.New("checkpoint id is unavailable")
				break
			}
			result, err = a.api.RestartSessionV3PlanCheckpoint(ctx, sessionID, checkpointID, planID)
		case "rewind":
			if checkpointID == "" {
				err = errors.New("checkpoint id is unavailable")
				break
			}
			result, err = a.api.RewindSessionV3PlanCheckpoint(ctx, sessionID, checkpointID, planID)
		case "toggle_policy":
			if action.PlanExecution.Automatic {
				result, err = a.api.ResumeSessionV3PlanCheckpointed(ctx, sessionID, client.SessionPlanCurrentRunRequest{PlanID: planID})
			} else {
				result, err = a.api.ResumeSessionV3PlanAutomatic(ctx, sessionID, client.SessionPlanCurrentRunRequest{PlanID: planID})
			}
		default:
			err = fmt.Errorf("unsupported plan action %q", action.PlanExecution.Operation)
		}
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("plan action failed: %v", err))
			a.chat.ShowToast(ui.ToastError, fmt.Sprintf("Plan action failed: %v", err))
			return
		}
		if strings.TrimSpace(result.Plan.ID) != "" {
			runID := ""
			if result.RunIntent != nil {
				runID = strings.TrimSpace(result.RunIntent.RunID)
			}
			a.chat.SetPlanExecutionState(chatSessionPlanFromClient(result.Plan), nil, runID, strings.TrimSpace(result.Transition))
		}
		a.home.SetStatus("plan action applied: " + strings.ReplaceAll(action.PlanExecution.Operation, "_", " "))
		a.chat.ShowToast(ui.ToastSuccess, "Plan execution updated")
	case ui.ChatActionSavePlan:
		if a.api == nil {
			a.home.SetStatus("save plan failed: api client is not configured")
			return
		}
		if a.chat == nil {
			a.home.SetStatus("save plan failed: chat is unavailable")
			return
		}
		sessionID := strings.TrimSpace(a.chat.SessionID())
		if sessionID == "" {
			a.home.SetStatus("save plan failed: session id is unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		saved, err := a.api.SaveSessionPlan(ctx, sessionID, client.SessionPlanUpsertRequest{
			ID:            strings.TrimSpace(action.Plan.ID),
			PlanID:        strings.TrimSpace(action.Plan.ID),
			Title:         strings.TrimSpace(action.Plan.Title),
			Plan:          action.Plan.Plan,
			Document:      clientSessionPlanDocumentFromAny(action.Plan.Document),
			Status:        strings.TrimSpace(action.Plan.Status),
			ApprovalState: strings.TrimSpace(action.Plan.ApprovalState),
		})
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("save plan failed: %v", err))
			return
		}
		a.chat.SetActivePlan(chatPlanLabel(saved))
		a.chat.AppendSystemMessage(fmt.Sprintf("Saved current plan %s (%s).", emptyFallback(strings.TrimSpace(saved.ID), "current"), emptyFallback(strings.TrimSpace(saved.Title), "untitled")))
		a.home.SetStatus(fmt.Sprintf("saved current plan: %s", emptyFallback(strings.TrimSpace(saved.Title), strings.TrimSpace(saved.ID))))
	case ui.ChatActionOpenAgentsModal:
		a.openAgentsModal()
	case ui.ChatActionOpenModelsModal:
		a.openModelsModal("")
	case ui.ChatActionCycleThinking:
		a.cycleThinkingLevel()
	case ui.ChatActionCycleRoute:
		a.cycleChatRoute()
	case ui.ChatActionToggleBypassPermissions:
		a.setPermissionsBypass(!a.homeModel.BypassPermissions)
	}
}

func (a *App) handleHomeAction(action ui.HomeAction) {
	switch action.Kind {
	case ui.HomeActionSetDefaultSessionMode:
		a.applyDefaultNewSessionModeSetting(action.SessionMode)
	case ui.HomeActionOpenSession, ui.HomeActionOpenAlertSession:
		err := a.openExistingSession(model.SessionSummary{
			ID:               strings.TrimSpace(action.SessionID),
			WorkspacePath:    strings.TrimSpace(action.WorkspacePath),
			WorkspaceName:    strings.TrimSpace(action.WorkspaceName),
			Title:            strings.TrimSpace(action.SessionTitle),
			Mode:             strings.TrimSpace(action.SessionMode),
			WorktreeEnabled:  action.WorktreeEnabled,
			WorktreeRootPath: strings.TrimSpace(action.WorktreeRootPath),
			WorktreeBranch:   strings.TrimSpace(action.WorktreeBranch),
		})
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("open session failed: %v", err))
		}
	case ui.HomeActionOpenWorkspaceSelector:
		a.showWorkspaceSelector()
	case ui.HomeActionSelectWorkspace:
		a.activateWorkspaceAtIndex(action.WorkspaceIndex)
	case ui.HomeActionOpenAgentsModal:
		a.openAgentsModal()
	case ui.HomeActionOpenProfilesModal:
		a.openProfilesModal()
	case ui.HomeActionSelectModelProfile:
		_ = a.selectHomeModelProfile(action.ModelProfileID)
	case ui.HomeActionRefreshCodexUsage:
		a.refreshHomeCodexAccount()
	case ui.HomeActionConsumeCodexReset:
		a.consumeHomeCodexResetCredit(action.ResetCreditID, action.IdempotencyKey)
	case ui.HomeActionCycleThinking:
		a.cycleThinkingLevel()
	case ui.HomeActionCycleRoute:
		a.cycleChatRoute()
	case ui.HomeActionClearAlerts:
		a.clearAlertsFromModal()
	case ui.HomeActionOpenAuthModal:
		a.openAuthModal()
	case ui.HomeActionSaveOnboarding:
		a.saveOnboarding(action.Username, action.SwarmName)
	case ui.HomeActionCreateOnboardingWorkspace:
		a.createOnboardingWorkspace(action.WorkspacePath)
	}
}

func (a *App) consumeBackgroundSessionsModalSelection() bool {
	if a == nil || a.route != "home" || a.home == nil || !a.home.SessionsModalVisible() {
		return false
	}
	selected, ok := a.home.SelectedSessionsModalItem()
	if !ok {
		return false
	}
	if !a.backgroundSessionMatchesOpenModal(selected) {
		return false
	}
	id := strings.TrimSpace(selected.ID)
	if id == "" {
		return false
	}
	for _, record := range a.homeModel.BackgroundSessions {
		if strings.TrimSpace(record.ChildSessionID) != id {
			continue
		}
		a.home.HideSessionsModal()
		if err := a.openExistingSession(model.SessionSummary{
			ID:               strings.TrimSpace(record.ChildSessionID),
			WorkspacePath:    emptyFallback(strings.TrimSpace(record.WorkspacePath), strings.TrimSpace(selected.WorkspacePath)),
			WorkspaceName:    emptyFallback(strings.TrimSpace(record.WorkspaceName), strings.TrimSpace(selected.WorkspaceName)),
			Title:            emptyFallback(strings.TrimSpace(record.ChildTitle), strings.TrimSpace(selected.Title)),
			Mode:             "auto",
			WorktreeEnabled:  strings.EqualFold(strings.TrimSpace(record.WorktreeMode), "on"),
			WorktreeRootPath: strings.TrimSpace(record.WorktreeRootPath),
			WorktreeBranch:   strings.TrimSpace(record.WorktreeBranch),
		}); err != nil {
			a.home.SetStatus(fmt.Sprintf("open session failed: %v", err))
		}
		return true
	}
	return false
}

func (a *App) handleAuthModalAction(action ui.AuthModalAction) {
	if !a.home.AuthModalVisible() && !a.home.OnboardingProviderActive() {
		return
	}
	switch action.Kind {
	case ui.AuthModalActionRefresh:
		statusHint := strings.TrimSpace(action.StatusHint)
		if statusHint == "" {
			statusHint = "Refreshing auth records..."
		}
		a.refreshAuthModalData(statusHint)
	case ui.AuthModalActionVerify:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		connection, err := a.api.VerifyAuthCredential(ctx, action.Provider, action.ID)
		if err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("verify credential failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("verify credential failed: %v", err))
			return
		}
		a.refreshAuthModalData("")
		method := strings.TrimSpace(connection.Method)
		if method == "" {
			method = "configured method"
		}
		msg := strings.TrimSpace(connection.Message)
		if msg == "" {
			msg = "connected"
		}
		if !connection.Connected {
			a.home.SetAuthModalError(fmt.Sprintf("credential verification failed: %s", msg))
			a.showToast(ui.ToastError, fmt.Sprintf("credential verification failed: %s", msg))
			return
		}
		a.home.SetAuthModalStatus(fmt.Sprintf("credential verified (%s): %s", method, msg))
	case ui.AuthModalActionUpsert:
		if action.Upsert == nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError("auth upsert payload is missing")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		record, err := a.api.UpsertAuthCredential(ctx, client.AuthCredentialUpsertRequest{
			ID:           action.Upsert.ID,
			Provider:     action.Upsert.Provider,
			Type:         action.Upsert.Type,
			Label:        action.Upsert.Label,
			Tags:         action.Upsert.Tags,
			APIKey:       action.Upsert.APIKey,
			AccessToken:  action.Upsert.AccessToken,
			RefreshToken: action.Upsert.RefreshToken,
			ExpiresAt:    action.Upsert.ExpiresAt,
			AccountID:    action.Upsert.AccountID,
			Active:       action.Upsert.Active,
		})
		if err == nil && a.chat != nil {
			a.chat.AppendUserAuthCommandMessage(action.Upsert.Provider)
		}
		if err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("save credential failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("save credential failed: %v", err))
			return
		}
		toastLevel, toastText := authCredentialUpsertToast(action.Upsert, record)
		a.showToast(toastLevel, toastText)
		a.notifyAuthAutoDefaults(record.AutoDefaults)
		if record.Connection != nil && !record.Connection.Connected {
			msg := strings.TrimSpace(record.Connection.Message)
			if msg == "" {
				msg = "connection test failed"
			}
			a.refreshAuthModalData("")
			a.home.SetAuthModalError(fmt.Sprintf("credential saved but verification failed: %s", msg))
			a.showToast(ui.ToastError, fmt.Sprintf("credential verification failed: %s", msg))
			return
		}
		a.refreshAuthModalData("")
		if a.home.OnboardingProviderActive() {
			a.home.ShowOnboardingWorkspace("Provider connected. Confirm your launch workspace to finish setup.")
		}
		if record.Connection != nil {
			method := strings.TrimSpace(record.Connection.Method)
			if method == "" {
				method = "configured method"
			}
			msg := strings.TrimSpace(record.Connection.Message)
			if msg == "" {
				msg = "connected"
			}
			a.home.SetAuthModalStatus(fmt.Sprintf("credential verified (%s): %s", method, msg))
		}
	case ui.AuthModalActionSetActive:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		record, err := a.api.SetActiveAuthCredential(ctx, action.Provider, action.ID)
		if err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("activate credential failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("activate credential failed: %v", err))
			return
		}
		a.showToast(ui.ToastSuccess, fmt.Sprintf("active credential set for %s", record.Provider))
		a.notifyAuthAutoDefaults(record.AutoDefaults)
		if record.Connection != nil && !record.Connection.Connected {
			msg := strings.TrimSpace(record.Connection.Message)
			if msg == "" {
				msg = "connection test failed"
			}
			a.refreshAuthModalData("")
			a.home.SetAuthModalError(fmt.Sprintf("credential activated but verification failed: %s", msg))
			a.showToast(ui.ToastError, fmt.Sprintf("credential verification failed: %s", msg))
			return
		}
		a.refreshAuthModalData("")
		if record.Connection != nil {
			method := strings.TrimSpace(record.Connection.Method)
			if method == "" {
				method = "configured method"
			}
			msg := strings.TrimSpace(record.Connection.Message)
			if msg == "" {
				msg = "connected"
			}
			a.home.SetAuthModalStatus(fmt.Sprintf("active credential verified (%s): %s", method, msg))
		}
	case ui.AuthModalActionDelete:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := a.api.DeleteAuthCredential(ctx, action.Provider, action.ID)
		if err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("delete credential failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("delete credential failed: %v", err))
			return
		}
		statusParts := []string{fmt.Sprintf("credential deleted: %s/%s", action.Provider, action.ID)}
		if result.Cleanup.ClearedGlobalPreference {
			statusParts = append(statusParts, "cleared default model")
		}
		if count := len(result.Cleanup.ResetAgents); count > 0 {
			label := "agents"
			if count == 1 {
				label = "agent"
			}
			statusParts = append(statusParts, fmt.Sprintf("reset %d %s to inherit; reassign in /agents", count, label))
		}
		a.showToast(ui.ToastSuccess, strings.Join(statusParts, " • "))
		a.refreshAuthModalData("")
		a.queueReload(true)
	case ui.AuthModalActionLogin:
		a.startProviderLogin(action.Login)
	case ui.AuthModalActionLoginCallback:
		a.completeProviderLogin(action.Login)
	case ui.AuthModalActionCopy:
		text := strings.TrimSpace(action.CopyText)
		if text == "" {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError("copy failed: auth URL is empty")
			return
		}
		if err := copyTextToClipboard(text); err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("copy failed: %v", err))
			return
		}
		switch a.home.AuthModalEditorMode() {
		case "codex_browser_pending":
			a.home.SetAuthModalLoading(true)
			a.home.SetAuthModalStatus("Auth URL copied to clipboard. Finish sign-in in your browser; this modal will close automatically after confirmation.")
			return
		case "codex_device_pending":
			a.home.SetAuthModalLoading(true)
			a.home.SetAuthModalStatus("Device sign-in value copied. Complete approval on another device; Swarm is still waiting for confirmation.")
			return
		}
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalStatus("Auth URL copied to clipboard. After sign-in, paste the callback URL or code here.")
		a.home.FocusAuthModalCallbackInput()
	default:
		a.home.SetAuthModalLoading(false)
	}
}

func (a *App) notifyAuthAutoDefaults(details *client.AutoDefaultsStatus) {
	if details == nil {
		return
	}
	if errText := strings.TrimSpace(details.Error); errText != "" {
		a.showToast(ui.ToastWarning, fmt.Sprintf("auth saved but utility defaults failed: %s", errText))
		return
	}
	if !details.Applied {
		return
	}

	provider := strings.TrimSpace(details.Provider)
	primaryModel := strings.TrimSpace(details.Model)
	utilityProvider := strings.TrimSpace(details.UtilityProvider)
	if utilityProvider == "" {
		utilityProvider = provider
	}
	utilityModel := strings.TrimSpace(details.UtilityModel)
	if utilityModel == "" {
		utilityModel = primaryModel
	}
	subagents := uniqueNonEmpty(details.Subagents)

	switch {
	case details.GlobalModel && len(subagents) > 0 && provider != "" && primaryModel != "":
		a.showToast(ui.ToastInfo, fmt.Sprintf("new-chat model set to %s/%s; utility model %s/%s assigned to subagents: %s", provider, model.DisplayModelName(provider, primaryModel), utilityProvider, model.DisplayModelName(utilityProvider, utilityModel), strings.Join(subagents, ", ")))
	case len(subagents) > 0 && utilityProvider != "" && utilityModel != "":
		a.showToast(ui.ToastInfo, fmt.Sprintf("utility model %s/%s assigned to subagents: %s", utilityProvider, model.DisplayModelName(utilityProvider, utilityModel), strings.Join(subagents, ", ")))
	case details.GlobalModel && provider != "" && primaryModel != "":
		a.showToast(ui.ToastInfo, fmt.Sprintf("new-chat model set to %s/%s", provider, model.DisplayModelName(provider, primaryModel)))
	}
	a.showAuthDefaultsInfo(details)
}

func (a *App) showAuthDefaultsInfo(details *client.AutoDefaultsStatus) {
	if a == nil || a.home == nil || details == nil {
		return
	}
	provider := strings.TrimSpace(details.Provider)
	primaryModel := strings.TrimSpace(details.Model)
	primaryThinking := strings.TrimSpace(details.Thinking)
	utilityProvider := strings.TrimSpace(details.UtilityProvider)
	if utilityProvider == "" {
		utilityProvider = provider
	}
	if provider == "" {
		provider = utilityProvider
	}
	utilityModel := strings.TrimSpace(details.UtilityModel)
	utilityThinking := strings.TrimSpace(details.UtilityThinking)
	if primaryModel == "" {
		primaryModel = utilityModel
	}
	if primaryThinking == "" {
		primaryThinking = utilityThinking
	}
	subagents := uniqueNonEmpty(details.Subagents)
	if provider == "" || primaryModel == "" {
		return
	}
	if utilityModel == "" || len(subagents) == 0 {
		return
	}
	info := &ui.AuthDefaultsInfo{
		Provider:        provider,
		PrimaryModel:    primaryModel,
		PrimaryThinking: primaryThinking,
		UtilityProvider: utilityProvider,
		UtilityModel:    utilityModel,
		UtilityThinking: utilityThinking,
		Subagents:       subagents,
	}
	a.home.ShowAuthDefaultsInfo(info)
}

func (a *App) handleWorkspaceModalAction(action ui.WorkspaceModalAction) {
	if !a.home.WorkspaceModalVisible() {
		return
	}
	switch action.Kind {
	case ui.WorkspaceModalActionRefresh:
		a.refreshWorkspaceModalData("Refreshing workspace list...")
	case ui.WorkspaceModalActionSave:
		targetPath := strings.TrimSpace(action.Path)
		if targetPath == "" {
			targetPath = a.activeContextPath()
		}
		if strings.TrimSpace(targetPath) == "" {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError("workspace path is required")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.AddWorkspace(ctx, targetPath, strings.TrimSpace(action.Name), strings.TrimSpace(action.ThemeID), action.MakeCurrent)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("save workspace failed: %v", err))
			return
		}
		linkedDir := strings.TrimSpace(action.LinkedDirectory)
		if linkedDir != "" {
			if _, dirErr := a.api.AddWorkspaceDirectory(ctx, strings.TrimSpace(resolution.ResolvedPath), linkedDir); dirErr != nil {
				a.home.SetWorkspaceModalLoading(false)
				a.home.SetWorkspaceModalError(fmt.Sprintf("link workspace directory failed: %v", dirErr))
				return
			}
		}
		if action.MakeCurrent {
			a.syncActiveWorkspaceSelection(resolution)
		}
		a.refreshWorkspaceModalData("")
		status := fmt.Sprintf("workspace saved: %s", displayPath(resolution.ResolvedPath))
		if linkedDir != "" {
			status = fmt.Sprintf("workspace saved and directory linked: %s", displayPath(resolution.ResolvedPath))
		}
		a.home.SetWorkspaceModalDirectory(a.activeContextPath())
		a.home.SetWorkspaceModalStatus(status)
		a.queueReload(false)
	case ui.WorkspaceModalActionSelect:
		selectorMode := a.home.WorkspaceModalIntent() == "select"
		if a.workspaceSwitchRunActive() {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError("workspace switching is unavailable while a run is active")
			return
		}
		previousWorkspacePath := a.activeWorkspacePath()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.SelectWorkspace(ctx, action.Path)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("activate workspace failed: %v", err))
			return
		}
		a.syncActiveWorkspaceSelection(resolution)
		if err := a.openV3ChatDraftAfterWorkspaceChange(previousWorkspacePath); err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("workspace switched, but new chat draft failed: %v", err))
			return
		}
		if selectorMode {
			a.home.HideWorkspaceModal()
			a.home.SetStatus(fmt.Sprintf("workspace active: %s", displayPath(resolution.ResolvedPath)))
		} else {
			a.home.SetWorkspaceModalDirectory(a.activeContextPath())
			a.refreshWorkspaceModalData("")
			a.home.SetWorkspaceModalStatus(fmt.Sprintf("workspace active: %s", displayPath(resolution.ResolvedPath)))
		}
		a.queueReload(false)
	case ui.WorkspaceModalActionMove:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.MoveWorkspace(ctx, action.Path, action.Delta)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("move workspace failed: %v", err))
			return
		}
		a.refreshWorkspaceModalData("")
		direction := "down"
		if action.Delta < 0 {
			direction = "up"
		}
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("workspace moved %s: %s", direction, resolution.WorkspaceName))
		a.queueReload(false)
	case ui.WorkspaceModalActionDelete:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.DeleteWorkspace(ctx, action.Path)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("delete workspace failed: %v", err))
			return
		}
		a.refreshWorkspaceModalData("")
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("workspace deleted: %s", displayPath(resolution.ResolvedPath)))
		a.queueReload(false)
	case ui.WorkspaceModalActionAddDirectory:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.AddWorkspaceDirectory(ctx, action.Path, action.DirectoryPath)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("link workspace directory failed: %v", err))
			return
		}
		a.refreshWorkspaceModalData("")
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("linked directory to %s: %s", resolution.WorkspaceName, displayPath(resolution.ResolvedPath)))
		a.queueReload(false)
	case ui.WorkspaceModalActionRemoveDirectory:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.RemoveWorkspaceDirectory(ctx, action.Path, action.DirectoryPath)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("remove workspace directory failed: %v", err))
			return
		}
		a.refreshWorkspaceModalData("")
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("removed linked directory from %s: %s", resolution.WorkspaceName, displayPath(resolution.ResolvedPath)))
		a.queueReload(false)
	case ui.WorkspaceModalActionOpenKeybinds:
		a.home.SetWorkspaceModalLoading(false)
		a.openKeybindsModal()
	default:
		a.home.SetWorkspaceModalLoading(false)
	}
}

func (a *App) handleWorktreesModalAction(action ui.WorktreesModalAction) {
	if !a.home.WorktreesModalVisible() {
		return
	}
	switch action.Kind {
	case ui.WorktreesModalActionRefresh:
		a.refreshWorktreesModalData("Refreshing worktrees settings...")
	case ui.WorktreesModalActionSetCreatedBranch:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		branchName := normalizeWorktreeBranchPrefix(strings.TrimSpace(action.BranchName))
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{
			WorkspacePath: a.activeContextPath(),
			BranchName:    stringPtr(branchName),
		})
		if err != nil {
			a.home.SetWorktreesModalLoading(false)
			a.home.SetWorktreesModalError(fmt.Sprintf("worktrees created branch update failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("worktrees created branch update failed: %v", err))
			return
		}
		a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
		a.home.SetWorktreesModalLoading(false)
		a.home.SetWorktreesModalStatus(a.worktreesStatusSummary(settings))
		a.showToast(ui.ToastSuccess, a.worktreesStatusSummary(settings))
	case ui.WorktreesModalActionCreateSession:
		title := strings.TrimSpace(action.Title)
		branch := strings.Trim(strings.TrimSpace(action.BranchName), "/")
		if err := a.openChatSessionWithWorktree(title, "", branch); err != nil {
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalLoading(false)
				a.home.SetWorktreesModalError(fmt.Sprintf("create worktree session failed: %v", err))
			} else if a.chat != nil {
				a.chat.SetStatus(fmt.Sprintf("create worktree session failed: %v", err))
			}
			return
		}
		// The HomePage persists behind chat, so explicitly clear the accepted
		// modal after navigation instead of leaving it open on the next home view.
		a.home.HideWorktreesModal()
	case ui.WorktreesModalActionSetBranchSource:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		baseBranch, useCurrentBranch := normalizeWorktreeSettingsBranchInput(action.BaseBranch)
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{
			WorkspacePath:    a.activeContextPath(),
			UseCurrentBranch: &useCurrentBranch,
			BaseBranch:       baseBranch,
		})
		if err != nil {
			a.home.SetWorktreesModalLoading(false)
			a.home.SetWorktreesModalError(fmt.Sprintf("worktrees branch-off source update failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("worktrees branch-off source update failed: %v", err))
			return
		}
		a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
		a.home.SetWorktreesModalLoading(false)
		a.home.SetWorktreesModalStatus(a.worktreesStatusSummary(settings))
		a.showToast(ui.ToastSuccess, a.worktreesStatusSummary(settings))
	default:
		a.home.SetWorktreesModalLoading(false)
	}
}

func (a *App) handleAgentsModalAction(action ui.AgentsModalAction) {
	if !a.home.AgentsModalVisible() {
		return
	}
	if action.Kind == ui.AgentsModalActionSave {
		if a.api == nil {
			a.home.SetAgentsModalError("agent model settings API is unavailable")
			return
		}
		patch := client.AgentModelSettingsPatch{}
		if strings.EqualFold(strings.TrimSpace(action.Agent), "swarm") {
			if action.Swarm == nil {
				a.home.SetAgentsModalError("complete Swarm Default and Plan assignments are required")
				return
			}
			patch.Swarm = action.Swarm
		} else {
			if action.Assignment == nil {
				a.home.SetAgentsModalError("system agent assignment is required")
				return
			}
			systemPatch := &client.AgentModelSettingsSystemAgentsPatch{}
			switch strings.ToLower(strings.TrimSpace(action.Agent)) {
			case "compact":
				systemPatch.Compact = action.Assignment
			case "finder":
				systemPatch.Finder = action.Assignment
			case "coder":
				systemPatch.Coder = action.Assignment
			case "designer":
				systemPatch.Designer = action.Assignment
			case "router":
				systemPatch.Router = action.Assignment
			default:
				a.home.SetAgentsModalError("unknown compiled system agent")
				return
			}
			patch.SystemAgents = systemPatch
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if _, err := a.api.PatchAgentModelSettings(ctx, patch); err != nil {
			a.home.SetAgentsModalError(fmt.Sprintf("save agent model settings failed: %v", err))
			return
		}
		a.home.HideAgentsModal()
		if a.route == "home" {
			a.showToast(ui.ToastSuccess, "agent model settings saved")
		}
		return
	}
	if action.Kind == ui.AgentsModalActionRefresh {
		a.refreshAgentsModalData("Refreshing agent model settings...")
		return
	}
	a.home.SetAgentsModalLoading(false)
}

func (a *App) handleThemeModalAction(action ui.ThemeModalAction) {
	switch action.Kind {
	case ui.ThemeModalActionPreview:
		if option, ok := a.previewThemeByTarget(action.ThemeID); ok {
			a.home.SetThemeModalStatus(fmt.Sprintf("previewing: %s", option.ID))
		}
	case ui.ThemeModalActionApply:
		themeID := strings.TrimSpace(action.ThemeID)
		if _, ok, err := a.applyThemeByTarget(themeID, true, false); !ok {
			a.clearThemePreview()
			a.home.SetStatus(fmt.Sprintf("unknown theme: %s", themeID))
		} else if err != nil {
			a.home.SetStatus(fmt.Sprintf("theme set failed: %v", err))
		} else if a.hasActiveWorkspaceThemeScope() {
			a.home.SetStatus(fmt.Sprintf("workspace theme set: %s", themeID))
		} else {
			a.home.SetStatus(fmt.Sprintf("theme set: %s", themeID))
		}
	case ui.ThemeModalActionCancel:
		a.clearThemePreview()
		themeID := strings.TrimSpace(action.ThemeID)
		if themeID == "" {
			themeID = a.effectiveThemeOption().ID
		}
		a.home.SetStatus(fmt.Sprintf("theme unchanged: %s", themeID))
	}
}

func (a *App) startProviderLogin(login *ui.AuthModalLogin) {
	provider := ""
	method := "auto"
	openBrowser := true
	if login != nil {
		provider = strings.ToLower(strings.TrimSpace(login.Provider))
		method = strings.ToLower(strings.TrimSpace(login.Method))
		if method == "" {
			method = "auto"
		}
		openBrowser = login.OpenBrowser
	}
	if provider == "" {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError("select a provider first")
		return
	}
	if provider == "copilot" {
		a.startCopilotProviderLogin(login)
		return
	}
	if provider != "codex" {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalStatus(fmt.Sprintf("%s uses API key credentials in this flow. Press Enter to add a key.", provider))
		return
	}

	if method != "auto" && method != "code" && method != "device" {
		method = "auto"
	}

	if !a.authLogging.CompareAndSwap(false, true) {
		a.home.SetAuthModalStatus("Codex OAuth login already in progress")
		return
	}

	a.home.SetAuthModalLoading(true)
	if method == "code" {
		defer a.authLogging.Store(false)
		if err := a.beginCodexCodeLogin(login); err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("oauth login failed: %v", err))
			return
		}
		a.home.SetAuthModalLoading(false)
		return
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 45*time.Second)
	session, browserWarning, err := a.beginCodexOAuthSession(startCtx, login, method, openBrowser)
	startCancel()
	if err != nil {
		a.authLogging.Store(false)
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("oauth login failed: %v", err))
		return
	}
	if method == "device" {
		a.home.StartAuthModalCodexDevicePending(codexDevicePendingStatus(browserWarning), session.VerificationURL, session.UserCode)
	} else {
		a.home.StartAuthModalCodexBrowserPending(codexBrowserPendingStatus(browserWarning), session.AuthURL)
	}
	a.home.SetAuthModalLoading(true)

	go func() {
		timeout := 6 * time.Minute
		if session.Method == "device" {
			timeout = 16 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result := a.runCodexOAuthLogin(ctx, session)
		select {
		case a.authLoginCh <- result:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptAuthReady))
		}
	}()
}

func (a *App) consumeAuthLoginResult() {
	defer a.authLogging.Store(false)
	select {
	case result := <-a.authLoginCh:
		if result.clearCodexPending {
			a.codexPending = nil
		}
		if !a.home.AuthModalVisible() && !a.home.OnboardingProviderActive() {
			if result.err == nil {
				if strings.TrimSpace(result.toast) != "" {
					a.showToast(result.toastLevel, result.toast)
				}
				a.notifyAuthAutoDefaults(result.autoDefaults)
				a.queueReload(false)
			}
			return
		}
		if result.err != nil {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalError(fmt.Sprintf("oauth login failed: %v", result.err))
			return
		}
		status := strings.TrimSpace(result.status)
		if status == "" {
			status = "OAuth login saved"
		}
		if result.hideAuthModal {
			a.home.HideAuthModal()
			if a.home.OnboardingProviderActive() {
				a.home.ShowOnboardingWorkspace("Provider connected. Confirm your launch workspace to finish setup.")
			}
		} else {
			a.home.SetAuthModalLoading(false)
			a.home.SetAuthModalStatus(status)
		}
		if strings.TrimSpace(result.toast) != "" {
			level := result.toastLevel
			a.showToast(level, result.toast)
		}
		a.notifyAuthAutoDefaults(result.autoDefaults)
		a.queueReload(false)
	default:
	}
}

func (a *App) beginCodexOAuthSession(ctx context.Context, login *ui.AuthModalLogin, method string, openBrowser bool) (codexOAuthLoginSession, string, error) {
	if a == nil || a.api == nil {
		return codexOAuthLoginSession{}, "", errors.New("auth api unavailable")
	}

	provider := "codex"
	label := ""
	active := true
	if login != nil {
		if trimmed := strings.ToLower(strings.TrimSpace(login.Provider)); trimmed != "" {
			provider = trimmed
		}
		label = strings.TrimSpace(login.Label)
		active = login.Active
	}

	apiMethod := "manual"
	if method == "device" {
		apiMethod = "device"
	}
	session, err := a.api.StartCodexOAuth(ctx, client.CodexOAuthStartRequest{
		Provider: provider,
		Label:    label,
		Active:   active,
		Method:   apiMethod,
	})
	if err != nil {
		return codexOAuthLoginSession{}, "", err
	}

	authURL := strings.TrimSpace(session.AuthURL)
	verificationURL := strings.TrimSpace(session.VerificationURL)
	userCode := strings.TrimSpace(session.UserCode)
	if method == "device" {
		if verificationURL == "" || userCode == "" {
			return codexOAuthLoginSession{}, "", errors.New("codex device login returned no verification URL or user code; use Remote URL fallback")
		}
	} else if authURL == "" {
		return codexOAuthLoginSession{}, "", errors.New("codex oauth start returned empty auth url")
	}
	browserWarning := ""
	if openBrowser && authURL != "" {
		if err := tryOpenBrowser(authURL); err != nil {
			browserWarning = fmt.Sprintf("Browser did not open automatically: %v", err)
		}
	}
	return codexOAuthLoginSession{
		Provider:        provider,
		Label:           label,
		Active:          active,
		Method:          method,
		SessionID:       strings.TrimSpace(session.SessionID),
		AuthURL:         authURL,
		VerificationURL: verificationURL,
		UserCode:        userCode,
	}, browserWarning, nil
}

func codexDevicePendingStatus(warning string) string {
	status := "Open the verification URL on any device, enter the displayed code, and approve Codex. Device code is recommended for remote TUI sessions."
	if strings.TrimSpace(warning) != "" {
		status += " " + strings.TrimSpace(warning)
	}
	return status
}

func codexBrowserPendingStatus(browserWarning string) string {
	status := "Finish local Codex sign-in in your browser. This modal will close automatically after confirmation."
	if strings.TrimSpace(browserWarning) != "" {
		status += " " + strings.TrimSpace(browserWarning)
	}
	return status
}

func (a *App) waitForCodexOAuthSession(ctx context.Context, sessionID string) (client.CodexOAuthSessionStatus, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		status, err := a.api.GetCodexOAuthStatus(ctx, sessionID)
		if err != nil {
			return client.CodexOAuthSessionStatus{}, err
		}
		switch strings.ToLower(strings.TrimSpace(status.Status)) {
		case "success", "error":
			return status, nil
		}
		select {
		case <-ctx.Done():
			return client.CodexOAuthSessionStatus{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) runCodexOAuthLogin(ctx context.Context, session codexOAuthLoginSession) authLoginResult {
	var completedSession client.CodexOAuthSessionStatus
	var err error
	if session.Method == "device" {
		completedSession, err = a.waitForCodexOAuthSession(ctx, session.SessionID)
	} else {
		callbackInput, callbackErr := waitForLocalCodexOAuthCallback(ctx, oauthStateFromAuthURL(session.AuthURL))
		if callbackErr != nil {
			return authLoginResult{err: callbackErr, clearCodexPending: true}
		}
		completedSession, err = a.api.CompleteCodexOAuth(ctx, client.CodexOAuthCompleteRequest{
			SessionID:     session.SessionID,
			CallbackInput: callbackInput,
		})
	}
	if err != nil {
		return authLoginResult{err: err, clearCodexPending: true}
	}

	statusValue := strings.ToLower(strings.TrimSpace(completedSession.Status))
	if statusValue == "error" {
		errText := strings.TrimSpace(completedSession.Error)
		if errText == "" {
			errText = "oauth login failed"
		}
		return authLoginResult{err: errors.New(errText), clearCodexPending: true}
	}
	if statusValue != "success" {
		return authLoginResult{err: fmt.Errorf("unexpected oauth status %q", completedSession.Status), clearCodexPending: true}
	}

	savedLabel := session.Label
	savedActive := session.Active
	autoDefaults := (*client.AutoDefaultsStatus)(nil)
	if completedSession.Credential != nil {
		if trimmed := strings.TrimSpace(completedSession.Credential.Label); trimmed != "" {
			savedLabel = trimmed
		}
		savedActive = completedSession.Credential.Active
		autoDefaults = completedSession.Credential.AutoDefaults
	}

	status, toast := codexLoginSuccessMessages(session.Provider, savedLabel, savedActive)
	if autoDefaults == nil {
		autoDefaults, err = a.applyAuthDefaultsAfterLogin(ctx, session.Provider, "oauth")
		if err != nil {
			if strings.TrimSpace(toast) != "" {
				toast += " "
			}
			toast += fmt.Sprintf("Utility defaults not applied: %v", err)
		}
	}

	return authLoginResult{
		status:            status,
		toastLevel:        ui.ToastSuccess,
		toast:             strings.TrimSpace(toast),
		autoDefaults:      autoDefaults,
		clearCodexPending: true,
		hideAuthModal:     true,
	}
}

func (a *App) startCopilotProviderLogin(login *ui.AuthModalLogin) {
	method := "cli"
	label := ""
	active := true
	if login != nil {
		if trimmed := normalizeCopilotAuthMethod(login.Method); trimmed != "" {
			method = trimmed
		}
		label = strings.TrimSpace(login.Label)
		active = login.Active
	}

	a.home.SetAuthModalLoading(true)
	saved, err := a.saveCopilotAuthSource(method, label, active)
	if err != nil {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("copilot auth setup failed: %v", err))
		return
	}

	if saved.Connection != nil && saved.Connection.Connected {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalStatus(fmt.Sprintf("Copilot auth source saved (%s): %s", saved.Connection.Method, strings.TrimSpace(saved.Connection.Message)))
		a.refreshAuthModalData(copilotAuthStatusHint(method))
		return
	}
	if method != "cli" && method != "gh" && saved.Connection != nil && !saved.Connection.Connected {
		a.home.SetAuthModalLoading(false)
		a.refreshAuthModalData("")
		msg := strings.TrimSpace(saved.Connection.Message)
		if msg == "" {
			msg = "connection test failed"
		}
		a.home.SetAuthModalError(fmt.Sprintf("copilot auth source saved, but verification failed: %s", msg))
		return
	}

	if method == "cli" || method == "gh" {
		a.home.SetAuthModalLoading(false)
		a.refreshAuthModalData("")
		msg := "connection test failed"
		methodLabel := method
		if saved.Connection != nil {
			if trimmed := strings.TrimSpace(saved.Connection.Method); trimmed != "" {
				methodLabel = trimmed
			}
			if trimmed := strings.TrimSpace(saved.Connection.Message); trimmed != "" {
				msg = trimmed
			}
		}
		a.home.SetAuthModalError(fmt.Sprintf("Copilot auth source saved, but the sidecar was not verified by the active swarmd runtime (%s): %s. Swarm no longer launches `%s` from /auth; sign in on that runtime, then press r/v to verify.", methodLabel, msg, copilotInteractiveLoginCommand(method).String()))
		return
	}

	a.home.SetAuthModalLoading(false)
	a.refreshAuthModalData(copilotAuthStatusHint(method))
}

func (a *App) saveCopilotAuthSource(method, label string, active bool) (client.AuthCredential, error) {
	req := client.AuthCredentialUpsertRequest{
		Provider: "copilot",
		Type:     method,
		Label:    strings.TrimSpace(label),
		Active:   active,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := a.api.UpsertAuthCredential(ctx, req)
	if err != nil {
		return client.AuthCredential{}, err
	}
	return record, nil
}

func normalizeCopilotAuthMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cli", "copilot", "copilot-cli", "copilot_login":
		return "cli"
	case "gh", "github", "github-cli", "gh_auth":
		return "gh"
	case "token", "api", "github-token", "github_token":
		return "api"
	default:
		return ""
	}
}

type interactiveCommandSpec struct {
	Name string
	Args []string
}

func (s interactiveCommandSpec) String() string {
	parts := append([]string{strings.TrimSpace(s.Name)}, s.Args...)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, " ")
}

func copilotInteractiveLoginCommand(method string) interactiveCommandSpec {
	switch normalizeCopilotAuthMethod(method) {
	case "gh":
		return interactiveCommandSpec{Name: "gh", Args: []string{"auth", "login"}}
	default:
		return interactiveCommandSpec{Name: "copilot", Args: []string{"login"}}
	}
}

func copilotInteractiveLoginStatus(method string) string {
	switch normalizeCopilotAuthMethod(method) {
	case "gh":
		return "Starting GitHub CLI auth for Copilot. Complete the login flow in the terminal, then Swarm will refresh status."
	default:
		return "Starting Copilot CLI login. Complete the login flow in the terminal, then Swarm will refresh status."
	}
}

func copilotAuthStatusHint(method string) string {
	switch normalizeCopilotAuthMethod(method) {
	case "gh":
		return "Refreshing Copilot auth status for the selected gh auth source. Use Enter or l to change method; use r or v to verify."
	case "api":
		return "Refreshing Copilot auth status for the selected GitHub token source. Use Enter or l to change method; use r or v to verify."
	default:
		return "Refreshing Copilot auth status for the selected copilot login source. Use Enter or l to change method; use r or v to verify."
	}
}

func (a *App) runInteractiveAuthCommand(ctx context.Context, spec interactiveCommandSpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("interactive auth command is not configured")
	}

	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if a.screen == nil {
		return cmd.Run()
	}
	if err := a.screen.Suspend(); err != nil {
		return fmt.Errorf("suspend screen: %w", err)
	}
	runErr := cmd.Run()
	resumeErr := a.screen.Resume()
	a.screen.EnablePaste()
	a.setMouseCapture(a.config.Input.MouseEnabled)
	a.home.SetPasteActive(a.pasteActive)
	a.screen.Clear()
	if resumeErr != nil {
		if runErr != nil {
			return fmt.Errorf("%v (resume screen failed: %w)", runErr, resumeErr)
		}
		return fmt.Errorf("resume screen failed: %w", resumeErr)
	}
	return runErr
}

func resolveRebuildBinaryPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("SWARM_REBUILD_BIN")); override != "" {
		return override, nil
	}
	if path, err := exec.LookPath("rebuild"); err == nil {
		return path, nil
	}

	isFile := func(path string) bool {
		info, statErr := os.Stat(path)
		return statErr == nil && !info.IsDir()
	}

	if toolDir := strings.TrimSpace(os.Getenv("SWARM_TOOL_BIN_DIR")); toolDir != "" {
		candidate := filepath.Join(toolDir, "rebuild")
		if isFile(candidate) {
			return candidate, nil
		}
	}

	exePath, err := os.Executable()
	if err == nil {
		base := filepath.Dir(exePath)
		candidates := []string{
			filepath.Clean(filepath.Join(base, "rebuild")),
			filepath.Clean(filepath.Join(base, "..", "libexec", "rebuild")),
		}
		for _, candidate := range candidates {
			if isFile(candidate) {
				return candidate, nil
			}
		}
	}

	return "", errors.New("rebuild binary not found (set SWARM_REBUILD_BIN)")
}
func (a *App) openWorkspaceModal() ([]client.WorkspaceEntry, error) {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorktreesModal()
	a.home.HideModelsModal()
	a.home.HideProfilesModal()
	a.home.HideCodexUsageModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.SetWorkspaceModalDirectory(a.activeContextPath())
	a.home.ShowWorkspaceModal()
	return a.loadWorkspaceModalEntries("Loading workspace manager...")
}

func (a *App) openWorktreesModalWithCreate(create bool) {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideModelsModal()
	a.home.HideProfilesModal()
	a.home.HideCodexUsageModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	if create {
		a.home.ShowWorktreeCreateModal()
	} else {
		a.home.ShowWorktreesModal()
	}
	if a.api != nil {
		statusHint := "Loading worktrees settings..."
		if create {
			statusHint = ""
		}
		a.refreshWorktreesModalData(statusHint)
	}
}

func (a *App) refreshWorktreesModalData(statusHint string) {
	if !a.home.WorktreesModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetWorktreesModalStatus(statusHint)
	}
	a.home.SetWorktreesModalLoading(true)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	settings, err := a.api.GetWorktreeSettings(ctx, a.activeContextPath())
	if err != nil {
		a.home.SetWorktreesModalLoading(false)
		a.home.SetWorktreesModalError(fmt.Sprintf("worktrees status failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("worktrees status failed: %v", err))
		return
	}
	a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
	a.home.SetWorktreesModalLoading(false)
	if !a.home.WorktreeCreateModalVisible() {
		a.home.SetWorktreesModalStatus(a.worktreesStatusSummary(settings))
	}
}

func (a *App) refreshWorkspaceModalData(statusHint string) {
	_, _ = a.loadWorkspaceModalEntries(statusHint)
}

func (a *App) loadWorkspaceModalEntries(statusHint string) ([]client.WorkspaceEntry, error) {
	if !a.home.WorkspaceModalVisible() {
		return nil, nil
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetWorkspaceModalStatus(statusHint)
	}
	a.home.SetWorkspaceModalDirectory(a.activeContextPath())
	a.home.SetWorkspaceModalLoading(true)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	entries, err := a.api.ListWorkspaces(ctx, 500)
	if err != nil {
		a.home.SetWorkspaceModalLoading(false)
		a.home.SetWorkspaceModalError(fmt.Sprintf("workspace list failed: %v", err))
		return nil, err
	}

	a.home.SetWorkspaceModalData(mapWorkspaceModalEntries(entries))
	a.home.SetWorkspaceModalLoading(false)
	if len(entries) == 0 {
		status := "No saved workspaces yet. Press s to start workspace setup."
		if a.home.WorkspaceModalIntent() == "add_dir" {
			status = "No saved workspaces yet. Press s to create one. On the last field, press Enter to save it and link the directory."
		}
		a.home.SetWorkspaceModalStatus(status)
		return entries, nil
	}
	if a.home.WorkspaceModalIntent() == "add_dir" {
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("saved workspaces: %d. Select one, press l for Link Directory, type a path, then press Enter to link it.", len(entries)))
		return entries, nil
	}
	a.home.SetWorkspaceModalStatus(fmt.Sprintf("saved workspaces: %d", len(entries)))
	return entries, nil
}

func (a *App) saveOnboarding(username, swarmName string) {
	username = strings.TrimSpace(username)
	swarmName = strings.TrimSpace(swarmName)
	if username == "" || swarmName == "" {
		a.home.SetOnboardingError("Your name and Swarm name are required.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	status, err := a.api.SaveOnboarding(ctx, client.SaveOnboardingInput{Username: username, SwarmName: swarmName})
	if err != nil {
		a.home.SetOnboardingError(fmt.Sprintf("identity save failed: %v", err))
		return
	}
	if session, err := a.api.IssueLocalProductSession(ctx); err == nil && strings.TrimSpace(session.Token) != "" {
		a.api.SetToken(session.Token)
	}
	a.home.SetOnboardingRequired(false, strings.TrimSpace(status.Identity.Username), strings.TrimSpace(status.Config.SwarmName))
	a.home.SetOnboardingWorkspacePath(a.startupCWD)
	a.home.ShowOnboardingProvider("Identity saved. Connect a provider, or press s to continue to workspace setup.")
	a.refreshAuthModalData("Loading providers...")
}

func (a *App) createOnboardingWorkspace(path string) {
	path = normalizePath(strings.TrimSpace(path))
	if path == "" {
		a.home.SetOnboardingError("The launch directory is unavailable; restart Swarm from the workspace you want to use.")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resolution, err := a.api.AddWorkspace(ctx, path, "", "", true)
		if err == nil {
			a.homeWorkspaceBootstrapped.Store(true)
		}
		var next model.HomeModel
		if err == nil {
			next, err = a.refreshHomeV3Model(ctx)
		}
		readyPath := firstNonEmpty(normalizePath(resolution.WorkspacePath), normalizePath(resolution.ResolvedPath), path)
		if err == nil && !homeModelHasReadyWorkspace(next, readyPath) {
			err = fmt.Errorf("workspace API completed but refreshed state does not include %s", displayPath(readyPath))
		}
		result := onboardingWorkspaceResult{model: next, path: readyPath, err: err}
		select {
		case a.onboardingWorkspaceCh <- result:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptOnboardingReady))
		}
	}()
}

func homeModelHasReadyWorkspace(home model.HomeModel, path string) bool {
	path = normalizePath(path)
	if path == "" {
		return false
	}
	for _, workspace := range home.Workspaces {
		if workspace.Active && pathsEqual(normalizePath(workspace.Path), path) {
			return true
		}
	}
	return false
}

func (a *App) consumeOnboardingWorkspaceResult() {
	select {
	case result := <-a.onboardingWorkspaceCh:
		if result.err != nil {
			a.home.SetOnboardingError(fmt.Sprintf("workspace setup failed: %v", result.err))
			return
		}
		a.syncActiveContextFromHomeModel(result.model)
		a.applyHomeModel(result.model)
		a.syncVaultUI()
		a.home.CompleteOnboardingWorkspace()
		a.home.SetStatus(fmt.Sprintf("workspace ready: %s", displayPath(result.path)))
	default:
	}
}

func (a *App) openAuthModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideWorktreesModal()
	a.home.HideWorkspaceModal()
	a.home.HideModelsModal()
	a.home.HideProfilesModal()
	a.home.HideCodexUsageModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowAuthModal()
	a.refreshAuthModalData("Loading auth manager...")
}

func (a *App) openProfilesModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideModelsModal()
	a.home.HideCodexUsageModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowProfilesModal()
}

func (a *App) openAgentsModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideModelsModal()
	a.home.HideProfilesModal()
	a.home.HideCodexUsageModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowAgentsModal()
	a.refreshAgentsModalData("Loading agent model settings...")
}

func (a *App) refreshAgentsModalData(statusHint string) {
	if !a.home.AgentsModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetAgentsModalStatus(statusHint)
	}
	a.home.SetAgentsModalLoading(true)

	if a.api == nil {
		a.home.SetAgentsModalLoading(false)
		a.home.SetAgentsModalError("agent API is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	settings, err := a.api.GetAgentModelSettings(ctx)
	if err != nil {
		a.home.SetAgentsModalLoading(false)
		a.home.SetAgentsModalError(fmt.Sprintf("agent model settings failed: %v", err))
		return
	}
	hints := []string{
		settings.Swarm.Action.Provider,
		settings.Swarm.Plan.Provider,
		settings.SystemAgents.Compact.Provider,
		settings.SystemAgents.Finder.Provider,
		settings.SystemAgents.Coder.Provider,
		settings.SystemAgents.Designer.Provider,
		settings.SystemAgents.Router.Provider,
	}
	resolvedModels := a.resolveProviderModelData(ctx, hints, 2000, 1200)
	a.home.SetAgentsModalData(mapCanonicalAgentModelSettings(settings, resolvedModels))
	a.home.SetAgentsModalLoading(false)
	status := "agent model settings loaded"
	if len(resolvedModels.Warnings) > 0 {
		status += " (" + strings.Join(uniqueNonEmpty(resolvedModels.Warnings), "; ") + ")"
	}
	a.home.SetAgentsModalStatus(status)
}

func (a *App) refreshAuthModalData(statusHint string) {
	if !a.home.AuthModalVisible() && !a.home.OnboardingProviderActive() {
		return
	}
	a.home.ClearAuthModalSnapshot()
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetAuthModalStatus(statusHint)
	}
	a.home.SetAuthModalLoading(true)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	providerStatuses, providerErr := a.api.ListProviders(ctx)
	credentials, credentialErr := a.api.ListAuthCredentials(ctx, "", "", 500)
	if providerErr != nil && credentialErr != nil {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("auth load failed: providers=%v, credentials=%v", providerErr, credentialErr))
		return
	}

	modalProviders := mergeAuthModalProviders(providerStatuses, credentials)
	modalProviders = filterOnboardingAuthMethods(modalProviders)
	modalCredentials := mapAuthModalCredentials(credentials.Records)
	agentProfiles := make([]ui.AgentModalProfile, 0)
	if agentState, err := a.api.ListAgents(ctx, 500); err == nil {
		for _, profile := range agentState.Profiles {
			agentProfiles = append(agentProfiles, ui.AgentModalProfile{
				Name:     strings.TrimSpace(profile.Name),
				Provider: normalizeModelProviderID(profile.Provider),
			})
		}
	}
	a.home.SetAuthModalData(modalProviders, modalCredentials)
	a.home.SetAuthModalAgentProfiles(agentProfiles)
	a.home.SetAuthModalLoading(false)

	switch {
	case credentialErr != nil:
		a.home.SetAuthModalError(fmt.Sprintf("credential list failed: %v", credentialErr))
	case providerErr != nil:
		a.home.SetAuthModalError(fmt.Sprintf("provider list failed: %v", providerErr))
	default:
		if status, ok := copilotAuthRefreshStatus(statusHint, modalProviders); ok {
			a.home.SetAuthModalStatus(status)
			return
		}
		a.home.SetAuthModalStatus(fmt.Sprintf("auth records loaded: %d", len(modalCredentials)))
	}
}

func copilotAuthRefreshStatus(statusHint string, providers []ui.AuthModalProvider) (string, bool) {
	if !strings.Contains(strings.ToLower(strings.TrimSpace(statusHint)), "copilot") {
		return "", false
	}
	for _, provider := range providers {
		if !strings.EqualFold(strings.TrimSpace(provider.ID), "copilot") {
			continue
		}
		if provider.Ready {
			reason := strings.TrimSpace(provider.Reason)
			if reason == "" {
				reason = "authenticated. New Copilot runs use the selected Swarm Copilot auth source until changed in /auth."
			}
			return fmt.Sprintf("Copilot auth status: %s", reason), true
		}
		reason := strings.TrimSpace(provider.Reason)
		if reason == "" {
			reason = "not authenticated. Press Enter or l to choose a Copilot auth source, then use r or v to verify."
		} else {
			lowerReason := strings.ToLower(reason)
			if !strings.Contains(lowerReason, "enter") && !strings.Contains(lowerReason, "press") && !strings.Contains(lowerReason, "verify") {
				reason += " Use Enter or l to change method; use r or v to verify."
			}
		}
		return fmt.Sprintf("Copilot auth status: %s", reason), true
	}
	return "Copilot auth status: unavailable (provider not reported).", true
}

func mergeAuthModalProviders(statuses []client.ProviderStatus, credentials client.AuthCredentialList) []ui.AuthModalProvider {
	// Hide Copilot from the auth provider picker for now. Existing credential
	// records remain stored, but the provider is not presented as usable until a
	// paid-plan environment is available for fair end-to-end testing.
	const copilotProviderTemporarilyDisabled = "copilot"
	providerMap := make(map[string]ui.AuthModalProvider, len(statuses)+len(credentials.Providers))
	for _, status := range statuses {
		id := strings.ToLower(strings.TrimSpace(status.ID))
		if id == "" || id == copilotProviderTemporarilyDisabled {
			continue
		}
		providerMap[id] = ui.AuthModalProvider{
			ID:              id,
			Ready:           status.Ready,
			Runnable:        status.Runnable,
			Reason:          strings.TrimSpace(status.Reason),
			RunReason:       strings.TrimSpace(status.RunReason),
			DefaultModel:    strings.TrimSpace(status.DefaultModel),
			DefaultThinking: strings.TrimSpace(status.DefaultThinking),
			AuthMethods:     mapAuthModalMethods(status.AuthMethods),
		}
	}
	for _, providerID := range credentials.Providers {
		id := strings.ToLower(strings.TrimSpace(providerID))
		if id == "" || id == copilotProviderTemporarilyDisabled {
			continue
		}
		if _, ok := providerMap[id]; !ok {
			providerMap[id] = ui.AuthModalProvider{
				ID:       id,
				Ready:    false,
				Runnable: false,
				Reason:   "stored credentials available",
			}
		}
	}
	for _, record := range credentials.Records {
		id := strings.ToLower(strings.TrimSpace(record.Provider))
		if id == "" || id == copilotProviderTemporarilyDisabled {
			continue
		}
		if _, ok := providerMap[id]; !ok {
			providerMap[id] = ui.AuthModalProvider{
				ID:       id,
				Ready:    false,
				Runnable: false,
				Reason:   "stored credentials available",
			}
		}
	}

	ids := make([]string, 0, len(providerMap))
	for id := range providerMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ui.AuthModalProvider, 0, len(ids))
	for _, id := range ids {
		out = append(out, providerMap[id])
	}
	return out
}

func filterOnboardingAuthMethods(providers []ui.AuthModalProvider) []ui.AuthModalProvider {
	for i := range providers {
		providerID := strings.ToLower(strings.TrimSpace(providers[i].ID))
		methods := providers[i].AuthMethods
		if providerID == "codex" {
			filtered := make([]ui.AuthModalAuthMethod, 0, len(methods))
			for _, method := range methods {
				methodID := strings.ToLower(strings.TrimSpace(method.ID))
				credentialType := strings.ToLower(strings.TrimSpace(method.CredentialType))
				label := strings.ToLower(strings.TrimSpace(method.Label))
				if methodID == "api" || credentialType == "api" || strings.Contains(label, "api key") {
					continue
				}
				filtered = append(filtered, method)
			}
			providers[i].AuthMethods = filtered
		}
	}
	return providers
}

func mapAuthModalMethods(methods []client.AuthMethod) []ui.AuthModalAuthMethod {
	if len(methods) == 0 {
		return nil
	}
	out := make([]ui.AuthModalAuthMethod, 0, len(methods))
	for _, method := range methods {
		id := strings.TrimSpace(method.ID)
		label := strings.TrimSpace(method.Label)
		credentialType := strings.TrimSpace(method.CredentialType)
		description := strings.TrimSpace(method.Description)
		if id == "" && label == "" {
			continue
		}
		out = append(out, ui.AuthModalAuthMethod{
			ID:             id,
			Label:          label,
			CredentialType: credentialType,
			Description:    description,
		})
	}
	return out
}

func mapAuthModalCredentials(records []client.AuthCredential) []ui.AuthModalCredential {
	out := make([]ui.AuthModalCredential, 0, len(records))
	for _, record := range records {
		out = append(out, ui.AuthModalCredential{
			ID:           record.ID,
			Provider:     record.Provider,
			Active:       record.Active,
			AuthType:     record.AuthType,
			Label:        record.Label,
			Tags:         append([]string(nil), record.Tags...),
			UpdatedAt:    record.UpdatedAt,
			CreatedAt:    record.CreatedAt,
			ExpiresAt:    record.ExpiresAt,
			Last4:        record.Last4,
			HasRefresh:   record.HasRefresh,
			HasAccountID: record.HasAccountID,
			StorageMode:  record.StorageMode,
		})
	}
	return out
}

func mapWorkspaceModalEntries(entries []client.WorkspaceEntry) []ui.WorkspaceModalWorkspace {
	out := make([]ui.WorkspaceModalWorkspace, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ui.WorkspaceModalWorkspace{
			Name:           strings.TrimSpace(entry.WorkspaceName),
			Path:           strings.TrimSpace(entry.Path),
			ThemeID:        strings.TrimSpace(entry.ThemeID),
			Directories:    append([]string(nil), entry.Directories...),
			SortIndex:      entry.SortIndex,
			Active:         entry.Active,
			AddedAt:        entry.AddedAt,
			UpdatedAt:      entry.UpdatedAt,
			LastSelectedAt: entry.LastSelectedAt,
		})
	}
	return out
}

func mapCanonicalAgentModelSettings(settings client.AgentModelSettings, resolved providerModelResolverResult) ui.AgentsModalData {
	modelsByProvider := make(map[string][]string, len(resolved.ModelsByProvider))
	for provider, models := range resolved.ModelsByProvider {
		provider = normalizeModelProviderID(provider)
		if provider != "" {
			modelsByProvider[provider] = append([]string(nil), models...)
		}
	}
	catalog := make(map[string]client.ModelCatalogRecord, len(resolved.CatalogByKey))
	for key, record := range resolved.CatalogByKey {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			catalog[key] = record
		}
	}
	providers := append([]string(nil), resolved.ProviderIDs...)
	assignments := []client.AgentModelAssignment{
		settings.Swarm.Action,
		settings.Swarm.Plan,
		settings.SystemAgents.Compact,
		settings.SystemAgents.Finder,
		settings.SystemAgents.Coder,
		settings.SystemAgents.Designer,
		settings.SystemAgents.Router,
	}
	for _, assignment := range assignments {
		provider := normalizeModelProviderID(assignment.Provider)
		if provider == "" {
			continue
		}
		providers = append(providers, provider)
		if modelID := strings.TrimSpace(assignment.Model); modelID != "" {
			modelsByProvider[provider] = append(modelsByProvider[provider], modelID)
		}
	}
	providers = dedupeModelValues(providers)
	for provider, models := range modelsByProvider {
		modelsByProvider[provider] = dedupeModelValues(models)
	}
	sort.Strings(providers)
	return ui.AgentsModalData{
		Settings:         settings,
		Providers:        providers,
		ModelsByProvider: modelsByProvider,
		ModelCatalog:     catalog,
	}
}

func (a *App) handleWorkspaceCommand(args []string) {
	if len(args) == 0 {
		a.showWorkspaceSelector()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "open", "manage", "crud":
		a.showWorkspaceManager()
	case "save":
		target := "."
		allowPathEdit := false
		if len(args) > 1 {
			target = strings.TrimSpace(strings.Join(args[1:], " "))
			allowPathEdit = true
		}
		target = a.resolveWorkspaceTarget(target)
		a.openWorkspaceModalForSave(target, allowPathEdit)
	case "select", "use":
		if len(args) < 2 {
			a.showWorkspaceSelector()
			return
		}
		if a.workspaceSwitchRunActive() {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("workspace switching is unavailable while a run is active")
			return
		}
		target := strings.TrimSpace(strings.Join(args[1:], " "))
		path, ok := a.findWorkspacePath(target)
		if !ok {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("workspace not found: %s", target))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resolution, err := a.api.SelectWorkspace(ctx, path)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("workspace switch failed: %v", err))
			return
		}
		previousWorkspacePath := a.activeWorkspacePath()
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("workspace active: %s", resolution.ResolvedPath))
		a.syncActiveWorkspaceSelection(resolution)
		if err := a.openV3ChatDraftAfterWorkspaceChange(previousWorkspacePath); err != nil {
			a.home.SetStatus(fmt.Sprintf("workspace switched, but new chat draft failed: %v", err))
			return
		}
		a.queueReload(false)
	case "tree", "find", "scan":
		query := strings.TrimSpace(strings.Join(args[1:], " "))
		a.scanWorkspaceTree(query)
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /workspace [select [name|#n]|manage|save|scan]")
	}
}

func (a *App) handleAddDirectoryCommand(args []string) {
	prefill := ""
	if len(args) > 0 {
		prefill = strings.TrimSpace(strings.Join(args, " "))
	}
	if prefill == "" {
		prefill = "~/"
	}
	a.openWorkspaceModalForAddDirectory(prefill)
}

func (a *App) handleWorktreesCommand(args []string) {
	if a.home == nil {
		return
	}
	if len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "new") {
		a.openWorktreesModalWithCreate(true)
		return
	}

	a.home.ClearCommandOverlay()
	a.home.SetStatus("usage: /worktrees new (alias: /wt new)")
}

func (a *App) scanWorkspaceTree(query string) {
	root := a.activeContextPath()
	if strings.TrimSpace(root) == "" {
		root = a.startupCWD
	}
	matches, err := discoverWorkspaceCandidates(root, query, 200)
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("workspace scan failed: %v", err))
		return
	}
	a.workspaceCandidates = matches

	lines := make([]string, 0, 8)
	header := fmt.Sprintf("workspace tree matches: %d", len(matches))
	if query != "" {
		header = fmt.Sprintf(`workspace tree matches for "%s": %d`, query, len(matches))
	}
	lines = append(lines, header)
	for i := 0; i < len(matches) && i < 7; i++ {
		lines = append(lines, fmt.Sprintf("#%d %s", i+1, displayPath(matches[i].Path)))
	}
	a.home.SetCommandOverlay(lines)
	a.home.SetStatus("use /workspace save #<n> to create one")
}

func (a *App) showWorkspaceSelector() {
	a.home.SetWorkspaceModalIntent("select", "")
	if _, err := a.openWorkspaceModal(); err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace selector failed: %v", err))
	}
}

func (a *App) showWorkspaceManager() {
	a.home.SetWorkspaceModalIntent("", "")
	if _, err := a.openWorkspaceModal(); err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace manager failed: %v", err))
	}
}

func (a *App) openWorkspaceModalForSave(target string, allowPathEdit bool) {
	a.home.SetWorkspaceModalIntent("", "")
	if _, err := a.openWorkspaceModal(); err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace manager failed: %v", err))
		return
	}
	a.home.OpenWorkspaceModalSaveEditor(target, allowPathEdit)
	a.home.SetStatus("workspace setup")
}

func (a *App) openWorkspaceModalForAddDirectory(prefill string) {
	a.home.SetWorkspaceModalIntent("add_dir", strings.TrimSpace(prefill))
	if _, err := a.openWorkspaceModal(); err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace manager failed: %v", err))
		return
	}
	a.home.SetStatus("workspace link-directory flow")
}

func (a *App) showAgentsManager() {
	a.openAgentsModal()
}

func (a *App) handleAgentsCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") {
		a.showAgentsManager()
		return
	}
	a.home.ClearCommandOverlay()
	a.home.SetStatus("usage: /agents")
}

func (a *App) handleAuthCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") {
		a.openAuthModal()
		return
	}
	if strings.EqualFold(args[0], "status") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		statuses, err := a.api.ListProviders(ctx)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("auth status failed: %v", err))
			return
		}
		lines := make([]string, 0, len(statuses)+2)
		lines = append(lines, fmt.Sprintf("providers: %d", len(statuses)))
		runnableCount := 0
		for _, provider := range statuses {
			health := "auth needed"
			switch {
			case provider.Runnable:
				health = "runnable"
				runnableCount++
			case provider.Ready:
				health = "not runnable"
			}
			line := fmt.Sprintf("- %s [%s]", strings.TrimSpace(provider.ID), health)
			reason := strings.TrimSpace(provider.Reason)
			if provider.Ready && !provider.Runnable {
				reason = strings.TrimSpace(provider.RunReason)
			}
			if reason != "" && !provider.Runnable {
				line += " " + reason
			}
			lines = append(lines, line)
		}
		a.home.SetCommandOverlay(lines)
		if runnableCount > 0 {
			a.home.SetStatus(fmt.Sprintf("runnable providers: %d", runnableCount))
		} else {
			a.home.SetStatus("Auth is missing, run /auth")
		}
		return
	}

	if !strings.EqualFold(args[0], "key") {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /auth <status|key>")
		return
	}
	if len(args) < 3 {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /auth key <provider> <api_key>")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(args[1]))
	if provider == "" {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /auth key <provider> <api_key>")
		return
	}
	key := strings.TrimSpace(strings.Join(args[2:], " "))
	if key == "" {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /auth key <provider> <api_key>")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := a.api.UpsertAuthCredential(ctx, client.AuthCredentialUpsertRequest{
		Provider: provider,
		Type:     "api",
		APIKey:   key,
		Active:   true,
	})
	if err == nil && a.chat != nil {
		a.chat.AppendUserAuthCommandMessage(provider)
	}
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("auth update failed: %v", err))
		return
	}
	a.home.ClearCommandOverlay()
	a.home.SetStatus(fmt.Sprintf("auth updated: %s (%s)", provider, emptyFallback(status.AuthType, "api")))
	a.notifyAuthAutoDefaults(status.AutoDefaults)
	a.queueReload(false)
}

func (a *App) handleVaultCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) == 0 {
		a.showVaultGuidance()
		switch {
		case !a.vault.Enabled:
			a.home.ShowVaultSetupWarning()
		case a.vault.Enabled && !a.vault.Unlocked:
			a.home.ShowVaultUnlockModal(false, "Vault is enabled and locked. Enter your password to unlock saved provider credentials. After unlocking, use /vault export or /vault import <file>.")
		default:
			a.home.ShowVaultStatusModal()
		}
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "export":
		a.handleVaultExportCommand(args[1:])
	case "import":
		a.handleVaultImportCommand(args[1:])
	default:
		a.home.SetStatus("usage: /vault [export [path]|import <path>]")
	}
}

func (a *App) showVaultGuidance() {
	lines := []string{
		"/vault export         export encrypted credentials to Downloads/Swarm",
		"/vault import <file>  import encrypted credentials from a bundle",
	}
	switch {
	case !a.vault.Enabled:
		lines = append(lines, "Vault is off. Enable it first or import a bundle to enable it with the import password.")
	case a.vault.Enabled && !a.vault.Unlocked:
		lines = append(lines, "Vault is locked. Unlock it before export. Import can unlock or enable using your passwords.")
	default:
		lines = append(lines, "Vault is unlocked. Export and import are available now.")
	}
	a.home.SetCommandOverlay(lines)
}

func (a *App) handleVaultExportCommand(args []string) {
	if a.api == nil {
		a.home.SetStatus("Vault API is unavailable.")
		return
	}
	if a.vault.Enabled && !a.vault.Unlocked {
		a.home.SetStatus("Vault is locked. Unlock it with /vault first, then run /vault export.")
		return
	}
	outputPath, err := a.resolveVaultExportPath(args)
	if err != nil {
		a.home.SetStatus(err.Error())
		return
	}
	bundlePassword, err := a.readSecretWithPrompt("Export password: ")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault export cancelled: %v", err))
		return
	}
	if strings.TrimSpace(bundlePassword) == "" {
		a.home.SetStatus("vault export requires a password")
		return
	}
	confirmPassword, err := a.readSecretWithPrompt("Confirm export password: ")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault export cancelled: %v", err))
		return
	}
	if bundlePassword != confirmPassword {
		a.home.SetStatus("vault export passwords do not match")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bundle, exported, err := a.api.ExportVaultCredentials(ctx, bundlePassword, "")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault export failed: %v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		a.home.SetStatus(fmt.Sprintf("vault export failed: %v", err))
		return
	}
	if err := os.WriteFile(outputPath, bundle, 0o600); err != nil {
		a.home.SetStatus(fmt.Sprintf("vault export failed: %v", err))
		return
	}
	lines := []string{
		fmt.Sprintf("Exported %d credential(s).", exported),
		fmt.Sprintf("Saved to %s", displayPath(outputPath)),
		"Move the file if needed, then delete it when the import is complete.",
	}
	a.home.SetCommandOverlay(lines)
	a.home.SetStatus(fmt.Sprintf("Vault export complete: %s", filepath.Base(outputPath)))
}

func (a *App) handleVaultImportCommand(args []string) {
	if a.api == nil {
		a.home.SetStatus("Vault API is unavailable.")
		return
	}
	if len(args) != 1 {
		a.home.SetStatus("usage: /vault import <path>")
		return
	}
	bundlePath := strings.TrimSpace(args[0])
	if bundlePath == "" {
		a.home.SetStatus("usage: /vault import <path>")
		return
	}
	bundlePath = filepath.Clean(bundlePath)
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault import failed: %v", err))
		return
	}
	bundlePassword, err := a.readSecretWithPrompt("Import password: ")
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault import cancelled: %v", err))
		return
	}
	if strings.TrimSpace(bundlePassword) == "" {
		a.home.SetStatus("vault import requires a password")
		return
	}
	vaultPassword := ""
	if a.vault.Enabled {
		vaultPassword, err = a.readSecretWithPrompt("Local vault password (Enter to reuse import password): ")
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("vault import cancelled: %v", err))
			return
		}
	}
	if strings.TrimSpace(vaultPassword) == "" {
		vaultPassword = bundlePassword
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := a.api.ImportVaultCredentials(ctx, bundlePassword, vaultPassword, bundle)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("vault import failed: %v", err))
		return
	}
	a.vault = result.Vault
	a.home.SetVaultEnabledState(result.Vault.Enabled, result.Vault.Unlocked)
	lines := []string{
		fmt.Sprintf("Imported %d credential(s).", result.Imported),
		fmt.Sprintf("Bundle: %s", displayPath(bundlePath)),
	}
	if result.Vault.Unlocked {
		lines = append(lines, "Vault unlocked. Credentials are now in place.")
	} else {
		lines = append(lines, "Import completed, but the vault is still locked. Unlock it with /vault.")
	}
	lines = append(lines, "You can delete the import file when you are done.")
	a.home.SetCommandOverlay(lines)
	if result.Vault.Enabled && result.Vault.Unlocked {
		a.home.SetVaultModalStatus(fmt.Sprintf("Imported %d credential(s). Vault unlocked. You can delete the import file now.", result.Imported))
		a.home.ShowVaultStatusModal()
	}
	a.home.SetStatus(fmt.Sprintf("Vault import complete: %d credential(s)", result.Imported))
	a.queueReload(false)
}

func (a *App) resolveVaultExportPath(args []string) (string, error) {
	if len(args) > 1 {
		return "", errors.New("usage: /vault export [path]")
	}
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return filepath.Clean(strings.TrimSpace(args[0])), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("could not determine home directory for Downloads export")
	}
	baseDir := filepath.Join(home, "Downloads", vaultExportDirName)
	name := fmt.Sprintf("swarm-credentials-%s%s", time.Now().Format("20060102-150405"), vaultExportFileExt)
	return filepath.Join(baseDir, name), nil
}

func (a *App) readSecretWithPrompt(label string) (string, error) {
	if a.screen != nil {
		if err := a.screen.Suspend(); err != nil {
			return "", err
		}
		defer func() {
			_ = a.screen.Resume()
			a.screen.EnablePaste()
			a.setMouseCapture(a.config.Input.MouseEnabled)
			a.home.SetPasteActive(a.pasteActive)
			a.screen.Clear()
		}()
	}
	fmt.Fprint(os.Stderr, label)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		secret, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(secret)), nil
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (a *App) handleVaultModalAction(action ui.VaultModalAction) {
	if a.api == nil {
		a.home.SetVaultModalLoading(false)
		a.home.SetVaultModalError("Vault API is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		status client.VaultStatus
		err    error
	)
	switch action.Kind {
	case ui.VaultModalActionEnable:
		status, err = a.api.EnableVault(ctx, action.Password)
	case ui.VaultModalActionUnlock:
		status, err = a.api.UnlockVault(ctx, action.Password)
	case ui.VaultModalActionLock:
		status, err = a.api.LockVault(ctx)
	case ui.VaultModalActionDisable:
		status, err = a.api.DisableVault(ctx, action.Password)
	default:
		a.home.SetVaultModalLoading(false)
		a.home.SetVaultModalError("Unknown vault action.")
		return
	}
	if err != nil {
		a.home.SetVaultModalLoading(false)
		a.home.SetVaultModalError(err.Error())
		return
	}
	a.vault = status
	a.home.SetVaultEnabledState(status.Enabled, status.Unlocked)
	a.home.SetVaultModalLoading(false)

	switch action.Kind {
	case ui.VaultModalActionEnable:
		a.home.SetVaultModalStatus("Vault enabled. Swarm will keep it unlocked until the app exits.")
		a.home.ShowVaultStatusModal()
	case ui.VaultModalActionUnlock:
		a.home.DismissVaultModal()
	case ui.VaultModalActionLock:
		a.applyHomeModel(a.lockedHomeModel())
		a.home.ShowVaultUnlockModal(true, "Vault locked. Enter your password to continue.")
	case ui.VaultModalActionDisable:
		a.home.DismissVaultModal()
		a.home.SetStatus("Vault disabled. Saved provider credentials now use local plaintext storage again.")
	}

	a.queueReload(false)
}

func (a *App) syncVaultUI() {
	if a.home == nil {
		return
	}
	if a.vault.Enabled && !a.vault.Unlocked {
		a.applyHomeModel(a.lockedHomeModel())
		if !a.home.VaultUnlockModalActive() {
			a.home.ShowVaultUnlockModal(true, "Vault is enabled. Enter your password to unlock Swarm.")
		}
		return
	}
	if a.home != nil && a.home.Status() == "" && a.vault.Enabled && a.vault.Unlocked {
		a.home.SetStatus("Vault unlocked. Saved provider credentials stay available until the app exits.")
	}
}

func (a *App) lockedHomeModel() model.HomeModel {
	next := model.EmptyHome()
	if a.api != nil {
		next.ServerURL = a.api.BaseURL()
	}
	contextPath := normalizePath(a.activeContextPath())
	if contextPath == "" {
		contextPath = normalizePath(a.startupCWD)
	}
	next.CWD = emptyFallback(contextPath, ".")
	next.HintLine = "Vault is locked. Unlock it to continue."
	next.TipLine = "/vault"
	return next
}

func (a *App) queueReload(silent bool) {
	if a.api == nil {
		return
	}
	if !a.reloading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		next, err := a.refreshHomeV3Model(ctx)
		result := homeReloadResult{
			model:  next,
			err:    err,
			silent: silent,
		}
		select {
		case a.reloadCh <- result:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptReloadReady))
		}
	}()
}

func (a *App) consumeReloadResult() {
	defer a.reloading.Store(false)
	select {
	case result := <-a.reloadCh:
		if result.hydrated != nil {
			a.applyTUISessionHydratedReload(*result.hydrated, result.sessionID)
			return
		}
		if result.sessionSnapshot != nil {
			a.homeModel.RecentSessions = modelSessionSummariesFromV3SyncSnapshot(*result.sessionSnapshot)
			if a.home != nil {
				a.home.SetModel(a.homeModel)
			}
			switch result.sessionOpenRoute {
			case "chat":
				a.openLoadedChatSessionsPalette(result.sessionQuery)
			case "home", "v3chat":
				a.openLoadedHomeSessionsModal(result.sessionQuery)
			}
			return
		}
		if result.err != nil {
			if !result.silent {
				if result.sessionOpenRoute != "" {
					a.home.SetStatus(fmt.Sprintf("sessions load failed: %v", result.err))
				} else {
					a.home.SetStatus(fmt.Sprintf("reload failed: %v", result.err))
				}
			}
			return
		}
		a.syncActiveContextFromHomeModel(result.model)
		a.applyHomeModel(result.model)
		a.syncVaultUI()
		a.announceStartupUpdate(result.model)
	default:
	}
}

func (a *App) consumeGitStatusRefreshResults() bool {
	if a == nil {
		return false
	}
	changed := false
	for {
		select {
		case result := <-a.gitWatcherReady:
			if result.generation != a.gitWatchGeneration.Load() || !pathsEqual(result.path, a.gitWatcherStartingPath) {
				discardRepoGitWatcher(result.watcher)
				continue
			}
			a.gitWatcherStartingPath = ""
			if result.err != nil || result.watcher == nil {
				continue
			}
			a.gitWatcher = result.watcher
			a.runGitRealtimeWatcher(result.watcher, result.generation, result.path)
		case result := <-a.gitStatusCh:
			if result.generation != a.gitWatchGeneration.Load() {
				continue
			}
			if !a.applyGitStatusRefresh(result) {
				continue
			}
			changed = true
		default:
			return changed
		}
	}
}

func activeAgentRuntime(state client.AgentState) (string, string, bool, bool) {
	active := strings.TrimSpace(state.ActivePrimary)
	if active == "" {
		active = "swarm"
	}
	for _, profile := range state.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.Name), active) {
			continue
		}
		executionSetting, exitPlanMode := agentProfileRuntime(profile)
		return active, executionSetting, exitPlanMode, true
	}
	return active, "", strings.EqualFold(active, "swarm"), false
}

func (a *App) syncChatAgentRuntime() {
	if a == nil || a.chat == nil {
		return
	}
	agent, executionSetting, exitPlanModeEnabled, runtimeKnown := a.currentChatAgentRuntime()
	a.chat.SetAgentRuntime(
		agent,
		executionSetting,
		exitPlanModeEnabled,
		runtimeKnown,
	)
	meta := a.chat.Meta()
	meta.Version = strings.TrimSpace(a.homeModel.Version)
	meta.UpdateVersionHint = homeUpdateVersionHint(a.homeModel.UpdateStatus)
	a.chat.SetMeta(meta)
}

func (a *App) applyLoadedAppConfig(cfg AppConfig) {
	if a == nil {
		return
	}
	a.config = cfg
	if a.keybinds == nil {
		a.keybinds = ui.NewDefaultKeyBindings()
	} else {
		a.keybinds = a.keybinds.Clone()
	}
	a.keybinds.ResetAll()
	a.keybinds.ApplyOverrides(a.config.Input.Keybinds)
	if a.home != nil {
		a.home.SetKeyBindings(a.keybinds)
		a.home.SetSwarmName(a.config.Swarm.Name)
		a.home.SetSessionMode(a.config.Chat.DefaultNewSessionMode)
		a.home.SetCommandSuggestions(buildHomeCommandSuggestions(a.config.Startup.DevMode))
	}
	if a.chat != nil {
		a.chat.SetKeyBindings(a.keybinds)
		a.chat.SetHeaderVisible(a.config.Chat.ShowHeader)
		a.chat.SetThinkingTagsVisible(a.config.Chat.ThinkingTags)
		a.chat.SetSwarmName(a.config.Swarm.Name)
	}
	if a.v3Chat != nil {
		a.v3Chat.SetHeaderVisible(a.config.Chat.ShowHeader)
		a.v3Chat.SetThinkingTagsVisible(a.config.Chat.ThinkingTags)
	}
	a.setMouseCapture(a.config.Input.MouseEnabled)
	a.mouseHintShown = false
	a.syncConfiguredCustomThemes()
	a.applyEffectiveTheme()
}

func (a *App) currentHomeModel() model.HomeModel {
	if a == nil {
		return model.HomeModel{}
	}
	next := a.homeModel
	if a.home == nil {
		return next
	}
	next.ModelProvider, next.ModelName, next.ThinkingLevel, next.ServiceTier, next.ContextMode = a.home.ModelState()
	return next
}

func (a *App) applyHomeModel(next model.HomeModel) {
	a.homeModel = next
	a.home.SetModel(next)
	route := a.selectedChatRouteForWorkspace(a.activeWorkspacePath())
	a.home.SetSessionIntent(buildHomeSessionIntent(a.home, route))
	a.home.SetSwarmNotificationCount(a.swarmNotificationCount)
	if next.UpdateStatus != nil {
		a.updateStatus = *next.UpdateStatus
	} else {
		a.updateStatus = client.UpdateStatus{}
	}
	a.syncChatAgentRuntime()
	a.refreshGitRealtimeWatcher()
	a.applyEffectiveTheme()
}

func (a *App) backgroundSessionMatchesOpenModal(item ui.ChatSessionPaletteItem) bool {
	if a == nil {
		return false
	}
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return false
	}
	for _, record := range a.homeModel.BackgroundSessions {
		if strings.TrimSpace(record.ChildSessionID) == id {
			return true
		}
	}
	return false
}

func (a *App) setSwarmNotificationCount(count int) {
	if a == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	a.swarmNotificationCount = count
	if a.home != nil {
		a.home.SetSwarmNotificationCount(count)
	}
	if a.chat != nil {
		a.chat.SetSwarmNotificationCount(count)
	}
}

func (a *App) queueNotificationCount() {
	if a == nil || a.api == nil || a.notificationCountCh == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		count, err := a.loadSwarmNotificationCount(ctx)
		select {
		case a.notificationCountCh <- notificationCountResult{count: count, err: err}:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptNotificationReady))
		}
	}()
}

func (a *App) consumeNotificationCountResult() {
	if a == nil || a.notificationCountCh == nil {
		return
	}
	select {
	case result := <-a.notificationCountCh:
		if result.err != nil {
			if a.home != nil {
				a.home.SetStatus(fmt.Sprintf("notification load failed: %v", result.err))
			}
			return
		}
		a.setSwarmNotificationCount(result.count)
	default:
	}
}

func (a *App) loadSwarmNotificationCount(ctx context.Context) (int, error) {
	if a == nil || a.api == nil {
		return 0, errors.New("api client unavailable")
	}
	summary, err := a.api.GetNotificationSummary(ctx, "")
	if err != nil {
		return 0, err
	}
	if summary.UnreadCount < 0 {
		return 0, nil
	}
	return summary.UnreadCount, nil
}

func homeUpdateVersionHint(status *client.UpdateStatus) string {
	if status == nil || !status.UpdateAvailable {
		return ""
	}
	return strings.TrimSpace(status.LatestVersion)
}

func (a *App) announceStartupUpdate(next model.HomeModel) {
	if a == nil || a.startupUpdateAnnounced {
		return
	}
	status := next.UpdateStatus
	if status == nil || !status.UpdateAvailable {
		return
	}
	a.startupUpdateAnnounced = true
	latest := strings.TrimSpace(status.LatestVersion)
	current := strings.TrimSpace(next.Version)
	if latest == "" {
		latest = "new release"
	}
	if current == "" {
		current = buildinfo.DisplayVersion()
	}
	message := fmt.Sprintf("update available: %s → %s", current, latest)
	a.showToast(ui.ToastInfo, message)
}

func (a *App) activeContextPath() string {
	if path := strings.TrimSpace(a.activePath); path != "" {
		return normalizePath(path)
	}
	if path := strings.TrimSpace(a.homeModel.CWD); path != "" {
		return normalizePath(path)
	}
	if path := strings.TrimSpace(a.startupCWD); path != "" {
		return normalizePath(path)
	}
	return ""
}

func (a *App) contextDisplayNameForPath(path, fallbackWorkspaceName string) string {
	target := normalizePath(strings.TrimSpace(path))
	fallbackWorkspaceName = strings.TrimSpace(fallbackWorkspaceName)
	if target != "" {
		workspacePath := normalizePath(a.activeWorkspacePath())
		if workspacePath != "" && pathsEqual(target, workspacePath) {
			if a.home != nil {
				if name := strings.TrimSpace(a.home.ActiveWorkspaceName()); name != "" {
					return name
				}
			}
			if fallbackWorkspaceName != "" {
				return fallbackWorkspaceName
			}
		}
		for _, directory := range a.homeModel.Directories {
			if !pathsEqual(directory.ResolvedPath, target) {
				continue
			}
			if name := strings.TrimSpace(directory.Name); name != "" {
				return name
			}
			break
		}
		if name := directoryNameForPath(target); name != "" {
			return name
		}
	}
	if fallbackWorkspaceName != "" {
		return fallbackWorkspaceName
	}
	return "directory"
}

func (a *App) syncActiveContextFromHomeModel(next model.HomeModel) {
	if a == nil {
		return
	}
	a.workspacePath = ""
	for _, ws := range next.Workspaces {
		if ws.Active {
			a.workspacePath = normalizePath(strings.TrimSpace(ws.Path))
			break
		}
	}
	if a.workspacePath != "" {
		a.activePath = a.workspacePath
	} else if next.CWD != "" {
		a.activePath = normalizePath(strings.TrimSpace(next.CWD))
	}
	a.refreshGitRealtimeWatcher()
}

func workspacePathMatchDepth(root, target string) int {
	root = normalizePath(strings.TrimSpace(root))
	target = normalizePath(strings.TrimSpace(target))
	if root == "" || target == "" {
		return -1
	}
	if pathsEqual(root, target) {
		return len(root)
	}
	if root == string(filepath.Separator) {
		return len(root)
	}
	if strings.HasPrefix(target, root) && len(target) > len(root) && target[len(root)] == filepath.Separator {
		return len(root)
	}
	return -1
}

func workspaceModelMatchDepth(entry model.Workspace, target string) int {
	best := workspacePathMatchDepth(entry.Path, target)
	for _, root := range entry.Directories {
		if depth := workspacePathMatchDepth(root, target); depth > best {
			best = depth
		}
	}
	return best
}

func resolveWorkspaceSelectionPath(target string, workspaces []model.Workspace, preferredPath string) string {
	target = normalizePath(strings.TrimSpace(target))
	preferredPath = normalizePath(strings.TrimSpace(preferredPath))
	bestPath := ""
	bestDepth := -1
	preferredDepth := -1
	for _, ws := range workspaces {
		depth := workspaceModelMatchDepth(ws, target)
		if depth < 0 {
			continue
		}
		path := normalizePath(strings.TrimSpace(ws.Path))
		if path == "" {
			continue
		}
		if preferredPath != "" && pathsEqual(path, preferredPath) {
			preferredDepth = depth
		}
		if depth > bestDepth {
			bestDepth = depth
			bestPath = path
		}
	}
	if preferredPath != "" && preferredDepth == bestDepth && preferredDepth >= 0 {
		return preferredPath
	}
	return bestPath
}

func (a *App) syncKnownWorkspaceSelectionForPath(path string) {
	if a == nil {
		return
	}
	target := normalizePath(strings.TrimSpace(path))
	if target != "" {
		if !pathsEqual(a.activePath, target) {
			a.markTUIRealtimeScopeStale("workspace scope changed")
		}
		a.activePath = target
		a.homeModel.CWD = target
	}
	selectedPath := normalizePath(strings.TrimSpace(a.workspacePath))
	if selectedPath == "" {
		for _, ws := range a.homeModel.Workspaces {
			if ws.Active {
				selectedPath = normalizePath(strings.TrimSpace(ws.Path))
				break
			}
		}
	}
	resolvedSelection := resolveWorkspaceSelectionPath(target, a.homeModel.Workspaces, selectedPath)
	workspaceRoots := make(map[string]struct{}, len(a.homeModel.Workspaces))
	for i := range a.homeModel.Workspaces {
		root := normalizePath(strings.TrimSpace(a.homeModel.Workspaces[i].Path))
		if root != "" {
			workspaceRoots[root] = struct{}{}
		}
		active := resolvedSelection != "" && root != "" && pathsEqual(root, resolvedSelection)
		a.homeModel.Workspaces[i].Active = active
	}
	for i := range a.homeModel.Directories {
		root := normalizePath(strings.TrimSpace(a.homeModel.Directories[i].ResolvedPath))
		_, isWorkspaceRoot := workspaceRoots[root]
		a.homeModel.Directories[i].IsWorkspace = isWorkspaceRoot
	}
	a.workspacePath = resolvedSelection
	selectedRouteID := a.resolveSelectedChatRouteIDForWorkspace(target, a.homeModel.ChatRoutes)
	a.selectedChatRouteID = selectedRouteID
	a.homeModel.SelectedChatRouteID = selectedRouteID
	if a.home != nil {
		a.home.SetModel(a.homeModel)
	}
	a.refreshGitRealtimeWatcher()
	a.applyEffectiveTheme()
}

func buildChatRoutesForWorkspaces(workspaces []model.Workspace, workspacePath string) []model.ChatRoute {
	return buildChatRoutesForWorkspacesWithHostTarget(workspaces, workspacePath, nil)
}

func buildChatRoutesForHomeModel(home model.HomeModel, workspacePath string) []model.ChatRoute {
	return buildChatRoutesForWorkspacesWithHostTarget(home.Workspaces, workspacePath, home.CurrentSwarmTarget)
}

func buildChatRoutesForWorkspacesWithHostTarget(workspaces []model.Workspace, workspacePath string, target *model.SwarmTarget) []model.ChatRoute {
	workspacePath = normalizePath(strings.TrimSpace(workspacePath))
	if workspacePath == "" && len(workspaces) > 0 {
		workspacePath = normalizePath(workspaces[0].Path)
	}
	var active model.Workspace
	for _, workspace := range workspaces {
		if pathsEqual(workspace.Path, workspacePath) {
			active = workspace
			break
		}
	}
	hostSwarmID := ""
	hostLabel := "host"
	if isPrimaryHostSwarmTarget(target) {
		hostSwarmID = strings.TrimSpace(target.SwarmID)
		if targetName := strings.TrimSpace(target.Name); targetName != "" {
			hostLabel = targetName
		}
	}
	hostBindingID := strings.TrimSpace(active.LocalWorkspaceBindingID)
	if hostBindingID == "" {
		for _, route := range active.TopologyRoutes {
			if strings.TrimSpace(route.WorkspaceBindingID) == "" {
				continue
			}
			if hostSwarmID != "" && strings.EqualFold(strings.TrimSpace(route.RuntimeSwarmID), hostSwarmID) {
				hostBindingID = strings.TrimSpace(route.WorkspaceBindingID)
				break
			}
		}
	}
	hostRouteID := primaryHostRouteID(hostSwarmID, hostBindingID)
	routes := []model.ChatRoute{{
		ID:                   hostRouteID,
		Label:                hostLabel,
		SwarmID:              hostSwarmID,
		WorkspaceBindingID:   hostBindingID,
		HostWorkspacePath:    workspacePath,
		RuntimeWorkspacePath: workspacePath,
		TargetKind:           "host",
		TargetRelationship:   "self",
	}}
	seen := map[string]struct{}{"host": {}}
	if hostRouteID != "" {
		seen[hostRouteID] = struct{}{}
	}
	for _, route := range active.TopologyRoutes {
		swarmID := strings.TrimSpace(route.RuntimeSwarmID)
		bindingID := strings.TrimSpace(route.WorkspaceBindingID)
		runtimePath := strings.TrimSpace(route.RuntimeWorkspacePath)
		routeID := strings.TrimSpace(route.RouteID)
		if routeID == "" && bindingID != "" {
			routeID = "swarm:" + swarmID + ":binding:" + bindingID
		}
		if swarmID == "" || bindingID == "" || routeID == "" {
			continue
		}
		if _, exists := seen[routeID]; exists {
			continue
		}
		seen[routeID] = struct{}{}
		label := strings.TrimSpace(route.RuntimeSwarmName)
		if label == "" {
			label = swarmID
		}
		hostWorkspacePath := normalizePath(strings.TrimSpace(route.HostWorkspacePath))
		if hostWorkspacePath == "" {
			hostWorkspacePath = workspacePath
		}
		routes = append(routes, model.ChatRoute{
			ID:                   routeID,
			Label:                label,
			SwarmID:              swarmID,
			WorkspaceBindingID:   strings.TrimSpace(route.WorkspaceBindingID),
			HostWorkspacePath:    hostWorkspacePath,
			RuntimeWorkspacePath: runtimePath,
			TargetKind:           strings.TrimSpace(route.RuntimeKind),
			TargetRelationship:   strings.TrimSpace(route.RuntimeRelationship),
		})
	}
	return routes
}

func normalizeSelectedRouteID(routeID string, routes []model.ChatRoute) string {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" || strings.EqualFold(routeID, "host") {
		if len(routes) > 0 {
			return emptyFallback(strings.TrimSpace(routes[0].ID), "host")
		}
		return "host"
	}
	for _, route := range routes {
		if strings.TrimSpace(route.ID) == routeID {
			return routeID
		}
	}
	if len(routes) > 0 {
		return emptyFallback(strings.TrimSpace(routes[0].ID), "host")
	}
	return "host"
}

func (a *App) defaultChatRouteIDForWorkspace(workspacePath string) string {
	if a == nil {
		return ""
	}
	workspacePath = normalizePath(strings.TrimSpace(workspacePath))
	if workspacePath == "" || len(a.config.Chat.DefaultWorkspaceRoutes) == 0 {
		return ""
	}
	if routeID := strings.TrimSpace(a.config.Chat.DefaultWorkspaceRoutes[workspacePath]); routeID != "" {
		return routeID
	}
	for path, routeID := range a.config.Chat.DefaultWorkspaceRoutes {
		if pathsEqual(path, workspacePath) {
			return strings.TrimSpace(routeID)
		}
	}
	return ""
}

func defaultChatRouteIDFromConfig(cfg AppConfig, workspacePath string) string {
	workspacePath = normalizePath(strings.TrimSpace(workspacePath))
	if workspacePath == "" || len(cfg.Chat.DefaultWorkspaceRoutes) == 0 {
		return ""
	}
	if routeID := strings.TrimSpace(cfg.Chat.DefaultWorkspaceRoutes[workspacePath]); routeID != "" {
		return routeID
	}
	for path, routeID := range cfg.Chat.DefaultWorkspaceRoutes {
		if pathsEqual(path, workspacePath) {
			return strings.TrimSpace(routeID)
		}
	}
	return ""
}

func (a *App) resolveSelectedChatRouteIDForWorkspace(workspacePath string, routes []model.ChatRoute) string {
	selected := ""
	if a != nil {
		selected = strings.TrimSpace(a.selectedChatRouteID)
		if selected == "" {
			selected = a.defaultChatRouteIDForWorkspace(workspacePath)
		}
	}
	return normalizeSelectedRouteID(selected, routes)
}

func (a *App) selectedChatRouteForWorkspace(workspacePath string) model.ChatRoute {
	if a == nil {
		return model.ChatRoute{}
	}
	routes := a.homeModel.ChatRoutes
	if len(routes) == 0 {
		routes = buildChatRoutesForHomeModel(a.homeModel, workspacePath)
	}
	selected := a.resolveSelectedChatRouteIDForWorkspace(workspacePath, routes)
	for _, route := range routes {
		if strings.TrimSpace(route.ID) == selected {
			return route
		}
	}
	if len(routes) == 0 {
		return model.ChatRoute{}
	}
	return routes[0]
}

func (a *App) selectedChatRouteLabelForWorkspace(workspacePath string) string {
	route := a.selectedChatRouteForWorkspace(workspacePath)
	return a.displayChatRouteLabel(route)
}

func (a *App) displayChatRouteLabel(route model.ChatRoute) string {
	if a != nil {
		if targetName := primaryHostRouteTargetName(route, a.homeModel.CurrentSwarmTarget); targetName != "" {
			return targetName
		}
	}
	return emptyFallback(strings.TrimSpace(route.Label), "host")
}

func isPrimaryHostChatRoute(route model.ChatRoute) bool {
	return strings.EqualFold(strings.TrimSpace(route.TargetRelationship), "self") && strings.EqualFold(strings.TrimSpace(route.TargetKind), "host")
}

func primaryHostRouteID(swarmID, bindingID string) string {
	swarmID = strings.TrimSpace(swarmID)
	bindingID = strings.TrimSpace(bindingID)
	if swarmID != "" && bindingID != "" {
		return "swarm:" + swarmID + ":binding:" + bindingID
	}
	if bindingID != "" {
		return "host:binding:" + bindingID
	}
	return "host"
}

func isPrimaryHostSwarmTarget(target *model.SwarmTarget) bool {
	if target == nil || strings.TrimSpace(target.SwarmID) == "" {
		return false
	}
	relationship := strings.ToLower(strings.TrimSpace(target.Relationship))
	kind := strings.ToLower(strings.TrimSpace(target.Kind))
	return relationship == "self" && (kind == "" || kind == "host" || kind == "local" || kind == "self")
}

func primaryHostRouteTargetName(route model.ChatRoute, target *model.SwarmTarget) string {
	if !isPrimaryHostChatRoute(route) || !isPrimaryHostSwarmTarget(target) {
		return ""
	}
	routeSwarmID := strings.TrimSpace(route.SwarmID)
	targetSwarmID := strings.TrimSpace(target.SwarmID)
	if routeSwarmID != "" && targetSwarmID != "" && routeSwarmID != targetSwarmID {
		return ""
	}
	return strings.TrimSpace(target.Name)
}

func sameSwarmID(left, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(left) == strings.TrimSpace(right)
}

func (a *App) sessionRouteLabelForWorkspace(workspacePath string, metadata map[string]any) string {
	if route, ok := a.sessionRouteFromMetadata(workspacePath, metadata); ok {
		return a.displayChatRouteLabel(route)
	}
	return a.selectedChatRouteLabelForWorkspace(workspacePath)
}

func (a *App) sessionRouteFromMetadata(workspacePath string, metadata map[string]any) (model.ChatRoute, bool) {
	if len(metadata) == 0 {
		return model.ChatRoute{}, false
	}
	hostWorkspacePath := firstNonEmpty(consumeStringMetadata(metadata, "swarm_v3_source_workspace_path"), workspacePath)
	runtimeWorkspacePath := consumeStringMetadata(metadata, "swarm_v3_runtime_workspace_path")
	workspaceBindingID := consumeStringMetadata(metadata, "swarm_v3_workspace_binding_id")
	childSwarmID := consumeStringMetadata(metadata, "swarm_v3_runtime_swarm_id")
	routeID := ""
	if childSwarmID != "" && workspaceBindingID != "" {
		routeID = "swarm:" + childSwarmID + ":binding:" + workspaceBindingID
	}
	if a == nil {
		routes := buildChatRoutesForWorkspaces(nil, firstNonEmpty(hostWorkspacePath, workspacePath))
		for _, route := range routes {
			if routeID != "" && strings.TrimSpace(route.ID) == routeID {
				return route, true
			}
		}
		return model.ChatRoute{}, false
	}
	routes := a.homeModel.ChatRoutes
	if len(routes) == 0 {
		routes = buildChatRoutesForHomeModel(a.homeModel, firstNonEmpty(hostWorkspacePath, workspacePath))
	}
	for _, route := range routes {
		if workspaceBindingID != "" && strings.TrimSpace(route.WorkspaceBindingID) == workspaceBindingID {
			return route, true
		}
		if workspaceBindingID == "" && childSwarmID != "" && strings.TrimSpace(route.SwarmID) == childSwarmID {
			if runtimeWorkspacePath == "" || pathsEqual(route.RuntimeWorkspacePath, runtimeWorkspacePath) {
				return route, true
			}
		}
	}
	if routeID != "" {
		return model.ChatRoute{ID: routeID, Label: childSwarmID, SwarmID: childSwarmID, WorkspaceBindingID: workspaceBindingID, HostWorkspacePath: hostWorkspacePath, RuntimeWorkspacePath: runtimeWorkspacePath}, true
	}
	return model.ChatRoute{}, false
}

func modelSwarmTargetFromClient(target *client.WorkspaceOverviewSwarmTarget) *model.SwarmTarget {
	if target == nil {
		return nil
	}
	if strings.TrimSpace(target.SwarmID) == "" {
		return nil
	}
	return &model.SwarmTarget{
		SwarmID:      strings.TrimSpace(target.SwarmID),
		Name:         strings.TrimSpace(target.Name),
		Role:         strings.TrimSpace(target.Role),
		Relationship: strings.TrimSpace(target.Relationship),
		Kind:         strings.TrimSpace(target.Kind),
		Online:       target.Online,
		Selectable:   target.Selectable,
		Current:      target.Current,
	}
}

func modelTopologyRoutesFromClient(routes []client.WorkspaceTopologyRoute) []model.WorkspaceTopologyRoute {
	if len(routes) == 0 {
		return nil
	}
	out := make([]model.WorkspaceTopologyRoute, 0, len(routes))
	for _, route := range routes {
		runtimeSwarmID := strings.TrimSpace(route.RuntimeSwarmID)
		runtimeWorkspacePath := strings.TrimSpace(route.RuntimeWorkspacePath)
		if runtimeSwarmID == "" || runtimeWorkspacePath == "" {
			continue
		}
		out = append(out, model.WorkspaceTopologyRoute{
			RouteID:              strings.TrimSpace(route.RouteID),
			WorkspaceBindingID:   strings.TrimSpace(route.WorkspaceBindingID),
			RuntimeSwarmID:       runtimeSwarmID,
			RuntimeSwarmName:     strings.TrimSpace(route.RuntimeSwarmName),
			RuntimeKind:          strings.TrimSpace(route.RuntimeKind),
			RuntimeRelationship:  strings.TrimSpace(route.RuntimeRelationship),
			HostWorkspacePath:    strings.TrimSpace(route.HostWorkspacePath),
			HostWorkspaceName:    strings.TrimSpace(route.HostWorkspaceName),
			RuntimeWorkspacePath: runtimeWorkspacePath,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) activeWorkspacePath() string {
	if path := strings.TrimSpace(a.workspacePath); path != "" {
		return normalizePath(path)
	}
	return ""
}

func (a *App) refreshGitRealtimeWatcher() {
	if a == nil {
		return
	}
	target := normalizePath(a.activeContextPath())
	if target == "" {
		a.stopGitRealtimeWatcher()
		return
	}
	if (a.gitWatcher != nil && pathsEqual(a.gitWatcher.path, target)) || pathsEqual(a.gitWatcherStartingPath, target) {
		return
	}
	a.stopGitRealtimeWatcher()
	a.startGitRealtimeWatcher(target)
}

// startGitRealtimeWatcher keeps repository discovery and recursive watch
// registration off the TUI event loop. Large worktrees can contain tens of
// thousands of directories, so constructing the watcher synchronously would
// freeze the first usable frame and every workspace switch.
func (a *App) startGitRealtimeWatcher(path string) {
	if a == nil || a.gitWatcherReady == nil {
		return
	}
	target := normalizePath(path)
	if target == "" {
		return
	}
	generation := a.gitWatchGeneration.Add(1)
	a.gitWatcherStartingPath = target
	go func() {
		watcher, err := newRepoGitWatcher(target)
		if generation != a.gitWatchGeneration.Load() {
			discardRepoGitWatcher(watcher)
			return
		}
		result := gitWatcherStartResult{generation: generation, path: target, watcher: watcher, err: err}
		select {
		case a.gitWatcherReady <- result:
		default:
			discardRepoGitWatcher(watcher)
			return
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptGitStatusReady))
		}
	}()
}

func (a *App) runGitRealtimeWatcher(watcher *repoGitWatcher, generation uint64, target string) {
	if a == nil || watcher == nil {
		return
	}
	go watcher.run(func() {
		status, ok := gitStatusForPath(target)
		result := gitStatusRefreshResult{generation: generation, path: target, status: status, ok: ok}
		select {
		case a.gitStatusCh <- result:
		default:
			select {
			case <-a.gitStatusCh:
			default:
			}
			select {
			case a.gitStatusCh <- result:
			default:
			}
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptGitStatusReady))
		}
	})
}

func discardRepoGitWatcher(watcher *repoGitWatcher) {
	if watcher != nil && watcher.watcher != nil {
		_ = watcher.watcher.Close()
	}
}

func (a *App) stopGitRealtimeWatcher() {
	if a == nil {
		return
	}
	a.gitWatchGeneration.Add(1)
	a.gitWatcherStartingPath = ""
	if a.gitWatcher == nil {
		return
	}
	a.gitWatcher.stopWatching()
	a.gitWatcher = nil
}

func (a *App) applyGitStatusRefresh(result gitStatusRefreshResult) bool {
	if a == nil || !result.ok {
		return false
	}
	target := normalizePath(result.path)
	if target == "" {
		return false
	}
	changed := false
	for i := range a.homeModel.Directories {
		if !pathsEqual(a.homeModel.Directories[i].ResolvedPath, target) {
			continue
		}
		before := a.homeModel.Directories[i]
		applyGitStatusToDirectory(&a.homeModel.Directories[i], result.status)
		if a.homeModel.Directories[i] != before {
			changed = true
		}
		break
	}
	if !changed {
		return false
	}
	if a.home != nil {
		a.home.SetModel(a.homeModel)
	}
	if a.chat != nil && pathsEqual(a.activePath, target) {
		a.chat.SetSessionBranch(result.status.Branch)
	}
	return true
}

func (a *App) syncActiveWorkspaceSelection(resolution client.WorkspaceResolution) {
	resolvedPath := normalizePath(strings.TrimSpace(resolution.ResolvedPath))
	workspacePath := normalizePath(strings.TrimSpace(resolution.WorkspacePath))
	if workspacePath == "" {
		a.syncKnownWorkspaceSelectionForPath(resolvedPath)
		return
	}
	previousWorkspacePath := a.workspacePath
	a.workspacePath = workspacePath
	if resolvedPath != "" {
		if !pathsEqual(a.activePath, resolvedPath) || !pathsEqual(previousWorkspacePath, workspacePath) {
			a.markTUIRealtimeScopeStale("workspace scope changed")
		}
		a.activePath = resolvedPath
		a.homeModel.CWD = resolvedPath
	}
	name := strings.TrimSpace(resolution.WorkspaceName)
	themeID := strings.TrimSpace(resolution.ThemeID)
	matched := false
	for i := range a.homeModel.Workspaces {
		active := pathsEqual(normalizePath(a.homeModel.Workspaces[i].Path), workspacePath)
		a.homeModel.Workspaces[i].Active = active
		if active {
			matched = true
			if name != "" {
				a.homeModel.Workspaces[i].Name = name
			}
			if themeID != "" || strings.TrimSpace(a.homeModel.Workspaces[i].ThemeID) != "" {
				a.homeModel.Workspaces[i].ThemeID = themeID
			}
		}
	}
	if !matched {
		fallbackName := name
		if fallbackName == "" {
			fallbackName = directoryNameForPath(workspacePath)
		}
		a.homeModel.Workspaces = append(a.homeModel.Workspaces, model.Workspace{
			Name:    fallbackName,
			Path:    workspacePath,
			ThemeID: themeID,
			Icon:    workspaceIcon(len(a.homeModel.Workspaces)),
			Active:  true,
		})
	}
	for i := range a.homeModel.Directories {
		if pathsEqual(normalizePath(a.homeModel.Directories[i].ResolvedPath), workspacePath) {
			a.homeModel.Directories[i].IsWorkspace = true
		}
	}
	a.home.SetModel(a.homeModel)
	a.refreshGitRealtimeWatcher()
	a.applyEffectiveTheme()
}

func (a *App) resolveWorkspaceTarget(value string) string {
	target := strings.TrimSpace(value)
	if target == "" || target == "." {
		return a.activeContextPath()
	}
	if strings.HasPrefix(target, "#") {
		idx, err := strconv.Atoi(strings.TrimPrefix(target, "#"))
		if err == nil && idx >= 1 && idx <= len(a.workspaceCandidates) {
			return a.workspaceCandidates[idx-1].Path
		}
	}
	return target
}

func (a *App) findWorkspacePath(value string) (string, bool) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", false
	}
	if strings.HasPrefix(target, "#") {
		idx, err := strconv.Atoi(strings.TrimPrefix(target, "#"))
		if err == nil && idx >= 1 {
			if idx <= len(a.homeModel.Workspaces) {
				return a.homeModel.Workspaces[idx-1].Path, true
			}
			if idx <= len(a.workspaceCandidates) {
				return a.workspaceCandidates[idx-1].Path, true
			}
		}
	}
	lower := strings.ToLower(target)
	for _, ws := range a.homeModel.Workspaces {
		if strings.EqualFold(ws.Name, target) || strings.EqualFold(ws.Path, target) {
			return ws.Path, true
		}
	}
	for _, ws := range a.homeModel.Workspaces {
		if strings.Contains(strings.ToLower(ws.Name), lower) || strings.Contains(strings.ToLower(ws.Path), lower) {
			return ws.Path, true
		}
	}
	return "", false
}

func workspaceIcon(index int) string {
	icons := []string{"*", "+", "-", "#", "=", "~", "%", "@", "&", "^", ":"}
	return icons[index%len(icons)]
}

func activeWorkspaceIndex(workspaces []model.Workspace) int {
	for i, ws := range workspaces {
		if ws.Active {
			return i
		}
	}
	return -1
}

func (a *App) homeInteractionActive() bool {
	if a.home == nil {
		return false
	}
	return a.home.OnboardingVisible() ||
		a.home.AlertsModalVisible() ||
		a.home.AuthModalVisible() ||
		a.home.AuthDefaultsInfoVisible() ||
		a.home.SessionsModalVisible() ||
		a.home.VaultModalVisible() ||
		a.home.WorkspaceModalVisible() ||
		a.home.WorktreesModalVisible() ||
		a.home.CodexUsageModalVisible() ||
		a.home.ProfilesModalVisible() ||
		a.home.ModelsModalVisible() ||
		a.home.AgentsModalVisible() ||
		a.home.VoiceModalVisible() ||
		a.home.ThemeModalVisible() ||
		a.home.KeybindsModalVisible()
}

func (a *App) workspaceSwitchRunActive() bool {
	if a.route == "v3chat" && a.v3Chat != nil {
		if runtime := a.v3Chat.Runtime(); runtime != nil && runtime.Store() != nil {
			_, active := v3chat.SelectActiveRun(runtime.Store().Snapshot())
			return active
		}
	}
	return a.route == "chat" && a.chat != nil && a.chat.RunInProgress()
}

func (a *App) workspaceSwitchHotkeyBlocked() bool {
	if a.workspaceSwitchRunActive() {
		message := "workspace switching is unavailable while a run is active"
		if a.home != nil {
			a.home.SetStatus(message)
		}
		if a.v3Chat != nil {
			a.v3Chat.SetStatus(message)
		}
		if a.chat != nil {
			a.chat.SetStatus(message)
		}
		return true
	}
	if a.route == "home" || a.route == "v3chat" {
		return a.homeInteractionActive()
	}
	if a.route == "chat" {
		message := "To change workspace, do /new or go to the home screen (Ctrl+B)"
		if a.chat != nil {
			a.chat.SetStatus("")
		}
		a.showToast(ui.ToastInfo, message)
	}
	return true
}

func (a *App) activateWorkspaceSlot(slot int) {
	if slot < 1 || slot > len(a.homeModel.Workspaces) {
		if a.home != nil {
			a.home.SetStatus(fmt.Sprintf("workspace slot %d is empty", slot))
		}
		return
	}
	a.activateWorkspaceAtIndex(slot - 1)
}

func (a *App) activateWorkspaceAtIndex(index int) {
	workspaces := a.homeModel.Workspaces
	if index < 0 || index >= len(workspaces) {
		a.home.SetStatus("workspace selection is out of range")
		return
	}
	target := strings.TrimSpace(workspaces[index].Path)
	if target == "" {
		a.home.SetStatus("selected workspace path is empty")
		return
	}
	previousWorkspacePath := a.activeWorkspacePath()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resolution, err := a.api.SelectWorkspace(ctx, target)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace switch failed: %v", err))
		return
	}
	a.syncActiveWorkspaceSelection(resolution)
	if err := a.openV3ChatDraftAfterWorkspaceChange(previousWorkspacePath); err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace switched, but new chat draft failed: %v", err))
		return
	}
	a.home.SetStatus(fmt.Sprintf("workspace active: %s", resolution.WorkspaceName))
	a.queueReload(false)
}

func (a *App) userFacingSessionPath(workspacePath string, worktreeEnabled bool, worktreeRootPath string) string {
	if worktreeEnabled {
		if root := strings.TrimSpace(worktreeRootPath); root != "" {
			return displayPath(root)
		}
	}
	return displayPath(workspacePath)
}

func displayPath(path string) string {
	trimmed := collapseWorktreeDisplayPath(path)
	if trimmed == "" {
		return "."
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(trimmed, home) {
		return "~" + strings.TrimPrefix(trimmed, home)
	}
	return trimmed
}

func collapseWorktreeDisplayPath(path string) string {
	return strings.TrimSpace(path)
}

func directoryNameForPath(path string) string {
	trimmed := collapseWorktreeDisplayPath(path)
	if trimmed == "" {
		return "directory"
	}
	name := filepath.Base(trimmed)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "directory"
	}
	return name
}

func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return trimmed
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

func pathsEqual(a, b string) bool {
	return normalizePath(a) == normalizePath(b)
}

type gitRepoStatus struct {
	Branch         string
	DirtyCount     int
	StagedCount    int
	ModifiedCount  int
	UntrackedCount int
	ConflictCount  int
	AheadCount     int
	BehindCount    int
	Upstream       string
	HasGit         bool
}

func applyGitStatusToDirectory(item *model.DirectoryItem, status gitRepoStatus) {
	if item == nil {
		return
	}
	item.Branch = status.Branch
	item.DirtyCount = status.DirtyCount
	item.StagedCount = status.StagedCount
	item.ModifiedCount = status.ModifiedCount
	item.UntrackedCount = status.UntrackedCount
	item.ConflictCount = status.ConflictCount
	item.AheadCount = status.AheadCount
	item.BehindCount = status.BehindCount
	item.Upstream = status.Upstream
	item.HasGit = status.HasGit
}

func newDirectoryItemWithGitStatus(path string, isWorkspace bool, status gitRepoStatus) model.DirectoryItem {
	item := model.DirectoryItem{
		Name:         directoryNameForPath(path),
		Path:         displayPath(path),
		ResolvedPath: path,
		AgentsToken:  "none",
		IsWorkspace:  isWorkspace,
	}
	applyGitStatusToDirectory(&item, status)
	return item
}

func branchForPath(path string) string {
	status, ok := gitStatusForPath(path)
	if !ok || !status.HasGit {
		return "-"
	}
	branch := strings.TrimSpace(status.Branch)
	if branch == "" || branch == "HEAD" {
		return "-"
	}
	return branch
}

func gitStatusForPath(path string) (gitRepoStatus, bool) {
	target := strings.TrimSpace(path)
	if target == "" {
		return gitRepoStatus{Branch: "-"}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", target, "status", "--porcelain=v2", "--branch")
	raw, err := cmd.Output()
	if err != nil {
		return gitRepoStatus{Branch: "-"}, false
	}
	return parseGitStatusPorcelainV2(string(raw)), true
}

func parseGitStatusPorcelainV2(raw string) gitRepoStatus {
	status := gitRepoStatus{Branch: "-", HasGit: true}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			switch branch {
			case "", "HEAD":
				status.Branch = "-"
			case "(detached)":
				status.Branch = "detached"
			default:
				status.Branch = branch
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			status.Upstream = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream "))
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				status.AheadCount = parseGitCount(fields[2])
				status.BehindCount = parseGitCount(fields[3])
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			status.DirtyCount++
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				accumulateGitXY(&status, fields[1])
			}
		case strings.HasPrefix(line, "u "):
			status.DirtyCount++
			status.ConflictCount++
		case strings.HasPrefix(line, "? "):
			status.DirtyCount++
			status.UntrackedCount++
		}
	}
	if strings.TrimSpace(status.Branch) == "" {
		status.Branch = "-"
	}
	return status
}

func accumulateGitXY(status *gitRepoStatus, xy string) {
	if status == nil || len(xy) < 2 {
		return
	}
	if xy[0] != '.' {
		status.StagedCount++
	}
	if xy[1] != '.' {
		status.ModifiedCount++
	}
}

func parseGitCount(value string) int {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "+")
	trimmed = strings.TrimPrefix(trimmed, "-")
	count, err := strconv.Atoi(trimmed)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func contextAgentsToken(rules []client.RuleSource) string {
	hasAgents := false
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule.Name), "AGENTS.md") {
			hasAgents = true
			break
		}
	}

	switch {
	case hasAgents:
		return "agents"
	case len(rules) > 0:
		return fmt.Sprintf("%d rules", len(rules))
	default:
		return "none"
	}
}

func formatAgo(tsMs int64) string {
	if tsMs <= 0 {
		return "-"
	}
	then := time.UnixMilli(tsMs)
	delta := time.Since(then)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return fmt.Sprintf("%ds", int(delta.Seconds()))
	case delta < time.Hour:
		return fmt.Sprintf("%dm", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh", int(delta.Hours()))
	default:
		return fmt.Sprintf("%dd", int(delta.Hours()/24))
	}
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func chatPlanLabel(plan client.SessionPlan) string {
	title := strings.TrimSpace(plan.Title)
	id := strings.TrimSpace(plan.ID)
	if title != "" {
		return title
	}
	if id != "" {
		return id
	}
	return "none"
}

func clampText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func homeQuickActions(next model.HomeModel) []string {
	if !next.AuthConfigured {
		return []string{"Auth: missing", "Run /auth"}
	}
	profile := "Agent model default"
	if strings.EqualFold(strings.TrimSpace(next.ActiveModelProfile.Source), "saved") {
		profile = emptyFallback(strings.TrimSpace(next.ActiveModelProfile.Name), "Saved profile")
	}
	setup := strings.Join([]string{profile, homeModelDisplayLabel(next), emptyFallback(next.ThinkingLevel, "unset"), emptyFallback(next.ServiceTier, "default")}, " · ")
	return []string{"Profile: " + setup}
}

func applyHomeModelResolved(next model.HomeModel, resolved client.ModelResolved) model.HomeModel {
	next.ModelProvider = strings.TrimSpace(resolved.Preference.Provider)
	next.ModelName = strings.TrimSpace(resolved.Preference.Model)
	next.ThinkingLevel = strings.TrimSpace(resolved.Preference.Thinking)
	next.ServiceTier = strings.TrimSpace(resolved.Preference.ServiceTier)
	next.ContextMode = strings.TrimSpace(resolved.Preference.ContextMode)
	next.ContextWindow = resolved.ContextWindow
	next.QuickActions = homeQuickActions(next)
	return next
}

func refreshHomeModelProfiles(next model.HomeModel, state client.ModelProfileState) model.HomeModel {
	next.ModelProfiles = append([]client.ModelProfile(nil), state.Profiles...)
	next.DefaultModelProfileID = strings.TrimSpace(state.DefaultProfileID)
	return next
}

func applyHomeModelProfiles(next model.HomeModel, state client.ModelProfileState) model.HomeModel {
	next = refreshHomeModelProfiles(next, state)
	next.ActiveModelProfile = model.ActiveModelProfile{Source: "agent-default"}
	if next.DefaultModelProfileID == "" {
		return next
	}
	for _, profile := range next.ModelProfiles {
		if strings.TrimSpace(profile.ProfileID) == next.DefaultModelProfileID {
			return applyHomeModelProfile(next, profile)
		}
	}
	return next
}

func applyHomeModelProfile(next model.HomeModel, profile client.ModelProfile) model.HomeModel {
	next.ActiveModelProfile = model.ActiveModelProfile{
		Source:    "saved",
		ProfileID: strings.TrimSpace(profile.ProfileID),
		Name:      strings.TrimSpace(profile.Name),
		ModelMode: strings.TrimSpace(profile.ModelMode),
	}
	applySelection := func(selection *client.ModelProfileSelection) (string, string, string, string, string) {
		if selection == nil {
			return "", "", "", "", ""
		}
		return strings.TrimSpace(selection.Provider), strings.TrimSpace(selection.Model), strings.TrimSpace(selection.Thinking), strings.TrimSpace(selection.ServiceTier), strings.TrimSpace(selection.ContextMode)
	}
	if strings.EqualFold(strings.TrimSpace(profile.ModelMode), "split") {
		next.PlanModelProvider, next.PlanModelName, next.PlanThinkingLevel, next.PlanServiceTier, next.PlanContextMode = applySelection(profile.Plan)
		next.AutoModelProvider, next.AutoModelName, next.AutoThinkingLevel, next.AutoServiceTier, next.AutoContextMode = applySelection(profile.Auto)
	} else if profile.Single != nil {
		next.ModelProvider = strings.TrimSpace(profile.Single.Provider)
		next.ModelName = strings.TrimSpace(profile.Single.Model)
		next.ThinkingLevel = strings.TrimSpace(profile.Single.Thinking)
		next.ServiceTier = strings.TrimSpace(profile.Single.ServiceTier)
		next.ContextMode = strings.TrimSpace(profile.Single.ContextMode)
		next.PlanModelProvider, next.AutoModelProvider = next.ModelProvider, next.ModelProvider
		next.PlanModelName, next.AutoModelName = next.ModelName, next.ModelName
		next.PlanThinkingLevel, next.AutoThinkingLevel = next.ThinkingLevel, next.ThinkingLevel
		next.PlanServiceTier, next.AutoServiceTier = next.ServiceTier, next.ServiceTier
		next.PlanContextMode, next.AutoContextMode = next.ContextMode, next.ContextMode
	}
	return next
}

func homeModelDisplayLabel(next model.HomeModel) string {
	return model.DisplayModelLabel(next.ModelProvider, next.ModelName, next.ServiceTier, next.ContextMode)
}

func mapWorktreesModalData(settings client.WorktreeSettings, resolvedBranch string) ui.WorktreesModalData {
	branchName := normalizeWorktreeBranchPrefix(strings.TrimSpace(settings.BranchName))
	if branchName == "" {
		branchName = "agent"
	}
	return ui.WorktreesModalData{
		WorkspacePath:    strings.TrimSpace(settings.WorkspacePath),
		Enabled:          settings.Enabled,
		UseCurrentBranch: settings.UseCurrentBranch,
		BaseBranch:       strings.TrimSpace(settings.BaseBranch),
		BranchName:       branchName,
		ResolvedBranch:   strings.TrimSpace(resolvedBranch),
		UpdatedAt:        settings.UpdatedAt,
	}
}

func worktreeBranchLabel(useCurrentBranch bool, baseBranch string) string {
	if useCurrentBranch {
		return "current branch"
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "unset"
	}
	return strings.TrimSpace(baseBranch)
}

func worktreeResolvedBranchLabel(useCurrentBranch bool, baseBranch, resolvedBranch string) string {
	if useCurrentBranch {
		if branch := strings.TrimSpace(resolvedBranch); branch != "" {
			return branch
		}
		return "unknown"
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "unset"
	}
	return strings.TrimSpace(baseBranch)
}

func normalizeWorktreeSettingsBranchInput(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "auto", "current", "current-branch", "current_branch":
		return "", true
	default:
		return trimmed, false
	}
}

func normalizeWorktreeBranchPrefix(value string) string {
	const (
		defaultWorktreeBranchPrefix = "agent"
		defaultWorktreeBranchName   = "agent/<id>"
		worktreeBranchIDPlaceholder = "<id>"
	)

	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return defaultWorktreeBranchPrefix
	}
	if strings.EqualFold(trimmed, defaultWorktreeBranchName) {
		return defaultWorktreeBranchPrefix
	}
	if strings.HasSuffix(trimmed, "/"+worktreeBranchIDPlaceholder) {
		trimmed = strings.TrimSuffix(trimmed, "/"+worktreeBranchIDPlaceholder)
		trimmed = strings.Trim(trimmed, "/")
	}
	if trimmed == "" {
		return defaultWorktreeBranchPrefix
	}
	return trimmed
}

func (a *App) currentWorktreeResolvedBranch() string {
	if a == nil {
		return ""
	}
	branch := strings.TrimSpace(branchForPath(a.activePath))
	if branch == "-" {
		return ""
	}
	return branch
}

func (a *App) worktreesStatusSummary(settings client.WorktreeSettings) string {
	resolved := worktreeResolvedBranchLabel(settings.UseCurrentBranch, strings.TrimSpace(settings.BaseBranch), a.currentWorktreeResolvedBranch())
	scope := displayPath(strings.TrimSpace(settings.WorkspacePath))
	createdBranch := normalizeWorktreeBranchPrefix(strings.TrimSpace(settings.BranchName))
	if createdBranch == "" {
		createdBranch = "agent"
	}
	return fmt.Sprintf("worktrees %s • workspace=%s • created=%s/<id> • source=%s • resolved=%s", onOffLabel(settings.Enabled), scope, createdBranch, worktreeBranchLabel(settings.UseCurrentBranch, strings.TrimSpace(settings.BaseBranch)), resolved)
}

func onOffLabel(enabled bool) string {
	if enabled {
		return "ON"
	}
	return "OFF"
}

func copyTextToClipboard(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("clipboard payload is empty")
	}

	candidates := []struct {
		bin  string
		args []string
	}{
		{bin: "wl-copy"},
		{bin: "xclip", args: []string{"-selection", "clipboard"}},
		{bin: "xsel", args: []string{"--clipboard", "--input"}},
		{bin: "pbcopy"},
		{bin: "clip"},
	}

	tried := make([]string, 0, len(candidates))
	runFailures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		tried = append(tried, candidate.bin)
		path, err := exec.LookPath(candidate.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, candidate.args...)
		cmd.Stdin = strings.NewReader(text)
		if runErr := cmd.Run(); runErr != nil {
			runFailures = append(runFailures, fmt.Sprintf("%s: %v", candidate.bin, runErr))
			continue
		}
		return nil
	}

	if len(runFailures) > 0 {
		if err := copyTextToClipboardOSC52(text); err == nil {
			return nil
		}
		return fmt.Errorf("clipboard command failed (%s)", strings.Join(runFailures, "; "))
	}
	return fmt.Errorf(
		"no clipboard utility available (tried: %s); install one of: wl-copy, xclip, xsel, pbcopy, clip",
		strings.Join(tried, ", "),
	)
}

func copyTextToClipboardOSC52(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("osc52 payload is empty")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()

	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if encoded == "" {
		return errors.New("osc52 payload encode failed")
	}
	if _, err := fmt.Fprintf(tty, "\x1b]52;c;%s\a", encoded); err != nil {
		return err
	}
	return nil
}

func (a *App) handlePermissionsCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.api == nil {
		a.home.SetStatus("permissions API unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "off":
			a.setPermissionsBypass(true)
			return
		case "on":
			a.setPermissionsBypass(false)
			return
		}
	}
	if len(args) == 0 || strings.EqualFold(args[0], "show") {
		policy, err := a.api.GetPermissionPolicy(ctx)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/permissions show failed: %v", err))
			return
		}
		a.openPermissionsPolicyModal(policy)
		a.home.SetStatus("permission policy loaded")
		return
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "allow", "ask", "deny":
		if len(args) < 3 {
			a.home.SetStatus("usage: /permissions [allow|ask|deny] [tool|bash-prefix|phrase] <value>")
			return
		}
		kindArg := strings.ToLower(strings.TrimSpace(args[1]))
		value := strings.TrimSpace(strings.Join(args[2:], " "))
		if value == "" {
			a.home.SetStatus("permission value is required")
			return
		}
		kind := "tool"
		rule := client.PermissionRule{Decision: action}
		switch kindArg {
		case "tool":
			rule.Kind = kind
			rule.Tool = value
		case "bash-prefix", "bash_prefix":
			rule.Kind = "bash_prefix"
			rule.Tool = "bash"
			rule.Pattern = value
		case "phrase":
			rule.Kind = "phrase"
			rule.Pattern = value
		default:
			a.home.SetStatus("kind must be tool, bash-prefix, or phrase")
			return
		}
		saved, err := a.api.AddPermissionRule(ctx, rule)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/permissions %s failed: %v", action, err))
			return
		}
		a.home.SetStatus(fmt.Sprintf("permission rule saved: %s", saved.ID))
	case "remove":
		if len(args) < 2 {
			a.home.SetStatus("usage: /permissions remove <rule-id>")
			return
		}
		removed, err := a.api.RemovePermissionRule(ctx, args[1])
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/permissions remove failed: %v", err))
			return
		}
		if !removed {
			a.home.SetStatus("permission rule not found")
			return
		}
		a.home.SetStatus("permission rule removed")
	case "reset":
		if _, err := a.api.ResetPermissionPolicy(ctx); err != nil {
			a.home.SetStatus(fmt.Sprintf("/permissions reset failed: %v", err))
			return
		}
		a.home.SetStatus("permission policy reset")
	case "explain":
		if len(args) < 2 {
			a.home.SetStatus("usage: /permissions explain <tool> [arguments]")
			return
		}
		toolName := strings.TrimSpace(args[1])
		arguments := ""
		if len(args) > 2 {
			arguments = strings.TrimSpace(strings.Join(args[2:], " "))
		}
		mode := "auto"
		if a.chat != nil {
			mode = a.chat.SessionMode()
		}
		explain, err := a.api.ExplainPermission(ctx, mode, toolName, arguments)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/permissions explain failed: %v", err))
			return
		}
		lines := []string{
			fmt.Sprintf("decision: %s", explain.Decision),
			fmt.Sprintf("source: %s", explain.Source),
			fmt.Sprintf("reason: %s", explain.Reason),
		}
		if strings.TrimSpace(explain.RulePreview) != "" {
			lines = append(lines, "rule: "+explain.RulePreview)
		}
		a.home.SetCommandOverlay(lines)
		a.home.SetStatus("permission explain loaded")
	default:
		a.home.SetStatus("usage: /permissions [on|off|show|allow|ask|deny|remove|reset|explain]")
	}
}

func (a *App) chatAvailableModels(providerHint string) []ui.ModelsModalEntry {
	if a == nil || a.api == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	activeProvider, activeModel, _, _, activeContextMode, _ := a.currentModelPreferenceState()
	resolved := a.resolveProviderModelData(ctx, []string{providerHint, activeProvider}, 2000, 1200)
	return a.chatAvailableModelsFromResolved(resolved, activeProvider, activeModel, activeContextMode)
}

func (a *App) chatAvailableModelsFromResolved(resolved providerModelResolverResult, activeProvider, activeModel, activeContextMode string) []ui.ModelsModalEntry {
	entries := make([]ui.ModelsModalEntry, 0, 1024)
	for _, providerID := range resolved.ProviderIDs {
		status, ok := resolved.ProviderStatuses[providerID]
		if ok && !status.Ready {
			continue
		}
		for _, modelID := range resolved.ModelsByProvider[providerID] {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			key := modelEntryKey(providerID, modelID)
			entry := ui.ModelsModalEntry{Provider: providerID, Model: modelID}
			if record, ok := resolved.CatalogByKey[key]; ok {
				entry.ContextMode = record.ContextMode
				entry.Reasoning = record.Reasoning
				entry.ThinkingOptions = append([]string(nil), record.ThinkingOptions...)
				entry.DefaultThinking = strings.TrimSpace(record.DefaultThinking)
				entry.ServiceTiers = append([]string(nil), record.ServiceTiers...)
				entry.DefaultServiceTier = strings.TrimSpace(record.DefaultServiceTier)
			}
			if enabled, ok := resolved.ReasoningByKey[key]; ok {
				entry.Reasoning = enabled
			}
			if entry.ContextMode == "" {
				entry.ContextMode = contextModeForModelEntry(providerID, modelID, activeContextMode)
			}
			entries = append(entries, entry)
			if model.SupportsCodex1MMode(providerID, modelID) {
				entry1M := entry
				entry1M.ContextMode = model.CodexContextMode1M
				entries = append(entries, entry1M)
			}
		}
	}
	if activeProvider != "" && activeModel != "" {
		entries = append(entries, ui.ModelsModalEntry{
			Provider:    activeProvider,
			Model:       activeModel,
			ContextMode: contextModeForModelEntry(activeProvider, activeModel, activeContextMode),
			Reasoning:   resolved.ReasoningByKey[modelEntryKey(activeProvider, activeModel)],
		})
	}
	seen := make(map[string]struct{}, len(entries))
	filtered := make([]ui.ModelsModalEntry, 0, len(entries))
	for _, entry := range entries {
		providerID := normalizeModelProviderID(entry.Provider)
		modelID := strings.TrimSpace(entry.Model)
		if providerID == "" || modelID == "" {
			continue
		}
		entry.Provider = providerID
		key := providerID + "\x00" + modelID + "\x00" + strings.TrimSpace(entry.ContextMode)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, entry)
	}
	return filtered
}
