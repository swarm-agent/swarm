package app

import (
	"net/http"
	"net/http/httptest"
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
