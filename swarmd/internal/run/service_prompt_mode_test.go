package run

import (
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
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

func TestProviderRequestRuntimeContextUsesResolvedIdentityAndUTC(t *testing.T) {
	now := time.Date(2026, time.July, 12, 9, 8, 7, 0, time.FixedZone("test", 2*60*60))
	base := provideriface.Request{
		ProviderLineageID: "stable-lineage",
		ProviderCacheKey:  "stable-cache",
		Model:             "gpt-5",
		Instructions:      "base instructions",
	}

	req := base.WithRuntimeContext("codex", now)
	for _, want := range []string{
		"[request-runtime-context]",
		"current_utc_time: 2026-07-12T07:08:07Z",
		"current_provider: codex",
		"current_model: gpt-5",
	} {
		if !strings.Contains(req.Instructions, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, req.Instructions)
		}
	}
	if strings.Contains(req.Instructions, "provider_model_change:") {
		t.Fatalf("normal request unexpectedly reports a model change:\n%s", req.Instructions)
	}
	if req.ProviderLineageID != base.ProviderLineageID || req.ProviderCacheKey != base.ProviderCacheKey {
		t.Fatalf("runtime context changed stable lineage/cache identity: %#v", req)
	}
}

func TestProviderRequestRuntimeContextReportsVerifiedModelChange(t *testing.T) {
	req := provideriface.Request{
		ProviderLineageID:         "new-lineage",
		PreviousProviderLineageID: "old-lineage",
		PreviousProviderID:        "anthropic",
		PreviousModel:             "claude-sonnet-4",
		NewProviderID:             "codex",
		NewModel:                  "gpt-5",
		Model:                     "gpt-5",
	}.WithRuntimeContext("codex", time.Date(2026, time.July, 12, 7, 8, 7, 0, time.UTC))

	for _, want := range []string{
		"provider_model_change: The resolved provider/model changed for this request.",
		"previous_provider: anthropic",
		"previous_model: claude-sonnet-4",
		"current_provider: codex",
		"current_model: gpt-5",
	} {
		if !strings.Contains(req.Instructions, want) {
			t.Fatalf("changed-model context missing %q:\n%s", want, req.Instructions)
		}
	}
}

func TestMasterHarnessRoutesAgentProgressToPlanManageAndKeepsTodosUserOwned(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")

	for _, want := range []string{
		"For multi-step implementation work, keep durable task state current",
		"batch all tasks completed since the last update with subtask_ids",
		"combine the final task transitions and checkpoint completion via complete_subtask complete_checkpoint=true",
		"do not waste a second tool call",
		"discovery do not count as completed task progress by themselves",
		"single concrete task, skip intermediate progress churn",
		"Preserve manage_todos as the user-owned workspace todo surface",
		"Do not use manage_todos for agent execution checklists or checkpoint progress",
		"In automatic execution, keep solving acceptance gaps that are resolvable with the available tools",
		"Discovering more work, scope growth, a missing interface/API or implementation, uncertainty, or an incomplete/failed first approach is not by itself a reason to stop",
		"Use mark_needs_review only when user or audit judgment is inherently required",
		"mark_blocked only for a named external dependency/input/unavailable permission",
		"mark_failed only for a nonrecoverable execution error",
		"plan_manage terminal checkpoint example",
		"On a blocked plan, call request_followup_checkpoint directly",
		"classify it before choosing a lifecycle action",
		"same deliverable",
		"complete replacement checkpoint_title, tasks, acceptance_criteria, and notes",
		"Never restart the unchanged checkpoint after requirement-changing feedback",
		"independent deliverable or separate task",
		"plan_manage requirement-changing restart example",
		"plain restart without change_request only for a true retry with unchanged requirements",
		"do not call resolve_blocked_checkpoint first",
		"Failed checkpoints remain stopped",
		"use manage-sessions deploy; do not use the task tool",
		"suggest a short worktree_name",
		"leave managed worktree isolation enabled by default",
		"approval UI lets them disable worktree isolation",
		"explicitly asks to use subagents",
		"names the agent or agents to run",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master harness prompt missing %q\n--- prompt ---\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"do not issue repeated complete_subtask calls before it",
		"do not call complete_subtask repeatedly first",
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
