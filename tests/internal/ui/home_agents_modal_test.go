package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func hasAgentsEditorField(editor *agentsModalEditor, key string) bool {
	if editor == nil {
		return false
	}
	for _, field := range editor.Fields {
		if field.Key == key {
			return true
		}
	}
	return false
}

func TestAgentsModalEnterOpensDetailOnSelectedProfile(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{
				Name:        "swarm",
				Mode:        "primary",
				Description: "main orchestrator",
				Provider:    "codex",
				Prompt:      "You are Swarm",
				Enabled:     true,
			},
		},
		ActivePrimary: "swarm",
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.agentsModal.Editor != nil {
		t.Fatalf("editor should stay closed when first opening a profile")
	}
	if got := p.agentsModal.Status; !strings.Contains(got, "Viewing profile: swarm") {
		t.Fatalf("status = %q, want viewing profile message", got)
	}
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details", p.agentsModal.Focus)
	}
}

func TestAgentsModalEnterFromDetailsOpensEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})
	p.agentsModal.Focus = agentsModalFocusDetails

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.agentsModal.Editor == nil {
		t.Fatalf("expected editor to open from details on Enter")
	}
	if p.agentsModal.Editor.TargetName != "swarm" {
		t.Fatalf("editor target = %q, want swarm", p.agentsModal.Editor.TargetName)
	}
}

func TestAgentsModalEscapeFromDetailsReturnsToProfiles(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 3

	p.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))

	if p.agentsModal.Focus != agentsModalFocusProfiles {
		t.Fatalf("focus = %v, want profiles", p.agentsModal.Focus)
	}
	if p.agentsModal.DetailScroll != 0 {
		t.Fatalf("detail scroll = %d, want 0", p.agentsModal.DetailScroll)
	}
}

func TestAgentsModalEnterInSearchReturnsToProfiles(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})
	p.agentsModal.Focus = agentsModalFocusSearch

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.agentsModal.Focus != agentsModalFocusProfiles {
		t.Fatalf("focus = %v, want profiles", p.agentsModal.Focus)
	}
	if p.agentsModal.Editor != nil {
		t.Fatalf("editor should stay closed when leaving search")
	}
}

func TestAgentsModalModelThinkingInheritanceLabel(t *testing.T) {
	if got := agentsModalModelLabel(""); got != "inherit default model" {
		t.Fatalf("agentsModalModelLabel(\"\") = %q", got)
	}
	if got := agentsModalThinkingLabel(""); got != "inherit default thinking" {
		t.Fatalf("agentsModalThinkingLabel(\"\") = %q", got)
	}
	if got := agentsModalModelLabel("codex-latest"); got != "codex-latest" {
		t.Fatalf("agentsModalModelLabel(non-empty) = %q", got)
	}
	if got := agentsModalThinkingLabel("medium"); got != "medium" {
		t.Fatalf("agentsModalThinkingLabel(non-empty) = %q", got)
	}
}

func TestAgentsModalEditorEnterTogglesEditMode(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details after opening profile", p.agentsModal.Focus)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Editor == nil {
		t.Fatalf("expected editor to open")
	}
	if p.agentsModal.Editor.Editing {
		t.Fatalf("editor should start in navigation mode")
	}
	selected := p.agentsModal.Editor.Selected

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !p.agentsModal.Editor.Editing {
		t.Fatalf("expected enter to enable field editing")
	}
	if p.agentsModal.Editor.Selected != selected {
		t.Fatalf("selected field changed on edit enter")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Editor.Editing {
		t.Fatalf("expected second enter to commit field editing")
	}
	if p.agentsModal.Editor.Selected != selected {
		t.Fatalf("selected field changed on commit enter")
	}
}

func TestAgentsModalEditorRestrictsModelAndThinkingToOptions(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{
			Name:     "swarm",
			Mode:     "primary",
			Provider: "codex",
			Enabled:  true,
		}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": []string{"gpt-5.4"}},
		ReasoningModels:  map[string]bool{"codex/gpt-5.4": true},
		DefaultProvider:  "codex",
		DefaultModel:     "gpt-5.4",
		DefaultThinking:  "high",
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details after opening profile", p.agentsModal.Focus)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	editor := p.agentsModal.Editor
	if editor == nil {
		t.Fatalf("expected editor")
	}

	var modelField, thinkingField *agentsModalEditorField
	for i := range editor.Fields {
		if editor.Fields[i].Key == "model" {
			modelField = &editor.Fields[i]
		}
		if editor.Fields[i].Key == "thinking" {
			thinkingField = &editor.Fields[i]
		}
	}
	if modelField == nil || thinkingField == nil {
		t.Fatalf("expected model and thinking fields")
	}
	if len(modelField.Options) < 2 {
		t.Fatalf("model options = %v, want inherit + available model", modelField.Options)
	}
	if modelField.Options[0] != "" {
		t.Fatalf("first model option should be inherit empty value")
	}
	if modelField.Options[1] != "gpt-5.4" {
		t.Fatalf("unexpected model option: %v", modelField.Options)
	}
	if len(thinkingField.Options) < 3 {
		t.Fatalf("thinking options too short: %v", thinkingField.Options)
	}
}

