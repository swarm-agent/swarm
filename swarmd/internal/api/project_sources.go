package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// Project source routes: the one HTTP surface of manage_projects list_sources,
// inspect_source and inspect_files. They are read-only and resolve only the
// caller account's own registered sources and exact task results; a linked
// home reaches them over its link (Server.CallFleetProject), so a linked
// machine answers with its own sources, never the home's.
//
//	GET  /v3/projects/{id}/sources                list_sources
//	GET  /v3/projects/{id}/sources/inspect        inspect_source (workspace_path, workspace_id, workspace_generation)
//	POST /v3/projects/{id}/sources/inspect-files  inspect_files (source or exact task result, one read/list/search/find)
const (
	projectSourcesSegment          = "sources"
	projectSourceInspectSegment    = "inspect"
	projectSourceInspectFilesSegmt = "inspect-files"
	// projectSourceInspectFilesMaxBytes bounds an inspect_files request body;
	// the selected read's own tool bounds its answer.
	projectSourceInspectFilesMaxBytes = 64 * 1024
)

// projectSourceInspectFilesRequest is inspect_files' body: the source or exact
// task result selector, and at most one read-only inspection operation. The
// project is the route's own; unknown fields are refused.
type projectSourceInspectFilesRequest struct {
	WorkspacePath       string `json:"workspace_path"`
	WorkspaceID         string `json:"workspace_id"`
	WorkspaceGeneration int64  `json:"workspace_generation"`
	TaskID              string `json:"task_id"`
	AttemptID           string `json:"attempt_id"`
	SourceSessionID     string `json:"source_session_id"`
	HeadCommit          string `json:"head_commit"`
	Inspection          any    `json:"inspection"`
}

// projectInspectionRunner runs one resolved inspection (tool.Runtime).
type projectInspectionRunner interface {
	RunProjectInspection(context.Context, identity.Principal, tool.ProjectInspectionTarget, any) (string, error)
}

// handleProjectSources serves the project source routes.
func (s *Server) handleProjectSources(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string, rest []string) {
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, errors.New("project_id is required"))
		return
	}
	if len(rest) == 0 && r.Method == http.MethodGet {
		result, err := s.InspectProjectSources(r.Context(), p, projectID, "", "", 0, true)
		if err != nil {
			writeError(w, projectSourceErrorStatus(err), err)
			return
		}
		result["project_id"] = projectID
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(rest) == 1 && rest[0] == projectSourceInspectSegment && r.Method == http.MethodGet {
		q := r.URL.Query()
		generation, err := projectSourceGeneration(q.Get("workspace_generation"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := s.InspectProjectSources(r.Context(), p, projectID, strings.TrimSpace(q.Get("workspace_path")), strings.TrimSpace(q.Get("workspace_id")), generation, false)
		if err != nil {
			writeError(w, projectSourceErrorStatus(err), err)
			return
		}
		result["project_id"] = projectID
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(rest) == 1 && rest[0] == projectSourceInspectFilesSegmt && r.Method == http.MethodPost {
		var req projectSourceInspectFilesRequest
		if err := decodeJSONLimited(w, r, &req, projectSourceInspectFilesMaxBytes); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.WorkspaceGeneration < 0 {
			writeError(w, http.StatusBadRequest, errors.New("workspace_generation must be a non-negative integer"))
			return
		}
		target, err := s.ResolveProjectInspectionForPrincipal(r.Context(), p, tool.ProjectInspectionRequest{
			ProjectID:           projectID,
			WorkspacePath:       strings.TrimSpace(req.WorkspacePath),
			WorkspaceID:         strings.TrimSpace(req.WorkspaceID),
			WorkspaceGeneration: req.WorkspaceGeneration,
			TaskID:              strings.TrimSpace(req.TaskID),
			AttemptID:           strings.TrimSpace(req.AttemptID),
			SessionID:           strings.TrimSpace(req.SourceSessionID),
			HeadCommit:          strings.TrimSpace(req.HeadCommit),
		})
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if req.Inspection == nil {
			// No operation: the resolved target alone, as inspect_files
			// answers without inspection.
			writeJSON(w, http.StatusOK, map[string]any{"target": target, "inspection_only": true, "guidance": "Inspection is not testing, acceptance or integration. Never validate against the source checkout instead."})
			return
		}
		runner, ok := s.toolCatalog.(projectInspectionRunner)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, errors.New("project inspection runtime unavailable; no checkout or command authority was granted"))
			return
		}
		out, err := runner.RunProjectInspection(r.Context(), p, target, req.Inspection)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, json.RawMessage(out))
		return
	}
	if len(rest) == 0 || len(rest) == 1 && (rest[0] == projectSourceInspectSegment || rest[0] == projectSourceInspectFilesSegmt) {
		methodNotAllowed(w)
		return
	}
	writeError(w, http.StatusNotFound, errors.New("no such project source route"))
}

// projectSourceGeneration reads workspace_generation: omitted or a
// non-negative integer.
func projectSourceGeneration(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("workspace_generation must be a non-negative integer")
	}
	return n, nil
}

// projectSourceErrorStatus maps a source resolution refusal: an unknown
// project is not found; every other refusal (stale generation, a path that is
// not an authorized catalog root) is a conflict with the caller's selector.
func projectSourceErrorStatus(err error) int {
	if err != nil && strings.Contains(err.Error(), "not found") {
		return http.StatusNotFound
	}
	return http.StatusConflict
}
