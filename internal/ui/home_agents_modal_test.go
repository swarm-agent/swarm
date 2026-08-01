package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestAgentsModalPriorityShowsOffAndUsesCatalogMappings(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, Provider: "anthropic", Model: "claude-test"}},
		Providers:        []string{"anthropic"},
		ModelsByProvider: map[string][]string{"anthropic": {"claude-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"anthropic/claude-test": {
				Provider:     "anthropic",
				Model:        "claude-test",
				ServiceTiers: []string{"standard", "priority", "batch"},
				ServiceTierMappings: []client.ModelCatalogServiceTierMapping{
					{Tier: "standard", SwarmSetting: "off"},
					{Tier: "priority", SwarmSetting: "fast"},
				},
			},
		},
	})
	page.openAgentsV2Editor()

	field := page.findAgentsModalEditorField(page.agentsModal.Editor, "service_tier")
	if field == nil {
		t.Fatal("priority field missing")
	}
	if got := agentsModalEditorFieldDisplayValue(*field, field.Placeholder); got != "off" {
		t.Fatalf("empty priority display = %q, want off", got)
	}
	if got, want := field.Options, []string{"", "priority", "fast"}; len(got) != len(want) {
		t.Fatalf("priority options = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("priority options = %#v, want %#v", got, want)
			}
		}
	}
	if got := agentsModalEditorOptionDisplay(""); got != "off" {
		t.Fatalf("empty priority option label = %q, want off", got)
	}
}

func TestAgentsModalInitialSelectedProfileHydratesCleanlyAndActionsWaitForEdit(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{
			Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "split",
			PlanProvider: "codex", PlanModel: "agent-plan", PlanThinking: "max", PlanServiceTier: "fast",
			AutoProvider: "codex", AutoModel: "agent-action", AutoThinking: "xhigh", AutoServiceTier: "fast",
		}},
		Providers: []string{"codex"},
		ModelsByProvider: map[string][]string{
			"codex": {"agent-plan", "agent-action", "profile-model"},
		},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/profile-model": {
				Provider: "codex", Model: "profile-model",
				ThinkingOptions: []string{"max", "xhigh"}, DefaultThinking: "xhigh",
				ServiceTiers: []string{"fast"},
			},
		},
		ModelProfiles: []client.ModelProfile{{
			ProfileID: "max", Name: "Max", ModelMode: "single",
			Single: &client.ModelProfileSelection{Provider: "codex", Model: "profile-model", Thinking: "xhigh", ServiceTier: "fast"},
		}},
		DefaultModelProfileID: "max",
		ActiveModelProfileID:  "max",
	})
	if page.agentsModal.Editor != nil || page.agentsModal.Screen != agentsV2ScreenList {
		t.Fatal("/agents must remain on the V2 list until an agent is opened")
	}
	page.openAgentsV2Editor()

	editor := page.agentsModal.Editor
	if editor == nil {
		t.Fatal("V2 Agents editor missing after opening the selected agent")
	}
	for key, want := range map[string]string{
		"model_profile": "max",
		"model_mode":    "single",
		"provider":      "codex",
		"model":         "profile-model",
		"thinking":      "xhigh",
		"service_tier":  "fast",
	} {
		field := page.findAgentsModalEditorField(editor, key)
		if field == nil || field.Value != want {
			t.Fatalf("initial %s = %#v, want %q from selected model profile", key, field, want)
		}
	}
	if agentsModalEditorHasPendingChanges(editor) {
		t.Fatal("selected model profile hydration made the initial editor dirty")
	}

	render := func() string {
		t.Helper()
		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		defer screen.Fini()
		screen.SetSize(110, 30)
		page.drawAgentsModal(screen)
		screen.Show()
		cells, width, _ := screen.GetContents()
		var text strings.Builder
		for i, cell := range cells {
			if i > 0 && i%width == 0 {
				text.WriteByte('\n')
			}
			if len(cell.Runes) > 0 {
				text.WriteRune(cell.Runes[0])
			} else {
				text.WriteByte(' ')
			}
		}
		return text.String()
	}
	initial := render()
	if strings.Contains(initial, "Choose how to continue") || strings.Contains(initial, "[ Save and apply ]") {
		t.Fatalf("completion actions rendered before an edit:\n%s", initial)
	}
	for _, want := range []string{"[Provider: codex]", "[Model: profile-model]", "[Thinking: xhigh]", "[Priority: fast]"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("initial render missing selected-profile value %q:\n%s", want, initial)
		}
	}

	thinking := page.findAgentsModalEditorField(editor, "thinking")
	for i := range editor.Fields {
		if &editor.Fields[i] == thinking {
			editor.Selected = i
			break
		}
	}
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if !agentsModalEditorHasPendingChanges(editor) {
		t.Fatal("first genuine profile edit did not make the editor dirty")
	}
	edited := render()
	if !strings.Contains(edited, "Choose how to continue") || !strings.Contains(edited, "[ Save and apply ]") {
		t.Fatalf("completion actions did not appear after the first edit:\n%s", edited)
	}
	page.handleAgentsModalEditorAction("temporary")
	action, ok := page.PopAgentsModalAction()
	if !ok || action.ModelProfile == nil || action.ModelProfile.Single == nil || action.ModelProfile.Single.Thinking != "max" {
		t.Fatalf("completion action did not preserve edited thinking value: %#v", action)
	}
}

