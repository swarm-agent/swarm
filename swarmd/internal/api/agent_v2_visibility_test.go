package api

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
