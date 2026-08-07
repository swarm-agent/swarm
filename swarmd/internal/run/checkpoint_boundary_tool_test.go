package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestPlanManageLegacyFollowupActionsAreDisabled(t *testing.T) {
	runSvc, _, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	for _, action := range []string{"request_followup_checkpoint", "request-followup-checkpoint", "followup_checkpoint", "request_changes"} {
		args := `{"action":"` + action + `","change_request":"legacy"}`
		if _, err := runSvc.executePlanManageTool("unused-session", args, ""); err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "transition_checkpoint_boundary") {
			t.Fatalf("legacy follow-up action %q error = %v", action, err)
		}
	}
}

func TestCheckpointBoundaryToolPayloadContinuesCurrentRun(t *testing.T) {
	payload := map[string]any{
		"action":            "transition_checkpoint_boundary",
		"next_action":       "continue_current_run",
		"run_id":            "run-current",
		"context_preserved": true,
	}
	raw, err := marshalPlanManagePayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(raw, `"next_action":"continue_current_run"`) || !strings.Contains(raw, `"run_id":"run-current"`) || strings.Contains(raw, `"next_run_id"`) || strings.Contains(raw, `"parent_turn_terminal"`) {
		t.Fatalf("current-run payload = %s", raw)
	}
	if providerManagedToolRequiresTurnRestart(tool.Call{Name: "plan_manage"}, tool.Result{Output: raw}) {
		t.Fatal("checkpoint assignment restarted the current provider turn")
	}
}

