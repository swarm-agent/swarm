package api

import (
	"errors"
	"net/http"
	"swarm/packages/swarmd/internal/identity"
)

const sessionAccessScope = "swarm:approve"
const retiredSessionAccess = "per-session access is retired; use /v3/permissions"

func sessionAccessAuthority(r *http.Request) error {
	if _, bound := r.Context().Value(applicationAgentContextKey{}).(applicationAgentBinding); bound {
		return errors.New("a gateway token cannot change permissions")
	}
	if record, ok := ScopedTokenFromRequest(r); ok && !record.HasScope(sessionAccessScope) {
		return errors.New("changing permissions needs swarm:approve")
	}
	return nil
}

// The legacy read remains an account-scoped compatibility alias only.
func (s *Server) handleSessionV3Access(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	_, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodPost {
		writeError(w, http.StatusBadRequest, errors.New(retiredSessionAccess))
		return
	}
	s.handleAccountPermissions(w, r)
}