func TestAgentsModalCompiledSubagentsOnlyOfferSingleProfiles(t *testing.T) {
	page := &HomePage{}
	page.agentsModal.ModelProfiles = []client.ModelProfile{
		{ProfileID: "single", Name: "Single", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "single-model"}},
		{ProfileID: "split", Name: "Split", ModelMode: "split", Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"}, Auto: &client.ModelProfileSelection{Provider: "codex", Model: "auto-model"}},
	}

	for _, agentName := range []string{"system-compact", "system-finder", "system-coder", "system-designer"} {
		got := page.agentsModalModelProfileOptions(AgentModalProfile{Name: agentName, Mode: "subagent"})
		if len(got) != 1 || got[0] != "single" {
			t.Fatalf("%s profile options = %#v, want [single]", agentName, got)
		}
	}

	got := page.agentsModalModelProfileOptions(AgentModalProfile{Name: "swarm", Mode: "primary"})
	if len(got) != 2 {
		t.Fatalf("primary profile options = %#v, want both profiles", got)
	}
}

func TestAgentsModalCompiledSubagentEditorCopiesSingleProfileWithoutSessionControls(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "system-compact", Mode: "subagent", Enabled: true, Provider: "codex", Model: "old-model", ModelMode: "split", DefaultSessionMode: "plan"}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"old-model", "single-model"}},
		ModelProfiles: []client.ModelProfile{
			{ProfileID: "single", Name: "Single", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "single-model", Thinking: "high"}},
			{ProfileID: "split", Name: "Split", ModelMode: "split", Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"}, Auto: &client.ModelProfileSelection{Provider: "codex", Model: "auto-model"}},
		},
	})
	page.openAgentsV2Editor()

	editor := page.agentsModal.Editor
	if editor == nil || !editor.AgentSettingsLocked {
		t.Fatalf("compiled subagent editor = %#v, want locked single-model settings", editor)
	}
	for _, key := range []string{"default_session_mode", "model_mode", "plan_provider", "auto_provider"} {
		field := page.findAgentsModalEditorField(editor, key)
		if field != nil && agentsModalEditorFieldVisible(editor, *field) {
			t.Fatalf("compiled subagent exposed unsupported %s field", key)
		}
	}
	profileField := page.findAgentsModalEditorField(editor, "model_profile")
	if profileField == nil || len(profileField.Options) != 1 || profileField.Options[0] != "single" {
		t.Fatalf("compiled subagent profile choices = %#v, want only single", profileField)
	}
	if profileField.Value != "" {
		t.Fatalf("compiled subagent inherited active session profile %q", profileField.Value)
	}
	profileField.Value = "single"
	if !page.applyAgentsModalModelProfile("single") {
		t.Fatal("single model profile was not copied into the subagent editor")
	}
	if page.agentsModal.SelectedModelProfileID != "" {
		t.Fatalf("compiled subagent changed shared selected profile to %q", page.agentsModal.SelectedModelProfileID)
	}
	page.submitAgentsModalEditor()
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionUpsert || action.Upsert == nil {
		t.Fatalf("compiled subagent save action = %#v", action)
	}
	if action.ModelProfileID != "" {
		t.Fatalf("compiled subagent save tried to apply session profile %q", action.ModelProfileID)
	}
	if action.Upsert.ModelMode != "single" || action.Upsert.Provider != "codex" || action.Upsert.Model != "single-model" || action.Upsert.Thinking != "high" {
		t.Fatalf("compiled subagent copied settings = %#v", action.Upsert)
	}
}

func TestAgentsModalSwarmUsesCanonicalDefaultSplitProfile(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
		ModelProfiles: []client.ModelProfile{{
			ProfileID: "dual", Name: "Dual", ModelMode: "split", IsDefault: true,
			Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"},
			Auto: &client.ModelProfileSelection{Provider: "codex", Model: "action-model"},
		}},
		DefaultModelProfileID: "dual",
	})

	lines := page.agentsModalModelBehaviorLines(AgentModalProfile{Name: "swarm", Mode: "primary"})
	joined := strings.Join(lines, "\n")
	if len(lines) != 2 || !strings.Contains(joined, "Plan: codex/plan-model") || !strings.Contains(joined, "Action: codex/action-model") {
		t.Fatalf("swarm behavior = %#v, want canonical split profile", lines)
	}
}

