package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestV3HomepageDrawsSimpleLaunchPromptOnCanonicalHomePage(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)

	page := NewHomePage(model.HomeModel{
		ActiveAgent:   "swarm",
		ModelProvider: "codex",
		ModelName:     "gpt-test",
		Workspaces: []model.Workspace{{
			Name:   "Default",
			Path:   "/workspace",
			Icon:   "◆",
			Active: true,
		}},
		Directories: []model.DirectoryItem{{
			Name:        "Default",
			Path:        "/workspace",
			IsWorkspace: true,
		}},
	})
	page.Draw(screen)

	text := dumpHomeTestScreen(screen, 100, 30)
	for _, want := range []string{"Talk to Swarm", "Ctrl+X sessions • / for commands"} {
		if !strings.Contains(text, want) {
			t.Fatalf("homepage missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Type below to begin", "↑ to revisit recents", "Start a session", "Ready for a new V3 session", "/agents to personalize your profiles", "SWARM HOME", "Center hive. Start fast.", "swarm://"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("homepage retained obsolete hero treatment %q:\n%s", unwanted, text)
		}
	}
}

func TestRequiredOnboardingReplacesHomepageAndAcceptsInput(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)

	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	if !page.OnboardingVisible() {
		t.Fatal("required onboarding was not made visible")
	}
	page.Draw(screen)

	text := dumpHomeTestScreen(screen, 100, 30)
	for _, want := range []string{"SWARM  ·  FIRST LAUNCH", "STEP 1 OF 3", "Your name", "Swarm name"} {
		if !strings.Contains(text, want) {
			t.Fatalf("required onboarding missing %q from home page:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Talk to Swarm") {
		t.Fatalf("main homepage rendered behind required onboarding:\n%s", text)
	}

	for _, r := range "alice" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	for _, r := range "Local Swarm" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopHomeAction()
	if !ok {
		t.Fatal("submitting required onboarding did not queue a save action")
	}
	if action.Kind != HomeActionSaveOnboarding || action.Username != "alice" || action.SwarmName != "Local Swarm" {
		t.Fatalf("onboarding action = %+v", action)
	}
}

func TestV3HomepageCompactLayoutOmitsLaunchPanel(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(60, 14)

	page := NewHomePage(model.EmptyHome())
	page.Draw(screen)

	if text := dumpHomeTestScreen(screen, 60, 14); strings.Contains(text, "SWARM HOME") {
		t.Fatalf("compact homepage unexpectedly rendered full launch panel:\n%s", text)
	}
}

func dumpHomeTestScreen(screen tcell.Screen, width, height int) string {
	var out strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			main, _, _, _ := screen.GetContent(x, y)
			if main == 0 {
				main = ' '
			}
			out.WriteRune(main)
		}
		out.WriteByte('\n')
	}
	return out.String()
}
