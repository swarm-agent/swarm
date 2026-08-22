package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	directAudioTranscriptMaxWords    = 10_000
	directAudioAnalysisMaxLevels     = 10_000
	directAudioAnalysisMaxEvents     = 10_000
	directAudioAnalysisMaxSections   = 1_000
)

func (s *Server) handleWorkspaceAudioTranscribe(w http.ResponseWriter, r *http.Request) {
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
		AudioRef      string   `json:"audio_ref"`
		AudioRefs     []string `json:"audio_refs"`
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
	refs := make([]string, 0, len(req.AudioRefs)+1)
	seen := make(map[string]struct{}, len(req.AudioRefs)+1)
	for _, ref := range append([]string{req.AudioRef}, req.AudioRefs...) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.workspace == nil || s.videoTranscription == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace audio transcription is not configured"))
		return
	}
	workspaceID, records, err := videosource.NewService(s.workspace, s.sessions.Store()).ResolveAudioClips(principal, req.WorkspacePath, refs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.resolveDirectVideoSession(principal, req.WorkspacePath, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if transcriptionWorkspaceIDForAPI(session) != workspaceID {
		writeError(w, http.StatusConflict, errors.New("audio transcription session workspace authority is inconsistent"))
		return
	}
	sources := make([]pebblestore.AudioSourceReference, 0, len(records))
	for _, record := range records {
		sources = append(sources, exactAudioSourceReference(record))
	}
	started, err := s.videoTranscription.StartRegisteredAudioSources(r.Context(), principal, session.ID, sources, focusNotes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(started.Jobs) != len(records) {
		writeError(w, http.StatusInternalServerError, errors.New("audio transcription did not create one job per selected audio source"))
		return
	}
	jobs := make([]map[string]any, 0, len(started.Jobs))
	for _, job := range started.Jobs {
		jobs = append(jobs, safeDirectVideoJob(job))
	}
	response := map[string]any{"ok": true, "session_id": session.ID, "jobs": jobs}
	if len(jobs) == 1 {
		response["job"] = jobs[0]
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleWorkspaceAudioTranscribeStatus(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directAudioRequest(w, r, &req)
	if !ok {
		return
	}
	job, ok := s.directAudioJob(w, principal, session, req.JobRef)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": safeDirectVideoJob(job)})
}

func (s *Server) handleWorkspaceAudioTranscribeRead(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directAudioRequest(w, r, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.TranscriptRef) == "" {
		writeError(w, http.StatusBadRequest, errors.New("transcript_ref is required"))
		return
	}
	transcript, err := s.videoTranscription.ReadByWorkspace(principal, transcriptionWorkspaceIDForAPI(session), strings.TrimSpace(req.TranscriptRef))
	if err != nil || transcript.SessionID != session.ID {
		if err == nil {
			err = errors.New("transcript is outside the audio transcription session scope")
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, found, err := s.sessions.Store().GetTranscriptionJob(principal.AccountScopeID, session.ID, transcript.JobRef)
	if err != nil || !found {
		if err == nil {
			err = errors.New("audio transcript job not found")
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.requireAudioJob(principal, job); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transcript": safeDirectAudioTranscript(transcript)})
}

func (s *Server) handleWorkspaceAudioTranscribeCancel(w http.ResponseWriter, r *http.Request) {
	var req directVideoJobRequest
	principal, session, ok := s.directAudioRequest(w, r, &req)
	if !ok {
		return
	}
	job, ok := s.directAudioJob(w, principal, session, req.JobRef)
	if !ok {
		return
	}
	cancelled, err := s.videoTranscription.Cancel(principal, session.ID, job.Ref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": safeDirectVideoJob(cancelled)})
}

func (s *Server) handleWorkspaceAudioAnalysisRead(w http.ResponseWriter, r *http.Request) {
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
		AudioRef      string `json:"audio_ref"`
	}
	if err := decodeJSONLimited(w, r, &req, directVideoRequestMaxBytes); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.workspace == nil || s.videoTranscription == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace audio analysis is not configured"))
		return
	}
	workspaceID, records, err := videosource.NewService(s.workspace, s.sessions.Store()).ResolveAudioClips(principal, req.WorkspacePath, []string{req.AudioRef})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	analysis, err := s.videoTranscription.ReadAudioAnalysis(principal, workspaceID, exactAudioSourceReference(records[0]))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "analysis": safeDirectAudioAnalysis(analysis)})
}

func (s *Server) directAudioRequest(w http.ResponseWriter, r *http.Request, req *directVideoJobRequest) (identity.Principal, pebblestore.SessionSnapshot, bool) {
	principal, session, ok := s.directVideoRequest(w, r, req)
	if !ok {
		return principal, session, false
	}
	return principal, session, true
}

func (s *Server) directAudioJob(w http.ResponseWriter, principal identity.Principal, session pebblestore.SessionSnapshot, ref string) (pebblestore.TranscriptionJob, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_ref is required"))
		return pebblestore.TranscriptionJob{}, false
	}
	persisted, found, err := s.sessions.Store().GetTranscriptionJob(principal.AccountScopeID, session.ID, ref)
	if err != nil || !found || persisted.UserID != principal.UserID {
		if err == nil {
			err = errors.New("audio transcription job not found in authenticated session scope")
		}
		writeError(w, http.StatusBadRequest, err)
		return pebblestore.TranscriptionJob{}, false
	}
	if err := s.requireAudioJob(principal, persisted); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return pebblestore.TranscriptionJob{}, false
	}
	jobs, err := s.videoTranscription.Status(principal, session.ID, []string{ref})
	if err != nil || len(jobs) != 1 {
		if err == nil {
			err = errors.New("audio transcription status did not return exactly one job")
		}
		writeError(w, http.StatusBadRequest, err)
		return pebblestore.TranscriptionJob{}, false
	}
	if err := s.requireAudioJob(principal, jobs[0]); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return pebblestore.TranscriptionJob{}, false
	}
	return jobs[0], true
}

func (s *Server) requireAudioJob(principal identity.Principal, job pebblestore.TranscriptionJob) error {
	attachment, found, err := s.sessions.Store().GetTranscriptionAttachment(principal.AccountScopeID, job.SessionID, job.AttachmentRef)
	if err != nil || !found {
		if err == nil {
			err = errors.New("audio transcription attachment not found")
		}
		return err
	}
	if attachment.MediaKind != pebblestore.TranscriptionMediaAudio {
		return errors.New("transcription job is not an audio job")
	}
	return nil
}

func exactAudioSourceReference(record pebblestore.AudioSourceRecord) pebblestore.AudioSourceReference {
	return pebblestore.AudioSourceReference{Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion}
}

func safeDirectAudioTranscript(transcript pebblestore.NormalizedTranscript) map[string]any {
	result := safeDirectVideoTranscript(transcript)
	words := transcript.Words
	truncated := len(words) > directAudioTranscriptMaxWords
	if truncated {
		words = words[:directAudioTranscriptMaxWords]
	}
	result["words"] = words
	result["words_truncated"] = truncated
	if truncated {
		result["details_truncated"] = true
	}
	return result
}

func safeDirectAudioAnalysis(snapshot pebblestore.AudioAnalysisSnapshot) map[string]any {
	levels, levelsTruncated := boundedSlice(snapshot.Levels, directAudioAnalysisMaxLevels)
	onsets, onsetsTruncated := boundedSlice(snapshot.Onsets, directAudioAnalysisMaxEvents)
	beats, beatsTruncated := boundedSlice(snapshot.Beats, directAudioAnalysisMaxEvents)
	sections, sectionsTruncated := boundedSlice(snapshot.Sections, directAudioAnalysisMaxSections)
	return map[string]any{
		"ref": snapshot.Ref, "schema_version": snapshot.SchemaVersion, "source_ref": snapshot.SourceRef,
		"analyzer_version": snapshot.AnalyzerVersion, "duration_ms": snapshot.DurationMs, "sample_interval_ms": snapshot.SampleIntervalMs,
		"levels": levels, "onsets": onsets, "tempo": snapshot.Tempo, "beats": beats, "sections": sections,
		"content_digest": snapshot.ContentDigest,
		"levels_truncated": levelsTruncated, "onsets_truncated": onsetsTruncated, "beats_truncated": beatsTruncated, "sections_truncated": sectionsTruncated,
		"details_truncated": levelsTruncated || onsetsTruncated || beatsTruncated || sectionsTruncated,
	}
}

func boundedSlice[T any](values []T, limit int) ([]T, bool) {
	if len(values) <= limit {
		return values, false
	}
	return values[:limit], true
}

