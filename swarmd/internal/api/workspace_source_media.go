package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

type workspaceSourceMediaDirectoryRequest struct {
	WorkspacePath string `json:"workspace_path"`
	DirectoryPath string `json:"directory_path"`
}

func (s *Server) handleWorkspaceSourceMediaDirectories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	if workspacePath == "" {
		resolution, found, err := s.workspace.CurrentBindingForPrincipal(principal)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("workspace binding not found"))
			return
		}
		workspacePath = resolution.WorkspacePath
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                       true,
		"workspace":                resolution,
		"source_media_directories": resolution.SourceMediaDirectories,
	})
}

func (s *Server) handleWorkspaceSourceMediaDirectoryAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	var req workspaceSourceMediaDirectoryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.AddSourceMediaDirectoryForPrincipal(principal, req.WorkspacePath, req.DirectoryPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                       true,
		"workspace":                resolution,
		"source_media_directories": resolution.SourceMediaDirectories,
	})
}

func (s *Server) handleWorkspaceSourceMediaDirectoryRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	var req workspaceSourceMediaDirectoryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.RemoveSourceMediaDirectoryForPrincipal(principal, req.WorkspacePath, req.DirectoryPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                       true,
		"workspace":                resolution,
		"source_media_directories": resolution.SourceMediaDirectories,
	})
}
