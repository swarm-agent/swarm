package api

import (
	"errors"
	"net/http"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errUnauthorizedPrincipal())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type":                 principal.Type,
		"userID":               principal.UserID,
		"user_id":              principal.UserID,
		"accountScopeID":       principal.AccountScopeID,
		"account_scope_id":     principal.AccountScopeID,
		"teamID":               nil,
		"team_id":              nil,
		"accountScopeSource":   principal.AccountScopeSource,
		"account_scope_source": principal.AccountScopeSource,
		"session_id":           principal.SessionID,
		"auth_provider":        principal.AuthProvider,
	})
}

func errUnauthorizedPrincipal() error { return errors.New("trusted principal is required") }
