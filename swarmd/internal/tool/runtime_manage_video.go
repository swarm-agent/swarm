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
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
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

type manageVideoProjectService interface {
	CreateProject(ctx context.Context, principal identity.Principal, input videoproject.CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error)
	CreateRevision(ctx context.Context, principal identity.Principal, input videoproject.CreateRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error)
	StartRenderJob(ctx context.Context, principal identity.Principal, input videoproject.StartRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error)
	GetProject(principal identity.Principal, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error)
	GetRevision(principal identity.Principal, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error)
	ListProjects(principal identity.Principal, sessionID string, limit int) ([]pebblestore.VideoProjectSnapshot, error)
	ListRevisions(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoProjectRevisionSnapshot, error)
	GetRenderJob(principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
	ListRenderJobs(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
}

type manageVideoRenderService interface {
	RenderJob(ctx context.Context, principal identity.Principal, req videorender.RenderJobRequest) (pebblestore.VideoRenderJobSnapshot, error)
	CancelRenderJob(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, error)
	GetRenderJobStatus(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
}

func manageVideoDefinition() Definition {
	return Definition{
		Type: "function", Name: "manage_video",
		Description: "List registered source-video folders, navigate bounded subdirectories, start transcription for selected opaque video references, inspect triggering-message attachments, check jobs, cancel one job, or read a durable transcript. Also create session-owned video projects, manage timeline revisions referencing source clips or managed artifacts, start bounded background renders, check render status, or cancel renders. Arbitrary paths, provider URIs, credentials, and provider payloads are never accepted or returned.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":             map[string]any{"type": "string", "enum": []string{"list_source_roots", "browse_source", "inspect_attachments", "start_transcription", "status", "cancel", "read_transcript", "create_project", "read_project", "get_project", "list_projects", "create_revision", "start_render", "render_status", "cancel_render"}},
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
				"project_id":         map[string]any{"type": "string", "description": "Opaque video project identifier for reading, revising, or rendering a project."},
				"revision_id":        map[string]any{"type": "string", "description": "Optional opaque project revision identifier."},
				"render_job_id":      map[string]any{"type": "string", "description": "Opaque render job identifier for checking status or cancelling a render."},
				"title":              map[string]any{"type": "string", "description": "Human-readable video project title."},
				"description":        map[string]any{"type": "string", "description": "Optional description for a video project or revision."},
				"output_preset":      map[string]any{"type": "string", "description": "Target video format preset (e.g. landscape_1080p, landscape_720p, portrait_1080p, portrait_720p, square_1080p, landscape_video, portrait_video, x_header)."},
				"change_summary":     map[string]any{"type": "string", "description": "Summary of changes made in this revision."},
				"timeline":           map[string]any{"type": "object", "description": "Structured video project timeline with clips, captions, and audio policy."},
				"initial_timeline":   map[string]any{"type": "object", "description": "Initial structured timeline when creating a video project."},
				"metadata":           map[string]any{"type": "object", "description": "Optional unstructured metadata for the video project."},
			},
			"required": []string{"action"}, "additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageVideo(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil {
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
		if r.videoSources == nil {
			return "", errors.New("manage_video source service is not configured")
		}
		workspaceID, roots, err := r.videoSources.ListRoots(scope.Principal, manageVideoWorkspacePath(session))
		if err != nil {
			return "", err
		}
		response["workspace_id"], response["roots"], response["count"] = workspaceID, roots, len(roots)
	case "browse_source":
		if r.videoSources == nil {
			return "", errors.New("manage_video source service is not configured")
		}
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
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
		focusNotes, err := videotranscription.NormalizeFocusNotes(asString(args["focus_notes"]))
		if err != nil {
			return "", err
		}
		videoRefs, parseErr := parseExactStringSlice(args["video_refs"], "video_refs")
		var started videotranscription.StartResult
		if parseErr == nil && len(videoRefs) > 0 {
			if r.videoSources == nil {
				return "", errors.New("manage_video source service is not configured")
			}
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
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
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
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
		job, err := r.video.Cancel(scope.Principal, scope.SessionID, strings.TrimSpace(asString(args["job_ref"])))
		if err != nil {
			return "", err
		}
		response["job"] = safeVideoJob(job)
	case "read_transcript":
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
		transcriptRef := strings.TrimSpace(asString(args["transcript_ref"]))
		sourceFingerprint := strings.TrimSpace(asString(args["source_fingerprint"]))
		var transcript pebblestore.NormalizedTranscript
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

	case "create_project":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		title := strings.TrimSpace(asString(args["title"]))
		if title == "" {
			title = "Untitled Video Project"
		}
		description := strings.TrimSpace(asString(args["description"]))
		outputPreset := strings.TrimSpace(asString(args["output_preset"]))
		if outputPreset == "" {
			outputPreset = pebblestore.VideoPresetLandscape1080p
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		var initialTimeline *pebblestore.VideoProjectTimeline
		if rawTimeline, ok := args["initial_timeline"]; ok && rawTimeline != nil {
			tl, err := parseTimeline(rawTimeline)
			if err != nil {
				return "", err
			}
			initialTimeline = tl
		} else if rawTimeline, ok := args["timeline"]; ok && rawTimeline != nil {
			tl, err := parseTimeline(rawTimeline)
			if err != nil {
				return "", err
			}
			initialTimeline = tl
		}
		var meta map[string]any
		if rawMeta, ok := args["metadata"].(map[string]any); ok {
			meta = rawMeta
		}
		workspaceID := ""
		for _, key := range []string{"workspace_id", "swarm_v3_source_workspace_id"} {
			if value, ok := session.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				workspaceID = strings.TrimSpace(value)
				break
			}
		}
		project, revision, err := r.videoProjects.CreateProject(ctx, scope.Principal, videoproject.CreateProjectInput{
			SessionID:       scope.SessionID,
			WorkspaceID:     workspaceID,
			ProjectID:       projectID,
			Title:           title,
			Description:     description,
			OutputPreset:    outputPreset,
			InitialTimeline: initialTimeline,
			Metadata:        meta,
		})
		if err != nil {
			return "", err
		}
		response["project"] = safeVideoProject(project)
		response["project_id"] = project.ID
		if revision != nil {
			response["revision"] = safeVideoProjectRevision(revision)
			response["revision_id"] = revision.ID
		}

	case "read_project", "get_project":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projects, err := r.videoProjects.ListProjects(scope.Principal, scope.SessionID, 50)
			if err != nil {
				return "", err
			}
			response["projects"] = safeVideoProjects(projects)
			response["count"] = len(projects)
		} else {
			project, ok, err := r.videoProjects.GetProject(scope.Principal, scope.SessionID, projectID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("video project %q not found", projectID)
				}
				return "", err
			}
			response["project"] = safeVideoProject(project)
			response["project_id"] = project.ID
			if project.CurrentRevisionID != "" {
				if rev, revOK, revErr := r.videoProjects.GetRevision(scope.Principal, scope.SessionID, projectID, project.CurrentRevisionID); revErr == nil && revOK {
					response["current_revision"] = safeVideoProjectRevision(&rev)
				}
			}
			if project.ActiveRenderJobID != "" {
				if job, jobOK, jobErr := r.videoProjects.GetRenderJob(scope.Principal, scope.SessionID, project.ActiveRenderJobID); jobErr == nil && jobOK {
					response["active_render_job"] = safeVideoRenderJob(job)
				}
			}
			if revs, revsErr := r.videoProjects.ListRevisions(scope.Principal, scope.SessionID, projectID, 10); revsErr == nil {
				response["revisions"] = safeVideoProjectRevisions(revs)
			}
		}

	case "list_projects":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projects, err := r.videoProjects.ListProjects(scope.Principal, scope.SessionID, 50)
		if err != nil {
			return "", err
		}
		response["projects"] = safeVideoProjects(projects)
		response["count"] = len(projects)

	case "create_revision":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			return "", errors.New("create_revision requires project_id")
		}
		rawTimeline, ok := args["timeline"]
		if !ok || rawTimeline == nil {
			return "", errors.New("create_revision requires timeline")
		}
		timeline, err := parseTimeline(rawTimeline)
		if err != nil {
			return "", err
		}
		if timeline == nil {
			return "", errors.New("create_revision requires non-empty timeline")
		}
		revisionID := strings.TrimSpace(asString(args["revision_id"]))
		description := strings.TrimSpace(asString(args["description"]))
		changeSummary := strings.TrimSpace(asString(args["change_summary"]))
		revision, project, err := r.videoProjects.CreateRevision(ctx, scope.Principal, videoproject.CreateRevisionInput{
			SessionID:       scope.SessionID,
			ProjectID:       projectID,
			RevisionID:      revisionID,
			Description:     description,
			ChangeSummary:   changeSummary,
			Timeline:        *timeline,
			AuthorPrincipal: scope.Principal.UserID,
		})
		if err != nil {
			return "", err
		}
		response["project"] = safeVideoProject(project)
		response["project_id"] = project.ID
		response["revision"] = safeVideoProjectRevision(&revision)
		response["revision_id"] = revision.ID
		response["revision_number"] = revision.RevisionNumber

	case "start_render":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			return "", errors.New("start_render requires project_id")
		}
		revisionID := strings.TrimSpace(asString(args["revision_id"]))
		jobID := strings.TrimSpace(firstNonEmptyString(asString(args["render_job_id"]), asString(args["job_id"])))
		job, err := r.videoProjects.StartRenderJob(ctx, scope.Principal, videoproject.StartRenderJobInput{
			SessionID:  scope.SessionID,
			ProjectID:  projectID,
			RevisionID: revisionID,
			JobID:      jobID,
		})
		if err != nil {
			return "", err
		}
		if r.videoRender != nil {
			workspacePath := manageVideoWorkspacePath(session)
			go func(jID, rID string) {
				_, _ = r.videoRender.RenderJob(context.Background(), scope.Principal, videorender.RenderJobRequest{
					SessionID:     scope.SessionID,
					ProjectID:     projectID,
					RevisionID:    rID,
					JobID:         jID,
					WorkspacePath: workspacePath,
				})
			}(job.ID, job.RevisionID)
		}
		response["render_job"] = safeVideoRenderJob(job)
		response["job_id"] = job.ID
		response["project_id"] = job.ProjectID
		response["revision_id"] = job.RevisionID
		response["revision_number"] = job.RevisionNumber
		response["status"] = job.Status

	case "render_status":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		jobID := strings.TrimSpace(firstNonEmptyString(asString(args["render_job_id"]), asString(args["job_id"]), asString(args["job_ref"])))
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if jobID != "" {
			job, ok, err := r.videoProjects.GetRenderJob(scope.Principal, scope.SessionID, jobID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("render job %q not found", jobID)
				}
				return "", err
			}
			response["render_job"] = safeVideoRenderJob(job)
			response["job_id"] = job.ID
			response["status"] = job.Status
			response["progress"] = job.Progress
			if job.OutputArtifact != nil {
				response["output_artifact"] = job.OutputArtifact
			}
		} else if projectID != "" {
			jobs, err := r.videoProjects.ListRenderJobs(scope.Principal, scope.SessionID, projectID, 50)
			if err != nil {
				return "", err
			}
			response["render_jobs"] = safeVideoRenderJobs(jobs)
			response["count"] = len(jobs)
		} else {
			return "", errors.New("render_status requires render_job_id or project_id")
		}

	case "cancel_render":
		jobID := strings.TrimSpace(firstNonEmptyString(asString(args["render_job_id"]), asString(args["job_id"]), asString(args["job_ref"])))
		if jobID == "" {
			return "", errors.New("cancel_render requires render_job_id")
		}
		if r.videoRender != nil {
			job, err := r.videoRender.CancelRenderJob(ctx, scope.Principal, scope.SessionID, jobID)
			if err != nil {
				return "", err
			}
			response["render_job"] = safeVideoRenderJob(job)
			response["job_id"] = job.ID
			response["status"] = job.Status
		} else {
			return "", errors.New("manage_video render service is not configured")
		}

	default:
		return "", fmt.Errorf("unsupported manage_video action %q", action)
	}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func parseTimeline(raw any) (*pebblestore.VideoProjectTimeline, error) {
	if raw == nil {
		return nil, nil
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal timeline: %w", err)
	}
	var timeline pebblestore.VideoProjectTimeline
	if err := json.Unmarshal(bytes, &timeline); err != nil {
		return nil, fmt.Errorf("invalid timeline payload: %w", err)
	}
	if timeline.SchemaVersion == 0 {
		timeline.SchemaVersion = pebblestore.VideoTimelineSchemaVersion
	}
	return &timeline, nil
}

