package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestEnrichSystemAgentModelsAppliesDesignerSingleModelOverride(t *testing.T) {
	state := client.AgentState{Profiles: []client.AgentProfile{{Name: "system-designer", Provider: "codex", Model: "utility-default", Thinking: "low", Protected: true}}}
	settings := client.UISettings{Agents: client.UIAgentSettings{Designer: client.UICompactAgentSettings{Provider: "anthropic", Model: "claude-sonnet", Thinking: "high", ServiceTier: "priority"}}}

	got := enrichSystemAgentModels(state, settings, model.HomeModel{})
	if len(got.Profiles) != 1 {
		t.Fatalf("profiles = %#v", got.Profiles)
	}
	profile := got.Profiles[0]
	if profile.Provider != "anthropic" || profile.Model != "claude-sonnet" || profile.Thinking != "high" || profile.AutoServiceTier != "priority" || profile.ModelMode != "single" || !profile.Protected {
		t.Fatalf("Designer model override = %#v", profile)
	}
}
