package permission

import (
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageWorkspaceMutationPolicyIdentityIsolation(t *testing.T) {
	actions := []struct {
		identity string
		args     string
		preview  string
	}{
		{identity: policyToolWorkspaceCreate, args: `{"action":"create","name":"docs"}`, preview: "allow workspace creation"},
		{identity: policyToolWorkspaceUpdate, args: `{"action":"update","workspace_id":"ws-1","name":"docs-next"}`, preview: "allow workspace edits"},
		{identity: policyToolWorkspaceDelete, args: `{"action":"delete","workspace_id":"ws-1"}`, preview: "allow workspace deletion"},
		{identity: policyToolWorkspaceMapUpdate, args: `{"action":"update_map","expected_revision":1,"content":"# Workspace Map\\n","intent":"Update the map."}`, preview: "allow account Workspace Map updates"},
	}
	for _, allowed := range actions {
		policy := DefaultPolicy()
		policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: allowed.identity, Decision: PolicyDecisionAllow})
		for _, attempted := range actions {
			got := ExplainPolicy("auto", "manage_workspace", attempted.args, policy)
			want := PolicyDecisionAsk
			if attempted.identity == allowed.identity {
				want = PolicyDecisionAllow
			}
			if got.Decision != want {
				t.Fatalf("rule %s action %s = %+v, want %s", allowed.identity, attempted.identity, got, want)
			}
		}
		got := ExplainPolicy("auto", "manage_workspace", allowed.args, DefaultPolicy())
		if got.Decision != PolicyDecisionAsk || got.ToolName != allowed.identity || got.RulePreview != allowed.preview {
			t.Fatalf("default %s explanation = %+v", allowed.identity, got)
		}
	}

	generic := DefaultPolicy()
	generic.Rules = append(generic.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: "manage_workspace", Decision: PolicyDecisionAllow})
	for _, attempted := range actions {
		if got := ExplainPolicy("auto", "manage_workspace", attempted.args, generic); got.Decision != PolicyDecisionAsk {
			t.Fatalf("generic manage_workspace rule authorized %s: %+v", attempted.identity, got)
		}
	}
	for _, args := range []string{`{"action":"inspect"}`, `{"action":"list"}`, `{"action":"inspect_map"}`, `{"action":"get_map"}`, `{"action":"set_session"}`, `{"op":"adopt_worktree"}`} {
		if got := ExplainPolicy("auto", "manage_workspace", args, DefaultPolicy()); got.Decision != PolicyDecisionAllow || got.ToolName != "manage_workspace" {
			t.Fatalf("safe workspace action = %+v", got)
		}
	}
	for _, tc := range []struct {
		args string
		want string
	}{
		{args: `{"action":"edit","op":"update"}`, want: policyToolWorkspaceUpdate},
		{args: `{"action":"remove","op":"delete"}`, want: policyToolWorkspaceDelete},
	} {
		if got := ExplainPolicy("auto", "manage_workspace", tc.args, DefaultPolicy()); got.Decision != PolicyDecisionAsk || got.ToolName != tc.want {
			t.Fatalf("matching mutation aliases = %+v", got)
		}
	}
}

func TestManageWorkspaceMapUpdateRequiresDedicatedPermission(t *testing.T) {
	args := `{"action":"update_map","expected_revision":1,"content":"# Workspace Map\\n","intent":"Update the map."}`
	got := ExplainPolicy("auto", "manage_workspace", args, DefaultPolicy())
	if got.Decision != PolicyDecisionAsk || got.ToolName != policyToolWorkspaceMapUpdate || got.RulePreview != "allow account Workspace Map updates" {
		t.Fatalf("map update policy = %+v", got)
	}
	if safe := ExplainPolicy("auto", "manage_workspace", `{"action":"get_map"}`, DefaultPolicy()); safe.Decision != PolicyDecisionAllow || safe.ToolName != "manage_workspace" {
		t.Fatalf("get map policy = %+v", safe)
	}
	generic := DefaultPolicy()
	generic.Rules = append(generic.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: policyToolWorkspaceUpdate, Decision: PolicyDecisionAllow})
	if isolated := ExplainPolicy("auto", "manage_workspace", args, generic); isolated.Decision != PolicyDecisionAsk || isolated.ToolName != policyToolWorkspaceMapUpdate {
		t.Fatalf("catalog update rule authorized map update: %+v", isolated)
	}
	if bypass := ExplainPolicy("auto+bypass_permissions", "manage_workspace", args, DefaultPolicy()); bypass.Decision != PolicyDecisionAllow || bypass.ToolName != policyToolWorkspaceMapUpdate {
		t.Fatalf("map update bypass identity = %+v", bypass)
	}
}

