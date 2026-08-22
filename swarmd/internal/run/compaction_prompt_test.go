package run

import (
	"fmt"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestCompactModelRuntimeUsesCanonicalProviderMappings(t *testing.T) {
	preference := pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Thinking:    "xhigh",
		ServiceTier: "fast",
		ContextMode: "full",
	}
	catalog := pebblestore.ModelCatalogRecord{
		Provider: "codex",
		Model:    "gpt-5.4",
		ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting: "xhigh", ProviderParameter: "reasoning.effort", ProviderValue: "xhigh",
		}},
		ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{
			Tier: "fast", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority",
		}},
	}
	compactModel := compactModelRuntime{ProviderID: "codex", Preference: preference, Catalog: catalog}
	req := compactModel.apply(provideriface.Request{ToolChoice: "none"})
	converted := codex.ToRequest(req)
	if req.ServiceTier != "fast" {
		t.Fatalf("Swarm Compact tier = %q, want fast", req.ServiceTier)
	}
	if converted.ServiceTier != "priority" {
		t.Fatalf("Codex Compact tier = %q, want canonical catalog-mapped priority", converted.ServiceTier)
	}
	if converted.ReasoningProviderValue != "xhigh" {
		t.Fatalf("Codex Compact reasoning = %q, want canonical catalog-mapped xhigh", converted.ReasoningProviderValue)
	}
	if _, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord); !ok {
		t.Fatalf("Compact request catalog = %#v", req.ModelCatalog)
	}
	if req.ToolChoice != "none" || len(req.Tools) != 0 {
		t.Fatalf("Compact request gained tools: choice=%q tools=%#v", req.ToolChoice, req.Tools)
	}
}

func TestCompactModelRuntimeNormalizesEveryRunnableProvider(t *testing.T) {
	for _, tc := range []struct {
		provider     string
		thinking     string
		tier         string
		wantTier     string
		wantThinking string
	}{
		{provider: "anthropic", thinking: "high", tier: "fast", wantTier: "fast", wantThinking: "high"},
		{provider: "codex", thinking: "high", tier: "fast", wantTier: "fast", wantThinking: "high"},
		{provider: "copilot", thinking: "xhigh", tier: "fast", wantTier: "", wantThinking: "high"},
		{provider: "fireworks", thinking: "xhigh", tier: "priority", wantTier: "priority", wantThinking: "high"},
		{provider: "google", thinking: "xhigh", tier: "fast", wantTier: "", wantThinking: "xhigh"},
		{provider: "openai", thinking: "xhigh", tier: "priority", wantTier: "priority", wantThinking: "xhigh"},
		{provider: "openrouter", thinking: "xhigh", tier: "priority", wantTier: "priority", wantThinking: "high"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			runtime := compactModelRuntime{ProviderID: tc.provider, Preference: pebblestore.ModelPreference{Provider: tc.provider, Model: "model", Thinking: normalizeThinkingWithProvider(tc.provider, tc.thinking), ServiceTier: resolvedServiceTierForProvider(tc.provider, tc.tier)}, Catalog: pebblestore.ModelCatalogRecord{Provider: tc.provider, Model: "model"}}
			req := runtime.apply(provideriface.Request{})
			if req.ServiceTier != tc.wantTier {
				t.Fatalf("%s Compact tier = %q, want %q", tc.provider, req.ServiceTier, tc.wantTier)
			}
			if req.Thinking != tc.wantThinking {
				t.Fatalf("%s Compact thinking = %q, want %q", tc.provider, req.Thinking, tc.wantThinking)
			}
			if catalog, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord); !ok || catalog.Provider != tc.provider {
				t.Fatalf("%s Compact catalog = %#v", tc.provider, req.ModelCatalog)
			}
		})
	}
}

