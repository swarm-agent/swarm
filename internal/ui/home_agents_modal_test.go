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

func TestAgentsModalCompiledSubagentsOnlyOfferSingleProfiles(t *testing.T) {
	page := &HomePage{}
	page.agentsModal.ModelProfiles = []client.ModelProfile{
		{ProfileID: "single", Name: "Single", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "single-model"}},
		{ProfileID: "split", Name: "Split", ModelMode: "split", Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"}, Auto: &client.ModelProfileSelection{Provider: "codex", Model: "auto-model"}},
	}

	for _, agentName := range []string{"system-finder", "system-clone", "coder", "system-designer"} {
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

func TestAgentsModalWideRendersAgentListBesideSettings(t *testing.T) {
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
		for _, want := range []string{"Agents [focus]", "swarm", "Model Settings"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d missing %q in:\n%s", width, want, text)
			}
		}
		if strings.Contains(text, "Active profile [focus]") || strings.Contains(text, "Profile controls stay on top") {
			t.Fatalf("width %d retained the stacked selector in:\n%s", width, text)
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
	if page.agentsModal.Editor == nil || page.agentsModal.Editor.TargetName != "reviewer" {
		t.Fatalf("editor target = %#v, want reviewer", page.agentsModal.Editor)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if page.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("Right focus = %v, want settings", page.agentsModal.Focus)
	}

	page.agentsModal.Focus = agentsModalFocusProfiles
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.agentsModal.Focus != agentsModalFocusDetails {
		t.Fatalf("Enter focus = %v, want settings", page.agentsModal.Focus)
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
