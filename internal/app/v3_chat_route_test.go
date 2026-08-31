package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func TestArchiveCommandIsSessionChatOnlyAndHasNoArguments(t *testing.T) {
	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if suggestion.Command == archiveCommandUsage {
			t.Fatal("home command suggestions expose /archive")
		}
	}
	for _, suggestion := range buildChatCommandSuggestions(false) {
		if suggestion.Command != archiveCommandUsage {
			continue
		}
		if len(suggestion.QuickTips) != 0 {
			t.Fatalf("archive quick tips = %#v, want no argument options", suggestion.QuickTips)
		}
		return
	}
	t.Fatal("session chat command suggestions do not include /archive")
}

func TestV3ArchiveCommandArchivesCurrentSessionAndReturnsHome(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sessions/session-archive/archive" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		requestSeen <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-archive", "archived": true})
	}))
	defer server.Close()

	store := v3chat.NewStore()
	store.Dispatch(v3chat.HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-archive"}}})
	page := v3chat.NewPage(v3chat.NewRuntime(nil, store, nil), v3chat.PageStyles{})
	home := ui.NewHomePage(model.EmptyHome())
	home.SetCommandSuggestions(buildHomeCommandSuggestions(false))
	app := &App{api: testAPIWithToken(server.URL), home: home, v3Chat: page, route: "v3chat"}
	app.handleArchiveCommand(nil)

	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("archive request was not sent")
	}
	deadline := time.Now().Add(time.Second)
	for app.route != "home" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.route != "home" || app.v3Chat != nil {
		t.Fatalf("archive navigation = route %q page %p, want home with closed page", app.route, app.v3Chat)
	}
	if got := app.home.Status(); got != "session archived" {
		t.Fatalf("archive status = %q", got)
	}
}

func TestV3ArchiveCommandFailureKeepsChatOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "archive blocked", http.StatusConflict)
	}))
	defer server.Close()

	store := v3chat.NewStore()
	store.Dispatch(v3chat.HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-archive"}}})
	page := v3chat.NewPage(v3chat.NewRuntime(nil, store, nil), v3chat.PageStyles{})
	home := ui.NewHomePage(model.EmptyHome())
	home.SetCommandSuggestions(buildHomeCommandSuggestions(false))
	app := &App{api: testAPIWithToken(server.URL), home: home, v3Chat: page, route: "v3chat"}
	app.handleArchiveCommand(nil)

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(page.Status(), "/archive failed:") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.route != "v3chat" || app.v3Chat != page {
		t.Fatalf("failed archive navigation = route %q page %p, want current chat %p", app.route, app.v3Chat, page)
	}
	if got := page.Status(); !strings.Contains(got, "/archive failed:") {
		t.Fatalf("failed archive status = %q", got)
	}
}

func TestCompactCommandIsSessionChatOnlyAndHasNoArguments(t *testing.T) {
	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if suggestion.Command == compactCommandUsage {
			t.Fatal("home command suggestions expose /compact")
		}
	}
	for _, suggestion := range buildChatCommandSuggestions(false) {
		if suggestion.Command != compactCommandUsage {
			continue
		}
		if len(suggestion.QuickTips) != 0 {
			t.Fatalf("compact quick tips = %#v, want no argument options", suggestion.QuickTips)
		}
		if strings.Contains(strings.ToLower(suggestion.Hint), "threshold") || strings.Contains(strings.ToLower(suggestion.Hint), "note") {
			t.Fatalf("compact hint exposes removed options: %q", suggestion.Hint)
		}
		return
	}
	t.Fatal("session chat command suggestions do not include /compact")
}

func TestV3ChatRenderWakeIsIdleAndCoalescesBurst(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	app := &App{screen: screen, pendingV3ChatRender: make(chan struct{}, 1)}

	events := make(chan tcell.Event, 1)
	quit := make(chan struct{})
	go screen.ChannelEvents(events, quit)
	select {
	case event := <-events:
		close(quit)
		t.Fatalf("idle V3 chat emitted terminal event %#v", event)
	case <-time.After(25 * time.Millisecond):
		close(quit)
	}
	for i := 0; i < 1000; i++ {
		app.requestV3ChatRender()
	}
	if got := len(app.pendingV3ChatRender); got != 1 {
		t.Fatalf("coalesced render queue length = %d, want 1", got)
	}
	app.consumeV3ChatRender()
	if got := len(app.pendingV3ChatRender); got != 0 {
		t.Fatalf("render queue after consume = %d", got)
	}
}

