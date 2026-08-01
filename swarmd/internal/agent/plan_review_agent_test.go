package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanSidechatMaterializationDoesNotOwnParentModelSelection(t *testing.T) {
	parent := pebblestore.AgentProfile{
		Provider: "provider-a", Model: "model-a", Thinking: "high",
		PlanProvider: "provider-plan", PlanModel: "model-plan", PlanThinking: "xhigh",
		Prompt: "private parent prompt", RuntimeMode: pebblestore.AgentRuntimeModeReadWrite,
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"write": {Enabled: pebblestore.BoolPtr(true)}}},
	}
	profile := PlanSidechatAgentProfileForParent(parent)
	if profile.Provider != "" || profile.Model != "" || profile.Thinking != "" || profile.PlanServiceTier != "" {
		t.Fatalf("Plan system profile inferred model settings instead of waiting for the parent session setup: %+v", profile)
	}
	if profile.Prompt == parent.Prompt || strings.Contains(profile.Prompt, "private parent prompt") {
		t.Fatalf("parent prompt leaked into review profile: %q", profile.Prompt)
	}
	if strings.Contains(strings.ToLower(profile.Prompt), "i cannot") || strings.Contains(strings.ToLower(profile.Prompt), "not allowed") {
		t.Fatalf("review prompt invites internal capability narration: %q", profile.Prompt)
	}
	if config := profile.ToolContract.Tools["write"]; config.Enabled == nil || *config.Enabled {
		t.Fatal("parent write capability leaked into review profile")
	}
}

func TestPlanSidechatPromptAttachesAuthoritativePlanContext(t *testing.T) {
	context := `{"plan_id":"plan-1","proposal_revision":4,"document":{"info":{"goal":"Ship it"}}}`
	prompt := PlanSidechatAgentPromptWithContext(context)
	for _, expected := range []string{
		"Authoritative pending plan context", context, "edit_pending_plan", "never claim that no plan is available",
		"document must be a native JSON object", "never pass document as JSON text", "quoted/stringified JSON",
		"including its current title", "Preserve that exact title", "never reuse a title from an older draft",
		`{"expected_revision":4,"document":{"title":"Plan: example"`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
	for _, expected := range []string{"only to Finder", "read-only contract", "cannot delegate further", "budgets", "depth checks"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing Finder delegation boundary %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, `"expected_revision":"4"`) || strings.Contains(prompt, `"document":"{`) {
		t.Fatalf("prompt example stringifies structured arguments: %s", prompt)
	}
}

func TestPlanSidechatIsRestrictedAndHidden(t *testing.T) {
	profile := PlanSidechatAgentProfile()
	if profile.Name != PlanSidechatAgentID || profile.Mode != ModeSubagent || !profile.Enabled {
		t.Fatalf("unexpected Plan sidechat profile: %+v", profile)
	}
	if profile.ExitPlanModeEnabled == nil || *profile.ExitPlanModeEnabled {
		t.Fatal("Plan sidechat must not exit plan mode")
	}
	for _, name := range []string{"write", "edit", "bash", "plan_manage", "ask_user", "exit_plan_mode", "manage_agent"} {
		config, ok := profile.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || *config.Enabled {
			t.Fatalf("tool %q must be explicitly disabled", name)
		}
	}
	for _, name := range []string{"read", "search", "list", "websearch", "webfetch", "edit_pending_plan", "task"} {
		config, ok := profile.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || !*config.Enabled {
			t.Fatalf("tool %q must be enabled", name)
		}
	}

	svc, agents := newTestService(t)
	if _, err := svc.ResolvePlanSidechatAgent(PlanSidechatAgentID); err != nil {
		t.Fatalf("dedicated resolver: %v", err)
	}
	if _, ok, err := agents.GetProfile(PlanSidechatAgentID); err != nil || ok {
		t.Fatalf("reserved profile persisted ok=%v err=%v", ok, err)
	}
	if _, err := svc.ResolveAgent(PlanSidechatAgentID); err == nil {
		t.Fatal("reserved profile resolved through normal agent API")
	}
	if _, _, _, err := svc.Upsert(UpsertInput{Name: PlanSidechatAgentID, Mode: ModeSubagent, Prompt: "replace"}); err == nil {
		t.Fatal("reserved profile was mutable")
	}
}

func TestFinderContractExplicitlyDisablesRecursiveTask(t *testing.T) {
	profile := FinderAgentProfileForParent(pebblestore.AgentProfile{})
	config, ok := profile.ToolContract.Tools["task"]
	if !ok || config.Enabled == nil || *config.Enabled {
		t.Fatal("Finder task capability must be explicitly disabled")
	}
}

func TestReservedAISidechatUsesCanonicalModelAndDisablesPlanTransitions(t *testing.T) {
	parent := pebblestore.AgentProfile{Provider: "single-provider", Model: "single-model", AutoProvider: "auto-provider", AutoModel: "auto-model", AutoThinking: "high", ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"write": {Enabled: pebblestore.BoolPtr(true)}, "plan_manage": {Enabled: pebblestore.BoolPtr(true)}}}}
	profile := AISidechatAgentProfileForParent(parent)
	if profile.Name != AISidechatAgentID || profile.Provider != "single-provider" || profile.Model != "single-model" || profile.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite {
		t.Fatalf("unexpected AI sidechat: %+v", profile)
	}
	for _, name := range []string{"plan_manage", "exit_plan_mode", "manage_agent", "ask_user"} {
		if cfg := profile.ToolContract.Tools[name]; cfg.Enabled == nil || *cfg.Enabled {
			t.Fatalf("%s must be disabled", name)
		}
	}
	fallback := AISidechatAgentProfileForParent(pebblestore.AgentProfile{Provider: "single-provider", Model: "single-model"})
	if fallback.Provider != "single-provider" || fallback.Model != "single-model" {
		t.Fatalf("single-model fallback failed: %+v", fallback)
	}
}
