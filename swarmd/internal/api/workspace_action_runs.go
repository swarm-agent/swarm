package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	actionruntime "swarm/packages/swarmd/internal/action"
	"swarm/packages/swarmd/internal/identity"
)

type workspaceActionRunRequest struct {
	WorkspacePath string            `json:"workspace_path"`
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
	scope, err := s.resolveWorkspaceActionScope(r, req.WorkspacePath)
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
	scope, err := s.resolveWorkspaceActionScope(r, r.URL.Query().Get("workspace_path"))
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
	scope, err := s.resolveWorkspaceActionScope(r, req.WorkspacePath)
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

func (s *Server) resolveWorkspaceActionScope(r *http.Request, rawPath string) (actionruntime.Scope, error) {
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
	if !workspaceScope.Matched || strings.TrimSpace(workspaceScope.WorkspaceID) == "" || strings.TrimSpace(workspaceScope.WorkspacePath) == "" {
		return actionruntime.Scope{}, errAccountOwnedWorkspacePathRequired
	}
	return actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspaceScope.WorkspacePath}, nil
}

func workspaceActionScopeStatus(err error) int {
	if errors.Is(err, identity.ErrPrincipalRequired) {
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}
