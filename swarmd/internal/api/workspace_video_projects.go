package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/videoproject"
)

type workspaceVideoProjectForkRequest struct {
	WorkspacePath        string `json:"workspace_path"`
	SourceSessionID      string `json:"source_session_id"`
	SourceProjectID      string `json:"source_project_id"`
	SourceRevisionID     string `json:"source_revision_id"`
	DestinationSessionID string `json:"destination_session_id"`
	DestinationProjectID string `json:"destination_project_id,omitempty"`
}

func (s *Server) handleWorkspaceVideoProjects(w http.ResponseWriter, r *http.Request) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	owned, err := s.resolveAccountOwnedPath(principal, r.URL.Query().Get("workspace_path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			limit = parsed
		}
	}
	items, err := s.videoProjects.ListWorkspaceCatalog(principal, owned.WorkspacePath, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "videos": items, "count": len(items)})
}

func (s *Server) handleWorkspaceVideoProjectFork(w http.ResponseWriter, r *http.Request) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req workspaceVideoProjectForkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	owned, err := s.resolveAccountOwnedPath(principal, req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sourceSession, sourceActive, sourceErr := s.sessions.GetSession(strings.TrimSpace(req.SourceSessionID))
	if sourceErr != nil {
		writeError(w, http.StatusInternalServerError, sourceErr)
		return
	}
	if !sourceActive {
		tombstone, tombstoneFound, tombstoneErr := s.sessions.Store().GetV3SessionTombstone(strings.TrimSpace(req.SourceSessionID))
		if tombstoneErr != nil {
			writeError(w, http.StatusInternalServerError, tombstoneErr)
			return
		}
		if !tombstoneFound || tombstone.Deleted || !tombstone.Archived {
			writeError(w, http.StatusNotFound, errors.New("source session not found"))
			return
		}
		sourceSession = tombstone.Session
	}
	if sourceSession.AccountScopeID != principal.AccountScopeID || (sourceSession.UserID != "" && sourceSession.UserID != principal.UserID) || strings.TrimSpace(sourceSession.WorkspacePath) != owned.WorkspacePath {
		writeError(w, http.StatusNotFound, errors.New("source session not found in current workspace"))
		return
	}
	destination, found, err := s.sessions.GetSession(strings.TrimSpace(req.DestinationSessionID))
	if err != nil || !found || destination.AccountScopeID != principal.AccountScopeID || (destination.UserID != "" && destination.UserID != principal.UserID) || strings.TrimSpace(destination.WorkspacePath) != owned.WorkspacePath {
		writeError(w, http.StatusNotFound, errors.New("destination session not found in current workspace"))
		return
	}
	workspaceID, _ := destination.Metadata["workspace_id"].(string)
	project, revision, err := s.videoProjects.ForkRevision(r.Context(), principal, videoproject.ForkRevisionInput{
		SourceSessionID: strings.TrimSpace(req.SourceSessionID), SourceProjectID: strings.TrimSpace(req.SourceProjectID), SourceRevisionID: strings.TrimSpace(req.SourceRevisionID),
		DestinationSessionID: destination.ID, DestinationWorkspaceID: strings.TrimSpace(workspaceID), ProjectID: strings.TrimSpace(req.DestinationProjectID),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "session_id": destination.ID, "project": project, "revision": revision})
}
