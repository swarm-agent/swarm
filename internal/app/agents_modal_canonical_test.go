package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestCanonicalAgentsModalStateUsesDesktopSystemSubagents(t *testing.T) {
	state := client.AgentState{
		Profiles: []client.AgentProfile{
			{Name: "swarm", Mode: "primary", Enabled: true},
			{Name: "custom-primary", Mode: "primary", Enabled: true},
			{Name: "random-subagent", Mode: "subagent", Enabled: true},
			{Name: "system-plan-sidechat", Mode: "subagent", Enabled: true},
			{Name: "system-finder", Mode: "subagent", Provider: "wrong", Model: "wrong", Enabled: true},
		},
	}
	settings := client.UISettings{Agents: client.UIAgentSettings{
		Compact:  client.UICompactAgentSettings{Provider: "codex", Model: "compact-model", Thinking: "low"},
		Finder:   client.UICompactAgentSettings{Provider: "anthropic", Model: "finder-model", Thinking: "medium"},
		Coder:    client.UICompactAgentSettings{Provider: "codex", Model: "coder-model", Thinking: "high"},
		Designer: client.UICompactAgentSettings{Provider: "google", Model: "designer-model", Thinking: "high"},
	}}

	got := canonicalAgentsModalState(state, settings)
	wantNames := []string{"swarm", "custom-primary", "system-compact", "system-finder", "system-coder", "system-designer"}
	if len(got.Profiles) != len(wantNames) {
		t.Fatalf("canonical profiles = %#v, want names %#v", got.Profiles, wantNames)
	}
	for i, want := range wantNames {
		if got.Profiles[i].Name != want {
			t.Fatalf("canonical profile[%d] = %q, want %q", i, got.Profiles[i].Name, want)
		}
	}
	for _, profile := range got.Profiles[2:] {
		if profile.Mode != "subagent" || profile.ModelMode != "single" || !profile.Enabled || !profile.Protected {
			t.Fatalf("compiled subagent contract = %#v", profile)
		}
	}
	if got.Profiles[3].Provider != "anthropic" || got.Profiles[3].Model != "finder-model" {
		t.Fatalf("finder settings = %#v, want UI settings authority", got.Profiles[3])
	}
}

func TestCompiledSystemAgentNamesCoverCanonicalIDs(t *testing.T) {
	for _, name := range []string{"system-compact", "system-finder", "system-coder", "system-designer"} {
		matched := isCompactSystemAgentName(name) || isFinderSystemAgentName(name) || isCloneSystemAgentName(name) || isDesignerSystemAgentName(name)
		if !matched {
			t.Fatalf("canonical compiled subagent %q was not recognized", name)
		}
	}
}
