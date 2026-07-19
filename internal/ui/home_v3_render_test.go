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
	for _, want := range []string{"Talk to Swarm", "Type below to begin"} {
		if !strings.Contains(text, want) {
			t.Fatalf("homepage missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Start a session", "Ready for a new V3 session", "/agents to personalize your profiles", "SWARM HOME", "Center hive. Start fast.", "swarm://"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("homepage retained obsolete hero treatment %q:\n%s", unwanted, text)
		}
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
