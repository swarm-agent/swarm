package permission

import (
	"fmt"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanManageLifecycleRequirementIsTyped(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "followup", args: `{"action":"request_followup_checkpoint","change_request":"add a review note"}`, want: "plan_followup_request"},
		{name: "request changes alias", args: `{"action":"request_changes","change_request":"add a review note"}`, want: "plan_followup_request"},
		{name: "revision", args: `{"action":"request_plan_revision","plan_id":"plan_1"}`, want: "plan_revision_request"},
		{name: "amendment", args: `{"action":"amend_plan","plan_id":"plan_1","base_revision":2,"replace_from_checkpoint_id":"cp-2","document":{"id":"plan_1","title":"Plan","checkpoints":[{"id":"cp-2","status":"pending"}]}}`, want: "plan_amendment_request"},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction"}`, want: "plan_new_request"},
		{name: "legacy save existing", args: `{"action":"save","plan_id":"plan_1","document":{"info":{"goal":"update"}}}`, want: "plan_revision_request"},
		{name: "bulk operations existing", args: `{"action":"update_info","plan_id":"plan_1","operations":[{"operation":"update_info","info":{"goal":"bulk"}}]}`, want: "plan_revision_request"},
		{name: "bulk checkpoint reorder existing", args: `{"action":"reorder_checkpoints","checkpoint_order":["cp-2","cp-1"]}`, want: "plan_revision_request"},
		{name: "draft save no active plan id", args: `{"action":"save","document":{"info":{"goal":"draft"}}}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanManageLifecycleRequirement(tc.args); got != tc.want {
				t.Fatalf("PlanManageLifecycleRequirement() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthorizeToolCallResolvesFollowupPolicyBeforeCreatePending(t *testing.T) {
	cases := []struct {
		name          string
		globalDefault string
		planOverride  string
		toolPolicy    string
		wantDecision  AuthorizationDecision
	}{
		{name: "ask default creates permission", globalDefault: sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, wantDecision: AuthorizationPending},
		{name: "auto default approves without permission", globalDefault: sessionruntime.PlanFollowupCheckpointPolicyAutoStart, wantDecision: AuthorizationApprove},
		{name: "plan auto override beats ask default", globalDefault: sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, planOverride: sessionruntime.PlanFollowupCheckpointPolicyAutoStart, wantDecision: AuthorizationApprove},
		{name: "plan ask override beats auto default", globalDefault: sessionruntime.PlanFollowupCheckpointPolicyAutoStart, planOverride: sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, wantDecision: AuthorizationPending},
		{name: "caller supplied policy is ignored", globalDefault: sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, toolPolicy: sessionruntime.PlanFollowupCheckpointPolicyAutoStart, wantDecision: AuthorizationPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sessionID, planID, cleanup := newPermissionLifecycleTestService(t, tc.planOverride)
			defer cleanup()
			svc.SetFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) { return tc.globalDefault, nil })

			args := fmt.Sprintf(`{"action":"request_followup_checkpoint","plan_id":%q,"change_request":"add a review note"`, planID)
			if tc.toolPolicy != "" {
				args += fmt.Sprintf(`,"followup_checkpoint_policy":%q,"policy":%q`, tc.toolPolicy, tc.toolPolicy)
			}
			args += `}`
			auth, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-1", CallID: "call-1", ToolName: "plan_manage", ToolArguments: args, Mode: sessionruntime.ModeAuto})
			if err != nil {
				t.Fatalf("authorize tool call: %v", err)
			}
			if auth.Decision != tc.wantDecision {
				t.Fatalf("decision = %q, want %q (record=%v reason=%q source=%q)", auth.Decision, tc.wantDecision, auth.Record != nil, auth.Reason, auth.Source)
			}
			if tc.wantDecision == AuthorizationPending && auth.Record == nil {
				t.Fatalf("pending decision should include permission record")
			}
			if tc.wantDecision == AuthorizationApprove && auth.Record != nil {
				t.Fatalf("approved dynamic policy should not create permission record")
			}
		})
	}
}

func TestAuthorizeToolCallResolvesFollowupPolicyEveryTime(t *testing.T) {
	svc, sessionID, planID, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	globalDefault := sessionruntime.PlanFollowupCheckpointPolicyAutoStart
	svc.SetFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) { return globalDefault, nil })
	args := fmt.Sprintf(`{"action":"request_followup_checkpoint","plan_id":%q,"change_request":"add a review note"}`, planID)

	auth, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-1", CallID: "call-1", ToolName: "plan_manage", ToolArguments: args, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("authorize auto default: %v", err)
	}
	if auth.Decision != AuthorizationApprove {
		t.Fatalf("auto default decision = %q, want approved", auth.Decision)
	}

	globalDefault = sessionruntime.PlanFollowupCheckpointPolicyRequireApproval
	auth, err = svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-2", CallID: "call-2", ToolName: "plan_manage", ToolArguments: args, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("authorize ask default: %v", err)
	}
	if auth.Decision != AuthorizationPending || auth.Record == nil {
		t.Fatalf("ask default authorization = %+v, want pending record", auth)
	}
}

func TestAuthorizeToolCallKeepsRevisionAndNewPlanApprovalGated(t *testing.T) {
	svc, sessionID, planID, cleanup := newPermissionLifecycleTestService(t, sessionruntime.PlanFollowupCheckpointPolicyAutoStart)
	defer cleanup()
	svc.SetFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) {
		return sessionruntime.PlanFollowupCheckpointPolicyAutoStart, nil
	})

	cases := []struct {
		name string
		args string
	}{
		{name: "revision", args: fmt.Sprintf(`{"action":"request_plan_revision","plan_id":%q,"reason":"revise"}`, planID)},
		{name: "amendment", args: fmt.Sprintf(`{"action":"amend_plan","plan_id":%q,"base_revision":1,"replace_from_checkpoint_id":"cp-1","update_summary":"amend","document":{"id":%q,"title":"Plan","checkpoints":[{"id":"cp-1","status":"pending"}]}}`, planID, planID)},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-" + tc.name, CallID: "call-" + tc.name, ToolName: "plan_manage", ToolArguments: tc.args, Mode: sessionruntime.ModeAuto})
			if err != nil {
				t.Fatalf("authorize tool call: %v", err)
			}
			if auth.Decision != AuthorizationPending || auth.Record == nil {
				t.Fatalf("authorization = %+v, want pending record", auth)
			}
		})
	}
}

func newPermissionLifecycleTestService(t *testing.T, planOverride string) (*Service, string, string, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-lifecycle.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-lifecycle", Title: "Lifecycle", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, UserID: "user-lifecycle", AccountScopeID: "account-lifecycle", Preference: &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"}})
	if err != nil {
		cleanup()
		t.Fatalf("create session: %v", err)
	}
	doc := &pebblestore.SessionPlanDocument{ID: "plan-lifecycle", Title: "Lifecycle Plan", Status: "approved", ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed, FollowupCheckpointPolicy: planOverride}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: sessionruntime.PlanCheckpointStatusCompleted}}}
	plan, _, err := sessions.SavePlanWithMetadata(session.ID, doc.ID, doc.Title, "# Lifecycle Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: doc})
	if err != nil {
		cleanup()
		t.Fatalf("save plan: %v", err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessions)
	return svc, session.ID, plan.ID, cleanup
}