func TestCompactInstructionsAreCaseSpecificAndToolFree(t *testing.T) {
	profile := agentruntime.CompactAgentProfileForParent(pebblestore.AgentProfile{Provider: "codex", Model: "utility"})
	if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 0 {
		t.Fatalf("Compact tools = %+v, want none", profile.ToolContract)
	}
	compactInstructions := buildMemoryCompactionInstructions(profile.Prompt, 9000, contextCompactionOriginManual)
	titleInstructions := strings.Join([]string{profile.Prompt, "Title-only case: generate a deterministic session title. Do not summarize or compact the conversation."}, "\n")
	if strings.Contains(titleInstructions, "Required sections:") || strings.Contains(titleInstructions, "Compaction mode:") {
		t.Fatalf("title instructions leaked compact-summary contract:\n%s", titleInstructions)
	}
	if !strings.Contains(compactInstructions, "Required sections:") {
		t.Fatalf("compact instructions missing summary contract:\n%s", compactInstructions)
	}
}

func TestBuildMemoryCompactionInstructionsByOrigin(t *testing.T) {
	manual := buildMemoryCompactionInstructions("", 9000, contextCompactionOriginManual)
	if !strings.Contains(manual, "manual user-requested compact") {
		t.Fatalf("manual instructions missing manual mode: %s", manual)
	}
	if !strings.Contains(manual, "If the user gave no specific compact note") {
		t.Fatalf("manual instructions missing no-note guidance: %s", manual)
	}

	threshold := buildMemoryCompactionInstructions("", 9000, contextCompactionOriginThreshold)
	if !strings.Contains(threshold, "proactive automatic compact") {
		t.Fatalf("threshold instructions missing proactive mode: %s", threshold)
	}
	if strings.Contains(threshold, "previous provider step overflowed") {
		t.Fatalf("threshold instructions should not describe overflow: %s", threshold)
	}

	overflow := buildMemoryCompactionInstructions("", 9000, contextCompactionOriginOverflow)
	if !strings.Contains(overflow, "provider context overflow") {
		t.Fatalf("overflow instructions missing overflow mode: %s", overflow)
	}
	if !strings.Contains(overflow, "may have been mid-task") {
		t.Fatalf("overflow instructions missing mid-task guidance: %s", overflow)
	}
}

func TestBuildMemoryCompactionPromptManualNoNoteLaterCompact(t *testing.T) {
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:    "manual context compact request",
		Chunk:        "[seq:1 role:user]\noriginal task",
		Origin:       contextCompactionOriginManual,
		CompactIndex: 3,
	})
	for _, want := range []string{
		"user manually requested a durable context summary",
		"This will become Compact #3.",
		"Manual compact note: none provided",
		"later compact",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("manual prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Current run user prompt:\n\nmanual context compact request") {
		t.Fatalf("default manual compact prompt leaked as user task:\n%s", prompt)
	}
}

func TestBuildMemoryCompactionPromptOverflowUsesVisibleTranscriptOnly(t *testing.T) {
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:    "fix compact overflow",
		Chunk:        "user:\nfix compact overflow\n\nassistant:\nstarted patch",
		Origin:       contextCompactionOriginOverflow,
		CompactIndex: 2,
	})
	for _, want := range []string{
		"exceeded the model context window",
		"may have stopped mid-thought or mid-action",
		"user:\nfix compact overflow",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("overflow prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"Current run user prompt:",
		"Active session plan at compaction time:",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("overflow prompt included non-conversation context %q:\n%s", unwanted, prompt)
		}
	}
}

