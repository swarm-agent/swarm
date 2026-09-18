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
		ProviderIDs:      []string{"codex", "anthropic"},
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

func TestMapCanonicalAgentModelSettingsFiltersUnauthedProviders(t *testing.T) {
	settings := client.AgentModelSettings{
		Swarm: client.SwarmAgentModelAssignments{
			Action: client.AgentModelAssignment{Provider: "google", Model: "gemini-3.8-flash", Thinking: "medium"},
			Plan:   client.AgentModelAssignment{Provider: "fireworks", Model: "deepseek-v4p1-flash", Thinking: "high"},
		},
		SystemAgents: client.SystemAgentModelAssignments{
			Compact:  client.AgentModelAssignment{Provider: "fireworks", Model: "glm-5p3-flash", Thinking: "high"},
			Finder:   client.AgentModelAssignment{Provider: "fireworks", Model: "glm-5p3-flash", Thinking: "high"},
			Coder:    client.AgentModelAssignment{Provider: "fireworks", Model: "glm-5p3-flash", Thinking: "high"},
			Designer: client.AgentModelAssignment{Provider: "fireworks", Model: "glm-5p3-flash", Thinking: "high"},
			Router:   client.AgentModelAssignment{Provider: "google", Model: "gemini-3.5-flash-lite", Thinking: "medium"},
		},
	}
	resolved := providerModelResolverResult{
		ProviderIDs: []string{"anthropic", "codex", "fireworks", "google", "openai", "openrouter"},
		ProviderStatuses: map[string]client.ProviderStatus{
			"google":     {ID: "google", Ready: true, Runnable: true},
			"fireworks":  {ID: "fireworks", Ready: true, Runnable: true},
			"anthropic":  {ID: "anthropic", Ready: false, Runnable: false, Reason: "missing key"},
			"codex":      {ID: "codex", Ready: false, Runnable: false, Reason: "missing auth"},
			"openai":     {ID: "openai", Ready: false, Runnable: false, Reason: "missing key"},
			"openrouter": {ID: "openrouter", Ready: false, Runnable: false, Reason: "missing key"},
		},
		ModelsByProvider: map[string][]string{
			"google":     {"gemini-3.8-flash", "gemini-3.5-flash-lite"},
			"fireworks":  {"deepseek-v4p1-flash", "glm-5p3-flash"},
			"anthropic":  {"claude-3-5-sonnet"},
			"openai":     {"gpt-5.4"},
			"openrouter": {"deepseek/chat"},
		},
		CatalogByKey: map[string]client.ModelCatalogRecord{
			"google/gemini-3.8-flash":       {Provider: "google", Model: "gemini-3.8-flash"},
			"fireworks/deepseek-v4p1-flash": {Provider: "fireworks", Model: "deepseek-v4p1-flash"},
			"anthropic/claude-3-5-sonnet":   {Provider: "anthropic", Model: "claude-3-5-sonnet"},
		},
	}

	got := mapCanonicalAgentModelSettings(settings, resolved)

	// Unauthed providers (anthropic, codex, openai, openrouter) should NOT be in Providers list
	expectedProviders := []string{"fireworks", "google"}
	if len(got.Providers) != len(expectedProviders) {
		t.Fatalf("got providers = %#v, want %#v", got.Providers, expectedProviders)
	}
	for i, p := range expectedProviders {
		if got.Providers[i] != p {
			t.Fatalf("provider[%d] = %q, want %q", i, got.Providers[i], p)
		}
	}

	// ModelsByProvider should only contain models for authed providers
	if _, ok := got.ModelsByProvider["openai"]; ok {
		t.Fatalf("ModelsByProvider contains unauthed provider openai: %#v", got.ModelsByProvider)
	}
	if _, ok := got.ModelsByProvider["openrouter"]; ok {
		t.Fatalf("ModelsByProvider contains unauthed provider openrouter: %#v", got.ModelsByProvider)
	}
	if _, ok := got.ModelsByProvider["codex"]; ok {
		t.Fatalf("ModelsByProvider contains unauthed provider codex: %#v", got.ModelsByProvider)
	}

	// Preserved assignments: if an assignment currently has an unauthed provider, it is preserved
	settingsWithAnthropic := settings
	settingsWithAnthropic.SystemAgents.Compact = client.AgentModelAssignment{
		Provider: "anthropic", Model: "claude-3-5-sonnet", Thinking: "high",
	}
	gotPreserved := mapCanonicalAgentModelSettings(settingsWithAnthropic, resolved)
	expectedWithAnthropic := []string{"anthropic", "fireworks", "google"}
	if len(gotPreserved.Providers) != len(expectedWithAnthropic) {
		t.Fatalf("got providers = %#v, want %#v", gotPreserved.Providers, expectedWithAnthropic)
	}
	if models := gotPreserved.ModelsByProvider["anthropic"]; len(models) != 1 || models[0] != "claude-3-5-sonnet" {
		t.Fatalf("anthropic models = %#v, want [claude-3-5-sonnet]", models)
	}
}
