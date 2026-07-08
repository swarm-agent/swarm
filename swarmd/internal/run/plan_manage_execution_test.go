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
	if payload.Action != "start_session_checkpoint" || payload.NextAction != "run_checkpoint_with_fresh_context" || payload.CheckpointID != "cp-1" {
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
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusPending},
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

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"done"}`, "", applyMutation)
	if err != nil {
		t.Fatalf("complete checkpoint: %v output=%s", err, raw)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append complete lifecycle: %v", err)
	}
	assertLifecycleMessage(0, "complete_checkpoint", "run_checkpoint_with_fresh_context", 3, "Checkpoint complete — Automatic mode", "Plan: Lifecycle Actions", "Completed: Checkpoint 1 — Model", "Next: Checkpoint 2 — API", "Context: Starting the next checkpoint with fresh context.")

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-2","run_id":"run-2","run_session_id":"child-session-2","parent_session_id":"parent-session","started_at":2345}`, "", applyMutation)
	if err != nil {
		t.Fatalf("start second checkpoint: %v output=%s", err, raw)
	}
	if len(appliedMutations) != 5 {
		t.Fatalf("start second checkpoint should only add plan saved mutation, count = %d: %#v", len(appliedMutations), appliedMutations)
	}

	raw, err = runSvc.executePlanManageToolWithMutation(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-2","report":"## Summary\n- done","result":"finished","validation":["- lifecycle handoff regression"]}`, "", applyMutation)
	if err != nil {
		t.Fatalf("complete final checkpoint: %v output=%s", err, raw)
	}
	if err := runSvc.appendPlanLifecycleMessageForToolResult(sessionID, tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}, applyMutation); err != nil {
		t.Fatalf("append final lifecycle: %v", err)
	}
	assertLifecycleMessage(1, "complete_checkpoint", "await_review", 6, "All checkpoints complete; review required — Automatic mode", "Completed: Checkpoint 2 — API", "Next: all checkpoints are complete; waiting for user review.")
	messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list messages after final handoff: %v", err)
	}
	if len(messages) != 3 || messages[2].Role != "system" || messages[2].Metadata["source"] != PlanExecutionFinalHandoffMessageSource || messages[2].Metadata["kind"] != "plan_final_checkpoint_handoff" || messages[2].Metadata["action"] != "complete_checkpoint" || messages[2].Metadata["next_action"] != "await_review" {
		t.Fatalf("final handoff message metadata/order = %#v", messages)
	}
	for _, want := range []string{"Final checkpoint handoff", "Report:\n## Summary\n- done", "Result: finished", "Validation:\n- lifecycle handoff regression", "Markdown is supported in this handoff and will be rendered for the user."} {
		if !strings.Contains(messages[2].Content, want) {
			t.Fatalf("final handoff content missing %q: %q", want, messages[2].Content)
		}
	}
	if strings.Contains(messages[1].Content, "Final checkpoint handoff") || strings.Contains(messages[1].Content, "Report:") || strings.Contains(messages[1].Content, "Result: finished") || strings.Contains(messages[1].Content, "Validation:") {
		t.Fatalf("final lifecycle message leaked handoff details: %q", messages[1].Content)
	}
	if len(appliedMutations) <= 7 || appliedMutations[7].Kind != sessionruntime.SessionMutationAppendMessage || appliedMutations[7].Message == nil || appliedMutations[7].Message.Metadata["source"] != PlanExecutionFinalHandoffMessageSource {
		t.Fatalf("final handoff mutation ordering = %#v", appliedMutations)
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
	assertLifecycleMessage(3, "resolve_blocked_checkpoint", "run_checkpoint_with_fresh_context", 9, "Blocked checkpoint resolved; starting next checkpoint — Automatic mode", "Resolved: Checkpoint a — Blocked", "Checkpoint: Checkpoint b — Next", "Context: Starting the next checkpoint with fresh context.")

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
	if payload.Action != "start_checkpoint" || payload.CheckpointID != "cp-1" || payload.NextAction != "run_checkpoint_with_fresh_context" {
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
	if completePayload.Action != "complete_checkpoint" || completePayload.NextCheckpointID != "cp-2" || completePayload.NextAction != "run_checkpoint_with_fresh_context" {
		t.Fatalf("complete payload action=%q next=%q next_action=%q raw=%s", completePayload.Action, completePayload.NextCheckpointID, completePayload.NextAction, raw)
	}
	if completePayload.Plan.Document == nil || completePayload.Plan.Document.ActiveCheckpointID != "cp-2" || completePayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("completed document = %#v", completePayload.Plan.Document)
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
	if payload.Action != "approve_and_start" || payload.NextAction != "run_checkpoint_with_fresh_context" || payload.CheckpointID != "cp-1" {
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

	explicitPayload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"request_new_plan","title":"Replacement Plan","plan":"# Replacement Plan","execution_granularity":"run_through","continue_automatically":false}`})
	if err != nil {
		t.Fatalf("build explicit permission payload: %v", err)
	}
	if !needsApproval {
		t.Fatalf("explicit request_new_plan should require approval")
	}
	explicit := explicitPayload.ApprovedArguments
	if explicit["execution_granularity"] != "run_through" || explicit["continuation_policy"] != sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint || explicit["continue_automatically"] != false {
		t.Fatalf("explicit execution controls were not preserved: %#v", explicit)
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
	if payload.Action != "request_new_plan" || payload.NextAction != "run_checkpoint_with_fresh_context" {
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
	if followupPayload.Action != "request_followup_checkpoint" || followupPayload.NextAction != "run_checkpoint_with_fresh_context" || followupPayload.CheckpointID == "" {
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
	if payload.Action != "request_new_plan" || payload.NextAction != "run_checkpoint_with_fresh_context" || payload.CheckpointID != "cp-1" {
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
	if payload.NextAction != "run_checkpoint_with_fresh_context" || payload.CheckpointID != "cp-proposed" || payload.ExecutionSummary.NextCheckpointID != "cp-proposed" {
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

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-2","report":"done"}`, "")
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

func TestExecutePlanManageApproveAndStartRunThroughCollapsesToSingleCheckpoint(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-single", "Single Plan", "# Single", "draft", "pending", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Objective: "Build model", Tasks: []string{"Task A"}, AcceptanceCriteria: []string{"A done"}},
			{ID: "cp-2", Title: "API", Objective: "Build API", Tasks: []string{"Task B"}, AcceptanceCriteria: []string{"B done"}},
		},
	}})
	if err != nil {
		t.Fatalf("save single plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"approve_and_start","execution_granularity":"run_through"}`, "")
	if err != nil {
		t.Fatalf("approve run through: %v output=%s", err, raw)
	}
	var payload struct {
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode run-through payload: %v", err)
	}
	doc := payload.Plan.Document
	if doc == nil || doc.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeSingleRun || len(doc.Checkpoints) != 1 || payload.CheckpointID != "plan-run" {
		t.Fatalf("run-through document = %#v raw=%s", doc, raw)
	}
	if doc.ActiveCheckpointID != "plan-run" || doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending || len(doc.Checkpoints[0].Tasks) == 0 || len(doc.Checkpoints[0].AcceptanceCriteria) != 2 {
		t.Fatalf("collapsed checkpoint = %#v", doc.Checkpoints[0])
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
	if restartPayload.NextAction != "run_checkpoint_with_fresh_context" || restartPayload.CheckpointID != "cp-2" || restarted.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || len(restarted.Checkpoints[1].Attempts) != 0 || restarted.Checkpoints[2].Status != sessionruntime.PlanCheckpointStatusCompleted {
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

	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-plan", Name: "plan_manage", Arguments: `{"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"done"}`})
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