func TestAgentsModalV2InitiallyRendersOnlyAgentList(t *testing.T) {
	for _, width := range []int{130, 84} {
		page := NewHomePage(model.EmptyHome())
		page.ShowAgentsModal()
		page.SetAgentsModalData(AgentsModalData{
			Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
			ModelProfiles: []client.ModelProfile{{
				ProfileID: "dual", Name: "Dual", ModelMode: "split",
				Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"},
				Auto: &client.ModelProfileSelection{Provider: "codex", Model: "action-model"},
			}},
			DefaultModelProfileID: "dual",
		})

		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		screen.SetSize(width, 38)
		page.drawAgentsModal(screen)
		screen.Show()
		cells, screenWidth, _ := screen.GetContents()
		var rendered strings.Builder
		for i, cell := range cells {
			if i > 0 && i%screenWidth == 0 {
				rendered.WriteByte('\n')
			}
			if len(cell.Runes) > 0 {
				rendered.WriteRune(cell.Runes[0])
			} else {
				rendered.WriteByte(' ')
			}
		}
		text := rendered.String()
		for _, want := range []string{"Agents", "Agent", "Model profile / model", "swarm", "Dual", "Plan: codex/plan-model"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d missing %q in:\n%s", width, want, text)
			}
		}
		for _, rejected := range []string{"Model Settings", "Edit agent", "[Provider:", "[Model:"} {
			if strings.Contains(text, rejected) {
				t.Fatalf("width %d V2 list leaked editor content %q:\n%s", width, rejected, text)
			}
		}
		screen.Fini()
	}
}

func TestAgentsModalListNavigationAndSettingsFocus(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{Profiles: []AgentModalProfile{
		{Name: "swarm", Mode: "primary", Enabled: true},
		{Name: "reviewer", Mode: "subagent", Enabled: true},
	}})

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := page.selectedAgentsModalName(); got != "reviewer" {
		t.Fatalf("selected profile = %q, want reviewer", got)
	}
	if page.agentsModal.Focus != agentsModalFocusProfiles {
		t.Fatalf("focus = %v, want agent list", page.agentsModal.Focus)
	}
	if page.agentsModal.Editor != nil || page.agentsModal.Screen != agentsV2ScreenList {
		t.Fatalf("list selection opened an editor early: screen=%v editor=%#v", page.agentsModal.Screen, page.agentsModal.Editor)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.agentsModal.Editor == nil || page.agentsModal.Editor.TargetName != "reviewer" {
		t.Fatalf("editor target = %#v, want reviewer", page.agentsModal.Editor)
	}
	if page.agentsModal.Screen != agentsV2ScreenEditor {
		t.Fatalf("Enter screen = %v, want V2 editor", page.agentsModal.Screen)
	}
	if page.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("Enter focus = %v, want settings", page.agentsModal.Focus)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.agentsModal.Screen != agentsV2ScreenList || page.agentsModal.Editor != nil {
		t.Fatalf("Esc did not return to the V2 list: screen=%v editor=%#v", page.agentsModal.Screen, page.agentsModal.Editor)
	}
}

func TestAgentsModalCreateNewProfileIsFinalRightProfileOption(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{
			{Name: "swarm", Mode: "primary", Enabled: true},
			{Name: "reviewer", Mode: "subagent", Enabled: true},
		},
		ModelProfiles: []client.ModelProfile{
			{ProfileID: "standard", Name: "Standard", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-standard"}},
			{ProfileID: "fast", Name: "Fast", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-fast"}},
		},
		ActiveModelProfileID: "standard",
	})
	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 34)
	page.drawAgentsModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	text := rendered.String()
	if !strings.Contains(text, "YOUR PROFILE") {
		t.Fatalf("right-side profile dropdown heading missing:\n%s", text)
	}
	createAt := strings.Index(text, "Create new profile")
	fastAt := strings.Index(text, "Fast")
	if createAt < 0 || fastAt < 0 || createAt < fastAt {
		t.Fatalf("Create new profile is not the final right-side profile option:\n%s", text)
	}
	leftPane := text
	if modelSettingsAt := strings.Index(text, "Model Settings"); modelSettingsAt >= 0 {
		leftPane = text[:modelSettingsAt]
	}
	if strings.Contains(leftPane, "Create new profile") {
		t.Fatalf("Create new profile must not appear in the left agent list:\n%s", text)
	}
}

