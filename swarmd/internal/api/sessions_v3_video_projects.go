package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
)

type sessionV3CreateVideoProjectRequest struct {
	ProjectID       string                            `json:"project_id"`
	Title           string                            `json:"title"`
	Description     string                            `json:"description"`
	OutputPreset    string                            `json:"output_preset"`
	InitialTimeline *pebblestore.VideoProjectTimeline `json:"initial_timeline"`
	Metadata        map[string]any                    `json:"metadata"`
}

type sessionV3CreateVideoProjectRevisionRequest struct {
	RevisionID      string                           `json:"revision_id"`
	Description     string                           `json:"description"`
	ChangeSummary   string                           `json:"change_summary"`
	Timeline        pebblestore.VideoProjectTimeline `json:"timeline"`
	AuthorPrincipal string                           `json:"author_principal"`
}

type sessionV3RestoreVideoProjectRevisionRequest struct {
	SourceRevisionID string `json:"source_revision_id"`
	RevisionID       string `json:"revision_id"`
	Description      string `json:"description"`
	ChangeSummary    string `json:"change_summary"`
	AuthorPrincipal  string `json:"author_principal"`
}

type sessionV3StartVideoRenderRequest struct {
	RevisionID string `json:"revision_id"`
	JobID      string `json:"job_id"`
	TimeoutMs  int64  `json:"timeout_ms"`
}

type sessionV3ExportVideoRequest struct {
	DestinationPath string `json:"destination_path"`
	JobID           string `json:"job_id"`
}

func (s *Server) handleSessionV3VideoProjects(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		projects, err := s.videoProjects.ListProjects(principal, sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"projects": projects,
			"count":    len(projects),
		})
	case http.MethodPost:
		var req sessionV3CreateVideoProjectRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("session not found")
			}
			writeError(w, http.StatusNotFound, err)
			return
		}
		workspaceID := ""
		if val, ok := session.Metadata["workspace_id"].(string); ok {
			workspaceID = val
		}
		project, revision, err := s.videoProjects.CreateProject(r.Context(), principal, videoprojectCreateProjectInput(sessionID, workspaceID, req))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		res := map[string]any{
			"ok":      true,
			"project": project,
		}
		if revision != nil {
			res["revision"] = revision
		}
		writeJSON(w, http.StatusCreated, res)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSessionV3VideoSubpath(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, subpath string) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	subpath = strings.Trim(subpath, "/")
	if subpath == "projects" {
		s.handleSessionV3VideoProjects(w, r, principal, sessionID)
		return
	}
	if subpath == "projects/primary" {
		s.handleSessionV3PrimaryVideoProject(w, r, principal, sessionID)
		return
	}
	if strings.HasPrefix(subpath, "projects/") {
		s.handleSessionV3VideoProjectDetail(w, r, principal, sessionID, strings.TrimPrefix(subpath, "projects/"))
		return
	}
	if strings.HasPrefix(subpath, "render-jobs/") {
		jobID := strings.TrimPrefix(subpath, "render-jobs/")
		if jobID == "" {
			writeError(w, http.StatusBadRequest, errors.New("job id is required"))
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		job, ok, err := s.videoProjects.GetRenderJob(principal, sessionID, jobID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("render job %q not found", jobID)
			}
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "render_job": job})
		return
	}
	writeError(w, http.StatusNotFound, errors.New("invalid video subpath"))
}

