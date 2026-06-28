package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) persistModeUpdatedV3Mutation(result sessionruntime.PlanLifecycleResult, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if applySessionMutation == nil || result.ModeEvent == nil {
		return nil
	}
	if strings.TrimSpace(result.Session.ID) == "" {
		return errors.New("mode update result is missing session id")
	}
	if result.ModeEvent.EventType != "session.mode.updated" {
		return fmt.Errorf("session %q did not return a committed session.mode.updated event", result.Session.ID)
	}
	payloadHash := planLifecycleModePayloadHash(*result.ModeEvent, result.Session)
	clientRequestID := planLifecycleModeClientRequestID(*result.ModeEvent, result.Session)
	mutation, err := applySessionMutation(sessionruntime.SessionMutationInput{
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

func planLifecycleModeClientRequestID(event pebblestore.EventEnvelope, session pebblestore.SessionSnapshot) string {
	if event.GlobalSeq > 0 {
		return fmt.Sprintf("plan-lifecycle-mode:%s:%d", strings.TrimSpace(session.ID), event.GlobalSeq)
	}
	return fmt.Sprintf("plan-lifecycle-mode:%s:%s:%d", strings.TrimSpace(session.ID), strings.TrimSpace(session.Mode), event.TsUnixMs)
}

func planLifecycleModePayloadHash(event pebblestore.EventEnvelope, session pebblestore.SessionSnapshot) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(session.ID) + "\x00plan_lifecycle_mode\x00" + strings.TrimSpace(session.Mode) + "\x00" + string(event.Payload)))
	return hex.EncodeToString(sum[:])
}
