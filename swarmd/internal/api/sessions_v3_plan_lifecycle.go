package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) appendSessionsV3PlanExecutionLifecycleSystemMessage(principal identity.Principal, sessionID, action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) error {
	message, ok := runruntime.BuildPlanExecutionLifecycleSystemMessage(runruntime.PlanExecutionLifecycleMessageInput{Action: action, Plan: plan, Payload: payload})
	if !ok {
		return nil
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session %q not found", sessionID)
	}
	if strings.TrimSpace(principal.UserID) == "" {
		principal.UserID = strings.TrimSpace(session.UserID)
	}
	if strings.TrimSpace(principal.AccountScopeID) == "" {
		principal.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
	}
	now := time.Now().UnixMilli()
	logicalKey := sessionsV3PlanLifecycleMessageLogicalKey(action, plan, payload)
	runID := sessionsV3PlanLifecycleMessageRunID(plan)
	snapshot := pebblestore.MessageSnapshot{
		ID:             sessionsV3PlanLifecycleMessageID(sessionID, runID, logicalKey),
		SessionID:      sessionID,
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		Role:           "system",
		Content:        message.Content,
		Metadata:       message.Metadata,
		CreatedAt:      now,
	}
	payloadHash, err := sessionsV3PlanLifecycleMessagePayloadHash(snapshot, runID, logicalKey, int(plan.Version))
	if err != nil {
		return err
	}
	eventPayload, err := sessionsV3PlanLifecycleMessageEventPayload(sessionID, snapshot, plan)
	if err != nil {
		return err
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		ClientRequestID: "plan-lifecycle-message:" + runID + ":" + logicalKey,
		IdempotencyKey:  "plan-lifecycle-message:" + runID + ":" + logicalKey,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		EventPayload:    eventPayload,
		Message:         &snapshot,
		NowUnixMs:       now,
	})
	if err != nil {
		return err
	}
	if result.Message == nil || result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
		return fmt.Errorf("plan execution lifecycle message mutation did not return committed realtime outbox")
	}
	return nil
}

func sessionsV3PlanLifecycleMessageRunID(plan pebblestore.SessionPlanSnapshot) string {
	planID := strings.TrimSpace(plan.ID)
	if planID == "" {
		return "plan-execution"
	}
	return "plan-execution:" + planID
}

func sessionsV3PlanLifecycleMessageLogicalKey(action string, plan pebblestore.SessionPlanSnapshot, payload map[string]any) string {
	parts := []string{"system", "plan_execution", strings.TrimSpace(action), fmt.Sprintf("v%d", plan.Version)}
	if checkpointID := sessionsV3PlanLifecycleStringPayload(payload, "checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if checkpointID := sessionsV3PlanLifecycleStringPayload(payload, "next_checkpoint_id"); checkpointID != "" {
		parts = append(parts, checkpointID)
	} else if plan.Document != nil && plan.Document.ActiveCheckpointID != "" {
		parts = append(parts, strings.TrimSpace(plan.Document.ActiveCheckpointID))
	}
	return strings.Join(parts, ":")
}

func sessionsV3PlanLifecycleStringPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func sessionsV3PlanLifecycleMessageEventPayload(sessionID string, message pebblestore.MessageSnapshot, plan pebblestore.SessionPlanSnapshot) (json.RawMessage, error) {
	message = sanitizeSessionsV3PlanLifecycleMessageForEvent(message)
	payload := map[string]any{
		"session_id":      strings.TrimSpace(sessionID),
		"kind":            sessionruntime.SessionMutationAppendMessage,
		"message":         message,
		"message_id":      strings.TrimSpace(message.ID),
		"role":            strings.TrimSpace(message.Role),
		"has_active_plan": true,
		"active_plan":     plan,
	}
	return json.Marshal(payload)
}

func sanitizeSessionsV3PlanLifecycleMessageForEvent(message pebblestore.MessageSnapshot) pebblestore.MessageSnapshot {
	message.ID = strings.TrimSpace(message.ID)
	message.SessionID = strings.TrimSpace(message.SessionID)
	message.UserID = strings.TrimSpace(message.UserID)
	message.AccountScopeID = strings.TrimSpace(message.AccountScopeID)
	message.Role = strings.TrimSpace(message.Role)
	message.Content = strings.TrimSpace(message.Content)
	if len(message.Metadata) > 0 {
		metadata := make(map[string]any, len(message.Metadata))
		for key, value := range message.Metadata {
			metadata[key] = value
		}
		message.Metadata = metadata
	}
	return message
}

func sessionsV3PlanLifecycleMessageID(sessionID, runID, logicalKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(logicalKey) + "\x00system"))
	return "v3msg_plan_" + hex.EncodeToString(sum[:16])
}

func sessionsV3PlanLifecycleMessagePayloadHash(message pebblestore.MessageSnapshot, runID, logicalKey string, step int) (string, error) {
	payload := map[string]any{
		"session_id":  strings.TrimSpace(message.SessionID),
		"run_id":      strings.TrimSpace(runID),
		"logical_key": strings.TrimSpace(logicalKey),
		"step":        step,
		"role":        "system",
		"content":     strings.TrimSpace(message.Content),
	}
	if len(message.Metadata) > 0 {
		payload["metadata"] = message.Metadata
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal plan lifecycle message payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
