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

func TestAgentsModalMouseOpensCanonicalAgentEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(canonicalAgentsModalTestData())
	screen := newModalMouseTestScreen(t, 110, 36)
	defer screen.Fini()
	p.drawAgentsModal(screen)
	target := findClickTarget(t, p.agentsModalTargets, "agents-agent", 2)
	p.HandleMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))
	if got := p.selectedAgentsModalName(); got != "finder" {
		t.Fatalf("selected agent = %q, want finder", got)
	}
	if p.agentsModal.Focus != agentsModalFocusAssignments {
		t.Fatalf("mouse focus = %v, want assignment selector", p.agentsModal.Focus)
	}
}

func TestAgentsModalMouseClickFieldStartsCanonicalEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(canonicalAgentsModalTestData())
	p.agentsModal.Focus = agentsModalFocusFields
	p.agentsModal.SelectedAssignment = 0

	screen := newModalMouseTestScreen(t, 110, 36)
	defer screen.Fini()
	p.drawAgentsModal(screen)

	target := findClickTarget(t, p.agentsModalTargets, "agents-field", 1)
	p.HandleChatOverlayMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))
	if p.agentsModal.Focus != agentsModalFocusFields || p.agentsModal.SelectedField != 1 || !p.agentsModal.EditingField {
		t.Fatalf("field click state = focus %v field %d editing %v", p.agentsModal.Focus, p.agentsModal.SelectedField, p.agentsModal.EditingField)
	}
}

/* Legacy profile-oriented mouse tests removed with the old /agents workflow.
func TestAgentsModalMouseOpensAgentInV2Editor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{Name: "swarm", Mode: "primary", Enabled: true},
			{Name: "finder", Mode: "subagent", Enabled: true},
		},
		ActivePrimary: "swarm",
	})

	screen := newModalMouseTestScreen(t, 110, 30)
	defer screen.Fini()
	p.drawAgentsModal(screen)

	target := findClickTarget(t, p.agentsModalTargets, "agents-profile", 1)
	p.HandleMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))

	if got := p.selectedAgentsModalName(); got != "finder" {
		t.Fatalf("selected profile = %q, want finder", got)
	}
	if p.agentsModal.Focus != agentsModalFocusDetails || p.agentsModal.Screen != agentsV2ScreenEditor {
		t.Fatalf("focus/screen = %v/%v, want V2 editor", p.agentsModal.Focus, p.agentsModal.Screen)
	}
	if p.agentsModal.Editor == nil || p.agentsModal.Editor.TargetName != "finder" {
		t.Fatalf("editor = %#v, want finder settings", p.agentsModal.Editor)
	}
}

func TestAgentsModalMouseSelectsCreateFromRightProfileDropdown(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
		ModelProfiles: []client.ModelProfile{{
			ProfileID: "standard", Name: "Standard", ModelMode: "single",
			Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-standard"},
		}},
		ActiveModelProfileID: "standard",
	})
	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	screen := newModalMouseTestScreen(t, 110, 34)
	defer screen.Fini()
	p.drawAgentsModal(screen)

	target := findClickTarget(t, p.agentsModalTargets, "agents-model-profile-option", 0)
	if target.Meta != agentsModalCreateProfileOption {
		for _, candidate := range p.agentsModalTargets {
			if candidate.Action == "agents-model-profile-option" && candidate.Meta == agentsModalCreateProfileOption {
				target = candidate
				break
			}
		}
	}
	if target.Meta != agentsModalCreateProfileOption {
		t.Fatalf("create profile mouse target not found: %v", p.agentsModalTargets)
	}
	p.HandleMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, 0))

	if p.agentsModal.Editor == nil || !p.agentsModal.Editor.CreateModelProfile || p.agentsModal.Editor.Mode != "model" {
		t.Fatalf("profile dropdown click did not prime saved model-profile creation editor: %#v", p.agentsModal.Editor)
	}
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want right settings pane", p.agentsModal.Focus)
	}
}

func TestAgentsModalMouseClickEditorFieldStartsEditing(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})
	if _, ok := p.selectedAgentsModalProfile(); !ok {
		t.Fatalf("expected selected profile")
	}
	p.openAgentsV2Editor()

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

*/