func TestV3ChatCtrlXKeepsChatMountedThroughSessionModal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sync/bootstrap" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"sessions_by_id": map[string]any{
				"current-session":  map[string]any{"id": "current-session", "title": "Current session", "updated_at": 2000},
				"selected-session": map[string]any{"id": "selected-session", "title": "Selected session", "updated_at": 1000},
			},
			"session_order": []string{"current-session", "selected-session"},
		})
	}))
	defer server.Close()

	page := v3chat.NewPage(v3chat.NewRuntime(nil, v3chat.NewStore(), nil), v3chat.PageStyles{})
	app := &App{
		api:      testAPIWithToken(server.URL),
		home:     ui.NewHomePage(model.EmptyHome()),
		v3Chat:   page,
		route:    "v3chat",
		config:   defaultAppConfig(),
		reloadCh: make(chan homeReloadResult, 1),
	}

	if !app.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlX, 0, tcell.ModNone)) {
		t.Fatal("Ctrl-X was not handled")
	}
	if app.route != "v3chat" || app.v3Chat != page {
		t.Fatalf("chat changed while session manager loaded: route=%q page=%p, want v3chat %p", app.route, app.v3Chat, page)
	}
	deadline := time.Now().Add(time.Second)
	for len(app.reloadCh) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	app.consumeReloadResult()
	if !app.home.SessionsModalVisible() {
		t.Fatal("session manager did not open over V3 chat")
	}
	if app.route != "v3chat" || app.v3Chat != page {
		t.Fatalf("chat changed after session manager opened: route=%q page=%p, want v3chat %p", app.route, app.v3Chat, page)
	}

	if !app.home.HandleChatOverlayKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) {
		t.Fatal("session manager did not handle Escape")
	}
	if app.home.SessionsModalVisible() {
		t.Fatal("session manager remained visible after Escape")
	}
	if app.route != "v3chat" || app.v3Chat != page {
		t.Fatalf("Escape left the current chat: route=%q page=%p, want v3chat %p", app.route, app.v3Chat, page)
	}

	app.openLoadedHomeSessionsModal("")
	app.home.HandleChatOverlayKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	app.home.HandleChatOverlayKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := app.home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionOpenSession || action.SessionID != "selected-session" {
		t.Fatalf("session selection action = %#v, ok=%v", action, ok)
	}
	if app.route != "v3chat" || app.v3Chat != page {
		t.Fatalf("chat changed before canonical selection navigation: route=%q page=%p, want v3chat %p", app.route, app.v3Chat, page)
	}
}

func TestV3ChatPlanModalEscapeClosesButCtrlCDoesNot(t *testing.T) {
	page := v3chat.NewPage(v3chat.NewRuntime(nil, v3chat.NewStore(), nil), v3chat.PageStyles{})
	if !page.OpenCurrentPlanModal(client.SessionPlan{ID: "plan", Document: &client.SessionPlanDocument{Title: "Current plan"}}) {
		t.Fatal("plan modal did not open")
	}
	app := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		v3Chat: page,
		route:  "v3chat",
		config: defaultAppConfig(),
	}

	ctrlC := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)
	if app.handleGlobalKey(ctrlC) {
		t.Fatal("Ctrl-C was intercepted by the global quit handler while the plan modal was open")
	}
	page.HandleKey(ctrlC)
	if !page.PlanModalVisible() {
		t.Fatal("Ctrl-C closed the plan modal")
	}
	if app.quitRequested {
		t.Fatal("Ctrl-C requested quit while the plan modal was open")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.PlanModalVisible() {
		t.Fatal("Escape did not close the plan modal")
	}
	if app.route != "v3chat" {
		t.Fatalf("Escape left chat route = %q, want v3chat", app.route)
	}
}

func TestV3ChatCtrlCClearsInputBeforeRequestingQuit(t *testing.T) {
	page := v3chat.NewPage(v3chat.NewRuntime(nil, v3chat.NewStore(), nil), v3chat.PageStyles{})
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	app := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		v3Chat: page,
		route:  "v3chat",
		config: defaultAppConfig(),
	}

	if !app.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)) {
		t.Fatal("first Ctrl-C was not handled")
	}
	if got := page.InputValue(); got != "" {
		t.Fatalf("V3 input = %q, want cleared input", got)
	}
	if app.quitRequested {
		t.Fatal("first Ctrl-C requested quit while input was non-empty")
	}
	if !app.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)) {
		t.Fatal("second Ctrl-C was not handled")
	}
	if !app.quitRequested {
		t.Fatal("second Ctrl-C did not request quit after input was cleared")
	}
}

