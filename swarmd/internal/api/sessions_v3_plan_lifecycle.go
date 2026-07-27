package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) publishCommittedPlanSaved(plan pebblestore.SessionPlanSnapshot, event *pebblestore.EventEnvelope) error {
	if strings.TrimSpace(plan.SessionID) == "" {
		return fmt.Errorf("plan %q is missing session id", plan.ID)
	}
	if event == nil || event.EventType != "session.plan.saved" {
		return fmt.Errorf("plan %q did not return a committed session.plan.saved event", plan.ID)
	}
	clientRequestID := sessionsV3PlanSavedClientRequestID(*event, plan)
	payloadHash := sessionsV3PlanSavedPayloadHash(*event, plan)
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       strings.TrimSpace(plan.SessionID),
		UserID:          strings.TrimSpace(plan.UserID),
		AccountScopeID:  strings.TrimSpace(plan.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationSavePlan,
		EventType:       "session.plan.saved",
		EventPayload:    append(json.RawMessage(nil), event.Payload...),
		NowUnixMs:       event.TsUnixMs,
	})
	if err != nil {
		return err
	}
	if result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
		return fmt.Errorf("session.plan.saved mutation did not return committed realtime outbox")
	}
	return nil
}

func sessionsV3PlanSavedClientRequestID(event pebblestore.EventEnvelope, plan pebblestore.SessionPlanSnapshot) string {
	if event.GlobalSeq > 0 {
		return fmt.Sprintf("plan-saved:%s:%d", strings.TrimSpace(plan.SessionID), event.GlobalSeq)
	}
	return fmt.Sprintf("plan-saved:%s:%s:v%d", strings.TrimSpace(plan.SessionID), strings.TrimSpace(plan.ID), plan.Version)
}

func sessionsV3PlanSavedPayloadHash(event pebblestore.EventEnvelope, plan pebblestore.SessionPlanSnapshot) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plan.SessionID) + "\x00" + strings.TrimSpace(plan.ID) + "\x00" + fmt.Sprintf("%d", plan.Version) + "\x00" + string(event.Payload)))
	return hex.EncodeToString(sum[:])
}

func (s *Server) publishCommittedModeUpdated(result sessionruntime.PlanLifecycleResult) error {
	if result.ModeEvent == nil {
		return nil
	}
	if strings.TrimSpace(result.Session.ID) == "" {
		return errors.New("mode update result is missing session id")
	}
	if result.ModeEvent.EventType != "session.mode.updated" {
		return fmt.Errorf("session %q did not return a committed session.mode.updated event", result.Session.ID)
	}
	payloadHash := sessionsV3PlanLifecycleModePayloadHash(*result.ModeEvent, result.Session)
	clientRequestID := sessionsV3PlanLifecycleModeClientRequestID(*result.ModeEvent, result.Session)
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       strings.TrimSpace(result.Session.ID),
		UserID:          strings.TrimSpace(result.Session.UserID),
		AccountScopeID:  strings.TrimSpace(result.Session.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMode,
		EventType:       "session.mode.updated",
		EventPayload:    append(json.RawMessage(nil), result.ModeEvent.Payload...),
		Session:         &result.Session,
		NowUnixMs:       result.ModeEvent.TsUnixMs,
	})
	if err != nil {
		return err
	}
	if mutation.RealtimeOutbox == nil || mutation.RealtimeOutbox.EndpointSeq == 0 {
		return errors.New("session.mode.updated mutation did not return committed realtime outbox")
	}
	return nil
}

func sessionsV3PlanLifecycleModeClientRequestID(event pebblestore.EventEnvelope, session pebblestore.SessionSnapshot) string {
	if event.GlobalSeq > 0 {
		return fmt.Sprintf("plan-lifecycle-mode:%s:%d", strings.TrimSpace(session.ID), event.GlobalSeq)
	}
	return fmt.Sprintf("plan-lifecycle-mode:%s:%s:%d", strings.TrimSpace(session.ID), strings.TrimSpace(session.Mode), event.TsUnixMs)
}

func sessionsV3PlanLifecycleModePayloadHash(event pebblestore.EventEnvelope, session pebblestore.SessionSnapshot) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(session.ID) + "\x00plan_lifecycle_mode\x00" + strings.TrimSpace(session.Mode) + "\x00" + string(event.Payload)))
	return hex.EncodeToString(sum[:])
}

