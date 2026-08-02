package api

import (
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPublicAgentProfilesUseCanonicalSystemAgentSettings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-model-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure compiled agent defaults: %v", err)
	}
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	settings := testAgentModelSettingsRecord("account-one")
	settings.SystemAgents.Coder = pebblestore.AgentModelAssignment{Provider: "codex", Model: "coder-model", Thinking: "xhigh", ServiceTier: "fast", ContextMode: "full"}
	if _, err := settingsStore.PutForAccount(settings); err != nil {
		t.Fatalf("seed canonical agent model settings: %v", err)
	}
	server := &Server{agents: agents, agentModelSettings: agentmodelsettings.NewService(settingsStore)}
	profiles, err := server.publicAgentProfiles("account-one", nil)
	if err != nil {
		t.Fatalf("project public agent profiles: %v", err)
	}
	for _, profile := range profiles {
		if profile.Name == agentruntime.CoderAgentID {
			if profile.Provider != "codex" || profile.Model != "coder-model" || profile.Thinking != "xhigh" || profile.ContextMode != "full" || profile.ToolContract == nil {
				t.Fatalf("canonical compiled Coder projection = %+v", profile)
			}
			return
		}
	}
	t.Fatalf("compiled Coder profile missing: %+v", profiles)
}

func TestCompactAgentStateForDesktopHidesSystemAgentsFromSummary(t *testing.T) {
	state := agentruntime.State{Profiles: []pebblestore.AgentProfile{
		{Name: agentruntime.SwarmAgentID, Mode: agentruntime.ModePrimary, Enabled: true},
		{Name: agentruntime.PlanSidechatAgentID, Mode: agentruntime.ModeSubagent, Enabled: true},
		{Name: agentruntime.AISidechatAgentID, Mode: agentruntime.ModeSubagent, Enabled: true},
		{Name: agentruntime.CompactAgentID, Mode: agentruntime.ModeSubagent, Enabled: true},
		{Name: "custom", Mode: agentruntime.ModeSubagent, Enabled: true},
	}}
	got := compactAgentStateForDesktop(state)
	profiles, ok := got["profiles"].([]compactAgentProfileForDesktop)
	if !ok {
		t.Fatalf("profiles type = %T", got["profiles"])
	}
	if len(profiles) != 2 || profiles[0].Name != agentruntime.SwarmAgentID || profiles[1].Name != "custom" {
		t.Fatalf("visible profiles = %+v", profiles)
	}
}
