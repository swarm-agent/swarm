package run

import (
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestModeCapabilityInstructionsUseExecutionModeWhenPlanModeDisabled(t *testing.T) {
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "explorer",
		Mode:                "subagent",
		ExecutionSetting:    pebblestore.AgentExecutionSettingRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
	})

	instructions := modeCapabilityInstructions(sessionruntime.ModePlan, false, profile)

	for _, want := range []string{
		"Current execution mode: read.",
		"Execution mode is controlled by the saved agent execution_setting because plan mode is disabled for this agent.",
		"Current agent runtime contract: read.",
		"Current agent exit-plan-mode enabled: false.",
		"With plan mode disabled, the backend uses the execution setting as the effective runtime mode.",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q\n--- instructions ---\n%s", want, instructions)
		}
	}
	for _, forbidden := range []string{
		"Current session mode: plan.",
		"Plan-mode expectation:",
		"Because the current session mode is plan",
	} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("instructions unexpectedly contain %q\n--- instructions ---\n%s", forbidden, instructions)
		}
	}
}

func TestModeCapabilityInstructionsUseSessionModeWhenPlanModeEnabled(t *testing.T) {
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "swarm",
		Mode:                "primary",
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
	})

	instructions := modeCapabilityInstructions(sessionruntime.ModePlan, false, profile)

	for _, want := range []string{
		"Current session mode: plan.",
		"Current agent runtime contract: plan -> auto",
		"Current agent exit-plan-mode enabled: true.",
		"Plan-mode expectation:",
		"Because the current session mode is plan",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q\n--- instructions ---\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "Current execution mode:") {
		t.Fatalf("plan-enabled instructions should not advertise static execution mode\n--- instructions ---\n%s", instructions)
	}
}
