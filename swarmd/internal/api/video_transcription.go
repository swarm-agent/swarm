package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	directVideoRequestMaxBytes       = 8 << 10
	directVideoTranscriptMaxBytes    = 512 << 10
	directVideoTranscriptMaxSegments = 500
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
		WorkspacePath string   `json:"workspace_path"`
		VideoRef      string   `json:"video_ref"`
		VideoRefs     []string `json:"video_refs"`
		FocusNotes    string   `json:"focus_notes"`
	}
	if err := decodeJSONLimited(w, r, &req, directVideoRequestMaxBytes); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	focusNotes, err := videotranscription.NormalizeFocusNotes(req.FocusNotes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	videoRefs := make([]string, 0, len(req.VideoRefs)+1)
	seenVideoRefs := make(map[string]struct{}, len(req.VideoRefs)+1)
	for _, candidate := range append([]string{req.VideoRef}, req.VideoRefs...) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, duplicate := seenVideoRefs[candidate]; duplicate {
			continue
		}
		seenVideoRefs[candidate] = struct{}{}
		videoRefs = append(videoRefs, candidate)
	}
	if len(videoRefs) == 0 || len(videoRefs) > pebblestore.SessionVideoAttachmentMaxCount {
		writeError(w, http.StatusBadRequest, fmt.Errorf("video_refs requires between 1 and %d unique video references", pebblestore.SessionVideoAttachmentMaxCount))
		return
	}
	session, err := s.resolveDirectVideoSession(principal, req.WorkspacePath, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	workspaceID := transcriptionWorkspaceIDForAPI(session)
	records := make([]pebblestore.VideoSourceRecord, 0, len(videoRefs))
	for _, videoRef := range videoRefs {
		record, found, readErr := s.sessions.Store().GetVideoSourceRecord(principal.AccountScopeID, workspaceID, videoRef)
		if readErr != nil || !found {
			if readErr == nil {
				readErr = errors.New("video reference is not registered in the authenticated workspace")
			}
			writeError(w, http.StatusBadRequest, readErr)
			return
		}
		records = append(records, record)
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	registeredRoots := make(map[string]struct{}, len(resolution.SourceMediaDirectories))
	for _, root := range resolution.SourceMediaDirectories {
		if resolvedRoot, resolveErr := videosource.ResolveRootPath(root); resolveErr == nil {
			registeredRoots[resolvedRoot] = struct{}{}
		}
	}
	for _, record := range records {
		if _, registered := registeredRoots[record.RootPath]; !registered {
			writeError(w, http.StatusBadRequest, errors.New("video reference no longer belongs to a registered source-media root"))
			return
		}
	}

	fingerprints := make([]string, 0, len(records))
	for _, record := range records {
		fingerprints = append(fingerprints, record.SourceFingerprint)
	}
	messageDigest := sha256.Sum256([]byte(strings.Join(fingerprints, "\x00")))
	messagePayloadHash := hex.EncodeToString(messageDigest[:])
	messageID := "media-transcription-" + messagePayloadHash
	if len(records) == 1 {
		messagePayloadHash = records[0].SourceFingerprint
		messageID = "media-transcription-" + messagePayloadHash
	}
	if message, exists, readErr := s.sessions.Store().GetV3MessageByID(session.ID, messageID); readErr != nil {
		writeError(w, http.StatusBadRequest, readErr)
		return
	} else if exists {
		if message.Role != "user" || message.AccountScopeID != principal.AccountScopeID || message.UserID != principal.UserID || len(message.VideoAttachments) != len(records) {
			writeError(w, http.StatusConflict, errors.New("direct transcription message authority is inconsistent"))
			return
		}
		for index, record := range records {
			if message.VideoAttachments[index].Ref != record.Ref || message.VideoAttachments[index].SourceFingerprint != record.SourceFingerprint {
				writeError(w, http.StatusConflict, errors.New("direct transcription message authority is inconsistent"))
				return
			}
		}
	} else {
		attachments := make([]pebblestore.SessionVideoAttachmentReference, 0, len(records))
		for _, record := range records {
			attachments = append(attachments, pebblestore.SessionVideoAttachmentReference{
				Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType,
				SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint,
			})
		}
		_, err = s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:       session.ID,
			UserID:          principal.UserID,
			AccountScopeID:  principal.AccountScopeID,
			ClientRequestID: messageID,
			IdempotencyKey:  messageID,
			PayloadHash:     messagePayloadHash,
			RequestHash:     messagePayloadHash,
			Kind:            pebblestore.V3SessionMutationAppendMessage,
			Message: &pebblestore.MessageSnapshot{
				ID:               messageID,
				SessionID:        session.ID,
				UserID:           principal.UserID,
				AccountScopeID:   principal.AccountScopeID,
				Role:             "user",
				Content:          "Direct media transcription",
				VideoAttachments: attachments,
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
	if len(started.Jobs) != len(records) {
		writeError(w, http.StatusInternalServerError, errors.New("direct transcription did not create one job per selected video"))
		return
	}
	safeJobs := make([]map[string]any, 0, len(started.Jobs))
	for _, job := range started.Jobs {
		safeJobs = append(safeJobs, safeDirectVideoJob(job))
	}
	response := map[string]any{"ok": true, "session_id": session.ID, "jobs": safeJobs}
	if len(safeJobs) == 1 {
		response["job"] = safeJobs[0]
	}
	writeJSON(w, http.StatusOK, response)
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
	response := map[string]any{"ok": true, "transcript": safeDirectVideoTranscript(transcript)}
	if req.IncludeIndex {
		index, manifest, indexErr := videotranscription.BuildVideoSectionIndex(transcript)
		if indexErr != nil {
			writeError(w, http.StatusBadRequest, indexErr)
			return
		}
		evidence, _ := videotranscription.BuildVideoEvidence(transcript, req.StartMs, req.EndMs)
		response["section_index"] = index
		response["evidence"] = evidence
		response["splice_manifest"] = manifest
	}
	writeJSON(w, http.StatusOK, response)
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
	JobRef        string `json:"job_ref"`
	TranscriptRef string `json:"transcript_ref"`
	StartMs       int64  `json:"start_ms"`
	EndMs         int64  `json:"end_ms"`
	IncludeIndex  bool   `json:"include_index"`
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
	if err := decodeJSONLimited(w, r, target, directVideoRequestMaxBytes); err != nil {
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

func safeDirectVideoTranscript(transcript pebblestore.NormalizedTranscript) map[string]any {
	text, textTruncated := boundedDirectVideoText(transcript.Text, directVideoTranscriptMaxBytes)
	segments := transcript.Segments
	segmentsTruncated := len(segments) > directVideoTranscriptMaxSegments
	if segmentsTruncated {
		segments = segments[:directVideoTranscriptMaxSegments]
	}
	return map[string]any{
		"ref": transcript.Ref, "job_ref": transcript.JobRef, "schema_version": transcript.SchemaVersion,
		"text": text, "segments": segments,
		"metadata": map[string]any{
			"language": transcript.Metadata.Language, "duration_ms": transcript.Metadata.DurationMs,
			"summary": transcript.Metadata.Summary, "content_empty": transcript.Metadata.ContentEmpty,
		},
		"validation": transcript.Validation, "content_digest": transcript.ContentDigest,
		"text_truncated": textTruncated, "segments_truncated": segmentsTruncated,
		"details_truncated": textTruncated || segmentsTruncated,
	}
}

func boundedDirectVideoText(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
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
