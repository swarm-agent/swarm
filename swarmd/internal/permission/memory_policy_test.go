package permission

import "testing"

// Purpose: ExplainPolicy, the actual admission authority used by
// AuthorizeToolCall, must require memory mutation consent even under bypass and
// the compiled Orchestrator's enabled-tool allow overlay. The pure policy layer
// is the narrowest proof that inspect stays available and denial still wins;
// permissionRequirement/defaultPolicyDecision alone cannot prove this boundary.
func TestMemoryMutationConsentOverridesToolAllowAndBypass(t *testing.T) {
	allow := Policy{Version: 1, Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "manage_memory", Decision: PolicyDecisionAllow}}}
	for _, mode := range []string{"auto", "auto+bypass_permissions", "yolo"} {
		for _, args := range []string{
			`{"action":"remember","intent":"requested"}`,
			`{"action":"edit","intent":"requested"}`,
			`{"action":"forget","intent":"requested"}`,
			`{}`, `not-json`,
		} {
			got := ExplainPolicy(mode, "manage_memory", args, allow)
			if got.Decision != PolicyDecisionAsk {
				t.Fatalf("%s/%s skipped consent: %+v", mode, args, got)
			}
		}
		if got := ExplainPolicy(mode, "manage_memory", `{"action":"inspect"}`, allow); got.Decision != PolicyDecisionAllow {
			t.Fatalf("inspect blocked: %+v", got)
		}
	}
	deny := Policy{Version: 1, Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "manage_memory", Decision: PolicyDecisionDeny}}}
	for _, action := range []string{"inspect", "remember", "edit", "forget"} {
		if got := ExplainPolicy("auto+bypass_permissions", "manage_memory", `{"action":"`+action+`"}`, deny); got.Decision != PolicyDecisionDeny {
			t.Fatalf("%s ignored explicit denial: %+v", action, got)
		}
	}
}
