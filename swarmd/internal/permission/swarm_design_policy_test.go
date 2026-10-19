package permission

import "testing"

// Requirement: swarm_design (design model calls that write workspace files) is
// auto-approved by default, denied in read-only sessions, and an explicit
// account deny rule still wins. Threat: design generation silently blocks on
// a prompt nobody answers, writes files from a read-only session, or ignores
// the owner's deny rule. Owners: defaultPolicyDecision and ExplainPolicy; the
// pure policy evaluator is the narrowest layer for these decisions.
func TestSwarmDesignPolicyAutoApprovesUnlessDeniedOrReadOnly(t *testing.T) {
	args := `{"brief":"a pricing page"}`
	if got := ExplainPolicy("auto", "swarm_design", args, DefaultPolicy()).Decision; got != PolicyDecisionAllow {
		t.Fatalf("auto default = %s, want allow", got)
	}
	if got := ExplainPolicy("read", "swarm_design", args, DefaultPolicy()).Decision; got != PolicyDecisionDeny {
		t.Fatalf("read-only default = %s, want deny", got)
	}
	deny := DefaultPolicy()
	deny.Rules = append(deny.Rules, PolicyRule{ID: "owner-denies-design", Kind: PolicyRuleKindTool, Decision: PolicyDecisionDeny, Tool: "swarm_design"})
	if got := ExplainPolicy("auto", "swarm_design", args, deny).Decision; got != PolicyDecisionDeny {
		t.Fatalf("explicit deny rule = %s, want deny", got)
	}
}