func TestAgentsModalCreateProfileUsesCanonicalSavedModelProfileAction(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-standard"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-standard": {Provider: "codex", Model: "gpt-standard", ThinkingOptions: []string{"off", "medium"}, DefaultThinking: "medium"},
		},
		ModelProfiles: []client.ModelProfile{{
			ProfileID: "standard", Name: "Standard", ModelMode: "single",
			Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-standard", Thinking: "medium"},
		}},
		ActiveModelProfileID: "standard",
	})

	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	field := page.findAgentsModalEditorField(page.agentsModal.Editor, "model_profile")
	if field == nil || len(field.Options) == 0 || field.Options[len(field.Options)-1] != agentsModalCreateProfileOption {
		t.Fatalf("right profile dropdown options = %#v, want create as final option", field)
	}
	field.Value = agentsModalCreateProfileOption
	page.agentsModal.Editor.EditingOption = agentsModalCreateProfileOption
	page.agentsModal.Editor.EditingOptionSet = true
	for i := range page.agentsModal.Editor.Fields {
		if &page.agentsModal.Editor.Fields[i] == field {
			page.agentsModal.Editor.Selected = i
			break
		}
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	editor := page.agentsModal.Editor
	if editor == nil || !editor.CreateModelProfile || editor.Mode != "model" {
		t.Fatalf("right profile dropdown did not open saved model-profile creation settings: %#v", editor)
	}
	if page.findAgentsModalEditorField(editor, "prompt") != nil || page.findAgentsModalEditorField(editor, "mode") != nil {
		t.Fatalf("saved model-profile editor must not expose agent identity fields: %#v", editor.Fields)
	}
	page.findAgentsModalEditorField(editor, "profile_name").Value = "Research profile"
	page.findAgentsModalEditorField(editor, "model_mode").Value = "single"
	page.findAgentsModalEditorField(editor, "provider").Value = "codex"
	page.findAgentsModalEditorField(editor, "model").Value = "gpt-standard"
	page.findAgentsModalEditorField(editor, "thinking").Value = "medium"
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModCtrl))
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionCreateModelProfile || action.ModelProfile == nil {
		t.Fatalf("save action = %#v, want canonical model-profile create", action)
	}
	if action.Upsert != nil || action.ModelProfile.Name != "Research profile" || action.ModelProfile.Single == nil || action.ModelProfile.Single.Model != "gpt-standard" {
		t.Fatalf("canonical model-profile payload = %#v", action)
	}
}

func TestAgentsModalNewProfileRequiresExplicitProviderAndModelForEveryPolicy(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:  []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, Provider: "codex", Model: "gpt-existing", Thinking: "high"}},
		Providers: []string{"codex", "claude"},
		ModelsByProvider: map[string][]string{
			"codex":  {"gpt-new"},
			"claude": {"sonnet-new"},
		},
		DefaultProvider: "codex",
		DefaultModel:    "gpt-new",
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-new":     {Provider: "codex", Model: "gpt-new", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
			"claude/sonnet-new": {Provider: "claude", Model: "sonnet-new", ThinkingOptions: []string{"off", "medium"}, DefaultThinking: "medium"},
		},
	})

	page.openAgentsModalCreateModelProfileEditor()
	editor := page.agentsModal.Editor
	if editor == nil || !editor.CreateModelProfile {
		t.Fatalf("create model-profile editor = %#v", editor)
	}

	assertExplicitSelection := func(label, prefix, providerID, modelID string) {
		t.Helper()
		provider := page.findAgentsModalEditorField(editor, prefix+"provider")
		modelField := page.findAgentsModalEditorField(editor, prefix+"model")
		if provider == nil || modelField == nil {
			t.Fatalf("%s provider/model fields missing: %#v", label, editor.Fields)
		}
		if provider.Value != "" || modelField.Value != "" {
			t.Fatalf("%s inherited provider/model: provider=%q model=%q", label, provider.Value, modelField.Value)
		}
		for _, option := range provider.Options {
			if strings.TrimSpace(option) == "" || strings.EqualFold(strings.TrimSpace(option), "inherit") {
				t.Fatalf("%s provider options include an inherited/empty choice: %#v", label, provider.Options)
			}
		}
		if len(modelField.Options) != 0 {
			t.Fatalf("%s model options before provider choice = %#v, want none", label, modelField.Options)
		}
		if agentsModalEditorFieldVisible(editor, *modelField) {
			t.Fatalf("%s model field must remain hidden until provider is chosen", label)
		}

		provider.Value = providerID
		page.syncAgentsModalEditorDependentOptions(editor)
		if modelField.Value != "" {
			t.Fatalf("%s model auto-selected after provider choice = %q, want empty", label, modelField.Value)
		}
		if !agentsModalEditorFieldVisible(editor, *modelField) {
			t.Fatalf("%s model field should become visible after provider is chosen", label)
		}
		if len(modelField.Options) != 1 || modelField.Options[0] != modelID {
			t.Fatalf("%s model options after provider choice = %#v, want [%s]", label, modelField.Options, modelID)
		}
		for _, option := range modelField.Options {
			if strings.TrimSpace(option) == "" || strings.EqualFold(strings.TrimSpace(option), "inherit") {
				t.Fatalf("%s model options include an inherited/empty choice: %#v", label, modelField.Options)
			}
		}
	}

	assertExplicitSelection("single", "", "codex", "gpt-new")

	page.findAgentsModalEditorField(editor, "provider").Value = ""
	page.findAgentsModalEditorField(editor, "model").Value = ""
	page.findAgentsModalEditorField(editor, "model_mode").Value = "split"
	page.syncAgentsModalEditorDependentOptions(editor)
	assertExplicitSelection("plan", "plan_", "codex", "gpt-new")
	assertExplicitSelection("action", "auto_", "claude", "sonnet-new")
}

