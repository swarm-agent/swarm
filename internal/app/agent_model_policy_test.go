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

func TestCycleThinkingBlockedByLockedAgentModelPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request while agent model is locked: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	policy := client.SessionV3AgentModelPolicy{
		AgentName: "swarm",
		Source:    "agent_preset",
		Locked:    true,
		Reason:    "locked by test policy",
		Preference: client.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "high",
		},
	}
	homeModel := model.HomeModel{
		ModelProvider: "codex",
		ModelName:     "gpt-5.4",
		ThinkingLevel: "high",
		RecentSessions: []model.SessionSummary{{
			ID:         "session-locked",
			SessionAPI: "v3",
			Metadata:   map[string]any{"v3_agent_model_policy": policy},
		}},
	}
	app := &App{
		api:          testAPIWithToken(server.URL),
		route:        "chat",
		homeModel:    homeModel,
		home:         ui.NewHomePage(homeModel),
		streamEvents: make(chan client.StreamEventEnvelope, 1),
	}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-locked",
		SessionMode:    "auto",
		AuthConfigured: true,
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		Meta:           ui.ChatSessionMeta{Agent: "swarm"},
	})

	app.cycleThinkingLevel()

	if called {
		t.Fatalf("model API was called despite locked agent policy")
	}
	if got := app.chat.Status(); !strings.Contains(got, "locked by test policy") {
		t.Fatalf("chat status = %q, want lock reason", got)
	}
}
