package run

import (
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) appendPlanExecutionLifecycleSystemMessage(sessionID, action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	handoffInput := PlanExecutionLifecycleMessageInput{Action: action, Plan: plan, Payload: payload}
	if blockedHandoff, ok := BuildBlockedPlanExecutionHandoffSystemMessage(handoffInput); ok {
		return s.appendPlanExecutionSystemMessage(sessionID, plan, blockedHandoff, planBlockedHandoffMessageLogicalKey(action, plan, payload), "plan execution blocked handoff", applySessionMutation)
	}
	message, ok := BuildPlanExecutionLifecycleSystemMessage(handoffInput)
	if !ok {
		return nil
	}
	if err := s.appendPlanExecutionSystemMessage(sessionID, plan, message, planLifecycleMessageLogicalKey(action, plan, payload), "plan execution lifecycle", applySessionMutation); err != nil {
		return err
	}
	handoffMessage, ok := BuildFinalPlanExecutionHandoffSystemMessage(handoffInput)
	logicalKey := planFinalHandoffMessageLogicalKey
	label := "plan execution final handoff"
	if !ok {
		return nil
	}
	if err := s.appendPlanExecutionSystemMessage(sessionID, plan, handoffMessage, logicalKey(action, plan, payload), label, applySessionMutation); err != nil {
		return err
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
