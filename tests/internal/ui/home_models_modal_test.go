package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func makeModelsModalEntries(provider string, count int) []ModelsModalEntry {
	out := make([]ModelsModalEntry, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, ModelsModalEntry{
			Provider: provider,
			Model:    fmt.Sprintf("model-%02d", i),
		})
	}
	return out
}

func TestModelsModalPageNavigationInModelPane(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true}},
		makeModelsModalEntries("codex", 30),
		"codex",
		"model-00",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels

	p.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	if p.modelsModal.SelectedModel != 10 {
		t.Fatalf("selected model after PgDn = %d, want 10", p.modelsModal.SelectedModel)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if p.modelsModal.SelectedModel != 29 {
		t.Fatalf("selected model after End = %d, want 29", p.modelsModal.SelectedModel)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	if p.modelsModal.SelectedModel != 0 {
		t.Fatalf("selected model after Home = %d, want 0", p.modelsModal.SelectedModel)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone))
	if p.modelsModal.SelectedModel != 0 {
		t.Fatalf("selected model after PgUp at top = %d, want 0", p.modelsModal.SelectedModel)
	}
}

func TestModelsModalModelPaneScrollTracksSelection(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true}},
		makeModelsModalEntries("codex", 40),
		"codex",
		"model-00",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels
	p.modelsModal.SelectedModel = 30

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	p.drawModelsModalModelPane(screen, Rect{X: 0, Y: 0, W: 80, H: 12})

	if p.modelsModal.ModelScroll <= 0 {
		t.Fatalf("model scroll = %d, want > 0", p.modelsModal.ModelScroll)
	}
	maxRows := (12 - 2) / 2
	selectedPos := 30
	if p.modelsModal.ModelScroll > selectedPos || p.modelsModal.ModelScroll+maxRows-1 < selectedPos {
		t.Fatalf("selected model not visible: scroll=%d rows=%d selectedPos=%d", p.modelsModal.ModelScroll, maxRows, selectedPos)
	}
}

func TestModelsModalTypingInListStartsSearch(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true}},
		makeModelsModalEntries("codex", 5),
		"codex",
		"model-00",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))

	if p.modelsModal.Focus != modelsModalFocusSearch {
		t.Fatalf("focus = %v, want search", p.modelsModal.Focus)
	}
	if p.modelsModal.Search != "x" {
		t.Fatalf("search = %q, want %q", p.modelsModal.Search, "x")
	}
}

func TestModelsModalEnterOnAuthRequiredProviderStartsInlineAuthEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: false, Runnable: false}},
		[]ModelsModalEntry{{Provider: "google", Model: "gemini-2.5-pro"}},
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if _, ok := p.PopModelsModalAction(); ok {
		t.Fatalf("did not expect action before inline auth editor submit")
	}
	if p.modelsModal.AuthEditor == nil {
		t.Fatalf("expected inline auth editor to open")
	}
	if p.modelsModal.AuthEditor.Provider != "google" {
		t.Fatalf("auth editor provider = %q, want google", p.modelsModal.AuthEditor.Provider)
	}
	if p.modelsModal.AuthEditor.Step != modelsModalAuthEditorStepAPIKey {
		t.Fatalf("auth editor step = %v, want API key step", p.modelsModal.AuthEditor.Step)
	}
}

func TestModelsModalEnterOnReadyProviderMovesFocusToModels(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: true, Runnable: true}},
		[]ModelsModalEntry{{Provider: "google", Model: "gemini-2.5-pro"}},
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.modelsModal.Focus != modelsModalFocusModels {
		t.Fatalf("focus = %v, want models focus", p.modelsModal.Focus)
	}
	if p.modelsModal.AuthEditor != nil {
		t.Fatalf("did not expect auth editor for ready provider")
	}
	if _, ok := p.PopModelsModalAction(); ok {
		t.Fatalf("did not expect action when entering model pane")
	}
}

func TestModelsModalEnterOnAuthRequiredModelStartsInlineAuthEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: false, Runnable: false}},
		[]ModelsModalEntry{{Provider: "google", Model: "gemini-2.5-pro"}},
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels
	p.modelsModal.SelectedModel = 0

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if _, ok := p.PopModelsModalAction(); ok {
		t.Fatalf("did not expect action before inline auth editor submit")
	}
	if p.modelsModal.AuthEditor == nil {
		t.Fatalf("expected inline auth editor to open")
	}
	if p.modelsModal.AuthEditor.Provider != "google" {
		t.Fatalf("auth editor provider = %q, want google", p.modelsModal.AuthEditor.Provider)
	}
}

func TestModelsModalEnterOnEmptyModelListStartsInlineAuthEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if _, ok := p.PopModelsModalAction(); ok {
		t.Fatalf("did not expect action before inline auth editor submit")
	}
	if p.modelsModal.AuthEditor == nil {
		t.Fatalf("expected inline auth editor to open")
	}
	if p.modelsModal.AuthEditor.Provider != "google" {
		t.Fatalf("auth editor provider = %q, want google", p.modelsModal.AuthEditor.Provider)
	}
}

