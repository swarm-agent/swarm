package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

type workspaceSkillDeleteRequest struct {
	WorkspacePath string `json:"workspace_path"`
	CanonicalName string `json:"canonical_name"`
}

func (s *Server) handleWorkspaceSkillDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.workspace == nil || s.discovery == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace skill service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	var req workspaceSkillDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	canonicalName := strings.TrimSpace(req.CanonicalName)
	if workspacePath == "" || canonicalName == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_path and canonical_name are required"))
		return
	}
	if discovery.NormalizeSkillName(canonicalName) != canonicalName {
		writeError(w, http.StatusBadRequest, errors.New("canonical_name must be a canonical skill name"))
		return
	}

	scope, err := s.workspace.ScopeForPathForPrincipal(principal, workspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !scope.Matched || strings.TrimSpace(scope.WorkspacePath) == "" {
		writeError(w, http.StatusBadRequest, errAccountOwnedWorkspacePathRequired)
		return
	}

	report, err := s.discovery.ScanScope(scope.WorkspacePath, scope.Directories)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var revision string
	for _, skill := range report.Skills {
		if skill.CanonicalName == canonicalName && skill.Scope == "workspace-local" && skill.Origin == "agents-project-skills" {
			revision = strings.TrimSpace(skill.Hash)
			break
		}
	}
	if revision == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("workspace Skill %q not found", canonicalName))
		return
	}

	if err := tool.DeleteWorkspaceSkill(tool.WorkspaceScope{
		PrimaryPath: scope.WorkspacePath,
		Roots:       scope.Directories,
		Principal:   principal,
	}, canonicalName, revision); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("delete workspace Skill %q: %w", canonicalName, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canonical_name": canonicalName})
}
