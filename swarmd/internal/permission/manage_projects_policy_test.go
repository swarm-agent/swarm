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
// immediately), and malformed or non-string fields, including when the
// proposal is also untitled or sourceless. Per the owner's decision a missing
// title or source alone no longer asks: the small Router call that names the
// task or picks its source is accepted running cost and the card still waits
// for Approve. Explicit ask and deny rules still win, also for an untitled
// proposal. Owner: isManageProjectsPendingTaskProposal in
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
		// Owner decision: a missing title (Router naming call) or a missing
		// source (Router source pick) is accepted running cost; the card still
		// waits for Approve.
		`{"action":"propose_task","project_id":"p","prompt":"Fix login","agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder"}`,
		`{"action":"create_task","project_id":"p","prompt":"Refactor checkout","agent":"swarm"}`,
		`{"action":"propose_task","project_id":"p","description":"Find dead code","intent":"audit"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Audit auth","agent":"finder","title":null,"workspace_id":null}`,
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
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"Coder","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":["coder"],"workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":7,"agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder","workspace_id":7}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":["/workspace"]}`,
		// Untitled or sourceless proposals that start work or spend still ask.
		`{"action":"propose_task","project_id":"p","prompt":"Fix","agent":"coder","auto_approve":true}`,
		`{"action":"create_task","project_id":"p","prompt":"Spec it","agent":"plan"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Logo","agent":"image"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Logo","intent":"image"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Clip","agent":"coder","operation":"extend"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Board","intent":"design"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Fix"}`,
		`{"action":"accept_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":"/workspace"}`,
		`{"action":"deploy_task","project_id":"p","task_id":"t"}`,
		`not json`,
	}
	for _, args := range startsOrSpends {
		if auth := authorize(args); auth.Decision != AuthorizationPending || auth.Record == nil {
			t.Fatalf("%s = %+v, want a pending approval", args, auth)
		}
	}
	for _, card := range []string{pendingCards[0], `{"action":"propose_task","project_id":"p","prompt":"Fix","agent":"coder"}`} {
		for _, decision := range []PolicyDecision{PolicyDecisionAsk, PolicyDecisionDeny} {
			policy := DefaultPolicy()
			policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: "manage_projects", Decision: decision})
			if got := ExplainPolicy("auto", "manage_projects", card, policy); got.Decision != decision {
				t.Fatalf("explicit %s rule for %s = %+v, want %s", decision, card, got, decision)
			}
		}
	}
}

