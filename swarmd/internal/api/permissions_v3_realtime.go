package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) publishPermissionSummaryV3Realtime(sessionID string, summary pebblestore.PermissionSummary) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return err
	}
	payloadSummary := sessionsV3PermissionSummaryFromStore(summary)
	payload, err := json.Marshal(map[string]any{
		"session_id":         sessionID,
		"permission_summary": payloadSummary,
		"summary":            payloadSummary,
	})
	if err != nil {
		return err
	}
	now := payloadSummary.UpdatedAt
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	hash := sha256.Sum256(payload)
	input := sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(session.UserID),
		AccountScopeID:  strings.TrimSpace(session.AccountScopeID),
		ClientRequestID: fmt.Sprintf("permission-summary:%s:%d:%x", sessionID, now, hash[:8]),
		IdempotencyKey:  fmt.Sprintf("permission-summary:%s:%d:%x", sessionID, now, hash[:8]),
		PayloadHash:     fmt.Sprintf("sha256:%x", hash),
		RequestHash:     fmt.Sprintf("sha256:%x", hash),
		Kind:            "permission.summary.update",
		EventType:       "permission.summary.updated",
		EventPayload:    payload,
		NowUnixMs:       now,
	}
	_, err = s.applySessionV3PrimaryMutation(input)
	return err
}

func sessionsV3PermissionSummaryFromStore(summary pebblestore.PermissionSummary) sessionsV3PermissionSummary {
	out := sessionsV3PermissionSummary{
		SessionID:            strings.TrimSpace(summary.SessionID),
		PendingApprovalCount: summary.PendingCount,
		OldestPendingAt:      summary.OldestPendingAt,
		NewestPendingAt:      summary.NewestPendingAt,
		UpdatedAt:            summary.UpdatedAt,
	}
	if out.PendingApprovalCount <= 0 {
		out.PendingApprovalCount = 0
		out.OldestPendingAt = 0
		out.NewestPendingAt = 0
	}
	return out
}
