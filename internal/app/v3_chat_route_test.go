package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

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
