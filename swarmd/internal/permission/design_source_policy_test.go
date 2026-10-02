package permission

import "testing"

// Purpose: ExplainDesignSourceRead must honor account restrictions before bypass
// without changing ordinary read authorization. This service-layer test uses the
// real policy store to prove default/explicit/bypass precedence and account
// isolation, preventing a sharing exception from weakening unrelated reads.
func TestDesignSourcePolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bypass bool
		rule   PolicyDecision
		want   PolicyDecision
	}{
		{"default", false, "", PolicyDecisionAsk},
		{"bypass", true, "", PolicyDecisionAllow},
		{"allow", false, PolicyDecisionAllow, PolicyDecisionAllow},
		{"deny", true, PolicyDecisionDeny, PolicyDecisionDeny},
		{"sensitive-consent", true, PolicyDecisionAsk, PolicyDecisionAsk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, sessionID, _, cleanup := newPermissionLifecycleTestService(t, "")
			defer cleanup()
			svc.SetBypassPermissions(tc.bypass)
			if tc.rule != "" {
				if _, err := svc.UpsertRuleForAccount("account-lifecycle", PolicyRule{Kind: PolicyRuleKindTool, Tool: "read", Decision: tc.rule}); err != nil {
					t.Fatal(err)
				}
			}
			args := `{"path":"source.svg","critical":true,"purpose":"Capture source bytes for delegated Designer provider context"}`
			got, err := svc.ExplainDesignSourceRead("account-lifecycle", args)
			if err != nil || got.Decision != tc.want {
				t.Fatalf("sharing decision: %+v %v", got, err)
			}
			foreign, err := svc.ExplainDesignSourceRead("another-account", args)
			wantForeign := PolicyDecisionAsk
			if tc.bypass {
				wantForeign = PolicyDecisionAllow
			}
			if err != nil || foreign.Decision != wantForeign {
				t.Fatalf("account policy leaked: %+v %v", foreign, err)
			}
			if tc.rule == "" {
				read, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", ToolName: "read", ToolArguments: args, Mode: "auto"})
				if err != nil || read.Decision != AuthorizationApprove || read.Record != nil {
					t.Fatalf("ordinary read changed: %+v %v", read, err)
				}
			}
			pending, err := svc.ListPending(sessionID, 20)
			if err != nil || len(pending) != 0 {
				t.Fatalf("policy explanation mutated pending state: %+v %v", pending, err)
			}
		})
	}
}