func TestProviderManagedCheckpointBoundaryOwnsCycleThroughFinalHandoff(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-cycle", "Plan cycle", "# Plan cycle", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:            pebblestore.SessionPlanInfo{Goal: "Exercise one complete post-handoff cycle."},
		ExecutionOrigin: sessionruntime.PlanExecutionOriginAutoSession,
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1", LastAttemptID: "cp-1:attempt-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Original", Objective: "Complete original work.", Status: sessionruntime.PlanCheckpointStatusCompleted,
			AcceptanceCriteria: []string{"Original work is complete."},
			Handoff:            &pebblestore.SessionPlanCheckpointHandoff{Overview: "Original work completed."},
		}},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save completed plan: %v", err)
	}

	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%t err=%v", ok, err)
	}
	session.UserID = "user-cycle"
	session.AccountScopeID = "account-cycle"
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		if input.UserID == "" {
			input.UserID = session.UserID
		}
		if input.AccountScopeID == "" {
			input.AccountScopeID = session.AccountScopeID
		}
		return sessionSvc.ApplySessionMutation(input)
	}
	if _, err := applyMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, ClientRequestID: "cycle-owner", IdempotencyKey: "cycle-owner", PayloadHash: "cycle-owner", RequestHash: "cycle-owner",
		Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &session,
	}); err != nil {
		t.Fatalf("set session ownership: %v", err)
	}
	const runID = "run-cycle"
	now := time.Now().UnixMilli()
	if _, err := applyMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, ClientRequestID: "cycle-run-pending", IdempotencyKey: "cycle-run-pending", PayloadHash: "cycle-run-pending", RequestHash: "cycle-run-pending",
		Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor}, NowUnixMs: now,
	}); err != nil {
		t.Fatalf("record pending current run: %v", err)
	}
	if _, err := applyMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, ClientRequestID: "cycle-run-running", IdempotencyKey: "cycle-run-running", PayloadHash: "cycle-run-running", RequestHash: "cycle-run-running",
		Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning}, NowUnixMs: now + 1,
	}); err != nil {
		t.Fatalf("mark current run running: %v", err)
	}
	if _, err := sessionSvc.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "cycle-epoch", PayloadHash: "cycle-epoch", Reason: "post_handoff_user_message", RunID: runID, SkipRunIntent: true, NowUnixMs: now + 1,
	}); err != nil {
		t.Fatalf("begin current execution epoch: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
		SessionID: sessionID, PermissionSessionID: sessionID, RunID: runID, SourceMessageID: "message-cycle", Step: 1,
		SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true, ApplySessionMutation: applyMutation,
	})
	boundaryArgs := `{"action":"transition_checkpoint_boundary","plan_id":"` + plan.ID + `","change_request":"exercise the complete cycle","checkpoint_title":"Complete cycle","tasks":["Complete the cycle"],"acceptance_criteria":["The cycle reaches a final handoff"]}`
	boundaryResult, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-boundary", Name: "plan_manage", Arguments: boundaryArgs})
	if err != nil || boundaryResult.Error != "" {
		t.Fatalf("transition boundary: result=%#v err=%v", boundaryResult, err)
	}
	if boundaryResult.RestartTurn {
		t.Fatalf("boundary restarted provider turn: %#v", boundaryResult)
	}
	var boundaryPayload map[string]any
	if err := json.Unmarshal([]byte(boundaryResult.Output), &boundaryPayload); err != nil {
		t.Fatalf("decode boundary output: %v", err)
	}
	if boundaryPayload["next_action"] != "continue_current_run" || boundaryPayload["run_id"] != runID || boundaryPayload["checkpoint_id"] != "followup-1" || boundaryPayload["attempt_id"] != "followup-1:attempt-1" || boundaryPayload["context_preserved"] != true {
		t.Fatalf("boundary payload lost current-run ownership: %#v", boundaryPayload)
	}
	for _, forbidden := range []string{"next_run_id", "run_request", "parent_turn_terminal"} {
		if _, exists := boundaryPayload[forbidden]; exists {
			t.Fatalf("boundary payload unexpectedly contains %q: %#v", forbidden, boundaryPayload)
		}
	}
	intents, err := sessionSvc.ListSessionRunIntents(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list run intents after boundary: %v", err)
	}
	if len(intents) != 1 || intents[0].RunID != runID || intents[0].CheckpointID != "followup-1" || intents[0].AttemptID != "followup-1:attempt-1" || intents[0].Status != sessionruntime.RunIntentRunning {
		t.Fatalf("boundary allocated or lost run ownership: %#v", intents)
	}
	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || active.Document == nil || active.Document.ExecutionState == nil {
		t.Fatalf("get plan after boundary: ok=%t err=%v plan=%#v", ok, err, active)
	}
	if active.Document.ExecutionState.CurrentRunID != runID || active.Document.ExecutionState.ActiveAttemptID != "followup-1:attempt-1" || active.Document.Checkpoints[1].RunID != runID || active.Document.Checkpoints[1].AttemptID != "followup-1:attempt-1" {
		t.Fatalf("plan/run ownership was not assigned atomically: %#v", active.Document)
	}

	terminalArgs := `{"action":"complete_checkpoint","checkpoint_id":"followup-1","report":"cycle complete","result":"final handoff recorded","changed_files":[],"validation":["integration cycle"],"handoff_overview":"The complete post-handoff cycle stayed on one run.","recommendation":{"decision":"ship","action":"review the cycle","reason":"The lifecycle completed on one run.","action_state":"ready"}}`
	terminalResult, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-terminal", Name: "plan_manage", Arguments: terminalArgs})
	if err != nil || terminalResult.Error != "" {
		t.Fatalf("complete checkpoint: result=%#v err=%v", terminalResult, err)
	}
	if !terminalResult.RestartTurn {
		t.Fatalf("final handoff did not terminate provider turn: %#v", terminalResult)
	}
	intents, err = sessionSvc.ListSessionRunIntents(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list run intents after terminal handoff: %v", err)
	}
	if len(intents) != 1 || intents[0].RunID != runID || intents[0].CheckpointID != "followup-1" || intents[0].AttemptID != "followup-1:attempt-1" {
		t.Fatalf("terminalization replaced or lost run ownership: %#v", intents)
	}
	active, ok, err = sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || active.Document == nil || active.Document.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusCompleted || active.Document.Checkpoints[1].RunID != runID || active.Document.Checkpoints[1].AttemptID != "followup-1:attempt-1" || active.Document.Checkpoints[1].Handoff == nil {
		t.Fatalf("final handoff lost checkpoint ownership: ok=%t err=%v plan=%#v", ok, err, active)
	}

	replay, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-boundary-retry", Name: "plan_manage", Arguments: boundaryArgs})
	if err != nil || replay.Error != "" || replay.RestartTurn {
		t.Fatalf("replay boundary: result=%#v err=%v", replay, err)
	}
	var replayPayload map[string]any
	if err := json.Unmarshal([]byte(replay.Output), &replayPayload); err != nil {
		t.Fatalf("decode replay output: %v", err)
	}
	if replayPayload["replayed"] != true || replayPayload["run_id"] != runID || replayPayload["checkpoint_id"] != "followup-1" {
		t.Fatalf("boundary replay changed ownership: %#v", replayPayload)
	}
	intents, err = sessionSvc.ListSessionRunIntents(sessionID, 0, 20)
	if err != nil || len(intents) != 1 {
		t.Fatalf("boundary replay allocated another run: intents=%#v err=%v", intents, err)
	}
}
