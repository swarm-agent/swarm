package run

import (
	"fmt"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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

func TestBuildMemoryCompactionPromptOverflowIncludesDraftAndPlan(t *testing.T) {
	plan := &pebblestore.SessionPlanSnapshot{ID: "plan_1", Title: "Ship fix", Plan: "# Plan\n1. Patch\n2. Test"}
	prompt := buildMemoryCompactionPrompt(memoryCompactionPromptOptions{
		RunPrompt:      "fix compact overflow",
		Chunk:          "[seq:1 role:user]\nfix compact overflow\n\n[role:assistant_draft]\nstarted patch",
		Origin:         contextCompactionOriginOverflow,
		AssistantDraft: "started patch",
		CompactIndex:   2,
		ActivePlan:     plan,
	})
	for _, want := range []string{
		"exceeded the model context window",
		"may have stopped mid-thought or mid-action",
		"assistant draft was captured",
		"Active session plan at compaction time",
		"Plan ID: plan_1",
		"mark/update them after compaction",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("overflow prompt missing %q:\n%s", want, prompt)
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

func TestManualCompactionAcknowledgementExcludedFromModelContext(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "system", Content: "[context-compact] index=3 origin=manual\n\nCompacted recap:\nsummary"},
		{Role: "assistant", Content: "Manual context compact complete (Compact #3).", Metadata: map[string]any{"source": "manual_context_compaction_ack"}},
		{Role: "user", Content: "continue"},
	}

	transcript := buildMemoryCompactionTranscript(messages, "")
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
