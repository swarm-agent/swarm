package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) persistPlanSavedV3Mutation(plan pebblestore.SessionPlanSnapshot, event *pebblestore.EventEnvelope, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if applySessionMutation == nil {
		return nil
	}
	if strings.TrimSpace(plan.SessionID) == "" {
		return fmt.Errorf("plan %q is missing session id", plan.ID)
	}
	if event == nil || event.EventType != "session.plan.saved" {
		return fmt.Errorf("plan %q did not return a committed session.plan.saved event", plan.ID)
	}
	clientRequestID := planSavedV3ClientRequestID(*event, plan)
	payloadHash := planSavedV3PayloadHash(*event, plan)
	result, err := applySessionMutation(sessionruntime.SessionMutationInput{
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

func planSavedV3ClientRequestID(event pebblestore.EventEnvelope, plan pebblestore.SessionPlanSnapshot) string {
	if event.GlobalSeq > 0 {
		return fmt.Sprintf("plan-saved:%s:%d", strings.TrimSpace(plan.SessionID), event.GlobalSeq)
	}
	return fmt.Sprintf("plan-saved:%s:%s:v%d", strings.TrimSpace(plan.SessionID), strings.TrimSpace(plan.ID), plan.Version)
}

func planSavedV3PayloadHash(event pebblestore.EventEnvelope, plan pebblestore.SessionPlanSnapshot) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plan.SessionID) + "\x00" + strings.TrimSpace(plan.ID) + "\x00" + fmt.Sprintf("%d", plan.Version) + "\x00" + string(event.Payload)))
	return hex.EncodeToString(sum[:])
}
