package run

import (
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) appendPlanExecutionLifecycleSystemMessage(sessionID, action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	handoffInput := PlanExecutionLifecycleMessageInput{Action: action, Plan: plan, Payload: payload}
	if reviewHandoff, ok := BuildFinalPlanExecutionHandoffSystemMessage(handoffInput); ok && strings.TrimSpace(action) == "mark_needs_review" {
		return s.appendFinalPlanExecutionSystemMessage(sessionID, plan, reviewHandoff, planFinalHandoffMessageLogicalKey(action, plan, payload), "plan execution review handoff")
	}
	if blockedHandoff, ok := BuildBlockedPlanExecutionHandoffSystemMessage(handoffInput); ok {
		return s.appendPlanExecutionSystemMessage(sessionID, plan, blockedHandoff, planBlockedHandoffMessageLogicalKey(action, plan, payload), "plan execution blocked handoff", applySessionMutation)
	}
	checkpointHandoff, hasCheckpointHandoff := BuildPlanExecutionCheckpointHandoffSystemMessage(handoffInput)
	if hasCheckpointHandoff && checkpointHandoff.Metadata["fresh_context"] == true {
		return s.appendPlanExecutionSystemMessage(sessionID, plan, checkpointHandoff, planCheckpointHandoffMessageLogicalKey(action, plan, payload), "plan execution checkpoint handoff", applySessionMutation)
	}
	message, ok := BuildPlanExecutionLifecycleSystemMessage(handoffInput)
	if !ok {
		return nil
	}
	if err := s.appendPlanExecutionSystemMessage(sessionID, plan, message, planLifecycleMessageLogicalKey(action, plan, payload), "plan execution lifecycle", applySessionMutation); err != nil {
		return err
	}
	if hasCheckpointHandoff {
		return s.appendPlanExecutionSystemMessage(sessionID, plan, checkpointHandoff, planCheckpointHandoffMessageLogicalKey(action, plan, payload), "plan execution checkpoint handoff", applySessionMutation)
	}
	handoffMessage, ok := BuildFinalPlanExecutionHandoffSystemMessage(handoffInput)
	logicalKey := planFinalHandoffMessageLogicalKey
	label := "plan execution final handoff"
	if !ok {
		return nil
	}
	if err := s.appendFinalPlanExecutionSystemMessage(sessionID, plan, handoffMessage, logicalKey(action, plan, payload), label); err != nil {
		return err
	}
	return nil
}

func (s *Service) appendFinalPlanExecutionSystemMessage(sessionID string, plan pebblestore.SessionPlanSnapshot, message PlanExecutionLifecycleMessage, logicalKey, label string) error {
	if s == nil || s.sessions == nil {
		return fmt.Errorf("append %s system message: session service is not configured", label)
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("append %s system message: %w", label, err)
	}
	if !ok {
		return fmt.Errorf("append %s system message: session %q not found", label, sessionID)
	}
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return fmt.Errorf("append %s system message: message content is required", label)
	}
	runID := planLifecycleMessageRunID(plan)
	logicalKey = strings.TrimSpace(logicalKey)
	handoff := pebblestore.MessageSnapshot{
		ID:             runMessageV3ID(sessionID, runID, logicalKey, "system"),
		SessionID:      sessionID,
		UserID:         strings.TrimSpace(session.UserID),
		AccountScopeID: strings.TrimSpace(session.AccountScopeID),
		Role:           "system",
		Content:        content,
		Metadata:       cloneGenericMap(message.Metadata),
	}
	payloadHash, err := runMessageV3PayloadHash(sessionID, runID, logicalKey, int(plan.Version), "system", content, handoff.Metadata)
	if err != nil {
		return fmt.Errorf("append %s system message: %w", label, err)
	}
	checkpointID := ""
	attemptID := ""
	if plan.Document != nil {
		checkpointID = strings.TrimSpace(plan.Document.ActiveCheckpointID)
		if plan.Document.ExecutionState != nil {
			if checkpointID == "" {
				checkpointID = strings.TrimSpace(plan.Document.ExecutionState.LastCheckpointID)
			}
			attemptID = strings.TrimSpace(plan.Document.ExecutionState.LastAttemptID)
		}
	}
	clientRequestID := runMessageV3ClientRequestID(sessionID, runID, logicalKey)
	result, err := s.sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID:           sessionID,
		UserID:              session.UserID,
		AccountScopeID:      session.AccountScopeID,
		ClientRequestID:     clientRequestID,
		PayloadHash:         payloadHash,
		Reason:              "final_plan_handoff",
		PlanID:              strings.TrimSpace(plan.ID),
		CheckpointID:        checkpointID,
		AttemptID:           attemptID,
		SourceMessageID:     handoff.ID,
		FinalHandoffMessage: &handoff,
		SkipRunIntent:       true,
	})
	if err != nil {
		return fmt.Errorf("append %s system message: %w", label, err)
	}
	if result.FinalHandoffMessage == nil || result.FinalHandoffEvent == nil || result.FinalHandoffOutbox == nil || result.FinalHandoffOutbox.EndpointSeq == 0 || result.Epoch.ParentEpochID != result.Predecessor.EpochID {
		return fmt.Errorf("%s mutation did not return committed handoff and successor epoch", label)
	}
	return nil
}

