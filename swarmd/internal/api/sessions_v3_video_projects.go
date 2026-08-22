package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
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

type sessionV3CreateVideoEditProposalRequest struct {
	ProposalID     string                           `json:"proposal_id"`
	BaseRevisionID string                           `json:"base_revision_id"`
	Title          string                           `json:"title"`
	Rationale      string                           `json:"rationale"`
	Plan           *pebblestore.VideoPlanProposal   `json:"plan"`
	Operations     []pebblestore.VideoEditOperation `json:"operations"`
	AffectedRanges []pebblestore.VideoTimelineRange `json:"affected_ranges"`
}

// sessionV3CreateVideoEditProposalResponse is the reviewed API contract:
// creation can only return a pending proposal/working revision. Acceptance and
// final render remain separate explicit user-triggered endpoints.
type sessionV3CreateVideoEditProposalResponse struct {
	OK                     bool                                  `json:"ok"`
	Proposal               pebblestore.VideoEditProposalSnapshot `json:"proposal"`
	ProposalStatus         string                                `json:"proposal_status"`
	RequiresUserAcceptance bool                                  `json:"requires_user_acceptance"`
	WorkingRevisionID      string                                `json:"working_revision_id,omitempty"`
	WorkingRevisionNumber  int                                   `json:"working_revision_number,omitempty"`
}

type sessionV3RejectVideoEditProposalRequest struct {
	Feedback string `json:"feedback"`
}

type sessionV3AcceptVideoEditProposalRequest struct {
	SelectedOperationIDs []string `json:"selected_operation_ids"`
	RevisionID           string   `json:"revision_id"`
	Description          string   `json:"description"`
	ChangeSummary        string   `json:"change_summary"`
	AuthorPrincipal      string   `json:"author_principal"`
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

const videoSourcePreviewTimeout = 5 * time.Minute

func (s *Server) handleSessionV3VideoSourceMedia(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if s.sessions.Store() == nil {
		writeError(w, http.StatusInternalServerError, errors.New("video source store is not configured"))
		return
	}
	sourceRef := strings.TrimSpace(r.URL.Query().Get("source_ref"))
	if sourceRef == "" {
		writeError(w, http.StatusBadRequest, errors.New("source_ref is required"))
		return
	}
	if strings.HasPrefix(sourceRef, "audiosrc_") {
		var record pebblestore.AudioSourceRecord
		for _, workspaceID := range pebblestore.SessionVideoWorkspaceIDs(session) {
			record, found, err = s.sessions.Store().GetAudioSourceRecord(principal.AccountScopeID, workspaceID, sourceRef)
			if err != nil || found {
				break
			}
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("audio source not found in the session workspace scope"))
			return
		}
		file, openErr := pebblestore.OpenValidatedAudioSource(record)
		if openErr != nil {
			writeError(w, http.StatusNotFound, errors.New("audio source is stale or unavailable"))
			return
		}
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil {
			writeError(w, http.StatusInternalServerError, statErr)
			return
		}
		serveVideoSourceContent(w, r, record.DisplayName, record.MIMEType, info, file)
		return
	}

	var record pebblestore.VideoSourceRecord
	for _, workspaceID := range pebblestore.SessionVideoWorkspaceIDs(session) {
		record, found, err = s.sessions.Store().GetVideoSourceRecord(principal.AccountScopeID, workspaceID, sourceRef)
		if err != nil || found {
			break
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("video source not found in the session workspace scope"))
		return
	}
	file, err := pebblestore.OpenValidatedVideoSource(record)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("video source is stale or unavailable"))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if videoSourceNeedsBrowserPreview(record) {
		preview, previewInfo, previewErr := s.ensureVideoSourceBrowserPreview(r.Context(), record, file)
		if previewErr != nil {
			writeError(w, http.StatusUnprocessableEntity, previewErr)
			return
		}
		defer preview.Close()
		serveVideoSourceContent(w, r, strings.TrimSuffix(record.DisplayName, filepath.Ext(record.DisplayName))+"-preview.mp4", "video/mp4", previewInfo, preview)
		return
	}
	serveVideoSourceContent(w, r, record.DisplayName, record.MIMEType, info, file)
}