func TestAgentsModalEmptyProviderAndModelPickersPreselectFirstOption(t *testing.T) {
	testCases := []struct {
		name      string
		modelMode string
		fieldKey  string
		provider  string
		wantFirst string
		wantNext  string
	}{
		{name: "single provider", modelMode: "single", fieldKey: "provider", wantFirst: "anthropic", wantNext: "codex"},
		{name: "single model", modelMode: "single", fieldKey: "model", provider: "anthropic", wantFirst: "claude-sonnet", wantNext: "claude-haiku"},
		{name: "plan provider", modelMode: "split", fieldKey: "plan_provider", wantFirst: "anthropic", wantNext: "codex"},
		{name: "plan model", modelMode: "split", fieldKey: "plan_model", provider: "anthropic", wantFirst: "claude-sonnet", wantNext: "claude-haiku"},
		{name: "action provider", modelMode: "split", fieldKey: "auto_provider", wantFirst: "anthropic", wantNext: "codex"},
		{name: "action model", modelMode: "split", fieldKey: "auto_model", provider: "anthropic", wantFirst: "claude-sonnet", wantNext: "claude-haiku"},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			newEditor := func() (*HomePage, *agentsModalEditor, *agentsModalEditorField) {
				t.Helper()
				page := NewHomePage(model.EmptyHome())
				page.ShowAgentsModal()
				page.SetAgentsModalData(AgentsModalData{
					Profiles:  []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
					Providers: []string{"anthropic", "codex"},
					ModelsByProvider: map[string][]string{
						"anthropic": {"claude-sonnet", "claude-haiku"},
						"codex":     {"gpt-5.6", "gpt-5.5"},
					},
				})
				page.openAgentsModalCreateModelProfileEditor()
				editor := page.agentsModal.Editor
				page.findAgentsModalEditorField(editor, "model_mode").Value = tt.modelMode
				if tt.provider != "" {
					prefix := strings.TrimSuffix(tt.fieldKey, "model")
					page.findAgentsModalEditorField(editor, prefix+"provider").Value = tt.provider
				}
				page.syncAgentsModalEditorDependentOptions(editor)
				field := page.findAgentsModalEditorField(editor, tt.fieldKey)
				if field == nil {
					t.Fatalf("field %q missing from %#v", tt.fieldKey, editor.Fields)
				}
				field.Value = ""
				for i := range editor.Fields {
					if &editor.Fields[i] == field {
						editor.Selected = i
						break
					}
				}
				editor.Editing = false
				return page, editor, field
			}

			page, editor, field := newEditor()
			page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			if field.Value != "" {
				t.Fatalf("opening picker persisted %q, want field to remain empty until confirmation", field.Value)
			}
			if !editor.EditingOptionSet || editor.EditingOption != tt.wantFirst {
				t.Fatalf("initial highlight = %q (set=%v), want %q", editor.EditingOption, editor.EditingOptionSet, tt.wantFirst)
			}
			page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			if field.Value != tt.wantFirst {
				t.Fatalf("immediate Enter selected %q, want %q", field.Value, tt.wantFirst)
			}

			page, editor, field = newEditor()
			page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
			if field.Value != "" {
				t.Fatalf("Down persisted %q before confirmation, want empty", field.Value)
			}
			if editor.EditingOption != tt.wantNext {
				t.Fatalf("highlight after one Down = %q, want %q", editor.EditingOption, tt.wantNext)
			}
			page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			if field.Value != tt.wantNext {
				t.Fatalf("Enter after one Down selected %q, want %q", field.Value, tt.wantNext)
			}
		})
	}
}