func (s *Service) appendPlanExecutionSystemMessage(sessionID string, plan pebblestore.SessionPlanSnapshot, message PlanExecutionLifecycleMessage, logicalKey, label string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	// Plan state is delivered by the canonical session.plan.saved realtime
	// outbox row. This system message is only transcript/history context; do
	// not piggyback active_plan on its session.message.appended payload.
	appendInput := runAppendMessageInput{
		SessionID:            sessionID,
		Role:                 "system",
		Content:              message.Content,
		Metadata:             message.Metadata,
		RunID:                planLifecycleMessageRunID(plan),
		Step:                 int(plan.Version),
		LogicalKey:           logicalKey,
		ApplySessionMutation: applySessionMutation,
	}
	if applySessionMutation != nil {
		_, _, mutation, err := s.appendRunMessageWithMutation(appendInput)
		if err != nil {
			return fmt.Errorf("append %s system message: %w", label, err)
		}
		if mutation == nil || mutation.Message == nil || mutation.RealtimeOutbox == nil || mutation.RealtimeOutbox.EndpointSeq == 0 {
			return fmt.Errorf("%s message mutation did not return committed realtime outbox", label)
		}
		return nil
	}
	_, _, _, err := s.appendRunMessage(appendInput)
	if err != nil {
		return fmt.Errorf("append %s system message: %w", label, err)
	}
	return nil
}

func planLifecycleMessageRunID(plan pebblestore.SessionPlanSnapshot) string {
	planID := strings.TrimSpace(plan.ID)
	if planID == "" {
		return "plan-execution"
	}
	return "plan-execution:" + planID
}

func planLifecycleMessageLogicalKey(action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) string {
	return strings.Join(planExecutionMessageLogicalKeyParts("plan_execution", action, plan, payload), ":")
}

func planCheckpointHandoffMessageLogicalKey(action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) string {
	return strings.Join(planExecutionMessageLogicalKeyParts("plan_checkpoint_handoff", action, plan, payload), ":")
}

func planFinalHandoffMessageLogicalKey(action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) string {
	return strings.Join(planExecutionMessageLogicalKeyParts("plan_final_handoff", action, plan, payload), ":")
}

func planBlockedHandoffMessageLogicalKey(action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) string {
	return strings.Join(planExecutionMessageLogicalKeyParts("plan_blocked_handoff", action, plan, payload), ":")
}

func planExecutionMessageLogicalKeyParts(kind, action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) []string {
	parts := []string{"system", kind, strings.TrimSpace(action), fmt.Sprintf("v%d", plan.Version)}
	if checkpointID := stringFromPlanPayload(payload, "checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if checkpointID := stringFromPlanPayload(payload, "next_checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if plan.Document != nil && plan.Document.ActiveCheckpointID != "" {
		parts = append(parts, strings.TrimSpace(plan.Document.ActiveCheckpointID))
	}
	return parts
}
