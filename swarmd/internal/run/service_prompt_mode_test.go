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
		"Execution mode is controlled by the saved agent runtime_mode because plan mode is disabled for this agent.",
		"Current agent runtime contract: read.",
		"Current agent exit-plan-mode enabled: false.",
		"With plan mode disabled, the backend uses runtime_mode as the effective runtime contract.",
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
		"Current agent runtime contract: plan_auto",
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

func TestMasterHarnessRoutesAgentProgressToPlanManageAndKeepsTodosUserOwned(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")

	for _, want := range []string{
		"For multi-step implementation work, keep agent execution progress in `plan_manage` on the active plan/checkpoint",
		"Preserve manage_todos as the user-owned workspace todo surface",
		"Do not use manage_todos for agent execution checklists or checkpoint progress",
		"plan_manage checkpoint checklist example",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master harness prompt missing %q\n--- prompt ---\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"owner_kind=agent",
		`"owner_kind":"agent"`,
		"manage_todos (agent checklist",
		"execution checklist in `manage_todos",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("master harness prompt still advertises manage_todos agent tracking via %q\n--- prompt ---\n%s", forbidden, prompt)
		}
	}
}
