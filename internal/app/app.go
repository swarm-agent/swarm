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
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/updatehandoff"
)

const (
	interruptTick           = "tick"
	interruptChatAsync      = "chat-async"
	interruptReloadReady    = "reload-ready"
	interruptAuthReady      = "auth-ready"
	interruptVoiceReady     = "voice-ready"
	interruptStreamReady    = "stream-ready"
	interruptGitStatusReady = "git-status-ready"
	interruptQuit           = "quit"
	defaultDaemonURL        = "http://127.0.0.1:7781"
	reloadInterval          = 3 * time.Second
	streamRenderMinInterval = 66 * time.Millisecond
	vaultExportDirName      = "Swarm"
	vaultExportFileExt      = ".swarmvault"
)

var (
	modelPresetsByProvider = map[string][]string{
		"anthropic": {
			"claude-opus-4-7",
			"claude-opus-4-6",
			"claude-opus-4-5",
			"claude-sonnet-4-6",
			"claude-sonnet-4-5",
			"claude-haiku-4-5",
		},
		"codex": {
			"gpt-5.5",
			"gpt-5.4",
			"gpt-5.4-mini",
			"gpt-5.3-codex",
			"gpt-5.3-codex-spark",
			"gpt-5.2",
			"gpt-5.1-codex-max",
			"gpt-5.1-codex-mini",
		},
		// Copilot presets are intentionally hidden for now. The provider code stays
		// in-tree, but we cannot fairly test or recommend it while the required paid
		// Copilot plan is unavailable.
		"fireworks": {
			"accounts/fireworks/models/kimi-k2p6",
			"accounts/fireworks/models/minimax-m2p7",
			"accounts/fireworks/models/kimi-k2p5",
		},
		"google": {
			"gemini-3.1-pro-preview",
			"gemini-3-flash-preview",
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.0-flash",
		},
		"openrouter": {
			"openai/gpt-5.5",
			"google/gemini-3-flash-preview",
			"openai/gpt-5.2",
			"openai/gpt-5.2-mini",
		},
	}
	thinkingPresets = []string{"off", "low", "medium", "high", "xhigh"}
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
		{Command: "/agents", Hint: "Open agents manager modal", QuickTips: []string{"/agents reset", "/agents restore", "/agents use <name>", "/agents prompt <name> <text>", "/agents delete <name>"}},
		{Command: "/auth", Hint: "Auth status or key setup", QuickTips: []string{"/auth status", "/auth key <provider> <api_key>"}},
		{Command: "/codex", Hint: "Show Codex gpt-5.4/gpt-5.5 runtime settings (Fast on/off)", QuickTips: []string{"/codex status", "/codex fast", "/fast"}},
		{Command: "/commit", Hint: "Launch the background commit agent to review diffs and commit changes", QuickTips: []string{"/commit [instructions]"}},
		{Command: "/compact", Hint: "Compact current chat context via memory agent", QuickTips: []string{"/compact [threshold%] [notes]"}},
		{Command: "/copy", Hint: "Copy chat snapshot to clipboard"},
		{Command: "/fast", Hint: "Toggle Codex Fast for the current chat or home draft (gpt-5.4/gpt-5.5)", QuickTips: []string{"alias tip: /codex fast"}},
		{Command: "/header", Hint: "Toggle chat header visibility", QuickTips: []string{"/header toggle"}},
		{Command: "/help", Hint: "Show command help"},
		{Command: "/home", Hint: "Return to home without ending the chat session"},
		{Command: "/keybinds", Hint: "Open keybindings modal", QuickTips: []string{"/keybinds list", "/keybinds reset [all]"}},
		{Command: "/mcp", Hint: "MCP management is deferred until Swarm Sync integration", QuickTips: []string{"Exa search can use the built-in free Exa MCP server", "Use /auth key exa <api_key> for webfetch/deep fetch"}},
		{Command: "/mode", Hint: "Set the default mode for new chats", QuickTips: []string{"/mode auto", "/mode plan", "/mode status"}},
		{Command: "/models", Hint: "Open model manager modal (favorites + provider catalog)"},
		{Command: "/mouse", Hint: "Toggle mouse click capture", QuickTips: []string{"/mouse toggle", "/mouse status"}},
		{Command: "/new", Hint: "Create a new session (scaffold)"},
		{Command: "/output", Hint: "Open the full bash output viewer"},
		{Command: "/permissions", Hint: "Show global permission policy", QuickTips: []string{"/permissions show", "/permissions allow tool <name>", "/permissions allow bash-prefix <command>", "/permissions deny phrase <text>"}},
		{Command: "/plan", Hint: "Plan commands in chat", QuickTips: []string{"/plan exit", "/plan list", "/plan use <plan_id>", "/plan new [title]"}},
		{Command: "/quit", Hint: "Exit swarmtui"},
		{Command: "/reload", Hint: "Reload home state from swarmd"},
		{Command: "/rebuild", Hint: "Rebuild the active lane, then exit swarmtui"},
		// Temporarily hidden from the UI surface.
		// {Command: "/sandbox", Hint: "Open sandbox setup modal (global ON/OFF)", QuickTips: []string{"/sandbox on", "/sandbox off", "/sandbox status"}},
		{Command: "/sessions", Hint: "Open recent sessions modal"},
		{Command: "/swarm", Hint: "Show swarm dashboard, pairing state, and approvals", QuickTips: []string{"/swarm status", "/swarm pending", "/swarm approve <id>", "/swarm reject <id>", "/swarm role master", "/swarm set <name>"}},
		{Command: "/update", Hint: updateHint, QuickTips: updateQuickTips},
		{Command: "/themes", Hint: "Open theme modal with live preview", QuickTips: []string{"/themes list", "/themes set <id>", "/themes create <id> from <base>", "/themes edit <id> <slot> <#RRGGBB>", "/themes delete <id>"}},
		{Command: "/thinking", Hint: "Use /thinking on, /thinking off, or /thinking status", QuickTips: []string{"/thinking on", "/thinking off", "/thinking status"}},
		{Command: "/vault", Hint: "Vault status, export, or import guidance"},
		{Command: "/voice", Hint: "Open voice modal (profiles + devices + STT/TTS + test)", QuickTips: []string{"/voice open", "/voice devices", "/voice device <id>", "/voice profile list", "/voice test 4"}},
		{Command: "/workspace", Hint: "Open workspace manager", QuickTips: []string{"/workspaces", "/workspace save", "/workspace scan [query]"}},
		{Command: "/worktrees", Hint: "Open worktrees menu for the active workspace", QuickTips: []string{"/wt", "/worktrees on", "/worktrees off", "/worktrees status", "/worktrees branch <name|current>"}},
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Command) < strings.ToLower(items[j].Command)
	})
	return items
}

type homeReloadResult struct {
	model  model.HomeModel
	err    error
	silent bool
}

type gitStatusRefreshResult struct {
	generation uint64
	path       string
	status     gitRepoStatus
	ok         bool
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
	Provider  string
	Label     string
	Active    bool
	SessionID string
	AuthURL   string
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
	route  string

	api            *client.API
	startupCWD     string
	activePath     string
	workspacePath  string
	homeModel      model.HomeModel
	updateStatus   client.UpdateStatus
	config         AppConfig
	themePreviewID string
	settingsLabel  string
	keybinds       *ui.KeyBindings

	lastReloadAt time.Time
	reloadCh     chan homeReloadResult
	reloading    atomic.Bool
	authLoginCh  chan authLoginResult
	authLogging  atomic.Bool
	codexPending *codexCodeLoginState

	voiceCaptureSeq        int64
	voiceCapture           activeVoiceCapture
	voiceCaptureCh         chan voiceCaptureEvent
	pasteActive            bool
	quitModal              quitModalState
	permissionsBypassModal permissionsBypassModalState

	streamEvents            chan client.StreamEventEnvelope
	streamCancel            context.CancelFunc
	streamSeq               atomic.Uint64
	streamRenderPending     bool
	lastStreamRenderAt      time.Time
	streamRenderWakePending atomic.Bool

	gitStatusCh        chan gitStatusRefreshResult
	gitWatcher         *repoGitWatcher
	gitWatchGeneration atomic.Uint64

	swarmNotificationCount int

	pendingChatRender  chan struct{}
	pendingStreamReady chan struct{}

	workspaceCandidates []workspaceCandidate
	mouseHintShown      bool
	vault               client.VaultStatus

	quitRequested bool

	devUpdateRequested     bool
	releaseUpdateRequested bool

	pendingLocalContainerUpdate *localContainerUpdateConfirmation
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
		screen:              s,
		home:                ui.NewHomePage(initial),
		route:               "home",
		api:                 api,
		startupCWD:          cwd,
		activePath:          cwd,
		workspacePath:       "",
		homeModel:           initial,
		config:              cfg,
		settingsLabel:       settingsBackendLabel,
		keybinds:            ui.NewDefaultKeyBindings(),
		lastReloadAt:        time.Now(),
		reloadCh:            make(chan homeReloadResult, 1),
		authLoginCh:         make(chan authLoginResult, 1),
		voiceCaptureCh:      make(chan voiceCaptureEvent, 4),
		streamEvents:        make(chan client.StreamEventEnvelope, 256),
		gitStatusCh:         make(chan gitStatusRefreshResult, 8),
		pendingChatRender:   make(chan struct{}, 1),
		pendingStreamReady:  make(chan struct{}, 1),
		workspaceCandidates: make([]workspaceCandidate, 0, 128),
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

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if count, err := app.loadSwarmNotificationCount(ctx); err == nil {
		app.setSwarmNotificationCount(count)
	}
	next, loadErr := app.refreshHomeModel(ctx)
	if loadErr != nil {
		app.home.SetStatus(fmt.Sprintf("backend unavailable: %v", loadErr))
		app.home.SetModel(app.homeModel)
	} else {
		app.syncActiveContextFromHomeModel(next)
		app.applyHomeModel(next)
		app.syncVaultUI()
		if cfgErr != nil {
			app.home.SetStatus(fmt.Sprintf("settings warning: %v", cfgErr))
		}
		app.announceStartupUpdate(next)
	}
	if loadErr != nil && cfgErr != nil {
		app.home.SetStatus(fmt.Sprintf("backend unavailable: %v (settings warning: %v)", loadErr, cfgErr))
	}
	app.startSessionEventStream()
	app.refreshGitRealtimeWatcher()
	app.announceAppliedUpdate()
	return app, nil
}

