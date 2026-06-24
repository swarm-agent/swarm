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
			Prompt:              strings.Repeat("settings-only prompt ", 32),
			RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
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
	for _, required := range []string{"profiles", "active_primary", "swarm", "runtime_mode", "plan_auto", "enabled"} {
		if !strings.Contains(body, required) {
			t.Fatalf("compact agent state missing %q: %s", required, body)
		}
	}
}
