package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

var nativePlanRuntimeActions = map[string]string{
	"activate_plan":       sessionruntime.PlanRuntimeActionActivate,
	"start_checkpoint":    sessionruntime.PlanRuntimeActionStartCheckpoint,
	"focus_subtask":       sessionruntime.PlanRuntimeActionFocusSubtask,
	"complete_subtask":    sessionruntime.PlanRuntimeActionCompleteSubtasks,
	"complete_checkpoint": sessionruntime.PlanRuntimeActionCheckpointOutcome,
	"checkpoint_outcome":  sessionruntime.PlanRuntimeActionCheckpointOutcome,
	"mark_needs_review":   sessionruntime.PlanRuntimeActionCheckpointOutcome,
	"mark_blocked":        sessionruntime.PlanRuntimeActionCheckpointOutcome,
	"mark_failed":         sessionruntime.PlanRuntimeActionCheckpointOutcome,
}

func isNativePlanRuntimeAction(action string) bool {
	_, ok := nativePlanRuntimeActions[strings.ToLower(strings.TrimSpace(action))]
	return ok
}

func legacyPlanExecutionAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "continue_checkpoint", "update_execution_state", "set_active_checkpoint":
		return true
	default:
		return false
	}
}

// executeNativePlanRuntimeAction is the sole plan_manage execution-progress
// bridge to the V3-native command service. Definition-authoring actions stay on
// their separate APIs and cannot reach this function.
func (s *Service) executeNativePlanRuntimeAction(sessionID, action string, args map[string]any, lifecycleRun planLifecycleRunContext) (string, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return "", errors.New("plan runtime command service is not configured")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	clientRequestID := strings.TrimSpace(mapString(args, "client_request_id"))
	if clientRequestID == "" {
		clientRequestID = strings.TrimSpace(mapString(args, "idempotency_key"))
	}
	if clientRequestID == "" {
		clientRequestID = strings.TrimSpace(firstNonEmptyString(mapString(args, "attempt_id"), lifecycleRun.RunID)) + ":" + action
	}
	clientRequestID = strings.Trim(clientRequestID, ":")
	if clientRequestID == "" {
		return "", errors.New("V3 plan runtime action requires client_request_id (or trusted attempt/run ownership); stale clients must refresh")
	}
	planID := strings.TrimSpace(firstNonEmptyString(mapString(args, "plan_id"), mapString(args, "id")))
	definitionRevision, err := planRuntimeUint64Arg(args, "definition_revision")
	if err != nil {
		return "", err
	}
	expectedSeq, err := planRuntimeOptionalExpectedSeq(args)
	if err != nil {
		return "", err
	}
	if _, supplied := args["expected_execution_seq"]; !supplied {
		summary, found, getErr := s.sessions.Store().GetPlanExecutionSummary(sessionID, planID)
		if getErr != nil {
			return "", getErr
		}
		if found {
			expectedSeq = summary.ExecutionSeq
		}
	}
	subtaskIDs := mapStringSlice(args, "subtask_ids")
	if id := strings.TrimSpace(mapString(args, "subtask_id")); id != "" && len(subtaskIDs) == 0 {
		subtaskIDs = []string{id}
	}
	runID := strings.TrimSpace(mapString(args, "run_id"))
	runSessionID := strings.TrimSpace(mapString(args, "run_session_id"))
	parentSessionID := strings.TrimSpace(mapString(args, "parent_session_id"))
	if action == "start_checkpoint" {
		// Run ownership supplied by the provider is untrusted. A provider-managed
		// checkpoint start must use the executor's trusted lifecycle context.
		if lifecycleRun.Inline {
			runID = strings.TrimSpace(lifecycleRun.RunID)
			runSessionID = strings.TrimSpace(lifecycleRun.RunSessionID)
			parentSessionID = strings.TrimSpace(lifecycleRun.ParentSessionID)
		}
	}
	input := sessionruntime.PlanRuntimeExecutionInput{
		Action: nativePlanRuntimeActions[action], SessionID: strings.TrimSpace(sessionID), PlanID: planID,
		AccountScopeID: strings.TrimSpace(session.AccountScopeID), ActorID: strings.TrimSpace(session.UserID),
		DefinitionRevision: definitionRevision, ExpectedExecutionSeq: expectedSeq, ClientRequestID: clientRequestID,
		CheckpointID: strings.TrimSpace(mapString(args, "checkpoint_id")), SubtaskIDs: subtaskIDs,
		CompleteCheckpoint: mapBool(args, "complete_checkpoint"), NextSubtaskID: strings.TrimSpace(mapString(args, "next_subtask_id")), AttemptID: strings.TrimSpace(mapString(args, "attempt_id")),
		Outcome:     planRuntimeOutcome(action, args),
		EvidenceRef: strings.TrimSpace(mapString(args, "evidence_ref")), NextAction: strings.TrimSpace(mapString(args, "next_action")),
		RunID: runID, EpochID: strings.TrimSpace(mapString(args, "epoch_id")), RunSessionID: runSessionID, ParentSessionID: parentSessionID,
	}
	receipt, err := sessionruntime.NewPlanRuntimeCommandService(s.sessions.Store()).Execute(input)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"tool": "plan_manage", "action": action, "status": "ok", "receipt": receipt,
		"plan_id": receipt.PlanID, "execution_seq": receipt.ExecutionSeq, "high_water_mark": receipt.HighWaterMark,
		"next_action": receipt.NextAction, "path_id": "tool.plan-runtime.v3", "details_truncated": false,
		"summary": fmt.Sprintf("applied %s at execution sequence %d", action, receipt.ExecutionSeq),
	}
	return marshalPlanManagePayload(payload)
}

func planRuntimeOutcome(action string, args map[string]any) string {
	if action == "complete_checkpoint" {
		return "completed"
	}
	switch action {
	case "mark_needs_review", "mark_blocked", "mark_failed":
		return strings.TrimPrefix(action, "mark_")
	default:
		return strings.TrimSpace(firstNonEmptyString(mapString(args, "outcome"), mapString(args, "result")))
	}
}

func planRuntimeOptionalExpectedSeq(args map[string]any) (uint64, error) {
	if _, ok := args["expected_execution_seq"]; !ok {
		return 0, nil
	}
	return planRuntimeUint64Arg(args, "expected_execution_seq")
}

func planRuntimeUint64Arg(args map[string]any, key string) (uint64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, fmt.Errorf("native plan runtime action requires %s", key)
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed != float64(uint64(typed)) {
			return 0, fmt.Errorf("%s must be an unsigned integer", key)
		}
		return uint64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("%s must be an unsigned integer", key)
		}
		return uint64(parsed), nil
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("%s must be an unsigned integer", key)
		}
		return uint64(typed), nil
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("%s must be an unsigned integer", key)
		}
		return uint64(typed), nil
	case uint64:
		return typed, nil
	default:
		return 0, fmt.Errorf("%s must be an unsigned integer", key)
	}
}
