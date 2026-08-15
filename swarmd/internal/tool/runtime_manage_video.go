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
	StartWithFocus(ctx context.Context, principal identity.Principal, sessionID, messageID, focusNotes string) (videotranscription.StartResult, error)
	StartRegisteredSources(ctx context.Context, principal identity.Principal, sessionID string, sources []pebblestore.SessionVideoAttachmentReference, focusNotes string) (videotranscription.StartResult, error)
	Status(principal identity.Principal, sessionID string, refs []string) ([]pebblestore.TranscriptionJob, error)
	Read(principal identity.Principal, sessionID, transcriptRef string) (pebblestore.NormalizedTranscript, error)
	ReadByWorkspace(principal identity.Principal, workspaceID, transcriptRef string) (pebblestore.NormalizedTranscript, error)
	ReadBySourceFingerprint(principal identity.Principal, workspaceID, sourceFingerprint string) (pebblestore.NormalizedTranscript, error)
	Cancel(principal identity.Principal, sessionID, jobRef string) (pebblestore.TranscriptionJob, error)
}

func manageVideoDefinition() Definition {
	return Definition{
		Type: "function", Name: "manage_video",
		Description: "List registered source-video folders, navigate bounded subdirectories, start transcription for selected opaque video references, inspect triggering-message attachments, check jobs, cancel one job, or read a durable transcript. Registered-root discovery, browsing, and selected-reference starts need trusted run plus account/workspace authority but no triggering-message attachment; attachment inspection and attachment-backed starts retain trusted triggering-message authority. Arbitrary paths, provider URIs, credentials, and provider payloads are never accepted or returned.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":             map[string]any{"type": "string", "enum": []string{"list_source_roots", "browse_source", "inspect_attachments", "start_transcription", "status", "cancel", "read_transcript"}},
				"source_root_ref":    map[string]any{"type": "string", "description": "Opaque root reference returned by list_source_roots."},
				"relative_path":      map[string]any{"type": "string", "description": "Bounded path under source_root_ref; use directory relative_path values returned by browse_source."},
				"video_refs":         map[string]any{"type": "array", "maxItems": pebblestore.SessionVideoAttachmentMaxCount, "items": map[string]any{"type": "string"}, "description": "Opaque video references returned by browse_source. With start_transcription, these are transcribed without needing a message attachment."},
				"job_refs":           map[string]any{"type": "array", "maxItems": pebblestore.SessionVideoAttachmentMaxCount, "items": map[string]any{"type": "string"}},
				"job_ref":            map[string]any{"type": "string"},
				"transcript_ref":     map[string]any{"type": "string"},
				"source_fingerprint": map[string]any{"type": "string", "description": "Exact source fingerprint returned for an attached source video; read_transcript may use it to retrieve an existing durable transcript for unchanged media."},
				"focus_notes":        map[string]any{"type": "string", "maxLength": videotranscription.MaxFocusNotesBytes, "description": "Optional job-specific instructions from the initiating user or AI for start_transcription only, for example: 'Silent software demo; produce a dense play-by-play of cursor actions, navigation, text changes, and visible results.' Guidance cannot change the multimodal schema, factuality rules, or source authority."},
				"max_bytes":          map[string]any{"type": "integer", "minimum": 1, "maximum": manageVideoMaxTranscriptBytes},
				"max_segments":       map[string]any{"type": "integer", "minimum": 1, "maximum": manageVideoMaxSegments},
				"start_ms":           map[string]any{"type": "integer", "minimum": 0, "description": "Optional inclusive evidence-range start for bounded transcript retrieval."},
				"end_ms":             map[string]any{"type": "integer", "minimum": 1, "description": "Optional exclusive evidence-range end for bounded transcript retrieval."},
				"include_index":      map[string]any{"type": "boolean", "description": "Derive the compact section index, ranged deduplicated evidence, and conservative splice manifest."},
				"index_only":         map[string]any{"type": "boolean", "description": "Return transcript authority metadata plus the compact index and bounded evidence without hydrating full transcript text or segments."},
			},
			"required": []string{"action"}, "additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageVideo(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.video == nil || r.videoSources == nil || r.sessions == nil {
		return "", errors.New("manage_video service is not configured")
	}
	if !scope.Principal.Valid() || strings.TrimSpace(scope.SessionID) == "" {
		return "", errors.New("manage_video requires authenticated session run context")
	}
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID {
		return "", errors.New("manage_video requires trusted run authority")
	}
	requiresTriggeringMessage := actionRequiresVideoTriggeringMessage(action, args)
	if requiresTriggeringMessage && strings.TrimSpace(run.MessageID) == "" {
		return "", errors.New("manage_video action requires trusted triggering message authority")
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
	var message pebblestore.MessageSnapshot
	if requiresTriggeringMessage {
		message, ok, err = r.sessions.GetV3MessageByID(scope.SessionID, run.MessageID)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("manage_video triggering message not found")
			}
			return "", err
		}
		if message.Role != "user" || message.AccountScopeID != scope.Principal.AccountScopeID || (message.UserID != "" && message.UserID != scope.Principal.UserID) {
			return "", errors.New("manage_video triggering message ownership is invalid")
		}
	}
	response := map[string]any{"tool": "manage_video", "action": action, "status": "ok", "session_id": scope.SessionID, "path_id": toolPathID("manage_video"), "details_truncated": false}
	if run.MessageID != "" {
		response["message_id"] = run.MessageID
	}
	switch action {
	case "list_source_roots":
		workspaceID, roots, err := r.videoSources.ListRoots(scope.Principal, manageVideoWorkspacePath(session))
		if err != nil {
			return "", err
		}
		response["workspace_id"], response["roots"], response["count"] = workspaceID, roots, len(roots)
	case "browse_source":
		result, err := r.videoSources.Browse(scope.Principal, manageVideoWorkspacePath(session), strings.TrimSpace(asString(args["source_root_ref"])), asString(args["relative_path"]))
		if err != nil {
			return "", err
		}
		response["workspace_id"], response["source_root_ref"], response["relative_path"] = result.WorkspaceID, result.RootRef, result.RelativePath
		response["directories"], response["videos"] = result.Directories, result.Clips
		response["directory_count"], response["video_count"] = len(result.Directories), len(result.Clips)
	case "inspect_attachments":
		attachments := make([]map[string]any, 0, len(message.VideoAttachments))
		for _, attachment := range message.VideoAttachments {
			attachments = append(attachments, map[string]any{"ref": attachment.Ref, "name": attachment.Name, "mime_type": attachment.MIMEType, "size_bytes": attachment.SizeBytes, "source_fingerprint": attachment.SourceFingerprint})
		}
		response["attachments"], response["count"] = attachments, len(attachments)
	case "start_transcription":
		focusNotes, err := videotranscription.NormalizeFocusNotes(asString(args["focus_notes"]))
		if err != nil {
			return "", err
		}
		videoRefs, parseErr := parseExactStringSlice(args["video_refs"], "video_refs")
		var started videotranscription.StartResult
		if parseErr == nil && len(videoRefs) > 0 {
			_, records, resolveErr := r.videoSources.ResolveClips(scope.Principal, manageVideoWorkspacePath(session), videoRefs)
			if resolveErr != nil {
				return "", resolveErr
			}
			sources := make([]pebblestore.SessionVideoAttachmentReference, 0, len(records))
			for _, record := range records {
				sources = append(sources, pebblestore.SessionVideoAttachmentReference{Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint})
			}
			started, err = r.video.StartRegisteredSources(ctx, scope.Principal, scope.SessionID, sources, focusNotes)
		} else if parseErr != nil {
			return "", parseErr
		} else {
			started, err = r.video.StartWithFocus(ctx, scope.Principal, scope.SessionID, run.MessageID, focusNotes)
		}
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
		transcriptRef := strings.TrimSpace(asString(args["transcript_ref"]))
		sourceFingerprint := strings.TrimSpace(asString(args["source_fingerprint"]))
		var transcript pebblestore.NormalizedTranscript
		var err error
		if transcriptRef != "" {
			transcript, err = r.video.Read(scope.Principal, scope.SessionID, transcriptRef)
		} else {
			err = errors.New("session transcript reference not supplied")
		}
		if err != nil {
			workspaceID := ""
			for _, key := range []string{"workspace_id", "swarm_v3_source_workspace_id"} {
				if value, ok := session.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
					workspaceID = strings.TrimSpace(value)
					break
				}
			}
			if workspaceID == "" {
				return "", err
			}
			if transcriptRef != "" {
				transcript, err = r.video.ReadByWorkspace(scope.Principal, workspaceID, transcriptRef)
			} else if sourceFingerprint != "" {
				transcript, err = r.video.ReadBySourceFingerprint(scope.Principal, workspaceID, sourceFingerprint)
			} else {
				return "", errors.New("read_transcript requires transcript_ref or source_fingerprint")
			}
			if err != nil {
				return "", err
			}
		}
		maxBytes := asInt(args["max_bytes"], 0)
		if maxBytes <= 0 || maxBytes > manageVideoMaxTranscriptBytes {
			maxBytes = manageVideoMaxTranscriptBytes
		}
		maxSegments := asInt(args["max_segments"], 0)
		if maxSegments <= 0 || maxSegments > manageVideoMaxSegments {
			maxSegments = manageVideoMaxSegments
		}
		indexOnly := asBool(args["index_only"])
		text, textTruncated := boundedUTF8(transcript.Text, maxBytes)
		segments := transcript.Segments
		segmentsTruncated := len(segments) > maxSegments
		if segmentsTruncated {
			segments = segments[:maxSegments]
		}
		if indexOnly {
			text, segments = "", []pebblestore.NormalizedTranscriptSegment{}
			textTruncated, segmentsTruncated = false, false
		}
		response["transcript"] = map[string]any{
			"ref": transcript.Ref, "job_ref": transcript.JobRef, "message_id": transcript.MessageID,
			"attachment_ref": transcript.AttachmentRef, "source_fingerprint": transcript.SourceFingerprint,
			"schema_version": transcript.SchemaVersion, "model_generated": transcript.ModelGenerated,
			"text": text, "segments": segments, "language": transcript.Metadata.Language, "duration_ms": transcript.Metadata.DurationMs,
			"summary": transcript.Metadata.Summary, "content_empty": transcript.Metadata.ContentEmpty,
			"validation": transcript.Validation.State, "content_digest": transcript.ContentDigest,
			"text_truncated": textTruncated, "segments_truncated": segmentsTruncated,
		}
		if asBool(args["include_index"]) || indexOnly {
			index, manifest, indexErr := videotranscription.BuildVideoSectionIndex(transcript)
			if indexErr != nil {
				return "", indexErr
			}
			evidence, _ := videotranscription.BuildVideoEvidence(transcript, int64(asInt(args["start_ms"], 0)), int64(asInt(args["end_ms"], 0)))
			response["section_index"] = index
			response["evidence"] = evidence
			response["splice_manifest"] = manifest
		}
		if indexOnly {
			response["transcript"].(map[string]any)["text_omitted"] = true
			response["transcript"].(map[string]any)["segments_omitted"] = true
		}
		response["details_truncated"] = textTruncated || segmentsTruncated
	default:
		return "", fmt.Errorf("unsupported manage_video action %q", action)
	}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func actionRequiresVideoTriggeringMessage(action string, args map[string]any) bool {
	if action == "inspect_attachments" {
		return true
	}
	if action != "start_transcription" {
		return false
	}
	videoRefs, err := parseExactStringSlice(args["video_refs"], "video_refs")
	return err == nil && len(videoRefs) == 0
}

func manageVideoWorkspacePath(session pebblestore.SessionSnapshot) string {
	if value, ok := session.Metadata["swarm_v3_source_workspace_path"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(session.WorkspacePath)
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
