package permission

import "testing"

func TestManageSessionsDefaultPermissionPolicyByAction(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		arguments   string
		want        PolicyDecision
		requirement string
	}{
		{name: "inspect", toolName: "manage-sessions", arguments: `{"action":"inspect"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "list", toolName: "manage_sessions", arguments: `{"action":"list"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "search", toolName: "manage-sessions", arguments: `{"action":"search","query":"needs review"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "get", toolName: "manage_sessions", arguments: `{"action":"get","session_id":"session-1"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "read messages", toolName: "manage-sessions", arguments: `{"action":"read_messages","session_id":"session-1"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "git status", toolName: "manage_sessions", arguments: `{"action":"git_status","session_id":"session-1"}`, want: PolicyDecisionAllow, requirement: "manage_sessions"},
		{name: "commit", toolName: "manage-sessions", arguments: `{"action":"commit","commits":[{"session_id":"session-1","message":"test"}]}`, want: PolicyDecisionAsk, requirement: "session_commit"},
		{name: "archive", toolName: "manage-sessions", arguments: `{"action":"archive","session_ids":["session-1"]}`, want: PolicyDecisionAsk, requirement: "session_archive"},
		{name: "unarchive", toolName: "manage-sessions", arguments: `{"action":"unarchive","session_ids":["session-1"]}`, want: PolicyDecisionAsk, requirement: "session_unarchive"},
		{name: "deploy", toolName: "manage-sessions", arguments: `{"action":"deploy","proposals":[{"prompt":"do work"}]}`, want: PolicyDecisionAsk, requirement: "session_deploy"},
		{name: "deploy bypass", toolName: "manage-sessions", arguments: `{"action":"deploy","proposals":[{"prompt":"do work"}]}`, want: PolicyDecisionAsk, requirement: "session_deploy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode := "auto"
			if tc.name == "deploy bypass" {
				mode = "auto+bypass_permissions"
			}
			explain := ExplainPolicy(mode, tc.toolName, tc.arguments, Policy{})
			if explain.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (reason=%q source=%q)", explain.Decision, tc.want, explain.Reason, explain.Source)
			}
			if got := authorizationRequirement("auto", tc.toolName, tc.arguments); got != tc.requirement {
				t.Fatalf("requirement = %q, want %q", got, tc.requirement)
			}
		})
	}
}
