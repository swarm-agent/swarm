package ui

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeAgentModeCapabilityPlanAuto(t *testing.T) {
	m := model.HomeModel{
		ActiveAgentRuntimeKnown: true,
		ActiveAgentExitPlanMode: true,
	}
	if got := homeAgentModeCapability(m, "auto"); got != "plan/auto" {
		t.Fatalf("capability = %q, want plan/auto", got)
	}
}

func TestHomePlanAutoStartsAtConfiguredModeAndSwitchesHydratedModel(t *testing.T) {
	m := model.HomeModel{
		ActiveAgentRuntimeKnown: true,
		ActiveAgentExitPlanMode: true,
		PlanModelProvider:       "codex",
		PlanModelName:           "plan-model",
		PlanThinkingLevel:       "xhigh",
		PlanServiceTier:         "flex",
		AutoModelProvider:       "openrouter",
		AutoModelName:           "auto-model",
		AutoThinkingLevel:       "medium",
		AutoServiceTier:         "fast",
	}
	page := NewHomePage(m)
	page.SetSessionMode("plan")
	if got := currentDisplayedHomeSessionMode(page); got != "plan" {
		t.Fatalf("initial mode = %q, want plan", got)
	}
	if page.model.ModelProvider != "codex" || page.model.ModelName != "plan-model" || page.model.ThinkingLevel != "xhigh" || page.model.ServiceTier != "flex" {
		t.Fatalf("plan model = (%q, %q, %q, %q)", page.model.ModelProvider, page.model.ModelName, page.model.ThinkingLevel, page.model.ServiceTier)
	}

	page.SetSessionMode(nextHomeSessionMode(page.SessionMode()))
	if got := currentDisplayedHomeSessionMode(page); got != "auto" {
		t.Fatalf("switched mode = %q, want auto", got)
	}
	if page.model.ModelProvider != "openrouter" || page.model.ModelName != "auto-model" || page.model.ThinkingLevel != "medium" || page.model.ServiceTier != "fast" {
		t.Fatalf("auto model = (%q, %q, %q, %q)", page.model.ModelProvider, page.model.ModelName, page.model.ThinkingLevel, page.model.ServiceTier)
	}
}

func TestHomeAgentModeCapabilitySingleMode(t *testing.T) {
	m := model.HomeModel{
		ActiveAgentRuntimeKnown:     true,
		ActiveAgentExitPlanMode:     false,
		ActiveAgentExecutionSetting: "readwrite",
	}
	if got := homeAgentModeCapability(m, "plan"); got != "readwrite" {
		t.Fatalf("capability = %q, want readwrite", got)
	}

	page := NewHomePage(m)
	if page.CanCycleSessionMode() {
		t.Fatal("single-mode agent must not cycle plan/auto mode")
	}
}
