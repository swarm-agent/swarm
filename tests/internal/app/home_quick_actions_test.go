package app

import (
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeQuickActionsUsesModelNameOnly(t *testing.T) {
	got := homeQuickActions(model.HomeModel{
		AuthConfigured: true,
		ActiveAgent:    "swarm",
		ModelProvider:  "codex",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
	})

	want := []string{
		"Agent: swarm",
		"Model: gpt-5-codex",
		"Thinking: high",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("homeQuickActions() = %v, want %v", got, want)
	}
}

func TestHomeQuickActionsUsesFastSuffixWithinModelLabelForGPT54(t *testing.T) {
	got := homeQuickActions(model.HomeModel{
		AuthConfigured: true,
		ActiveAgent:    "swarm",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		ServiceTier:    "fast",
		ContextMode:    "1m",
	})

	want := []string{
		"Agent: swarm",
		"Model: gpt-5.4(fast)",
		"Thinking: high",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("homeQuickActions() = %v, want %v", got, want)
	}
}
