package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

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
		{name: "amendment", args: `{"action":"amend_plan","plan_id":"plan_1","base_revision":2,"replace_from_checkpoint_id":"cp-2","document":{"id":"plan_1","title":"Plan","checkpoints":[{"id":"cp-2","status":"pending"}]}}`, want: "plan_amendment_request"},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction"}`, want: "plan_new_request"},
		{name: "legacy save existing", args: `{"action":"save","plan_id":"plan_1","document":{"info":{"goal":"update"}}}`, want: "plan_revision_request"},
		{name: "bulk operations existing", args: `{"action":"update_info","plan_id":"plan_1","operations":[{"operation":"update_info","info":{"goal":"bulk"}}]}`, want: "plan_revision_request"},
		{name: "bulk checkpoint reorder existing", args: `{"action":"reorder_checkpoints","checkpoint_order":["cp-2","cp-1"]}`, want: "plan_revision_request"},
		{name: "draft save no active plan id", args: `{"action":"save","document":{"info":{"goal":"draft"}}}`, want: ""},
		{name: "add subtask is execution state", args: `{"action":"add_subtask","plan_id":"plan_1","checkpoint_id":"cp-1","subtask":{"title":"docs"}}`, want: ""},
		{name: "focus subtask is execution state", args: `{"action":"focus_subtask","plan_id":"plan_1","checkpoint_id":"cp-1","subtask_id":"task-1"}`, want: ""},
		{name: "complete subtask is execution state", args: `{"action":"complete_subtask","plan_id":"plan_1","checkpoint_id":"cp-1","subtask_id":"task-1"}`, want: ""},
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

