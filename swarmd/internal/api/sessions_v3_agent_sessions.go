package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Session lifecycle for a gateway serving one agent's conversations:
//
//	GET  /v3/sessions:agent?agent=&state=active|ended|all&updated_after=&updated_before=&cursor=&limit=
//	POST /v3/sessions/{id}/end   {client_request_id, reason}
//	DELETE /v3/sessions/{id}     (the existing hard delete)
//
// An agent-bound token reaches these for its own agent only (see
// agentBoundRequestError); the owner names the agent. Ending is durable
// session metadata ("ended": {at, by, reason}) written through
// ApplySessionMutation: afterwards the session accepts no messages, takeovers
// or operator replies, and its run in flight is stopped.

const (
	sessionEndedMetadataKey      = "ended"
	sessionChangeEnded           = "session.ended"
	sessionChangeTakeoverStarted = "takeover.started"
	sessionChangeTakeoverEnded   = "takeover.ended"
	sessionEndReasonMaxRunes     = 200
	agentSessionsDefaultLimit    = 50
	agentSessionsMaxLimit        = 200
	// agentSessionsScanLimit bounds how many of the account's sessions one
	// listing reads.
	agentSessionsScanLimit = 100_000
)

var errSessionV3Ended = errors.New("the conversation has ended; start a new session")

// sessionEndedState returns the session's ended record, or nil.
func sessionEndedState(session pebblestore.SessionSnapshot) map[string]any {
	raw, ok := session.Metadata[sessionEndedMetadataKey].(map[string]any)
	if !ok || raw["at"] == nil {
		return nil
	}
	return raw
}

func sessionEnded(session pebblestore.SessionSnapshot) bool {
	return sessionEndedState(session) != nil
}

