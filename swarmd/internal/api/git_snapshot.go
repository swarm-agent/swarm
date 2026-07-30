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
	if err := s.populateSessionGitCommits(context.Background(), principal, strings.TrimSpace(r.URL.Query().Get("session_id")), workspacePath, limit, &snapshot); err != nil {
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
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || !session.WorktreeEnabled {
		return nil
	}
	baseRef := strings.TrimSpace(session.WorktreeBaseBranch)
	if baseCommit := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "base_commit")); baseCommit != "" {
		baseRef = baseCommit
	}
	snapshot.SessionCommits = gitstatus.ListCommitsSince(ctx, workspacePath, baseRef, limit)
	return nil
}

func (s *Server) resolveSessionGitWorkspacePath(principal identity.Principal, sessionID string) (string, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", false, nil
	}
	if s == nil || s.sessions == nil {
		return "", false, errors.New("session service not configured")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", false, err
	}
	if !ok || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "", false, errors.New("session not found")
	}
	if !session.WorktreeEnabled {
		return "", false, nil
	}
	worktreePath := strings.TrimSpace(session.WorktreeRootPath)
	if worktreePath == "" || worktreePath != strings.TrimSpace(session.WorkspacePath) {
		return "", false, errors.New("session worktree path is incomplete")
	}
	return worktreePath, true, nil
}

func (s *Server) resolveGitStatusWorkspacePath(r *http.Request, principal identity.Principal) (string, error) {
	if worktreePath, ok, err := s.resolveSessionGitWorkspacePath(principal, r.URL.Query().Get("session_id")); err != nil {
		return "", err
	} else if ok {
		return worktreePath, nil
	}
	workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(r.URL.Query().Get("cwd"))
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
