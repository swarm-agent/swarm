package run

import (
	"fmt"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestModeCapabilityInstructions(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		contains    []string
		notContains []string
	}{
		{
			name: "plan",
			mode: "plan",
			contains: []string{
				"Current session mode: plan.",
				"The current session mode above is authoritative for this turn and supersedes any earlier transcript text, tool output, or UI guidance that described a different mode.",
				"Session mode can be changed between turns; do not treat an earlier auto/plan state as permanent.",
				"Current agent runtime contract: plan -> auto (exit_plan_mode transitions an approved plan turn to auto; it does not make auto mode irreversible).",
				"Use ask-user only for true product/decision forks; do not use ask-user to request tool permissions.",
				"- tool availability is determined by plan mode until exit_plan_mode switches the session to auto.",
				"- exit_plan_mode is available for this agent, but still requires explicit approval and only succeeds from session plan mode.",
				"- plan_manage is available in both plan and auto to inspect or update saved plans; it does not change session mode.",
				"Keep refining the plan with plan_manage as needed. Call exit_plan_mode only when you want approval to leave plan mode. After approval, execution continues in auto on the same active plan/checklist, and plan_manage can still update it.",
				"Because the current session mode is plan, you may call exit_plan_mode when the plan is actionable even if earlier transcript text says the session already exited plan mode or that exit_plan_mode cannot be called from auto.",
			},
			notContains: []string{
				"- bash: requires explicit user approval before execution.",
			},
		},
		{
			name: "auto",
			mode: "auto",
			contains: []string{
				"Current session mode: auto.",
				"The current session mode above is authoritative for this turn and supersedes any earlier transcript text, tool output, or UI guidance that described a different mode.",
				"Session mode can be changed between turns; do not treat an earlier auto/plan state as permanent.",
				"Current agent runtime contract: plan -> auto (exit_plan_mode transitions an approved plan turn to auto; it does not make auto mode irreversible).",
				"Execution expectation: continue implementation; ask-user only for true product/decision forks.",
				fmt.Sprintf("If an active plan exists, use plan_manage get-active/save to inspect or revise it without switching modes. Do not call exit_plan_mode from auto; it only applies when leaving plan mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
			},
		},
		{
			name: "invalid_defaults_to_plan",
			mode: "unknown",
			contains: []string{
				"Current session mode: plan.",
				"- tool availability is determined by plan mode until exit_plan_mode switches the session to auto.",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out := modeCapabilityInstructions(tc.mode, false, pebblestore.AgentProfile{Name: "swarm"})
			for _, expected := range tc.contains {
				if !strings.Contains(out, expected) {
					t.Fatalf("expected %q in output:\n%s", expected, out)
				}
			}
			for _, blocked := range tc.notContains {
				if strings.Contains(out, blocked) {
					t.Fatalf("did not expect %q in output:\n%s", blocked, out)
				}
			}
		})
	}
}

func TestComposeModeAwareInstructions_AppendsModePolicy(t *testing.T) {
	base := "custom system instruction"
	out := composeModeAwareInstructions(base, "auto", false, pebblestore.AgentProfile{Name: "swarm"})
	if !strings.Contains(out, base) {
		t.Fatalf("expected base instructions to be preserved: %q", out)
	}
	if !strings.Contains(out, "Current session mode: auto.") {
		t.Fatalf("expected mode block to be appended: %q", out)
	}
	if !strings.Contains(out, "The current session mode above is authoritative for this turn") {
		t.Fatalf("expected authoritative current-mode guidance: %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("If an active plan exists, use plan_manage get-active/save to inspect or revise it without switching modes. Do not call exit_plan_mode from auto; it only applies when leaving plan mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet)) {
		t.Fatalf("expected auto-mode active-plan guidance: %q", out)
	}
}

func TestComposeModeAwareInstructions_PlanReentryOverridesStaleAutoTranscript(t *testing.T) {
	base := strings.Join([]string{
		"custom system instruction",
		"Earlier assistant: This session has already exited plan mode — the earlier exit_plan_mode call was approved and switched us to auto mode.",
		"Earlier assistant: I can't call exit_plan_mode again from auto mode.",
	}, "\n")
	out := composeModeAwareInstructions(base, "plan", false, pebblestore.AgentProfile{Name: "swarm"})
	for _, expected := range []string{
		"Current session mode: plan.",
		"The current session mode above is authoritative for this turn",
		"Session mode can be changed between turns; do not treat an earlier auto/plan state as permanent.",
		"Because the current session mode is plan, you may call exit_plan_mode when the plan is actionable even if earlier transcript text says the session already exited plan mode or that exit_plan_mode cannot be called from auto.",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, out)
		}
	}
}