func safeVideoProjects(projects []pebblestore.VideoProjectSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		out = append(out, safeVideoProject(p))
	}
	return out
}

func safeVideoProject(project pebblestore.VideoProjectSnapshot) map[string]any {
	res := map[string]any{
		"id":                      project.ID,
		"session_id":              project.SessionID,
		"title":                   project.Title,
		"description":             project.Description,
		"output_preset":           project.OutputPreset,
		"current_revision_id":     project.CurrentRevisionID,
		"current_revision_number": project.CurrentRevisionNumber,
		"revision_count":          project.RevisionCount,
		"active_render_job_id":    project.ActiveRenderJobID,
		"created_at":              project.CreatedAt,
		"updated_at":              project.UpdatedAt,
	}
	if len(project.Metadata) > 0 {
		res["metadata"] = project.Metadata
	}
	return res
}

func safeVideoProjectRevisions(revs []pebblestore.VideoProjectRevisionSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(revs))
	for _, r := range revs {
		out = append(out, safeVideoProjectRevision(&r))
	}
	return out
}

func safeVideoProjectRevision(rev *pebblestore.VideoProjectRevisionSnapshot) map[string]any {
	if rev == nil {
		return nil
	}
	return map[string]any{
		"id":                 rev.ID,
		"project_id":         rev.ProjectID,
		"revision_number":    rev.RevisionNumber,
		"session_id":         rev.SessionID,
		"parent_revision_id": rev.ParentRevisionID,
		"description":        rev.Description,
		"change_summary":     rev.ChangeSummary,
		"timeline":           rev.Timeline,
		"created_at":         rev.CreatedAt,
	}
}