func TestV3ChatCtrlCJumpsScrollbackBeforeRequestingQuit(t *testing.T) {
	page := v3chat.NewPage(v3chat.NewRuntime(nil, v3chat.NewStore(), nil), v3chat.PageStyles{})
	page.HandleKey(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone))
	app := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		v3Chat: page,
		route:  "v3chat",
		config: defaultAppConfig(),
	}

	if !app.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)) {
		t.Fatal("first Ctrl-C was not handled")
	}
	if app.quitRequested {
		t.Fatal("first Ctrl-C requested quit while transcript was scrolled up")
	}
	if page.ConsumeQuitScrollbackJump() {
		t.Fatal("first Ctrl-C did not restore the live bottom position")
	}

	if !app.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)) {
		t.Fatal("second Ctrl-C was not handled")
	}
	if !app.quitRequested {
		t.Fatal("second Ctrl-C did not request quit from the live bottom position")
	}
}

func TestV3ChatFooterUsesBackendResolvedSwarmIdentity(t *testing.T) {
	app := &App{
		config: AppConfig{Swarm: SwarmConfig{Name: "Configured Desk"}},
		homeModel: model.HomeModel{CurrentSwarmTarget: &model.SwarmTarget{
			SwarmID: "host-swarm", Name: "Primary Desk", Relationship: "self", Kind: "host",
		}},
	}
	primary := model.ChatRoute{SwarmID: "host-swarm", Label: "Local", TargetKind: "host", TargetRelationship: "self"}
	if got := app.v3ChatFooterRouteLabel(primary); got != "Primary Desk" {
		t.Fatalf("primary footer identity = %q, want backend target name", got)
	}

	remote := model.ChatRoute{SwarmID: "remote-swarm", Label: "Remote Desk", TargetKind: "container", TargetRelationship: "child"}
	if got := app.v3ChatFooterRouteLabel(remote); got != "Remote Desk" {
		t.Fatalf("remote footer identity = %q, want route target name", got)
	}
}

func TestV3ChatFooterUsesConfiguredSwarmNameForLegacyHostFallback(t *testing.T) {
	app := &App{config: AppConfig{Swarm: SwarmConfig{Name: "Configured Desk"}}}
	if got := app.v3ChatFooterRouteLabel(model.ChatRoute{Label: "host"}); got != "Configured Desk" {
		t.Fatalf("legacy host footer identity = %q, want configured swarm name", got)
	}
}

func TestV3ChatStylesUseCanonicalMarkdownThemePath(t *testing.T) {
	app := &App{}
	styles := app.v3ChatStyles()
	if styles.RenderMarkdown == nil {
		t.Fatal("V3 chat markdown renderer was not connected")
	}
	lines := styles.RenderMarkdown("# Heading\n\n- **item**", 40)
	if len(lines) < 3 || lines[0].Text != "Heading" || lines[2].Text != "• item" {
		t.Fatalf("canonical markdown lines = %#v", lines)
	}
	headingForeground, _, headingAttributes := lines[0].Style.Decompose()
	wantForeground, _, wantAttributes := app.effectiveThemeOption().Theme.MarkdownHeading.Decompose()
	if headingForeground != wantForeground || headingAttributes != wantAttributes {
		t.Fatalf("heading theme = fg %v attrs %v, want fg %v attrs %v", headingForeground, headingAttributes, wantForeground, wantAttributes)
	}
	if len(lines[2].Spans) == 0 {
		t.Fatalf("inline markdown spans were not preserved: %#v", lines[2])
	}
}

func TestV3ChatHomeProfileUsesCanonicalCreateAndFooterHandoff(t *testing.T) {
	profile := model.ActiveModelProfile{Source: "saved", ProfileID: "profile-1", Name: "Focused work", ModelMode: "split"}
	page := ui.NewHomePage(model.HomeModel{ActiveAgent: "swarm", ActiveModelProfile: profile})
	intent := buildHomeSessionIntent(page, model.ChatRoute{})
	if intent.Profile != profile {
		t.Fatalf("home-to-chat profile handoff = %#v, want %#v", intent.Profile, profile)
	}
	if choice := v3ChatCreateModelProfile(intent.Profile, "swarm"); choice != nil {
		t.Fatalf("Swarm create sent favorite as session override: %#v", choice)
	}
	choice := v3ChatCreateModelProfile(intent.Profile, "finder")
	if choice == nil || choice.SavedProfileID != "profile-1" {
		t.Fatalf("non-Swarm create model profile choice = %#v", choice)
	}
	if got := v3ChatHomeProfileLabel(profile); got != "Focused work" {
		t.Fatalf("chat footer profile label = %q, want Focused work", got)
	}
}

