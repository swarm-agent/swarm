package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestExecutePlanManageStartSessionCheckpointCreatesRunRequest(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"start_session_checkpoint","change_request":"fix my sidebar and make active item visible","checkpoint_title":"Fix sidebar visibility","tasks":["Inspect sidebar","Keep active item visible"],"acceptance_criteria":["Active item stays visible"],"notes":"Relevant files: web/src","run_id":"run-session-checkpoint-1","run_session_id":"child-session","parent_session_id":"parent-session","started_at":1234}`, "")
	if err != nil {
		t.Fatalf("start session checkpoint: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		RunRequest   struct {
			Context struct {
				PlanID       string `json:"plan_id"`
				CheckpointID string `json:"checkpoint_id"`
				AttemptID    string `json:"attempt_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
		Plan struct {
			ID       string                           `json:"id"`
			Active   bool                             `json:"active"`
			Status   string                           `json:"status"`
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Action != "start_session_checkpoint" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-1" {
		t.Fatalf("action/next/checkpoint = %q/%q/%q", payload.Action, payload.NextAction, payload.CheckpointID)
	}
	if payload.Plan.ID == "" || !payload.Plan.Active || payload.Plan.Status != "approved" || payload.Plan.Document == nil {
		t.Fatalf("plan = %#v", payload.Plan)
	}
	if payload.RunRequest.Context.PlanID != payload.Plan.ID || payload.RunRequest.Context.CheckpointID != "cp-1" || payload.RunRequest.Context.AttemptID != "cp-1:attempt-1" {
		t.Fatalf("run request = %#v", payload.RunRequest.Context)
	}
	checkpoint := payload.Plan.Document.Checkpoints[0]
	if checkpoint.Status != sessionruntime.PlanCheckpointStatusInProgress || checkpoint.Objective != "fix my sidebar and make active item visible" || checkpoint.RunID != "run-session-checkpoint-1" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if !strings.Contains(checkpoint.Notes, "Relevant files: web/src") {
		t.Fatalf("checkpoint notes = %q", checkpoint.Notes)
	}
}

func TestProviderManagedAutoStartSessionCheckpointContinuesCurrentRun(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID: sessionID, PermissionSessionID: sessionID, RunID: "run-inline", Step: 7,
		SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true,
		ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			if input.UserID == "" {
				input.UserID = principal.UserID
			}
			if input.AccountScopeID == "" {
				input.AccountScopeID = principal.AccountScopeID
			}
			return sessionSvc.ApplySessionMutation(input)
		},
	})
	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-inline", Name: "plan_manage", Arguments: `{"action":"start_session_checkpoint","change_request":"fix the sidebar","checkpoint_title":"Fix sidebar"}`})
	if err != nil {
		t.Fatalf("execute inline start: %v", err)
	}
	if result.RestartTurn {
		t.Fatalf("inline auto checkpoint restarted turn: %#v", result)
	}
	var payload struct {
		NextAction       string `json:"next_action"`
		ContextPreserved bool   `json:"context_preserved"`
		RunOwnership     struct {
			RunID        string `json:"run_id"`
			CheckpointID string `json:"checkpoint_id"`
		} `json:"run_ownership"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload.NextAction != "continue_current_run" || !payload.ContextPreserved || payload.RunOwnership.RunID != "run-inline" || payload.RunOwnership.CheckpointID != "cp-1" {
		t.Fatalf("inline payload = %#v output=%s", payload, result.Output)
	}
	plan, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("active plan: ok=%v err=%v", ok, err)
	}
	if plan.Document == nil || plan.Document.ExecutionState == nil || plan.Document.ExecutionState.CurrentRunID != "run-inline" || plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress {
		t.Fatalf("inline plan state = %#v", plan.Document)
	}
}

func TestProviderManagedAutoCheckpointCanMarkBlocked(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID: sessionID, PermissionSessionID: sessionID, RunID: "run-blocked-inline", Step: 1,
		SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true,
		ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			if input.UserID == "" {
				input.UserID = principal.UserID
			}
			if input.AccountScopeID == "" {
				input.AccountScopeID = principal.AccountScopeID
			}
			return sessionSvc.ApplySessionMutation(input)
		},
	})
	if result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-start", Name: "plan_manage", Arguments: `{"action":"start_session_checkpoint","change_request":"demonstrate blocked","checkpoint_title":"Blocked demo"}`}); err != nil || strings.TrimSpace(result.Error) != "" {
		t.Fatalf("start inline checkpoint: result=%#v err=%v", result, err)
	}
	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-block", Name: "plan_manage", Arguments: `{"action":"mark_blocked","checkpoint_id":"cp-1","report":"dependency missing","result":"blocked"}`})
	if err != nil || strings.TrimSpace(result.Error) != "" {
		t.Fatalf("mark inline checkpoint blocked: result=%#v err=%v", result, err)
	}
	var payload struct {
		Action     string `json:"action"`
		NextAction string `json:"next_action"`
		Plan       struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode blocked output: %v", err)
	}
	if payload.Action != "mark_blocked" || payload.NextAction != "stopped" || payload.Plan.Document == nil || payload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusBlocked {
		t.Fatalf("blocked payload = %#v output=%s", payload, result.Output)
	}
}

func TestProviderManagedAutoFollowupWithoutPlanNormalizesToAtomicStart(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID: sessionID, PermissionSessionID: sessionID, RunID: "run-normalized", Step: 3,
		SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true,
		ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			if input.UserID == "" {
				input.UserID = principal.UserID
			}
			if input.AccountScopeID == "" {
				input.AccountScopeID = principal.AccountScopeID
			}
			return sessionSvc.ApplySessionMutation(input)
		},
	})
	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-normalized", Name: "plan_manage", Arguments: `{"action":"request_followup_checkpoint","change_request":"fix the timer","checkpoint_title":"Fix timer","tasks":["Repair timer"],"acceptance_criteria":["Timer refreshes"]}`})
	if err != nil {
		t.Fatalf("normalize no-plan follow-up: %v", err)
	}
	if result.Error != "" || result.RestartTurn {
		t.Fatalf("normalized result = %#v", result)
	}
	var payload struct {
		Action           string `json:"action"`
		NextAction       string `json:"next_action"`
		ContextPreserved bool   `json:"context_preserved"`
		CheckpointID     string `json:"checkpoint_id"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode normalized output: %v", err)
	}
	if payload.Action != "start_session_checkpoint" || payload.NextAction != "continue_current_run" || !payload.ContextPreserved || payload.CheckpointID != "cp-1" {
		t.Fatalf("normalized payload = %#v output=%s", payload, result.Output)
	}
	plan, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || plan.Document == nil {
		t.Fatalf("active plan after normalization: ok=%v err=%v plan=%#v", ok, err, plan)
	}
	if plan.Document.ExecutionState == nil || plan.Document.ExecutionState.CurrentRunID != "run-normalized" || plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress {
		t.Fatalf("normalized plan was not atomically started: %#v", plan.Document)
	}
}

func TestExecutePlanManageStartSessionCheckpointRejectsActivePlan(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-existing", "Existing", "# Existing", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusPending}}}})
	if err != nil {
		t.Fatalf("save active plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"start_session_checkpoint","change_request":"fix another thing"}`, "")
	if err == nil || !strings.Contains(err.Error(), "requires no active plan") {
		t.Fatalf("error = %v raw=%s, want active-plan refusal", err, raw)
	}
}

func TestExecutePlanManageCheckpointOutcomeUpdatesExecutionMetadata(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"checkpoint_outcome","checkpoint_id":"cp-1","outcome":"completed","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","report":"model complete","changed_files":["swarmd/internal/session/plan_execution.go"],"validation":["targeted test"]}`, "")
	if err != nil {
		t.Fatalf("checkpoint outcome: %v output=%s", err, raw)
	}
	var payload struct {
		Action string `json:"action"`
		Plan   struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Action != "checkpoint_outcome" {
		t.Fatalf("action = %q", payload.Action)
	}
	doc := payload.Plan.Document
	if doc == nil {
		t.Fatal("missing structured document in payload")
	}
	if doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("active checkpoint = %q, want cp-2", doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted || doc.Checkpoints[0].AttemptID != "attempt-1" || len(doc.Checkpoints[0].Attempts) != 1 {
		t.Fatalf("checkpoint metadata = %#v", doc.Checkpoints[0])
	}
	if doc.ExecutionState == nil || doc.ExecutionState.LastCheckpointID != "cp-1" || doc.ExecutionState.CurrentRunID != "run-1" || doc.ExecutionState.ParentSessionID != "parent-session" {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
	}
}

func TestExecutePlanManageUpdateExecutionPolicy(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusPending}},
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"update_execution_policy","execution_policy":{"mode":"automatic","shape":"checkpointed"}}`, "")
	if err != nil {
		t.Fatalf("update execution policy: %v output=%s", err, raw)
	}
	var payload struct {
		Plan struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Plan.Document == nil || payload.Plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || payload.Plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed {
		t.Fatalf("execution policy = %#v", payload.Plan.Document)
	}
}

func TestExecutePlanManageStartCheckpointDoesNotEmitPlanLifecycleSystemMessage(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-lifecycle", "Lifecycle Plan", "# Lifecycle", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save lifecycle plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","started_at":1234}`, "")
	if err != nil {
		t.Fatalf("start checkpoint: %v output=%s", err, raw)
	}
	messages, err := sessionSvc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("start checkpoint should not emit lifecycle message, messages = %#v", messages)
	}
}

