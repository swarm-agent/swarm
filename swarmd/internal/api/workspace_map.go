package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// handleWorkspaceMap adapts the existing account map authority for an explicit
// Desktop save gesture. It does not create a map on read or replace its metadata.
func (s *Server) handleWorkspaceMap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !p.Valid() {
		http.Error(w, "authentication required", 401)
		return
	}
	if s.memory == nil {
		http.Error(w, "memory unavailable", 503)
		return
	}
	maps := s.memory.Store.WorkspaceMapView()
	var result any
	var err error
	switch r.Method {
	case http.MethodGet:
		var record store.WorkspaceMap
		var found bool
		record, found, err = maps.GetForAccount(p.AccountScopeID)
		result = map[string]any{"workspace_map": record, "found": found}
	case http.MethodPost:
		var req struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Content          string `json:"content"`
			Confirm          bool   `json:"confirm"`
			Intent           string `json:"intent"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil {
			http.Error(w, "invalid workspace map request", 400)
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			http.Error(w, "one request required", 400)
			return
		}
		if !req.Confirm || strings.TrimSpace(req.Intent) == "" || len(req.Intent) > 500 {
			http.Error(w, "explicit workspace map save confirmation and intent required", 403)
			return
		}
		result, err = maps.UpdateForAccount(p.AccountScopeID, req.ExpectedRevision, req.Content)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrWorkspaceMapRevisionConflict) {
			status = 409
		}
		if errors.Is(err, store.ErrMemoryPolicy) {
			status = 403
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
