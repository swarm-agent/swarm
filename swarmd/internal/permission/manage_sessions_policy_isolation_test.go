package permission

import "testing"

func TestManageSessionsMutationPolicyIdentityIsolation(t *testing.T) {
	actions := []struct {
		identity string
		args     string
	}{
		{identity: "session_commit", args: `{"action":"commit","commits":[{"session_id":"session-1","message":"test"}]}`},
		{identity: "session_archive", args: `{"action":"archive","session_ids":["session-1"]}`},
		{identity: "session_unarchive", args: `{"action":"unarchive","session_ids":["session-1"]}`},
	}
	for _, allowed := range actions {
		policy := Policy{Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: allowed.identity, Decision: PolicyDecisionAllow}}}
		for _, attempted := range actions {
			want := PolicyDecisionAsk
			if attempted.identity == allowed.identity {
				want = PolicyDecisionAllow
			}
			if got := ExplainPolicy("auto", "manage_sessions", attempted.args, policy).Decision; got != want {
				t.Fatalf("rule %s action %s = %s, want %s", allowed.identity, attempted.identity, got, want)
			}
		}
		generic := Policy{Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "manage_sessions", Decision: PolicyDecisionAllow}}}
		if got := ExplainPolicy("auto", "manage_sessions", allowed.args, generic).Decision; got != PolicyDecisionAsk {
			t.Fatalf("generic rule authorized %s: %s", allowed.identity, got)
		}
	}
}

func TestManageSessionsDeployCannotBePersistentlyAllowedOrBypassed(t *testing.T) {
	args := `{"action":"deploy","proposals":[{"prompt":"work"}]}`
	policy := Policy{Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "session_deploy", Decision: PolicyDecisionAllow}}}
	for _, mode := range []string{"auto", "auto+bypass_permissions"} {
		if got := ExplainPolicy(mode, "manage_sessions", args, policy).Decision; got != PolicyDecisionAsk {
			t.Fatalf("deploy mode %s = %s, want ask", mode, got)
		}
	}
}
