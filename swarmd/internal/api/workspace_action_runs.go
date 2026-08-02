package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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
	if workspaceScope.Matched && strings.TrimSpace(workspaceScope.WorkspaceID) != "" && strings.TrimSpace(workspaceScope.WorkspacePath) != "" {
		return actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspaceScope.WorkspacePath}, nil
	}
	return s.resolveWorkspaceActionBindingScope(principal, workspaceScope.ResolvedPath)
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
		binding, found, err := s.workspaceActionSessionBinding(principal, session)
		if err != nil {
			return actionruntime.Scope{}, err
		}
		if !found || !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) ||
			(strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID)) ||
			strings.TrimSpace(binding.SourceWorkspaceID) == "" || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
			continue
		}
		entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, binding.SourceWorkspaceID)
		if err != nil {
			return actionruntime.Scope{}, err
		}
		if !found || entry.WorkspaceGeneration != binding.SourceWorkspaceGeneration ||
			filepath.Clean(strings.TrimSpace(entry.Path)) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
			continue
		}
		candidate := actionruntime.Scope{AccountScopeID: principal.AccountScopeID, WorkspaceID: entry.WorkspaceID, WorkspacePath: entry.Path, RuntimePath: runtimePath}
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

func (s *Server) workspaceActionSessionBinding(principal identity.Principal, session pebblestore.SessionSnapshot) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	seen := make(map[string]struct{}, 8)
	for depth := 0; depth < 100; depth++ {
		if strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		if _, exists := seen[sessionID]; exists {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("worktree session lineage contains a cycle")
		}
		seen[sessionID] = struct{}{}
		bindingID := firstNonEmpty(
			strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_workspace_binding_id")),
			strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "local_workspace_binding_id")),
		)
		if bindingID != "" {
			return s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, bindingID)
		}
		parentSessionID := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "parent_session_id"))
		if parentSessionID == "" {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		parent, found, err := s.sessions.GetSession(parentSessionID)
		if err != nil || !found {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
		}
		session = parent
	}
	return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("worktree session lineage exceeds the supported depth")
}

func workspaceActionScopeStatus(err error) int {
	if errors.Is(err, identity.ErrPrincipalRequired) {
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}
