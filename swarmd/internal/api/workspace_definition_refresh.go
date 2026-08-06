package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const WorkspaceDefinitionRefreshPath = "/v1/workspace/definitions/refresh"

type workspaceDefinitionRefreshFailure struct {
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspacePath string `json:"workspace_path"`
	Error         string `json:"error"`
}

// handleWorkspaceDefinitionRefresh starts one fresh, hidden Router definition
// session for every active saved workspace owned by the current account. Each
// launch is independent so the jobs run in parallel and a later click creates
// a new definition generation instead of reusing an earlier run.
func (s *Server) handleWorkspaceDefinitionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}

	entries, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated := make([]pebblestore.WorkspaceEntry, 0, len(entries))
	workspaceCount := 0
	launchedCount := 0
	failures := make([]workspaceDefinitionRefreshFailure, 0)
	for _, entry := range entries {
		if strings.TrimSpace(entry.State) != "active" || strings.TrimSpace(entry.Path) == "" || strings.TrimSpace(entry.WorkspaceID) == "" {
			continue
		}
		workspaceCount++
		pending, err := s.workspace.MarkDefinitionPendingForPrincipal(principal, entry.Path)
		if err != nil {
			failures = append(failures, workspaceDefinitionRefreshFailure{WorkspaceID: entry.WorkspaceID, WorkspacePath: entry.Path, Error: err.Error()})
			continue
		}
		if err := s.launchWorkspaceDefinitionJob(principal, pending); err != nil {
			if failed, current, persistErr := s.workspace.FailDefinitionForPrincipal(principal, pending.Path, pending.DefinitionGeneration, err.Error(), workspaceDefinitionModelSuggestion, 0); persistErr == nil && current {
				pending = failed
			}
			updated = append(updated, pending)
			failures = append(failures, workspaceDefinitionRefreshFailure{WorkspaceID: pending.WorkspaceID, WorkspacePath: pending.Path, Error: err.Error()})
			continue
		}
		launchedCount++
		updated = append(updated, pending)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"workspace_count": workspaceCount,
		"launched_count":  launchedCount,
		"failed_count":    len(failures),
		"workspaces":      updated,
		"failures":        failures,
	})
}
