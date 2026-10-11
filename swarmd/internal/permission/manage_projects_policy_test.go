package permission

import (
	"fmt"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: in a project chat the orchestrator's read-only management calls
// (manage_projects list/get/list_sources/inspect_source/inspect_files/
// list_media/get_media/list_tasks/get_task and manage_sessions
// list/search/get/read_messages) must not stop the run with an approval card,
// while every manage_projects action that writes, deploys, deletes or pends
// the run still asks under the default policy (task proposals that only wait
// on their card are covered by TestPendingTaskProposalsNeedNoToolApproval),
// and an explicit deny rule still wins. inspect_files is a read: it resolves
// an authorized source or exact quiescent task result and runs one
// read/list/search/find tool confined to that tree. Owner:
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
		{"manage_projects", `{"action":"inspect_files","project_id":"p","workspace_id":"w","inspection":{"tool":"read","arguments":{"path":"README.md"}}}`},
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
		`{"action":"report_task","project_id":"p"}`,
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

// Purpose: an Orchestrator task proposal whose created card waits in
// pending_approval already has one approval gate, the card's Approve, so the
// manage_projects propose_task/create_task call must not raise a second
// approval. Any proposal that could start work or spend before that Approve
// keeps the tool-call approval: auto_approve (starts at once), a plan agent
// (starts a planning session), media agents/intents/operations (may generate
// immediately), a missing title (Router naming call) or a missing source
// (Router source selection), plus malformed or non-string fields. Explicit ask
// and deny rules still win. Owner: isManageProjectsPendingTaskProposal in
// defaultPolicyDecision behind Service.AuthorizeToolCall, the narrowest layer
// that decides and would create the pending approval record; the creation
// statuses it relies on are owned by api.Server.CreateProjectTask.
func TestPendingTaskProposalsNeedNoToolApproval(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-task-proposal.pebble"))
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
		SessionID: "session-task-proposal", Title: "Project chat", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		UserID: "user-a", AccountScopeID: "account-a", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)
	call := 0
	authorize := func(args string) AuthorizationResult {
		t.Helper()
		call++
		auth, err := svc.AuthorizeToolCall(AuthorizationInput{
			SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-1", CallID: fmt.Sprintf("call-%d", call), ToolName: "manage_projects", ToolArguments: args, Mode: sessionruntime.ModeAuto,
		})
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return auth
	}
	pendingCards := []string{
		`{"action":"propose_task","project_id":"p","title":"Fix login","agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"create_task","project_id":"p","title":"Fix login","agent":"coder","workspace_id":"w","workspace_generation":2,"auto_approve":false}`,
		`{"action":"propose_task","project_id":"p","title":"Checkout","agent":"swarm","feature_size":"big","workspace_id":"w","plan_document":{"id":"plan","title":"Checkout","checkpoints":[]}}`,
		`{"action":"propose_task","project_id":"p","title":"Two fixes","agent":"swarm","workspace_path":"/workspace","coder_assignments":[{"title":"a"},{"title":"b"}]}`,
		`{"action":"propose_task","project_id":"p","title":"Audit","agent":"finder","workspace_path":"/workspace"}`,
		`{"action":"PROPOSE_TASK","project_id":"p","title":"Fix","intent":"code","workspace_path":"/workspace"}`,
	}
	for _, args := range pendingCards {
		if auth := authorize(args); auth.Decision != AuthorizationApprove || auth.Record != nil {
			t.Fatalf("%s = %+v, want approved without a pending record", args, auth)
		}
	}
	startsOrSpends := []string{
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":"/workspace","auto_approve":true}`,
		`{"action":"create_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":"/workspace","auto_approve":"false"}`,
		`{"action":"propose_task","project_id":"p","title":"Plan","agent":"plan","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Logo","agent":"image","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Clip","agent":"video","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Jingle","agent":"sound","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Board","agent":"designer","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Logo","agent":"swarm","intent":"image","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Trim","agent":"coder","operation":"edit","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Clip","operation":"create","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Fix login","agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"Coder","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":["coder"],"workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":7,"agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"accept_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"deploy_task","project_id":"p","task_id":"t"}`,
		`not json`,
	}
	for _, args := range startsOrSpends {
		if auth := authorize(args); auth.Decision != AuthorizationPending || auth.Record == nil {
			t.Fatalf("%s = %+v, want a pending approval", args, auth)
		}
	}
	card := pendingCards[0]
	for _, decision := range []PolicyDecision{PolicyDecisionAsk, PolicyDecisionDeny} {
		policy := DefaultPolicy()
		policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: "manage_projects", Decision: decision})
		if got := ExplainPolicy("auto", "manage_projects", card, policy); got.Decision != decision {
			t.Fatalf("explicit %s rule = %+v, want %s", decision, got, decision)
		}
	}
}
