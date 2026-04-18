package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestShowAuthDefaultsInfoOpensModal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAuthDefaultsInfo(&AuthDefaultsInfo{
		Provider:        "google",
		PrimaryModel:    "gemini-3.1-pro-preview",
		PrimaryThinking: "high",
		UtilityProvider: "google",
		UtilityModel:    "gemini-3-flash-preview",
		UtilityThinking: "high",
		Subagents:       []string{"explorer", "memory", "parallel"},
	})

	if !p.AuthDefaultsInfoVisible() {
		t.Fatalf("expected auth defaults info modal to be visible")
	}
	if p.authDefaultsInfoModal.Info.UtilityModel != "gemini-3-flash-preview" {
		t.Fatalf("utility model = %q", p.authDefaultsInfoModal.Info.UtilityModel)
	}
}

func TestAuthDefaultsInfoModalDismissesOnEnterAndEscape(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAuthDefaultsInfo(&AuthDefaultsInfo{
		Provider:        "codex",
		PrimaryModel:    "gpt-5.4",
		PrimaryThinking: "high",
		UtilityProvider: "codex",
		UtilityModel:    "gpt-5.4-mini",
		UtilityThinking: "medium",
		Subagents:       []string{"explorer", "memory", "parallel"},
	})
	if !p.AuthDefaultsInfoVisible() {
		t.Fatalf("expected modal to open")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.AuthDefaultsInfoVisible() {
		t.Fatalf("expected modal to close on Enter")
	}

	p.ShowAuthDefaultsInfo(&AuthDefaultsInfo{
		Provider:     "codex",
		PrimaryModel: "gpt-5.4",
		UtilityModel: "gpt-5.4-mini",
		Subagents:    []string{"explorer", "memory", "parallel"},
	})
	if !p.AuthDefaultsInfoVisible() {
		t.Fatalf("expected modal to reopen")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if p.AuthDefaultsInfoVisible() {
		t.Fatalf("expected modal to close on Escape")
	}
}

func TestAuthDefaultsInfoModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAuthDefaultsInfo(&AuthDefaultsInfo{Provider: "codex", PrimaryModel: "gpt-5.4", UtilityModel: "gpt-5.4-mini", Subagents: []string{"explorer"}})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 44, 12
	screen.SetSize(w, h)
	p.drawAuthDefaultsInfoModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Recommended agent defaults applied") {
		t.Fatalf("expected auth defaults modal on narrow screen, got:\n%s", text)
	}
}