func TestModelsModalEnterOnReadyModelQueuesSetDefaultAndClose(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: true, Runnable: true}},
		[]ModelsModalEntry{{Provider: "google", Model: "gemini-2.5-pro"}},
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels
	p.modelsModal.SelectedModel = 0

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected set-default action")
	}
	if action.Kind != ModelsModalActionSetActiveModel {
		t.Fatalf("action kind = %q, want %q", action.Kind, ModelsModalActionSetActiveModel)
	}
	if action.Provider != "google" || action.Model != "gemini-2.5-pro" {
		t.Fatalf("action target = %s/%s, want google/gemini-2.5-pro", action.Provider, action.Model)
	}
	if !action.CloseAfter {
		t.Fatalf("expected CloseAfter true")
	}
}

func TestModelsModalInlineAuthEditorSubmitWithoutLabel(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for _, r := range "sk-test-1234" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected a pending models modal action")
	}
	if action.Kind != ModelsModalActionUpsertAPIKey {
		t.Fatalf("action kind = %q, want %q", action.Kind, ModelsModalActionUpsertAPIKey)
	}
	if action.Provider != "google" {
		t.Fatalf("action provider = %q, want google", action.Provider)
	}
	if action.APIKey != "sk-test-1234" {
		t.Fatalf("action APIKey = %q, want %q", action.APIKey, "sk-test-1234")
	}
	if action.KeyLabel != "" {
		t.Fatalf("action KeyLabel = %q, want empty", action.KeyLabel)
	}
	if !action.SetActive {
		t.Fatalf("expected SetActive true")
	}
}

func TestModelsModalInlineAuthEditorSubmitWithLabel(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
		"",
		"",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for _, r := range "sk-test-5678" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	for _, r := range "work laptop" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected a pending models modal action")
	}
	if action.Kind != ModelsModalActionUpsertAPIKey {
		t.Fatalf("action kind = %q, want %q", action.Kind, ModelsModalActionUpsertAPIKey)
	}
	if action.Provider != "google" {
		t.Fatalf("action provider = %q, want google", action.Provider)
	}
	if action.APIKey != "sk-test-5678" {
		t.Fatalf("action APIKey = %q, want %q", action.APIKey, "sk-test-5678")
	}
	if action.KeyLabel != "work laptop" {
		t.Fatalf("action KeyLabel = %q, want %q", action.KeyLabel, "work laptop")
	}
	if !action.SetActive {
		t.Fatalf("expected SetActive true")
	}
}

func TestModelThinkingForActionUsesOffWhenModelHasNoReasoning(t *testing.T) {
	got := modelThinkingForAction(ModelsModalEntry{
		Provider:         "google",
		Model:            "gemini-2.0-flash",
		Reasoning:        false,
		FavoriteThinking: "xhigh",
	}, "high")
	if got != "off" {
		t.Fatalf("thinking = %q, want off", got)
	}
}

func TestModelsModalThinkingHotkeySetsSelectedModelThinking(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{
			{ID: "google", Ready: true, Runnable: true},
		},
		[]ModelsModalEntry{
			{Provider: "google", Model: "gemini-2.5-pro", Reasoning: true},
		},
		"google",
		"gemini-2.5-pro",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected thinking set action")
	}
	if action.Kind != ModelsModalActionSetActiveModel {
		t.Fatalf("action kind = %q, want %q", action.Kind, ModelsModalActionSetActiveModel)
	}
	if action.Provider != "google" || action.Model != "gemini-2.5-pro" {
		t.Fatalf("action target = %s/%s, want google/gemini-2.5-pro", action.Provider, action.Model)
	}
	if action.Thinking != "medium" {
		t.Fatalf("thinking = %q, want medium", action.Thinking)
	}
	if !action.CloseAfter {
		t.Fatalf("expected CloseAfter true")
	}
}

func TestModelsModalThinkingHotkeyForcesOffForNonReasoningModel(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{
			{ID: "google", Ready: true, Runnable: true},
		},
		[]ModelsModalEntry{
			{Provider: "google", Model: "gemini-2.0-flash", Reasoning: false},
		},
		"google",
		"gemini-2.0-flash",
		"high",
	)
	p.modelsModal.Focus = modelsModalFocusModels

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, '5', tcell.ModNone))

	action, ok := p.PopModelsModalAction()
	if !ok {
		t.Fatalf("expected thinking set action")
	}
	if action.Thinking != "off" {
		t.Fatalf("thinking = %q, want off", action.Thinking)
	}
}

func TestModelsModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowModelsModal()
	p.SetModelsModalData(
		[]ModelsModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]ModelsModalEntry{{Provider: "codex", Model: "gpt-5.4"}},
		"codex",
		"gpt-5.4",
		"high",
	)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 52, 14
	screen.SetSize(w, h)
	p.drawModelsModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Models") {
		t.Fatalf("expected models modal on narrow screen, got:\n%s", text)
	}
}