func TestExecutePlanManageLifecycleSystemMessagesForControlAndOutcomeActions(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-lifecycle-actions", "Plan: Lifecycle Actions", "# Lifecycle", "draft", "pending", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusPending, Subtasks: []pebblestore.SessionPlanSubtask{{ID: "task-1", Title: "Implement", Status: sessionruntime.PlanSubtaskStatusPending}, {ID: "task-2", Title: "Document", Status: sessionruntime.PlanSubtaskStatusPending}}},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save lifecycle actions plan: %v", err)
	}

	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		appliedMutations = append(appliedMutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}

	assertLifecycleMessage := func(index int, action, nextAction string, expectedMutationOffset int, contains ...string) {
		t.Helper()
		messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 20)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) < index+1 {
			t.Fatalf("message count = %d, want at least %d: %#v", len(messages), index+1, messages)
		}
		message := messages[index]
		if message.Role != "system" || message.Metadata["source"] != PlanExecutionLifecycleMessageSource || message.Metadata["kind"] != "plan_execution_break" {
			t.Fatalf("message[%d] role/metadata = role %q metadata %#v", index, message.Role, message.Metadata)
		}
		if message.Metadata["action"] != action || message.Metadata["next_action"] != nextAction {
			t.Fatalf("message[%d] action metadata = %#v", index, message.Metadata)
		}
		for _, want := range contains {
			if !strings.Contains(message.Content, want) {
				t.Fatalf("message[%d] content missing %q: %q", index, want, message.Content)
			}
		}
		for _, forbidden := range []string{"Plan: Plan:", "plan-lifecycle-actions", "Policy: automatic / checkpointed"} {
			if strings.Contains(message.Content, forbidden) {
				t.Fatalf("message[%d] content includes internal/duplicated text %q: %q", index, forbidden, message.Content)
			}
		}
		if len(appliedMutations) <= expectedMutationOffset {
			t.Fatalf("applied mutations missing expected lifecycle mutation offset %d: %#v", expectedMutationOffset, appliedMutations)
		}
		messageMutation := appliedMutations[expectedMutationOffset]
		if messageMutation.Kind != sessionruntime.SessionMutationAppendMessage || messageMutation.EventType != "session.message.appended" || messageMutation.Message == nil || messageMutation.Message.Metadata["source"] != PlanExecutionLifecycleMessageSource || messageMutation.Message.Metadata["action"] != action {
			t.Fatalf("message[%d] expected mutation metadata = %#v", index, messageMutation)
		}
	}

	raw, err := runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"approve_and_start","execution_granularity":"checkpointed","continue_automatically":true}`, "", applyMutation)
	if err != nil {
		t.Fatalf("approve and start: %v output=%s", err, raw)
	}
	if len(appliedMutations) != 1 {
		t.Fatalf("approve and start should only add plan saved mutation, count = %d: %#v", len(appliedMutations), appliedMutations)
	}

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","started_at":1234}`, "", applyMutation)
	if err != nil {
		t.Fatalf("start checkpoint: %v output=%s", err, raw)
	}
	if len(appliedMutations) != 2 {
		t.Fatalf("start checkpoint should only add plan saved mutation, count = %d: %#v", len(appliedMutations), appliedMutations)
	}

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-1","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","report":"## Outcome\n- backend handoff ready","result":"checkpoint complete","changed_files":["swarmd/internal/run/plan_lifecycle_message.go"],"validation":["focused contract review"]}`, "", applyMutation)
	if err != nil {
		t.Fatalf("complete checkpoint: %v output=%s", err, raw)
	}
	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || active.Document == nil {
		t.Fatalf("get plan after atomic checkpoint completion: ok=%v err=%v plan=%#v", ok, err, active)
	}
	completedCheckpoint := active.Document.Checkpoints[0]
	if completedCheckpoint.ActiveSubtaskID != "" || len(completedCheckpoint.Subtasks) != 2 || completedCheckpoint.Subtasks[0].Status != sessionruntime.PlanSubtaskStatusCompleted || completedCheckpoint.Subtasks[1].Status != sessionruntime.PlanSubtaskStatusCompleted || active.Document.ActiveCheckpointID != "cp-2" {
		t.Fatalf("single complete_checkpoint did not atomically complete subtasks and advance: %#v active=%q", completedCheckpoint, active.Document.ActiveCheckpointID)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append complete lifecycle: %v", err)
	}
	firstCompletionRaw := raw
	messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list checkpoint handoff messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "system" || messages[0].Metadata["source"] != PlanExecutionCheckpointHandoffMessageSource || messages[0].Metadata["kind"] != "plan_checkpoint_handoff" {
		t.Fatalf("automatic completion handoff metadata/order = %#v", messages)
	}
	for key, want := range map[string]any{"action": "complete_checkpoint", "checkpoint_id": "cp-1", "next_checkpoint_id": "cp-2", "next_action": "run_checkpoint_with_current_context", "fresh_context": false, "context_preserved": true, "outcome": sessionruntime.PlanCheckpointStatusCompleted, "attempt_id": "attempt-1", "run_id": "run-1", "run_session_id": "child-session", "parent_session_id": "parent-session"} {
		if messages[0].Metadata[key] != want {
			t.Fatalf("automatic completion handoff metadata[%q] = %#v, want %#v: %#v", key, messages[0].Metadata[key], want, messages[0].Metadata)
		}
	}
	for _, want := range []string{"Checkpoint handoff", "Completed: Checkpoint 1 — Model", "Next: Checkpoint 2 — API", "Context: Continuing with the same execution-epoch conversation.", "Report:\n## Outcome\n- backend handoff ready", "Result: checkpoint complete", "Changed files:\n- swarmd/internal/run/plan_lifecycle_message.go", "Validation:\n- focused contract review"} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("automatic completion handoff missing %q: %q", want, messages[0].Content)
		}
	}
	if len(appliedMutations) <= 3 || appliedMutations[3].Kind != sessionruntime.SessionMutationAppendMessage || appliedMutations[3].Message == nil || appliedMutations[3].Message.Metadata["source"] != PlanExecutionCheckpointHandoffMessageSource {
		t.Fatalf("automatic completion handoff mutation ordering = %#v", appliedMutations)
	}

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-2","run_id":"run-2","run_session_id":"child-session-2","parent_session_id":"parent-session","started_at":2345}`, "", applyMutation)
	if err != nil {
		t.Fatalf("start second checkpoint: %v output=%s", err, raw)
	}
	if len(appliedMutations) != 5 {
		t.Fatalf("start second checkpoint should only add plan saved mutation, count = %d: %#v", len(appliedMutations), appliedMutations)
	}

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-2","report":"## Summary\n- done","result":"finished","validation":["lifecycle handoff regression","second validation"]}`, "", applyMutation)
	if err != nil {
		t.Fatalf("complete final checkpoint: %v output=%s", err, raw)
	}
	predecessorEpoch, ok, err := sessionSvc.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok {
		t.Fatalf("get predecessor epoch before final handoff: ok=%v err=%v", ok, err)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append final lifecycle: %v", err)
	}
	assertLifecycleMessage(1, "complete_checkpoint", "await_review", 6, "All checkpoints complete; review required — Automatic mode", "Completed: Checkpoint 2 — API", "Next: all checkpoints are complete; waiting for user review.")
	messages, err = sessionSvc.ListSessionMessages(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list messages after final handoff: %v", err)
	}
	if len(messages) != 3 || messages[2].Role != "system" || messages[2].Metadata["source"] != PlanExecutionFinalHandoffMessageSource || messages[2].Metadata["kind"] != "plan_final_checkpoint_handoff" || messages[2].Metadata["action"] != "complete_checkpoint" || messages[2].Metadata["next_action"] != "await_review" {
		t.Fatalf("final handoff message metadata/order = %#v", messages)
	}
	for _, want := range []string{"Final checkpoint handoff", "Report:\n## Summary\n- done", "\n\nResult: finished", "\n\nValidation:\n- lifecycle handoff regression\n- second validation"} {
		if !strings.Contains(messages[2].Content, want) {
			t.Fatalf("final handoff content missing %q: %q", want, messages[2].Content)
		}
	}
	if strings.Contains(messages[2].Content, "Validation: Read-only git inspection") || strings.Contains(messages[2].Content, "; Revert operation:") {
		t.Fatalf("final handoff validation should render as a markdown list, not semicolon-joined prose: %q", messages[2].Content)
	}
	if strings.Contains(messages[2].Content, "Markdown is supported in this handoff") {
		t.Fatalf("final handoff content leaked markdown-support note: %q", messages[2].Content)
	}
	if strings.Contains(messages[1].Content, "Final checkpoint handoff") || strings.Contains(messages[1].Content, "Report:") || strings.Contains(messages[1].Content, "Result: finished") || strings.Contains(messages[1].Content, "Validation:") {
		t.Fatalf("final lifecycle message leaked handoff details: %q", messages[1].Content)
	}
	successorEpoch, ok, err := sessionSvc.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok || successorEpoch.ParentEpochID != predecessorEpoch.EpochID || successorEpoch.Ordinal != predecessorEpoch.Ordinal+1 {
		t.Fatalf("final handoff successor epoch: ok=%v err=%v predecessor=%#v successor=%#v", ok, err, predecessorEpoch, successorEpoch)
	}
	sealedPredecessor, ok, err := sessionSvc.GetExecutionEpoch(sessionID, predecessorEpoch.EpochID)
	if err != nil || !ok || sealedPredecessor.Status != pebblestore.ExecutionEpochStatusSealed || sealedPredecessor.LastRootSeq != messages[2].GlobalSeq {
		t.Fatalf("final handoff predecessor boundary: ok=%v err=%v predecessor=%#v handoff=%#v", ok, err, sealedPredecessor, messages[2])
	}

	followupInput := PlanExecutionLifecycleMessageInput{
		Action: "complete_checkpoint",
		Plan: pebblestore.SessionPlanSnapshot{ID: "plan-followup-final", Title: "Follow-up final", Document: &pebblestore.SessionPlanDocument{
			ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{
				{ID: "cp-1", Title: "Original", Status: sessionruntime.PlanCheckpointStatusCompleted},
				{ID: "followup-2.5", Title: "Run rebuilt matrix", Status: sessionruntime.PlanCheckpointStatusCompleted},
				{ID: "cp-3", Title: "Deferred", Status: sessionruntime.PlanCheckpointStatusPending},
			},
			ActiveCheckpointID: "followup-2.5",
			ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "followup-2.5", LastOutcome: sessionruntime.PlanCheckpointStatusCompleted},
		}},
		Payload: map[string]any{
			"next_action":   "await_review",
			"report":        "## Matrix\n- live actions passed",
			"result":        "all acceptance criteria met",
			"changed_files": []any{"swarmd/internal/run/plan_lifecycle_message.go"},
			"validation":    []any{"durable matrix passed"},
		},
	}
	followupLifecycle, ok := BuildPlanExecutionLifecycleSystemMessage(followupInput)
	if !ok || !strings.Contains(followupLifecycle.Content, "Follow-up checkpoint complete; review required — Automatic mode") || !strings.Contains(followupLifecycle.Content, "Next: waiting for checkpoint review.") || strings.Contains(followupLifecycle.Content, "Report:") {
		t.Fatalf("follow-up final lifecycle message = %#v", followupLifecycle)
	}
	followupCheckpointHandoff, ok := BuildPlanExecutionCheckpointHandoffSystemMessage(followupInput)
	if ok {
		t.Fatalf("follow-up final-review transition must not also emit a generic checkpoint handoff: %#v", followupCheckpointHandoff)
	}
	followupHandoff, ok := BuildFinalPlanExecutionHandoffSystemMessage(followupInput)
	if !ok || followupHandoff.Metadata["source"] != PlanExecutionFinalHandoffMessageSource || followupHandoff.Metadata["kind"] != "plan_final_checkpoint_handoff" || followupHandoff.Metadata["checkpoint_id"] != "followup-2.5" {
		t.Fatalf("follow-up final handoff metadata = %#v", followupHandoff)
	}
	for _, want := range []string{"Final checkpoint handoff", "follow-up checkpoint is complete and waiting for review", "Report:\n## Matrix\n- live actions passed", "Result: all acceptance criteria met", "Changed files:\n- swarmd/internal/run/plan_lifecycle_message.go", "Validation:\n- durable matrix passed"} {
		if !strings.Contains(followupHandoff.Content, want) {
			t.Fatalf("follow-up final handoff missing %q: %q", want, followupHandoff.Content)
		}
	}

	_, _, err = sessionSvc.SavePlanWithMetadata(sessionID, "plan-blocked-lifecycle", "Plan: Blocked Lifecycle", "# Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusBlocked, AttemptID: "attempt-blocked", RunID: "run-blocked"},
			{ID: "cp-b", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-a",
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-a", LastAttemptID: "attempt-blocked", LastOutcome: sessionruntime.PlanCheckpointStatusBlocked},
	}})
	if err != nil {
		t.Fatalf("save blocked lifecycle plan: %v", err)
	}
	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"resolve_blocked_checkpoint","checkpoint_id":"cp-a","start_next":true,"attempt_id":"attempt-b","run_id":"run-b","run_session_id":"child-session-b","parent_session_id":"parent-session","reviewed_at":3456}`, "", applyMutation)
	if err != nil {
		t.Fatalf("resolve blocked checkpoint: %v output=%s", err, raw)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append resolve blocked lifecycle: %v", err)
	}
	assertLifecycleMessage(3, "resolve_blocked_checkpoint", "run_checkpoint_with_current_context", 9, "Blocker resolved; resuming current checkpoint — Automatic mode", "Checkpoint: Checkpoint a — Blocked", "Context: Resuming this checkpoint with fresh recovery context; it remains incomplete until the resumed agent records a normal outcome.")

	_, _, err = sessionSvc.SavePlanWithMetadata(sessionID, "plan-provider-blocked-lifecycle", "Plan: Provider Blocked Lifecycle", "# Provider Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-provider-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusBlocked, AttemptID: "attempt-provider-blocked", RunID: "run-provider-blocked", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-provider-blocked", CheckpointID: "cp-provider-a", Status: sessionruntime.PlanCheckpointStatusBlocked, Outcome: sessionruntime.PlanCheckpointStatusBlocked, RunID: "run-provider-blocked"}}},
			{ID: "cp-provider-b", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-provider-a",
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-provider-a", LastAttemptID: "attempt-provider-blocked", LastOutcome: sessionruntime.PlanCheckpointStatusBlocked},
	}})
	if err != nil {
		t.Fatalf("save provider blocked lifecycle plan: %v", err)
	}
	providerRaw, err := runSvc.executePlanLifecycleControlAction(sessionID, "resolve_blocked_checkpoint", map[string]any{
		"checkpoint_id": "cp-provider-a",
		"start_next":    true,
		"reviewed_at":   4567,
	}, applyMutation, planLifecycleRunContext{Inline: true, RunID: "user-continuation-run", RunSessionID: sessionID, ParentSessionID: sessionID})
	if err != nil {
		t.Fatalf("provider-managed resolve blocked checkpoint: %v output=%s", err, providerRaw)
	}
	var providerPayload struct {
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(providerRaw), &providerPayload); err != nil {
		t.Fatalf("decode provider resolve payload: %v", err)
	}
	if providerPayload.NextAction != "run_checkpoint_with_current_context" || providerPayload.CheckpointID != "cp-provider-a" || providerPayload.Plan.Document == nil {
		t.Fatalf("provider resolve payload = %#v raw=%s", providerPayload, providerRaw)
	}
	if providerPayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress || providerPayload.Plan.Document.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || providerPayload.Plan.Document.ActiveCheckpointID != "cp-provider-a" {
		t.Fatalf("provider resolve must resume the blocked checkpoint and leave the later checkpoint pending: %#v", providerPayload.Plan.Document)
	}

	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: firstCompletionRaw}, applyMutation); err != nil {
		t.Fatalf("reappend automatic completion handoff: %v", err)
	}
	messages, err = sessionSvc.ListSessionMessages(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list messages after idempotent handoff replay: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("idempotent handoff replay appended a duplicate message: %#v", messages)
	}

	outbox, err := sessionSvc.ListRealtimeOutboxForSessionAfterSeq(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list realtime outbox: %v", err)
	}
	if len(outbox) != 10 {
		t.Fatalf("realtime outbox count = %d, want 10: %#v", len(outbox), outbox)
	}
	wantEvents := []string{
		"session.plan.saved",
		"session.plan.saved",
		"session.plan.saved", "session.message.appended",
		"session.plan.saved",
		"session.plan.saved", "session.message.appended", "session.message.appended",
		"session.plan.saved", "session.message.appended",
	}
	for i, want := range wantEvents {
		if outbox[i].Event.EventType != want {
			t.Fatalf("realtime outbox[%d] = %q, want %q", i, outbox[i].Event.EventType, want)
		}
	}
}

func TestExecutePlanManageManualReviewCompletionEmitsLifecycleThenSeparateHandoff(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-manual-review", "Plan: Manual Review", "# Manual Review", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "attempt-1", CurrentRunID: "run-1", CurrentSessionID: "child-session", ParentSessionID: "parent-session"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Implementation", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "attempt-1", RunID: "run-1", SessionID: "child-session"},
			{ID: "cp-2", Title: "Verification", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save manual review plan: %v", err)
	}

	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		appliedMutations = append(appliedMutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}

	raw, err := runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-1","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","report":"## Outcome\n- implementation ready","result":"checkpoint complete","changed_files":["swarmd/internal/run/plan_lifecycle_message.go"],"validation":["not run; not requested"]}`, "", applyMutation)
	if err != nil {
		t.Fatalf("complete manual review checkpoint: %v output=%s", err, raw)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append manual review lifecycle and handoff: %v", err)
	}

	messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list manual review messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("manual review completion messages = %#v", messages)
	}
	lifecycle := messages[0]
	if lifecycle.Metadata["source"] != PlanExecutionLifecycleMessageSource || lifecycle.Metadata["kind"] != "plan_execution_break" || lifecycle.Metadata["next_action"] != "await_review" {
		t.Fatalf("manual review lifecycle metadata = %#v", lifecycle.Metadata)
	}
	for _, want := range []string{"Checkpoint complete — Manual review mode", "Completed: Checkpoint 1 — Implementation", "Next: waiting for checkpoint review."} {
		if !strings.Contains(lifecycle.Content, want) {
			t.Fatalf("manual review lifecycle missing %q: %q", want, lifecycle.Content)
		}
	}
	for _, forbidden := range []string{"implementation ready", "Result:", "Changed files:", "Validation:"} {
		if strings.Contains(lifecycle.Content, forbidden) {
			t.Fatalf("manual review lifecycle leaked handoff detail %q: %q", forbidden, lifecycle.Content)
		}
	}
	handoff := messages[1]
	for key, want := range map[string]any{"source": PlanExecutionCheckpointHandoffMessageSource, "kind": "plan_checkpoint_handoff", "action": "complete_checkpoint", "checkpoint_id": "cp-1", "next_checkpoint_id": "cp-2", "next_action": "await_review", "fresh_context": false, "review_required": true, "outcome": sessionruntime.PlanCheckpointStatusCompleted, "attempt_id": "attempt-1", "run_id": "run-1", "run_session_id": "child-session", "parent_session_id": "parent-session"} {
		if handoff.Metadata[key] != want {
			t.Fatalf("manual review handoff metadata[%q] = %#v, want %#v: %#v", key, handoff.Metadata[key], want, handoff.Metadata)
		}
	}
	for _, want := range []string{"Checkpoint handoff", "Completed: Checkpoint 1 — Implementation", "Review: Review this checkpoint before starting Checkpoint 2 — Verification.", "Report:\n## Outcome\n- implementation ready", "Result: checkpoint complete", "Changed files:\n- swarmd/internal/run/plan_lifecycle_message.go", "Validation:\n- not run; not requested"} {
		if !strings.Contains(handoff.Content, want) {
			t.Fatalf("manual review handoff missing %q: %q", want, handoff.Content)
		}
	}
	if len(appliedMutations) != 3 || appliedMutations[1].Message == nil || appliedMutations[1].Message.Metadata["source"] != PlanExecutionLifecycleMessageSource || appliedMutations[2].Message == nil || appliedMutations[2].Message.Metadata["source"] != PlanExecutionCheckpointHandoffMessageSource {
		t.Fatalf("manual review lifecycle/handoff mutation ordering = %#v", appliedMutations)
	}

	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("reappend manual review lifecycle and handoff: %v", err)
	}
	messages, err = sessionSvc.ListSessionMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list manual review messages after replay: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("idempotent manual review replay appended duplicate messages: %#v", messages)
	}
}

