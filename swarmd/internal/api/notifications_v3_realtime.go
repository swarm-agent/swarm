package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/notification"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	v3NotificationResourceEventType = "notification.resource.updated"
	v3NotificationResourceSessionID = "__desktop_notifications__"
)

type sessionsV3NotificationResourcePayload struct {
	AccountScopeID string                           `json:"account_scope_id,omitempty"`
	SwarmID        string                           `json:"swarm_id"`
	EventType      string                           `json:"event_type"`
	Notification   *pebblestore.NotificationRecord  `json:"notification,omitempty"`
	Summary        *pebblestore.NotificationSummary `json:"summary,omitempty"`
	Deleted        int                              `json:"deleted,omitempty"`
	RecordedAt     int64                            `json:"recorded_at"`
}

func (s *Server) publishNotificationV3Realtime(event notification.RealtimeEvent) {
	if s == nil || s.sessions == nil {
		return
	}
	payload := sessionsV3NotificationResourcePayload{
		AccountScopeID: strings.TrimSpace(event.AccountScopeID),
		SwarmID:        strings.TrimSpace(event.SwarmID),
		EventType:      strings.TrimSpace(event.EventType),
		Deleted:        event.Deleted,
		RecordedAt:     event.RecordedAt,
	}
	if payload.RecordedAt <= 0 {
		payload.RecordedAt = time.Now().UnixMilli()
	}
	if event.Notification != nil {
		notification := *event.Notification
		payload.Notification = &notification
		if payload.SwarmID == "" {
			payload.SwarmID = strings.TrimSpace(notification.SwarmID)
		}
		if payload.AccountScopeID == "" {
			payload.AccountScopeID = strings.TrimSpace(notification.AccountScopeID)
		}
	}
	if event.Summary != nil {
		summary := *event.Summary
		payload.Summary = &summary
		if payload.SwarmID == "" {
			payload.SwarmID = strings.TrimSpace(summary.SwarmID)
		}
		if payload.AccountScopeID == "" {
			payload.AccountScopeID = strings.TrimSpace(summary.AccountScopeID)
		}
	}
	if payload.SwarmID == "" || payload.AccountScopeID == "" {
		return
	}
	if payload.EventType == "" {
		payload.EventType = v3NotificationResourceEventType
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	hash := sha256.Sum256(raw)
	id := ""
	if payload.Notification != nil {
		id = strings.TrimSpace(payload.Notification.ID)
	}
	if id == "" {
		id = payload.SwarmID
	}
	mutation := sessionruntime.SessionMutationInput{
		SessionID:       v3NotificationResourceSessionID,
		UserID:          "desktop",
		AccountScopeID:  payload.AccountScopeID,
		ClientRequestID: fmt.Sprintf("notification-resource:%s:%s:%d:%x", payload.SwarmID, id, payload.RecordedAt, hash[:8]),
		IdempotencyKey:  fmt.Sprintf("notification-resource:%s:%s:%d:%x", payload.SwarmID, id, payload.RecordedAt, hash[:8]),
		PayloadHash:     fmt.Sprintf("sha256:%x", hash),
		RequestHash:     fmt.Sprintf("sha256:%x", hash),
		Kind:            "notification.resource.update",
		EventType:       v3NotificationResourceEventType,
		EventPayload:    raw,
		NowUnixMs:       payload.RecordedAt,
	}
	_, _ = s.applySessionV3PrimaryMutation(mutation)
}

func sessionsV3NotificationResourcePayloadFromRecord(record sessionruntime.RealtimeOutboxRecord) (sessionsV3NotificationResourcePayload, bool) {
	if strings.TrimSpace(record.Event.EventType) != v3NotificationResourceEventType || len(record.Event.Payload) == 0 {
		return sessionsV3NotificationResourcePayload{}, false
	}
	var payload sessionsV3NotificationResourcePayload
	if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
		return sessionsV3NotificationResourcePayload{}, false
	}
	if strings.TrimSpace(payload.SwarmID) == "" || strings.TrimSpace(payload.AccountScopeID) == "" {
		return sessionsV3NotificationResourcePayload{}, false
	}
	return payload, true
}
