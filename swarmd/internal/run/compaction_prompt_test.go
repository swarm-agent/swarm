package run

import (
	"fmt"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
