package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

func (s *Server) resolveDirectVideoSession(principal identity.Principal, workspacePath, sessionID string) (pebblestore.SessionSnapshot, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.workspace == nil || s.videoTranscription == nil {
		return pebblestore.SessionSnapshot{}, errors.New("workspace video transcription is not configured")
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		session, ok, err := s.sessions.Store().GetSession(sessionID)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("transcription session not found")
			}
			return pebblestore.SessionSnapshot{}, err
		}
		if session.AccountScopeID != principal.AccountScopeID ||
			session.UserID != principal.UserID ||
			filepath.Clean(session.WorkspacePath) != filepath.Clean(resolution.WorkspacePath) ||
			transcriptionWorkspaceIDForAPI(session) != resolution.WorkspaceID ||
			session.Metadata["system_session"] != true || session.Metadata["navigation_hidden"] != true || session.Metadata["settings_locked"] != true {
			return pebblestore.SessionSnapshot{}, errors.New("transcription session is outside the authenticated workspace scope")
		}
		return session, nil
	}

	digest := sha256.Sum256([]byte(strings.Join([]string{
		principal.AccountScopeID,
		principal.UserID,
		resolution.WorkspaceID,
		"direct-video-transcription.v1",
	}, "\x00")))
	directSessionID := "media-transcription-v1-" + hex.EncodeToString(digest[:])
	if existing, ok, err := s.sessions.Store().GetSession(directSessionID); err != nil {
		return pebblestore.SessionSnapshot{}, err
	} else if ok {
		if existing.AccountScopeID != principal.AccountScopeID ||
			existing.UserID != principal.UserID ||
			filepath.Clean(existing.WorkspacePath) != filepath.Clean(resolution.WorkspacePath) ||
			transcriptionWorkspaceIDForAPI(existing) != resolution.WorkspaceID ||
			existing.Metadata["system_session"] != true || existing.Metadata["navigation_hidden"] != true || existing.Metadata["settings_locked"] != true {
			return pebblestore.SessionSnapshot{}, errors.New("direct transcription session authority is inconsistent")
		}
		return existing, nil
	}

	now := time.Now().UnixMilli()
	created := pebblestore.SessionSnapshot{
		ID:             directSessionID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Media transcription",
		WorkspacePath:  resolution.WorkspacePath,
		WorkspaceName:  resolution.WorkspaceName,
		Mode:           "auto",
		Metadata: map[string]any{
			"workspace_id":      resolution.WorkspaceID,
			"navigation_hidden": true,
			"system_session":    true,
			"settings_locked":   true,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err = s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       created.ID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "direct-video-session:" + directSessionID,
		IdempotencyKey:  "direct-video-session:" + directSessionID,
		PayloadHash:     hex.EncodeToString(digest[:]),
		RequestHash:     hex.EncodeToString(digest[:]),
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &created,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	stored, ok, err := s.sessions.Store().GetSession(directSessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("direct transcription session was not durably readable")
		}
		return pebblestore.SessionSnapshot{}, err
	}
	return stored, nil
}

func (s *Server) handleWorkspaceVideoTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		VideoRef      string `json:"video_ref"`
		FocusNotes    string `json:"focus_notes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	focusNotes, err := videotranscription.NormalizeFocusNotes(req.FocusNotes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	videoRef := strings.TrimSpace(req.VideoRef)
	if videoRef == "" {
		writeError(w, http.StatusBadRequest, errors.New("video_ref is required"))
		return
	}
	session, err := s.resolveDirectVideoSession(principal, req.WorkspacePath, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	workspaceID := transcriptionWorkspaceIDForAPI(session)
	record, ok, err := s.sessions.Store().GetVideoSourceRecord(principal.AccountScopeID, workspaceID, videoRef)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("video reference is not registered in the authenticated workspace")
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	registeredRoot := false
	for _, root := range resolution.SourceMediaDirectories {
		resolvedRoot, resolveErr := resolveVideoFolderPath(root)
		if resolveErr == nil && resolvedRoot == record.RootPath {
			registeredRoot = true
			break
		}
	}
	if !registeredRoot {
		writeError(w, http.StatusBadRequest, errors.New("video reference no longer belongs to a registered source-media root"))
		return
	}

	messageID := "media-transcription-" + record.SourceFingerprint
	if message, exists, readErr := s.sessions.Store().GetV3MessageByID(session.ID, messageID); readErr != nil {
		writeError(w, http.StatusBadRequest, readErr)
		return
	} else if exists {
		if message.Role != "user" || message.AccountScopeID != principal.AccountScopeID || message.UserID != principal.UserID ||
			len(message.VideoAttachments) != 1 || message.VideoAttachments[0].Ref != record.Ref ||
			message.VideoAttachments[0].SourceFingerprint != record.SourceFingerprint {
			writeError(w, http.StatusConflict, errors.New("direct transcription message authority is inconsistent"))
			return
		}
	} else {
		_, err = s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:       session.ID,
			UserID:          principal.UserID,
			AccountScopeID:  principal.AccountScopeID,
			ClientRequestID: messageID,
			IdempotencyKey:  messageID,
			PayloadHash:     record.SourceFingerprint,
			RequestHash:     record.SourceFingerprint,
			Kind:            pebblestore.V3SessionMutationAppendMessage,
			Message: &pebblestore.MessageSnapshot{
				ID:             messageID,
				SessionID:      session.ID,
				UserID:         principal.UserID,
				AccountScopeID: principal.AccountScopeID,
				Role:           "user",
				Content:        "Direct media transcription",
				VideoAttachments: []pebblestore.SessionVideoAttachmentReference{{
					Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType,
					SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint,
				}},
			},
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	started, err := s.videoTranscription.StartWithFocus(r.Context(), principal, session.ID, messageID, focusNotes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(started.Jobs) != 1 {
		writeError(w, http.StatusInternalServerError, errors.New("direct transcription did not create exactly one job"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": session.ID, "job": safeDirectVideoJob(started.Jobs[0]),
	})
}

func (s *Server) handleWorkspaceVideoTranscribeStatus(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directVideoRequest(w, r, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.JobRef) == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_ref is required"))
		return
	}
	jobs, err := s.videoTranscription.Status(principal, session.ID, []string{strings.TrimSpace(req.JobRef)})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(jobs) != 1 {
		writeError(w, http.StatusInternalServerError, errors.New("transcription status did not return exactly one job"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": safeDirectVideoJob(jobs[0])})
}

func (s *Server) handleWorkspaceVideoTranscribeRead(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directVideoRequest(w, r, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.TranscriptRef) == "" {
		writeError(w, http.StatusBadRequest, errors.New("transcript_ref is required"))
		return
	}
	transcript, err := s.videoTranscription.ReadByWorkspace(principal, transcriptionWorkspaceIDForAPI(session), strings.TrimSpace(req.TranscriptRef))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if transcript.SessionID != session.ID {
		writeError(w, http.StatusBadRequest, errors.New("transcript is outside the direct transcription session scope"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transcript": map[string]any{
		"ref": transcript.Ref, "job_ref": transcript.JobRef, "schema_version": transcript.SchemaVersion,
		"text": transcript.Text, "segments": transcript.Segments, "metadata": transcript.Metadata,
		"validation": transcript.Validation, "content_digest": transcript.ContentDigest,
	}})
}

func (s *Server) handleWorkspaceVideoTranscribeCancel(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directVideoRequest(w, r, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.JobRef) == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_ref is required"))
		return
	}
	job, err := s.videoTranscription.Cancel(principal, session.ID, strings.TrimSpace(req.JobRef))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": safeDirectVideoJob(job)})
}

type directVideoJobRequest struct {
	WorkspacePath string `json:"workspace_path"`
	SessionID     string `json:"session_id"`
	JobRef         string `json:"job_ref"`
	TranscriptRef  string `json:"transcript_ref"`
}

func (s *Server) directVideoRequest(w http.ResponseWriter, r *http.Request, target *directVideoJobRequest) (identity.Principal, pebblestore.SessionSnapshot, bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	if err := decodeJSON(r, target); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	session, err := s.resolveDirectVideoSession(principal, target.WorkspacePath, target.SessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	return principal, session, true
}

func safeDirectVideoJob(job pebblestore.TranscriptionJob) map[string]any {
	return map[string]any{
		"ref": job.Ref, "transcript_ref": job.TranscriptRef, "status": job.Status,
		"failure_code": job.FailureCode, "failure_reason": job.FailureReason,
		"created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "completed_at": job.CompletedAt,
	}
}

func transcriptionWorkspaceIDForAPI(session pebblestore.SessionSnapshot) string {
	for _, key := range []string{"workspace_id", "swarm_v3_source_workspace_id"} {
		if value, ok := session.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	sum := sha256.Sum256([]byte(filepath.Clean(strings.TrimSpace(session.WorkspacePath))))
	return "workspace_" + hex.EncodeToString(sum[:])
}
