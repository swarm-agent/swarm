package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Client sessions: how a client app the person runs (their console, on this
// machine or on their own PC) is the owner of this Swarm without an AI key.
// One is minted only over the owner socket, by root or the daemon's user on
// this machine, so a token that leaks cannot make more. The token is sent as
// X-Swarm-Token to the loopback API; it carries the owner's rights, the same
// as the desktop UI, and stops working when revoked here.

const clientSessionsPath = "/v1/auth/clients"

func (s *Server) handleClientSessions(w http.ResponseWriter, r *http.Request) {
	if s.identitySessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("identity session service not configured"))
		return
	}
	if principal, ok := PrincipalFromRequest(r); !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, clientSessionsPath), "/")
	switch {
	case r.Method == http.MethodGet && id == "":
		records, err := s.identitySessions.ListClientSessions()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if records == nil {
			records = []pebblestore.ClientSessionRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"clients": records})
	case r.Method == http.MethodPost && id == "":
		if !isLocalTransportRequest(r) {
			writeError(w, http.StatusForbidden, errors.New("a client session is made on the machine: sudo swarmctl client connect NAME"))
			return
		}
		var req struct {
			Name   string `json:"name"`
			Device string `json:"device"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid client session request"))
			return
		}
		issued, err := s.identitySessions.IssueDeviceClientSession(req.Name, req.Device)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"token":      issued.Token,
			"session_id": issued.SessionID,
			"name":       strings.TrimSpace(req.Name),
			"device":     strings.TrimSpace(req.Device),
			"issued_at":  issued.IssuedAt.UnixMilli(),
			"expires_at": issued.ExpiresAt.UnixMilli(),
		})
	case r.Method == http.MethodDelete && id != "":
		record, err := s.identitySessions.RevokeClientSession(id)
		if errors.Is(err, identity.ErrClientSessionNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "client": record})
	default:
		methodNotAllowed(w)
	}
}