func TestBuildMemoryCompactionPromptCarriesDurableActiveCheckpoint(t *testing.T) {
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:      "context overflow compact request",
		Chunk:          "tool:\n- name: edit\n- outcome: updated service.go",
		Origin:         contextCompactionOriginOverflow,
		CompactIndex:   2,
		ActivePlanText: "Plan ID: plan-1\nActive checkpoint ID: cp-2\n- Status: in_progress",
	})
	for _, want := range []string{
		"Durable active plan/checkpoint state (authoritative",
		"Active checkpoint ID: cp-2",
		"updated service.go",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("overflow prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildOverflowCompactionCheckpointDirectsSameCheckpointContinuation(t *testing.T) {
	checkpoint := buildCompactionCheckpointMessage("durable recap", contextCompactionOriginOverflow, 2, "Plan One (plan-1)")
	for _, want := range []string{
		"Resume the same interrupted task and active plan checkpoint",
		"Do not restart completed discovery or edits",
		"Attached plan: Plan One (plan-1)",
	} {
		if !strings.Contains(checkpoint, want) {
			t.Fatalf("overflow checkpoint missing %q:\n%s", want, checkpoint)
		}
	}
}

func TestCompactedActivePlanTextIncludesCurrentExecutionScope(t *testing.T) {
	text := compactedActivePlanText(&pebblestore.SessionPlanSnapshot{
		ID:            "plan-1",
		Title:         "Plan One",
		Status:        "approved",
		ApprovalState: "approved",
		Document: &pebblestore.SessionPlanDocument{
			ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: "automatic", Shape: "checkpointed"},
			ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: "running", ActiveAttemptID: "cp-2:attempt-1", CurrentRunID: "run-2"},
			ActiveCheckpointID: "cp-2",
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{
				ID:              "cp-2",
				Title:           "Implement continuation",
				Status:          "in_progress",
				AttemptID:       "cp-2:attempt-1",
				RunID:           "run-2",
				ActiveSubtaskID: "subtask-2",
				Subtasks:        []pebblestore.SessionPlanSubtask{{ID: "subtask-1", Title: "Trace failure", Status: "completed"}, {ID: "subtask-2", Title: "Patch continuation", Status: "in_progress"}},
			}},
		},
	})
	for _, want := range []string{
		"Active checkpoint ID: cp-2",
		"Execution state: running",
		"Active attempt ID: cp-2:attempt-1",
		"- Status: in_progress",
		"Subtask subtask-1 [completed]: Trace failure",
		"Subtask subtask-2 [in_progress]: Patch continuation",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compacted plan text missing %q:\n%s", want, text)
		}
	}
}

func TestNextMemoryCompactionIndex(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "user", Content: "hello"},
		{Role: "system", Content: "[context-compact] index=2 origin=manual\n\nCompacted recap:\none"},
		{Role: "system", Content: "[context-compact] index=4 origin=overflow\n\nCompacted recap:\ntwo"},
	}
	if got := nextMemoryCompactionIndex(messages); got != 5 {
		t.Fatalf("nextMemoryCompactionIndex = %d, want 5", got)
	}
}

func TestMemoryCompactionToolCallIDUsesCompactIndex(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "system", Content: "[context-compact] index=2 origin=manual\n\nCompacted recap:\none"},
	}
	stream := newMemoryCompactionToolStream(nil, 1, contextCompactionOriginManual, 1)
	stream.SetCompactIndex(nextMemoryCompactionIndex(messages))

	if got, want := stream.CallID, "context-compact:manual:3"; got != want {
		t.Fatalf("manual compact CallID = %q, want %q", got, want)
	}
	if got, want := stream.Attempt, 1; got != want {
		t.Fatalf("manual compact attempt = %d, want %d", got, want)
	}
}

func TestBuildManualCompactionAssistantTextIncludesUserVisibleRecap(t *testing.T) {
	text := buildManualCompactionAssistantText("important compact summary", 3, "Plan title (plan_1)")
	for _, want := range []string{
		"Manual context compact complete (Compact #3).",
		"Compacted recap:",
		"important compact summary",
		"Attached plan: Plan title (plan_1)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manual compact assistant text missing %q:\n%s", want, text)
		}
	}
}