func TestExecutePlanManageRequestFollowupAtomicallyUnblocksAndReturnsFreshRun(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-blocked-followup", "Plan: Blocked Follow-up", "# Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed, FollowupCheckpointPolicy: sessionruntime.PlanFollowupCheckpointPolicyAutoStart},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusBlocked, AttemptID: "attempt-blocked", RunID: "run-blocked", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-blocked", CheckpointID: "cp-a", Status: sessionruntime.PlanCheckpointStatusBlocked, Outcome: sessionruntime.PlanCheckpointStatusBlocked, RunID: "run-blocked"}}},
			{ID: "cp-b", Title: "Later", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-a",
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-a", LastAttemptID: "attempt-blocked", LastOutcome: sessionruntime.PlanCheckpointStatusBlocked},
	}})
	if err != nil {
		t.Fatalf("save blocked follow-up plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_followup_checkpoint","plan_id":"plan-blocked-followup","change_request":"replace the blocked work","checkpoint_title":"Replacement work","run_id":"run-followup","run_session_id":"child-followup","parent_session_id":"parent-session","started_at":3456}`, "")
	if err != nil {
		t.Fatalf("request blocked follow-up: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		RunRequest   struct {
			Context struct {
				CheckpointID string `json:"checkpoint_id"`
				AttemptID    string `json:"attempt_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
		ExecutionSummary sessionruntime.PlanExecutionSummary `json:"execution_summary"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode blocked follow-up payload: %v", err)
	}
	if payload.Action != "request_followup_checkpoint" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "followup-1" || payload.RunRequest.Context.CheckpointID != "followup-1" || payload.RunRequest.Context.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("follow-up next action = %#v raw=%s", payload, raw)
	}
	if payload.ExecutionSummary.Blocked || payload.ExecutionSummary.Failed || payload.ExecutionSummary.NextCheckpointID != "followup-1" {
		t.Fatalf("follow-up execution summary = %#v", payload.ExecutionSummary)
	}
	if payload.Plan.Document == nil || strings.Join(checkpointIDsForRunTest(payload.Plan.Document.Checkpoints), ",") != "cp-a,followup-1,cp-b" {
		t.Fatalf("follow-up document/order = %#v", payload.Plan.Document)
	}
	resolved := payload.Plan.Document.Checkpoints[0]
	inserted := payload.Plan.Document.Checkpoints[1]
	if resolved.Status != sessionruntime.PlanCheckpointStatusCompleted || resolved.Result != "superseded_by_followup" || resolved.Review == nil || resolved.Review.Status != sessionruntime.PlanCheckpointReviewStatusApproved || len(resolved.Attempts) != 1 || resolved.Attempts[0].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("resolved blocked checkpoint = %#v", resolved)
	}
	if inserted.Status != sessionruntime.PlanCheckpointStatusInProgress || payload.Plan.Document.ActiveCheckpointID != inserted.ID {
		t.Fatalf("inserted checkpoint = %#v active=%q", inserted, payload.Plan.Document.ActiveCheckpointID)
	}
}

func TestExecutePlanManageInlineFinalReviewFollowupDefersCheckpointStart(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-final-review-followup", "Plan: Final Review", "# Final Review", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:               pebblestore.SessionPlanInfo{Goal: "Complete the plan and handle an independent final-review follow-up."},
		ExecutionOrigin:    sessionruntime.PlanExecutionOriginAutoSession,
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed, FollowupCheckpointPolicy: sessionruntime.PlanFollowupCheckpointPolicyAutoStart},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Final", Objective: "Complete the original work.", AcceptanceCriteria: []string{"The original work is complete."}, Status: sessionruntime.PlanCheckpointStatusCompleted}},
		ActiveCheckpointID: "cp-1",
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1", LastAttemptID: "cp-1:attempt-1"},
	}})
	if err != nil {
		t.Fatalf("save final-review plan: %v", err)
	}

	raw, err := runSvc.executePlanLifecycleControlAction(sessionID, "request_followup_checkpoint", map[string]any{
		"plan_id":             "plan-final-review-followup",
		"change_request":      "investigate launch readiness",
		"checkpoint_title":    "Launch readiness",
		"tasks":               []any{"Investigate launch readiness"},
		"acceptance_criteria": []any{"A launch-readiness answer is appended"},
	}, nil, planLifecycleRunContext{RunID: "parent-run", RunSessionID: sessionID, ParentSessionID: sessionID, SourceMessageID: "message-final-review-followup", Inline: true})
	if err != nil {
		t.Fatalf("request inline final-review follow-up: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		RunRequest   struct {
			Context struct {
				CheckpointID string `json:"checkpoint_id"`
				AttemptID    string `json:"attempt_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
		Plan struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode inline final-review follow-up payload: %v raw=%s", err, raw)
	}
	if payload.Action != "request_followup_checkpoint" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "followup-1" || payload.RunRequest.Context.CheckpointID != "followup-1" || payload.RunRequest.Context.AttemptID != "" {
		t.Fatalf("inline follow-up handoff = %#v raw=%s", payload, raw)
	}
	if payload.Plan.Document == nil || len(payload.Plan.Document.Checkpoints) != 2 {
		t.Fatalf("inline follow-up document = %#v", payload.Plan.Document)
	}
	followup := payload.Plan.Document.Checkpoints[1]
	if followup.Status != sessionruntime.PlanCheckpointStatusPending || followup.AttemptID != "" || followup.RunID != "" || len(followup.Attempts) != 0 {
		t.Fatalf("inline follow-up was started by its parent run: %#v", followup)
	}
	if payload.Plan.Document.ExecutionState == nil || payload.Plan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateIdle || payload.Plan.Document.ExecutionState.CurrentRunID != "" {
		t.Fatalf("inline follow-up execution state = %#v", payload.Plan.Document.ExecutionState)
	}

	startResult, err := sessionruntime.NewPlanLifecycleService(sessionSvc).StartCheckpoint(sessionruntime.PlanLifecycleExecutionInput{SessionID: sessionID, PlanID: "plan-final-review-followup", CheckpointID: "followup-1", RunID: "followup-run", RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: 1234})
	if err != nil {
		t.Fatalf("start deferred follow-up checkpoint: %v", err)
	}
	if startResult.Summary.NextCheckpointStatus != sessionruntime.PlanCheckpointStatusInProgress || startResult.AttemptID != "followup-1:attempt-1" || startResult.Plan.Document.Checkpoints[1].RunID != "followup-run" {
		t.Fatalf("started deferred follow-up = %#v", startResult)
	}
}