func TestAgentsModalProfileSwitchAndDefaultQueueDistinctActions(t *testing.T) {
	newPage := func() *HomePage {
		page := NewHomePage(model.EmptyHome())
		page.ShowAgentsModal()
		page.SetAgentsModalData(AgentsModalData{
			Profiles: []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
			ModelProfiles: []client.ModelProfile{
				{ProfileID: "default", Name: "Default", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-default"}},
				{ProfileID: "selected", Name: "Selected", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-selected"}},
			},
			DefaultModelProfileID: "default",
			ActiveModelProfileID:  "default",
		})
		page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
		field := page.findAgentsModalEditorField(page.agentsModal.Editor, "model_profile")
		if field == nil {
			t.Fatal("profile field missing")
		}
		if !page.applyAgentsModalModelProfile("selected") {
			t.Fatal("failed to hydrate selected profile into V2 editor")
		}
		return page
	}

	defaultPage := newPage()
	defaultPage.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone))
	defaultAction, ok := defaultPage.PopAgentsModalAction()
	if !ok || defaultAction.Kind != AgentsModalActionSetProfileDefault || defaultAction.ModelProfileID != "selected" {
		t.Fatalf("D action = %#v, want account-default action for selected profile", defaultAction)
	}
	if !defaultPage.AgentsModalVisible() {
		t.Fatal("D closed the modal before the default operation completed")
	}

	switchPage := newPage()
	switchPage.queueAgentsModalProfileSwitch("selected")
	switchAction, ok := switchPage.PopAgentsModalAction()
	if !ok || switchAction.Kind != AgentsModalActionSwitchProfile || switchAction.ModelProfileID != "selected" {
		t.Fatalf("switch action = %#v, want canonical active-profile switch", switchAction)
	}
	if !switchPage.AgentsModalVisible() {
		t.Fatal("profile switch closed the modal before the app confirmed success")
	}
}

func TestAgentsModalProfileDirectionsWrapAndRemainComplete(t *testing.T) {
	const width = 36
	lines := agentsModalProfileDirectionLines(width)
	if len(lines) < 2 {
		t.Fatalf("profile directions did not wrap at width %d: %#v", width, lines)
	}
	for _, line := range lines {
		if got := len([]rune(line)); got > width {
			t.Fatalf("wrapped profile direction %q is %d runes wide, want at most %d", line, got, width)
		}
	}
	if got := strings.Join(lines, " "); got != agentsModalProfileDirections {
		t.Fatalf("wrapped profile directions = %q, want complete text %q", got, agentsModalProfileDirections)
	}
	for _, want := range []string{"Profile directions:", "Enter selects a profile", "D sets account default", "use the completion buttons below"} {
		if !strings.Contains(agentsModalProfileDirections, want) {
			t.Fatalf("profile directions missing %q: %q", want, agentsModalProfileDirections)
		}
	}
}

func TestAgentsModalEditorOffersDesktopStyleCompletionActions(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "single", Provider: "codex", Model: "gpt-test", Thinking: "high"}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-test": {Provider: "codex", Model: "gpt-test", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
		},
		ModelProfiles:        []client.ModelProfile{{ProfileID: "saved", Name: "Saved", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-test", Thinking: "high"}}},
		ActiveModelProfileID: "saved",
	})
	page.openAgentsV2Editor()

	actions := agentsModalEditorActions(page.agentsModal.Editor)
	labels := make([]string, 0, len(actions))
	for _, action := range actions {
		labels = append(labels, action.Label)
	}
	for _, want := range []string{"Cancel", "Continue for this chat only", "Save as new", "Save and apply"} {
		found := false
		for _, label := range labels {
			if label == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("editor actions = %#v, missing Desktop-equivalent %q", labels, want)
		}
	}

	page.handleAgentsModalEditorAction("temporary")
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionApplyTemporary || action.ModelProfile == nil || action.ModelProfile.Single == nil {
		t.Fatalf("temporary action = %#v, want temporary model-profile application", action)
	}
	if action.ModelProfile.Single.Provider != "codex" || action.ModelProfile.Single.Model != "gpt-test" {
		t.Fatalf("temporary model profile = %#v", action.ModelProfile)
	}

	page.SetAgentsModalLoading(false)
	page.handleAgentsModalEditorAction("save")
	action, ok = page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionUpdateModelProfile || action.ModelProfileID != "saved" || !action.ApplyModelProfile {
		t.Fatalf("save action = %#v, want saved-profile update and apply", action)
	}
}

func TestAgentsModalEditorCancelDiscardsWithoutSaveConfirmation(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "single", Provider: "codex", Model: "gpt-test", Thinking: "high"}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-test": {Provider: "codex", Model: "gpt-test", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
		},
	})
	page.openAgentsV2Editor()
	editor := page.agentsModal.Editor
	thinking := page.findAgentsModalEditorField(editor, "thinking")
	if thinking == nil {
		t.Fatal("thinking field missing")
	}
	thinking.Value = "off"
	if !agentsModalEditorHasPendingChanges(editor) {
		t.Fatal("test setup did not create pending changes")
	}

	page.handleAgentsModalEditorAction("cancel")

	if page.agentsModal.ConfirmUnsaved {
		t.Fatal("Cancel opened a save confirmation; want immediate discard")
	}
	if page.agentsModal.Editor != nil || page.agentsModal.Screen != agentsV2ScreenList {
		t.Fatalf("Cancel left editor open: screen=%q editor=%#v", page.agentsModal.Screen, page.agentsModal.Editor)
	}
	if page.agentsModal.Status != "changes discarded; back to agent list" {
		t.Fatalf("Cancel status = %q", page.agentsModal.Status)
	}
	if _, ok := page.PopAgentsModalAction(); ok {
		t.Fatal("Cancel queued a save/apply action")
	}
}

