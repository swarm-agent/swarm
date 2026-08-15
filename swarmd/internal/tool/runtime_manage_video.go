package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	manageVideoMaxTranscriptBytes = 64 << 10
	manageVideoMaxSegments        = 200
)

type manageVideoService interface {
	Start(ctx context.Context, principal identity.Principal, sessionID, messageID string) (videotranscription.StartResult, error)
	Status(principal identity.Principal, sessionID string, refs []string) ([]pebblestore.TranscriptionJob, error)
	Read(principal identity.Principal, sessionID, transcriptRef string) (pebblestore.NormalizedTranscript, error)
	Cancel(principal identity.Principal, sessionID, jobRef string) (pebblestore.TranscriptionJob, error)
}

func manageVideoDefinition() Definition {
	return Definition{
		Type: "function", Name: "manage_video",
		Description: "Inspect video attachments on the trusted triggering user message, start a bounded transcription batch, check exact job references, cancel one job, or read one durably stored transcript by exact opaque reference. Session, account, workspace, message, and source authority come only from trusted run context; paths, provider URIs, credentials, and provider payloads are never accepted or returned.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":         map[string]any{"type": "string", "enum": []string{"inspect_attachments", "start_transcription", "status", "cancel", "read_transcript"}},
				"job_refs":       map[string]any{"type": "array", "maxItems": pebblestore.SessionVideoAttachmentMaxCount, "items": map[string]any{"type": "string"}},
				"job_ref":        map[string]any{"type": "string"},
				"transcript_ref": map[string]any{"type": "string"},
				"max_bytes":      map[string]any{"type": "integer", "minimum": 1, "maximum": manageVideoMaxTranscriptBytes},
				"max_segments":   map[string]any{"type": "integer", "minimum": 1, "maximum": manageVideoMaxSegments},
			},
			"required": []string{"action"}, "additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageVideo(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.video == nil || r.sessions == nil {
		return "", errors.New("manage_video service is not configured")
	}
	if !scope.Principal.Valid() || strings.TrimSpace(scope.SessionID) == "" {
		return "", errors.New("manage_video requires authenticated session run context")
	}
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID || strings.TrimSpace(run.MessageID) == "" || strings.TrimSpace(run.RunID) == "" {
		return "", errors.New("manage_video requires trusted triggering message and run authority")
	}
	session, ok, err := r.sessions.GetSession(scope.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("manage_video session not found")
		}
		return "", err
	}
	if session.AccountScopeID != scope.Principal.AccountScopeID || (session.UserID != "" && session.UserID != scope.Principal.UserID) {
		return "", errors.New("manage_video session ownership does not match authenticated principal")
	}
	message, ok, err := r.sessions.GetV3MessageByID(scope.SessionID, run.MessageID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("manage_video triggering message not found")
		}
		return "", err
	}
	if message.Role != "user" || message.AccountScopeID != scope.Principal.AccountScopeID || (message.UserID != "" && message.UserID != scope.Principal.UserID) {
		return "", errors.New("manage_video triggering message ownership is invalid")
	}
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	response := map[string]any{"tool": "manage_video", "action": action, "status": "ok", "session_id": scope.SessionID, "message_id": run.MessageID, "path_id": toolPathID("manage_video"), "details_truncated": false}
	switch action {
	case "inspect_attachments":
		attachments := make([]map[string]any, 0, len(message.VideoAttachments))
		for _, attachment := range message.VideoAttachments {
			attachments = append(attachments, map[string]any{"ref": attachment.Ref, "name": attachment.Name, "mime_type": attachment.MIMEType, "size_bytes": attachment.SizeBytes, "source_fingerprint": attachment.SourceFingerprint})
		}
		response["attachments"], response["count"] = attachments, len(attachments)
	case "start_transcription":
		started, err := r.video.Start(ctx, scope.Principal, scope.SessionID, run.MessageID)
		if err != nil {
			return "", err
		}
		response["jobs"], response["count"] = safeVideoJobs(started.Jobs), len(started.Jobs)
	case "status":
		refs, err := parseExactStringSlice(args["job_refs"], "job_refs")
		if err != nil {
			return "", err
		}
		jobs, err := r.video.Status(scope.Principal, scope.SessionID, refs)
		if err != nil {
			return "", err
		}
		response["jobs"], response["count"] = safeVideoJobs(jobs), len(jobs)
	case "cancel":
		job, err := r.video.Cancel(scope.Principal, scope.SessionID, strings.TrimSpace(asString(args["job_ref"])))
		if err != nil {
			return "", err
		}
		response["job"] = safeVideoJob(job)
	case "read_transcript":
		transcript, err := r.video.Read(scope.Principal, scope.SessionID, strings.TrimSpace(asString(args["transcript_ref"])))
		if err != nil {
			return "", err
		}
		maxBytes := asInt(args["max_bytes"], 0)
		if maxBytes <= 0 || maxBytes > manageVideoMaxTranscriptBytes {
			maxBytes = manageVideoMaxTranscriptBytes
		}
		maxSegments := asInt(args["max_segments"], 0)
		if maxSegments <= 0 || maxSegments > manageVideoMaxSegments {
			maxSegments = manageVideoMaxSegments
		}
		text, textTruncated := boundedUTF8(transcript.Text, maxBytes)
		segments := transcript.Segments
		segmentsTruncated := len(segments) > maxSegments
		if segmentsTruncated {
			segments = segments[:maxSegments]
		}
		response["transcript"] = map[string]any{
			"ref": transcript.Ref, "job_ref": transcript.JobRef, "message_id": transcript.MessageID,
			"attachment_ref": transcript.AttachmentRef, "source_fingerprint": transcript.SourceFingerprint,
			"schema_version": transcript.SchemaVersion, "model_generated": transcript.ModelGenerated,
			"text": text, "segments": segments, "language": transcript.Metadata.Language, "duration_ms": transcript.Metadata.DurationMs,
			"validation": transcript.Validation.State, "content_digest": transcript.ContentDigest,
			"text_truncated": textTruncated, "segments_truncated": segmentsTruncated,
		}
		response["details_truncated"] = textTruncated || segmentsTruncated
	default:
		return "", fmt.Errorf("unsupported manage_video action %q", action)
	}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func safeVideoJobs(jobs []pebblestore.TranscriptionJob) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, safeVideoJob(job))
	}
	return out
}

func safeVideoJob(job pebblestore.TranscriptionJob) map[string]any {
	return map[string]any{
		"job_ref": job.Ref, "transcript_ref": job.TranscriptRef, "status": job.Status,
		"message_id": job.MessageID, "attachment_ref": job.AttachmentRef, "source_fingerprint": job.SourceFingerprint,
		"model_generated": true, "failure_code": job.FailureCode, "failure_reason": job.FailureReason,
		"created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "completed_at": job.CompletedAt,
	}
}

func boundedUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value, true
}