// Purpose: the Orchestrator's compiled tool contract turns its enabled
// manage_projects tool into the overlay rule "allow tool: manage_projects",
// which explainExplicitRule evaluates before defaultPolicyDecision. Without a
// separate policy identity that overlay (or any saved generic manage_projects
// allow rule) silently pre-approved the Orchestrator approving or deploying
// its own task cards, auto-approved/plan/media proposals that start work or
// spend at once, and project/task deletion and archiving, defeating the card's
// Approve gate. Requirement: those actions keep asking under that overlay;
// reads, pending-card proposals and other actions keep the shared identity; an
// explicit deny for manage_projects still blocks the split actions; a rule for
// one split identity does not authorize the other; bypass keeps its existing
// meaning; and the pending record is created without execution. Owner:
// manageProjectsPolicyIdentity in buildPolicyEvalContext and the transport
// deny in explainExplicitDeny, evaluated by Service.AuthorizeToolCall with the
// overlay, the narrowest layer that both decides and records the approval.
func TestProjectTaskLaunchAndRemovalKeepApprovalUnderToolAllow(t *testing.T) {
	overlay := &Policy{Version: 1, Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: "manage_projects", Decision: PolicyDecisionAllow}}}
	launches := []string{
		`{"action":"approve_task","project_id":"p","task_id":"t"}`,
		`{"action":" ACCEPT_TASK ","project_id":"p","task_id":"t"}`,
		`{"action":"deploy_task","project_id":"p","task_id":"t"}`,
		`{"action":"propose_task","project_id":"p","title":"Fix","agent":"coder","workspace_path":"/workspace","auto_approve":true}`,
		`{"action":"create_task","project_id":"p","prompt":"Spec it","agent":"plan"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Logo","agent":"image"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Clip","intent":"video"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Trim","agent":"coder","operation":"edit"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Fix"}`,
	}
	removals := []string{
		`{"action":"delete","id":"p"}`,
		`{"action":"Delete_Task","project_id":"p","task_id":"t"}`,
		`{"action":"archive_task","project_id":"p","task_id":"t"}`,
	}
	shared := []string{
		`{"action":"list"}`, `{}`,
		`{"action":"get_task","project_id":"p","task_id":"t"}`,
		`{"action":"propose_task","project_id":"p","prompt":"Fix login","agent":"coder"}`,
		`{"action":"wait_tasks","project_id":"p","task_ids":["t"]}`,
		`{"action":"update_task","project_id":"p","task_id":"t"}`,
	}
	for _, c := range []struct {
		identity string
		args     []string
	}{{policyToolProjectTaskLaunch, launches}, {policyToolProjectRemoval, removals}} {
		for _, args := range c.args {
			got := ExplainPolicy("auto", "manage_projects", args, *overlay)
			if got.Decision != PolicyDecisionAsk || got.ToolName != c.identity {
				t.Fatalf("%s under allow tool: manage_projects = %+v, want ask as %s", args, got, c.identity)
			}
			if bypass := ExplainPolicy("auto+bypass_permissions", "manage_projects", args, DefaultPolicy()); bypass.Decision != PolicyDecisionAllow {
				t.Fatalf("%s under bypass = %+v, want the existing bypass allow", args, bypass)
			}
			deny := Policy{Version: 1, Rules: append([]PolicyRule{{Kind: PolicyRuleKindTool, Tool: "manage-projects", Decision: PolicyDecisionDeny}}, overlay.Rules...)}
			if got := ExplainPolicy("auto+bypass_permissions", "manage_projects", args, deny); got.Decision != PolicyDecisionDeny {
				t.Fatalf("%s ignored deny tool: manage_projects: %+v", args, got)
			}
			rule, ok := policyRuleFromToolCall("manage_projects", args, PolicyDecisionAllow)
			if !ok || rule.Tool != c.identity {
				t.Fatalf("remembered rule for %s = %+v, want tool %s", args, rule, c.identity)
			}
		}
	}
	for _, args := range shared {
		if got := ExplainPolicy("auto", "manage_projects", args, *overlay); got.Decision != PolicyDecisionAllow || got.ToolName != "manage_projects" {
			t.Fatalf("%s = %+v, want the shared manage_projects identity allowed by the overlay", args, got)
		}
	}
	// A saved allow for one split identity covers only that identity.
	launchOnly := Policy{Version: 1, Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Tool: policyToolProjectTaskLaunch, Decision: PolicyDecisionAllow}}}
	if got := ExplainPolicy("auto", "manage_projects", launches[0], launchOnly); got.Decision != PolicyDecisionAllow {
		t.Fatalf("explicit project_task_launch allow = %+v", got)
	}
	if got := ExplainPolicy("auto", "manage_projects", removals[0], launchOnly); got.Decision != PolicyDecisionAsk {
		t.Fatalf("project_task_launch allow authorized a removal: %+v", got)
	}

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-project-launch.pebble"))
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
		SessionID: "session-project-launch", Title: "Project chat", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		UserID: "user-a", AccountScopeID: "account-a", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)
	for i, args := range append(append([]string{}, launches...), removals...) {
		auth, err := svc.AuthorizeToolCall(AuthorizationInput{
			SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-1", CallID: fmt.Sprintf("call-%d", i), ToolName: "manage_projects", ToolArguments: args, Mode: sessionruntime.ModeAuto, Overlay: overlay,
		})
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if auth.Decision != AuthorizationPending || auth.Record == nil || auth.Record.Status != pebblestore.PermissionStatusPending || auth.Record.ExecutionStatus != pebblestore.PermissionExecWaitingApproval {
			t.Fatalf("%s with the Orchestrator overlay = %+v, want a pending, unexecuted approval", args, auth)
		}
	}
	auth, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-1", CallID: "call-card", ToolName: "manage_projects", ToolArguments: shared[3], Mode: sessionruntime.ModeAuto, Overlay: overlay,
	})
	if err != nil || auth.Decision != AuthorizationApprove || auth.Record != nil {
		t.Fatalf("pending-card proposal with the overlay = %+v, %v; want approved without a record", auth, err)
	}
}
