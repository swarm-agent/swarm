package permission

import (
	"path/filepath"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: AuthorizeToolCall and rejectWorkerPrompt must use only ordinary
// owner bypass/policy authority for durable workflow reviews, without pending
// consent even during sandbox startup. Temporary durable tasks at this boundary
// prove decisions and pending-state postconditions without executing commands.
func TestWorkflowNativeReviewPermissions(t *testing.T) {
	for _, agent := range []string{"system-reviewer", "system-reviewer-fixer"} {
		t.Run(agent, func(t *testing.T) {
			for _, tc := range []struct {
				name, status, automation, command string
				bypass, blocked                   bool
				rule                              PolicyDecision
				want                              AuthorizationDecision
			}{
				{name: "bypass", bypass: true, want: AuthorizationApprove},
				{name: "chained mutation", command: "git log -1; git push origin HEAD:dev", want: AuthorizationDeny},
				{name: "owner ask", rule: PolicyDecisionAsk, want: AuthorizationDeny},
				{name: "policy allow", rule: PolicyDecisionAllow, want: AuthorizationApprove},
				{name: "policy deny", bypass: true, rule: PolicyDecisionDeny, want: AuthorizationDeny},
				{name: "startup", bypass: true, blocked: true, want: AuthorizationDeny},
				{name: "interactive", automation: "interactive", rule: PolicyDecisionAsk, want: AuthorizationPending},
				{name: "completed", status: "completed", rule: PolicyDecisionAsk, want: AuthorizationPending},
				{name: "failed", status: "failed", rule: PolicyDecisionAsk, want: AuthorizationPending},
				{name: "needs review", status: "needs_review", rule: PolicyDecisionAsk, want: AuthorizationPending},
			} {
				t.Run(tc.name, func(t *testing.T) {
					db, err := store.Open(filepath.Join(t.TempDir(), "state.pebble"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					sessions := store.NewSessionStore(db)
					status := tc.status
					if status == "" {
						status = "in_progress"
					}
					automation := tc.automation
					if automation == "" {
						automation = "workflow:review"
					}
					fixes := ""
					if agent == "system-reviewer-fixer" {
						fixes = "security"
					}
					task := &store.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Review", Agent: "reviewer", Status: status, SessionID: "review", AutomationID: automation, ReviewFixes: fixes}
					if err := sessions.PutProjectTask("account", task); err != nil {
						t.Fatal(err)
					}
					svc := NewService(store.NewPermissionStore(db), nil, nil)
					svc.SetSessionResolver(sessionAccessLookup{"review": {ID: "review", AccountScopeID: "account", Metadata: map[string]any{"project_id": "project", "task_id": "task", "agent_name": agent}}})
					svc.SetBypassPermissions(tc.bypass)
					svc.SetBypassGate(func() (bool, string) { return !tc.blocked, "sandbox starting" })
					var overlay *Policy
					if tc.rule != "" {
						overlay = &Policy{Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "bash", Decision: tc.rule}}}
					}
					command := tc.command
					if command == "" {
						command = "git log -1; git diff --stat"
					}
					input := AuthorizationInput{SessionID: "review", AccountScopeID: "account", RunID: "run", CallID: "call", Mode: "auto", ToolName: "bash", ToolArguments: `{"command":"` + command + `","category":"read","explanation":["Inspect repository history"],"critical":false}`, Overlay: overlay}
					got, err := svc.AuthorizeToolCall(input)
					if err != nil || got.Decision != tc.want {
						t.Fatalf("decision %+v err %v want %s", got, err, tc.want)
					}
					if tc.name == "bypass" && got.Source != "bypass_permissions" {
						t.Fatalf("not bypass: %+v", got)
					}
					wantPending := 0
					if tc.want == AuthorizationPending {
						wantPending = 1
					}
					pending, err := svc.PendingCount("review")
					if err != nil || pending != wantPending {
						t.Fatalf("pending %d err %v", pending, err)
					}
					if tc.blocked {
						if !strings.Contains(got.Reason, "retry the tool call after sandbox readiness") {
							t.Fatalf("missing retry guidance: %+v", got)
						}
						svc.SetBypassGate(func() (bool, string) { return true, "" })
						got, err = svc.AuthorizeToolCall(input)
						if err != nil || got.Decision != AuthorizationApprove || got.Source != "bypass_permissions" {
							t.Fatalf("ready retry %+v err %v", got, err)
						}
						pending, err = svc.PendingCount("review")
						if err != nil || pending != 0 {
							t.Fatalf("ready pending %d err %v", pending, err)
						}
					}
				})
			}
		})
	}
}