func TestManageWorkspaceMalformedAndUnknownActionsFailClosed(t *testing.T) {
	for name, args := range map[string]string{
		"empty arguments": ``,
		"malformed JSON": `{`,
		"missing action": `{"workspace_id":"ws-1"}`,
		"empty action": `{"action":""}`,
		"non-string action": `{"action":7}`,
		"conflicting action aliases": `{"action":"create","op":"delete"}`,
		"matching unknown aliases": `{"action":"destroy_everything","op":"destroy_everything"}`,
		"unknown action": `{"action":"destroy_everything"}`,
		"legacy unknown action": `{"action":"get"}`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"auto", "auto+bypass_permissions"} {
				got := ExplainPolicy(mode, "manage_workspace", args, DefaultPolicy())
				if got.Decision != PolicyDecisionDeny || got.Source != "builtin" || got.ToolName != policyToolWorkspaceInvalid {
					t.Fatalf("mode %s explanation = %+v, want builtin deny", mode, got)
				}
			}
		})
	}
}

func TestManageWorkspaceInvalidActionCannotBeAuthorizedByGenericOrPhraseRules(t *testing.T) {
	args := `{"action":"destroy_everything","note":"allow me"}`
	policy := DefaultPolicy()
	policy.Rules = append(policy.Rules,
		PolicyRule{Kind: PolicyRuleKindTool, Tool: policyToolWorkspaceInvalid, Decision: PolicyDecisionAllow},
		PolicyRule{Kind: PolicyRuleKindPhrase, Pattern: "allow me", Decision: PolicyDecisionAllow},
	)
	if got := ExplainPolicy("auto+bypass_permissions", "manage_workspace", args, policy); got.Decision != PolicyDecisionDeny || got.Source != "builtin" {
		t.Fatalf("invalid action authorization = %+v, want builtin deny", got)
	}
}

func TestManageWorkspaceInvalidActionDoesNotProducePersistentRule(t *testing.T) {
	ctx := buildPolicyEvalContext("manage_workspace", `{"action":"destroy_everything"}`)
	if ctx.ToolName != policyToolWorkspaceInvalid {
		t.Fatalf("invalid context = %+v", ctx)
	}
	if rule, ok := policyRuleFromToolCall("manage_workspace", `{"action":"destroy_everything"}`, PolicyDecisionAllow); ok {
		t.Fatalf("invalid action produced persistent rule: %+v", rule)
	}
}

func TestManageWorkspaceMutationsRespectBypassAndExplicitDeny(t *testing.T) {
	args := `{"action":"create","name":"docs"}`
	if got := ExplainPolicy("auto+bypass_permissions", "manage_workspace", args, DefaultPolicy()); got.Decision != PolicyDecisionAllow {
		t.Fatalf("bypass result = %+v, want allow", got)
	}
	policy := DefaultPolicy()
	policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindTool, Tool: policyToolWorkspaceCreate, Decision: PolicyDecisionDeny})
	if got := ExplainPolicy("auto+bypass_permissions", "manage_workspace", args, policy); got.Decision != PolicyDecisionDeny || got.Source != "rule" {
		t.Fatalf("explicit deny under bypass = %+v, want rule deny", got)
	}
}