func TestDefaultInstructions_IncludeParallelAndDelegationGuidance(t *testing.T) {
	out := defaultInstructions("/tmp/workspace")
	for _, expected := range []string{
		"Execution strategy:",
		"Batch independent inspection calls in the same step",
		"For read, it is safe to request up to 2000 lines per call",
		"Before delegating, do a quick first pass with agentic_search/read/list",
		"Match effort to request scope: for narrow, explicit asks",
		"Delegate to subagents only when scope is broad, cross-cutting, or still unclear",
		"For unfamiliar codebases, use task with subagent_type=explorer",
		"Do not send vague delegation prompts; include what you already checked",
		"run multiple explorer delegations in parallel when possible",
		"Avoid over-delegation: if one quick read/agentic_search confirms the needed change",
		"final Relevant filepaths list",
		"Keep operations inside workspace root: /tmp/workspace",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, out)
		}
	}
}

func TestBuildTaskDelegationPrompt_IncludesExplorerAndPathGuidance(t *testing.T) {
	out := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		Description:    "scope repo",
		Prompt:         "inspect the runtime",
		ReportMaxChars: 1200,
		ParentSession: pebblestore.SessionSnapshot{
			ID:                 "session-1",
			Title:              "Investigate @explorer",
			Mode:               "auto",
			WorkspacePath:      "/tmp/workspace",
			WorkspaceName:      "workspace",
			Metadata:           map[string]any{"target_kind": "subagent", "foo": "bar"},
			WorktreeEnabled:    true,
			WorktreeRootPath:   "/tmp/workspace",
			WorktreeBaseBranch: "main",
			WorktreeBranch:     "agent/session-1",
		},
		ParentMessages: []pebblestore.MessageSnapshot{
			{Role: "user", Content: "check the subagent flow"},
			{Role: "assistant", Content: "I found the delegation prompt builder."},
			{Role: "tool", Content: `{"path_id":"tool.history.v1","tool":"search","call_id":"search_1","arguments":"{\"query\":\"buildTaskDelegationPrompt\"}","output":"found buildTaskDelegationPrompt in service_tools.go","completed_output":"search \"buildTaskDelegationPrompt\" (1 match)"}`},
		},
		PermissionSessionID:  "session-1",
		TargetedSubagentName: "explorer",
	})
	for _, expected := range []string{
		"Completion contract:",
		"use the provided context as your starting point",
		"First gauge scope quickly: if the request is narrow and explicit",
		"Use agentic_search first (queries=[...] when scopes are independent)",
		"Summarize the relevant architecture/flow, then identify areas of interest and likely attack points",
		"Back key findings with concrete evidence (path and line anchors where possible)",
		"Relevant filepaths:",
		"Open questions / missing filepaths:",
		"Parent session context:",
		"Recent visible parent transcript:",
		"- user: check the subagent flow",
		"- assistant: I found the delegation prompt builder.",
		"- tool: [search] search \"buildTaskDelegationPrompt\" (1 match)",
		"- session_id: session-1",
		"- permission_session_id: session-1",
		"- metadata_json:",
		"- git_metadata_json:",
		"- launch source: targeted_subagent",
		"- requested subagent: @explorer",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, out)
		}
	}
}

func TestModeCapabilityInstructions_WithBypassPermissions(t *testing.T) {
	out := modeCapabilityInstructions("auto", true, pebblestore.AgentProfile{Name: "swarm"})
	for _, expected := range []string{
		"Current session mode: auto.",
		"Permission bypass is active: normal tool approval prompts are skipped.",
		"task still requires explicit approval before launching subagents, even when permission bypass is active.",
		"exit_plan_mode still requires explicit approval even when permission bypass is active.",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, out)
		}
	}
}