func videoSourceNeedsBrowserPreview(record pebblestore.VideoSourceRecord) bool {
	extension := strings.ToLower(filepath.Ext(record.DisplayName))
	return extension == ".mkv" || strings.EqualFold(strings.TrimSpace(record.MIMEType), "video/x-matroska")
}

func (s *Server) ensureVideoSourceBrowserPreview(ctx context.Context, record pebblestore.VideoSourceRecord, source *os.File) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(s.dataDir) == "" {
		return nil, nil, errors.New("video source preview storage is not configured")
	}
	previewDir := filepath.Join(s.dataDir, "video-source-previews")
	if err := os.MkdirAll(previewDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create video source preview storage: %w", err)
	}
	previewPath := filepath.Join(previewDir, strings.TrimPrefix(record.Ref, "videosrc_")+"-"+record.SourceFingerprint+".mp4")
	if preview, info, err := openVideoSourcePreview(previewPath); err == nil {
		return preview, info, nil
	}
	temporary, err := os.CreateTemp(previewDir, ".video-source-preview-*.mp4")
	if err != nil {
		return nil, nil, fmt.Errorf("create video source preview: %w", err)
	}
	temporaryPath := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(temporaryPath)
		return nil, nil, fmt.Errorf("close video source preview target: %w", closeErr)
	}
	defer os.Remove(temporaryPath)

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("rewind video source: %w", err)
	}
	transcodeCtx, cancel := context.WithTimeout(ctx, videoSourcePreviewTimeout)
	defer cancel()
	cmd := exec.CommandContext(transcodeCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "pipe:0", "-map", "0:v:0", "-an",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "22", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", "-y", temporaryPath,
	)
	cmd.Stdin = source
	var stderr strings.Builder
	cmd.Stderr = &boundedVideoPreviewError{writer: &stderr, remaining: 16 << 10}
	if err := cmd.Run(); err != nil {
		if errors.Is(transcodeCtx.Err(), context.DeadlineExceeded) {
			return nil, nil, errors.New("browser-compatible video source preview timed out")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, nil, fmt.Errorf("create browser-compatible video source preview: %s", detail)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return nil, nil, fmt.Errorf("secure video source preview: %w", err)
	}
	if err := os.Rename(temporaryPath, previewPath); err != nil {
		if preview, info, openErr := openVideoSourcePreview(previewPath); openErr == nil {
			return preview, info, nil
		}
		return nil, nil, fmt.Errorf("install video source preview: %w", err)
	}
	preview, info, err := openVideoSourcePreview(previewPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open video source preview: %w", err)
	}
	return preview, info, nil
}

func openVideoSourcePreview(path string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		file.Close()
		if err == nil {
			err = errors.New("video source preview is empty")
		}
		return nil, nil, err
	}
	return file, info, nil
}

func serveVideoSourceContent(w http.ResponseWriter, r *http.Request, displayName, mediaType string, info os.FileInfo, file *os.File) {
	disposition := mime.FormatMediaType("inline", map[string]string{"filename": displayName})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, displayName, info.ModTime(), file)
}

type boundedVideoPreviewError struct {
	writer    io.Writer
	remaining int
}