func checkpointIDsForRunTest(checkpoints []pebblestore.SessionPlanCheckpoint) []string {
	ids := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		ids = append(ids, checkpoint.ID)
	}
	return ids
}

func TestExecutePlanManageBlockedCheckpointHandoffIsStandalone(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-blocked-handoff", "Plan: Blocked Handoff", "# Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "attempt-blocked", CurrentRunID: "run-blocked", CurrentSessionID: "child-session", ParentSessionID: "parent-session"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "attempt-blocked", RunID: "run-blocked", SessionID: "child-session"},
			{ID: "cp-b", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-a",
	}})
	if err != nil {
		t.Fatalf("save blocked handoff plan: %v", err)
	}

	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.UserID == "" {
			input.UserID = "user-test"
		}
		if input.AccountScopeID == "" {
			input.AccountScopeID = "account-test"
		}
		appliedMutations = append(appliedMutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}

	raw, err := runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"mark_blocked","checkpoint_id":"cp-a","attempt_id":"attempt-blocked","run_id":"run-blocked","run_session_id":"child-session","parent_session_id":"parent-session","handoff_title":"Dependency required","handoff_overview":"The checkpoint cannot continue until the deployment dependency is available.","impact_bullets":["Resolution: make the dependency available, then resume this checkpoint.","No deployment state changed."],"suggested_prompts":[{"label":"Resume checkpoint","prompt":"The dependency is available now. Resume this checkpoint."}],"report":"## Blocker\n- dependency missing","result":"blocked","validation":["- not run; blocked by dependency"]}`, "", applyMutation)
	if err != nil {
		t.Fatalf("mark blocked: %v output=%s", err, raw)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append blocked lifecycle: %v", err)
	}

	messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after blocked handoff: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("blocked outcome should append one standalone handoff, got %#v", messages)
	}
	handoff := messages[0]
	if handoff.Role != "system" || handoff.Metadata["source"] != PlanExecutionBlockedHandoffMessageSource || handoff.Metadata["kind"] != "plan_blocked_checkpoint_handoff" || handoff.Metadata["action"] != "mark_blocked" || handoff.Metadata["next_action"] != "stopped" {
		t.Fatalf("blocked handoff metadata = %#v", handoff.Metadata)
	}
	for _, want := range []string{"Dependency required", "The checkpoint cannot continue until the deployment dependency is available.", "- Resolution: make the dependency available, then resume this checkpoint.", "- No deployment state changed."} {
		if !strings.Contains(handoff.Content, want) {
			t.Fatalf("blocked handoff content missing %q: %q", want, handoff.Content)
		}
	}
	for _, unwanted := range []string{"Status: BLOCKED", "Plan: Blocked Handoff", "Report:", "Validation:"} {
		if strings.Contains(handoff.Content, unwanted) {
			t.Fatalf("blocked handoff content should keep %q out of the compact message: %q", unwanted, handoff.Content)
		}
	}
	blockedProjection, ok := handoff.Metadata["blocked_handoff"].(map[string]any)
	blockedDetails, detailsOK := blockedProjection["details"].(map[string]any)
	blockedPrompts, promptsOK := blockedProjection["suggested_prompts"].([]any)
	if !ok || !detailsOK || !promptsOK || blockedProjection["title"] != "Dependency required" || blockedDetails["report"] != "## Blocker\n- dependency missing" || len(blockedPrompts) != 1 {
		t.Fatalf("blocked handoff projection = %#v", handoff.Metadata["blocked_handoff"])
	}
	if len(appliedMutations) != 2 || appliedMutations[1].Kind != sessionruntime.SessionMutationAppendMessage || appliedMutations[1].Message == nil || appliedMutations[1].Message.Metadata["source"] != PlanExecutionBlockedHandoffMessageSource {
		t.Fatalf("blocked handoff mutation ordering = %#v", appliedMutations)
	}
}

func TestExecutePlanManageStartAndContinueCheckpoint(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusPending},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","started_at":1234}`, "")
	if err != nil {
		t.Fatalf("start checkpoint: %v output=%s", err, raw)
	}
	var payload struct {
		Action           string                              `json:"action"`
		CheckpointID     string                              `json:"checkpoint_id"`
		NextAction       string                              `json:"next_action"`
		ExecutionSummary sessionruntime.PlanExecutionSummary `json:"execution_summary"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Action != "start_checkpoint" || payload.CheckpointID != "cp-1" || payload.NextAction != "run_checkpoint_with_current_context" {
		t.Fatalf("start payload action=%q checkpoint=%q next=%q raw=%s", payload.Action, payload.CheckpointID, payload.NextAction, raw)
	}
	doc := payload.Plan.Document
	if doc == nil || doc.ActiveCheckpointID != "cp-1" || doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress || doc.Checkpoints[0].AttemptID != "attempt-1" {
		t.Fatalf("started document = %#v", doc)
	}
	if doc.ExecutionState == nil || doc.ExecutionState.Status != sessionruntime.PlanExecutionStateInProgress || doc.ExecutionState.ActiveAttemptID != "attempt-1" || doc.ExecutionState.CurrentRunID != "run-1" {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
	}

	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"complete_checkpoint","report":"done"}`, "")
	if err != nil {
		t.Fatalf("complete checkpoint: %v output=%s", err, raw)
	}
	var completePayload struct {
		Action           string `json:"action"`
		NextCheckpointID string `json:"next_checkpoint_id"`
		NextAction       string `json:"next_action"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &completePayload); err != nil {
		t.Fatalf("decode complete payload: %v", err)
	}
	if completePayload.Action != "complete_checkpoint" || completePayload.NextCheckpointID != "cp-2" || completePayload.NextAction != "run_checkpoint_with_current_context" {
		t.Fatalf("complete payload action=%q next=%q next_action=%q raw=%s", completePayload.Action, completePayload.NextCheckpointID, completePayload.NextAction, raw)
	}
	if completePayload.Plan.Document == nil || completePayload.Plan.Document.ActiveCheckpointID != "cp-2" || completePayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("completed document = %#v", completePayload.Plan.Document)
	}
}

func TestExecutePlanManageProviderManagedStartDefersDurableTransition(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-provider-start", "Provider start", "# Provider start", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Title: "Provider start",
		Info:  pebblestore.SessionPlanInfo{Goal: "Start the pending checkpoint in a fresh run."},
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateIdle},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Implement",
			Objective:          "Implement the requested change.",
			AcceptanceCriteria: []string{"The change is implemented."},
			Order:              1,
			Status:             sessionruntime.PlanCheckpointStatusPending,
		}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save provider-managed plan: %v", err)
	}

	raw, err := runSvc.executePlanManageToolWithLifecycleRunContext(sessionID, `{"action":"start_checkpoint","checkpoint_id":"cp-1"}`, "", nil, planLifecycleRunContext{
		RunID:           "parent-run",
		RunSessionID:    sessionID,
		ParentSessionID: sessionID,
		Inline:          true,
	})
	if err != nil {
		t.Fatalf("prepare provider-managed checkpoint start: %v", err)
	}
	var payload struct {
		Action                  string `json:"action"`
		CheckpointID            string `json:"checkpoint_id"`
		NextAction              string `json:"next_action"`
		CheckpointStartDeferred bool   `json:"checkpoint_start_deferred"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode provider-managed start payload: %v", err)
	}
	if payload.Action != "start_checkpoint" || payload.CheckpointID != "cp-1" || payload.NextAction != "run_checkpoint_with_current_context" || !payload.CheckpointStartDeferred {
		t.Fatalf("provider-managed start payload = %+v", payload)
	}

	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || active.Document == nil {
		t.Fatalf("get active plan after deferred start: ok=%v err=%v plan=%+v", ok, err, active)
	}
	checkpoint := active.Document.Checkpoints[0]
	if checkpoint.Status != sessionruntime.PlanCheckpointStatusPending || checkpoint.AttemptID != "" || checkpoint.RunID != "" || len(checkpoint.Attempts) != 0 {
		t.Fatalf("deferred start mutated checkpoint = %+v", checkpoint)
	}
	if active.Document.ExecutionState == nil || active.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateIdle || active.Document.ExecutionState.CurrentRunID != "" {
		t.Fatalf("deferred start mutated execution state = %+v", active.Document.ExecutionState)
	}
}