func TestAgentsModalEditorButtonsStayVisibleAndOperableAtConstrainedHeight(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles: []AgentModalProfile{{
			Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "split",
			PlanProvider: "codex", PlanModel: "gpt-test", PlanThinking: "high",
			AutoProvider: "codex", AutoModel: "gpt-test", AutoThinking: "high",
		}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-test": {Provider: "codex", Model: "gpt-test", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
		},
		ModelProfiles: []client.ModelProfile{{
			ProfileID: "saved", Name: "Saved", ModelMode: "split",
			Plan: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-test", Thinking: "high"},
			Auto: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-test", Thinking: "high"},
		}},
		ActiveModelProfileID: "saved",
	})
	page.openAgentsV2Editor()
	editor := page.agentsModal.Editor
	actionThinking := page.findAgentsModalEditorField(editor, "auto_thinking")
	if actionThinking == nil {
		t.Fatal("Action thinking field missing")
	}
	actionThinking.Value = "off"
	visible := agentsModalVisibleEditorFieldIndexes(editor)
	editor.Selected = visible[len(visible)-1]
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if !editor.ActionFocused || editor.ActionSelected != 0 {
		t.Fatalf("Down from final field focus = action:%v index:%d, want first completion button", editor.ActionFocused, editor.ActionSelected)
	}
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	if editor.ActionFocused || editor.Selected != visible[len(visible)-1] {
		t.Fatalf("Up from first completion button = action:%v field:%d, want final field %d", editor.ActionFocused, editor.Selected, visible[len(visible)-1])
	}
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if !editor.ActionFocused || editor.ActionSelected != 0 {
		t.Fatalf("Tab from final field focus = action:%v index:%d, want first completion button", editor.ActionFocused, editor.ActionSelected)
	}
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if editor.ActionSelected != 1 {
		t.Fatalf("Right action index = %d, want 1", editor.ActionSelected)
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 20)
	page.drawAgentsModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	text := rendered.String()
	actionAt := strings.Index(text, "Action")
	for _, label := range []string{"Cancel", "Continue for this chat only", "Save as new", "Save and apply"} {
		buttonAt := strings.Index(text, "[ "+label+" ]")
		if buttonAt < 0 || buttonAt < actionAt {
			t.Fatalf("constrained editor did not keep %q visible below Action:\n%s", label, text)
		}
	}

	var temporaryTarget *clickTarget
	for i := range page.agentsModalTargets {
		target := &page.agentsModalTargets[i]
		if target.Action == "agents-editor-action" && target.Meta == "temporary" {
			temporaryTarget = target
			break
		}
	}
	if temporaryTarget == nil {
		t.Fatal("visible Continue for this chat only button was not registered as a mouse target")
	}
	page.activateAgentsModalTarget(*temporaryTarget)
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionApplyTemporary {
		t.Fatalf("mouse activation action = %#v, want temporary application", action)
	}
}

func TestAgentsModalExistingProfileDraftImmediatelyShowsCompletionActions(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "single", Provider: "codex", Model: "gpt-test", Thinking: "high"}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-test": {Provider: "codex", Model: "gpt-test", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
		},
		ModelProfiles:        []client.ModelProfile{{ProfileID: "saved", Name: "Saved", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "gpt-test", Thinking: "high"}}},
		ActiveModelProfileID: "saved",
	})
	page.openAgentsV2Editor()
	editor := page.agentsModal.Editor
	thinking := page.findAgentsModalEditorField(editor, "thinking")
	if thinking == nil {
		t.Fatal("thinking field missing")
	}
	for i := range editor.Fields {
		if &editor.Fields[i] == thinking {
			editor.Selected = i
			break
		}
	}

	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if thinking.Value != "high" || editor.EditingOption != "off" {
		t.Fatalf("draft picker state = value %q draft %q, want unchanged value and off draft", thinking.Value, editor.EditingOption)
	}
	if !agentsModalEditorHasPendingChanges(editor) {
		t.Fatal("uncommitted picker change was not detected immediately")
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 20)
	page.drawAgentsModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	text := rendered.String()
	for _, label := range []string{"Cancel", "Continue for this chat only", "Save as new", "Save and apply"} {
		if !strings.Contains(text, "[ "+label+" ]") {
			t.Fatalf("existing-profile draft did not immediately show %q at constrained height:\n%s", label, text)
		}
	}

	page.handleAgentsModalEditorAction("temporary")
	action, ok := page.PopAgentsModalAction()
	if !ok || action.ModelProfile == nil || action.ModelProfile.Single == nil || action.ModelProfile.Single.Thinking != "off" {
		t.Fatalf("completion action did not preserve active picker draft: %#v", action)
	}
}

