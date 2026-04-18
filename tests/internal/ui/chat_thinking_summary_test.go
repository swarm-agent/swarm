package ui

import "testing"

func TestMergeThinkingStreamHandlesCumulativeChunks(t *testing.T) {
	current := mergeThinkingStream("", "**Inspecting**")
	current = mergeThinkingStream(current, "**Inspecting current state**")

	if got := normalizeThinkingSummary(current); got != "Inspecting current state" {
		t.Fatalf("normalizeThinkingSummary(merged) = %q", got)
	}
}

func TestMergeThinkingStreamHandlesOverlappingChunks(t *testing.T) {
	current := mergeThinkingStream("", "Inspecting current")
	current = mergeThinkingStream(current, " current state")

	if got := normalizeThinkingSummary(current); got != "Inspecting current state" {
		t.Fatalf("normalizeThinkingSummary(merged) = %q", got)
	}
}

func TestMergeAssistantStreamDedupesCumulativeMarkdownChunks(t *testing.T) {
	current := mergeAssistantStream("", "## Heading\n- one")
	current = mergeAssistantStream(current, "## Heading\n- one\n- two")

	if current != "## Heading\n- one\n- two" {
		t.Fatalf("mergeAssistantStream(cumulative) = %q", current)
	}
}

func TestMergeAssistantStreamPreservesWhitespaceOnlyChunks(t *testing.T) {
	current := mergeAssistantStream("Hey,", " ")
	current = mergeAssistantStream(current, "I'm")
	current = mergeAssistantStream(current, " ")
	current = mergeAssistantStream(current, "Claude Opus!")

	if current != "Hey, I'm Claude Opus!" {
		t.Fatalf("mergeAssistantStream(whitespace chunks) = %q", current)
	}
}

func TestNormalizeThinkingSummaryStripsTagMarkup(t *testing.T) {
	raw := "<thinking>Thinking through request</thinking> [reasoning]step[/reasoning]"
	if got := normalizeThinkingSummary(raw); got != "Thinking through request step" {
		t.Fatalf("normalizeThinkingSummary(tagged) = %q", got)
	}
}

func TestNormalizeThinkingSummaryDedupesRepeatedSentence(t *testing.T) {
	raw := "Inspecting current project state. Inspecting current project state."
	if got := normalizeThinkingSummary(raw); got != "Inspecting current project state." {
		t.Fatalf("normalizeThinkingSummary(repeated sentence) = %q", got)
	}
}

func TestNormalizeThinkingSummaryDedupesRepeatedTokenHalves(t *testing.T) {
	raw := "Inspecting current project state Inspecting current project state"
	if got := normalizeThinkingSummary(raw); got != "Inspecting current project state" {
		t.Fatalf("normalizeThinkingSummary(repeated halves) = %q", got)
	}
}