func TestExecutePlanManageApproveAndStartAppliesExecutionPolicy(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-approve", "Approve Plan", "# Approve", "draft", "pending", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "ai-attempt", CurrentRunID: "ai-run"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "ai-attempt", RunID: "ai-run", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "ai-attempt", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusInProgress},
		},
		ActiveCheckpointID: "cp-2",
	}})
	if err != nil {
		t.Fatalf("save approve plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"approve_and_start","execution_granularity":"checkpointed","continue_automatically":true}`, "")
	if err != nil {
		t.Fatalf("approve and start: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		RunRequest   struct {
			PlanCheckpointContext struct {
				PlanID       string `json:"plan_id"`
				CheckpointID string `json:"checkpoint_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
		Plan struct {
			Status        string                           `json:"status"`
			ApprovalState string                           `json:"approval_state"`
			Document      *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode approve payload: %v", err)
	}
	if payload.Action != "approve_and_start" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-1" {
		t.Fatalf("approve payload action=%q checkpoint=%q next=%q raw=%s", payload.Action, payload.CheckpointID, payload.NextAction, raw)
	}
	if payload.RunRequest.PlanCheckpointContext.PlanID != "plan-approve" || payload.RunRequest.PlanCheckpointContext.CheckpointID != "cp-1" {
		t.Fatalf("run request = %#v", payload.RunRequest.PlanCheckpointContext)
	}
	doc := payload.Plan.Document
	if payload.Plan.Status != "approved" || payload.Plan.ApprovalState != "approved" || doc == nil {
		t.Fatalf("approved plan = %#v doc=%#v", payload.Plan, doc)
	}
	if doc.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || doc.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || doc.ExecutionState != nil || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("approved document policy/state = %#v", doc)
	}
	for _, checkpoint := range doc.Checkpoints {
		if checkpoint.Status != sessionruntime.PlanCheckpointStatusPending || checkpoint.AttemptID != "" || len(checkpoint.Attempts) != 0 {
			t.Fatalf("checkpoint runtime was not reset: %#v", checkpoint)
		}
	}
}

func TestPlanManagePermissionPayloadRequestNewPlanDefaultsAutomaticCheckpointed(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	payload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"request_new_plan","title":"Replacement Plan","document":{"title":"Replacement Plan","checkpoints":[{"id":"cp-new","title":"New","status":"pending"}]}}`})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if !needsApproval {
		t.Fatalf("request_new_plan should require approval")
	}
	approved := payload.ApprovedArguments
	if approved["execution_granularity"] != sessionruntime.PlanAcceptanceGranularityCheckpointed || approved["continuation_policy"] != sessionruntime.PlanAcceptanceContinuationAutomatic || approved["continue_automatically"] != true || approved["approval_confirmed"] != true {
		t.Fatalf("approved execution defaults = %#v", approved)
	}
	doc, ok := payload.Document.(*pebblestore.SessionPlanDocument)
	if !ok || doc == nil || doc.Title != "Replacement Plan" || len(doc.Checkpoints) != 1 || doc.Checkpoints[0].ID != "cp-new" {
		t.Fatalf("document-only request_new_plan payload lost structured document: %#v", payload.Document)
	}
	if _, hasPlanID := approved["plan_id"]; hasPlanID {
		t.Fatalf("separate request_new_plan unexpectedly injected plan_id: %#v", approved)
	}

	approvedArgs := planManageApprovalArguments(map[string]any{"action": "request_new_plan", "approved_arguments": map[string]any{"action": "request_new_plan", "title": "Approved replacement"}})
	if approvedArgs["execution_granularity"] != sessionruntime.PlanAcceptanceGranularityCheckpointed || approvedArgs["continuation_policy"] != sessionruntime.PlanAcceptanceContinuationAutomatic || approvedArgs["continue_automatically"] != true || approvedArgs["approval_confirmed"] != true {
		t.Fatalf("approved feedback defaults = %#v", approvedArgs)
	}
}

func TestPlanManagePermissionPayloadRequestFollowupUsesLifecycleArguments(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-followup-permission", "Followup Plan", "# Followup", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save follow-up plan: %v", err)
	}

	payload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"request_followup_checkpoint","plan_id":"plan-followup-permission","change_request":"add a review note","checkpoint_title":"Audit note handoff","tasks":["Preserve request"],"acceptance_criteria":["No context lost"],"notes":"handoff context"}`})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if !needsApproval || payload.PathID != "tool.plan-followup-request.v1" || !payload.ApprovalRequired {
		t.Fatalf("follow-up permission payload = %#v needsApproval=%v", payload, needsApproval)
	}
	approved := payload.ApprovedArguments
	if approved["action"] != "request_followup_checkpoint" || approved["change_request"] != "add a review note" || approved["checkpoint_title"] != "Audit note handoff" || approved["notes"] != "handoff context" || approved["approval_confirmed"] != true {
		t.Fatalf("approved lifecycle arguments = %#v", approved)
	}
	for _, patchKey := range []string{"document_operation", "operation", "document_patch", "operations", "checkpoint_order", "active_checkpoint_id"} {
		if _, ok := approved[patchKey]; ok {
			t.Fatalf("approved lifecycle arguments leaked patch key %q: %#v", patchKey, approved)
		}
	}
}

func TestPlanManagePermissionPayloadRequestNewPlanRejectsEmptyTitleOnly(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"request_new_plan","title":"Empty Proposal"}`})
	if err == nil || !strings.Contains(err.Error(), "structured document or plan text") {
		t.Fatalf("empty request_new_plan permission err=%v", err)
	}
}

func TestExecutePlanManageRequestNewPlanReplacementApprovesActivePlan(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	originalDoc := &pebblestore.SessionPlanDocument{
		Title: "Original Plan",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: sessionruntime.PlanCheckpointStatusCompleted}},
	}
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-replace", "Original Plan", "# Original", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: originalDoc})
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}
	replacement := &pebblestore.SessionPlanDocument{
		Title: "Replacement Plan",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: "running", ActiveAttemptID: "stale-attempt"},
		Checkpoints:    []pebblestore.SessionPlanCheckpoint{{ID: "cp-new", Title: "New", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "stale-attempt"}},
	}
	replacementRaw, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("marshal replacement: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_new_plan","plan_id":"plan-replace","title":"Replacement Plan","plan":"# Replacement Plan","approval_confirmed":true,"document":`+string(replacementRaw)+`}`, "")
	if err != nil {
		t.Fatalf("request replacement plan: %v output=%s", err, raw)
	}
	var payload struct {
		Action     string `json:"action"`
		NextAction string `json:"next_action"`
		Plan       struct {
			ID            string                           `json:"id"`
			Status        string                           `json:"status"`
			ApprovalState string                           `json:"approval_state"`
			Active        bool                             `json:"active"`
			Document      *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode request_new_plan payload: %v", err)
	}
	if payload.Action != "request_new_plan" || payload.NextAction != "run_checkpoint_with_current_context" {
		t.Fatalf("replacement request should approve active replacement and be runnable: raw=%s", raw)
	}
	if payload.Plan.ID != "plan-replace" || !payload.Plan.Active || payload.Plan.Status != "approved" || payload.Plan.ApprovalState != "approved" {
		t.Fatalf("replacement plan state = %#v", payload.Plan)
	}
	if payload.Plan.Document == nil || payload.Plan.Document.ID != "plan-replace" || payload.Plan.Document.Status != "approved" || payload.Plan.Document.ActiveCheckpointID != "cp-new" {
		t.Fatalf("replacement document = %#v", payload.Plan.Document)
	}
	if payload.Plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || payload.Plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || payload.Plan.Document.ExecutionState != nil {
		t.Fatalf("replacement approval policy/state = %#v", payload.Plan.Document)
	}
	if len(payload.Plan.Document.Checkpoints) != 1 || payload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending || payload.Plan.Document.Checkpoints[0].AttemptID != "" {
		t.Fatalf("replacement checkpoint runtime was not reset: %#v", payload.Plan.Document.Checkpoints)
	}

	followupRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_followup_checkpoint","plan_id":"plan-replace","change_request":"Add follow-up.","checkpoint_title":"Follow-up handoff","tasks":["Handle follow-up"],"acceptance_criteria":["Follow-up preserved"],"notes":"Lifecycle notes should not be parsed as a patch.","approval_confirmed":true}`, "")
	if err != nil {
		t.Fatalf("request follow-up after replacement: %v output=%s", err, followupRaw)
	}
	var followupPayload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(followupRaw), &followupPayload); err != nil {
		t.Fatalf("decode follow-up payload: %v", err)
	}
	if followupPayload.Action != "request_followup_checkpoint" || followupPayload.NextAction != "run_checkpoint_with_current_context" || followupPayload.CheckpointID == "" {
		t.Fatalf("approved follow-up should be inserted and runnable, raw=%s", followupRaw)
	}
	if followupPayload.Plan.Document == nil || len(followupPayload.Plan.Document.Checkpoints) != 2 {
		t.Fatalf("follow-up document missing inserted checkpoint: %#v", followupPayload.Plan.Document)
	}
	var inserted *pebblestore.SessionPlanCheckpoint
	for i := range followupPayload.Plan.Document.Checkpoints {
		if followupPayload.Plan.Document.Checkpoints[i].Title == "Follow-up handoff" {
			inserted = &followupPayload.Plan.Document.Checkpoints[i]
			break
		}
	}
	if inserted == nil || inserted.Notes == "" || inserted.Tasks[0] != "Handle follow-up" || inserted.AcceptanceCriteria[0] != "Follow-up preserved" {
		t.Fatalf("follow-up checkpoint did not preserve lifecycle fields: %#v", followupPayload.Plan.Document.Checkpoints)
	}
}

