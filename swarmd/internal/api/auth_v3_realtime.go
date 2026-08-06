package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	v3AuthResourceEventType = "auth.credentials.updated"
	v3AuthResourceSessionID = "__desktop_auth__"
)

type sessionsV3AuthResourcePayload struct {
	AccountScopeID string `json:"account_scope_id"`
	EventType      string `json:"event_type"`
	Provider       string `json:"provider,omitempty"`
	RecordedAt     int64  `json:"recorded_at"`
	EventSequence  uint64 `json:"event_sequence"`
}

func (s *Server) publishAuthCredentialV3Realtime(accountScopeID string, event pebblestore.EventEnvelope) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if s == nil || s.sessions == nil || accountScopeID == "" || !strings.HasPrefix(strings.TrimSpace(event.EventType), "auth.") {
		return
	}
	payload := sessionsV3AuthResourcePayload{
		AccountScopeID: accountScopeID,
		EventType:      strings.TrimSpace(event.EventType),
		Provider:       strings.TrimSpace(event.EntityID),
		RecordedAt:     event.TsUnixMs,
		EventSequence:  event.GlobalSeq,
	}
	if payload.RecordedAt <= 0 || payload.EventSequence == 0 {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	hash := sha256.Sum256(raw)
	key := fmt.Sprintf("auth-resource:%s:%d:%x", accountScopeID, payload.EventSequence, hash[:8])
	mutation := sessionruntime.SessionMutationInput{
		SessionID:       v3AuthResourceSessionID,
		UserID:          "desktop",
		AccountScopeID:  accountScopeID,
		ClientRequestID: key,
		IdempotencyKey:  key,
		PayloadHash:     fmt.Sprintf("sha256:%x", hash),
		RequestHash:     fmt.Sprintf("sha256:%x", hash),
		Kind:            "auth.credentials.update",
		EventType:       v3AuthResourceEventType,
		EventPayload:    raw,
		NowUnixMs:       payload.RecordedAt,
	}
	_, _ = s.applySessionV3PrimaryMutation(mutation)
}

func sessionsV3AuthResourcePayloadFromRecord(record sessionruntime.RealtimeOutboxRecord) (sessionsV3AuthResourcePayload, bool) {
	if strings.TrimSpace(record.Event.EventType) != v3AuthResourceEventType || len(record.Event.Payload) == 0 {
		return sessionsV3AuthResourcePayload{}, false
	}
	var payload sessionsV3AuthResourcePayload
	if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
		return sessionsV3AuthResourcePayload{}, false
	}
	if strings.TrimSpace(payload.AccountScopeID) == "" || strings.TrimSpace(payload.EventType) == "" || payload.EventSequence == 0 {
		return sessionsV3AuthResourcePayload{}, false
	}
	return payload, true
}