func (a *App) Close() {
	if a.streamCancel != nil {
		a.streamCancel()
		a.streamCancel = nil
	}
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
	stop := make(chan struct{})
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	go func() {
		for {
			select {
			case <-tick.C:
				if a.screen != nil {
					a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTick))
				}
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)

	dirty := true
	for {
		if dirty {
			if a.route == "chat" && a.chat != nil {
				a.chat.Draw(a.screen)
				if a.home != nil {
					a.home.DrawChatOverlay(a.screen)
				}
			} else {
				a.home.Draw(a.screen)
			}
			if a.quitModalActive() {
				a.drawQuitModal()
			}
			if a.permissionsBypassModalActive() {
				a.drawPermissionsBypassModal()
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
				a.consumePendingChatRender()
				if a.handleChatAsync() {
					dirty = true
				}
			case interruptReloadReady:
				a.consumeReloadResult()
				dirty = true
			case interruptAuthReady:
				a.consumeAuthLoginResult()
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
			if a.permissionsBypassModalActive() {
				if a.handlePermissionsBypassModalMouse(e) {
					dirty = true
					continue
				}
			}
			if a.quitModalActive() {
				if a.handleQuitModalMouse(e) {
					dirty = true
					continue
				}
			}
			if a.quitRequested {
				continue
			}
			if a.route == "chat" && a.home != nil && a.home.ChatOverlayVisible() {
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
			if a.permissionsBypassModalActive() {
				if a.handlePermissionsBypassModalKey(e) {
					dirty = true
					continue
				}
			}
			if a.quitModalActive() {
				if a.handleQuitModalKey(e) {
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
				a.setPasteActive(false)
				dirty = true
			}
			if a.route == "chat" && a.home != nil {
				if a.home.HandleChatOverlayKey(e) {
					a.consumeHomeOverlayActions()
					dirty = true
					continue
				}
			}
			if handled := a.handleGlobalKey(e); handled {
				dirty = true
				continue
			}
			if a.route == "chat" && a.home != nil && a.home.ChatOverlayVisible() {
				dirty = true
				continue
			}
			if a.voiceInputLocked() {
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

func (a *App) consumePendingChatRender() {
	if a == nil {
		return
	}
	select {
	case <-a.pendingChatRender:
	default:
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

func (a *App) stopSessionEventStream() {
	if a == nil || a.streamCancel == nil {
		return
	}
	a.streamCancel()
	a.streamCancel = nil
}

func (a *App) runSessionEventStream(ctx context.Context) {
	if a == nil || a.api == nil {
		return
	}
	lastSeen := a.streamSeq.Load()
	channels := []string{"swarm:*", "session:*", "ui:*", "workspace:*"}
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
	case "swarm.enrollment.pending", "swarm.enrollment.approved", "swarm.enrollment.rejected", "notification.created", "notification.updated":
		return a.applySwarmStreamEvent(event)
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
	a.config = appConfigFromUISettings(settings)
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
		a.home.SetModel(a.homeModel)
	}
	if a.chat != nil {
		a.chat.SetKeyBindings(a.keybinds)
		a.chat.SetHeaderVisible(a.config.Chat.ShowHeader)
		a.chat.SetThinkingTagsVisible(a.config.Chat.ThinkingTags)
		a.chat.SetSwarmName(a.config.Swarm.Name)
	}
	a.setMouseCapture(a.config.Input.MouseEnabled)
	a.mouseHintShown = false
	a.syncConfiguredCustomThemes()
	a.applyEffectiveTheme()
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
	case "swarm.enrollment.pending":
		a.swarmNotificationCount++
		a.setSwarmNotificationCount(a.swarmNotificationCount)
		return true
	case "swarm.enrollment.approved", "swarm.enrollment.rejected":
		if a.swarmNotificationCount > 0 {
			a.swarmNotificationCount--
		}
		a.setSwarmNotificationCount(a.swarmNotificationCount)
		return true
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
	var raw client.SessionRunStreamEvent
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
	if keybinds.Match(ev, ui.KeybindGlobalOpenAgents) {
		if a.route == "chat" && a.chat != nil && a.chat.PermissionModalVisible() {
			return false
		}
		if a.route == "chat" && a.chat != nil {
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
	if keybinds.Match(ev, ui.KeybindGlobalOpenModels) {
		if a.route == "chat" && a.chat != nil {
			a.openModelsModal("")
			return false
		}
		if a.route == "chat" {
			a.route = "home"
			a.chat = nil
		}
		if a.route == "home" {
			a.home.SetPasteActive(a.pasteActive)
			a.openModelsModal("")
			return false
		}
	}
	if keybinds.Match(ev, ui.KeybindGlobalWorkspacePrev) {
		if a.workspaceCycleHotkeyBlocked() {
			return true
		}
		a.cycleWorkspaceBy(-1)
		return true
	}
	for slot := 1; slot <= ui.WorkspaceSlotCount; slot++ {
		id, ok := ui.WorkspaceSlotKeybindID(slot)
		if !ok {
			continue
		}
		if !keybinds.Match(ev, id) {
			continue
		}
		if a.workspaceCycleHotkeyBlocked() {
			return true
		}
		a.activateWorkspaceSlot(slot)
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalWorkspaceNext) {
		if a.workspaceCycleHotkeyBlocked() {
			return true
		}
		a.cycleWorkspaceBy(1)
		return true
	}
	if keybinds.Match(ev, ui.KeybindGlobalCycleThinking) {
		if a.route == "home" &&
			(a.home.AuthModalVisible() ||
				a.home.VaultModalVisible() ||
				a.home.WorkspaceModalVisible() ||
				a.home.SandboxModalVisible() ||
				a.home.WorktreesModalVisible() ||
				a.home.ModelsModalVisible() ||
				a.home.AgentsModalVisible() ||
				a.home.VoiceModalVisible() ||
				a.home.ThemeModalVisible() ||
				a.home.KeybindsModalVisible()) {
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
				a.home.SandboxModalVisible() ||
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
	}

	if keybinds.Match(ev, ui.KeybindGlobalQuit) {
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
	if a.home.SessionsModalVisible() ||
		a.home.AuthModalVisible() ||
		a.home.VaultModalVisible() ||
		a.home.WorkspaceModalVisible() ||
		a.home.SandboxModalVisible() ||
		a.home.WorktreesModalVisible() ||
		a.home.ModelsModalVisible() ||
		a.home.AgentsModalVisible() ||
		a.home.VoiceModalVisible() ||
		a.home.ThemeModalVisible() ||
		a.home.KeybindsModalVisible() {
		return false
	}
	if a.home.SessionsFocused() {
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
	case "new":
		a.handleNewCommand()
	case "plan":
		a.handlePlanCommand(args)
	case "compact":
		a.handleCompactCommand(args)
	case "commit":
		a.handleCommitCommand(args)
	case "fast":
		a.handleCodexCommand([]string{"fast"})
	case "codex":
		a.handleCodexCommand(args)
	case "workspace":
		a.handleWorkspaceCommand(args)
	case "workspaces":
		a.handleWorkspaceCommand(args)
	case "add-dir":
		a.handleAddDirectoryCommand(args)
	case "mcp":
		a.handleMCPCommand(args)
	// Temporarily hidden from the UI surface.
	// case "sandbox":
	// 	a.handleSandboxCommand(args)
	case "permissions":
		a.handlePermissionsCommand(args)
	case "output":
		a.handleOutputCommand(args)
	case "worktrees", "wt":
		a.handleWorktreesCommand(args)
	case "mode":
		a.handleModeCommand(args)
	case "models":
		a.handleModelsCommand(args)
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
		"/sessions   (open recent sessions modal)",
		"/new   (create and open a new session)",
		"/home   (return to home from chat)",
		"/plan exit [title]   (open plan-exit approval modal in chat)",
		"/plan list   (list saved plans for this session)",
		"/plan use <plan_id>   (set active plan)",
		"/plan new [title]   (create and activate a new plan draft)",
		"/compact [threshold%] [notes]   (compact now + optionally set auto-compact threshold)",
		"/commit [instructions]   (launch background commit agent to review diffs and commit)",
		"/fast   (toggle Codex Fast for current chat or home draft; gpt-5.4/gpt-5.5)",
		"/codex [status|fast]   (Codex gpt-5.4/gpt-5.5 runtime settings; Fast on-off)",
		"/workspace   (open workspace manager)",
		"/workspaces   (alias for /workspace)",
		"/workspace save [path|#n]   (open workspace setup)",
		"/add-dir [path]   (open workspace linked-directory flow)",
		"/workspace scan [query]",
		"/mcp   (deferred: MCP management needs Swarm Sync; Exa search can use the built-in free Exa MCP server)",
		// Temporarily hidden from the UI surface.
		// "/sandbox   (open sandbox setup modal)",
		// "/sandbox [on|off|status]",
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
		"/worktrees   (open worktrees menu)",
		"/wt   (alias for /worktrees)",
		"/worktrees [on|off|status|branch <name>]",
		"/agents   (open agents manager modal)",
		"/agents restore   (restore built-in agents without deleting custom ones)",
		"/agents reset   (delete custom agents/tools and restore built-ins)",
		"/agents use <primary-agent>",
		"/agents prompt <name> <prompt text>",
		"/agents delete <name>",
		"/mode [auto|plan|status]   (default mode for new chats)",
		"/models   (open model manager modal)",
		fmt.Sprintf("%s   (open agents manager modal)", keybinds.Label(ui.KeybindGlobalOpenAgents)),
		fmt.Sprintf("%s   (open model manager modal)", keybinds.Label(ui.KeybindGlobalOpenModels)),
		fmt.Sprintf("%s   (cycle workspace previous)", keybinds.Label(ui.KeybindGlobalWorkspacePrev)),
		fmt.Sprintf("%s   (cycle workspace next)", keybinds.Label(ui.KeybindGlobalWorkspaceNext)),
		fmt.Sprintf("%s   (activate workspace slot 1)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot1)),
		fmt.Sprintf("%s   (activate workspace slot 2)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot2)),
		fmt.Sprintf("%s   (activate workspace slot 3)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot3)),
		fmt.Sprintf("%s   (activate workspace slot 4)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot4)),
		fmt.Sprintf("%s   (activate workspace slot 5)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot5)),
		fmt.Sprintf("%s   (activate workspace slot 6)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot6)),
		fmt.Sprintf("%s   (activate workspace slot 7)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot7)),
		fmt.Sprintf("%s   (activate workspace slot 8)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot8)),
		fmt.Sprintf("%s   (activate workspace slot 9)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot9)),
		fmt.Sprintf("%s   (activate workspace slot 10)", keybinds.Label(ui.KeybindGlobalWorkspaceSlot10)),
		fmt.Sprintf("%s   (cycle thinking level)", keybinds.Label(ui.KeybindGlobalCycleThinking)),
		"/themes   (open theme modal with live preview)",
		"/themes [open|list|set|next|prev|status|create|edit|delete|slots]",
		"/themes create <id> [from <theme>]",
		"/themes edit <id> <slot> <#RRGGBB>",
		"/themes delete <id>",
		"/header [on|off|toggle|status]   (chat header visibility)",
		"/swarm [status|pending|approve <id>|reject <id>|set <name>|<name>]   (show dashboard, review pending children, or change device identity)",
		updateHelpLine(a.startupDevMode()),
		"/thinking [on|off|toggle|status]   (show or hide reasoning/thinking tags)",
		"/mouse [on|off|toggle|status]   (mouse click capture)",
		fmt.Sprintf("%s   (toggle mouse click capture)", keybinds.Label(ui.KeybindGlobalToggleMouse)),
		"/voice [open|device <id>|stt [provider] [model]|profile [list|use <id>|upsert <id> <adapter> [model]|whisper [id] [model]|delete <id>]|tts [provider] [voice]|test [seconds]]",
		fmt.Sprintf("%s   (record voice + transcribe into input)", keybinds.Label(ui.KeybindGlobalVoiceInput)),
		"/keybinds   (open keybind manager modal)",
		"/keybinds list",
		"/keybinds reset [all]",
		"/copy   (copy chat snapshot to clipboard)",
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
	if a.chat == nil {
		a.home.SetStatus("/copy is available in chat sessions only")
		return
	}
	if len(args) > 0 {
		a.home.SetStatus("usage: /copy")
		return
	}

	payload := a.chat.ClipboardText()
	successStatus := "copied chat snapshot to clipboard"
	if err := copyTextToClipboard(payload); err != nil {
		a.home.SetStatus(fmt.Sprintf("copy failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("copy failed: %v", err))
		return
	}

	a.home.SetStatus(successStatus)
	a.showToast(ui.ToastSuccess, successStatus)
}

func (a *App) handleOutputCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.chat == nil {
		a.home.SetStatus("/output is available in chat sessions only")
		return
	}
	if len(args) > 0 {
		a.home.SetStatus("usage: /output")
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
	a.openHomeSessionsModal(query)
}

func (a *App) handleNewCommand() {
	if err := a.openChatSession("New Session", ""); err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("/new failed: %v", err))
		return
	}
}

func (a *App) handlePlanCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.route != "chat" || a.chat == nil {
		a.home.SetStatus("plan commands are available in chat: /plan [show|exit|list|use|new]")
		return
	}
	sessionID := strings.TrimSpace(a.chat.SessionID())
	if sessionID == "" {
		a.home.SetStatus("session id is unavailable")
		return
	}
	showCurrentPlan := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		plan, ok, err := a.api.GetActiveSessionPlan(ctx, sessionID)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/plan failed: %v", err))
			return
		}
		if !ok {
			plan = client.SessionPlan{Title: "Current Plan", Status: "draft"}
		}
		if !a.chat.OpenCurrentPlanModal(ui.ChatSessionPlan{ID: strings.TrimSpace(plan.ID), Title: strings.TrimSpace(plan.Title), Plan: plan.Plan, Status: strings.TrimSpace(plan.Status), ApprovalState: strings.TrimSpace(plan.ApprovalState)}) {
			a.home.SetStatus("current plan modal is unavailable while another modal is open")
			return
		}
		if ok {
			a.home.SetStatus(fmt.Sprintf("current plan: %s", emptyFallback(strings.TrimSpace(plan.Title), strings.TrimSpace(plan.ID))))
		} else {
			a.home.SetStatus("current plan: no active plan")
		}
	}
	if len(args) == 0 || strings.EqualFold(args[0], "help") {
		if len(args) == 0 {
			showCurrentPlan()
			return
		}
		a.home.SetStatus("usage: /plan [show|exit|list|use|new]")
		a.chat.AppendSystemMessage("usage:\n/plan\n/plan show\n/plan exit [title]\n/plan list [limit]\n/plan use <plan_id>\n/plan new [title]")
		return
	}

	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "show":
		showCurrentPlan()
	case "exit":
		if a.chat.PermissionModalVisible() {
			a.home.SetStatus("resolve pending permissions before exiting plan mode")
			return
		}
		title := "Exit Plan Mode"
		if len(args) > 1 {
			title = strings.TrimSpace(strings.Join(args[1:], " "))
		}
		body := a.buildPlanExitModalBody(title)
		if !a.chat.OpenExitPlanModeModal(title, body) {
			a.home.SetStatus("exit plan modal is unavailable while pending permissions are open")
			return
		}
		a.home.SetStatus("review plan approval and confirm")
	case "list":
		limit := 20
		if len(args) > 1 {
			if parsed, err := strconv.Atoi(strings.TrimSpace(args[1])); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		plans, activeID, err := a.api.ListSessionPlans(ctx, sessionID, limit)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/plan list failed: %v", err))
			return
		}
		lines := []string{fmt.Sprintf("plans: %d (active: %s)", len(plans), emptyFallback(activeID, "none"))}
		if len(plans) == 0 {
			lines = append(lines, "No saved plans yet. Use /plan new [title].")
		} else {
			maxLines := len(plans)
			if maxLines > 12 {
				maxLines = 12
			}
			for i := 0; i < maxLines; i++ {
				plan := plans[i]
				activeMark := " "
				if plan.Active {
					activeMark = "*"
				}
				lines = append(lines, fmt.Sprintf("%s %s  %s", activeMark, plan.ID, clampText(plan.Title, 56)))
			}
			if len(plans) > maxLines {
				lines = append(lines, fmt.Sprintf("... %d more", len(plans)-maxLines))
			}
		}
		a.home.SetCommandOverlay(lines)
		a.home.SetStatus("plan list loaded")
	case "use":
		if len(args) < 2 {
			a.home.SetStatus("usage: /plan use <plan_id>")
			return
		}
		planID := strings.TrimSpace(args[1])
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		plan, err := a.api.SetActiveSessionPlan(ctx, sessionID, planID)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/plan use failed: %v", err))
			return
		}
		a.home.SetStatus(fmt.Sprintf("active plan: %s", plan.ID))
		a.chat.SetActivePlan(chatPlanLabel(plan))
		a.chat.AppendSystemMessage(fmt.Sprintf("Active plan set to %s (%s).", plan.ID, emptyFallback(plan.Title, "untitled")))
	case "new":
		title := "New Plan"
		if len(args) > 1 {
			title = strings.TrimSpace(strings.Join(args[1:], " "))
		}
		activate := true
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		plan, err := a.api.SaveSessionPlan(ctx, sessionID, client.SessionPlanUpsertRequest{
			Title:    title,
			Plan:     "# " + title + "\n\n- [ ] next step\n",
			Status:   "draft",
			Activate: &activate,
		})
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/plan new failed: %v", err))
			return
		}
		a.home.SetStatus(fmt.Sprintf("created plan: %s", plan.ID))
		a.chat.SetActivePlan(chatPlanLabel(plan))
		a.chat.AppendSystemMessage(fmt.Sprintf("Created new active plan %s (%s).", plan.ID, emptyFallback(plan.Title, "untitled")))
		showCurrentPlan()
	default:
		a.home.SetStatus("usage: /plan [show|exit|list|use|new]")
		a.chat.AppendSystemMessage("usage:\n/plan\n/plan show\n/plan exit [title]\n/plan list [limit]\n/plan use <plan_id>\n/plan new [title]")
	}
}

func (a *App) handleCompactCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.route != "chat" || a.chat == nil {
		a.home.SetStatus("compact command is available in chat: /compact [threshold%] [notes]")
		return
	}
	options := parseCompactCommandArgs(args)
	if options.HasThreshold {
		if a.api == nil {
			a.home.SetStatus("/compact failed: api client is not configured")
			return
		}
		sessionID := strings.TrimSpace(a.chat.SessionID())
		if sessionID == "" {
			a.home.SetStatus("/compact failed: session id is unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		session, err := a.api.GetSession(ctx, sessionID)
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/compact failed: load session metadata: %v", err))
			return
		}
		metadata := cloneMetadataMap(session.Metadata)
		if options.ThresholdPercent > 0 {
			metadata[compactThresholdMetadataKey] = normalizeCompactThresholdPercent(options.ThresholdPercent)
		} else {
			delete(metadata, compactThresholdMetadataKey)
		}
		if _, err := a.api.UpdateSessionMetadata(ctx, sessionID, metadata); err != nil {
			a.home.SetStatus(fmt.Sprintf("/compact failed: save threshold: %v", err))
			return
		}
		if options.ThresholdPercent > 0 {
			a.chat.AppendSystemMessage(fmt.Sprintf("Auto compact threshold set to %s remaining context.", formatCompactThresholdPercent(options.ThresholdPercent)))
		} else {
			a.chat.AppendSystemMessage("Auto compact threshold cleared for this session.")
		}
	}
	if !a.chat.StartManualCompact(options.Note) {
		a.home.SetStatus("/compact ignored (run already active or chat unavailable)")
		return
	}
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
		TargetName:          "commit",
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
		"execution_context": map[string]any{
			"workspace_path":       launchRecord.WorkspacePath,
			"cwd":                  launchRecord.CWD,
			"worktree_mode":        launchRecord.WorktreeMode,
			"worktree_root_path":   launchRecord.WorktreeRootPath,
			"worktree_branch":      launchRecord.WorktreeBranch,
			"worktree_base_branch": launchRecord.WorktreeBaseBranch,
		},
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

func (a *App) buildPlanExitModalBody(title string) string {
	mode := "plan"
	if a.chat != nil {
		mode = a.chat.SessionMode()
	}
	modelLabel := model.DisplayModelLabel(a.homeModel.ModelProvider, a.homeModel.ModelName, a.homeModel.ServiceTier, a.homeModel.ContextMode)
	if modelLabel == "unset" {
		modelLabel = "unset"
	}
	workspace := strings.TrimSpace(a.home.ActiveWorkspaceName())
	if workspace == "" {
		workspace = strings.TrimSpace(a.homeModel.CWD)
	}
	if workspace == "" {
		workspace = "workspace"
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Exit Plan Mode"
	}

	lines := []string{
		fmt.Sprintf("Title: %s", title),
		fmt.Sprintf("Current mode: %s", mode),
		fmt.Sprintf("Workspace: %s", workspace),
		fmt.Sprintf("Model: %s", modelLabel),
		"",
		"Confirming this request will switch this session to auto mode.",
		"After the switch, normal auto-mode tool permission policy applies.",
		"",
		"Check before approve:",
		"- implementation steps are complete and concrete",
		"- risks and rollback notes are captured",
		"- next execution tasks are explicit",
	}
	return strings.Join(lines, "\n")
}

func (a *App) openChatSession(titleSeed, initialPrompt string) error {
	if a.api == nil {
		return errors.New("api client is not configured")
	}

	workspacePath := strings.TrimSpace(a.activeContextPath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	if workspacePath == "" {
		return errors.New("workspace path is required")
	}

	workspaceName := a.contextDisplayNameForPath(workspacePath, strings.TrimSpace(a.home.ActiveWorkspaceName()))

	title := strings.TrimSpace(titleSeed)
	if title == "" {
		title = "New Session"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	preference := client.ModelPreference{
		Provider:    strings.TrimSpace(a.homeModel.ModelProvider),
		Model:       strings.TrimSpace(a.homeModel.ModelName),
		Thinking:    strings.TrimSpace(a.homeModel.ThinkingLevel),
		ServiceTier: strings.TrimSpace(a.homeModel.ServiceTier),
		ContextMode: strings.TrimSpace(a.homeModel.ContextMode),
	}
	if strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" || strings.TrimSpace(preference.Thinking) == "" {
		return errors.New("new sessions require an explicit draft model selection")
	}
	session, err := a.api.CreateSession(ctx, title, workspacePath, workspaceName, preference)
	if err != nil {
		return err
	}
	warning := strings.TrimSpace(session.Warning)
	selectedMode := "auto"
	if a.home != nil {
		selectedMode = a.home.SessionMode()
	}
	selectedMode = strings.TrimSpace(selectedMode)
	if selectedMode != "" && !strings.EqualFold(strings.TrimSpace(session.Mode), selectedMode) {
		mode, modeErr := a.api.SetSessionMode(ctx, strings.TrimSpace(session.ID), selectedMode)
		if modeErr != nil {
			return fmt.Errorf("set session mode %q failed: %w", selectedMode, modeErr)
		}
		session.Mode = mode
	}
	created := model.SessionSummary{
		ID:                 strings.TrimSpace(session.ID),
		WorkspacePath:      strings.TrimSpace(session.WorkspacePath),
		WorkspaceName:      strings.TrimSpace(session.WorkspaceName),
		Title:              strings.TrimSpace(session.Title),
		Mode:               strings.TrimSpace(session.Mode),
		Metadata:           cloneMetadataMap(session.Metadata),
		Preference:         session.Preference,
		WorktreeEnabled:    session.WorktreeEnabled,
		WorktreeRootPath:   strings.TrimSpace(session.WorktreeRootPath),
		WorktreeBaseBranch: strings.TrimSpace(session.WorktreeBaseBranch),
		WorktreeBranch:     strings.TrimSpace(session.WorktreeBranch),
	}
	if created.WorkspacePath == "" {
		created.WorkspacePath = workspacePath
	}
	if created.WorkspaceName == "" {
		created.WorkspaceName = workspaceName
	}
	created.WorkspaceName = a.contextDisplayNameForPath(created.WorkspacePath, created.WorkspaceName)
	if created.Title == "" {
		created.Title = title
	}
	if err := a.openSessionSummary(created, strings.TrimSpace(initialPrompt)); err != nil {
		return err
	}
	if warning != "" {
		if a.chat != nil {
			a.chat.SetStatus(warning)
		}
		a.showToast(ui.ToastWarning, warning)
	}
	return nil
}

func (a *App) openExistingSession(summary model.SessionSummary) error {
	return a.openSessionSummary(summary, "")
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
	if a.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if resolved, err := a.api.GetSessionPreference(ctx, sessionID); err == nil {
			modelProvider = strings.TrimSpace(resolved.Preference.Provider)
			modelName = strings.TrimSpace(resolved.Preference.Model)
			thinkingLevel = strings.TrimSpace(resolved.Preference.Thinking)
			serviceTier = strings.TrimSpace(resolved.Preference.ServiceTier)
			contextMode = strings.TrimSpace(resolved.Preference.ContextMode)
			contextWindow = resolved.ContextWindow
		}
		if mode, err := a.api.GetSessionMode(ctx, sessionID); err == nil {
			summary.Mode = mode
		}
		cancel()
	}
	if modelProvider == "" && modelName == "" {
		modelProvider = strings.TrimSpace(summary.Preference.Provider)
		modelName = strings.TrimSpace(summary.Preference.Model)
		thinkingLevel = strings.TrimSpace(summary.Preference.Thinking)
		serviceTier = strings.TrimSpace(summary.Preference.ServiceTier)
		contextMode = strings.TrimSpace(summary.Preference.ContextMode)
	}
	openedSummary := model.SessionSummary{
		ID:                     sessionID,
		WorkspacePath:          strings.TrimSpace(summary.WorkspacePath),
		WorkspaceName:          strings.TrimSpace(summary.WorkspaceName),
		Title:                  title,
		Mode:                   strings.TrimSpace(summary.Mode),
		Metadata:               cloneMetadataMap(summary.Metadata),
		PendingPermissionCount: summary.PendingPermissionCount,
		Lifecycle:              cloneClientSessionLifecycle(summary.Lifecycle),
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
	a.upsertHomeSessionSummary(openedSummary)
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

func resolveSessionEffectiveAgent(summary model.SessionSummary, fallbackAgent, fallbackExecution string, fallbackExitPlanMode, fallbackRuntimeKnown bool) (string, string, bool, bool) {
	metadata := summary.Metadata
	resolved := consumeStringMetadata(metadata, "subagent")
	if resolved == "" {
		resolved = consumeStringMetadata(metadata, "requested_subagent")
	}
	if resolved == "" {
		resolved = lineageAgentName(consumeStringMetadata(metadata, "lineage_label"))
	}
	if resolved == "" {
		targetKind := consumeStringMetadata(metadata, "target_kind")
		targetName := consumeStringMetadata(metadata, "target_name")
		if targetKind != "" && targetName != "" {
			resolved = targetName
		}
	}
	if resolved == "" {
		resolved = consumeStringMetadata(metadata, "background_agent")
	}
	if resolved == "" {
		resolved = consumeStringMetadata(metadata, "requested_background_agent")
	}
	if resolved == "" {
		resolved = consumeStringMetadata(metadata, "agent_name")
	}
	if resolved == "" {
		resolved = emptyFallback(strings.TrimSpace(fallbackAgent), "swarm")
	}
	if strings.EqualFold(strings.TrimSpace(resolved), strings.TrimSpace(fallbackAgent)) {
		return emptyFallback(strings.TrimSpace(fallbackAgent), "swarm"), strings.TrimSpace(fallbackExecution), fallbackExitPlanMode, fallbackRuntimeKnown
	}
	return resolved, "", true, true
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
		return resolveSessionEffectiveAgent(summary, fallbackAgent, fallbackExecution, fallbackExitPlanMode, fallbackRuntimeKnown)
	}
	return fallbackAgent, fallbackExecution, fallbackExitPlanMode, fallbackRuntimeKnown
}

func normalizeBackgroundStatus(status string, pendingPermissions int) string {
	if pendingPermissions > 0 {
		return "blocked"
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "ready":
		return "idle"
	default:
		return strings.TrimSpace(status)
	}
}

func backgroundStatusBadge(record model.BackgroundSessionSummary) string {
	status := normalizeBackgroundStatus(record.Status, record.PendingPermissions)
	if status == "" {
		status = "idle"
	}
	if record.PendingPermissions > 0 {
		return fmt.Sprintf("bg %d blocked", record.PendingPermissions)
	}
	return "bg " + status
}

func backgroundHeaderBadge(records []model.BackgroundSessionSummary) string {
	if len(records) == 0 {
		return ""
	}
	blocked := 0
	running := 0
	for _, record := range records {
		switch normalizeBackgroundStatus(record.Status, record.PendingPermissions) {
		case "blocked":
			blocked++
		case "running":
			running++
		}
	}
	parts := []string{fmt.Sprintf("bg:%d", len(records))}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("run:%d", running))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("blocked:%d", blocked))
	}
	return strings.Join(parts, " ")
}

func (a *App) latestBackgroundSummaryForChat() *model.BackgroundSessionSummary {
	if a == nil || a.chat == nil {
		return nil
	}
	current := strings.TrimSpace(a.chat.SessionID())
	if current == "" {
		return nil
	}
	for _, record := range a.homeModel.BackgroundSessions {
		if strings.TrimSpace(record.ChildSessionID) == current || strings.TrimSpace(record.ParentSessionID) == current {
			copy := record
			return &copy
		}
	}
	return nil
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

func (a *App) openChatView(sessionID, sessionTitle, workspacePath, workspaceName, sessionMode, worktreeBranch string, worktreeEnabled bool, worktreeRootPath, initialPrompt, modelProvider, modelName, thinkingLevel, serviceTier, contextMode string, contextWindow int) error {
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

	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		Backend:            newAPIChatBackend(a.api),
		SessionID:          strings.TrimSpace(sessionID),
		SessionTitle:       strings.TrimSpace(sessionTitle),
		InitialPrompt:      strings.TrimSpace(initialPrompt),
		Presets:            a.home.ModelPresets(),
		SessionTabs:        chatSessionTabsFromSummaries(a.homeModel.RecentSessions),
		CommandSuggestions: buildHomeCommandSuggestions(a.startupDevMode()),
		ShowHeader:         a.config.Chat.ShowHeader,
		AuthConfigured:     a.homeModel.AuthConfigured,
		ShowThinkingTags:   boolPtr(a.config.Chat.ThinkingTags),
		ModelProvider:      modelProvider,
		ModelName:          modelName,
		AvailableModels:    a.chatAvailableModels(modelProvider),
		ThinkingLevel:      thinkingLevel,
		ServiceTier:        serviceTier,
		ContextMode:        contextMode,
		ContextWindow:      contextWindow,
		SessionMode:        sessionMode,
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
		resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown := resolveSessionEffectiveAgent(summary,
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
			Mode:            strings.TrimSpace(summary.Mode),
			UpdatedAgo:      strings.TrimSpace(summary.UpdatedAgo),
			Provider:        strings.TrimSpace(summary.Preference.Provider),
			ModelName:       strings.TrimSpace(summary.Preference.Model),
			ServiceTier:     strings.TrimSpace(summary.Preference.ServiceTier),
			ContextMode:     strings.TrimSpace(summary.Preference.ContextMode),
			Background:      lineage.Background,
			ParentSessionID: strings.TrimSpace(lineage.ParentSessionID),
			LineageKind:     strings.TrimSpace(lineage.LineageKind),
			LineageLabel:    strings.TrimSpace(lineage.LineageLabel),
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
			Mode:            mode,
			UpdatedAgo:      strings.TrimSpace(summary.UpdatedAgo),
			Provider:        strings.TrimSpace(summary.Preference.Provider),
			ModelName:       strings.TrimSpace(summary.Preference.Model),
			ServiceTier:     strings.TrimSpace(summary.Preference.ServiceTier),
			ContextMode:     strings.TrimSpace(summary.Preference.ContextMode),
			Background:      lineage.Background,
			ParentSessionID: strings.TrimSpace(lineage.ParentSessionID),
			LineageKind:     strings.TrimSpace(lineage.LineageKind),
			LineageLabel:    strings.TrimSpace(lineage.LineageLabel),
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

func backgroundSessionPaletteItemsFromSummaries(records []model.BackgroundSessionSummary) []ui.ChatSessionPaletteItem {
	items := make([]ui.ChatSessionPaletteItem, 0, len(records))
	for _, record := range records {
		title := strings.TrimSpace(record.ChildTitle)
		if title == "" {
			title = strings.TrimSpace(record.ChildSessionID)
		}
		workspaceName := strings.TrimSpace(record.WorkspaceName)
		if label := strings.TrimSpace(record.TargetName); label != "" {
			workspaceName = strings.TrimSpace(strings.Join([]string{workspaceName, label}, " ← "))
		} else if parent := strings.TrimSpace(record.ParentTitle); parent != "" {
			workspaceName = fmt.Sprintf("%s ← %s", emptyFallback(workspaceName, "background"), parent)
		}
		meta := strings.TrimSpace(record.Status)
		if meta == "" {
			meta = strings.TrimSpace(record.TargetName)
		}
		items = append(items, ui.ChatSessionPaletteItem{
			ID:              strings.TrimSpace(record.ChildSessionID),
			Title:           title,
			WorkspaceName:   workspaceName,
			WorkspacePath:   strings.TrimSpace(record.CWD),
			Mode:            "background",
			UpdatedAgo:      meta,
			Background:      true,
			ParentSessionID: strings.TrimSpace(record.ParentSessionID),
			LineageKind:     "background_agent",
			LineageLabel:    ui.SessionLineageDisplay(ui.SessionLineage{Background: true, ParentSessionID: strings.TrimSpace(record.ParentSessionID), LineageKind: "background_agent", LineageLabel: strings.TrimSpace(record.TargetName), TargetKind: strings.TrimSpace(record.TargetKind), TargetName: strings.TrimSpace(record.TargetName)}),
			TargetKind:      strings.TrimSpace(record.TargetKind),
			TargetName:      strings.TrimSpace(record.TargetName),
			Depth:           0,
		})
	}
	return items
}

func (a *App) backgroundSessionSummaries() []model.BackgroundSessionSummary {
	if a == nil {
		return nil
	}
	records := make([]model.BackgroundSessionSummary, 0)
	for _, summary := range a.homeModel.RecentSessions {
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
	for _, record := range a.homeModel.BackgroundSessions {
		if strings.TrimSpace(record.ChildSessionID) == "" {
			continue
		}
		found := false
		for _, existing := range records {
			if strings.TrimSpace(existing.ChildSessionID) == strings.TrimSpace(record.ChildSessionID) {
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
		parentID := strings.TrimSpace(ui.SessionLineageFromSummary(summary).ParentSessionID)
		depth := 0
		if parentID != "" {
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

func (a *App) commitSessionTitle(parentTitle, instructions string) string {
	parentTitle = strings.TrimSpace(parentTitle)
	instructions = strings.TrimSpace(instructions)
	if parentTitle == "" {
		parentTitle = "Session"
	}
	if instructions == "" {
		return fmt.Sprintf("Commit · %s", parentTitle)
	}
	return clampText(fmt.Sprintf("Commit · %s · %s", parentTitle, instructions), 80)
}

func (a *App) commitRunInstructions(userInstructions string) string {
	instructions := []string{
		"You are the background commit agent handling /commit from the TUI.",
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

func (a *App) commitLineageMetadata(parentSessionID string, parentSummary model.SessionSummary, instructions string, execCtx *client.RunExecutionContext) map[string]any {
	metadata := map[string]any{
		"parent_session_id":          strings.TrimSpace(parentSessionID),
		"parent_title":               strings.TrimSpace(parentSummary.Title),
		"commit_instructions":        strings.TrimSpace(instructions),
		"lineage_kind":               "background_agent",
		"lineage_label":              "@commit",
		"launch_source":              "commit",
		"requested_background_agent": "commit",
		"background_agent":           "commit",
	}
	if execCtx != nil {
		metadata["execution_context"] = map[string]any{
			"workspace_path":       strings.TrimSpace(execCtx.WorkspacePath),
			"cwd":                  strings.TrimSpace(execCtx.CWD),
			"worktree_mode":        strings.TrimSpace(execCtx.WorktreeMode),
			"worktree_root_path":   strings.TrimSpace(execCtx.WorktreeRootPath),
			"worktree_branch":      strings.TrimSpace(execCtx.WorktreeBranch),
			"worktree_base_branch": strings.TrimSpace(execCtx.WorktreeBaseBranch),
		}
	}
	return metadata
}

func (a *App) commitSessionWorkspacePaths(parentSummary model.SessionSummary, execCtx *client.RunExecutionContext) (workspacePath, hostWorkspacePath, runtimeWorkspacePath string) {
	workspacePath = strings.TrimSpace(parentSummary.WorkspacePath)
	if execCtx != nil {
		runtimeWorkspacePath = strings.TrimSpace(execCtx.WorkspacePath)
		if runtimeWorkspacePath == "" {
			runtimeWorkspacePath = strings.TrimSpace(execCtx.CWD)
		}
	}
	if metadata := parentSummary.Metadata; len(metadata) > 0 {
		hostWorkspacePath = consumeStringMetadata(metadata, "swarm_routed_host_workspace_path")
		if runtimeWorkspacePath == "" {
			runtimeWorkspacePath = consumeStringMetadata(metadata, "swarm_routed_runtime_workspace_path")
		}
	}
	if hostWorkspacePath == "" {
		hostWorkspacePath = workspacePath
	}
	if runtimeWorkspacePath == "" {
		runtimeWorkspacePath = workspacePath
	}
	if workspacePath == "" {
		workspacePath = firstNonEmpty(hostWorkspacePath, runtimeWorkspacePath)
	}
	return workspacePath, hostWorkspacePath, runtimeWorkspacePath
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

func (a *App) commitChildSessionBaseMetadata(parentMetadata map[string]any) map[string]any {
	base := cloneMetadataMap(parentMetadata)
	if len(base) == 0 {
		return nil
	}
	keys := []string{
		"swarm_session_hosted",
		"swarm_routed_host_swarm_id",
		"swarm_routed_host_backend_url",
		"swarm_routed_host_workspace_path",
		"swarm_routed_runtime_workspace_path",
		"swarm_routed_child_swarm_id",
	}
	filtered := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := base[key]; ok {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func (a *App) createBackgroundCommitSession(ctx context.Context, parentSessionID string, parentSummary model.SessionSummary, instructions string) (model.SessionSummary, error) {
	execCtx := a.commitExecutionContext(parentSummary)
	workspacePath, hostWorkspacePath, runtimeWorkspacePath := a.commitSessionWorkspacePaths(parentSummary, execCtx)
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.activeContextPath())
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	if workspacePath == "" {
		return model.SessionSummary{}, errors.New("workspace path is required")
	}
	if hostWorkspacePath == "" {
		hostWorkspacePath = workspacePath
	}
	if runtimeWorkspacePath == "" {
		runtimeWorkspacePath = firstNonEmpty(strings.TrimSpace(execCtx.WorkspacePath), strings.TrimSpace(execCtx.CWD), workspacePath)
	}
	workspaceName := strings.TrimSpace(parentSummary.WorkspaceName)
	if workspaceName == "" {
		workspaceName = directoryNameForPath(hostWorkspacePath)
	}
	metadata := mergeMetadataMaps(a.commitChildSessionBaseMetadata(parentSummary.Metadata), a.commitLineageMetadata(parentSessionID, parentSummary, instructions, execCtx))
	child, err := a.api.CreateSessionWithOptions(ctx, client.SessionCreateOptions{
		Title:                a.commitSessionTitle(parentSummary.Title, instructions),
		WorkspacePath:        workspacePath,
		HostWorkspacePath:    hostWorkspacePath,
		RuntimeWorkspacePath: runtimeWorkspacePath,
		WorkspaceName:        workspaceName,
		Mode:                 "auto",
		AgentName:            emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm"),
		Metadata:             metadata,
		Preference:           parentSummary.Preference,
		WorktreeMode:         strings.TrimSpace(execCtx.WorktreeMode),
	})
	if err != nil {
		return model.SessionSummary{}, err
	}
	metadata = mergeMetadataMaps(child.Metadata, metadata)
	child.Metadata = metadata
	return model.SessionSummary{
		ID:                     strings.TrimSpace(child.ID),
		WorkspacePath:          strings.TrimSpace(child.WorkspacePath),
		WorkspaceName:          strings.TrimSpace(child.WorkspaceName),
		Title:                  strings.TrimSpace(child.Title),
		Mode:                   strings.TrimSpace(child.Mode),
		Metadata:               metadata,
		Preference:             child.Preference,
		WorktreeEnabled:        child.WorktreeEnabled,
		WorktreeRootPath:       strings.TrimSpace(child.WorktreeRootPath),
		WorktreeBaseBranch:     strings.TrimSpace(child.WorktreeBaseBranch),
		WorktreeBranch:         strings.TrimSpace(child.WorktreeBranch),
		UpdatedAgo:             formatAgo(child.UpdatedAt),
		Lifecycle:              child.Lifecycle,
		PendingPermissionCount: child.PendingPermissionCount,
	}, nil
}

func (a *App) startBackgroundCommitRun(ctx context.Context, childSummary model.SessionSummary, instructions string) (client.BackgroundRunAccepted, error) {
	prompt := "Review the git diff in scope, prepare the right staged set, and create the commit now."
	return a.api.StartBackgroundSessionRun(ctx, strings.TrimSpace(childSummary.ID), prompt, "", a.commitRunInstructions(instructions), client.RunSessionOptions{
		Compact:          false,
		Background:       true,
		TargetKind:       "background",
		TargetName:       "commit",
		ExecutionContext: a.commitExecutionContext(childSummary),
	})
}

func (a *App) consumeHomeOverlayActions() {
	if a.home == nil {
		return
	}
	for {
		processed := false

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
		if action, ok := a.home.PopMCPModalAction(); ok {
			a.handleMCPModalAction(action)
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
		if action, ok := a.home.PopSandboxModalAction(); ok {
			a.handleSandboxModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopWorktreesModalAction(); ok {
			a.handleWorktreesModalAction(action)
			processed = true
		}
		if action, ok := a.home.PopMCPModalAction(); ok {
			a.handleMCPModalAction(action)
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

func (a *App) consumeModelsModalActions() {
	if a.home == nil {
		return
	}
	for {
		action, ok := a.home.PopModelsModalAction()
		if !ok {
			return
		}
		a.handleModelsModalAction(action)
	}
}

func (a *App) consumeThemeModalActions() {
	if a.home == nil {
		return
	}
	for {
		action, ok := a.home.PopThemeModalAction()
		if !ok {
			return
		}
		a.handleThemeModalAction(action)
	}
}

func cloneClientSessionLifecycle(lifecycle *client.SessionLifecycleSnapshot) *client.SessionLifecycleSnapshot {
	if lifecycle == nil {
		return nil
	}
	copy := *lifecycle
	return &copy
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
	if value := strings.TrimSpace(incoming.UpdatedAgo); value != "" {
		merged.UpdatedAgo = value
	}
	if incoming.Lifecycle != nil {
		merged.Lifecycle = cloneClientSessionLifecycle(incoming.Lifecycle)
	}
	return merged
}

func modelSessionSummaryFromClient(record client.SessionSummary) model.SessionSummary {
	title := strings.TrimSpace(record.Title)
	if title == "" {
		title = strings.TrimSpace(record.ID)
	}
	return model.SessionSummary{
		ID:                     strings.TrimSpace(record.ID),
		WorkspacePath:          strings.TrimSpace(record.WorkspacePath),
		WorkspaceName:          strings.TrimSpace(record.WorkspaceName),
		Title:                  title,
		Mode:                   strings.TrimSpace(record.Mode),
		Metadata:               cloneMetadataMap(record.Metadata),
		PendingPermissionCount: record.PendingPermissionCount,
		Lifecycle:              cloneClientSessionLifecycle(record.Lifecycle),
		Preference:             mergeClientModelPreference(client.ModelPreference{}, record.Preference),
		WorktreeEnabled:        record.WorktreeEnabled,
		WorktreeRootPath:       strings.TrimSpace(record.WorktreeRootPath),
		WorktreeBaseBranch:     strings.TrimSpace(record.WorktreeBaseBranch),
		WorktreeBranch:         strings.TrimSpace(record.WorktreeBranch),
		UpdatedAgo:             formatAgo(record.UpdatedAt),
	}
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

func chatSessionTabsFromClientSummaries(records []client.SessionSummary) []ui.ChatSessionTab {
	return chatSessionTabsWithExtras(nil, records)
}

const (
	workspaceOverviewDesktopSessionLimit = 200
	homeRecentSessionLimit               = 50
	// Home only renders workspace names/directories on first paint.
	homeWorkspaceOverviewSessionLimit = 1
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
			Mode:            strings.TrimSpace(tab.Mode),
			UpdatedAgo:      strings.TrimSpace(tab.UpdatedAgo),
			Provider:        strings.TrimSpace(tab.Provider),
			ModelName:       strings.TrimSpace(tab.ModelName),
			ServiceTier:     strings.TrimSpace(tab.ServiceTier),
			ContextMode:     strings.TrimSpace(tab.ContextMode),
			Background:      tab.Background,
			ParentSessionID: strings.TrimSpace(tab.ParentSessionID),
			LineageKind:     strings.TrimSpace(tab.LineageKind),
			LineageLabel:    strings.TrimSpace(tab.LineageLabel),
			TargetKind:      strings.TrimSpace(tab.TargetKind),
			TargetName:      strings.TrimSpace(tab.TargetName),
			Depth:           tab.Depth,
		})
	}

	return items
}

func (a *App) openHomeSessionsModal(query string) {
	a.home.ClearCommandOverlay()
	if a.api == nil {
		a.home.SetStatus("sessions modal failed: api unavailable")
		return
	}
	workspacePath := strings.TrimSpace(a.activeContextPath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	if workspacePath == "" {
		a.home.SetStatus("sessions modal failed: workspace path is required")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	sessions, err := a.api.ListSessionsForCWD(ctx, workspaceOverviewDesktopSessionLimit, workspacePath)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("sessions modal failed: %v", err))
		return
	}
	items := chatSessionPaletteItemsFromTabs(scopedSessionTabsForPath(workspacePath, a.homeModel.RecentSessions, sessions))
	if !a.home.OpenSessionsModal(items, strings.TrimSpace(query)) {
		a.home.SetStatus("sessions modal unavailable while another modal is open")
		return
	}
	a.home.SetStatus("sessions modal")
}

func (a *App) openChatSessionsPalette(query string) error {
	if a.chat == nil {
		return errors.New("chat is unavailable")
	}
	if a.api == nil {
		return errors.New("api is unavailable")
	}
	workspacePath := strings.TrimSpace(a.activeContextPath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	if workspacePath == "" {
		return errors.New("workspace path is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	sessions, err := a.api.ListSessionsForCWD(ctx, workspaceOverviewDesktopSessionLimit, workspacePath)
	if err != nil {
		return err
	}
	tabs := scopedSessionTabsForPath(workspacePath, a.homeModel.RecentSessions, sessions)
	a.chat.SetSessionTabs(tabs)
	if query == "" {
		query = strings.TrimSpace(a.chat.SessionID())
	}
	if !a.chat.OpenSessionsPalette(a.chat.SessionPaletteItems(), strings.TrimSpace(query)) {
		a.home.SetStatus("sessions palette unavailable while another modal is open")
		return nil
	}
	a.home.SetStatus("sessions palette")
	return nil
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
	case ui.ChatActionToggleBypassPermissions:
		a.setPermissionsBypass(!a.homeModel.BypassPermissions)
	}
}

func (a *App) backgroundModalOrCommandOpen() bool {
	if a == nil || a.route != "home" || a.home == nil {
		return false
	}
	return a.home.SessionsModalVisible() ||
		a.home.AuthModalVisible() ||
		a.home.WorkspaceModalVisible() ||
		a.home.SandboxModalVisible() ||
		a.home.WorktreesModalVisible() ||
		a.home.ModelsModalVisible() ||
		a.home.AgentsModalVisible() ||
		a.home.VoiceModalVisible() ||
		a.home.ThemeModalVisible() ||
		a.home.KeybindsModalVisible()
}

func (a *App) openBackgroundSessionsModal() {
	if a == nil || a.home == nil {
		return
	}
	a.home.ClearCommandOverlay()
	if a.backgroundModalOrCommandOpen() {
		a.home.SetStatus("background summary unavailable while another modal is open")
		return
	}
	a.refreshBackgroundSessions()
	items := backgroundSessionPaletteItemsFromSummaries(a.homeModel.BackgroundSessions)
	if !a.home.OpenSessionsModal(items, "") {
		a.home.SetStatus("background summary unavailable while another modal is open")
		return
	}
	a.home.SetStatus("background agents")
}

func (a *App) handleHomeAction(action ui.HomeAction) {
	switch action.Kind {
	case ui.HomeActionSetDefaultSessionMode:
		a.applyDefaultNewSessionModeSetting(action.SessionMode)
	case ui.HomeActionOpenSession:
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
	case ui.HomeActionSelectWorkspace:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.SelectWorkspace(ctx, strings.TrimSpace(action.WorkspacePath))
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("activate workspace failed: %v", err))
			return
		}
		a.activePath = strings.TrimSpace(resolution.ResolvedPath)
		a.workspacePath = strings.TrimSpace(resolution.WorkspacePath)
		a.syncActiveWorkspaceSelection(resolution)
		a.home.SetStatus(fmt.Sprintf("workspace active: %s", displayPath(resolution.ResolvedPath)))
		a.queueReload(false)
	case ui.HomeActionOpenAgentsModal:
		a.openAgentsModal()
	case ui.HomeActionOpenModelsModal:
		a.openModelsModal("")
	case ui.HomeActionCycleThinking:
		a.cycleThinkingLevel()
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
	if !a.home.AuthModalVisible() {
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
		if a.home.AuthModalEditorMode() == "codex_browser_pending" {
			a.home.SetAuthModalLoading(true)
			a.home.SetAuthModalStatus("Auth URL copied to clipboard. Finish sign-in in your browser; this modal will close automatically after confirmation.")
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
			a.activePath = strings.TrimSpace(resolution.ResolvedPath)
			a.workspacePath = strings.TrimSpace(resolution.WorkspacePath)
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
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		resolution, err := a.api.SelectWorkspace(ctx, action.Path)
		if err != nil {
			a.home.SetWorkspaceModalLoading(false)
			a.home.SetWorkspaceModalError(fmt.Sprintf("activate workspace failed: %v", err))
			return
		}
		a.activePath = strings.TrimSpace(resolution.ResolvedPath)
		a.workspacePath = strings.TrimSpace(resolution.WorkspacePath)
		a.syncActiveWorkspaceSelection(resolution)
		a.home.SetWorkspaceModalDirectory(a.activeContextPath())
		a.refreshWorkspaceModalData("")
		a.home.SetWorkspaceModalStatus(fmt.Sprintf("workspace active: %s", displayPath(resolution.ResolvedPath)))
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

func (a *App) handleSandboxModalAction(action ui.SandboxModalAction) {
	if !a.home.SandboxModalVisible() {
		return
	}
	switch action.Kind {
	case ui.SandboxModalActionRefresh:
		a.refreshSandboxModalData("Running sandbox preflight...", true)
	case ui.SandboxModalActionSetEnabled:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		status, err := a.api.SetSandboxEnabled(ctx, action.Enabled)
		a.home.SetSandboxModalData(mapSandboxModalData(status))
		a.home.SetSandboxModalLoading(false)
		if err != nil {
			a.home.SetSandboxModalError(fmt.Sprintf("sandbox update failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("sandbox update failed: %v", err))
			return
		}
		label := "OFF"
		if status.Enabled {
			label = "ON"
		}
		a.home.SetSandboxModalStatus(fmt.Sprintf("Sandbox: %s", label))
		a.showToast(ui.ToastSuccess, fmt.Sprintf("Sandbox %s (global)", label))
	case ui.SandboxModalActionCopySetup:
		command := strings.TrimSpace(action.Command)
		if command == "" {
			a.home.SetSandboxModalLoading(false)
			a.home.SetSandboxModalError("Copy failed. Run: swarm sandbox_command")
			a.showToast(ui.ToastError, "Copy failed. Run: swarm sandbox_command")
			return
		}
		if err := copyTextToClipboard(command); err != nil {
			a.home.SetSandboxModalLoading(false)
			a.home.SetSandboxModalError("Copy failed. Run: swarm sandbox_command")
			a.showToast(ui.ToastError, "Copy failed. Run: swarm sandbox_command")
			return
		}
		a.home.SetSandboxModalLoading(false)
		a.home.SetSandboxModalStatus("Copied sandbox setup commands")
		a.showToast(ui.ToastSuccess, "Copied sandbox setup commands")
	default:
		a.home.SetSandboxModalLoading(false)
	}
}

func (a *App) handleWorktreesModalAction(action ui.WorktreesModalAction) {
	if !a.home.WorktreesModalVisible() {
		return
	}
	switch action.Kind {
	case ui.WorktreesModalActionRefresh:
		a.refreshWorktreesModalData("Refreshing worktrees settings...")
	case ui.WorktreesModalActionSetMode:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		enabled := action.Enabled
		useCurrentBranch := true
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{WorkspacePath: a.activeContextPath(), Enabled: &enabled, UseCurrentBranch: &useCurrentBranch})
		if err != nil {
			a.home.SetWorktreesModalLoading(false)
			a.home.SetWorktreesModalError(fmt.Sprintf("worktrees update failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("worktrees update failed: %v", err))
			return
		}
		a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
		a.home.SetWorktreesModalLoading(false)
		a.home.SetWorktreesModalStatus(a.worktreesStatusSummary(settings))
		a.showToast(ui.ToastSuccess, a.worktreesStatusSummary(settings))
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

func (a *App) handleMCPModalAction(action ui.MCPModalAction) {
	if !a.home.MCPModalVisible() {
		return
	}
	switch action.Kind {
	case ui.MCPModalActionRefresh:
		a.refreshMCPModalData("Refreshing MCP servers...")
	case ui.MCPModalActionSetEnabled:
		id := strings.TrimSpace(action.ID)
		if id == "" {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError("MCP server id is required")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		server, err := a.api.SetMCPServerEnabled(ctx, id, action.Enabled)
		if err != nil {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError(fmt.Sprintf("mcp toggle failed: %v", err))
			return
		}
		state := "disabled"
		if server.Enabled {
			state = "enabled"
		}
		a.home.SetMCPModalStatus(fmt.Sprintf("MCP %s: %s", server.ID, state))
		a.refreshMCPModalData("")
	case ui.MCPModalActionDelete:
		id := strings.TrimSpace(action.ID)
		if id == "" {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError("MCP server id is required")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := a.api.DeleteMCPServer(ctx, id); err != nil {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError(fmt.Sprintf("mcp delete failed: %v", err))
			return
		}
		a.home.SetMCPModalStatus(fmt.Sprintf("MCP server removed: %s", id))
		a.refreshMCPModalData("")
	case ui.MCPModalActionUpsert:
		if action.Upsert == nil {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError("mcp upsert payload is missing")
			return
		}
		upsert := action.Upsert
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		server, err := a.api.UpsertMCPServer(ctx, client.MCPServerUpsertRequest{
			ID:        strings.TrimSpace(upsert.ID),
			Name:      strings.TrimSpace(upsert.Name),
			Transport: strings.TrimSpace(upsert.Transport),
			URL:       strings.TrimSpace(upsert.URL),
			Command:   strings.TrimSpace(upsert.Command),
			Args:      append([]string(nil), upsert.Args...),
			Enabled:   upsert.Enabled,
			Source:    strings.TrimSpace(upsert.Source),
		})
		if err != nil {
			a.home.SetMCPModalLoading(false)
			a.home.SetMCPModalError(fmt.Sprintf("mcp save failed: %v", err))
			return
		}
		target := strings.TrimSpace(server.URL)
		if target == "" {
			target = strings.TrimSpace(server.Command)
		}
		a.home.SetMCPModalStatus(fmt.Sprintf("MCP server saved: %s (%s)", server.ID, emptyFallback(target, "configured")))
		a.refreshMCPModalData("")
	default:
		a.home.SetMCPModalLoading(false)
	}
}

func (a *App) handleAgentsModalAction(action ui.AgentsModalAction) {
	if !a.home.AgentsModalVisible() {
		return
	}
	switch action.Kind {
	case ui.AgentsModalActionRefresh:
		a.refreshAgentsModalData("Refreshing agent profiles...")
	case ui.AgentsModalActionSetUtilityAI:
		input := action.UtilityAI
		if input == nil || strings.TrimSpace(input.Provider) == "" || strings.TrimSpace(input.Model) == "" {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError("choose a provider and model for Utility AI")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := a.api.RestoreAgentDefaults(ctx, client.ProviderDefaultsPreview{
			UtilityProvider:   strings.TrimSpace(input.Provider),
			UtilityModel:      strings.TrimSpace(input.Model),
			UtilityThinking:   strings.TrimSpace(input.Thinking),
			OverwriteExplicit: input.OverwriteExplicit,
		})
		if err != nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError(fmt.Sprintf("set Utility AI failed: %v", err))
			return
		}
		status := fmt.Sprintf("set Utility AI baseline %s/%s", strings.TrimSpace(input.Provider), model.DisplayModelName(input.Provider, input.Model))
		if input.OverwriteExplicit {
			status = fmt.Sprintf("cleared Utility AI overrides and set %s/%s", strings.TrimSpace(input.Provider), model.DisplayModelName(input.Provider, input.Model))
		}
		if result.ProviderDefaultsPreview != nil {
			targets := result.ProviderDefaultsPreview.UtilityBaselineAgents
			if len(targets) == 0 && len(result.ProviderDefaultsPreview.CustomUtilityAgents) == 0 {
				targets = result.ProviderDefaultsPreview.UtilityAgents
			}
			if input.OverwriteExplicit {
				if len(result.ProviderDefaultsPreview.UtilityAgents) > 0 {
					status = fmt.Sprintf("cleared Utility AI overrides and set %s/%s for %s", strings.TrimSpace(input.Provider), model.DisplayModelName(input.Provider, input.Model), strings.Join(result.ProviderDefaultsPreview.UtilityAgents, ", "))
				}
			} else {
				if len(targets) > 0 {
					status = fmt.Sprintf("set Utility AI baseline %s/%s for %s", strings.TrimSpace(input.Provider), model.DisplayModelName(input.Provider, input.Model), strings.Join(targets, ", "))
				} else if len(result.ProviderDefaultsPreview.CustomUtilityAgents) > 0 {
					status = fmt.Sprintf("no blank Utility AI agents to set for %s/%s", strings.TrimSpace(input.Provider), model.DisplayModelName(input.Provider, input.Model))
				}
				if len(result.ProviderDefaultsPreview.CustomUtilityAgents) > 0 {
					status += fmt.Sprintf("; preserved overrides for %s", strings.Join(result.ProviderDefaultsPreview.CustomUtilityAgents, ", "))
				}
			}
		}
		a.home.SetAgentsModalStatus(status)
		a.refreshAgentsModalData("")
		a.queueReload(false)
	case ui.AgentsModalActionResetDefaults:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := a.api.ResetAgentDefaults(ctx)
		if err != nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError(fmt.Sprintf("reset defaults failed: %v", err))
			return
		}
		a.home.SetAgentsModalStatus(fmt.Sprintf("reset agents to built-in defaults: %d profiles", len(result.Profiles)))
		a.refreshAgentsModalData("")
		a.queueReload(false)
	case ui.AgentsModalActionActivatePrimary:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		active, _, err := a.api.ActivatePrimaryAgent(ctx, action.Name)
		if err != nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError(fmt.Sprintf("activate primary failed: %v", err))
			return
		}
		a.home.SetAgentsModalStatus(fmt.Sprintf("active primary: %s", emptyFallback(active, action.Name)))
		a.queueReload(false)
		a.syncChatAgentRuntime()
		a.refreshAgentsModalData("")
	case ui.AgentsModalActionUpsert:
		if action.Upsert == nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError("agent upsert payload is missing")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		req := client.AgentUpsertRequest{
			Name:        action.Upsert.Name,
			Mode:        action.Upsert.Mode,
			Description: action.Upsert.Description,
			Prompt:      action.Upsert.Prompt,
			Enabled:     action.Upsert.Enabled,
		}
		if strings.TrimSpace(action.Upsert.Mode) != "" ||
			strings.TrimSpace(action.Upsert.Description) != "" ||
			strings.TrimSpace(action.Upsert.Prompt) != "" ||
			strings.TrimSpace(action.Upsert.Provider) != "" ||
			strings.TrimSpace(action.Upsert.Model) != "" ||
			strings.TrimSpace(action.Upsert.Thinking) != "" ||
			action.Upsert.Enabled == nil {
			// Full editor submits must preserve explicit "inherit" clears for provider/model/thinking.
			req.Provider = stringPtr(action.Upsert.Provider)
			req.Model = stringPtr(action.Upsert.Model)
			req.Thinking = stringPtr(action.Upsert.Thinking)
		}
		profile, _, err := a.api.UpsertAgent(ctx, req)
		if err != nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError(fmt.Sprintf("save agent failed: %v", err))
			return
		}
		a.home.SetAgentsModalStatus(fmt.Sprintf("agent saved: %s (%s)", profile.Name, profile.Mode))
		a.refreshAgentsModalData("")
		a.queueReload(false)
	case ui.AgentsModalActionDelete:
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		deleted, activePrimary, _, err := a.api.DeleteAgent(ctx, action.Name)
		if err != nil {
			a.home.SetAgentsModalLoading(false)
			a.home.SetAgentsModalError(fmt.Sprintf("delete agent failed: %v", err))
			return
		}
		if strings.TrimSpace(activePrimary) == "" {
			activePrimary = "swarm"
		}
		a.home.SetAgentsModalStatus(fmt.Sprintf("agent deleted: %s (active primary: %s)", emptyFallback(deleted, action.Name), activePrimary))
		a.refreshAgentsModalData("")
		a.queueReload(false)
	default:
		a.home.SetAgentsModalLoading(false)
	}
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

	if method != "auto" && method != "code" {
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
	session, browserWarning, err := a.beginCodexBrowserLogin(startCtx, login, openBrowser)
	startCancel()
	if err != nil {
		a.authLogging.Store(false)
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("oauth login failed: %v", err))
		return
	}
	a.home.StartAuthModalCodexBrowserPending(codexBrowserPendingStatus(browserWarning), session.AuthURL)
	a.home.SetAuthModalLoading(true)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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
		if !a.home.AuthModalVisible() {
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

func (a *App) beginCodexBrowserLogin(ctx context.Context, login *ui.AuthModalLogin, openBrowser bool) (codexOAuthLoginSession, string, error) {
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

	session, err := a.api.StartCodexOAuth(ctx, client.CodexOAuthStartRequest{
		Provider: provider,
		Label:    label,
		Active:   active,
		Method:   "manual",
	})
	if err != nil {
		return codexOAuthLoginSession{}, "", err
	}

	authURL := strings.TrimSpace(session.AuthURL)
	if authURL == "" {
		return codexOAuthLoginSession{}, "", errors.New("codex oauth start returned empty auth url")
	}
	browserWarning := ""
	if openBrowser && authURL != "" {
		if err := tryOpenBrowser(authURL); err != nil {
			browserWarning = fmt.Sprintf("Browser did not open automatically: %v", err)
		}
	}
	return codexOAuthLoginSession{
		Provider:  provider,
		Label:     label,
		Active:    active,
		SessionID: strings.TrimSpace(session.SessionID),
		AuthURL:   authURL,
	}, browserWarning, nil
}

func codexBrowserPendingStatus(browserWarning string) string {
	status := "Finish Codex sign-in in your browser. This modal will close automatically after confirmation."
	if strings.TrimSpace(browserWarning) != "" {
		status += " " + strings.TrimSpace(browserWarning)
	}
	return status
}

func (a *App) runCodexOAuthLogin(ctx context.Context, session codexOAuthLoginSession) authLoginResult {
	callbackInput, err := waitForLocalCodexOAuthCallback(ctx, oauthStateFromAuthURL(session.AuthURL))
	if err != nil {
		return authLoginResult{err: err, clearCodexPending: true}
	}
	completedSession, err := a.api.CompleteCodexOAuth(ctx, client.CodexOAuthCompleteRequest{
		SessionID:     session.SessionID,
		CallbackInput: callbackInput,
	})
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
	a.home.HideSandboxModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.SetWorkspaceModalDirectory(a.activeContextPath())
	a.home.ShowWorkspaceModal()
	return a.loadWorkspaceModalEntries("Loading workspace manager...")
}

func (a *App) openSandboxModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowSandboxModal()
	a.refreshSandboxModalData("Loading sandbox status...", false)
}

func (a *App) openWorktreesModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideSandboxModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowWorktreesModal()
	a.refreshWorktreesModalData("Loading worktrees settings...")
}

func (a *App) openMCPModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideSandboxModal()
	a.home.HideWorktreesModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowMCPModal()
	a.refreshMCPModalData("Loading MCP servers...")
}

func (a *App) refreshMCPModalData(statusHint string) {
	if !a.home.MCPModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetMCPModalStatus(statusHint)
	}
	a.home.SetMCPModalLoading(true)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	servers, err := a.api.ListMCPServers(ctx, 500)
	if err != nil {
		a.home.SetMCPModalLoading(false)
		a.home.SetMCPModalError(fmt.Sprintf("mcp list failed: %v", err))
		return
	}
	a.home.SetMCPModalData(mapMCPModalServers(servers))
	a.home.SetMCPModalLoading(false)
	a.home.SetMCPModalStatus(fmt.Sprintf("mcp servers loaded: %d", len(servers)))
}

func (a *App) refreshSandboxModalData(statusHint string, forcePreflight bool) {
	if !a.home.SandboxModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetSandboxModalStatus(statusHint)
	}
	a.home.SetSandboxModalLoading(true)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var (
		status client.SandboxStatus
		err    error
	)
	if forcePreflight {
		status, err = a.api.PreflightSandbox(ctx)
	} else {
		status, err = a.api.GetSandboxStatus(ctx)
	}
	if err != nil {
		a.home.SetSandboxModalLoading(false)
		a.home.SetSandboxModalError(fmt.Sprintf("sandbox status failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("sandbox status failed: %v", err))
		return
	}
	a.home.SetSandboxModalData(mapSandboxModalData(status))
	a.home.SetSandboxModalLoading(false)
	a.home.SetSandboxModalStatus(fmt.Sprintf("sandbox %s (ready=%t)", onOffLabel(status.Enabled), status.Ready))
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
	a.home.SetWorktreesModalStatus(a.worktreesStatusSummary(settings))
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

func (a *App) openAuthModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideSandboxModal()
	a.home.HideWorktreesModal()
	a.home.HideWorkspaceModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowAuthModal()
	a.refreshAuthModalData("Loading auth manager...")
}

func (a *App) openAgentsModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideSandboxModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowAgentsModal()
	a.refreshAgentsModalData("Loading agent profiles...")
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

	state, err := a.api.ListAgents(ctx, 500)
	if err != nil {
		a.home.SetAgentsModalLoading(false)
		a.home.SetAgentsModalError(fmt.Sprintf("agent list failed: %v", err))
		return
	}
	hints := make([]string, 0, len(state.Profiles)+2)
	hints = append(hints, a.homeModel.ModelProvider)
	for _, profile := range state.Profiles {
		hints = append(hints, profile.Provider)
	}
	resolvedModels := a.resolveProviderModelData(ctx, hints, 2000, 1200)

	a.home.SetAgentsModalData(mapAgentsModalData(
		state,
		resolvedModels,
		strings.TrimSpace(a.homeModel.ModelProvider),
		strings.TrimSpace(a.homeModel.ModelName),
		strings.TrimSpace(a.homeModel.ThinkingLevel),
	))
	a.home.SetAgentsModalLoading(false)
	status := fmt.Sprintf("agent profiles loaded: %d", len(state.Profiles))
	if len(resolvedModels.Warnings) > 0 {
		status += " (" + strings.Join(uniqueNonEmpty(resolvedModels.Warnings), "; ") + ")"
	}
	a.home.SetAgentsModalStatus(status)
}

func (a *App) refreshAuthModalData(statusHint string) {
	if !a.home.AuthModalVisible() {
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

func mapAgentsModalData(state client.AgentState, resolved providerModelResolverResult, defaultProvider, defaultModel, defaultThinking string) ui.AgentsModalData {
	profiles := make([]ui.AgentModalProfile, 0, len(state.Profiles))
	modelsByProvider := make(map[string][]string, len(resolved.ModelsByProvider)+8)
	for providerID, models := range resolved.ModelsByProvider {
		providerID = normalizeModelProviderID(providerID)
		if providerID == "" {
			continue
		}
		modelsByProvider[providerID] = append([]string(nil), models...)
	}
	reasoningModels := make(map[string]bool, len(resolved.ReasoningByKey)+32)
	for key, enabled := range resolved.ReasoningByKey {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		reasoningModels[key] = enabled
	}

	providerSet := make(map[string]struct{}, len(resolved.ProviderIDs)+8)
	for _, providerID := range resolved.ProviderIDs {
		providerID = normalizeModelProviderID(providerID)
		if providerID != "" {
			providerSet[providerID] = struct{}{}
		}
	}

	for _, profile := range state.Profiles {
		providerID := normalizeModelProviderID(profile.Provider)
		modelID := strings.TrimSpace(profile.Model)
		if providerID != "" {
			providerSet[providerID] = struct{}{}
			if modelID != "" && modelAllowedByProviderPreset(providerID, modelID) {
				modelsByProvider[providerID] = append(modelsByProvider[providerID], modelID)
				reasonKey := modelEntryKey(providerID, modelID)
				if reasonKey != "" {
					if _, ok := reasoningModels[reasonKey]; !ok {
						reasoningModels[reasonKey] = true
					}
				}
			}
		}
		profiles = append(profiles, ui.AgentModalProfile{
			Name:             strings.TrimSpace(profile.Name),
			Mode:             strings.TrimSpace(profile.Mode),
			Description:      strings.TrimSpace(profile.Description),
			Provider:         providerID,
			Model:            modelID,
			Thinking:         strings.TrimSpace(profile.Thinking),
			Prompt:           strings.TrimSpace(profile.Prompt),
			ExecutionSetting: strings.TrimSpace(profile.ExecutionSetting),
			Enabled:          profile.Enabled,
			UpdatedAt:        profile.UpdatedAt,
		})
	}

	providers := make([]string, 0, len(providerSet))
	for providerID := range providerSet {
		providers = append(providers, providerID)
	}
	sort.Strings(providers)
	for _, providerID := range providers {
		for _, preset := range modelPresetListForProvider(providerID) {
			modelID := strings.TrimSpace(preset)
			if modelID == "" {
				continue
			}
			modelsByProvider[providerID] = append(modelsByProvider[providerID], modelID)
			reasonKey := modelEntryKey(providerID, modelID)
			if reasonKey != "" {
				if _, ok := reasoningModels[reasonKey]; !ok {
					reasoningModels[reasonKey] = true
				}
			}
		}
	}
	for providerID, models := range modelsByProvider {
		modelsByProvider[providerID] = dedupeModelValues(models)
		defaultModelForProvider := ""
		if status, ok := resolved.ProviderStatuses[providerID]; ok {
			defaultModelForProvider = strings.TrimSpace(status.DefaultModel)
		}
		sort.SliceStable(modelsByProvider[providerID], func(i, j int) bool {
			left := strings.TrimSpace(modelsByProvider[providerID][i])
			right := strings.TrimSpace(modelsByProvider[providerID][j])
			if defaultModelForProvider != "" {
				leftIsDefault := strings.EqualFold(left, defaultModelForProvider)
				rightIsDefault := strings.EqualFold(right, defaultModelForProvider)
				if leftIsDefault != rightIsDefault {
					return leftIsDefault
				}
			}
			return modelIDLessForProvider(providerID, left, right)
		})
	}

	sort.SliceStable(profiles, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(profiles[i].Mode))
		right := strings.ToLower(strings.TrimSpace(profiles[j].Mode))
		if left != right {
			if left == "primary" {
				return true
			}
			if right == "primary" {
				return false
			}
		}
		return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
	})

	activeSubagent := make(map[string]string, len(state.ActiveSubagent))
	for role, name := range state.ActiveSubagent {
		role = strings.ToLower(strings.TrimSpace(role))
		name = strings.ToLower(strings.TrimSpace(name))
		if role == "" || name == "" {
			continue
		}
		activeSubagent[role] = name
	}

	defaultProvider = normalizeModelProviderID(defaultProvider)
	if defaultProvider != "" && !stringInSlice(providers, defaultProvider) {
		defaultProvider = ""
	}
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultProvider == "" {
		defaultModel = ""
	} else if !hasModelValue(modelsByProvider[defaultProvider], defaultModel) {
		if status, ok := resolved.ProviderStatuses[defaultProvider]; ok {
			candidate := strings.TrimSpace(status.DefaultModel)
			if hasModelValue(modelsByProvider[defaultProvider], candidate) {
				defaultModel = candidate
			}
		}
		if !hasModelValue(modelsByProvider[defaultProvider], defaultModel) && len(modelsByProvider[defaultProvider]) > 0 {
			defaultModel = modelsByProvider[defaultProvider][0]
		}
	}
	if defaultProvider != "" && defaultModel != "" {
		reasonKey := modelEntryKey(defaultProvider, defaultModel)
		if reasonKey != "" {
			if _, ok := reasoningModels[reasonKey]; !ok {
				reasoningModels[reasonKey] = true
			}
		}
	}

	defaultThinking = strings.ToLower(strings.TrimSpace(defaultThinking))
	if defaultThinking == "" {
		defaultThinking = "xhigh"
	}

	data := ui.AgentsModalData{
		Profiles:         profiles,
		ActivePrimary:    strings.TrimSpace(state.ActivePrimary),
		ActiveSubagent:   activeSubagent,
		Version:          state.Version,
		Providers:        providers,
		ModelsByProvider: modelsByProvider,
		ReasoningModels:  reasoningModels,
		DefaultProvider:  defaultProvider,
		DefaultModel:     defaultModel,
		DefaultThinking:  defaultThinking,
	}
	if state.ProviderDefaultsPreview != nil {
		preview := state.ProviderDefaultsPreview
		data.UtilityProvider = emptyFallback(strings.TrimSpace(preview.UtilityProvider), strings.TrimSpace(preview.Provider))
		data.UtilityModel = strings.TrimSpace(preview.UtilityModel)
		data.UtilityThinking = strings.TrimSpace(preview.UtilityThinking)
		data.UtilityAgents = append([]string(nil), preview.UtilityAgents...)
		data.CustomUtilityAgents = append([]string(nil), preview.CustomUtilityAgents...)
		data.UtilityBaselineAgents = append([]string(nil), preview.UtilityBaselineAgents...)
		data.StaleInheritedAgents = append([]string(nil), preview.StaleInheritedAgents...)
	}
	return data
}

func hasModelValue(models []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model), target) {
			return true
		}
	}
	return false
}

func (a *App) handleWorkspaceCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") || strings.EqualFold(args[0], "manage") || strings.EqualFold(args[0], "crud") {
		a.showWorkspaceManager()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
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
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /workspace select <name|#n>")
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
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("workspace active: %s", resolution.ResolvedPath))
		a.activePath = strings.TrimSpace(resolution.ResolvedPath)
		a.workspacePath = strings.TrimSpace(resolution.WorkspacePath)
		a.syncActiveWorkspaceSelection(resolution)
		a.queueReload(false)
	case "tree", "find", "scan":
		query := strings.TrimSpace(strings.Join(args[1:], " "))
		a.scanWorkspaceTree(query)
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /workspace [open|save|select|scan]")
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

func (a *App) handleMCPCommand(args []string) {
	a.home.ClearCommandOverlay()
	a.home.SetStatus("MCP management is deferred until Swarm Sync integration; Exa search can use the built-in free Exa MCP server")
}

func (a *App) handleSandboxCommand(args []string) {
	if a.home == nil {
		return
	}
	a.openSandboxModal()
	if len(args) == 0 {
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "status", "open":
		a.refreshSandboxModalData("Running sandbox preflight...", true)
	case "on", "enable":
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		status, err := a.api.SetSandboxEnabled(ctx, true)
		a.home.SetSandboxModalData(mapSandboxModalData(status))
		if err != nil {
			a.home.SetSandboxModalError(fmt.Sprintf("sandbox enable failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("sandbox enable failed: %v", err))
			return
		}
		a.home.SetSandboxModalStatus("Sandbox ON")
		a.showToast(ui.ToastSuccess, "Sandbox ON (global)")
	case "off", "disable":
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		status, err := a.api.SetSandboxEnabled(ctx, false)
		a.home.SetSandboxModalData(mapSandboxModalData(status))
		if err != nil {
			a.home.SetSandboxModalError(fmt.Sprintf("sandbox disable failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("sandbox disable failed: %v", err))
			return
		}
		a.home.SetSandboxModalStatus("Sandbox OFF")
		a.showToast(ui.ToastSuccess, "Sandbox OFF (global)")
	default:
		a.home.SetSandboxModalStatus("usage: /sandbox [on|off|status]")
	}
}

func (a *App) handleWorktreesCommand(args []string) {
	if a.home == nil {
		return
	}
	if len(args) == 0 {
		a.openWorktreesModal()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "open":
		a.openWorktreesModal()
	case "status":
		a.home.ClearCommandOverlay()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		settings, err := a.api.GetWorktreeSettings(ctx, a.activeContextPath())
		if err != nil {
			message := fmt.Sprintf("worktrees status failed: %v", err)
			a.home.SetStatus(message)
			a.showToast(ui.ToastError, message)
			return
		}
		message := a.worktreesStatusSummary(settings)
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
		if !a.home.WorktreesModalVisible() {
			a.showToast(ui.ToastInfo, message)
		}
	case "on", "enable":
		a.home.ClearCommandOverlay()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		enabled := true
		useCurrentBranch := true
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{WorkspacePath: a.activeContextPath(), Enabled: &enabled, UseCurrentBranch: &useCurrentBranch})
		if err != nil {
			message := fmt.Sprintf("worktrees enable failed: %v", err)
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalError(message)
			}
			a.home.SetStatus(message)
			a.showToast(ui.ToastError, message)
			return
		}
		message := a.worktreesStatusSummary(settings)
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
		a.showToast(ui.ToastSuccess, message)
	case "off", "disable":
		a.home.ClearCommandOverlay()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		enabled := false
		useCurrentBranch := true
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{WorkspacePath: a.activeContextPath(), Enabled: &enabled, UseCurrentBranch: &useCurrentBranch})
		if err != nil {
			message := fmt.Sprintf("worktrees disable failed: %v", err)
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalError(message)
			}
			a.home.SetStatus(message)
			a.showToast(ui.ToastError, message)
			return
		}
		message := a.worktreesStatusSummary(settings)
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
		a.showToast(ui.ToastSuccess, message)
	case "branch", "base":
		a.home.ClearCommandOverlay()
		if len(args) < 2 {
			message := "usage: /worktrees branch <name|current>"
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalStatus(message)
			}
			a.home.SetStatus(message)
			return
		}
		targetBranch := strings.TrimSpace(strings.Join(args[1:], " "))
		baseBranch, useCurrentBranch := normalizeWorktreeSettingsBranchInput(targetBranch)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{WorkspacePath: a.activeContextPath(), UseCurrentBranch: &useCurrentBranch, BaseBranch: baseBranch})
		if err != nil {
			message := fmt.Sprintf("worktrees branch-off source update failed: %v", err)
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalError(message)
			}
			a.home.SetStatus(message)
			a.showToast(ui.ToastError, message)
			return
		}
		message := a.worktreesStatusSummary(settings)
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
		a.showToast(ui.ToastSuccess, message)
	case "created-branch":
		a.home.ClearCommandOverlay()
		if len(args) < 2 {
			message := "usage: /worktrees created-branch <prefix>"
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalStatus(message)
			}
			a.home.SetStatus(message)
			return
		}
		branchName := normalizeWorktreeBranchPrefix(strings.TrimSpace(strings.Join(args[1:], " ")))
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		settings, err := a.api.UpdateWorktreeSettings(ctx, client.WorktreeSettingsUpdateRequest{WorkspacePath: a.activeContextPath(), BranchName: stringPtr(branchName)})
		if err != nil {
			message := fmt.Sprintf("worktrees created branch update failed: %v", err)
			if a.home.WorktreesModalVisible() {
				a.home.SetWorktreesModalError(message)
			}
			a.home.SetStatus(message)
			a.showToast(ui.ToastError, message)
			return
		}
		message := a.worktreesStatusSummary(settings)
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalData(mapWorktreesModalData(settings, a.currentWorktreeResolvedBranch()))
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
		a.showToast(ui.ToastSuccess, message)
	default:
		message := "usage: /worktrees [on|off|status|branch <name|current>|created-branch <prefix>]"
		if a.home.WorktreesModalVisible() {
			a.home.SetWorktreesModalStatus(message)
		}
		a.home.SetStatus(message)
	}
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

func (a *App) showWorkspaceManager() {
	a.home.SetWorkspaceModalIntent("", "")
	_, _ = a.openWorkspaceModal()
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

func (a *App) showMCPManager() {
	a.openMCPModal()
}

func (a *App) showAgentsManager() {
	a.openAgentsModal()
}

func (a *App) handleAgentsCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") || strings.EqualFold(args[0], "manage") || strings.EqualFold(args[0], "crud") || strings.EqualFold(args[0], "list") {
		a.showAgentsManager()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "default", "defaults", "restore":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := a.api.RestoreAgentDefaults(ctx)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("restore defaults failed: %v", err))
			return
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("restored default agents: %d profiles", len(result.Profiles)))
		a.queueReload(false)
		if a.home.AgentsModalVisible() {
			a.refreshAgentsModalData("")
		}
	case "reset":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := a.api.ResetAgentDefaults(ctx)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("reset defaults failed: %v", err))
			return
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("reset agents to built-in defaults: %d profiles", len(result.Profiles)))
		a.queueReload(false)
		if a.home.AgentsModalVisible() {
			a.refreshAgentsModalData("")
		}
	case "use":
		if len(args) < 2 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /agents use <primary-agent>")
			return
		}
		target := strings.TrimSpace(args[1])
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		active, _, err := a.api.ActivatePrimaryAgent(ctx, target)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("agent activate failed: %v", err))
			return
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("active primary agent: %s", emptyFallback(active, target)))
		a.queueReload(false)
		a.syncChatAgentRuntime()
		if a.home.AgentsModalVisible() {
			a.refreshAgentsModalData("")
		}
	case "prompt":
		if len(args) < 3 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /agents prompt <name> <prompt>")
			return
		}
		name := strings.TrimSpace(args[1])
		prompt := strings.TrimSpace(strings.Join(args[2:], " "))
		if prompt == "" {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /agents prompt <name> <prompt>")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		profile, _, err := a.api.UpsertAgent(ctx, client.AgentUpsertRequest{
			Name:   name,
			Prompt: prompt,
		})
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("agent prompt update failed: %v", err))
			return
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("agent prompt updated: %s", profile.Name))
		a.queueReload(false)
		if a.home.AgentsModalVisible() {
			a.refreshAgentsModalData("")
		}
	case "delete", "remove":
		if len(args) < 2 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /agents delete <name>")
			return
		}
		target := strings.TrimSpace(args[1])
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		deleted, activePrimary, _, err := a.api.DeleteAgent(ctx, target)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("agent delete failed: %v", err))
			return
		}
		a.home.ClearCommandOverlay()
		if strings.TrimSpace(activePrimary) == "" {
			activePrimary = "swarm"
		}
		a.home.SetStatus(fmt.Sprintf("agent deleted: %s (active primary: %s)", emptyFallback(deleted, target), activePrimary))
		a.queueReload(false)
		if a.home.AgentsModalVisible() {
			a.refreshAgentsModalData("")
		}
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /agents [open|restore|reset|use|prompt|delete]")
	}
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
		next, err := a.refreshHomeModel(ctx)
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
		if result.err != nil {
			if !result.silent {
				a.home.SetStatus(fmt.Sprintf("reload failed: %v", result.err))
			}
			return
		}
		a.syncActiveContextFromHomeModel(result.model)
		a.applyHomeModel(result.model)
		a.syncVaultUI()
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
		exitPlanMode := true
		if profile.ExitPlanModeEnabled != nil {
			exitPlanMode = *profile.ExitPlanModeEnabled
		}
		return active, strings.TrimSpace(profile.ExecutionSetting), exitPlanMode, true
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

func (a *App) applyHomeModel(next model.HomeModel) {
	a.homeModel = next
	a.home.SetModel(next)
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

func (a *App) refreshHomeModel(ctx context.Context) (model.HomeModel, error) {
	next := model.EmptyHome()
	next.ServerURL = a.api.BaseURL()
	contextPath := normalizePath(a.activeContextPath())
	if contextPath == "" {
		contextPath = normalizePath(a.startupCWD)
	}
	next.CWD = contextPath
	if next.CWD == "" {
		next.CWD = "."
	}

	errorsSeen := make([]string, 0, 8)

	if strings.TrimSpace(a.api.Token()) == "" {
		if err := a.api.EnsureLocalAuth(ctx); err != nil {
			errorsSeen = append(errorsSeen, "local auth bootstrap failed")
		}
	}

	vaultStatus, vaultErr := a.api.GetVaultStatus(ctx)
	if vaultErr == nil {
		a.vault = vaultStatus
		if vaultStatus.Enabled && !vaultStatus.Unlocked {
			next.HintLine = "Vault is locked. Unlock it before using Swarm."
			next.TipLine = "/vault"
			return next, nil
		}
	} else {
		errorsSeen = append(errorsSeen, "vault status unavailable")
	}

	health, err := a.api.GetHealth(ctx)
	if err == nil {
		if mode := strings.TrimSpace(health.Mode); mode != "" {
			next.ServerMode = mode
		}
		next.BypassPermissions = health.BypassPermissions
	} else {
		errorsSeen = append(errorsSeen, "daemon status unavailable")
	}

	worktreeSettings, err := a.api.GetWorktreeSettings(ctx, next.CWD)
	if err == nil {
		next.WorktreesEnabled = worktreeSettings.Enabled
	} else {
		errorsSeen = append(errorsSeen, "worktrees settings unavailable")
	}

	overview, err := a.api.WorkspaceOverview(ctx, next.CWD, nil, homeWorkspaceOverviewSessionLimit)
	activePath := normalizePath(strings.TrimSpace(next.CWD))
	activeIsWorkspace := false
	activeIsWorkspaceRoot := false
	if err == nil {
		selectedWorkspacePath := ""
		if overview.CurrentWorkspace != nil {
			selectedWorkspacePath = normalizePath(strings.TrimSpace(overview.CurrentWorkspace.WorkspacePath))
			if selectedWorkspacePath == "" {
				selectedWorkspacePath = normalizePath(strings.TrimSpace(overview.CurrentWorkspace.ResolvedPath))
			}
		}
		seenDirectories := make(map[string]struct{}, len(overview.Workspaces)+len(overview.Directories))
		for i, entry := range overview.Workspaces {
			entryPath := normalizePath(entry.Path)
			if entryPath == "" {
				continue
			}
			name := strings.TrimSpace(entry.WorkspaceName)
			if name == "" {
				name = filepath.Base(entryPath)
			}
			if name == "" || name == "." || name == string(filepath.Separator) {
				name = "workspace"
			}
			directories := append([]string(nil), entry.Directories...)
			if len(directories) == 0 {
				directories = []string{entryPath}
			}
			next.Workspaces = append(next.Workspaces, model.Workspace{
				Name:        name,
				Path:        entryPath,
				Directories: directories,
				ThemeID:     strings.TrimSpace(entry.ThemeID),
				Icon:        workspaceIcon(i),
			})
			next.Directories = append(next.Directories, model.DirectoryItem{
				Name:         name,
				Path:         displayPath(entryPath),
				ResolvedPath: entryPath,
				Branch:       "-",
				DirtyCount:   0,
				AgentsToken:  "none",
				IsWorkspace:  true,
			})
			seenDirectories[entryPath] = struct{}{}
		}
		preferredWorkspacePath := selectedWorkspacePath
		if preferredWorkspacePath == "" {
			preferredWorkspacePath = normalizePath(strings.TrimSpace(a.workspacePath))
		}
		activeWorkspacePath := resolveWorkspaceSelectionPath(activePath, next.Workspaces, preferredWorkspacePath)
		activeIsWorkspace = activeWorkspacePath != ""
		activeIsWorkspaceRoot = activeWorkspacePath != "" && pathsEqual(activePath, activeWorkspacePath)
		for i := range next.Workspaces {
			next.Workspaces[i].Active = activeWorkspacePath != "" && pathsEqual(next.Workspaces[i].Path, activeWorkspacePath)
		}
		for _, entry := range overview.Directories {
			entryPath := normalizePath(entry.Path)
			if entryPath == "" {
				continue
			}
			if _, exists := seenDirectories[entryPath]; exists {
				continue
			}
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				name = directoryNameForPath(entryPath)
			}
			next.Directories = append(next.Directories, model.DirectoryItem{
				Name:         name,
				Path:         displayPath(entryPath),
				ResolvedPath: entryPath,
				Branch:       "-",
				DirtyCount:   0,
				AgentsToken:  "none",
				IsWorkspace:  false,
			})
			seenDirectories[entryPath] = struct{}{}
		}
	} else {
		errorsSeen = append(errorsSeen, "workspace overview unavailable")
	}

	gitStatus, _ := gitStatusForPath(activePath)
	if activeIsWorkspace {
		matched := false
		for i := range next.Directories {
			if pathsEqual(next.Directories[i].ResolvedPath, activePath) {
				next.Directories[i].IsWorkspace = activeIsWorkspaceRoot
				applyGitStatusToDirectory(&next.Directories[i], gitStatus)
				matched = true
				break
			}
		}
		if !matched && activePath != "" {
			next.Directories = append([]model.DirectoryItem{
				newDirectoryItemWithGitStatus(activePath, activeIsWorkspaceRoot, gitStatus),
			}, next.Directories...)
		}
	} else {
		next.Directories = append([]model.DirectoryItem{
			newDirectoryItemWithGitStatus(activePath, false, gitStatus),
		}, next.Directories...)
	}

	providerStatuses, err := a.api.ListProviders(ctx)
	runnableProviders := 0
	if err == nil {
		for _, status := range providerStatuses {
			id := strings.ToLower(strings.TrimSpace(status.ID))
			if id == "" {
				continue
			}
			if status.Runnable {
				runnableProviders++
			}
		}
		next.AuthConfigured = runnableProviders > 0
	} else {
		errorsSeen = append(errorsSeen, "provider status unavailable")
	}

	modelResolved, err := a.api.GetModel(ctx)
	if err == nil {
		next = applyHomeModelResolved(next, modelResolved)
	} else {
		errorsSeen = append(errorsSeen, "model preference unavailable")
	}

	updateStatus, updateErr := a.api.GetUpdateStatus(ctx)
	if updateErr == nil {
		next.UpdateStatus = &updateStatus
		if current := strings.TrimSpace(updateStatus.CurrentVersion); current != "" {
			next.Version = current
		}
		a.updateStatus = updateStatus
	} else if strings.TrimSpace(buildinfo.DisplayVersion()) != "dev" {
		errorsSeen = append(errorsSeen, "update status unavailable")
	}

	if providerID := strings.ToLower(strings.TrimSpace(next.ModelProvider)); providerID != "" {
		credentials, credErr := a.api.ListAuthCredentials(ctx, providerID, "", 50)
		if credErr == nil {
			for _, record := range credentials.Records {
				if record.Active {
					next.AuthType = strings.TrimSpace(record.AuthType)
					next.AuthLast4 = strings.TrimSpace(record.Last4)
					break
				}
			}
		}
	}

	agentState, err := a.api.ListAgents(ctx, 200)
	if err == nil {
		next.ActiveAgent, next.ActiveAgentExecutionSetting, next.ActiveAgentExitPlanMode, next.ActiveAgentRuntimeKnown = activeAgentRuntime(agentState)
		next.Subagents = chatMentionSubagentNames(agentState)
	} else {
		next.ActiveAgent = "swarm"
		next.ActiveAgentExecutionSetting = ""
		next.ActiveAgentExitPlanMode = true
		next.ActiveAgentRuntimeKnown = true
		errorsSeen = append(errorsSeen, "agent state unavailable")
	}

	contextReport, err := a.api.ContextSources(ctx, contextPath)
	if err == nil {
		next.RuleCount = len(contextReport.Rules)
		next.SkillCount = len(contextReport.Skills)
		agentsToken := contextAgentsToken(contextReport.Rules)
		for i := range next.Directories {
			if pathsEqual(next.Directories[i].ResolvedPath, contextPath) {
				next.Directories[i].AgentsToken = agentsToken
			}
		}
	} else {
		errorsSeen = append(errorsSeen, "context scan unavailable")
	}

	// Home does not render usage summaries, so avoid one /usage request per
	// session during startup and keep the initial recent-session slice small.
	sessions, sessionsErr := a.api.ListSessionsForExactCWD(ctx, homeRecentSessionLimit, contextPath)
	if sessionsErr == nil {
		modelSessions := make([]model.SessionSummary, 0, len(sessions))
		for _, session := range sessions {
			modelSessions = append(modelSessions, modelSessionSummaryFromClient(session))
		}
		for _, session := range applySessionDepths(modelSessions) {
			title := strings.TrimSpace(session.Title)
			if title == "" {
				title = session.ID
			}
			session.Title = title
			next.RecentSessions = append(next.RecentSessions, session)
		}
	} else {
		errorsSeen = append(errorsSeen, "session list unavailable")
	}

	next.QuickActions = homeQuickActions(next)

	directoryMode := activeWorkspaceIndex(next.Workspaces) < 0
	if directoryMode && !next.AuthConfigured {
		next.HintLine = "Directory mode and auth is missing, run /auth"
		next.TipLine = "/workspace  •  /auth"
	} else if directoryMode {
		next.HintLine = "Directory mode (no active workspace)"
		next.TipLine = "/workspace"
	} else if !next.AuthConfigured {
		next.HintLine = "Auth is missing, run /auth"
		next.TipLine = "/auth"
	} else {
		next.HintLine = "ctrl+down enters sessions • ctrl+up exits sessions"
		next.TipLine = ""
	}

	next.HintLine = strings.TrimSpace(next.HintLine)

	if len(errorsSeen) > 0 {
		preview := strings.Join(errorsSeen, "; ")
		if len(preview) > 96 {
			preview = preview[:96] + "..."
		}
		if strings.TrimSpace(next.HintLine) == "" {
			next.HintLine = fmt.Sprintf("degraded: %s", preview)
		} else {
			next.HintLine = fmt.Sprintf("%s • degraded: %s", next.HintLine, preview)
		}
	}

	cycleLabel := "Shift+Tab"
	if keybinds := a.activeKeyBindings(); keybinds != nil {
		label := strings.TrimSpace(keybinds.Label(ui.KeybindChatCycleMode))
		if label != "" {
			cycleLabel = label
		}
	}
	modeHint := fmt.Sprintf("%s cycle mode", cycleLabel)
	if next.HintLine == "" {
		next.HintLine = modeHint
	} else if !strings.Contains(strings.ToLower(next.HintLine), "cycle mode") {
		next.HintLine = next.HintLine + " • " + modeHint
	}
	if len(errorsSeen) > 0 && len(next.Workspaces) == 0 && strings.TrimSpace(next.ModelName) == "" && len(next.RecentSessions) == 0 {
		return next, errors.New(strings.Join(errorsSeen, "; "))
	}
	return next, nil
}

func homeUpdateVersionHint(status *client.UpdateStatus) string {
	if status == nil || !status.UpdateAvailable {
		return ""
	}
	return strings.TrimSpace(status.LatestVersion)
}

func (a *App) announceStartupUpdate(next model.HomeModel) {
	if a == nil {
		return
	}
	status := next.UpdateStatus
	if status == nil || !status.UpdateAvailable {
		return
	}
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
	if next.CWD != "" {
		a.activePath = normalizePath(strings.TrimSpace(next.CWD))
	}
	a.workspacePath = ""
	for _, ws := range next.Workspaces {
		if ws.Active {
			a.workspacePath = normalizePath(strings.TrimSpace(ws.Path))
			break
		}
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

func workspaceEntryMatchDepth(entry client.WorkspaceOverviewWorkspace, target string) int {
	best := workspacePathMatchDepth(entry.Path, target)
	for _, root := range entry.Directories {
		if depth := workspacePathMatchDepth(root, target); depth > best {
			best = depth
		}
	}
	return best
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
	if a.home != nil {
		a.home.SetModel(a.homeModel)
	}
	a.refreshGitRealtimeWatcher()
	a.applyEffectiveTheme()
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
	if a.gitWatcher != nil && pathsEqual(a.gitWatcher.path, target) {
		return
	}
	a.stopGitRealtimeWatcher()
	a.startGitRealtimeWatcher(target)
}

func (a *App) startGitRealtimeWatcher(path string) {
	if a == nil {
		return
	}
	target := normalizePath(path)
	if target == "" {
		return
	}
	watcher, err := newRepoGitWatcher(target)
	if err != nil {
		return
	}
	generation := a.gitWatchGeneration.Add(1)
	a.gitWatcher = watcher
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

func (a *App) stopGitRealtimeWatcher() {
	if a == nil || a.gitWatcher == nil {
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
	a.workspacePath = workspacePath
	if resolvedPath != "" {
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

func (a *App) workspaceCycleHotkeyBlocked() bool {
	if a.route != "home" {
		if a.route == "chat" {
			message := "To change workspace, do /new or go to the home screen (Ctrl+B)"
			if a.chat != nil {
				a.chat.SetStatus("")
			}
			a.showToast(ui.ToastInfo, message)
			return true
		}
		return true
	}
	return a.home.AuthModalVisible() ||
		a.home.AuthDefaultsInfoVisible() ||
		a.home.SessionsModalVisible() ||
		a.home.WorkspaceModalVisible() ||
		a.home.SandboxModalVisible() ||
		a.home.WorktreesModalVisible() ||
		a.home.ModelsModalVisible() ||
		a.home.AgentsModalVisible() ||
		a.home.VoiceModalVisible() ||
		a.home.ThemeModalVisible() ||
		a.home.KeybindsModalVisible()
}

func (a *App) cycleWorkspaceBy(delta int) {
	if delta == 0 {
		return
	}
	workspaces := a.homeModel.Workspaces
	if len(workspaces) == 0 {
		a.home.SetStatus("no saved workspaces to cycle")
		return
	}
	current := activeWorkspaceIndex(workspaces)
	next := 0
	if current < 0 {
		if delta < 0 {
			next = len(workspaces) - 1
		}
	} else {
		next = (current + delta + len(workspaces)) % len(workspaces)
	}
	a.activateWorkspaceAtIndex(next)
}

func (a *App) activateWorkspaceSlot(slot int) {
	if slot < 1 || slot > len(a.homeModel.Workspaces) {
		a.home.SetStatus(fmt.Sprintf("workspace slot %d is empty", slot))
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resolution, err := a.api.SelectWorkspace(ctx, target)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("workspace switch failed: %v", err))
		return
	}
	a.activePath = strings.TrimSpace(resolution.ResolvedPath)
	a.workspacePath = strings.TrimSpace(resolution.WorkspacePath)
	a.syncActiveWorkspaceSelection(resolution)
	a.home.SetStatus(fmt.Sprintf("workspace active: %s", resolution.WorkspaceName))
	a.queueReload(false)
}

func (a *App) userFacingSessionPath(workspacePath string, worktreeEnabled bool, worktreeRootPath string) string {
	if worktreeEnabled {
		if root := strings.TrimSpace(worktreeRootPath); root != "" {
			return displayPath(root)
		}
	}
	if root := inferWorktreeRepoRoot(workspacePath); root != "" {
		return displayPath(root)
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
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if root := inferWorktreeRepoRoot(trimmed); root != "" {
		return root
	}
	return trimmed
}

func inferWorktreeRepoRoot(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" {
		return ""
	}
	needle := string(filepath.Separator) + ".swarm" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	index := strings.Index(clean, needle)
	if index <= 0 {
		return ""
	}
	root := strings.TrimSpace(clean[:index])
	if root == "" {
		return ""
	}
	return root
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
	hasClaude := false
	for _, rule := range rules {
		switch strings.ToLower(strings.TrimSpace(rule.Name)) {
		case "agents.md":
			hasAgents = true
		case "claude.md":
			hasClaude = true
		}
	}

	switch {
	case hasAgents && hasClaude:
		return "agents+claude"
	case hasAgents:
		return "agents"
	case hasClaude:
		return "claude"
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
		return []string{
			"Agent: " + emptyFallback(next.ActiveAgent, "swarm"),
			"Auth: missing",
			"Run /auth",
		}
	}
	actions := []string{
		"Agent: " + emptyFallback(next.ActiveAgent, "swarm"),
		"Model: " + homeModelDisplayLabel(next),
		"Thinking: " + emptyFallback(next.ThinkingLevel, "unset"),
	}
	return actions
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

func homeModelDisplayLabel(next model.HomeModel) string {
	return model.DisplayModelLabel(next.ModelProvider, next.ModelName, next.ServiceTier, next.ContextMode)
}

func mapSandboxModalData(status client.SandboxStatus) ui.SandboxModalData {
	checks := make([]ui.SandboxModalCheck, 0, len(status.Checks))
	for _, check := range status.Checks {
		checks = append(checks, ui.SandboxModalCheck{
			Name:   strings.TrimSpace(check.Name),
			OK:     check.OK,
			Detail: strings.TrimSpace(check.Detail),
		})
	}
	remediation := make([]string, 0, len(status.Remediation))
	for _, line := range status.Remediation {
		remediation = append(remediation, strings.TrimSpace(line))
	}
	return ui.SandboxModalData{
		Enabled:      status.Enabled,
		UpdatedAt:    status.UpdatedAt,
		Ready:        status.Ready,
		Summary:      strings.TrimSpace(status.Summary),
		Checks:       checks,
		Remediation:  remediation,
		SetupCommand: strings.TrimSpace(status.SetupCommand),
	}
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

func worktreeStatusLabel(settings client.WorktreeSettings) string {
	createdBranch := normalizeWorktreeBranchPrefix(strings.TrimSpace(settings.BranchName))
	if createdBranch == "" {
		createdBranch = "agent"
	}
	return fmt.Sprintf("worktrees %s (created=%s/<id>, source=%s)", onOffLabel(settings.Enabled), createdBranch, worktreeBranchLabel(settings.UseCurrentBranch, strings.TrimSpace(settings.BaseBranch)))
}

func mapMCPModalServers(servers []client.MCPServer) []ui.MCPModalServer {
	out := make([]ui.MCPModalServer, 0, len(servers))
	for _, server := range servers {
		out = append(out, ui.MCPModalServer{
			ID:          strings.TrimSpace(server.ID),
			Name:        strings.TrimSpace(server.Name),
			Transport:   strings.TrimSpace(server.Transport),
			URL:         strings.TrimSpace(server.URL),
			Command:     strings.TrimSpace(server.Command),
			Args:        append([]string(nil), server.Args...),
			Enabled:     server.Enabled,
			Source:      strings.TrimSpace(server.Source),
			EnvCount:    len(server.Env),
			HeaderCount: len(server.Headers),
			CreatedAt:   server.CreatedAt,
			UpdatedAt:   server.UpdatedAt,
		})
	}
	return out
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

func envMouseEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "", "0", "false", "no", "off":
		return false
	default:
		return false
	}
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
		lines := []string{fmt.Sprintf("permission policy v%d", policy.Version)}
		if len(policy.Rules) == 0 {
			lines = append(lines, "No explicit rules. Built-in defaults: allow bash prefixes cd, ls; ask most others.")
		} else {
			for _, rule := range policy.Rules {
				lines = append(lines, fmt.Sprintf("- %s  [%s]  %s", rule.ID, rule.Decision, a.previewPermissionRule(rule)))
			}
		}
		a.home.SetCommandOverlay(lines)
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

func (a *App) previewPermissionRule(rule client.PermissionRule) string {
	decision := strings.TrimSpace(rule.Decision)
	if decision == "" {
		decision = "allow"
	}
	switch strings.TrimSpace(rule.Kind) {
	case "bash_prefix":
		return fmt.Sprintf("%s bash command prefix: %s", decision, strings.TrimSpace(rule.Pattern))
	case "phrase":
		return fmt.Sprintf("%s phrase: %s", decision, strings.TrimSpace(rule.Pattern))
	default:
		return fmt.Sprintf("%s tool: %s", decision, strings.TrimSpace(rule.Tool))
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
