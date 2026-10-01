package api

import (
	"net/http"
	"strconv"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleProjectDesigns(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	limit := 20
	var err error
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err != nil {
		designAPIError(w, pebblestore.ErrDesignInvalid)
		return
	}
	rows, cursor, err := s.sessions.Store().ListProjectDesignRequests(pebblestore.DesignPrincipal{AccountID: p.AccountScopeID, PrincipalID: p.UserID}, projectID, r.URL.Query().Get("after"), limit)
	if err != nil {
		designAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"designs": rows, "next_cursor": cursor})
}