func (s *Server) publishPlanLifecycleResult(result sessionruntime.PlanLifecycleResult) error {
	if result.PlanEvent != nil {
		if err := s.publishCommittedPlanSaved(result.Plan, result.PlanEvent); err != nil {
			return err
		}
	}
	if result.ModeEvent != nil {
		if err := s.publishCommittedModeUpdated(result); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) publishPlanLifecycleSystemMessage(principal identity.Principal, sessionID, transition string, result sessionruntime.PlanLifecycleResult) error {
	message, ok := runruntime.BuildPlanExecutionLifecycleSystemMessage(runruntime.PlanExecutionLifecycleMessageInput{
		Action:  transition,
		Plan:    result.Plan,
		Payload: sessionsV3PlanLifecycleMessagePayload(result),
	})
	if !ok {
		return nil
	}
	if !principal.Valid() {
		principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: result.Session.UserID, AccountScopeID: result.Session.AccountScopeID}
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = strings.TrimSpace(result.Plan.SessionID)
	}
	clientRequestID := sessionsV3PlanLifecycleMessageClientRequestID(sessionID, transition, result.Plan, message.Metadata)
	payloadHash := sessionsV3PlanLifecycleMessagePayloadHash(sessionID, transition, message.Content, message.Metadata)
	now := time.Now().UnixMilli()
	mutation, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       strings.TrimSpace(sessionID),
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message: &pebblestore.MessageSnapshot{
			Role:      "system",
			Content:   message.Content,
			Metadata:  message.Metadata,
			CreatedAt: now,
		},
		NowUnixMs: now,
	})
	if err != nil {
		return err
	}
	if mutation.RealtimeOutbox == nil || mutation.RealtimeOutbox.EndpointSeq == 0 {
		return errors.New("plan execution lifecycle message mutation did not return committed realtime outbox")
	}
	return nil
}

func sessionsV3PlanLifecycleMessagePayload(result sessionruntime.PlanLifecycleResult) map[string]any {
	payload := map[string]any{"action": strings.TrimSpace(result.Action)}
	if strings.TrimSpace(result.Action) == "resolve_blocked_checkpoint" && result.CheckpointID != "" {
		payload["resumed_checkpoint_id"] = strings.TrimSpace(result.CheckpointID)
	}
	if result.CheckpointID != "" {
		payload["checkpoint_id"] = strings.TrimSpace(result.CheckpointID)
	} else if result.Plan.Document != nil {
		if checkpointID := strings.TrimSpace(result.Plan.Document.ActiveCheckpointID); checkpointID != "" {
			payload["checkpoint_id"] = checkpointID
		}
	}
	if result.Summary.NextCheckpointID != "" {
		payload["next_checkpoint_id"] = strings.TrimSpace(result.Summary.NextCheckpointID)
	}
	if result.Summary.PlanComplete {
		payload["next_action"] = "plan_complete"
	} else if result.Summary.ReviewRequired {
		payload["next_action"] = "await_review"
	} else if result.Summary.Blocked || result.Summary.Failed {
		payload["next_action"] = "stopped"
	} else if result.Summary.NextCheckpointID != "" {
		switch strings.TrimSpace(result.Action) {
		case "resume_checkpoint":
			payload["next_action"] = "resume_checkpoint_context"
		case "start_checkpoint", "continue_checkpoint", "restart_checkpoint", "rewind_to_checkpoint":
			payload["next_action"] = "run_checkpoint_with_fresh_context"
		case "resolve_blocked_checkpoint":
			if result.Summary.NextCheckpointStatus == sessionruntime.PlanCheckpointStatusInProgress && result.Summary.NextCheckpointID == result.CheckpointID {
				payload["next_action"] = "run_checkpoint_with_fresh_context"
			} else {
				payload["next_action"] = "blocker_resolved_resume_pending"
			}
		case "accept_checkpoint":
			payload["next_action"] = "continue_checkpoint"
		default:
			if result.Summary.AutoAdvanceAllowed {
				payload["next_action"] = "run_checkpoint_with_fresh_context"
			} else {
				payload["next_action"] = "continue_checkpoint"
			}
		}
	}
	return payload
}

func sessionsV3PlanLifecycleMessageClientRequestID(sessionID, transition string, plan pebblestore.SessionPlanSnapshot, metadata map[string]any) string {
	checkpointID := ""
	if metadata != nil {
		checkpointID = strings.TrimSpace(fmt.Sprint(metadata["checkpoint_id"]))
	}
	return strings.Join([]string{
		"plan-lifecycle-message",
		strings.TrimSpace(sessionID),
		strings.TrimSpace(transition),
		fmt.Sprintf("v%d", plan.Version),
		checkpointID,
	}, ":")
}

func sessionsV3PlanLifecycleMessagePayloadHash(sessionID, transition, content string, metadata map[string]any) string {
	rawMetadata, _ := json.Marshal(metadata)
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(transition) + "\x00" + content + "\x00" + string(rawMetadata)))
	return hex.EncodeToString(sum[:])
}
