package agentmodel

import (
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveSystemAgentUsesCanonicalAccountModelForAllOnboardingAgents(t *testing.T) {
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
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	configured := pebblestore.AgentModelAssignment{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}
	if _, err := settingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account-a",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured,
		},
	}); err != nil {
		t.Fatalf("configure agent models: %v", err)
	}
	settings := agentmodelsettings.NewService(settingsStore)

	for _, agentID := range []string{
		agentruntime.CompactAgentID,
		agentruntime.FinderAgentID,
		agentruntime.CoderAgentID,
		agentruntime.DesignerAgentID,
		agentruntime.ImageAgentID,
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

func TestResolveSystemAgentRejectsMissingCanonicalRecord(t *testing.T) {
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
	settings := agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(store))

	_, _, err = ResolveSystemAgent(models, agents, settings, "account-a", agentruntime.FinderAgentID, "")
	if err == nil || !strings.Contains(err.Error(), "agent model settings not found") {
		t.Fatalf("missing canonical settings error = %v", err)
	}
}
