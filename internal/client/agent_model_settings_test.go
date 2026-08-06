package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentModelSettingsClientUsesCanonicalGetAndPatch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/agent-model-settings" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if requests == 1 && r.Method != http.MethodGet {
			t.Fatalf("first method = %s", r.Method)
		}
		if requests == 2 {
			if r.Method != http.MethodPatch {
				t.Fatalf("second method = %s", r.Method)
			}
			var patch AgentModelSettingsPatch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			if patch.Swarm == nil || patch.Swarm.Action.Model != "action-2" || patch.Swarm.Plan.Model != "plan" {
				t.Fatalf("patch = %#v", patch)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"agent_model_settings":{"swarm":{"action":{"provider":"codex","model":"action","thinking":"high"},"plan":{"provider":"codex","model":"plan","thinking":"high"}},"system_agents":{"compact":{"provider":"codex","model":"compact","thinking":"low"},"finder":{"provider":"codex","model":"finder","thinking":"high"},"coder":{"provider":"codex","model":"coder","thinking":"high"},"designer":{"provider":"codex","model":"designer","thinking":"high"},"router":{"provider":"codex","model":"router","thinking":"high"}},"updated_at":1}}`))
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	settings, err := api.GetAgentModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.SystemAgents.Router.Model != "router" {
		t.Fatalf("router = %#v", settings.SystemAgents.Router)
	}
	settings, err = api.PatchAgentModelSettings(context.Background(), AgentModelSettingsPatch{Swarm: &AgentModelSettingsSwarmPatch{
		Action: AgentModelAssignment{Provider: "codex", Model: "action-2", Thinking: "high"},
		Plan:   settings.Swarm.Plan,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Swarm.Action.Model != "action" {
		t.Fatalf("response action = %#v", settings.Swarm.Action)
	}
}
