package app

import (
	"encoding/json"
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

func TestSelectHomeModelProfileRejectsMissingFavoriteWithoutWrites(t *testing.T) {
	// Purpose: selectHomeModelProfile may only apply a loaded account favorite;
	// an unknown ID must not fall back to the retired account-default profile API.
	// The TUI HTTP boundary proves rejection and absence of writes.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unknown favorite caused request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	initial := model.HomeModel{ActiveAgent: "swarm", ModelName: "old-model"}
	page := ui.NewHomePage(initial)
	app := &App{api: testAPIWithToken(server.URL), home: page, homeModel: initial, route: "home"}
	if err := app.selectHomeModelProfile("unknown"); err == nil {
		t.Fatal("missing favorite was accepted")
	}
	if app.homeModel.ModelName != "old-model" || app.homeModel.DefaultModelProfileID != "" {
		t.Fatal("rejected favorite changed home state")
	}
}

func TestSelectHomeModelProfileUpdatesSwarmActionAndPreservesPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent-model-settings":
			_, _ = w.Write([]byte(`{"agent_model_settings":{"swarm":{"action":{"provider":"codex","model":"old-action","thinking":"medium"},"plan":{"provider":"codex","model":"plan-model","thinking":"xhigh"}},"system_agents":{},"updated_at":1}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agent-model-settings":
			var patch client.AgentModelSettingsPatch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			if patch.Swarm == nil || patch.Swarm.Action.Model != "gpt-focus" || patch.Swarm.Plan.Model != "plan-model" {
				t.Fatalf("canonical model patch = %#v", patch)
			}
			_, _ = w.Write([]byte(`{"agent_model_settings":{"swarm":{"action":{"provider":"codex","model":"gpt-focus","thinking":"high"},"plan":{"provider":"codex","model":"plan-model","thinking":"xhigh"}},"system_agents":{},"updated_at":2}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	profiles := []client.ModelProfile{{ProfileID: "focus", Name: "Focus", Provider: "codex", Model: "gpt-focus", Thinking: "high"}}
	initial := model.HomeModel{AuthConfigured: true, ActiveAgent: "swarm", ActiveAgentExitPlanMode: true, ModelProfiles: profiles}
	page := ui.NewHomePage(initial)
	app := &App{api: testAPIWithToken(server.URL), home: page, homeModel: initial, route: "home"}
	if err := app.selectHomeModelProfile("focus"); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	page.SetSessionMode("auto")
	_, actionModel, _, _, _ := page.ModelState()
	page.SetSessionMode("plan")
	_, planModel, _, _, _ := page.ModelState()
	if actionModel != "gpt-focus" || planModel != "plan-model" {
		t.Fatalf("mode models = action %q plan %q", actionModel, planModel)
	}
	if app.homeModel.ActiveModelProfile.Source != "agent-default" || app.homeModel.DefaultModelProfileID != "" {
		t.Fatalf("favorite became a session/default profile authority: %#v", app.homeModel.ActiveModelProfile)
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
