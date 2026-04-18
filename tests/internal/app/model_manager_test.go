package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestNextThinkingLevelCyclesInPresetOrder(t *testing.T) {
	levels := []string{"off", "low", "medium", "high", "xhigh", "off"}
	for i := 0; i < len(levels)-1; i++ {
		got := nextThinkingLevel("", levels[i])
		if got != levels[i+1] {
			t.Fatalf("nextThinkingLevel(%q) = %q, want %q", levels[i], got, levels[i+1])
		}
	}
}

func TestNextThinkingLevelFallsBackForUnknownValue(t *testing.T) {
	if got := nextThinkingLevel("", "max"); got != "off" {
		t.Fatalf("nextThinkingLevel(%q) = %q, want off", "max", got)
	}
}

func TestApplyHomeModelResolvedCopiesCodexRuntimeState(t *testing.T) {
	next := applyHomeModelResolved(modelEmptyHomeForTest(), client.ModelResolved{
		Preference: client.ModelPreference{
			Provider:    "codex",
			Model:       "gpt-5.4",
			Thinking:    "high",
			ServiceTier: "fast",
			ContextMode: "1m",
		},
		ContextWindow: 1050000,
	})
	if next.ModelProvider != "codex" || next.ModelName != "gpt-5.4" || next.ThinkingLevel != "high" {
		t.Fatalf("unexpected resolved model identity: %#v", next)
	}
	if next.ServiceTier != "fast" || next.ContextMode != "1m" {
		t.Fatalf("unexpected codex runtime flags: %#v", next)
	}
	if next.ContextWindow != 1050000 {
		t.Fatalf("context window = %d, want 1050000", next.ContextWindow)
	}
}

func modelEmptyHomeForTest() model.HomeModel {
	return model.HomeModel{ActiveAgent: "swarm"}
}
