package ui

import "testing"

func TestMergeAssistantStreamPreservesLeadingDeltaWhitespace(t *testing.T) {
	current := "I'm Swarm, your coding"
	current = mergeAssistantStream(current, " assistant. I'm ready to help you")
	current = mergeAssistantStream(current, " with anything in your workspace")

	if current != "I'm Swarm, your coding assistant. I'm ready to help you with anything in your workspace" {
		t.Fatalf("mergeAssistantStream(leading whitespace deltas) = %q", current)
	}
}

func TestMergeThinkingStreamAccumulatesIncrementalSyntheticReasoning(t *testing.T) {
	current := mergeThinkingStream("", "The")
	current = mergeThinkingStream(current, " user")
	current = mergeThinkingStream(current, " is")

	if got := normalizeThinkingSummary(current); got != "The user is" {
		t.Fatalf("mergeThinkingStream(incremental reasoning) = %q normalized=%q", current, got)
	}
}