func TestAgentsModalSubmitFallsBackToAvailableModelAndThinking(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{
				Name:     "swarm",
				Mode:     "primary",
				Provider: "codex",
				Enabled:  true,
			},
		},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": []string{"gpt-5.4"}},
		ReasoningModels:  map[string]bool{"codex/gpt-5.4": false},
		DefaultProvider:  "codex",
		DefaultModel:     "gpt-5.4",
		DefaultThinking:  "xhigh",
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details after opening profile", p.agentsModal.Focus)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	editor := p.agentsModal.Editor
	if editor == nil {
		t.Fatalf("expected editor")
	}
	for i := range editor.Fields {
		switch editor.Fields[i].Key {
		case "model":
			editor.Fields[i].Value = "not-available"
		case "thinking":
			editor.Fields[i].Value = "high"
		}
	}

	p.submitAgentsModalEditor()
	action, ok := p.PopAgentsModalAction()
	if !ok {
		t.Fatalf("expected pending upsert action")
	}
	if action.Upsert == nil {
		t.Fatalf("expected upsert payload")
	}
	if action.Upsert.Model != "gpt-5.4" {
		t.Fatalf("model fallback = %q, want %q", action.Upsert.Model, "gpt-5.4")
	}
	if action.Upsert.Thinking != "off" {
		t.Fatalf("thinking fallback = %q, want off for non-reasoning model", action.Upsert.Thinking)
	}
}

func TestAgentsModalEditorIncludesProviderField(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details after opening profile", p.agentsModal.Focus)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Editor == nil {
		t.Fatalf("expected edit editor")
	}
	if !hasAgentsEditorField(p.agentsModal.Editor, "provider") {
		t.Fatalf("provider should be editable in profile editor")
	}

	p.agentsModal.Editor = nil
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone))
	if p.agentsModal.Editor == nil {
		t.Fatalf("expected create editor")
	}
	if !hasAgentsEditorField(p.agentsModal.Editor, "provider") {
		t.Fatalf("provider should be editable in create editor")
	}
}

func TestAgentsModalSubmitPreservesSelectedProvider(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{
				Name:     "swarm",
				Mode:     "primary",
				Provider: "other",
				Enabled:  true,
			},
		},
		Providers:       []string{"other"},
		DefaultProvider: "other",
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("focus = %v, want details after opening profile", p.agentsModal.Focus)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if p.agentsModal.Editor == nil {
		t.Fatalf("expected editor")
	}

	p.submitAgentsModalEditor()
	action, ok := p.PopAgentsModalAction()
	if !ok || action.Upsert == nil {
		t.Fatalf("expected pending upsert action")
	}
	if action.Upsert.Provider != "other" {
		t.Fatalf("provider = %q, want other", action.Upsert.Provider)
	}
}

func TestAgentsModalDeleteRejectsMemory(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "memory", Mode: "subagent", Enabled: true}},
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone))
	if got := p.agentsModal.Status; !strings.Contains(strings.ToLower(got), "memory is protected") {
		t.Fatalf("status = %q, want memory protected message", got)
	}
	if _, ok := p.PopAgentsModalAction(); ok {
		t.Fatalf("unexpected pending action when deleting memory")
	}
}

func TestAgentsModalSetUtilityAIAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles:  []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
		Providers: []string{"codex"},
		ModelsByProvider: map[string][]string{
			"codex": {"gpt-5.4"},
		},
		UtilityProvider:       "codex",
		UtilityModel:          "gpt-5.4",
		UtilityAgents:         []string{"explorer", "memory", "parallel"},
		CustomUtilityAgents:   []string{"memory"},
		UtilityBaselineAgents: []string{"explorer", "parallel"},
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'R', tcell.ModShift))
	if p.agentsModal.Editor == nil || p.agentsModal.Editor.Mode != "utility-ai" {
		t.Fatalf("expected Utility AI editor, got %#v", p.agentsModal.Editor)
	}
	if got := strings.ToLower(p.agentsModal.Status); !strings.Contains(got, "explorer, parallel") || !strings.Contains(got, "overrides for memory") || !strings.Contains(got, "clear overrides") {
		t.Fatalf("status = %q, want baseline targets and clear-overrides guidance", p.agentsModal.Status)
	}
	p.submitAgentsModalEditor()
	action, ok := p.PopAgentsModalAction()
	if !ok {
		t.Fatalf("expected set Utility AI action")
	}
	if action.Kind != AgentsModalActionSetUtilityAI {
		t.Fatalf("action kind = %q, want %q", action.Kind, AgentsModalActionSetUtilityAI)
	}
	if action.UtilityAI == nil || action.UtilityAI.Provider != "codex" || action.UtilityAI.Model != "gpt-5.4" {
		t.Fatalf("utility input = %#v, want codex/gpt-5.4", action.UtilityAI)
	}
	if action.UtilityAI.OverwriteExplicit {
		t.Fatalf("overwrite explicit = true, want false for normal Set Utility AI")
	}

	p.agentsModal.Editor = &agentsModalEditor{
		Mode:       "utility-ai",
		TargetName: "utility-ai",
		Fields: []agentsModalEditorField{
			{Key: "provider", Value: "codex"},
			{Key: "model", Value: "gpt-5.4"},
			{Key: "thinking", Value: "off"},
			{Key: "scope", Value: "clear overrides"},
		},
	}
	p.submitAgentsModalEditor()
	action, ok = p.PopAgentsModalAction()
	if !ok {
		t.Fatalf("expected clear-overrides Utility AI action")
	}
	if action.UtilityAI == nil || !action.UtilityAI.OverwriteExplicit {
		t.Fatalf("utility input = %#v, want overwrite explicit", action.UtilityAI)
	}
}

func TestAgentsModalResetDefaultsRequiresConfirmation(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
	})

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'Z', tcell.ModShift))
	if _, ok := p.PopAgentsModalAction(); ok {
		t.Fatalf("did not expect reset action on first Shift+Z")
	}
	if got := p.agentsModal.Status; !strings.Contains(strings.ToLower(got), "warning") {
		t.Fatalf("status = %q, want warning", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'Z', tcell.ModShift))
	action, ok := p.PopAgentsModalAction()
	if !ok {
		t.Fatalf("expected reset defaults action")
	}
	if action.Kind != AgentsModalActionResetDefaults {
		t.Fatalf("action kind = %q, want %q", action.Kind, AgentsModalActionResetDefaults)
	}
}

func TestAgentsModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAgentsModal()
	p.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{
			Name:             "swarm",
			Mode:             "primary",
			Description:      "main orchestrator",
			Provider:         "codex",
			Prompt:           "You are Swarm",
			ExecutionSetting: "readwrite",
			Enabled:          true,
		}},
		ActivePrimary: "swarm",
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 54, 16
	screen.SetSize(w, h)
	p.drawAgentsModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Agents Manager") {
		t.Fatalf("expected agents modal on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "memory cannot be deleted") {
		t.Fatalf("expected memory protection notice, got:\n%s", text)
	}
	if !strings.Contains(text, "Shift+R set Utility AI") {
		t.Fatalf("expected Shift+R set Utility AI hint, got:\n%s", text)
	}
	if !strings.Contains(text, "Shift+Z reset all") {
		t.Fatalf("expected Shift+Z reset hint, got:\n%s", text)
	}
	if !strings.Contains(text, "Enter open profile") {
		t.Fatalf("expected enter-open-profile hint, got:\n%s", text)
	}
	if !strings.Contains(text, "Primary") {
		t.Fatalf("expected primary section heading, got:\n%s", text)
	}
	if !strings.Contains(text, "role: main orchestrator") {
		t.Fatalf("expected role text, got:\n%s", text)
	}
	if !strings.Contains(text, "mode: primary") {
		t.Fatalf("expected mode text, got:\n%s", text)
	}
	if !strings.Contains(text, "execution: read/write") {
		t.Fatalf("expected execution text, got:\n%s", text)
	}
	if strings.Contains(text, "Profile Details") {
		t.Fatalf("did not expect split detail pane on cards view, got:\n%s", text)
	}
	if !strings.Contains(text, "Shift+Z destructively resets everything") {
		t.Fatalf("expected destructive reset hint, got:\n%s", text)
	}
}
