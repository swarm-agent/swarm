package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestMapCanonicalAgentModelSettingsPreservesDaemonAuthority(t *testing.T) {
	settings := client.AgentModelSettings{
		Swarm: client.SwarmAgentModelAssignments{
			Action: client.AgentModelAssignment{Provider: "codex", Model: "action", Thinking: "high"},
			Plan:   client.AgentModelAssignment{Provider: "anthropic", Model: "plan", Thinking: "high"},
		},
		SystemAgents: client.SystemAgentModelAssignments{
			Compact: client.AgentModelAssignment{Provider: "codex", Model: "compact", Thinking: "low"},
			Router:  client.AgentModelAssignment{Provider: "codex", Model: "router", Thinking: "high"},
		},
	}
	resolved := providerModelResolverResult{
		ProviderIDs:     []string{"codex", "anthropic"},
		ModelsByProvider: map[string][]string{"codex": {"action", "compact", "router"}},
		CatalogByKey: map[string]client.ModelCatalogRecord{
			"codex/action": {Provider: "codex", Model: "action"},
		},
	}

	got := mapCanonicalAgentModelSettings(settings, resolved)
	if got.Settings.Swarm.Action.Model != "action" || got.Settings.Swarm.Plan.Model != "plan" {
		t.Fatalf("Swarm settings = %#v", got.Settings.Swarm)
	}
	if got.Settings.SystemAgents.Router.Model != "router" {
		t.Fatalf("Router settings = %#v", got.Settings.SystemAgents.Router)
	}
	if len(got.Providers) != 2 || got.Providers[0] != "anthropic" || got.Providers[1] != "codex" {
		t.Fatalf("providers = %#v", got.Providers)
	}
}
