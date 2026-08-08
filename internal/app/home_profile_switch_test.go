package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestAgentsModalCanonicalSaveClosesOnlyAfterSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent-model-settings" || r.Method != http.MethodPatch {
			t.Fatalf("unexpected save request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"agent_model_settings":{"swarm":{"action":{"provider":"codex","model":"action","thinking":"high"},"plan":{"provider":"codex","model":"plan","thinking":"high"}},"system_agents":{},"updated_at":1}}`))
	}))
	defer server.Close()

	page := ui.NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	app := &App{api: testAPIWithToken(server.URL), home: page, route: "home"}
	app.handleAgentsModalAction(ui.AgentsModalAction{
		Kind:  ui.AgentsModalActionSave,
		Agent: "swarm",
		Swarm: &client.AgentModelSettingsSwarmPatch{
			Action: client.AgentModelAssignment{Provider: "codex", Model: "action", Thinking: "high"},
			Plan:   client.AgentModelAssignment{Provider: "codex", Model: "plan", Thinking: "high"},
		},
	})
	if page.AgentsModalVisible() {
		t.Fatal("successful canonical save left Agents modal open")
	}
}

func TestHomeCommandSuggestionsExcludeMode(t *testing.T) {
	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if strings.EqualFold(strings.TrimSpace(suggestion.Command), "/mode") {
			t.Fatal("/mode remains exposed in TUI command suggestions")
		}
	}
}

func TestSelectHomeModelProfileUpdatesDefaultAndFooterModel(t *testing.T) {
	// swarmd's canonical favorite wire is flat. This regression fixture must not
	// use the retired nested single/split profile shape.
	profileJSON := `{"profile_id":"focus","name":"Focus","provider":"codex","model":"gpt-focus","thinking":"high","is_default":true}`
	footerUpdatedBeforeRefresh := false
	var page *ui.HomePage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/model-profiles/default":
			_, _ = w.Write([]byte(`{"model_profile":` + profileJSON + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model-profiles":
			if page != nil {
				provider, modelName, thinking, _, _ := page.ModelState()
				footerUpdatedBeforeRefresh = provider == "codex" && modelName == "gpt-focus" && thinking == "high"
			}
			_, _ = w.Write([]byte(`{"model_profiles":[` + profileJSON + `],"default_profile_id":"focus"}`))
		default:
			t.Fatalf("unexpected profile request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	initial := model.HomeModel{AuthConfigured: true, ActiveAgent: "swarm", ActiveAgentExitPlanMode: true, ModelProvider: "codex", ModelName: "old-model"}
	page = ui.NewHomePage(initial)
	app := &App{api: testAPIWithToken(server.URL), home: page, homeModel: initial, route: "home"}
	if err := app.selectHomeModelProfile("focus"); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if app.homeModel.DefaultModelProfileID != "focus" || app.homeModel.ActiveModelProfile.ProfileID != "focus" {
		t.Fatalf("profile state not updated: %#v", app.homeModel.ActiveModelProfile)
	}
	if !footerUpdatedBeforeRefresh {
		t.Fatal("homepage footer did not update from the confirmed profile before collection refresh")
	}
	provider, modelName, thinking, _, _ := page.ModelState()
	if provider != "codex" || modelName != "gpt-focus" || thinking != "high" {
		t.Fatalf("footer model state = (%q, %q, %q), want selected default", provider, modelName, thinking)
	}
	intent := page.SessionIntent()
	if intent.Profile.ProfileID != "focus" || intent.Preference.Provider != "codex" || intent.Preference.Model != "gpt-focus" || intent.Preference.Thinking != "high" {
		t.Fatalf("new-session intent not updated: profile=%#v preference=%#v", intent.Profile, intent.Preference)
	}
	selections := app.v3ChatDraftModeSelections()
	for _, mode := range []string{"plan", "auto"} {
		selection := selections[mode]
		if selection.ModelProfile == nil || selection.ModelProfile.SavedProfileID != "focus" || selection.Preference.Model != "gpt-focus" {
			t.Fatalf("%s /new selection not updated: %#v", mode, selection)
		}
	}
}

func TestAgentsModalCanonicalSaveFailureKeepsModalOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	page := ui.NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	api := client.New(server.URL)
	api.SetToken("test-token")
	app := &App{api: api, home: page, route: "home"}
	assignment := client.AgentModelAssignment{Provider: "codex", Model: "finder", Thinking: "high"}
	app.handleAgentsModalAction(ui.AgentsModalAction{Kind: ui.AgentsModalActionSave, Agent: "finder", Assignment: &assignment})
	if !page.AgentsModalVisible() {
		t.Fatal("failed canonical save closed Agents modal")
	}
}