func (s *Server) handleSessionV3VideoProjectDetail(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, projectPath string) {
	projectID, rest, hasRest := strings.Cut(projectPath, "/")
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}
	if !hasRest {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		project, ok, err := s.videoProjects.GetProject(principal, sessionID, projectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", projectID)
			}
			writeError(w, http.StatusNotFound, err)
			return
		}
		res := map[string]any{"ok": true, "project": project}
		if project.CurrentRevisionID != "" {
			if rev, revOK, revErr := s.videoProjects.GetRevision(principal, sessionID, projectID, project.CurrentRevisionID); revErr == nil && revOK {
				res["current_revision"] = rev
			}
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	switch rest {
	case "revisions":
		switch r.Method {
		case http.MethodGet:
			limit := 50
			if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
				if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
					limit = parsed
				}
			}
			revisions, err := s.videoProjects.ListRevisions(principal, sessionID, projectID, limit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revisions": revisions, "count": len(revisions)})
		case http.MethodPost:
			var req sessionV3CreateVideoProjectRevisionRequest
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			author := req.AuthorPrincipal
			if author == "" {
				author = principal.UserID
			}
			revision, project, err := s.videoProjects.CreateRevision(r.Context(), principal, videoprojectCreateRevisionInput(sessionID, projectID, req, author))
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "revision": revision, "project": project})
		default:
			methodNotAllowed(w)
		}
	case "render":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req sessionV3StartVideoRenderRequest
		if r.Body != nil && r.ContentLength > 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		job, err := s.videoProjects.StartRenderJob(r.Context(), principal, videoprojectStartRenderJobInput(sessionID, projectID, req))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if s.videoRender != nil {
			session, _, _ := s.sessions.GetSession(sessionID)
			wsPath := session.WorkspacePath
			if val, ok := session.Metadata["swarm_v3_source_workspace_path"].(string); ok && val != "" {
				wsPath = val
			}
			var timeout time.Duration
			if req.TimeoutMs > 0 {
				timeout = time.Duration(req.TimeoutMs) * time.Millisecond
			}
			renderReq := videorender.RenderJobRequest{
				SessionID:     sessionID,
				ProjectID:     projectID,
				RevisionID:    job.RevisionID,
				JobID:         job.ID,
				WorkspacePath: wsPath,
				Timeout:       timeout,
			}
			go func(p identity.Principal, rReq videorender.RenderJobRequest) {
				_, _ = s.videoRender.RenderJob(context.Background(), p, rReq)
			}(principal, renderReq)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "render_job": job})
	case "render-jobs":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		limit := 50
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		jobs, err := s.videoProjects.ListRenderJobs(principal, sessionID, projectID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "render_jobs": jobs, "count": len(jobs)})
	case "export":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req sessionV3ExportVideoRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.handleSessionV3VideoExport(w, r, principal, sessionID, projectID, req)
	default:
		if strings.HasPrefix(rest, "revisions/") && strings.HasSuffix(rest, "/restore") {
			revID := strings.TrimSuffix(strings.TrimPrefix(rest, "revisions/"), "/restore")
			if revID == "" {
				writeError(w, http.StatusBadRequest, errors.New("revision id is required"))
				return
			}
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			var req sessionV3RestoreVideoProjectRevisionRequest
			if r.Body != nil && r.ContentLength > 0 {
				if err := decodeJSON(r, &req); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
			if strings.TrimSpace(req.SourceRevisionID) != "" && strings.TrimSpace(req.SourceRevisionID) != revID {
				writeError(w, http.StatusBadRequest, errors.New("source_revision_id must match the revision in the request path"))
				return
			}
			author := strings.TrimSpace(req.AuthorPrincipal)
			if author == "" {
				author = principal.UserID
			}
			revision, project, err := s.videoProjects.RestoreRevision(r.Context(), principal, videoproject.RestoreRevisionInput{
				SessionID: sessionID, ProjectID: projectID, SourceRevisionID: revID, RevisionID: req.RevisionID,
				Description: req.Description, ChangeSummary: req.ChangeSummary, AuthorPrincipal: author, NowUnixMs: time.Now().UnixMilli(),
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "revision": revision, "project": project})
			return
		}
		if strings.HasPrefix(rest, "revisions/") {
			revID := strings.TrimPrefix(rest, "revisions/")
			if revID == "" {
				writeError(w, http.StatusBadRequest, errors.New("revision id is required"))
				return
			}
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			revision, ok, err := s.videoProjects.GetRevision(principal, sessionID, projectID, revID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("revision %q for project %q not found", revID, projectID)
				}
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": revision})
			return
		}
		writeError(w, http.StatusNotFound, errors.New("invalid video project subpath"))
	}
}

