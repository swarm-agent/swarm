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

// Purpose: resolveAgentToolStates must disable manage_artifact even when a
// custom or built-in contract explicitly enables it. This resolver-level test
// isolates the global override from the separate orchestrator override and
// proves unrelated explicit tool grants survive without mutating the contract.
func TestResolveAgentToolStatesDisablesExplicitManageArtifact(t *testing.T) {
	for _, name := range []string{"custom-media-agent", agentruntime.DesignerAgentID, agentruntime.ImageAgentID, agentruntime.VideoAgentID} {
		t.Run(name, func(t *testing.T) {
			profile := pebblestore.AgentProfile{Name: name, ToolContract: &pebblestore.AgentToolContract{
				Preset: "custom",
				Tools: map[string]pebblestore.AgentToolConfig{
					"manage_artifact": {Enabled: pebblestore.BoolPtr(true)},
					"read":            {Enabled: pebblestore.BoolPtr(true)},
				},
			}}
			resolved, err := resolveAgentToolStates(profile, map[string]struct{}{"manage_artifact": {}, "read": {}})
			if err != nil {
				t.Fatal(err)
			}
			artifact, found := resolved.Tools["manage_artifact"]
			if !found || artifact.Enabled || artifact.Source != "runtime.media_tasks" {
				t.Fatalf("artifact override missing: %+v", artifact)
			}
			if !resolved.Tools["read"].Enabled {
				t.Fatal("unrelated explicit grant disabled")
			}
			if !*profile.ToolContract.Tools["manage_artifact"].Enabled {
				t.Fatal("resolver mutated input contract")
			}
		})
	}
}