func TestMemoryCompactionTranscriptIncludesVisibleConversationAndBoundedToolOutcomes(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{GlobalSeq: 1, Role: "system", Content: "runtime contract and tool policy"},
		{GlobalSeq: 2, Role: "user", Content: "fix the memory context"},
		{GlobalSeq: 3, Role: "reasoning", Content: "private reasoning summary"},
		{GlobalSeq: 4, Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"edit","call_id":"call-edit","arguments":"{\"path\":\"swarmd/internal/run/service.go\"}","output":"updated swarmd/internal/run/service.go with the continuation fix"}`},
		{GlobalSeq: 5, Role: "assistant", Content: "I found the oversized context path."},
		{GlobalSeq: 6, Role: "system", Content: "[context-compact] index=2 origin=threshold\n\nCompacted recap:\nprior visible recap"},
		{GlobalSeq: 7, Role: "user", Content: "/auth secret", Metadata: map[string]any{"source": "command"}},
	}

	transcript := buildMemoryCompactionTranscript(messages)
	for _, want := range []string{
		"user:\nfix the memory context",
		"assistant:\nI found the oversized context path.",
		"assistant:\n[context-compact] index=2 origin=threshold",
		"- name: edit",
		"swarmd/internal/run/service.go",
		"continuation fix",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("memory transcript missing %q:\n%s", want, transcript)
		}
	}
	for _, unwanted := range []string{
		"runtime contract and tool policy",
		"private reasoning summary",
		"/auth secret",
		"seq:",
		"role:",
	} {
		if strings.Contains(transcript, unwanted) {
			t.Fatalf("memory transcript included internal context %q:\n%s", unwanted, transcript)
		}
	}
}

func TestTaskCompactionTranscriptPreservesTypedDiscoveryAndTruncatedMutationEvidence(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"search","arguments":"{\"query\":\"TaskContextCompaction\",\"path\":\"swarmd/internal/run\"}","output":"{\"summary\":\"found TaskContextCompaction in service.go\",\"truncated\":true}"}`},
		{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"edit","arguments":"{\"path\":\"swarmd/internal/run/service.go\"}","completed_output":"{\"path\":\"swarmd/internal/run/service.go\",\"replacements\":1,\"old_string_truncated\":true,\"new_string_truncated\":false,\"summary\":\"updated Compact assembly\"}"}`},
	}
	transcript := buildTaskCompactionTranscript(messages)
	for _, want := range []string{
		"- kind: discovery",
		"- name: search",
		"TaskContextCompaction",
		"found TaskContextCompaction in service.go",
		"- kind: mutation",
		"- name: edit",
		"updated Compact assembly",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("Task transcript missing typed evidence %q:\n%s", want, transcript)
		}
	}
}

func TestDelegatedSubagentRunStartMetaSeparatesBaseBranchAndCommit(t *testing.T) {
	launch := taskLaunchPrepared{
		SubagentProfile: pebblestore.AgentProfile{Name: "system-coder"},
		ContinuationBoundary: func(RunContinuationBoundaryInput) (RunContinuationBoundaryDecision, error) {
			return RunContinuationBoundaryDecision{}, nil
		},
		ChildWorkspacePath:  "/workspace/child",
		ChildWorktreeBase:   "dev-parent",
		ChildWorktreeBranch: "agent/child-context",
		TaskBase:            &worktreeruntime.TaskBase{RepoRoot: "/repo", ParentBranch: "dev-parent", BaseCommit: "base-commit-123"},
	}
	meta := delegatedSubagentRunStartMeta(launch, "permission-session", identity.Principal{AccountScopeID: "account-1"}, nil)
	if meta.TaskCompaction == nil {
		t.Fatal("delegated run meta omitted Task compaction context")
	}
	if got := meta.TaskCompaction.BaseBranch; got != "dev-parent" {
		t.Fatalf("Task base branch = %q, want dev-parent", got)
	}
	if got := meta.TaskCompaction.ImmutableBaseCommit; got != "base-commit-123" {
		t.Fatalf("Task immutable base commit = %q, want base-commit-123", got)
	}
	if meta.TaskCompaction.ImmutableBaseCommit == meta.TaskCompaction.BaseBranch {
		t.Fatalf("Task base branch was mislabeled as immutable commit: %+v", meta.TaskCompaction)
	}
}

func TestBoundTaskCompactionTranscriptKeepsSemanticEvidenceUnits(t *testing.T) {
	entries := []taskCompactionTranscriptEntry{
		{Text: "assistant:\n" + strings.Repeat("low-priority narration ", 100), Priority: 2, Order: 0},
		{Text: "typed tool evidence:\n- kind: discovery\n- outcome: middle-symbol -> service.go", Priority: 3, Order: 1},
		{Text: "typed tool evidence:\n- kind: mutation\n- outcome: changed service.go", Priority: 4, Order: 2},
		{Text: "assistant prior compact checkpoint:\ncritical baseline", Priority: 6, Order: 3},
	}
	bounded := boundTaskCompactionTranscript(entries, 1150)
	for _, want := range []string{"critical baseline", "middle-symbol -> service.go", "changed service.go", "semantic budget notice"} {
		if !strings.Contains(bounded, want) {
			t.Fatalf("semantic Task budget dropped %q:\n%s", want, bounded)
		}
	}
	if strings.Contains(bounded, "older middle transcript omitted") || strings.Contains(bounded, strings.Repeat("low-priority narration ", 50)) {
		t.Fatalf("semantic Task budget reverted to rune slicing or retained low-priority dump:\n%s", bounded)
	}
}

func TestCompactInstructionsDefineConservativePersistentLoopWarning(t *testing.T) {
	for _, origin := range []string{
		contextCompactionOriginManual,
		contextCompactionOriginThreshold,
		contextCompactionOriginOverflow,
		contextCompactionOriginPlanGuard,
		contextCompactionOriginTask,
	} {
		t.Run(origin, func(t *testing.T) {
			instructions := buildMemoryCompactionInstructions("", 9000, origin)
			for _, want := range []string{
				"two or more attempts or compaction epochs",
				"substantially equivalent operations",
				"same unchanged blocker, contradiction, or result",
				"without new authoritative evidence, a file/workspace change, a commit, a newly validated result, or a materially distinct recovery path",
				"A bounded retry is productive, not a loop",
				"A failed first attempt, uncertainty, or scope growth alone is not a loop",
				"NO-PROGRESS LOOP WARNING (carry forward until resolved):",
				"workspace/Git state when known",
				"one bounded next action that is not another substantially equivalent retry",
				"copy the warning forward under the exact same heading",
				"Remove it only when visible authoritative evidence proves the blocker resolved or material progress invalidates the repetition claim",
			} {
				if !strings.Contains(instructions, want) {
					t.Fatalf("%s Compact instructions missing %q:\n%s", origin, want, instructions)
				}
			}
		})
	}
}

func TestLaterCompactCarriesUnresolvedLoopWarningFromPriorCheckpoint(t *testing.T) {
	warning := `NO-PROGRESS LOOP WARNING (carry forward until resolved):
