package run

import (
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestModeCapabilityInstructionsUseExecutionModeWhenPlanModeDisabled(t *testing.T) {
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "finder",
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
		"Plan-mode Coder-wave assessment:",
		"do not overload one Coder with several independent systems or deliverables",
		"do not split a cohesive change into tiny artificial assignments",
		"do not create a separate dependency graph, planning file, wave manifest artifact, or orchestration document",
		"Coders in the same wave must have dependency-ready, non-overlapping owned scopes",
		"place them in sequential waves after parent integration",
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

func TestModeCapabilityInstructionsKeepCoderWavePlanningOutOfAutoMode(t *testing.T) {
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "swarm",
		Mode:                "primary",
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
	})

	instructions := modeCapabilityInstructions(sessionruntime.ModeAuto, false, profile)
	for _, forbidden := range []string{
		"Plan-mode Coder-wave assessment:",
		"do not create a separate dependency graph",
		"Coders in the same wave must have dependency-ready, non-overlapping owned scopes",
	} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("auto instructions unexpectedly contain plan-only wave guidance %q\n--- instructions ---\n%s", forbidden, instructions)
		}
	}
}

func TestMasterHarnessAllowsApprovedPlanCoderWavesWithoutGenericSessionDelegation(t *testing.T) {
	instructions := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"approved structured-plan checkpoint that calls for a dependency-ready Coder wave",
		"Concurrent Coder assignments must have non-overlapping owned scopes",
		"sequence dependent or overlapping implementation work into later waves after the parent integrates the prerequisite wave",
		"Never reinterpret a generic new-session request as delegation",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("master harness missing Coder-wave contract %q\n--- instructions ---\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "Intentional overlapping Coder scopes are allowed") {
		t.Fatalf("master harness still permits concurrent overlapping Coder scopes\n--- instructions ---\n%s", instructions)
	}
}

func TestModeCapabilityInstructionsUseInjectedPlanPresenceInsteadOfGetActiveProbe(t *testing.T) {
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "swarm",
		Mode:                "primary",
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
	})

	instructions := modeCapabilityInstructions(sessionruntime.ModeAuto, false, profile)
	for _, want := range []string{
		"active_plan_present field as the authoritative plan-existence signal",
		"do not call plan_manage get-active merely to probe for a plan",
		"use get-active only if full plan details are materially needed beyond the injected state",
		"When that state says an active plan exists, continue its scoped lifecycle",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("auto instructions missing %q\n--- instructions ---\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "If an active plan exists, use plan_manage get-active to inspect it") {
		t.Fatalf("auto instructions still encourage get-active as a startup probe\n--- instructions ---\n%s", instructions)
	}
}

func TestSubagentPolicyInstructionsUseSimplifiedContract(t *testing.T) {
	instructions := subagentPolicyInstructions(permission.DefaultSubagentPolicy())
	for _, want := range []string{
		"- mode: bounded",
		"- automatic_launches_per_parent_run: 5",
		"- active_child_limit: 5",
		"- over_budget_action: ask",
		"- require_write_isolation: true",
		"- delegation_scope: parent sessions only; child sessions cannot invoke task delegation",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("subagent policy instructions missing %q:\n%s", want, instructions)
		}
	}
	for _, want := range []string{"wave/task-call budget", "each accepted task call consumes one wave regardless of child count", "hard ceiling for both one task call and aggregate active children"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("subagent policy instructions do not explain independent wave and concurrency semantics; missing %q:\n%s", want, instructions)
		}
	}
	for _, removed := range []string{"absolute_wave_maximum", "max_depth"} {
		if strings.Contains(instructions, removed) {
			t.Fatalf("subagent policy instructions still contain removed field %q:\n%s", removed, instructions)
		}
	}
}

func TestAppendResolvedModelPolicyInstructionsReportsRequestFacts(t *testing.T) {
	instructions := AppendResolvedModelPolicyInstructions("base", sessionruntime.ModePlan, pebblestore.ModelPreference{
		Provider: "Codex", Model: "gpt-5.4", Thinking: "high", ServiceTier: "priority", ContextMode: "1m",
	})
	for _, want := range []string{
		"Resolved model policy (authoritative for this run):",
		"- session_mode: plan",
		"- provider: codex",
		"- model: gpt-5.4",
		"- thinking: high",
		"- service_tier: priority",
		"- context_mode: 1m",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instructions)
		}
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

	for _, forbidden := range []string{
		"Include or link the actual requested artifact in the assistant response before the terminal lifecycle call",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("master harness retained duplicate-completion instruction %q:\n%s", forbidden, prompt)
		}
	}

	for _, want := range []string{
		"For multi-step implementation work, keep durable task state current",
		"terminal plan_manage call is the single canonical user-visible completion",
		"Do not emit a text completion report before or after that terminal call",
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
		"plan_manage final checkpoint example",
		"handoff_overview",
		"Do not emit a separate assistant completion report before or after this call",
		"On a blocked plan, call request_followup_checkpoint directly",
		"Session mode=auto is not evidence that a plan exists", "when active_plan_present=false, never call request_followup_checkpoint",
		"start_session_checkpoint is the one atomic create-and-start operation", "do not call start_checkpoint afterward",
		"never precede it with request_followup_checkpoint or follow it with start_checkpoint",
		"original request explicitly requires a later AI, fresh context, a second checkpoint",
		"include every known checkpoint up front",
		"valid only from the parent conversation, never from a provider-managed checkpoint run",
		"do not call or retry request_followup_checkpoint: the backend rejects it",
		"never claim the checkpoint was added after a failed tool result",
		"classify it by its effect on the deliverable contract",
		"not by whether it is phrased as an imperative",
		"choose the least disruptive valid route",
		"inquiry or guidance only means answer or acknowledge without plan mutation",
		"localized additive patch whose existing checklist remains valid means add_subtask",
		"same-contract feedback that supersedes the checklist means replace_subtasks with the complete authoritative list",
		"Make the hero headline blue",
		"Add 8px below the card title",
		"checkpoint redefinition that invalidates the objective or acceptance criteria means restart_checkpoint",
		"independently shippable work or a separate review/failure boundary means request_followup_checkpoint",
		"preserving checkpoint identity and attempt history",
		"complete replacement checkpoint_title, tasks, acceptance_criteria, and notes",
		"call resolve_blocked_checkpoint with start_next=true",
		"the same checkpoint resumes in a fresh provider run",
		"never completes the blocked checkpoint and never selects a later checkpoint",
		"leave the checkpoint blocked and explain the exact resolution still needed",
		"Never restart an unchanged checkpoint merely to clear a block",
		"plan_manage add_subtask exact call shape",
		`"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"title":"Measure Swarm hosting capacity"}`,
		"subtask as a JSON object with a non-empty title",
		"Do not pass title at the top level",
		"do not pass subtask as bare text",
		"do not issue a partial call before this complete call",
		"continuing the same non-blocked/non-failed checkpoint without resetting its attempt history",
		"plan_manage requirement-changing restart example",
		"use restart only when feedback invalidates the current objective or acceptance criteria",
		"Use no plan mutation for inquiry/guidance, add_subtask for localized additive edits, replace_subtasks for a superseded same-contract checklist, and request_followup_checkpoint for independently shippable work",
		"do not call resolve_blocked_checkpoint first",
		"Failed checkpoints remain stopped",
		"Never use add_subtask to clear a blocked or failed checkpoint",
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
