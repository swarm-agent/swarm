package run

import (
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestContextCompactionCheckpointMetadataDefinesReplacementGeneration(t *testing.T) {
	metadata := ContextCompactionCheckpointMetadata(nil, "summary", contextCompactionOriginOverflow, 4)
	generation, ok := metadata[contextCompactionGenerationMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("generation metadata missing or wrong type: %+v", metadata)
	}
	if generation["version"] != contextCompactionGenerationVersion || generation["origin"] != contextCompactionOriginOverflow || generation["compact_index"] != 4 {
		t.Fatalf("generation metadata = %+v", generation)
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

func TestMemoryCompactionTranscriptUsesBoundedCanonicalToolProjection(t *testing.T) {
	largeOutput := strings.Repeat("raw-wrapper-payload-", 6000)
	valid := formatToolHistory(tool.Call{CallID: "call-1", Name: "bash", Arguments: `{"command":"large"}`}, tool.Result{CallID: "call-1", Name: "bash", Output: largeOutput})
	messages := []pebblestore.MessageSnapshot{
		{GlobalSeq: 1, Role: "tool", Content: valid},
		{GlobalSeq: 2, Role: "tool", Content: `{"path_id":"run.tool-history.v2","tool":"bash"}`},
		{GlobalSeq: 3, Role: "tool", Content: "malformed tool wrapper"},
		{GlobalSeq: 4, Role: "user", Content: "latest real user request"},
	}

	transcript := buildMemoryCompactionTranscript(messages, "")
	for _, want := range []string{"function_call", "function_call_output", "call-1", "truncated_for_model", "latest real user request"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("canonical compaction transcript missing %q:\n%s", want, transcript)
		}
	}
	for _, forbidden := range []string{largeOutput, "malformed tool wrapper"} {
		if strings.Contains(transcript, forbidden) {
			t.Fatalf("raw or malformed tool storage leaked into compaction transcript")
		}
	}
	if got := len([]rune(transcript)); got > memoryCompactionTranscriptMaxRunes {
		t.Fatalf("compaction transcript runes = %d, max %d", got, memoryCompactionTranscriptMaxRunes)
	}
}

func TestBoundedMemoryCompactionEntriesRetainsLatestRealUserRequest(t *testing.T) {
	entries := []string{strings.Repeat("old context ", 100), "[seq:2 model_context]\nlatest real user request"}
	got := boundedMemoryCompactionEntries(entries, 80)
	if !strings.Contains(got, "latest real user request") || strings.Contains(got, "old context") {
		t.Fatalf("bounded transcript did not preserve latest request: %q", got)
	}
}

func TestDurableCompactionFactsRejectsFalseDeploymentAfterNewCommitAndCancelledDeploy(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{GlobalSeq: 10, Role: "tool", Content: formatToolHistory(tool.Call{CallID: "deploy-old", Name: "deploy", Arguments: `{}`}, tool.Result{CallID: "deploy-old", Name: "deploy", Output: "deployed commit 1fb164f5"})},
		{GlobalSeq: 20, Role: "tool", Content: formatToolHistory(tool.Call{CallID: "commit-new", Name: "git_commit", Arguments: `{}`}, tool.Result{CallID: "commit-new", Name: "git_commit", Output: "created commit 4eba49a"})},
		{GlobalSeq: 21, Role: "user", Content: "deploy the new commit"},
	}
	intents := []pebblestore.V3SessionRunIntent{{RunID: "deploy-4eba49a", Status: sessionruntime.RunIntentCancelled, BlockedReason: "cancelled during deploy"}}

	facts := buildDurableCompactionFacts(messages, intents, &pebblestore.SessionPlanSnapshot{ID: "plan-1", Status: "running"})
	for _, want := range []string{"4eba49a", "1fb164f5", "status=cancelled", "NOT confirmed deployed/live", "Latest durable user request: deploy the new commit", "Active plan state:"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("durable facts missing %q:\n%s", want, facts)
		}
	}
	if strings.Contains(facts, "4eba49a is deployed") {
		t.Fatalf("durable facts falsely reported new commit deployed:\n%s", facts)
	}
}

func TestDurableFactsRejectContradictoryGeneratedRecap(t *testing.T) {
	facts := "Unconfirmed/cancelled work: run deploy-new ended cancelled; no success claim may be inferred."
	if !durableFactsRejectGeneratedRecap(facts) {
		t.Fatal("cancelled durable work did not reject generated recap")
	}
	if durableFactsRejectGeneratedRecap("Run intent: run_id=done status=completed") {
		t.Fatal("terminal successful durable work unexpectedly rejected generated recap")
	}
}

func TestCompactionCheckpointSeparatesGeneratedRecapAndDurableFacts(t *testing.T) {
	facts := "Run intent: run_id=deploy-new status=cancelled"
	reconciled := strings.Join([]string{"Generated recap (non-authoritative; durable facts below prevail):", "Everything deployed successfully.", contextCompactionFactsHeading, facts}, "\n\n")
	checkpoint := buildCompactionCheckpointMessage(reconciled, contextCompactionOriginOverflow, 2, "")
	if !strings.Contains(checkpoint, "Generated recap (non-authoritative") || !strings.Contains(checkpoint, contextCompactionFactsHeading) {
		t.Fatalf("checkpoint did not separate generated recap from durable facts:\n%s", checkpoint)
	}
	if got := durableFactsTailFromSummary(reconciled); got != facts {
		t.Fatalf("durable facts metadata tail = %q, want %q", got, facts)
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