- repeated operation: npm view @xterm/addon-webgl@0.17.0 dist.integrity
- repetition evidence: Compact #2 and two tool attempts returned the same unavailable-version result
- unchanged blocker: the exact package version is unavailable
- completed work: manifest edits are complete
- workspace/Git state: package.json modified; no commit recorded
- exact resolution needed: choose an available package version
- bounded next action: report the version contradiction and request the version decision`
	messages := []pebblestore.MessageSnapshot{
		{GlobalSeq: 1, Role: "user", Content: "install the exact package version"},
		{GlobalSeq: 2, Role: "system", Content: "[context-compact] index=2 origin=threshold\n\nCompacted recap:\n" + warning},
		{GlobalSeq: 3, Role: "assistant", Content: "I preserved the partial manifest work."},
	}

	trimmed := trimMessagesToLatestCompactionCheckpoint(messages)
	transcript := buildMemoryCompactionTranscript(trimmed)
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:    "install the exact package version",
		Chunk:        transcript,
		Origin:       contextCompactionOriginThreshold,
		CompactIndex: 3,
	})
	for _, want := range []string{
		"This is a later compact",
		"NO-PROGRESS LOOP WARNING (carry forward until resolved):",
		"npm view @xterm/addon-webgl@0.17.0 dist.integrity",
		"choose an available package version",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("later Compact prompt lost stable warning field %q:\n%s", want, prompt)
		}
	}
}

func TestCompactIncidentFixtureExposesUnavailableExactVersionAndTimeout(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "user", Content: "Install exactly @xterm/addon-webgl@0.17.0 and keep completed manifest edits."},
		{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"bash","arguments":"{\"command\":\"npm view @xterm/addon-webgl@0.17.0 dist.integrity\"}","error":"npm ERR! code E404: No match found for version 0.17.0"}`},
		{Role: "assistant", Content: "The exact registry version appears unavailable; I will try one bounded registry query with a distinct endpoint."},
		{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"bash","arguments":"{\"command\":\"npm view @xterm/addon-webgl versions --json\"}","error":"command timed out after 120000ms"}`},
		{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"edit","arguments":"{\"path\":\"package.json\"}","completed_output":"{\"summary\":\"preserved completed manifest edits\",\"replacements\":1}"}`},
	}
	transcript := buildMemoryCompactionTranscript(messages)
	instructions := buildMemoryCompactionInstructions("", 9000, contextCompactionOriginOverflow)
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:    "Install exactly @xterm/addon-webgl@0.17.0",
		Chunk:        transcript,
		Origin:       contextCompactionOriginOverflow,
		CompactIndex: 2,
	})
	for _, want := range []string{
		"@xterm/addon-webgl@0.17.0",
		"No match found for version 0.17.0",
		"timed out after 120000ms",
		"preserved completed manifest edits",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("incident Compact prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, want := range []string{
		"same unchanged blocker, contradiction, or result",
		"materially distinct recovery path",
		"A bounded retry is productive, not a loop",
		"NO-PROGRESS LOOP WARNING (carry forward until resolved):",
		"Do not recommend another equivalent lookup, install, command, or tool call after warning",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("incident Compact instructions missing %q:\n%s", want, instructions)
		}
	}
}