func (w *boundedVideoPreviewError) Write(payload []byte) (int, error) {
	original := len(payload)
	if len(payload) > w.remaining {
		payload = payload[:w.remaining]
	}
	if len(payload) > 0 {
		_, _ = w.writer.Write(payload)
		w.remaining -= len(payload)
	}
	return original, nil
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
		jobPath := strings.TrimPrefix(subpath, "render-jobs/")
		jobID, action, hasAction := strings.Cut(jobPath, "/")
		if jobID == "" {
			writeError(w, http.StatusBadRequest, errors.New("job id is required"))
			return
		}
		if hasAction {
			if action != "cancel" || r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			if s.videoRender == nil {
				writeError(w, http.StatusInternalServerError, errors.New("video render service is not configured"))
				return
			}
			job, err := s.videoRender.CancelRenderJob(r.Context(), principal, sessionID, jobID)
			if err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "render_job": job})
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
		confirmedRevisionID := project.ConfirmedRevisionID
		if confirmedRevisionID == "" {
			confirmedRevisionID = project.CurrentRevisionID
		}
		if confirmedRevisionID != "" {
			if rev, revOK, revErr := s.videoProjects.GetRevision(principal, sessionID, projectID, confirmedRevisionID); revErr == nil && revOK {
				res["confirmed_revision"] = rev
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
	case "edit-proposals":
		switch r.Method {
		case http.MethodGet:
			proposals, err := s.videoProjects.ListEditProposals(principal, sessionID, projectID, 100)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposals": proposals, "count": len(proposals)})
		case http.MethodPost:
			var req sessionV3CreateVideoEditProposalRequest
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			proposal, err := s.videoProjects.CreateEditProposal(r.Context(), principal, videoproject.CreateEditProposalInput{SessionID: sessionID, ProjectID: projectID, ProposalID: req.ProposalID, BaseRevisionID: req.BaseRevisionID, Title: req.Title, Rationale: req.Rationale, Plan: req.Plan, Operations: req.Operations, AffectedRanges: req.AffectedRanges, NowUnixMs: time.Now().UnixMilli()})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, sessionV3CreateVideoEditProposalResponse{OK: true, Proposal: proposal, ProposalStatus: proposal.Status, RequiresUserAcceptance: true, WorkingRevisionID: proposal.WorkingRevisionID, WorkingRevisionNumber: proposal.WorkingRevisionNumber})
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
			s.videoRender.StartRenderJob(principal, renderReq)
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
		if strings.HasPrefix(rest, "edit-proposals/") {
			path := strings.TrimPrefix(rest, "edit-proposals/")
			proposalID, action, hasAction := strings.Cut(path, "/")
			if proposalID == "" {
				writeError(w, http.StatusBadRequest, errors.New("proposal id is required"))
				return
			}
			if !hasAction {
				if r.Method != http.MethodGet {
					methodNotAllowed(w)
					return
				}
				proposal, ok, err := s.videoProjects.GetEditProposal(principal, sessionID, projectID, proposalID)
				if err != nil || !ok {
					writeError(w, http.StatusNotFound, errors.New("video edit proposal not found"))
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal": proposal})
				return
			}
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			switch action {
			case "accept":
				var req sessionV3AcceptVideoEditProposalRequest
				if err := decodeJSON(r, &req); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				author := req.AuthorPrincipal
				if author == "" {
					author = principal.UserID
				}
				proposal, revision, project, err := s.videoProjects.AcceptEditProposal(r.Context(), principal, videoproject.AcceptEditProposalInput{SessionID: sessionID, ProjectID: projectID, ProposalID: proposalID, RevisionID: req.RevisionID, Description: req.Description, ChangeSummary: req.ChangeSummary, AuthorPrincipal: author, SelectedOperationIDs: req.SelectedOperationIDs, NowUnixMs: time.Now().UnixMilli()})
				if err != nil {
					writeError(w, http.StatusConflict, err)
					return
				}
				writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "proposal": proposal, "revision": revision, "project": project})
				return
			case "reject":
				var req sessionV3RejectVideoEditProposalRequest
				if r.Body != nil && r.ContentLength > 0 {
					if err := decodeJSON(r, &req); err != nil {
						writeError(w, http.StatusBadRequest, err)
						return
					}
				}
				proposal, err := s.videoProjects.RejectEditProposal(r.Context(), principal, sessionID, projectID, proposalID, req.Feedback, time.Now().UnixMilli())
				if err != nil {
					writeError(w, http.StatusConflict, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposal": proposal})
				return
			default:
				writeError(w, http.StatusNotFound, errors.New("invalid video edit proposal action"))
				return
			}
		}
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
			confirmedRevisionID := project.ConfirmedRevisionID
			if confirmedRevisionID == "" {
				confirmedRevisionID = project.CurrentRevisionID
			}
			if confirmedRevisionID != "" {
				if rev, found, getErr := s.videoProjects.GetRevision(principal, sessionID, project.ID, confirmedRevisionID); getErr == nil && found {
					res["confirmed_revision"] = rev
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
