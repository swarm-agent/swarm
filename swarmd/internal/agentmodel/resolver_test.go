package agentmodel

import (
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestResolveSystemAgentUsesConfiguredAccountModelForAllOnboardingAgents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure compiled agents: %v", err)
	}
	models := model.NewService(pebblestore.NewModelStore(store), events, model.NewCatalogService(pebblestore.NewModelCatalogStore(store)))
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatalf("ensure model defaults: %v", err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	configured := uisettings.CompactAgentSettings{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}
	if _, err := settings.SetForAccount("account-a", uisettings.UISettings{Agents: uisettings.AgentSettings{
		Compact:  configured,
		Finder:   configured,
		Coder:    configured,
		Designer: configured,
		Router:   configured,
	}}); err != nil {
		t.Fatalf("configure agent models: %v", err)
	}

	for _, agentID := range []string{
		agentruntime.CompactAgentID,
		agentruntime.FinderAgentID,
		agentruntime.CoderAgentID,
		agentruntime.DesignerAgentID,
		agentruntime.RouterAgentID,
	} {
		t.Run(agentID, func(t *testing.T) {
			resolved, profile, err := ResolveSystemAgent(models, agents, settings, "account-a", agentID, "")
			if err != nil {
				t.Fatalf("resolve %s: %v", agentID, err)
			}
			if resolved.Preference.Provider != "codex" || resolved.Preference.Model != "gpt-5.4" {
				t.Fatalf("resolved preference = %#v, want configured codex/gpt-5.4", resolved.Preference)
			}
			if profile.Name != agentID || profile.Provider != "codex" || profile.Model != "gpt-5.4" {
				t.Fatalf("compiled profile = %#v, want %s with configured model", profile, agentID)
			}
		})
	}
}

func TestResolveSystemAgentRejectsMissingConfiguredModel(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure compiled agents: %v", err)
	}
	models := model.NewService(pebblestore.NewModelStore(store), events, model.NewCatalogService(pebblestore.NewModelCatalogStore(store)))
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(store))

	_, _, err = ResolveSystemAgent(models, agents, settings, "account-a", agentruntime.FinderAgentID, "")
	if err == nil || !strings.Contains(err.Error(), "Finder provider and model settings are required") {
		t.Fatalf("missing Finder settings error = %v", err)
	}
}
