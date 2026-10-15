package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// recordJobActivity persists delivery/landing receipts on the owning session's
// existing event log and realtime outbox. Content from publish tools is excluded.
// The receipt key is stable across retries of the same outcome.
func (c *controlMCPCall) recordJobActivity(kind, a, b string, refs controlMCPJobRefs, state, head string) error {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || refs.sessionID == "" {
		return errors.New("activity receipt requires an owned session")
	}
	allowed, err := c.server.authorizeReadableSessionV3Access(principal, refs.sessionID)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("activity receipt session is not owned")
	}
	payload, err := json.Marshal(map[string]string{"state": state, "job_id": controlMCPJobID(kind, a, b), "project_id": refs.projectID})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(append(append([]byte(refs.sessionID+"\x00"+head+"\x00"), payload...), []byte(state)...))
	key := "activity:" + hex.EncodeToString(digest[:])
	_, err = c.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: refs.sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Kind: sessionruntime.SessionMutationRecordDiagnostic, EventType: "session.delivery.updated", EventPayload: payload, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key})
	return err
}
