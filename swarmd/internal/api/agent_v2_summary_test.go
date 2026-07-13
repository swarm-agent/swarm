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
			ModelMode:           "split",
			PlanProvider:        "codex",
			PlanModel:           "gpt-5.4",
			PlanThinking:        "high",
			PlanServiceTier:     "fast",
			AutoProvider:        "codex",
			AutoModel:           "gpt-5.4-mini",
			AutoThinking:        "medium",
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
	for _, required := range []string{"profiles", "active_primary", "swarm", "runtime_mode", "plan_auto", "default_session_mode", "auto", "enabled", "model_mode", "split", "plan_provider", "plan_model", "plan_thinking", "plan_service_tier", "auto_provider", "auto_model", "auto_thinking", "auto_service_tier", "gpt-5.4", "gpt-5.4-mini", "fast", "flex"} {
		if !strings.Contains(body, required) {
			t.Fatalf("compact agent state missing %q: %s", required, body)
		}
	}
}