func TestAuthorizeToolCallNoActivePlanUsesAtomicSessionCheckpointPath(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-no-active-plan.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-no-active-plan", Mode: sessionruntime.ModeAuto, UserID: "user-lifecycle", AccountScopeID: "account-lifecycle", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Preference: &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessions)
	svc.SetFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) {
		return sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, nil
	})

	auth, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: session.ID, AccountScopeID: "account-lifecycle", RunID: "run-no-plan", CallID: "call-no-plan",
		ToolName: "plan_manage", ToolArguments: `{"action":"request_followup_checkpoint","change_request":"wait ten seconds"}`, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize no-plan checkpoint request: %v", err)
	}
	if auth.Decision != AuthorizationApprove || auth.Record != nil {
		t.Fatalf("no-plan checkpoint authorization = %+v, want direct approval", auth)
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

func TestPlanAcceptanceAlwaysAllowPreservesCanonicalPendingArguments(t *testing.T) {
	svc, sessionID, _, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	policy, err := svc.CurrentPolicyForAccount("account-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	policy.PlanAcceptance.Mode = CapabilityModeAlwaysAllow
	if _, err := svc.UpdateCapabilityPoliciesForAccount("account-lifecycle", policy.SessionDeploy, policy.PlanAcceptance); err != nil {
		t.Fatal(err)
	}
	args := `{"action":"request_new_plan","title":"Canonical","document":{"title":"Canonical","info":{"goal":"ship"},"checkpoints":[{"id":"cp-1","title":"One","status":"pending","tasks":["ship"],"acceptance_criteria":["done"]}]}}`
	auth, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run", CallID: "call", ToolName: "plan_manage", ToolArguments: args, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Decision != AuthorizationApprove || auth.Record != nil || auth.Source != "plan_acceptance_policy" {
		t.Fatalf("authorization = %+v", auth)
	}
}

func TestAuthorizeToolCallBypassPreservesHardDeniesAndPlanAcceptance(t *testing.T) {
	svc, sessionID, _, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	svc.SetBypassPermissions(true)

	dangerous, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-bash", CallID: "call-bash",
		ToolName: "bash", ToolArguments: bashEffectArguments(t, "rm -rf /", "delete", true), Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize dangerous bash: %v", err)
	}
	if dangerous.Decision != AuthorizationDeny || dangerous.Source != "builtin" {
		t.Fatalf("dangerous bash authorization = %+v, want builtin deny", dangerous)
	}

	planArgs := `{"action":"request_new_plan","title":"Plan","document":{"title":"Plan","info":{"goal":"ship"},"checkpoints":[{"id":"cp-1","title":"One","status":"pending","tasks":["ship"],"acceptance_criteria":["done"]}]}}`
	plan, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID: sessionID, AccountScopeID: "account-lifecycle", RunID: "run-plan", CallID: "call-plan",
		ToolName: "plan_manage", ToolArguments: planArgs, Mode: sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize plan acceptance: %v", err)
	}
	if plan.Decision != AuthorizationPending || plan.Record == nil || plan.Source != "plan_acceptance_policy" {
		t.Fatalf("plan acceptance authorization = %+v, want pending dedicated approval", plan)
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

func TestReservedPlanSidechatEditDoesNotCreateGenericPermission(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "reserved-plan-sidechat.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "plan-sidechat", Mode: sessionruntime.ModeAuto, AccountScopeID: "account", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "medium"}, Metadata: map[string]any{"system_sidechat_kind": "plan", "lineage_kind": "system_sidechat"}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessions)
	result, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: session.ID, AccountScopeID: "account", RunID: "run", CallID: "call", ToolName: "edit_pending_plan", ToolArguments: `{}`, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != AuthorizationApprove || result.Source != "system_sidechat" || result.Record != nil {
		t.Fatalf("reserved plan edit should execute without generic permission: %+v", result)
	}
}

func TestCreatePendingPlanProposalAddsCanonicalSidechatMetadata(t *testing.T) {
	svc, sessionID, _, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	payload := `{"action":"request_new_plan","title":"Generated","document":{"title":"Generated","checkpoints":[{"id":"cp-1","title":"One","status":"pending"}]},"approved_arguments":{"action":"request_new_plan","title":"Generated","document":{"title":"Generated","checkpoints":[{"id":"cp-1","title":"One","status":"pending"}]}}}`
	record, err := svc.CreatePending(CreateInput{SessionID: sessionID, RunID: "run-new-plan", CallID: "call-new-plan", ToolName: "plan_manage", ToolArguments: payload, Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("create pending proposal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(record.ToolArguments), &got); err != nil {
		t.Fatalf("decode pending proposal: %v", err)
	}
	planID, _ := got["plan_id"].(string)
	if planID == "" || got["proposal_revision"] != float64(1) || got["document"] == nil {
		t.Fatalf("pending proposal metadata = %#v", got)
	}
	document, _ := got["document"].(map[string]any)
	approved, _ := got["approved_arguments"].(map[string]any)
	approvedDocument, _ := approved["document"].(map[string]any)
	if document["id"] != planID || approvedDocument["id"] != planID {
		t.Fatalf("canonical plan binding missing: plan_id=%q document=%#v approved=%#v", planID, document, approved)
	}
	if _, hasReplacementTarget := approved["plan_id"]; hasReplacementTarget {
		t.Fatalf("new-plan approval must not treat the permission-derived proposal id as an existing replacement target: %#v", approved)
	}

	resolved, err := svc.Resolve(sessionID, record.ID, ActionAllowOnce, "approved")
	if err != nil {
		t.Fatalf("approve new-plan proposal: %v", err)
	}
	var resolvedArgs map[string]any
	if err := json.Unmarshal([]byte(resolved.ApprovedArguments), &resolvedArgs); err != nil {
		t.Fatalf("decode resolved arguments: %v", err)
	}
	if _, hasReplacementTarget := resolvedArgs["plan_id"]; hasReplacementTarget {
		t.Fatalf("resolved new-plan arguments injected a nonexistent replacement target: %#v", resolvedArgs)
	}
}

func TestPendingPlanProposalEditIsRevisionedAndApprovalUsesBackendDocument(t *testing.T) {
	svc, sessionID, _, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	payload := `{"path_id":"permission.exit-plan-mode.v1","tool":"exit_plan_mode","plan_id":"plan-new","title":"Original","document":{"id":"plan-new","title":"Original","checkpoints":[{"id":"cp-1","title":"One","status":"pending"}]},"approved_arguments":{"plan_id":"plan-new","title":"Original","document":{"id":"plan-new","title":"Original","checkpoints":[{"id":"cp-1","title":"One","status":"pending"}]}}}`
	record, err := svc.CreatePending(CreateInput{SessionID: sessionID, RunID: "run-plan", CallID: "call-plan", ToolName: "exit_plan_mode", ToolArguments: payload, Mode: sessionruntime.ModePlan})
	if err != nil {
		t.Fatalf("create pending proposal: %v", err)
	}
	if record.ProposalRevision != 1 {
		t.Fatalf("initial revision = %d, want 1", record.ProposalRevision)
	}
	var pendingPayload map[string]any
	if err := json.Unmarshal([]byte(record.ToolArguments), &pendingPayload); err != nil {
		t.Fatalf("decode pending proposal payload: %v", err)
	}
	if got := pendingPayload["proposal_revision"]; got != float64(1) {
		t.Fatalf("payload proposal_revision = %#v, want 1", got)
	}
	if pendingPayload["plan_id"] != "plan-new" || pendingPayload["document"] == nil {
		t.Fatalf("pending proposal metadata = %#v", pendingPayload)
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waited := make(chan pebblestore.PermissionRecord, 1)
	go func() {
		resolved, _ := svc.WaitForResolution(waitCtx, sessionID, record.ID)
		waited <- resolved
	}()

	published := make(chan pebblestore.PermissionRecord, 1)
	svc.SetPermissionRealtimePublisher(func(gotSessionID string, got pebblestore.PermissionRecord) error {
		if gotSessionID != sessionID {
			t.Fatalf("published session = %q, want %q", gotSessionID, sessionID)
		}
		published <- got
		return nil
	})
	editedDoc := &pebblestore.SessionPlanDocument{ID: "plan-new", Title: "Edited", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Newest", Status: sessionruntime.PlanCheckpointStatusPending}}}
	edited, err := svc.EditPendingPlanProposal(PendingPlanProposalEditInput{SessionID: sessionID, PermissionID: record.ID, ExpectedRevision: 1, Document: editedDoc})
	if err != nil {
		t.Fatalf("edit pending proposal: %v", err)
	}
	if edited.ProposalRevision != 2 || edited.Record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("edited result = %+v", edited)
	}
	select {
	case got := <-published:
		if got.ID != record.ID || got.ProposalRevision != 2 {
			t.Fatalf("published permission = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("proposal edit was not published to parent realtime")
	}
	select {
	case got := <-waited:
		t.Fatalf("proposal edit resolved waiter unexpectedly: %+v", got)
	default:
	}
	if _, err := svc.EditPendingPlanProposal(PendingPlanProposalEditInput{SessionID: sessionID, PermissionID: record.ID, ExpectedRevision: 1, Document: editedDoc}); err == nil {
		t.Fatal("stale proposal edit unexpectedly succeeded")
	}
	staleClient := `{"title":"STALE","document":{"id":"plan-new","title":"STALE","checkpoints":[{"id":"cp-stale","title":"Stale","status":"pending"}]},"continuation_policy":"review_each_checkpoint"}`
	resolved, err := svc.ResolveWithArguments(sessionID, record.ID, ActionAllowOnce, "approved", staleClient)
	if err != nil {
		t.Fatalf("approve edited proposal: %v", err)
	}
	var approved map[string]any
	if err := json.Unmarshal([]byte(resolved.ApprovedArguments), &approved); err != nil {
		t.Fatalf("decode approved arguments: %v", err)
	}
	if approved["title"] != "Edited" || approved["continuation_policy"] != "review_each_checkpoint" {
		t.Fatalf("approved arguments = %#v", approved)
	}
	doc, _ := approved["document"].(map[string]any)
	if doc["title"] != "Edited" {
		t.Fatalf("approved document = %#v", doc)
	}
	select {
	case got := <-waited:
		if got.Status != pebblestore.PermissionStatusApproved {
			t.Fatalf("waiter status = %q", got.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not resolve waiter")
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
