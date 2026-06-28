package run

import (
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) appendPlanExecutionLifecycleSystemMessage(sessionID, action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	message, ok := BuildPlanExecutionLifecycleSystemMessage(PlanExecutionLifecycleMessageInput{Action: action, Plan: plan, Payload: payload})
	if !ok {
		return nil
	}
	appendInput := runAppendMessageInput{
		SessionID:            sessionID,
		Role:                 "system",
		Content:              message.Content,
		Metadata:             message.Metadata,
		RunID:                planLifecycleMessageRunID(plan),
		Step:                 int(plan.Version),
		LogicalKey:           planLifecycleMessageLogicalKey(action, plan, payload),
		ActivePlan:           &plan,
		ApplySessionMutation: applySessionMutation,
	}
	if applySessionMutation != nil {
		_, _, mutation, err := s.appendRunMessageWithMutation(appendInput)
		if err != nil {
			return fmt.Errorf("append plan execution lifecycle system message: %w", err)
		}
		if mutation == nil || mutation.Message == nil || mutation.RealtimeOutbox == nil || mutation.RealtimeOutbox.EndpointSeq == 0 {
			return errors.New("plan execution lifecycle message mutation did not return committed realtime outbox")
		}
		return nil
	}
	_, _, _, err := s.appendRunMessage(appendInput)
	if err != nil {
		return fmt.Errorf("append plan execution lifecycle system message: %w", err)
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
	parts := []string{"system", "plan_execution", strings.TrimSpace(action), fmt.Sprintf("v%d", plan.Version)}
	if checkpointID := stringFromPlanPayload(payload, "checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if checkpointID := stringFromPlanPayload(payload, "next_checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if plan.Document != nil && plan.Document.ActiveCheckpointID != "" {
		parts = append(parts, strings.TrimSpace(plan.Document.ActiveCheckpointID))
	}
	return strings.Join(parts, ":")
}