func safeVideoRenderJobs(jobs []pebblestore.VideoRenderJobSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, safeVideoRenderJob(j))
	}
	return out
}

func safeVideoRenderJob(job pebblestore.VideoRenderJobSnapshot) map[string]any {
	res := map[string]any{
		"id":              job.ID,
		"project_id":      job.ProjectID,
		"revision_id":     job.RevisionID,
		"revision_number": job.RevisionNumber,
		"session_id":      job.SessionID,
		"status":          job.Status,
		"progress":        job.Progress,
		"created_at":      job.CreatedAt,
		"updated_at":      job.UpdatedAt,
	}
	if job.FailureCode != "" {
		res["failure_code"] = job.FailureCode
	}
	if job.FailureReason != "" {
		res["failure_reason"] = job.FailureReason
	}
	if job.OutputPreset != "" {
		res["output_preset"] = job.OutputPreset
	}
	if job.OutputWidth > 0 {
		res["output_width"] = job.OutputWidth
	}
	if job.OutputHeight > 0 {
		res["output_height"] = job.OutputHeight
	}
	if job.OutputFPS > 0 {
		res["output_fps"] = job.OutputFPS
	}
	if job.OutputDurationMs > 0 {
		res["output_duration_ms"] = job.OutputDurationMs
	}
	if job.OutputSizeBytes > 0 {
		res["output_size_bytes"] = job.OutputSizeBytes
	}
	if job.OutputDigestSHA256 != "" {
		res["output_digest_sha256"] = job.OutputDigestSHA256
	}
	if job.OutputArtifact != nil {
		res["output_artifact"] = job.OutputArtifact
	}
	if job.StartedAt > 0 {
		res["started_at"] = job.StartedAt
	}
	if job.CompletedAt > 0 {
		res["completed_at"] = job.CompletedAt
	}
	return res
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
