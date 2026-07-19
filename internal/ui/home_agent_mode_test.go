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
		PlanContextMode:         "plan-context",
		AutoModelProvider:       "openrouter",
		AutoModelName:           "auto-model",
		AutoThinkingLevel:       "medium",
		AutoServiceTier:         "fast",
		AutoContextMode:         "auto-context",
	}
	page := NewHomePage(m)
	page.SetSessionMode("plan")
	if got := currentDisplayedHomeSessionMode(page); got != "on" {
		t.Fatalf("initial mode = %q, want on", got)
	}
	if page.model.ModelProvider != "codex" || page.model.ModelName != "plan-model" || page.model.ThinkingLevel != "xhigh" || page.model.ServiceTier != "flex" || page.model.ContextMode != "plan-context" {
		t.Fatalf("plan model = (%q, %q, %q, %q, %q)", page.model.ModelProvider, page.model.ModelName, page.model.ThinkingLevel, page.model.ServiceTier, page.model.ContextMode)
	}

	page.SetSessionMode(nextHomeSessionMode(page.SessionMode()))
	if got := currentDisplayedHomeSessionMode(page); got != "off" {
		t.Fatalf("switched mode = %q, want off", got)
	}
	if page.model.ModelProvider != "openrouter" || page.model.ModelName != "auto-model" || page.model.ThinkingLevel != "medium" || page.model.ServiceTier != "fast" || page.model.ContextMode != "auto-context" {
		t.Fatalf("auto model = (%q, %q, %q, %q, %q)", page.model.ModelProvider, page.model.ModelName, page.model.ThinkingLevel, page.model.ServiceTier, page.model.ContextMode)
	}
}

func TestHomeModelStateAndFooterUseEffectiveModeSelection(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ActiveAgentExitPlanMode: true,
		PlanModelProvider:       "codex", PlanModelName: "plan-model", PlanThinkingLevel: "max", PlanServiceTier: "priority", PlanContextMode: "plan-context",
		AutoModelProvider: "openrouter", AutoModelName: "auto-model", AutoThinkingLevel: "high", AutoServiceTier: "flex", AutoContextMode: "auto-context",
	})
	page.sessionMode = "plan"
	provider, modelName, thinking, serviceTier, contextMode := page.ModelState()
	if provider != "codex" || modelName != "plan-model" || thinking != "max" || serviceTier != "priority" || contextMode != "plan-context" {
		t.Fatalf("plan effective state = (%q, %q, %q, %q, %q)", provider, modelName, thinking, serviceTier, contextMode)
	}
	footer := page.homeFooterState()
	if footer.ModelLabel != model.DisplayModelLabel(provider, modelName, serviceTier, contextMode) || footer.Thinking != thinking || footer.ServiceTier != serviceTier {
		t.Fatalf("footer does not match plan effective state: %#v", footer)
	}

	page.sessionMode = "auto"
	provider, modelName, thinking, serviceTier, contextMode = page.ModelState()
	if provider != "openrouter" || modelName != "auto-model" || thinking != "high" || serviceTier != "flex" || contextMode != "auto-context" {
		t.Fatalf("auto effective state = (%q, %q, %q, %q, %q)", provider, modelName, thinking, serviceTier, contextMode)
	}
	footer = page.homeFooterState()
	if footer.ModelLabel != model.DisplayModelLabel(provider, modelName, serviceTier, contextMode) || footer.Thinking != thinking || footer.ServiceTier != serviceTier {
		t.Fatalf("footer does not match auto effective state: %#v", footer)
	}
}

func TestHomePlanToggleRequiresBothEffectiveSelections(t *testing.T) {
	page := NewHomePage(model.HomeModel{ActiveAgentExitPlanMode: true})
	if page.CanCycleSessionMode() {
		t.Fatal("plan toggle must wait for both effective selections")
	}
	page.model.PlanModelProvider, page.model.PlanModelName = "codex", "plan-model"
	page.model.AutoModelProvider, page.model.AutoModelName = "codex", "auto-model"
	if !page.CanCycleSessionMode() {
		t.Fatal("plan toggle should be available when both effective selections are known")
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
