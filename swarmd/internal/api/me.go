package api

import (
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	actor, ok := productActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errUnauthorizedPrincipal())
		return
	}
	principal, err := identity.PrincipalFromActor(actor)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorizedPrincipal())
		return
	}
	var teamID any
	if actor.TeamID != "" {
		teamID = actor.TeamID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type":                 principal.Type,
		"bootstrapped":         true,
		"userID":               actor.UserID,
		"user_id":              actor.UserID,
		"accountScopeID":       actor.AccountScopeID,
		"account_scope_id":     actor.AccountScopeID,
		"username":             actor.User.Username,
		"teamID":               teamID,
		"team_id":              teamID,
		"teamDisplayName":      actor.Team.Name,
		"team_display_name":    actor.Team.Name,
		"teamDefault":          actor.Team.Default,
		"team_default":         actor.Team.Default,
		"membershipRole":       actor.Membership.Role,
		"membership_role":      actor.Membership.Role,
		"accountScopeSource":   principal.AccountScopeSource,
		"account_scope_source": principal.AccountScopeSource,
		"session_id":           principal.SessionID,
		"auth_provider":        principal.AuthProvider,
	})
}

func errUnauthorizedPrincipal() error { return errors.New("trusted principal is required") }
