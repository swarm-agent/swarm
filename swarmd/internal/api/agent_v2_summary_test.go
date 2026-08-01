package api

import (
	"encoding/json"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCompactAgentStateForDesktopOmitsSettingsOnlyPayload(t *testing.T) {
	compact := compactAgentStateForDesktop(agentruntime.State{
		Profiles: []pebblestore.AgentProfile{{
			Name:                "swarm",
			Mode:                "primary",
			Description:         "Swarm",
			Provider:            "codex",
			Model:               "gpt-5-codex",
			Thinking:            "medium",
			AutoServiceTier:     "flex",
			Prompt:              strings.Repeat("settings-only prompt ", 32),
			RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
			DefaultSessionMode:  pebblestore.AgentDefaultSessionModeAuto,
			ExecutionSetting:    "",
			ExitPlanModeEnabled: pebblestore.BoolPtr(true),
			ToolScope:           &pebblestore.AgentToolScope{Preset: "read_write"},
			ToolContract:        &pebblestore.AgentToolContract{Preset: "read_write"},
			Enabled:             true,
			UpdatedAt:           42,
		}},
		ActivePrimary:  "swarm",
		ActiveSubagent: map[string]string{},
		Version:        1,
	})

	encoded, err := json.Marshal(compact)
	if err != nil {
		t.Fatalf("marshal compact agent state: %v", err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"settings-only prompt", "prompt", "tool_contract", "tool_scope", "provider_defaults_preview", "tool_inventory"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("compact agent state contains %q: %s", forbidden, body)
		}
	}
	for _, forbidden := range []string{"model_mode", "plan_provider", "plan_model", "plan_thinking", "plan_service_tier", "auto_provider", "auto_model", "auto_thinking", "auto_service_tier", "flex"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("compact agent state retained split model field %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{"profiles", "swarm", "runtime_mode", "plan_auto", "default_session_mode", "auto", "enabled"} {
		if !strings.Contains(body, required) {
			t.Fatalf("compact agent state missing %q: %s", required, body)
		}
	}
}

func TestCompactAgentStateForDesktopMaterializesCompiledSwarmAsEnabled(t *testing.T) {
	compact := compactAgentStateForDesktop(agentruntime.State{
		Profiles: []pebblestore.AgentProfile{{
			Name:               agentruntime.SwarmAgentID,
			Mode:               agentruntime.ModePrimary,
			RuntimeMode:        pebblestore.AgentRuntimeModePlanAuto,
			DefaultSessionMode: pebblestore.AgentDefaultSessionModeAuto,
			Enabled:            false,
		}},
		ActivePrimary: agentruntime.SwarmAgentID,
	})

	profiles, ok := compact["profiles"].([]compactAgentProfileForDesktop)
	if !ok || len(profiles) != 1 {
		t.Fatalf("compact profiles = %#v, want one compiled swarm profile", compact["profiles"])
	}
	profile := profiles[0]
	if profile.Name != agentruntime.SwarmAgentID || !profile.Enabled || !profile.Protected {
		t.Fatalf("compact swarm profile = %+v, want enabled protected compiled profile", profile)
	}
}
