package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

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
	choice := v3ChatCreateModelProfile(intent.Profile)
	if choice == nil || choice.SavedProfileID != "profile-1" {
		t.Fatalf("create model profile choice = %#v", choice)
	}
	if got := v3ChatHomeProfileLabel(profile); got != "Focused work" {
		t.Fatalf("chat footer profile label = %q, want Focused work", got)
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
	end := strings.Index(appSource[start:], "func (a *App) openLegacyChatSessionWithWorktree")
	if start < 0 || end < 0 {
		t.Fatal("new homepage route boundary not found")
	}
	productionRoute := appSource[start : start+end]
	if !strings.Contains(productionRoute, "openNewV3Chat") {
		t.Fatal("new homepage route does not open V3 page")
	}
	for _, forbidden := range []string{"ui.NewChatPage", "startSessionEventStream", "StreamEvents", "time.NewTicker"} {
		if strings.Contains(productionRoute, forbidden) || strings.Contains(string(routeRaw), forbidden) {
			t.Fatalf("new homepage V3 route contains forbidden legacy/polling token %q", forbidden)
		}
	}
}
