package api

import (
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/videosource"
)

func (s *Server) handleWorkspaceVideoScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if s.workspace == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace video source services are not configured"))
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		RootPath      string `json:"root_path"`
		RelativePath  string `json:"relative_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := videosource.NewService(s.workspace, s.sessions.Store()).BrowsePath(principal, req.WorkspacePath, req.RootPath, req.RelativePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "workspace_id": result.WorkspaceID, "root_path": result.RootPath,
		"relative_path": result.RelativePath, "directories": result.Directories, "clips": result.Clips,
	})
}
