package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/gitstatus"
)

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	workspacePath, err := s.resolveGitStatusWorkspacePath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("recent_limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("recent_limit must be a non-negative integer"))
			return
		}
		limit = parsed
	}
	snapshot, err := gitstatus.SnapshotForPath(context.Background(), workspacePath, gitstatus.Options{RecentLimit: limit, IncludeDetails: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": snapshot,
	})
}

func (s *Server) resolveGitStatusWorkspacePath(r *http.Request) (string, error) {
	workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(r.URL.Query().Get("cwd"))
	}
	if workspacePath == "" && s.workspace != nil {
		current, ok, err := s.workspace.CurrentBinding()
		if err != nil {
			return "", err
		}
		if ok {
			workspacePath = strings.TrimSpace(current.ResolvedPath)
		}
	}
	if workspacePath == "" {
		return "", errors.New("workspace_path is required")
	}
	if s.workspace == nil {
		return workspacePath, nil
	}
	scope, err := s.workspace.ScopeForPath(workspacePath)
	if err != nil {
		return "", err
	}
	if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
		return strings.TrimSpace(scope.WorkspacePath), nil
	}
	return strings.TrimSpace(scope.ResolvedPath), nil
}
