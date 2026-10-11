package permission

import (
	"fmt"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: in a project chat the orchestrator's read-only management calls
// (manage_projects list/get/list_sources/inspect_source/list_media/get_media/
// list_tasks/get_task and manage_sessions list/search/get/read_messages) must
// not stop the run with an approval card, while every manage_projects action
// that writes, deploys, deletes, runs file tools or pends the run still asks
// under the default policy, and an explicit deny rule still wins. Owner:
// defaultPolicyDecision / IsManageProjectsReadOnlyAction behind
// Service.AuthorizeToolCall, the narrowest layer that both decides and would
// create the pending approval record.
func TestManagementReadActionsNeedNoApproval(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-manage-projects.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "session-project-chat", Title: "Project chat", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		UserID: "user-a", AccountScopeID: "account-a", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)
	call := 0
	authorize := func(tool, args string) AuthorizationResult {
		t.Helper()
		call++
		auth, err := svc.AuthorizeToolCall(AuthorizationInput{
			SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-1", CallID: fmt.Sprintf("call-%d", call), ToolName: tool, ToolArguments: args, Mode: sessionruntime.ModeAuto,
		})
		if err != nil {
			t.Fatalf("%s %s: %v", tool, args, err)
		}
		return auth
	}

	reads := []struct{ tool, args string }{
		{"manage_projects", `{}`},
		{"manage_projects", `{"action":"list"}`},
		{"manage_projects", `{"action":"get","id":"p"}`},
		{"manage_projects", `{"action":"list_sources","project_id":"p"}`},
		{"manage_projects", `{"action":"inspect_source","project_id":"p","workspace_path":"/workspace"}`},
		{"manage_projects", `{"action":"list_media","project_id":"p"}`},
		{"manage_projects", `{"action":"get_media","project_id":"p","media_id":"m"}`},
		{"manage_projects", `{"action":"list_tasks","project_id":"p"}`},
		{"manage_projects", `{"action":"get_task","project_id":"p","task_id":"t"}`},
		{"manage_sessions", `{"action":"list"}`},
		{"manage-sessions", `{"action":"read_messages","session_id":"s"}`},
		{"manage_sessions", `{"action":"get","session_id":"s"}`},
		{"manage_sessions", `{"action":"search","query":"x"}`},
	}
	for _, read := range reads {
		if auth := authorize(read.tool, read.args); auth.Decision != AuthorizationApprove || auth.Record != nil {
			t.Fatalf("%s %s = %+v, want approved without a pending record", read.tool, read.args, auth)
		}
	}
	for _, args := range []string{
		`{"action":"create","name":"x"}`, `{"action":"update","id":"p"}`, `{"action":"delete","id":"p"}`,
		`{"action":"synthesize_context","id":"p"}`, `{"action":"propose_task","project_id":"p"}`, `{"action":"create_task","project_id":"p"}`,
		`{"action":"approve_task","project_id":"p"}`, `{"action":"accept_task","project_id":"p"}`, `{"action":"deploy_task","project_id":"p"}`,
		`{"action":"refine_task","project_id":"p"}`, `{"action":"reopen_task","project_id":"p"}`, `{"action":"update_task","project_id":"p"}`,
		`{"action":"archive_task","project_id":"p"}`, `{"action":"unarchive_task","project_id":"p"}`, `{"action":"delete_task","project_id":"p"}`,
		`{"action":"reconcile_tasks","project_id":"p"}`, `{"action":"edit_requirements","project_id":"p"}`, `{"action":"wait_tasks","project_id":"p"}`,
		`{"action":"report_task","project_id":"p"}`, `{"action":"inspect_files","project_id":"p"}`,
		`{"action":7}`, `not json`, `{"action":"list_tasks_and_delete"}`,
	} {
		if auth := authorize("manage_projects", args); auth.Decision != AuthorizationPending || auth.Record == nil {
			t.Fatalf("manage_projects %s = %+v, want a pending approval", args, auth)
		}
	}
	deny := DefaultPolicy()
	deny.Rules = append(deny.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: "manage_projects", Decision: PolicyDecisionDeny})
	if got := ExplainPolicy("auto", "manage_projects", `{"action":"list_tasks","project_id":"p"}`, deny); got.Decision != PolicyDecisionDeny {
		t.Fatalf("explicit deny rule = %+v, want deny", got)
	}
}
