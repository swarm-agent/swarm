package permission

import "testing"

// Purpose: recovery mutations must not inherit inspection's automatic approval.
// The policy identity boundary is the narrowest layer proving separate approval
// identities, including that neither reclaim nor copy aliases catalog updates.
func TestWorktreeRecoveryRequiresSeparateApproval(t *testing.T) {
	for action, want := range map[string]string{"reclaim_worktree": "workspace_reclaim", "copy_worktree": "workspace_copy"} {
		name, reason := manageWorkspacePolicyIdentity(`{"action":"` + action + `"}`)
		if name != want || reason != "" {
			t.Fatalf("identity: %q %q", name, reason)
		}
		if got := defaultPolicyDecision("auto", name, "{}"); got != PolicyDecisionAsk {
			t.Fatalf("recovery auto-approved: %v", got)
		}
	}
}
