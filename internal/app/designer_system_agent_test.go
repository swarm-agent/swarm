package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestCanonicalAgentSettingsIncludeDesignerAndRouter(t *testing.T) {
	settings := client.AgentModelSettings{SystemAgents: client.SystemAgentModelAssignments{
		Designer: client.AgentModelAssignment{Provider: "google", Model: "designer-model", Thinking: "high"},
		Router:   client.AgentModelAssignment{Provider: "codex", Model: "router-model", Thinking: "high"},
	}}
	got := mapCanonicalAgentModelSettings(settings, providerModelResolverResult{})
	if got.Settings.SystemAgents.Designer.Model != "designer-model" {
		t.Fatalf("designer = %#v", got.Settings.SystemAgents.Designer)
	}
	if got.Settings.SystemAgents.Router.Model != "router-model" {
		t.Fatalf("router = %#v", got.Settings.SystemAgents.Router)
	}
}
