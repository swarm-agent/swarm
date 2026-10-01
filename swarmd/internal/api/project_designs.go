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
	archived, err := designArchiveView(r)
	if err != nil {
		designAPIError(w, err)
		return
	}
	filtered := make([]pebblestore.ProjectDesignEntry, 0, len(rows))
	for _, row := range rows {
		if len(pebblestore.FilterDesignCatalogView([]pebblestore.DesignRequest{row.Request}, archived)) != 0 {
			filtered = append(filtered, row)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"designs": filtered, "next_cursor": cursor})
}

// Filtering never expands a bounded page; clients must follow next_cursor even
// when a page is empty. Omitted view is the active library.
func designArchiveView(r *http.Request) (bool, error) {
	switch r.URL.Query().Get("view") {
	case "", "active":
		return false, nil
	case "archived":
		return true, nil
	default:
		return false, pebblestore.ErrDesignInvalid
	}
}