func (s *Server) handleSessionV3PrimaryVideoProject(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		writeError(w, http.StatusNotFound, err)
		return
	}
	if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	projects, err := s.videoProjects.ListProjects(principal, sessionID, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, project := range projects {
		if project.ProjectKind == pebblestore.VideoProjectKindVideoTool {
			res := map[string]any{"ok": true, "project": project, "project_id": project.ID}
			if project.CurrentRevisionID != "" {
				if rev, found, getErr := s.videoProjects.GetRevision(principal, sessionID, project.ID, project.CurrentRevisionID); getErr == nil && found {
					res["current_revision"] = rev
				}
			}
			writeJSON(w, http.StatusOK, res)
			return
		}
	}
	if r.Method == http.MethodGet {
		writeError(w, http.StatusNotFound, errors.New("primary video tool project not found"))
		return
	}
	var req sessionV3CreateVideoProjectRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	workspaceID := ""
	if val, ok := session.Metadata["workspace_id"].(string); ok {
		workspaceID = val
	}
	input := videoprojectCreateProjectInput(sessionID, workspaceID, req)
	input.ProjectKind = pebblestore.VideoProjectKindVideoTool
	project, revision, err := s.videoProjects.GetOrCreatePrimaryVideoToolProject(r.Context(), principal, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res := map[string]any{"ok": true, "project": project, "project_id": project.ID}
	if revision != nil {
		res["revision"] = revision
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleSessionV3VideoExport(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, projectID string, req sessionV3ExportVideoRequest) {
	destinationPath := strings.TrimSpace(req.DestinationPath)
	if destinationPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("destination_path is required"))
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		writeError(w, http.StatusNotFound, err)
		return
	}
	wsPath := session.WorkspacePath
	if val, ok := session.Metadata["swarm_v3_source_workspace_path"].(string); ok && val != "" {
		wsPath = val
	}
	if wsPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("session workspace path is not available"))
		return
	}
	// Verify destination path is inside workspace
	absDest, err := filepath.Abs(destinationPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	absWS, err := filepath.Abs(wsPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rel, err := filepath.Rel(absWS, absDest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, errors.New("destination_path must be within workspace"))
		return
	}

	jobID := strings.TrimSpace(req.JobID)
	var job pebblestore.VideoRenderJobSnapshot
	if jobID != "" {
		job, ok, err = s.videoProjects.GetRenderJob(principal, sessionID, jobID)
	} else {
		project, pOK, pErr := s.videoProjects.GetProject(principal, sessionID, projectID)
		if pErr != nil || !pOK {
			writeError(w, http.StatusNotFound, errors.New("video project not found"))
			return
		}
		if project.ActiveRenderJobID == "" {
			writeError(w, http.StatusBadRequest, errors.New("project has no active render job"))
			return
		}
		job, ok, err = s.videoProjects.GetRenderJob(principal, sessionID, project.ActiveRenderJobID)
	}
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, errors.New("render job not found"))
		return
	}
	if job.Status != pebblestore.VideoRenderJobStatusReady || job.OutputArtifact == nil {
		writeError(w, http.StatusBadRequest, errors.New("render job is not ready or has no output artifact"))
		return
	}

	if s.artifacts == nil {
		writeError(w, http.StatusInternalServerError, errors.New("artifact registry is not configured"))
		return
	}
	variant, found, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, sessionID, job.OutputArtifact.CollectionID, job.OutputArtifact.VariantID)
	if err != nil || !found || variant.EventSeq != job.OutputArtifact.EventSeq {
		writeError(w, http.StatusBadRequest, errors.New("output artifact reference is no longer exact or available"))
		return
	}
	artifactService, err := s.artifacts.ServiceForSession(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve artifact service: %w", err))
		return
	}
	file, _, err := artifactService.Open(r.Context(), variant)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("open output artifact: %w", err))
		return
	}
	defer file.Close()

	if err := os.MkdirAll(filepath.Dir(absDest), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create destination directory: %w", err))
		return
	}
	out, err := os.OpenFile(absDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create destination file: %w", err))
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write destination file: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"destination_path": absDest,
		"size_bytes":       job.OutputSizeBytes,
		"digest_sha256":    job.OutputDigestSHA256,
	})
}

func videoprojectCreateProjectInput(sessionID, workspaceID string, req sessionV3CreateVideoProjectRequest) videoproject.CreateProjectInput {
	return videoproject.CreateProjectInput{
		SessionID:       sessionID,
		WorkspaceID:     workspaceID,
		ProjectID:       req.ProjectID,
		Title:           req.Title,
		Description:     req.Description,
		OutputPreset:    req.OutputPreset,
		InitialTimeline: req.InitialTimeline,
		Metadata:        req.Metadata,
		NowUnixMs:       time.Now().UnixMilli(),
	}
}

func videoprojectCreateRevisionInput(sessionID, projectID string, req sessionV3CreateVideoProjectRevisionRequest, author string) videoproject.CreateRevisionInput {
	return videoproject.CreateRevisionInput{
		SessionID:       sessionID,
		ProjectID:       projectID,
		RevisionID:      req.RevisionID,
		Description:     req.Description,
		ChangeSummary:   req.ChangeSummary,
		Timeline:        req.Timeline,
		AuthorPrincipal: author,
		NowUnixMs:       time.Now().UnixMilli(),
	}
}

func videoprojectStartRenderJobInput(sessionID, projectID string, req sessionV3StartVideoRenderRequest) videoproject.StartRenderJobInput {
	return videoproject.StartRenderJobInput{
		SessionID:  sessionID,
		ProjectID:  projectID,
		RevisionID: req.RevisionID,
		JobID:      req.JobID,
		NowUnixMs:  time.Now().UnixMilli(),
	}
}
