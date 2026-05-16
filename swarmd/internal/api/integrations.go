package api

import (
	"errors"
	"net/http"
	"strings"

	integrationruntime "swarm/packages/swarmd/internal/integration"
)

func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		writeError(w, http.StatusInternalServerError, errors.New("integration service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		req := integrationruntime.Request{
			Action:    firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("action")), "list"),
			Resource:  strings.TrimSpace(r.URL.Query().Get("resource")),
			PackID:    strings.TrimSpace(r.URL.Query().Get("pack_id")),
			VersionID: strings.TrimSpace(r.URL.Query().Get("version_id")),
			ID:        strings.TrimSpace(r.URL.Query().Get("id")),
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			limit, ok := parsePositiveInt(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			req.Limit = limit
		}
		response, err := s.integrations.Handle(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var req integrationruntime.Request
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		response, err := s.integrations.Handle(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		methodNotAllowed(w)
	}
}