func sessionEndedAt(session pebblestore.SessionSnapshot) int64 {
	state := sessionEndedState(session)
	if state == nil {
		return 0
	}
	switch v := state["at"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

type sessionsV3EndRequest struct {
	Reason          string `json:"reason,omitempty"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

func (s *Server) handleSessionV3End(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3EndRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	reason := strings.Join(strings.Fields(req.Reason), " ")
	if utf8.RuneCountInString(reason) > sessionEndReasonMaxRunes {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reason is at most %d characters", sessionEndReasonMaxRunes))
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	response := map[string]any{"ok": true, "session_id": sessionID}
	if state := sessionEndedState(session); state != nil {
		response["ended"] = state
		response["already_ended"] = true
	} else {
		now := time.Now().UnixMilli()
		state := map[string]any{"at": now, "by": principal.UserID}
		if reason != "" {
			state["reason"] = reason
		}
		next := session
		next.Metadata = cloneStringAnyMap(session.Metadata)
		next.Metadata[sessionEndedMetadataKey] = state
		next.UpdatedAt = now
		payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"end": reason})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "metadata": next.Metadata, "updated_at": now, "change": sessionChangeEnded})
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
		if result.Session != nil {
			state = sessionEndedState(*result.Session)
		}
		response["ended"] = state
		response["mutation"] = result
	}
	// Stopping is repeated on every call, so retrying end after a failed stop
	// finishes the job.
	runID, stopErr := s.stopActiveSessionRun(principal, sessionID, "the conversation was ended")
	if runID != "" {
		response["stopped_run_id"] = runID
	}
	if stopErr != nil {
		response["ok"] = false
		response["error"] = "the conversation ended but its run could not be stopped: " + stopErr.Error()
		writeJSON(w, http.StatusInternalServerError, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// stopActiveSessionRun cancels the session's active run, if any, and its
// pending tool calls. It returns the run it stopped.
func (s *Server) stopActiveSessionRun(principal identity.Principal, sessionID, reason string) (string, error) {
	intent, active, err := s.sessions.GetSessionActiveRunIntent(sessionID)
	if err != nil {
		return "", err
	}
	if !active || strings.TrimSpace(intent.RunID) == "" {
		return "", nil
	}
	runID := strings.TrimSpace(intent.RunID)
	if s.v3SessionExecutor == nil {
		return runID, errors.New("sessions v3 executor is not configured")
	}
	if _, _, err := s.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID, StopCause: sessionruntime.PlanStoppedByUser}, reason); err != nil {
		return runID, err
	}
	if s.perm != nil {
		if _, err := s.perm.CancelRunPending(sessionID, runID, reason); err != nil {
			return runID, err
		}
	}
	return runID, nil
}

// agentForSessionsRequest returns the agent a listing or feed is about: an
// agent-bound token's own agent, or the owner's "agent" query parameter.
func agentForSessionsRequest(r *http.Request) (string, error) {
	requested := strings.TrimSpace(r.URL.Query().Get("agent"))
	if rec, ok := ScopedTokenFromRequest(r); ok && rec != nil && strings.TrimSpace(rec.AgentName) != "" {
		agent := strings.TrimSpace(rec.AgentName)
		if requested != "" && requested != agent {
			return "", errors.New("an agent-bound token sees only its own agent")
		}
		return agent, nil
	}
	if requested == "" {
		return "", errors.New("agent is required")
	}
	return requested, nil
}

func parseUnixMillisParam(raw, name string) (int64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false, errors.New(name + " must be a Unix time in milliseconds")
	}
	return parsed, true, nil
}

type agentSessionsCursor struct {
	UpdatedAt int64  `json:"u"`
	ID        string `json:"i"`
}

func encodeAgentSessionsCursor(c agentSessionsCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeAgentSessionsCursor(raw string) (agentSessionsCursor, error) {
	var c agentSessionsCursor
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(decoded, &c) != nil || c.ID == "" {
		return agentSessionsCursor{}, errors.New("cursor is not one this listing returned")
	}
	return c, nil
}

// handleSessionsV3AgentSessions lists one agent's sessions in the caller's
// account and user, most recently updated first, in pages.
func (s *Server) handleSessionsV3AgentSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	agent, err := agentForSessionsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	query := r.URL.Query()
	state := strings.TrimSpace(query.Get("state"))
	if state == "" {
		state = "all"
	}
	if state != "all" && state != "active" && state != "ended" {
		writeError(w, http.StatusBadRequest, errors.New(`state must be "active", "ended" or "all"`))
		return
	}
	updatedAfter, hasAfter, err := parseUnixMillisParam(query.Get("updated_after"), "updated_after")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updatedBefore, hasBefore, err := parseUnixMillisParam(query.Get("updated_before"), "updated_before")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := agentSessionsDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > agentSessionsMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Errorf("limit must be 1 to %d", agentSessionsMaxLimit))
			return
		}
		limit = parsed
	}
	var cursor *agentSessionsCursor
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		decoded, err := decodeAgentSessionsCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cursor = &decoded
	}
	all, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, agentSessionsScanLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	matched := make([]pebblestore.SessionSnapshot, 0)
	for _, session := range all {
		if sessionsV3MetadataString(session.Metadata, "agent_name") != agent {
			continue
		}
		ended := sessionEnded(session)
		if (state == "active" && ended) || (state == "ended" && !ended) {
			continue
		}
		if (hasAfter && session.UpdatedAt <= updatedAfter) || (hasBefore && session.UpdatedAt >= updatedBefore) {
			continue
		}
		if cursor != nil && (session.UpdatedAt > cursor.UpdatedAt || (session.UpdatedAt == cursor.UpdatedAt && session.ID >= cursor.ID)) {
			continue
		}
		matched = append(matched, session)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].UpdatedAt != matched[j].UpdatedAt {
			return matched[i].UpdatedAt > matched[j].UpdatedAt
		}
		return matched[i].ID > matched[j].ID
	})
	nextCursor := ""
	if len(matched) > limit {
		matched = matched[:limit]
		last := matched[len(matched)-1]
		nextCursor = encodeAgentSessionsCursor(agentSessionsCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	items := make([]map[string]any, 0, len(matched))
	for _, session := range matched {
		item := map[string]any{
			"id": session.ID, "title": session.Title, "created_at": session.CreatedAt, "updated_at": session.UpdatedAt,
			"message_count": session.MessageCount, "last_message_at": session.LastMessageAt, "state": "active",
		}
		if ended := sessionEndedState(session); ended != nil {
			item["state"] = "ended"
			item["ended"] = ended
		}
		if takeover, ok := session.Metadata[sessionTakeoverMetadataKey].(map[string]any); ok {
			item["takeover"] = takeover
		}
		_, active, err := s.sessions.GetSessionActiveRunIntent(session.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		item["run_active"] = active
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent, "sessions": items, "next_cursor": nextCursor})
}