func TestExecutePlanManageRequestNewPlanSeparateProposalAwaitsApproval(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-original", "Original Plan", "# Original", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Title:       "Original Plan",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: sessionruntime.PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_new_plan","title":"Separate Proposal","plan":"# Separate Proposal","document":{"title":"Separate Proposal","checkpoints":[{"id":"cp-proposed","title":"Proposed","status":"pending"}]}}`, "")
	if err != nil {
		t.Fatalf("request separate proposal: %v output=%s", err, raw)
	}
	var payload struct {
		NextAction string `json:"next_action"`
		Plan       struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			ApprovalState string `json:"approval_state"`
			Active        bool   `json:"active"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode separate proposal payload: %v", err)
	}
	if payload.NextAction != "await_approval" || payload.Plan.Active || payload.Plan.Status != "pending_approval" || payload.Plan.ApprovalState != "pending" {
		t.Fatalf("separate proposal payload = %#v raw=%s", payload, raw)
	}
	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	if active.ID != "plan-original" || active.ApprovalState != "approved" {
		t.Fatalf("active plan should remain original, got %#v", active)
	}
}

func TestExecutePlanManageRequestNewPlanWithoutActivePlanRequiresApproval(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_new_plan","title":"Big Plan","document":{"title":"Big Plan","checkpoints":[{"id":"cp-1","title":"First","status":"pending"},{"id":"cp-2","title":"Second","status":"pending"}]}}`, "")
	if err == nil || !strings.Contains(err.Error(), "requires user approval") {
		t.Fatalf("request_new_plan without approval err=%v raw=%s", err, raw)
	}
	if active, ok, err := sessionSvc.GetActivePlan(sessionID); err != nil || ok {
		t.Fatalf("unapproved request_new_plan should not create active plan: ok=%v err=%v active=%#v", ok, err, active)
	}
}

func TestExecutePlanManageRequestNewPlanPermissionThenApprovalWithoutActivePlanRuns(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}

	call := tool.Call{Name: "plan_manage", Arguments: `{"action":"propose_plan","title":"Big Plan","document":{"title":"Big Plan","execution_state":{"status":"running","active_attempt_id":"stale-attempt"},"checkpoints":[{"id":"cp-1","title":"First","status":"in_progress","attempt_id":"stale-attempt"},{"id":"cp-2","title":"Second","status":"pending"}]}}`}
	permissionPayload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, call)
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if !needsApproval || permissionPayload.PathID != "tool.plan-new-request.v1" || permissionPayload.Action != "request_new_plan" {
		t.Fatalf("permission payload = %#v needsApproval=%v", permissionPayload, needsApproval)
	}
	approved := permissionPayload.ApprovedArguments
	if approved["action"] != "request_new_plan" || approved["approval_confirmed"] != true || approved["execution_granularity"] != sessionruntime.PlanAcceptanceGranularityCheckpointed || approved["continue_automatically"] != true {
		t.Fatalf("approved arguments = %#v", approved)
	}
	feedbackRaw, err := json.Marshal(map[string]any{"action": permissionPayload.Action, "approved_arguments": approved})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}
	raw, err := runSvc.executePlanManageTool(sessionID, call.Arguments, string(feedbackRaw))
	if err != nil {
		t.Fatalf("approved request_new_plan: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			ID            string                           `json:"id"`
			Status        string                           `json:"status"`
			ApprovalState string                           `json:"approval_state"`
			Active        bool                             `json:"active"`
			Document      *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode approved payload: %v", err)
	}
	if payload.Action != "request_new_plan" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-1" {
		t.Fatalf("approved payload action=%q next=%q checkpoint=%q raw=%s", payload.Action, payload.NextAction, payload.CheckpointID, raw)
	}
	if payload.Plan.ID == "" || !payload.Plan.Active || payload.Plan.Status != "approved" || payload.Plan.ApprovalState != "approved" || payload.Plan.Document == nil {
		t.Fatalf("approved plan = %#v", payload.Plan)
	}
	if payload.Plan.Document.ExecutionState != nil || payload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending || payload.Plan.Document.Checkpoints[0].AttemptID != "" {
		t.Fatalf("approved document runtime was not reset: %#v", payload.Plan.Document)
	}
}

func TestExecutePlanManageRequestNewPlanApprovedSeparatePlanActivatesAndRuns(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-original", "Original Plan", "# Original", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Title:       "Original Plan",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: sessionruntime.PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_new_plan","title":"Approved Separate","plan":"# Approved Separate","approval_confirmed":true,"document":{"title":"Approved Separate","execution_state":{"status":"running","active_attempt_id":"stale-attempt"},"checkpoints":[{"id":"cp-proposed","title":"Proposed","status":"in_progress","attempt_id":"stale-attempt"}]}}`, "")
	if err != nil {
		t.Fatalf("approve separate proposal: %v output=%s", err, raw)
	}
	var payload struct {
		NextAction       string                              `json:"next_action"`
		CheckpointID     string                              `json:"checkpoint_id"`
		ExecutionSummary sessionruntime.PlanExecutionSummary `json:"execution_summary"`
		Plan             struct {
			ID            string                           `json:"id"`
			Status        string                           `json:"status"`
			ApprovalState string                           `json:"approval_state"`
			Active        bool                             `json:"active"`
			Document      *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode approved separate payload: %v", err)
	}
	if payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-proposed" || payload.ExecutionSummary.NextCheckpointID != "cp-proposed" {
		t.Fatalf("approved separate proposal should be runnable: %#v raw=%s", payload, raw)
	}
	if payload.Plan.ID == "plan-original" || !payload.Plan.Active || payload.Plan.Status != "approved" || payload.Plan.ApprovalState != "approved" {
		t.Fatalf("approved separate plan state = %#v", payload.Plan)
	}
	if payload.Plan.Document == nil || payload.Plan.Document.ID != payload.Plan.ID || payload.Plan.Document.Status != "approved" || payload.Plan.Document.ActiveCheckpointID != "cp-proposed" {
		t.Fatalf("approved separate document = %#v", payload.Plan.Document)
	}
	if payload.Plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || payload.Plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || payload.Plan.Document.ExecutionState != nil {
		t.Fatalf("approved separate policy/runtime = %#v", payload.Plan.Document)
	}
	if len(payload.Plan.Document.Checkpoints) != 1 || payload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending || payload.Plan.Document.Checkpoints[0].AttemptID != "" {
		t.Fatalf("approved separate checkpoint runtime was not reset: %#v", payload.Plan.Document.Checkpoints)
	}
	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	if active.ID != payload.Plan.ID || active.ID == "plan-original" || active.ApprovalState != "approved" {
		t.Fatalf("active approved separate plan = %#v", active)
	}
}

func TestExecutePlanManageRequestNewPlanApprovalWithoutDocumentFails(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_new_plan","title":"Missing Document","plan":"# Missing Document","approval_confirmed":true}`, "")
	if err == nil || !strings.Contains(err.Error(), "structured document") {
		t.Fatalf("approval without document err=%v output=%s", err, raw)
	}
}

func TestExecutePlanManageFinalCheckpointAwaitsReview(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-final-review", "Final Review", "# Final", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusInProgress},
		},
		ActiveCheckpointID: "cp-2",
	}})
	if err != nil {
		t.Fatalf("save final review plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-2","report":"done","recommendation":{"decision":"ship","action":"accept_and_archive","reason":"focused checks passed","action_state":"ready"}}`, "")
	if err != nil {
		t.Fatalf("complete final checkpoint: %v output=%s", err, raw)
	}
	var payload struct {
		NextAction       string                              `json:"next_action"`
		NextCheckpointID string                              `json:"next_checkpoint_id"`
		ExecutionSummary sessionruntime.PlanExecutionSummary `json:"execution_summary"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode final payload: %v", err)
	}
	doc := payload.Plan.Document
	if payload.NextAction != "await_review" || payload.NextCheckpointID != "cp-2" || payload.ExecutionSummary.PlanComplete || !payload.ExecutionSummary.ReviewRequired {
		t.Fatalf("final payload raw=%s summary=%#v", raw, payload.ExecutionSummary)
	}
	if doc == nil || doc.ExecutionState == nil || doc.ExecutionState.Status != sessionruntime.PlanExecutionStateWaitingReview || doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("final doc = %#v", doc)
	}
	if doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted || doc.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("checkpoint statuses = %#v", doc.Checkpoints)
	}
	if recommendation := doc.Checkpoints[1].Recommendation; recommendation == nil || recommendation.Decision != "ship" || recommendation.Action != "accept_and_archive" || recommendation.ActionState != "ready" {
		t.Fatalf("checkpoint recommendation = %#v", recommendation)
	}
}

