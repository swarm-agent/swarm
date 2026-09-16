package api

import (
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/workspace"
)

// These are authenticated, explicit user operations, independent from optional
// provider-backed onboarding conversations and from catalog/session mutation.
func (s *Server) handleWorkspaceRepositoryReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	review, err := s.workspace.ReviewRepositoryForPrincipal(principal, r.URL.Query().Get("path"))
	if err != nil {
		writeWorkspaceRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": review})
}

func (s *Server) handleWorkspaceRepositoryBaseline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req workspace.RepositoryBaselineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, err := s.workspace.PrepareRepositoryBaselineForPrincipal(principal, req)
	if err != nil {
		writeWorkspaceRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repository": state})
}

// Inspection reports typed prerequisites without attempting any mutation.
func (s *Server) handleWorkspaceRepositoryInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	state, err := s.workspace.InspectRepositoryForPrincipal(principal, r.URL.Query().Get("path"))
	if err != nil {
		writeWorkspaceRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repository": state})
}
