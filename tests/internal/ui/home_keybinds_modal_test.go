package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestKeybindsModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.keybindsModal.Visible = true

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 50, 14
	screen.SetSize(w, h)
	p.drawKeybindsModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Keybinds") {
		t.Fatalf("expected keybinds modal on narrow screen, got:\n%s", text)
	}
}

func TestHomeFooterRegistersClickableAMTChips(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:    "local",
		ActiveAgent:   "swarm",
		ModelProvider: "codex",
		ModelName:     "gpt-5.4",
		ThinkingLevel: "high",
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 24)
	page.Draw(screen)

	if len(page.bottomBarTargets) < 3 {
		t.Fatalf("bottomBarTargets = %d, want at least 3", len(page.bottomBarTargets))
	}

	want := map[string]bool{
		"open-agents-modal": false,
		"open-models-modal": false,
		"cycle-thinking":    false,
	}
	for _, target := range page.bottomBarTargets {
		if _, ok := want[target.Action]; ok {
			want[target.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("missing bottom bar target for %s", action)
		}
	}
}
