package permission

import (
	"path/filepath"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AuthorizeToolCall must let durable workflow-native reviews use their
// ordinary tools without pending approvals, while preserving policy refusals and
// interactive consent. A real temporary task store at the authorization boundary
// is the narrowest layer proving provenance and the absence of pending state.
func TestWorkflowNativeReviewPermissions(t *testing.T) {
	for _, tc := range []struct {
		name, agent, fixes, automation, session, account, tool, args string
		want                                                         AuthorizationDecision
	}{
		{"review", "system-reviewer", "", "workflow:review", "review", "account", "bash", `{"command":"git log -1; git diff --stat","category":"read"}`, AuthorizationApprove},
		{"fix bash", "system-reviewer-fixer", "security", "workflow:review", "review", "account", "bash", `{"command":"git diff --stat","category":"read"}`, AuthorizationApprove},
		{"fix commit", "system-reviewer-fixer", "security", "workflow:review", "review", "account", "git_commit", `{"message":"fix"}`, AuthorizationApprove},
		{"fix add", "system-reviewer-fixer", "security", "workflow:review", "review", "account", "git_add", `{"pathspec":["fix.go"]}`, AuthorizationApprove},
		{"interactive", "system-reviewer", "", "", "review", "account", "bash", `{"command":"git log -1","category":"read"}`, AuthorizationPending},
		{"stale session", "system-reviewer", "", "workflow:review", "other", "account", "bash", `{"command":"git log -1","category":"read"}`, AuthorizationPending},
		{"cross account", "system-reviewer", "", "workflow:review", "review", "other", "bash", `{"command":"git log -1","category":"read"}`, AuthorizationPending},
		{"hard refusal", "system-reviewer", "", "workflow:review", "review", "account", "bash", `{"command":"rm -rf /","category":"delete","critical":true}`, AuthorizationDeny},
		{"deny", "system-reviewer", "", "workflow:review", "review", "account", "bash", `{"command":"git log -1","category":"read"}`, AuthorizationDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "state.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sessions := store.NewSessionStore(db)
			task := &store.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Review", Agent: "reviewer", Status: "in_progress", SessionID: tc.session, AutomationID: tc.automation, ReviewFixes: tc.fixes}
			if err := sessions.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			svc := NewService(store.NewPermissionStore(db), nil, nil)
			svc.SetSessionResolver(sessionAccessLookup{"review": {ID: "review", AccountScopeID: "account", Metadata: map[string]any{"project_id": "project", "task_id": "task", "agent_name": tc.agent}}})
			var overlay *Policy
			if tc.name == "deny" {
				overlay = &Policy{Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "bash", Decision: PolicyDecisionDeny}}}
			}
			got, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: "review", AccountScopeID: tc.account, RunID: "run", CallID: "call", Mode: "auto", ToolName: tc.tool, ToolArguments: tc.args, Overlay: overlay})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision %+v want %s", got, tc.want)
			}
			pending, err := svc.PendingCount("review")
			if err != nil {
				t.Fatal(err)
			}
			wantPending := 0
			if tc.want == AuthorizationPending {
				wantPending = 1
			}
			if pending != wantPending {
				t.Fatalf("pending %d want %d", pending, wantPending)
			}
		})
	}
}
