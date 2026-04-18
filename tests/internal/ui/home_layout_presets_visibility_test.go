package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeDrawWideDoesNotRenderPresetsRow(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5",
			"Thinking: high",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 24)
	for _, unwanted := range []string{"[Agent: swarm]", "[Model: gpt-5]", "[Thinking: high]"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected homepage to hide presets row token %q, got:\n%s", unwanted, text)
		}
	}
}

func TestHomeDrawCompactDoesNotRenderPresetsRow(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5",
			"Thinking: high",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(56, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 56, 24)
	for _, unwanted := range []string{"[a:swarm]", "[m:gpt-5]", "[t:high]"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected compact homepage to hide presets row token %q, got:\n%s", unwanted, text)
		}
	}
}
