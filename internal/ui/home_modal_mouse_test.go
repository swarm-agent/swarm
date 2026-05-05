package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func newModalMouseTestScreen(t *testing.T, width, height int) tcell.Screen {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	screen.SetSize(width, height)
	return screen
}

func findClickTarget(t *testing.T, targets []clickTarget, action string, index int) clickTarget {
	t.Helper()
	for _, target := range targets {
		if target.Action == action && target.Index == index {
			return target
		}
	}
	t.Fatalf("target %s/%d not found in %v", action, index, targets)
	return clickTarget{}
}

func TestModelsModalMouseClickModelQueuesAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]ModelsModalEntry{
			{Provider: "codex", Model: "gpt-5.4", Reasoning: true},
			{Provider: "codex", Model: "gpt-5.5", Reasoning: true},
		},
		"codex",
		"gpt-5.4",
		"",
		"high",
	)

	screen := newModalMouseTestScreen(t, 100, 28)
	defer screen.Fini()
	p.drawModelsModal(screen)

	target := findClickTarget(t, p.modelsModalTargets, "models-model", 1)
	p.HandleMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected model action")
	}
	if action.Kind != ModelsModalActionSetActiveModel || action.Provider != "codex" || action.Model != "gpt-5.5" {
		t.Fatalf("action = %+v, want set codex/gpt-5.5", action)
	}
	if !action.CloseAfter {
		t.Fatalf("expected CloseAfter true")
	}
}

func TestModelsModalChatOverlayMouseRoutesToModelTargets(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: true, Runnable: true}},
		[]ModelsModalEntry{{Provider: "google", Model: "gemini-2.5-pro", Reasoning: true}},
		"google",
		"gemini-2.5-pro",
		"",
		"high",
	)

	screen := newModalMouseTestScreen(t, 100, 28)
	defer screen.Fini()
	p.drawModelsModal(screen)

	target := findClickTarget(t, p.modelsModalTargets, "models-model", 0)
	if !p.HandleChatOverlayMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0)) {
		t.Fatalf("expected chat overlay mouse to be handled")
	}
	if _, ok := p.PopModelsModalAction(); !ok {
		t.Fatalf("expected model action through chat overlay mouse route")
	}
}

func TestAgentsModalMouseClickSelectedProfileOpensDetails(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{Name: "swarm", Mode: "primary", Enabled: true},
			{Name: "explorer", Mode: "subagent", Enabled: true},
		},
		ActivePrimary: "swarm",
	})

	screen := newModalMouseTestScreen(t, 110, 30)
	defer screen.Fini()
	p.drawAgentsModal(screen)

	target := findClickTarget(t, p.agentsModalTargets, "agents-profile", 0)
	p.HandleMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))

	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details", p.agentsModal.Focus)
	}
	if p.agentsModal.Editor != nil {
		t.Fatalf("profile click should open details, not editor")
	}
}

func TestAgentsModalMouseClickEditorFieldStartsEditing(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		t.Fatalf("expected selected profile")
	}
	p.openAgentsModalEditEditor(profile)

	screen := newModalMouseTestScreen(t, 110, 30)
	defer screen.Fini()
	p.drawAgentsModal(screen)

	target := findClickTarget(t, p.agentsModalTargets, "agents-editor-field", 1)
	p.HandleChatOverlayMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))

	if p.agentsModal.Editor == nil {
		t.Fatalf("expected editor to remain open")
	}
	if p.agentsModal.Editor.Selected != 1 {
		t.Fatalf("selected field = %d, want 1", p.agentsModal.Editor.Selected)
	}
	if !p.agentsModal.Editor.Editing {
		t.Fatalf("expected clicked editor field to enter editing mode")
	}
}
