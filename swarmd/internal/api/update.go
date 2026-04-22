package api

import (
	"net/http"

	"swarm/packages/swarmd/internal/update"
)

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status := s.update.Status(r.Context(), false)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	plan, err := s.update.Apply(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) SetUpdateService(updateSvc *update.Service) {
	if s == nil {
		return
	}
	s.update = updateSvc
}
