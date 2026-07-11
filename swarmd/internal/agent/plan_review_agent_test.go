package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanReviewAgentInheritsModelWithoutInheritingCapabilities(t *testing.T) {
	parent := pebblestore.AgentProfile{
		Provider: "provider-a", Model: "model-a", Thinking: "high",
		Prompt: "private parent prompt", RuntimeMode: pebblestore.AgentRuntimeModeReadWrite,
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"write": {Enabled: pebblestore.BoolPtr(true)}}},
	}
	profile := PlanReviewAgentProfileForParent(parent)
	if profile.Provider != parent.Provider || profile.Model != parent.Model || profile.Thinking != parent.Thinking {
		t.Fatalf("model settings not inherited: %+v", profile)
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

func TestPlanReviewAgentIsRestrictedAndHidden(t *testing.T) {
	profile := PlanReviewAgentProfile()
	if profile.Name != PlanReviewAgentID || profile.Mode != ModeSubagent || !profile.Enabled {
		t.Fatalf("unexpected plan review profile: %+v", profile)
	}
	if profile.ExitPlanModeEnabled == nil || *profile.ExitPlanModeEnabled {
		t.Fatal("plan review agent must not exit plan mode")
	}
	for _, name := range []string{"write", "edit", "bash", "task", "plan_manage", "ask_user", "exit_plan_mode", "manage_agent"} {
		config, ok := profile.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || *config.Enabled {
			t.Fatalf("tool %q must be explicitly disabled", name)
		}
	}
	for _, name := range []string{"read", "search", "list"} {
		config, ok := profile.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || !*config.Enabled {
			t.Fatalf("tool %q must be enabled", name)
		}
	}

	svc, agents := newTestService(t)
	if _, err := svc.ResolvePlanReviewAgent(PlanReviewAgentID); err != nil {
		t.Fatalf("dedicated resolver: %v", err)
	}
	if _, ok, err := agents.GetProfile(PlanReviewAgentID); err != nil || ok {
		t.Fatalf("reserved profile persisted ok=%v err=%v", ok, err)
	}
	if _, err := svc.ResolveAgent(PlanReviewAgentID); err == nil {
		t.Fatal("reserved profile resolved through normal agent API")
	}
	if _, _, _, err := svc.Upsert(UpsertInput{Name: PlanReviewAgentID, Mode: ModeSubagent, Prompt: "replace"}); err == nil {
		t.Fatal("reserved profile was mutable")
	}
}
