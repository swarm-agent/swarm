package api

import (
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

func (s *Server) handleModelCatalogCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service not configured"))
		return
	}

	refresh, err := s.model.CheckCatalog(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":      false,
			"error":   err.Error(),
			"refresh": refresh,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"refresh": refresh,
	})
}