func TestExecutePlanManageRepeatedFinalCompleteCheckpointIsRejected(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-final-review-repeat", "Final Review Repeat", "# Final", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusCompleted},
		},
		ActiveCheckpointID: "cp-2",
	}})
	if err != nil {
		t.Fatalf("save waiting-review plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-2","report":"done again"}`, "")
	if err == nil {
		t.Fatalf("repeated complete_checkpoint succeeded unexpectedly: %s", raw)
	}
	if !strings.Contains(err.Error(), "request_followup_checkpoint") {
		t.Fatalf("error = %v, want request_followup_checkpoint guidance (raw=%s)", err, raw)
	}
}

func TestExecutePlanManageAcceptRestartAndRewind(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-control", "Control Plan", "# Control", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, ActiveAttemptID: "cp-1:attempt-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusPending}, AttemptID: "cp-1:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusFailed, AttemptID: "cp-2:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-2:attempt-1", Status: sessionruntime.PlanCheckpointStatusFailed}}},
			{ID: "cp-3", Title: "UI", Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "cp-3:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-3:attempt-1", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save control plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"restart_checkpoint","checkpoint_id":"cp-2"}`, "")
	if err != nil {
		t.Fatalf("restart checkpoint: %v output=%s", err, raw)
	}
	var restartPayload struct {
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &restartPayload); err != nil {
		t.Fatalf("decode restart payload: %v", err)
	}
	restarted := restartPayload.Plan.Document
	if restartPayload.NextAction != "run_checkpoint_with_current_context" || restartPayload.CheckpointID != "cp-2" || restarted.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || len(restarted.Checkpoints[1].Attempts) != 0 || restarted.Checkpoints[2].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("restart payload raw=%s doc=%#v", raw, restarted)
	}

	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"rewind_to_checkpoint","checkpoint_id":"cp-2"}`, "")
	if err != nil {
		t.Fatalf("rewind checkpoint: %v output=%s", err, raw)
	}
	var rewindPayload struct {
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &rewindPayload); err != nil {
		t.Fatalf("decode rewind payload: %v", err)
	}
	rewound := rewindPayload.Plan.Document
	if rewindPayload.CheckpointID != "cp-2" || rewound.ActiveCheckpointID != "cp-2" || rewound.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || rewound.Checkpoints[2].Status != sessionruntime.PlanCheckpointStatusPending || len(rewound.Checkpoints[2].Attempts) != 0 {
		t.Fatalf("rewind payload raw=%s doc=%#v", raw, rewound)
	}
}

func TestExecutePlanManageRestartCheckpointAtomicallyReplacesChangedRequirements(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-restart-replace", "Restart Replace", "# Restart", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStatePaused, LastCheckpointID: "cp-1", LastAttemptID: "cp-1:attempt-1", LastOutcome: sessionruntime.PlanCheckpointStatusPaused},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Old title", Status: sessionruntime.PlanCheckpointStatusPaused, Objective: "old objective", Tasks: []string{"old task"}, AcceptanceCriteria: []string{"old criterion"}, AttemptID: "cp-1:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", Status: sessionruntime.PlanCheckpointStatusPaused}}}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save replace-restart plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"restart_checkpoint","checkpoint_id":"cp-1","change_request":"redirect the same feature to the new behavior","checkpoint_title":"New behavior","tasks":["implement redirected behavior","remove stale assumptions"],"acceptance_criteria":["new behavior works","old requirement is not retained"],"notes":"replacement handoff","source_message_id":"redirect-message"}`, "")
	if err != nil {
		t.Fatalf("replace and restart checkpoint: %v output=%s", err, raw)
	}
	var payload struct {
		NextAction string `json:"next_action"`
		Plan       struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode replacement restart payload: %v", err)
	}
	checkpoint := payload.Plan.Document.Checkpoints[0]
	if payload.NextAction != "run_checkpoint_with_current_context" || checkpoint.Title != "New behavior" || checkpoint.Objective != "redirect the same feature to the new behavior" || strings.Join(checkpoint.Tasks, ",") != "implement redirected behavior,remove stale assumptions" || strings.Join(checkpoint.AcceptanceCriteria, ",") != "new behavior works,old requirement is not retained" || checkpoint.SourceMessageID != "redirect-message" {
		t.Fatalf("replacement restart did not carry new requirements: raw=%s checkpoint=%#v", raw, checkpoint)
	}
	if checkpoint.Status != sessionruntime.PlanCheckpointStatusPending || checkpoint.AttemptID != "" || len(checkpoint.Attempts) != 0 || !strings.Contains(checkpoint.Notes, "replacement handoff") {
		t.Fatalf("replacement restart did not reset the checkpoint for a fresh execution attempt: %#v", checkpoint)
	}
	if !strings.Contains(checkpoint.Notes, "Current user request / change_request:") || strings.Contains(checkpoint.Notes, "Original user request/context:") || strings.Contains(checkpoint.Notes, "old objective") {
		t.Fatalf("replacement restart retained a competing prior objective: %#v", checkpoint)
	}
}

func TestExecutePlanManageRestartCheckpointRejectsIncompleteReplacement(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-restart-incomplete", "Restart Incomplete", "# Restart", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Old", Status: sessionruntime.PlanCheckpointStatusPaused, Objective: "old", Tasks: []string{"old"}, AcceptanceCriteria: []string{"old"}}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save incomplete-restart plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"restart_checkpoint","checkpoint_id":"cp-1","change_request":"changed requirements but no acceptance criteria","checkpoint_title":"Changed","tasks":["implement changed requirements"]}`, "")
	if err == nil || !strings.Contains(err.Error(), "replacement requires acceptance_criteria") {
		t.Fatalf("incomplete replacement restart err=%v raw=%s", err, raw)
	}
}

func TestExecutePlanManageNeedsReviewAndBlockedStopAdvancement(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"mark_needs_review","report":"needs audit"}`, "")
	if err != nil {
		t.Fatalf("mark needs review: %v output=%s", err, raw)
	}
	var reviewPayload struct {
		NextAction       string `json:"next_action"`
		NextCheckpointID string `json:"next_checkpoint_id"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &reviewPayload); err != nil {
		t.Fatalf("decode review payload: %v", err)
	}
	if reviewPayload.NextAction != "await_review" || reviewPayload.NextCheckpointID != "cp-1" || reviewPayload.Plan.Document.ActiveCheckpointID != "cp-1" || reviewPayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusNeedsReview || reviewPayload.Plan.Document.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending {
		t.Fatalf("review payload=%s document=%#v", raw, reviewPayload.Plan.Document)
	}

	_, _, err = sessionSvc.SavePlanWithMetadata(sessionID, "plan-blocked", "Blocked Plan", "# Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-b", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-a",
	}})
	if err != nil {
		t.Fatalf("save blocked plan: %v", err)
	}
	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"mark_blocked","report":"dependency missing"}`, "")
	if err != nil {
		t.Fatalf("mark blocked: %v output=%s", err, raw)
	}
	var blockedPayload struct {
		NextAction string `json:"next_action"`
		Plan       struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &blockedPayload); err != nil {
		t.Fatalf("decode blocked payload: %v", err)
	}
	if blockedPayload.NextAction != "stopped" || blockedPayload.Plan.Document.ActiveCheckpointID != "cp-a" || blockedPayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusBlocked {
		t.Fatalf("blocked payload=%s document=%#v", raw, blockedPayload.Plan.Document)
	}
}

func TestProviderManagedSubtaskReplacementTransfersOwnershipAndAllowsCompletion(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-replan", "Replan", "# Replan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Title: "Replan", Info: pebblestore.SessionPlanInfo{Goal: "Finish same checkpoint"},
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "old-run", CurrentSessionID: sessionID, ParentSessionID: sessionID},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Same contract", Objective: "Finish same contract", AcceptanceCriteria: []string{"Contract complete"}, Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "cp-1:attempt-1", RunID: "old-run", SessionID: sessionID, Subtasks: []pebblestore.SessionPlanSubtask{{ID: "stale", Title: "Obsolete", Status: sessionruntime.PlanSubtaskStatusCompleted}}, Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, RunID: "old-run", SessionID: sessionID, ParentSessionID: sessionID}}}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{SessionID: sessionID, PermissionSessionID: sessionID, RunID: "new-run", Step: 1, SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true, ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.UserID == "" {
			input.UserID = principal.UserID
		}
		if input.AccountScopeID == "" {
			input.AccountScopeID = principal.AccountScopeID
		}
		return sessionSvc.ApplySessionMutation(input)
	}})
	added, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "add", Name: "plan_manage", Arguments: `{"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"id":"temporary","title":"Temporary additive work"}}`})
	if err != nil || added.Error != "" {
		t.Fatalf("add_subtask failed: result=%#v err=%v", added, err)
	}
	addedPlan, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || addedPlan.Document == nil || addedPlan.Document.Checkpoints[0].RunID != "new-run" || addedPlan.Document.ExecutionState.CurrentRunID != "new-run" {
		t.Fatalf("add_subtask did not transfer trusted ownership: ok=%v err=%v plan=%#v", ok, err, addedPlan.Document)
	}
	replaced, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "replace", Name: "plan_manage", Arguments: `{"action":"replace_subtasks","checkpoint_id":"cp-1","subtasks":[{"id":"new-task","title":"Revised work"}]}`})
	if err != nil || replaced.Error != "" {
		t.Fatalf("replace_subtasks failed: result=%#v err=%v", replaced, err)
	}
	plan, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || plan.Document == nil {
		t.Fatalf("get replanned state: ok=%v err=%v", ok, err)
	}
	checkpoint := plan.Document.Checkpoints[0]
	if checkpoint.RunID != "new-run" || plan.Document.ExecutionState.CurrentRunID != "new-run" || len(checkpoint.Subtasks) != 1 || checkpoint.Subtasks[0].ID != "new-task" {
		t.Fatalf("trusted ownership/checklist not transferred: %#v", plan.Document)
	}
	completed, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "complete", Name: "plan_manage", Arguments: `{"action":"complete_subtask","checkpoint_id":"cp-1","subtask_id":"new-task","complete_checkpoint":true,"report":"done","result":"done","handoff_overview":"Replanned work is complete.","recommendation":{"decision":"ship","action":"review","reason":"complete","action_state":"ready"}}`})
	if err != nil || completed.Error != "" {
		t.Fatalf("completion under transferred ownership failed: result=%#v err=%v", completed, err)
	}

	foreign := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{SessionID: sessionID, PermissionSessionID: sessionID, RunID: "foreign-run", Step: 2, SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true})
	result, err := foreign.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "foreign", Name: "plan_manage", Arguments: `{"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"foreign","handoff_overview":"Foreign completion."}`})
	if err != nil || !strings.Contains(result.Error, "does not own the active run") {
		t.Fatalf("foreign completion was not rejected: result=%#v err=%v", result, err)
	}
}

