package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestModelsModalDrawsProvidersAboveModelsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]ModelsModalEntry{{Provider: "codex", Model: "gpt-5.4"}},
		"codex",
		"gpt-5.4",
		"",
		"high",
	)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 52, 16
	screen.SetSize(w, h)
	p.drawModelsModal(screen)

	text := dumpScreenText(screen, w, h)
	for _, want := range []string{"Models · codex", "gpt-5.4", "Providers", "codex [ready]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected narrow models modal to contain %q, got:\n%s", want, text)
		}
	}
	providersRow := strings.Index(text, "Providers")
	modelsRow := strings.Index(text, "Models (favorites/newest)")
	if providersRow < 0 || modelsRow < 0 || providersRow > modelsRow {
		t.Fatalf("expected providers pane above models pane on narrow screen, got:\n%s", text)
	}
}
