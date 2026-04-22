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

func (s *Server) SetUpdateService(updateSvc *update.Service) {
	if s == nil {
		return
	}
	s.update = updateSvc
}