func TestAgentsModalNewProfileEditImmediatelyShowsCompletionActions(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:         []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true}},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": {"gpt-test"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/gpt-test": {Provider: "codex", Model: "gpt-test", ThinkingOptions: []string{"off", "high"}, DefaultThinking: "high"},
		},
	})
	page.openAgentsModalCreateModelProfileEditor()
	editor := page.agentsModal.Editor
	if editor == nil || !editor.CreateModelProfile || !editor.Editing {
		t.Fatalf("new-profile editor state = %#v", editor)
	}

	page.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyRune, 'N', tcell.ModNone))
	if !agentsModalEditorHasPendingChanges(editor) {
		t.Fatal("new-profile text edit was not detected immediately")
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 20)
	page.drawAgentsModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	text := rendered.String()
	for _, label := range []string{"Cancel", "Continue for this chat only", "Create profile and apply"} {
		if !strings.Contains(text, "[ "+label+" ]") {
			t.Fatalf("new-profile edit did not immediately show %q at constrained height:\n%s", label, text)
		}
	}
}

func TestAgentsModalSingleModelRendersProviderFirstThenVerticalOptions(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(AgentsModalData{
		Profiles:  []AgentModalProfile{{Name: "swarm", Mode: "primary", Enabled: true, ModelMode: "single"}},
		Providers: []string{"anthropic", "codex"},
		ModelsByProvider: map[string][]string{
			"anthropic": {"claude-sonnet"},
		},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"anthropic/claude-sonnet": {
				Provider:        "anthropic",
				Model:           "claude-sonnet",
				ThinkingOptions: []string{"off", "high"},
				DefaultThinking: "high",
				ServiceTiers:    []string{"priority"},
			},
		},
	})
	page.openAgentsV2Editor()

	render := func() []string {
		t.Helper()
		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		defer screen.Fini()
		screen.SetSize(110, 38)
		page.drawAgentsModal(screen)
		screen.Show()
		cells, width, _ := screen.GetContents()
		lines := make([]string, 0, len(cells)/width)
		for start := 0; start < len(cells); start += width {
			var line strings.Builder
			end := minInt(start+width, len(cells))
			for _, cell := range cells[start:end] {
				if len(cell.Runes) > 0 {
					line.WriteRune(cell.Runes[0])
				} else {
					line.WriteByte(' ')
				}
			}
			lines = append(lines, line.String())
		}
		return lines
	}
	findLine := func(lines []string, text string) int {
		t.Helper()
		for i, line := range lines {
			if strings.Contains(line, text) {
				return i
			}
		}
		return -1
	}

	initial := render()
	providerLine := findLine(initial, "[Provider: choose provider]")
	if providerLine < 0 {
		t.Fatalf("single-model provider choice missing from initial render:\n%s", strings.Join(initial, "\n"))
	}
	for _, hidden := range []string{"[Model:", "[Thinking:", "[Priority:"} {
		if findLine(initial, hidden) >= 0 {
			t.Fatalf("single-model %s rendered before provider selection:\n%s", hidden, strings.Join(initial, "\n"))
		}
	}

	editor := page.agentsModal.Editor
	page.findAgentsModalEditorField(editor, "provider").Value = "anthropic"
	page.syncAgentsModalEditorDependentOptions(editor)
	providerChosen := render()
	providerLine = findLine(providerChosen, "[Provider: anthropic]")
	modelLine := findLine(providerChosen, "[Model: choose model]")
	if providerLine < 0 || modelLine <= providerLine {
		t.Fatalf("model did not render underneath the selected provider:\n%s", strings.Join(providerChosen, "\n"))
	}

	page.findAgentsModalEditorField(editor, "model").Value = "claude-sonnet"
	page.syncAgentsModalEditorDependentOptions(editor)
	complete := render()
	ordered := []string{"[Provider: anthropic]", "[Model: claude-sonnet]", "[Thinking: high]", "[Priority: off]"}
	previous := -1
	for _, text := range ordered {
		line := findLine(complete, text)
		if line <= previous {
			t.Fatalf("single-model fields are not a vertical ordered list (%q at line %d after %d):\n%s", text, line, previous, strings.Join(complete, "\n"))
		}
		previous = line
	}
}

func TestAgentsModalCompiledSubagentDisplayNames(t *testing.T) {
	for input, want := range map[string]string{
		"system-clone":    "Coder",
		"clone":           "Coder",
		"coder":           "Coder",
		"system-finder":   "Finder",
		"system-designer": "Designer",
		"designer":        "Designer",
		"writer":          "writer",
	} {
		if got := agentsModalDisplayName(input); got != want {
			t.Fatalf("agentsModalDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}