func TestPlanGuardCheckpointPreservesLoopWarningVerbatim(t *testing.T) {
	warning := "NO-PROGRESS LOOP WARNING (carry forward until resolved):\n- unchanged blocker: exact package version unavailable\n- bounded next action: request an available version"
	checkpoint := buildCompactionCheckpointMessage(warning, contextCompactionOriginPlanGuard, 4, "Plan One (plan-1)")
	for _, want := range []string{
		warning,
		"origin=plan_guard",
		"Attached plan: Plan One (plan-1)",
	} {
		if !strings.Contains(checkpoint, want) {
			t.Fatalf("plan-guard checkpoint lost %q:\n%s", want, checkpoint)
		}
	}
}

func TestManualCompactionAcknowledgementExcludedFromModelContext(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "system", Content: "[context-compact] index=3 origin=manual\n\nCompacted recap:\nsummary"},
		{Role: "assistant", Content: "Manual context compact complete (Compact #3).", Metadata: map[string]any{"source": "manual_context_compaction_ack"}},
		{Role: "user", Content: "continue"},
	}

	transcript := buildMemoryCompactionTranscript(messages)
	if strings.Contains(transcript, "Manual context compact complete") {
		t.Fatalf("manual compact acknowledgement leaked into compaction transcript:\n%s", transcript)
	}
	input := buildInput(messages)
	for _, item := range input {
		if strings.Contains(fmt.Sprint(item), "Manual context compact complete") {
			t.Fatalf("manual compact acknowledgement leaked into model input: %#v", input)
		}
	}
}
