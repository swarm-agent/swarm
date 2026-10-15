package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Human takeover of a conversation, for example a public sealed agent's: the
// owner (never an agent-bound token, whose gate refuses these routes) pauses
// the agent, replies as a person and hands the conversation back.
//
//   - State lives in session metadata under "takeover" ({state: "human", by,
//     since} or {state: "agent", ended_at}); a gateway reads it from the session.
//   - While the state is "human", a posted visitor message is stored with a
//     dispatch_blocked run intent (reason human_takeover), so no model run starts.
//   - Operator replies are assistant messages with source "operator", so the
//     agent sees them as part of the conversation after hand-back.
//   - Starting a takeover cancels the agent's run in flight.
//
// Every write crosses ApplySessionMutation.

const (
	sessionTakeoverMetadataKey   = "takeover"
	sessionTakeoverBlockedReason = "human_takeover"
	sessionOperatorMessageMax    = 16 << 10
)

type sessionsV3TakeoverRequest struct {
	Action          string `json:"action"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

type sessionsV3OperatorMessageRequest struct {
	Content         string `json:"content"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

// sessionTakeoverActive reports whether a person currently holds the conversation.
func sessionTakeoverActive(session pebblestore.SessionSnapshot) bool {
	raw, ok := session.Metadata[sessionTakeoverMetadataKey].(map[string]any)
	return ok && raw["state"] == "human"
}

func (s *Server) handleSessionV3Takeover(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authorize := s.authorizeSessionsV3PrimarySession
	if r.Method == http.MethodGet {
		authorize = s.authorizeReadableSessionV3Access
	}
	if found, err := authorize(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "takeover": session.Metadata[sessionTakeoverMetadataKey]})
		return
	}
	var req sessionsV3TakeoverRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	action := strings.TrimSpace(req.Action)
	if action != "start" && action != "end" {
		writeError(w, http.StatusBadRequest, errors.New(`action must be "start" or "end"`))
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	now := time.Now().UnixMilli()
	state := map[string]any{"state": "agent", "ended_at": now}
	if action == "start" {
		state = map[string]any{"state": "human", "by": principal.UserID, "since": now}
	}
	next := session
	next.Metadata = cloneStringAnyMap(session.Metadata)
	next.Metadata[sessionTakeoverMetadataKey] = state
	next.UpdatedAt = now
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"takeover": action})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "metadata": next.Metadata, "updated_at": now})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationUpdateMetadata, EventPayload: eventPayload, Session: &next, NowUnixMs: now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if action == "start" && !result.Replayed {
		s.cancelRunForTakeover(principal, sessionID)
	}
	if result.Session != nil {
		state, _ = result.Session.Metadata[sessionTakeoverMetadataKey].(map[string]any)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "takeover": state, "mutation": result})
}

// cancelRunForTakeover stops the agent's run in flight, if any, when a person
// takes the conversation. Nothing is in flight is the common case.
func (s *Server) cancelRunForTakeover(principal identity.Principal, sessionID string) {
	runID := s.v3SessionExecutor.activeRunID(sessionID)
	if runID == "" {
		return
	}
	const reason = "a person took over the conversation"
	if _, cancelled, err := s.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID, StopCause: sessionruntime.PlanStoppedByUser}, reason); err == nil && cancelled && s.perm != nil {
		_, _ = s.perm.CancelRunPending(sessionID, runID, reason)
	}
}

func (s *Server) handleSessionV3OperatorMessages(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req sessionsV3OperatorMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" || len(content) > sessionOperatorMessageMax {
		writeError(w, http.StatusBadRequest, errors.New("content is required (at most 16 KiB)"))
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if !sessionTakeoverActive(session) {
		writeError(w, http.StatusConflict, errors.New("take over the conversation before replying as a person"))
		return
	}
	now := time.Now().UnixMilli()
	sum := sha256.Sum256([]byte(sessionID + "\x00" + clientRequestID + "\x00operator_reply"))
	message := pebblestore.MessageSnapshot{
		ID: "v3msg_operator_" + hex.EncodeToString(sum[:16]), Role: "assistant", Content: content, CreatedAt: now,
		Metadata: map[string]any{"source": "operator", "message_kind": "operator_reply", "operator_user_id": principal.UserID, "visible": true},
	}
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationAppendMessage, map[string]any{"source": "operator", "content": content})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message, NowUnixMs: now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "message": result.Message, "mutation": result})
}
