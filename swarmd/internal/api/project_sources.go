package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// Project source routes: the linked-machine side of manage_projects
// list_sources, inspect_source and inspect_files. They are read-only and
// resolve only this machine's own registered sources and exact task results;
// a linked home reaches them over its link (Server.CallFleetProject), so a
// linked machine answers with its own sources, never the home's.
//
// They answer only this Swarm's linked home over the link (linkedSourceCaller):
// the request came through the TLS-pinned link listener with that home's own
// session, which only the listener can attest. The home's side of that
// authority is what CallFleetProject's callers require
// (tool.Runtime.homeConversation: the home's own Orchestrator conversation,
// never a task's, or a Swarm Control key for list_sources and inspect_source
// as at home; inspect_files is never served to Swarm Control), and the home's
// raw machine proxy refuses
// these routes (fleetSourceProxyRefused), so nothing else of the home reaches
// them. Locally the same service stays where it was: the native
// manage_projects actions (inspect_files only in a bound project or task
// conversation, ResolveProjectInspection) and Swarm Control's catalog
// list_sources and inspect_source. A project or session read scope, an
// owner client or any other local caller is refused here, so the route never
// widens who may inspect sources.
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
	if err := linkedSourceCaller(r, p); err != nil {
		writeError(w, http.StatusForbidden, err)
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

// errProjectSourcesLinkedOnly refuses a project source route caller that is
// not this Swarm's linked home over the link.
var errProjectSourcesLinkedOnly = errors.New("project source routes answer only this Swarm's linked home over the link; here, use manage_projects list_sources, inspect_source or inspect_files (inspect_files in a project or task conversation)")

// linkedSourceCaller admits a project source route caller only when the link
// listener attested it: the request came over the TLS-pinned link from a
// linked home (fleetHomeFromRequest, set only by fleetLinkHandler) and its
// principal is that home's own session here. No header, argument or token
// scope can stand in for it.
func linkedSourceCaller(r *http.Request, p identity.Principal) error {
	home, overLink := fleetHomeFromRequest(r)
	if !overLink || strings.TrimSpace(home.Fingerprint) == "" || strings.TrimSpace(home.SessionID) == "" {
		return errProjectSourcesLinkedOnly
	}
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.SessionID != home.SessionID {
		return errors.New("project source routes: the caller is not the linked home's own session here")
	}
	return nil
}

// fleetSourceProxyDecodeRounds bounds how many further percent-decodings
// fleetSourceProxyRefused tries; a path still encoded after them is refused.
const fleetSourceProxyDecodeRounds = 8

// fleetSourceProxyRefused keeps the home's raw machine proxy
// (/v3/fleet/machines/{name}/api/...) away from a machine's project source
// routes (/v3/projects/{id}/sources...): the home reaches them only through
// CallFleetProject, behind its bound-conversation check, never as a bare
// owner request.
//
// path is the home's already-decoded path, which the proxy sends on as it is
// (fleet.Client.Do), so the machine parses and decodes it again. The check is
// made on that machine-side path: parsed the way the request is sent (a ?
// or # ends the path there), then at every further percent-decoding, each
// cleaned as the machine's router would. A path that cannot be sent, or is
// still encoded after the bounded decodings, is refused.
func fleetSourceProxyRefused(path string) bool {
	sent, err := url.Parse("https://machine/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return true
	}
	decoded := sent.Path
	for round := 0; ; round++ {
		if fleetSourceRoute(decoded) {
			return true
		}
		if !strings.Contains(decoded, "%") {
			return false
		}
		if round == fleetSourceProxyDecodeRounds {
			return true
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			// Not decodable again: the machine's router sees it as it is,
			// which was checked above.
			return false
		}
		decoded = next
	}
}

// fleetSourceRoute reports whether a decoded path, cleaned as the machine's
// router would, is a project source route (/v3/projects/{id}/sources...).
func fleetSourceRoute(path string) bool {
	segments := strings.Split(strings.Trim(pathpkg.Clean("/"+path), "/"), "/")
	return len(segments) >= 4 && segments[0] == "v3" && segments[1] == "projects" && segments[3] == projectSourcesSegment
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
