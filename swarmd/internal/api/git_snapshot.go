package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
)

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	workspacePath, err := s.resolveGitStatusWorkspacePath(r, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("recent_limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 || parsed > 100 {
			writeError(w, http.StatusBadRequest, errors.New("recent_limit must be an integer between 0 and 100"))
			return
		}
		limit = parsed
	}
	snapshot, err := gitstatus.SnapshotForPath(r.Context(), workspacePath, gitstatus.Options{RecentLimit: limit, IncludeDetails: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.populateSessionGitCommits(r.Context(), principal, strings.TrimSpace(r.URL.Query().Get("session_id")), workspacePath, limit, &snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": snapshot,
	})
}

func (s *Server) populateSessionGitCommits(ctx context.Context, principal identity.Principal, sessionID, workspacePath string, limit int, snapshot *gitstatus.Snapshot) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || snapshot == nil || s == nil || s.sessions == nil {
		return nil
	}
	item, err := s.selectedSessionRepository(principal, sessionID, workspacePath)
	if err != nil {
		return err
	}
	if item.BaseCommit != "" && item.Kind != "source" {
		snapshot.SessionCommits = gitstatus.ListCommitsSince(ctx, workspacePath, item.BaseCommit, limit)
	}
	return nil
}

func (s *Server) resolveGitStatusWorkspacePath(r *http.Request, principal identity.Principal) (string, error) {
	workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if workspacePath != "" && cwd != "" && workspacePath != cwd {
		return "", errors.New("conflicting repository selectors")
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(r.URL.Query().Get("cwd"))
	}
	if sessionID := strings.TrimSpace(r.URL.Query().Get("session_id")); sessionID != "" {
		item, err := s.selectedSessionRepository(principal, sessionID, workspacePath)
		return item.WorkspacePath, err
	}
	if workspacePath == "" && s.workspace != nil {
		current, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
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
	owned, err := s.resolveAccountOwnedPath(principal, workspacePath)
	if err != nil {
		return "", err
	}
	return owned.ResolvedPath, nil
}