func TestProviderManagedPlanManageLifecycleMessageFollowsToolCompletion(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-provider-lifecycle", "Provider Lifecycle", "# Lifecycle", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-2", Title: "Second", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save provider lifecycle plan: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.UserID == "" {
			input.UserID = principal.UserID
		}
		if input.AccountScopeID == "" {
			input.AccountScopeID = principal.AccountScopeID
		}
		appliedMutations = append(appliedMutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}
	var events []StreamEvent
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID:            sessionID,
		PermissionSessionID:  sessionID,
		RunID:                "run-provider-lifecycle",
		Step:                 1,
		SessionMode:          sessionruntime.ModeAuto,
		Principal:            principal,
		Emit:                 func(event StreamEvent) { events = append(events, event) },
		ApplySessionMutation: applyMutation,
		ProviderManagedV3:    true,
	})
	if invoker == nil {
		t.Fatal("provider managed invoker is nil")
	}

	missingHandoff, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-plan-missing-handoff", Name: "plan_manage", Arguments: `{"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"done"}`})
	if err != nil || !strings.Contains(missingHandoff.Error, "final checkpoint completion requires handoff_overview") {
		t.Fatalf("provider managed final completion without structured handoff = %#v, err=%v", missingHandoff, err)
	}
	if len(appliedMutations) != 0 {
		t.Fatalf("rejected final completion mutated durable state: %#v", appliedMutations)
	}

	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-plan", Name: "plan_manage", Arguments: `{"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"done","handoff_overview":"The checkpoint is complete and ready for review."}`})
	if err != nil {
		t.Fatalf("execute provider managed plan_manage: %v", err)
	}
	if !result.RestartTurn {
		t.Fatalf("provider managed plan_manage should request restart, result = %#v", result)
	}

	wantMutations := []struct {
		kind      string
		eventType string
	}{
		{sessionruntime.SessionMutationSavePlan, "session.plan.saved"},
		{sessionruntime.SessionMutationAppendMessage, "session.tool.completed"},
		{sessionruntime.SessionMutationAppendMessage, "session.message.appended"},
	}
	if len(appliedMutations) != len(wantMutations) {
		t.Fatalf("mutation count = %d, want %d: %#v", len(appliedMutations), len(wantMutations), appliedMutations)
	}
	for i, want := range wantMutations {
		if appliedMutations[i].Kind != want.kind || appliedMutations[i].EventType != want.eventType {
			t.Fatalf("mutation[%d] kind/event = %q/%q, want %q/%q", i, appliedMutations[i].Kind, appliedMutations[i].EventType, want.kind, want.eventType)
		}
	}

	if len(events) < 2 || events[0].Type != StreamEventToolStarted || events[1].Type != StreamEventToolCompleted {
		t.Fatalf("stream events did not emit tool start/completion first: %#v", events)
	}

	outbox, err := sessionSvc.ListRealtimeOutboxForSessionAfterSeq(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	wantOutbox := []string{"session.plan.saved", "session.tool.completed", "session.message.appended"}
	if len(outbox) != len(wantOutbox) {
		t.Fatalf("outbox count = %d, want %d: %#v", len(outbox), len(wantOutbox), outbox)
	}
	for i, want := range wantOutbox {
		if outbox[i].Event.EventType != want {
			t.Fatalf("outbox[%d] = %q, want %q", i, outbox[i].Event.EventType, want)
		}
	}

	messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "tool" || messages[1].Role != "system" {
		t.Fatalf("message ordering = %#v", messages)
	}
	if messages[1].Metadata["source"] != PlanExecutionLifecycleMessageSource {
		t.Fatalf("system lifecycle metadata = %#v", messages[1].Metadata)
	}
}

func TestPlanManageRequestNewPlanWithActivePlanUsesCanonicalApprovalForSingleAndMultiCheckpointDocuments(t *testing.T) {
	for _, tc := range []struct {
		name        string
		checkpoints []map[string]any
	}{
		{
			name: "single checkpoint",
			checkpoints: []map[string]any{{
				"id": "cp-new-1", "title": "Implement unrelated task", "status": "pending", "order": 1,
				"tasks": []string{"Implement the unrelated task"}, "acceptance_criteria": []string{"The unrelated task is complete"},
			}},
		},
		{
			name: "multiple checkpoints",
			checkpoints: []map[string]any{
				{"id": "cp-new-1", "title": "Implement phase", "status": "pending", "order": 1, "tasks": []string{"Implement the first phase"}, "acceptance_criteria": []string{"The first phase is complete"}},
				{"id": "cp-new-2", "title": "Review phase", "status": "pending", "order": 2, "tasks": []string{"Review the independent phase"}, "acceptance_criteria": []string{"The second phase is complete"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
			defer cleanup()

			sessionID := createPlanManageTestSession(t, sessionSvc)
			_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-original", "Original Plan", "# Original", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
				Title:           "Original Plan",
				Info:            pebblestore.SessionPlanInfo{Goal: "Finish the original product goal."},
				ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
				Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Original", Objective: "Finish original work.", AcceptanceCriteria: []string{"Original work is done."}, Status: sessionruntime.PlanCheckpointStatusCompleted, Order: 1}},
			}})
			if err != nil {
				t.Fatalf("save original plan: %v", err)
			}

			document := map[string]any{
				"title":       "Unrelated Plan",
				"info":        map[string]any{"goal": "Complete a separate unrelated product goal."},
				"checkpoints": tc.checkpoints,
			}
			documentRaw, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}
			call := tool.Call{Name: "plan_manage", Arguments: `{"action":"request_new_plan","title":"Unrelated Plan","document":` + string(documentRaw) + `}`}
			permissionPayload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, call)
			if err != nil {
				t.Fatalf("build request_new_plan approval: %v", err)
			}
			if !needsApproval || permissionPayload.Action != "request_new_plan" || permissionPayload.PathID != "tool.plan-new-request.v1" {
				t.Fatalf("permission payload = %#v needsApproval=%v", permissionPayload, needsApproval)
			}
			activeBefore, ok, err := sessionSvc.GetActivePlan(sessionID)
			if err != nil || !ok || activeBefore.ID != "plan-original" {
				t.Fatalf("pending approval replaced active plan: ok=%v err=%v active=%#v", ok, err, activeBefore)
			}

			feedbackRaw, err := json.Marshal(map[string]any{"action": permissionPayload.Action, "approved_arguments": permissionPayload.ApprovedArguments})
			if err != nil {
				t.Fatalf("marshal approval feedback: %v", err)
			}
			raw, err := runSvc.executePlanManageTool(sessionID, call.Arguments, string(feedbackRaw))
			if err != nil {
				t.Fatalf("approve request_new_plan: %v output=%s", err, raw)
			}
			var result struct {
				Action       string `json:"action"`
				NextAction   string `json:"next_action"`
				CheckpointID string `json:"checkpoint_id"`
				RunRequest   struct {
					Context *RunPlanCheckpointContext `json:"plan_checkpoint_context"`
				} `json:"run_request"`
				Plan struct {
					ID       string                           `json:"id"`
					Active   bool                             `json:"active"`
					Document *pebblestore.SessionPlanDocument `json:"document"`
				} `json:"plan"`
			}
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatalf("decode approved request_new_plan: %v", err)
			}
			if result.Action != "request_new_plan" || result.NextAction != "run_checkpoint_with_current_context" || result.CheckpointID != "cp-new-1" || result.RunRequest.Context == nil || result.RunRequest.Context.CheckpointID != "cp-new-1" {
				t.Fatalf("approved request_new_plan did not return fresh-context start: %#v raw=%s", result, raw)
			}
			if !result.Plan.Active || result.Plan.ID == "" || result.Plan.ID == "plan-original" || result.Plan.Document == nil || len(result.Plan.Document.Checkpoints) != len(tc.checkpoints) {
				t.Fatalf("approved new plan = %#v", result.Plan)
			}
			activeAfter, ok, err := sessionSvc.GetActivePlan(sessionID)
			if err != nil || !ok || activeAfter.ID != result.Plan.ID {
				t.Fatalf("approved plan was not activated: ok=%v err=%v active=%#v result=%#v", ok, err, activeAfter, result.Plan)
			}
		})
	}
}

func TestProviderManagedPlanManageAllowsRequestNewPlanButRejectsRecursiveCheckpointCreation(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-provider-guard", "Provider Guard", "# Guard", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-provider-guard", CurrentSessionID: sessionID, ParentSessionID: sessionID},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Owned", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-provider-guard", SessionID: sessionID}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save provider guard plan: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID: sessionID, PermissionSessionID: sessionID, RunID: "run-provider-guard", Step: 1,
		SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true,
		ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			if input.UserID == "" {
				input.UserID = principal.UserID
			}
			if input.AccountScopeID == "" {
				input.AccountScopeID = principal.AccountScopeID
			}
			return sessionSvc.ApplySessionMutation(input)
		},
	})

	for _, action := range []string{"start_session_checkpoint", "request_followup_checkpoint"} {
		result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-" + action, Name: "plan_manage", Arguments: `{"action":"` + action + `","change_request":"recursive work","checkpoint_title":"Recursive"}`})
		if err != nil {
			t.Fatalf("execute %s: %v", action, err)
		}
		if !strings.Contains(result.Error, "recursive session checkpoint creation is not allowed") {
			t.Fatalf("%s escaped checkpoint guard: %#v", action, result)
		}
	}

	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-request-new-plan", Name: "plan_manage", Arguments: `{"action":"request_new_plan","title":"Unrelated Plan","approval_confirmed":true,"document":{"title":"Unrelated Plan","info":{"goal":"Complete an unrelated goal."},"checkpoints":[{"id":"cp-new","title":"Unrelated","status":"pending","order":1,"tasks":["Implement unrelated work"],"acceptance_criteria":["Unrelated work is complete"]}]}}`})
	if err != nil {
		t.Fatalf("execute request_new_plan: %v", err)
	}
	if strings.Contains(result.Error, "recursive session checkpoint creation") || result.Error != "" {
		t.Fatalf("request_new_plan was rejected by checkpoint guard: %#v", result)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode request_new_plan result: %v output=%s", err, result.Output)
	}
	if payload.Action != "request_new_plan" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-new" {
		t.Fatalf("request_new_plan result = %#v output=%s", payload, result.Output)
	}
}

func TestProviderManagedPlanManageRejectsFollowupFromCheckpointRun(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-provider-lifecycle", "Provider Lifecycle", "# Lifecycle", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-provider-lifecycle", CurrentSessionID: sessionID, ParentSessionID: sessionID},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-provider-lifecycle", SessionID: sessionID},
			{ID: "cp-2", Title: "Second", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save provider lifecycle plan: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.UserID == "" {
			input.UserID = principal.UserID
		}
		if input.AccountScopeID == "" {
			input.AccountScopeID = principal.AccountScopeID
		}
		appliedMutations = append(appliedMutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID:            sessionID,
		PermissionSessionID:  sessionID,
		RunID:                "run-provider-lifecycle",
		Step:                 1,
		SessionMode:          sessionruntime.ModeAuto,
		Principal:            principal,
		ApplySessionMutation: applyMutation,
		ProviderManagedV3:    true,
	})
	if invoker == nil {
		t.Fatal("provider managed invoker is nil")
	}

	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-plan", Name: "plan_manage", Arguments: `{"action":"request_followup_checkpoint","change_request":"add another checkpoint"}`})
	if err != nil {
		t.Fatalf("execute provider managed plan_manage: %v", err)
	}
	if result.RestartTurn {
		t.Fatalf("rejected follow-up should not request restart, result = %#v", result)
	}
	if !strings.Contains(result.Error, "session checkpoint creation is not allowed from checkpoint run") {
		t.Fatalf("result error = %q", result.Error)
	}
	for _, mutation := range appliedMutations {
		if mutation.Kind == sessionruntime.SessionMutationSavePlan {
			t.Fatalf("rejected follow-up should not save plan, mutations = %#v", appliedMutations)
		}
	}
	plan, ok, err := sessionSvc.GetPlan(sessionID, "plan-provider-lifecycle")
	if err != nil || !ok {
		t.Fatalf("get plan ok=%v err=%v", ok, err)
	}
	if len(plan.Document.Checkpoints) != 2 || plan.Document.ActiveCheckpointID != "cp-1" {
		t.Fatalf("plan changed after rejection: %#v", plan.Document)
	}
}
