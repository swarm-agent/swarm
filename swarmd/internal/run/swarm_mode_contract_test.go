package run

import (
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSwarmModeAvailableOnlyToCompiledSwarmPrimary(t *testing.T) {
	svc := &Service{tools: tool.NewRuntime(1)}
	swarm := agentruntime.SwarmAgentProfileForContext(pebblestore.AgentProfile{})
	resolved, _, disabled, err := svc.ResolveAgentToolContract(swarm)
	if err != nil {
		t.Fatalf("ResolveAgentToolContract(Swarm): %v", err)
	}
	if !resolved.Tools["swarm_mode"].Enabled || disabled["swarm_mode"] {
		t.Fatalf("Swarm swarm_mode resolution = %+v disabled=%v", resolved.Tools["swarm_mode"], disabled)
	}

	for _, profile := range []pebblestore.AgentProfile{
		agentruntime.RouterAgentProfileForParent(swarm),
		agentruntime.FinderAgentProfileForParent(swarm),
		agentruntime.CoderAgentProfileForParent(swarm),
		agentruntime.DesignerAgentProfileForParent(swarm),
		{Name: "other-primary", Mode: agentruntime.ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"swarm_mode": {Enabled: pebblestore.BoolPtr(true)}}}},
		{Name: agentruntime.SwarmAgentID, Mode: agentruntime.ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, Protected: false, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"swarm_mode": {Enabled: pebblestore.BoolPtr(true)}}}},
	} {
		resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
		if err != nil {
			t.Fatalf("ResolveAgentToolContract(%s): %v", profile.Name, err)
		}
		if resolved.Tools["swarm_mode"].Enabled || !disabled["swarm_mode"] {
			t.Fatalf("profile %s inherited swarm_mode: %+v disabled=%v", profile.Name, resolved.Tools["swarm_mode"], disabled)
		}
	}
}

func TestMasterHarnessDocumentsSwarmModeSafetyContracts(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, required := range []string{"Use swarm_mode only", "groups expansion in tens", "owned_scope_template", "canonical task launch path", "Coder clean-parent/isolated-worktree/scoped-commit", "manage_worktree integrate"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("master harness missing %q", required)
		}
	}
}