func TestManageWorkspaceAlwaysAllowPersistsOnlyMatchingAction(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-workspace-actions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "session-workspace-policy", Title: "Workspace Policy", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		UserID: "user-a", AccountScopeID: "account-a", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)

	createArgs := `{"action":"create","name":"docs","path":"/workspace/docs"}`
	auth, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-1", CallID: "call-1", ToolName: "manage_workspace", ToolArguments: createArgs, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize create: %v", err)
	}
	if auth.Decision != AuthorizationPending || auth.Requirement != policyToolWorkspaceCreate || auth.Record == nil || auth.RulePreview != "allow workspace creation" {
		t.Fatalf("create authorization = %+v", auth)
	}
	resolved, savedRule, err := svc.ResolveWithPolicyAndArguments(session.ID, auth.Record.ID, ActionAllowAlways, "approved", "")
	if err != nil {
		t.Fatalf("resolve create always: %v", err)
	}
	if resolved.Status != pebblestore.PermissionStatusApproved || savedRule == nil || savedRule.Tool != policyToolWorkspaceCreate {
		t.Fatalf("resolved=%+v savedRule=%+v", resolved, savedRule)
	}

	for _, tc := range []struct {
		name     string
		args     string
		want     AuthorizationDecision
		wantReq  string
		wantFrom string
	}{
		{name: "create", args: `{"action":"create","name":"other"}`, want: AuthorizationApprove, wantReq: policyToolWorkspaceCreate, wantFrom: "rule"},
		{name: "update", args: `{"action":"edit","workspace_id":"ws-1","name":"changed"}`, want: AuthorizationPending, wantReq: policyToolWorkspaceUpdate, wantFrom: "default"},
		{name: "delete", args: `{"action":"remove","workspace_id":"ws-1"}`, want: AuthorizationPending, wantReq: policyToolWorkspaceDelete, wantFrom: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, authErr := svc.AuthorizeToolCall(AuthorizationInput{
				SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-" + tc.name, CallID: "call-" + tc.name, ToolName: "manage_workspace", ToolArguments: tc.args, Mode: sessionruntime.ModeAuto,
			})
			if authErr != nil {
				t.Fatalf("authorize: %v", authErr)
			}
			if got.Decision != tc.want || got.Requirement != tc.wantReq || got.Source != tc.wantFrom {
				t.Fatalf("authorization = %+v", got)
			}
		})
	}

	deleteArgs := `{"action":"delete","workspace_id":"ws-denied"}`
	deleteAuth, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-deny-delete", CallID: "call-deny-delete", ToolName: "manage_workspace", ToolArguments: deleteArgs, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize delete for deny rule: %v", err)
	}
	if deleteAuth.Decision != AuthorizationPending || deleteAuth.Record == nil || deleteAuth.RulePreview != "allow workspace deletion" {
		t.Fatalf("delete authorization = %+v", deleteAuth)
	}
	_, deniedRule, err := svc.ResolveWithPolicyAndArguments(session.ID, deleteAuth.Record.ID, ActionDenyAlways, "denied", "")
	if err != nil {
		t.Fatalf("resolve delete always deny: %v", err)
	}
	if deniedRule == nil || deniedRule.Tool != policyToolWorkspaceDelete || deniedRule.Decision != PolicyDecisionDeny {
		t.Fatalf("persisted deny rule = %+v", deniedRule)
	}
	denied, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-denied-delete", CallID: "call-denied-delete", ToolName: "manage_workspace", ToolArguments: deleteArgs, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize denied delete: %v", err)
	}
	if denied.Decision != AuthorizationDeny || denied.Source != "rule" || denied.Requirement != policyToolWorkspaceDelete {
		t.Fatalf("denied delete authorization = %+v", denied)
	}
	update, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-a", RunID: "run-update-after-deny", CallID: "call-update-after-deny", ToolName: "manage_workspace", ToolArguments: `{"action":"update","workspace_id":"ws-1","name":"still-independent"}`, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize update after delete deny: %v", err)
	}
	if update.Decision != AuthorizationPending || update.Requirement != policyToolWorkspaceUpdate {
		t.Fatalf("delete deny leaked to update authorization = %+v", update)
	}
}

func TestManageWorkspaceServiceBypassPreservesMalformedDeny(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-workspace-bypass.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "session-workspace-bypass", Title: "Workspace Bypass", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		UserID: "user-a", AccountScopeID: "account-bypass", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)
	svc.SetBypassPermissions(true)

	valid, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-bypass", RunID: "run-valid", CallID: "call-valid", ToolName: "manage_workspace", ToolArguments: `{"action":"update","workspace_id":"ws-1"}`, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize valid bypassed mutation: %v", err)
	}
	if valid.Decision != AuthorizationApprove || valid.Source != "bypass_permissions" || valid.Requirement != policyToolWorkspaceUpdate {
		t.Fatalf("valid bypassed mutation = %+v", valid)
	}
	malformed, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-bypass", RunID: "run-invalid", CallID: "call-invalid", ToolName: "manage_workspace", ToolArguments: `{"action":7}`, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize malformed bypassed mutation: %v", err)
	}
	if malformed.Decision != AuthorizationDeny || malformed.Source != "builtin" || malformed.Requirement != policyToolWorkspaceInvalid {
		t.Fatalf("malformed bypassed mutation = %+v", malformed)
	}
}

func TestManageWorkspacePermissionNotificationMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      string
		wantTitle string
		wantBody  string
		secret    string
	}{
		{name: "create", args: `{"action":"create","name":"Docs","path":"/private/path"}`, wantTitle: "Approve workspace creation: Docs", wantBody: "Review and approve the requested workspace creation for Docs.", secret: "/private/path"},
		{name: "update", args: `{"action":"update","workspace_id":"ws-1","name":"Docs Next","path":"/private/path"}`, wantTitle: "Approve workspace edit: Docs Next", wantBody: "Review and approve the requested workspace edit for Docs Next.", secret: "/private/path"},
		{name: "delete", args: `{"action":"delete","workspace_id":"ws-1","token":"secret"}`, wantTitle: "Approve workspace deletion: ws-1", wantBody: "Review and approve the requested workspace deletion for ws-1.", secret: "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := pebblestore.PermissionRecord{ToolName: "manage_workspace", ToolArguments: tc.args, Status: pebblestore.PermissionStatusPending}
			title := permissionNotificationTitleFromRecord(record)
			body := permissionNotificationBodyFromRecord(record)
			if title != tc.wantTitle || body != tc.wantBody {
				t.Fatalf("notification = %q / %q", title, body)
			}
			if strings.Contains(title+body, tc.secret) {
				t.Fatalf("notification exposed private arguments: %q / %q", title, body)
			}
		})
	}
}