func TestV3ChatDraftSelectionsReuseHomepagePlanAutoProjection(t *testing.T) {
	profile := model.ActiveModelProfile{Source: "saved", ProfileID: "profile-1", Name: "Focused work", ModelMode: "split"}
	home := ui.NewHomePage(model.HomeModel{
		ActiveAgent:             "swarm",
		ActiveAgentExitPlanMode: true,
		ActiveModelProfile:      profile,
		PlanModelProvider:       "codex", PlanModelName: "gpt-5.4", PlanThinkingLevel: "high", PlanServiceTier: "fast", PlanContextMode: "1m",
		AutoModelProvider: "openrouter", AutoModelName: "auto-model", AutoThinkingLevel: "medium", AutoServiceTier: "flex", AutoContextMode: "auto-context",
		ContextWindow: 180000,
	})
	home.SetSessionMode("auto")
	app := &App{home: home, homeModel: model.HomeModel{ContextWindow: 180000}}
	selections := app.v3ChatDraftModeSelections()
	plan, auto := selections["plan"], selections["auto"]
	if plan.Preference.Model != "gpt-5.4" || plan.Preference.Thinking != "high" || plan.Preference.ServiceTier != "fast" || plan.Preference.ContextMode != "1m" || plan.ContextWindow != model.CodexGPT54LargeContextWindow {
		t.Fatalf("plan draft selection = %#v", plan)
	}
	if auto.Preference.Provider != "openrouter" || auto.Preference.Model != "auto-model" || auto.Preference.Thinking != "medium" || auto.Preference.ServiceTier != "flex" || auto.Preference.ContextMode != "auto-context" || auto.ContextWindow != 180000 {
		t.Fatalf("auto draft selection = %#v", auto)
	}
	if plan.AgentModelPolicy.ProfileName != "Focused work" || auto.AgentModelPolicy.ProfileName != "Focused work" || plan.ModelProfile != nil || auto.ModelProfile != nil {
		t.Fatalf("Swarm draft profile choices = plan %#v auto %#v", plan, auto)
	}
	if home.SessionMode() != "auto" {
		t.Fatalf("draft selection projection mutated homepage mode to %q", home.SessionMode())
	}
}

func TestWorkspaceShortcutsUseGlobalHandlingAndCanonicalActivation(t *testing.T) {
	appRaw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(appRaw)
	for _, required := range []string{
		"KeybindGlobalWorkspaceSelect",
		"WorkspaceSlotKeybindID",
		"workspaceSwitchHotkeyBlocked",
		"activateWorkspaceSlot",
		"activateWorkspaceAtIndex",
		"SelectWorkspace",
		"syncActiveWorkspaceSelection",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("workspace keyboard route is missing canonical path: %q", required)
		}
	}
	for _, retired := range []string{"KeybindGlobalWorkspacePrev", "KeybindGlobalWorkspaceNext", "cycleWorkspaceBy"} {
		if strings.Contains(source, retired) {
			t.Fatalf("unrequested workspace cycling route remains: %q", retired)
		}
	}
}

func TestNewHomepageChatRouteUsesV3PageBoundary(t *testing.T) {
	appRaw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	routeRaw, err := os.ReadFile("v3_chat_page.go")
	if err != nil {
		t.Fatal(err)
	}
	appSource := string(appRaw)
	start := strings.Index(appSource, "func (a *App) openChatSessionWithWorktree")
	end := strings.Index(appSource[start:], "func (a *App) openExistingSession")
	if start < 0 || end < 0 {
		t.Fatal("new homepage route boundary not found")
	}
	productionRoute := appSource[start : start+end]
	if !strings.Contains(productionRoute, "openNewV3Chat") {
		t.Fatal("new homepage route does not open V3 page")
	}
	for _, required := range []string{"openRoutedV3Primer", "PlanModeRequested", "canonicalSelfChatRoute"} {
		if !strings.Contains(string(routeRaw), required) {
			t.Fatalf("new homepage route is missing mandatory routed V3 boundary %q", required)
		}
	}
	for _, retired := range []string{"Managed" + "Worktree" + "Requested", "Worktree" + "Mode:", "a.v3Chat." + "OpenNew("} {
		if strings.Contains(string(routeRaw), retired) {
			t.Fatalf("new homepage route retains retired direct/worktree-toggle authority %q", retired)
		}
	}
	if strings.Contains(appSource, "openLegacyChatSessionWithWorktree") {
		t.Fatal("new homepage route still exposes legacy session wiring")
	}
	for _, forbidden := range []string{"ui.NewChatPage", "startSessionEventStream", "StreamEvents", "time.NewTicker"} {
		if strings.Contains(productionRoute, forbidden) || strings.Contains(string(routeRaw), forbidden) {
			t.Fatalf("new homepage V3 route contains forbidden legacy/polling token %q", forbidden)
		}
	}
}
