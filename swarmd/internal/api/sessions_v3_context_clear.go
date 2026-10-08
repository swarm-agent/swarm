package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleSessionV3ClearContext(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "sessions:write") {
		return
	}
	var req struct {
		ClientRequestID      string  `json:"client_request_id"`
		ExpectedLastEventSeq *uint64 `json:"expected_last_event_seq"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ClientRequestID == "" || req.ExpectedLastEventSeq == nil {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id and expected_last_event_seq are required"))
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil || !found {
		writeSessionNotFound(w)
		return
	}
	payload, _ := json.Marshal(req)
	digest := sha256.Sum256(payload)
	result, err := s.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		Kind: pebblestore.V3SessionMutationClearContext, SessionID: sessionID,
		UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: req.ClientRequestID, PayloadHash: hex.EncodeToString(digest[:]),
		ExpectedLastEventSeq: req.ExpectedLastEventSeq,
	})
	if err != nil {
		var conflict *pebblestore.V3ProjectionConflictError
		status := http.StatusInternalServerError
		if errors.Is(err, pebblestore.ErrSessionContextClearConflict) || errors.Is(err, pebblestore.ErrV3IdempotencyConflict) || errors.As(err, &conflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mutation": sessionV3MutationResultResponse(result)})
}
