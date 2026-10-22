package run

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestOrchestratorKeepsMachineToolsFromAStaleProfile(t *testing.T) {
	known := map[string]struct{}{"read": {}}
	for _, definition := range pebblestore.MachineClientTools() {
		known[definition.Name] = struct{}{}
	}
	stale := pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}}
	resolved, err := resolveAgentToolStates(stale, known)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range pebblestore.MachineClientTools() {
		if !resolved.Tools[definition.Name].Enabled {
			t.Fatalf("orchestrator lacks %s", definition.Name)
		}
	}
	other, err := resolveAgentToolStates(pebblestore.AgentProfile{Name: "my-agent", ToolContract: stale.ToolContract}, known)
	if err != nil {
		t.Fatal(err)
	}
	if other.Tools[pebblestore.MachineToolRun].Enabled {
		t.Fatal("another agent got machine_run")
	}
}

func TestOrchestratorHasNoOldArtifactTools(t *testing.T) {
	known := map[string]struct{}{"manage_artifact": {}, "manage_video": {}}
	stale := pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"manage_artifact": {Enabled: pebblestore.BoolPtr(true)}, "manage_video": {Enabled: pebblestore.BoolPtr(true)}}}}
	resolved, err := resolveAgentToolStates(stale, known)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Tools["manage_artifact"].Enabled || resolved.Tools["manage_video"].Enabled {
		t.Fatal("orchestrator kept the old artifact tools")
	}
}
