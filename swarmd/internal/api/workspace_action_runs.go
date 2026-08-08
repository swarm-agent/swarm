package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	actionruntime "swarm/packages/swarmd/internal/action"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type workspaceActionRunRequest struct {
	WorkspacePath string            `json:"workspace_path"`
	SessionID     string            `json:"session_id"`
	ActionID      string            `json:"action_id"`
	RunID         string            `json:"run_id"`
	Inputs        map[string]string `json:"inputs"`
}

func (s *Server) handleWorkspaceActionRunStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.actionRuns == nil {
		writeError(w, http.StatusInternalServerError, errors.New("action runner not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("action runner is shutting down"))
		return
	}
	var req workspaceActionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope, err := s.resolveWorkspaceActionScope(r, req.WorkspacePath, req.SessionID)
	if err != nil {
		writeError(w, workspaceActionScopeStatus(err), err)
		return
	}
	if strings.TrimSpace(req.ActionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("action_id is required"))
		return
	}
	run, err := s.actionRuns.Start(actionruntime.RunInput{Scope: scope, ActionID: req.ActionID, Inputs: req.Inputs})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "workspace_id": scope.WorkspaceID, "workspace_path": scope.WorkspacePath, "run": run})
}

func (s *Server) handleWorkspaceActionRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.actionRuns == nil {
		writeError(w, http.StatusInternalServerError, errors.New("action runner not configured"))
		return
	}
	scope, err := s.resolveWorkspaceActionScope(r, r.URL.Query().Get("workspace_path"), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, workspaceActionScopeStatus(err), err)
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required"))
		return
	}
	run, found, err := s.actionRuns.Get(scope, runID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("action run %q not found", runID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_id": scope.WorkspaceID, "workspace_path": scope.WorkspacePath, "run": run})
}

func (s *Server) handleWorkspaceActionRunCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.actionRuns == nil {
		writeError(w, http.StatusInternalServerError, errors.New("action runner not configured"))
		return
	}
	var req workspaceActionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope, err := s.resolveWorkspaceActionScope(r, req.WorkspacePath, req.SessionID)
	if err != nil {
		writeError(w, workspaceActionScopeStatus(err), err)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required"))
		return
	}
	run, found, err := s.actionRuns.Cancel(scope, runID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("action run %q not found", runID))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "workspace_id": scope.WorkspaceID, "workspace_path": scope.WorkspacePath, "run": run})
}

func (s *Server) resolveWorkspaceActionScope(r *http.Request, rawPath, sessionID string) (actionruntime.Scope, error) {
	if s.workspace == nil {
		return actionruntime.Scope{}, errors.New("workspace service not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		return actionruntime.Scope{}, identity.ErrPrincipalRequired
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return actionruntime.Scope{}, errors.New("workspace path is required")
	}
	workspaceScope, err := s.workspace.ScopeForPathForPrincipal(principal, rawPath)
	if err != nil {
		return actionruntime.Scope{}, err
	}
	if strings.TrimSpace(sessionID) != "" {
		return s.resolveWorkspaceActionSessionScope(principal, workspaceScope.ResolvedPath, sessionID)
	}
	if workspaceScope.Matched && strings.TrimSpace(workspaceScope.WorkspaceID) != "" && strings.TrimSpace(workspaceScope.WorkspacePath) != "" {
		return actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspaceScope.WorkspacePath}, nil
	}
	return s.resolveWorkspaceActionBindingScope(principal, workspaceScope.ResolvedPath)
}

func (s *Server) resolveWorkspaceActionSessionScope(principal identity.Principal, runtimePath, sessionID string) (actionruntime.Scope, error) {
	runtimePath = strings.TrimSpace(runtimePath)
	sessionID = strings.TrimSpace(sessionID)
	if runtimePath == "" || sessionID == "" || s.sessions == nil || s.topology == nil {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return actionruntime.Scope{}, err
	}
	if !found || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return actionruntime.Scope{}, errors.New("session not found")
	}
	if filepath.Clean(strings.TrimSpace(session.WorkspacePath)) != filepath.Clean(runtimePath) {
		return actionruntime.Scope{}, errors.New("session workspace path does not match")
	}
	binding, found, err := s.sessionWorkspaceBindingFromLineage(principal, session)
	if err != nil {
		return actionruntime.Scope{}, err
	}
	if !found {
		if !session.WorktreeEnabled {
			workspaceScope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, runtimePath)
			if scopeErr != nil {
				return actionruntime.Scope{}, scopeErr
			}
			if workspaceScope.Matched && strings.TrimSpace(workspaceScope.WorkspaceID) != "" && strings.TrimSpace(workspaceScope.WorkspacePath) != "" {
				return actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspaceScope.WorkspacePath}, nil
			}
		}
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	return s.workspaceActionScopeFromBinding(principal, binding, runtimePath)
}

func (s *Server) resolveWorkspaceActionBindingScope(principal identity.Principal, runtimePath string) (actionruntime.Scope, error) {
	runtimePath = strings.TrimSpace(runtimePath)
	if runtimePath == "" || s.sessions == nil || s.topology == nil {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	sessions, err := s.sessions.ListSessionsForAccountPath(principal.AccountScopeID, runtimePath, 1000)
	if err != nil {
		return actionruntime.Scope{}, err
	}
	var canonical actionruntime.Scope
	for _, session := range sessions {
		if strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
			continue
		}
		binding, found, err := s.sessionWorkspaceBindingFromLineage(principal, session)
		if err != nil {
			return actionruntime.Scope{}, err
		}
		if !found {
			continue
		}
		candidate, err := s.workspaceActionScopeFromBinding(principal, binding, runtimePath)
		if err != nil {
			if errors.Is(err, errAccountOwnedWorkspacePathRequired) {
				continue
			}
			return actionruntime.Scope{}, err
		}
		if canonical.WorkspaceID != "" && (canonical.WorkspaceID != candidate.WorkspaceID || filepath.Clean(canonical.WorkspacePath) != filepath.Clean(candidate.WorkspacePath)) {
			return actionruntime.Scope{}, errors.New("worktree resolves to multiple canonical workspaces")
		}
		canonical = candidate
	}
	if canonical.WorkspaceID == "" {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	return canonical, nil
}

func (s *Server) workspaceActionScopeFromBinding(principal identity.Principal, binding pebblestore.TopologyWorkspaceBindingRecord, runtimePath string) (actionruntime.Scope, error) {
	if !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) ||
		(strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID)) ||
		strings.TrimSpace(binding.SourceWorkspaceID) == "" || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, binding.SourceWorkspaceID)
	if err != nil {
		return actionruntime.Scope{}, err
	}
	if !found || entry.WorkspaceGeneration != binding.SourceWorkspaceGeneration ||
		filepath.Clean(strings.TrimSpace(entry.Path)) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	return actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: entry.WorkspaceID, WorkspacePath: entry.Path, RuntimePath: runtimePath}, nil
}

func workspaceActionScopeStatus(err error) int {
	if errors.Is(err, identity.ErrPrincipalRequired) {
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}
