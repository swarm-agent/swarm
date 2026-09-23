package tool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/artifactv2"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videocomposition"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	manageVideoMaxTranscriptBytes = 64 << 10
	manageVideoMaxSegments        = 200
	manageVideoMaxWords           = 2_000
	manageVideoMaxAnalysisPoints  = 5_000
)

// Conversion responses expose only a digest-verified bounded prefix for
// container inspection; complete derivatives remain private render inputs.
const manageVideoDerivativeEvidencePrefix = 64

type manageVideoService interface {
	StartWithFocus(ctx context.Context, principal identity.Principal, sessionID, messageID, focusNotes string) (videotranscription.StartResult, error)
	StartRegisteredSources(ctx context.Context, principal identity.Principal, sessionID string, sources []pebblestore.SessionVideoAttachmentReference, focusNotes string) (videotranscription.StartResult, error)
	StartRegisteredAudioSources(ctx context.Context, principal identity.Principal, sessionID string, sources []pebblestore.AudioSourceReference, focusNotes string) (videotranscription.StartResult, error)
	Status(principal identity.Principal, sessionID string, refs []string) ([]pebblestore.TranscriptionJob, error)
	Read(principal identity.Principal, sessionID, transcriptRef string) (pebblestore.NormalizedTranscript, error)
	ReadByWorkspace(principal identity.Principal, workspaceID, transcriptRef string) (pebblestore.NormalizedTranscript, error)
	ReadBySourceFingerprint(principal identity.Principal, workspaceID, sourceFingerprint string) (pebblestore.NormalizedTranscript, error)
	ReadAudioAnalysisByWorkspace(principal identity.Principal, workspaceID, analysisRef, sourceFingerprint string) (pebblestore.AudioAnalysisSnapshot, error)
	Cancel(principal identity.Principal, sessionID, jobRef string) (pebblestore.TranscriptionJob, error)
	SourceName(principal identity.Principal, sessionID, attachmentRef string) (string, error)
}

type manageVideoProjectService interface {
	CreateProject(ctx context.Context, principal identity.Principal, input videoproject.CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error)
	GetOrCreatePrimaryVideoToolProject(ctx context.Context, principal identity.Principal, input videoproject.CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error)
	CreateRevision(ctx context.Context, principal identity.Principal, input videoproject.CreateRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error)
	RestoreRevision(ctx context.Context, principal identity.Principal, input videoproject.RestoreRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error)
	StartRenderJob(ctx context.Context, principal identity.Principal, input videoproject.StartRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error)
	GetProject(principal identity.Principal, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error)
	GetRevision(principal identity.Principal, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error)
	ListProjects(principal identity.Principal, sessionID string, limit int) ([]pebblestore.VideoProjectSnapshot, error)
	ListRevisions(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoProjectRevisionSnapshot, error)
	GetRenderJob(principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
	ListRenderJobs(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
	CreateEditProposal(ctx context.Context, principal identity.Principal, input videoproject.CreateEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error)
	GetEditProposal(principal identity.Principal, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error)
	ListEditProposals(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error)
	SelectAnimationCandidate(ctx context.Context, principal identity.Principal, input videoproject.SelectAnimationCandidateInput) (pebblestore.VideoEditProposalSnapshot, error)
	PromoteAnimationDerivative(ctx context.Context, principal identity.Principal, input videoproject.PromoteAnimationDerivativeInput) (pebblestore.VideoEditProposalSnapshot, error)
	UpdateComposition(ctx context.Context, principal identity.Principal, input videoproject.UpdateCompositionInput) (pebblestore.VideoEditProposalSnapshot, error)
}

type manageVideoRenderService interface {
	InspectFrames(context.Context, identity.Principal, videorender.FrameInspectionRequest) (videorender.FrameInspectionResult, error)
	StartRenderJob(principal identity.Principal, req videorender.RenderJobRequest)
	CancelRenderJob(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, error)
	GetRenderJobStatus(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
}

type manageVideoActionSpec struct {
	Name          string
	SuccessTitle  string
	ActivityLabel string
	StudioAllowed bool
}

var manageVideoActionRegistry = []manageVideoActionSpec{
	{"capabilities", "Video capabilities ready", "Checking video capabilities", true},
	{"inspect_context", "Video context loaded", "Inspecting Video Studio context", true},
	{"inspect_frames", "Video frames ready", "Inspecting exact video frames", true},
	{"list_source_roots", "Media sources ready", "Finding media sources", true},
	{"browse_source", "Media source opened", "Browsing media sources", true},
	{"inspect_attachments", "Video attachments checked", "Checking video attachments", true},
	{"start_transcription", "Transcription started", "Starting media transcription", true},
	{"status", "Transcription status checked", "Checking transcription progress", true},
	{"cancel", "Transcription cancelled", "Cancelling video transcription", true},
	{"read_transcript", "Transcript ready", "Reading media transcript", true},
	{"read_audio_analysis", "Audio analysis ready", "Reading deterministic audio analysis", true},
	{"create_project", "Video project ready", "Setting up video project", true},
	{"read_project", "Video project loaded", "Loading video project", true},
	{"get_project", "Video project loaded", "Loading video project", true},
	{"list_projects", "Video projects loaded", "Loading video projects", true},
	{"inspect_accepted_cut", "Accepted cut loaded", "Inspecting accepted cut", true},
	{"create_edit_proposal", "New change added", "Preparing video working change", true},
	{"propose_plan", "New video change added", "Preparing visual video change", true},
	{"propose_html_iteration", "HTML iteration added", "Preparing live HTML iteration", true},
	{"convert_artifact_v2", "Artifact V2 proposal added", "Converting exact Artifact V2 head", true},
	{"convert_artifact_v3", "Artifact V3 proposal added", "Rendering exact Artifact V3 revision", true},
	{"import_audio_artifact", "Audio artifact imported", "Importing audio artifact into Video Studio", true},
	{"register_audio_artifact", "Audio artifact registered", "Registering audio artifact into Video Studio", true},
	{"select_animation_candidate", "Animation candidate selected", "Selecting exact HTML animation candidate", true},
	{"promote_animation_derivative", "Animation derivative promoted", "Promoting exact MP4 animation derivative", true},
	{"inspect_composition", "Composition loaded", "Inspecting pending spatial composition", true},
	{"update_composition", "Composition updated", "Updating pending spatial composition", true},
	{"proposal_status", "Proposal status updated", "Checking edit proposal", true},
	{"recommend_render_settings", "Render settings recommended", "Reviewing render settings", true},
	{"create_revision", "Video edit saved", "Saving video edit", false},
	{"restore_revision", "Video version restored", "Restoring video version", false},
	{"start_render", "Video render started", "Starting video render", false},
	{"render_status", "Render status updated", "Checking render progress", true},
	{"cancel_render", "Video render cancelled", "Cancelling video render", true},
	{"help", "Video help ready", "Reading video documentation", true},
}

func manageVideoActionNames(studio bool) []string {
	names := make([]string, 0, len(manageVideoActionRegistry))
	for _, spec := range manageVideoActionRegistry {
		if !studio || spec.StudioAllowed {
			names = append(names, spec.Name)
		}
	}
	return names
}

// ManageVideoActionNames returns the canonical action enum for provider schema
// materialization. Video Studio callers receive only actions the runtime allows.
func ManageVideoActionNames(studio bool) []string {
	return manageVideoActionNames(studio)
}

func manageVideoAction(action string) (manageVideoActionSpec, bool) {
	for _, spec := range manageVideoActionRegistry {
		if spec.Name == action {
			return spec, true
		}
	}
	return manageVideoActionSpec{}, false
}

func manageVideoDefinition() Definition {
	return Definition{
		Type: "function", Name: "manage_video",
		Description: "Video Studio multi-clip project management, timeline editing, visual plan proposals, soundtracks, and rendering. Distinct from single-pass AI video generation; creates and edits multi-part video timelines with ready media visuals. Call action='help' for detailed workflows, schemas, and examples.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":                   map[string]any{"type": "string", "enum": manageVideoActionNames(false), "description": "Action name. Call capabilities or action='help' for details."},
				"source_root_ref":          map[string]any{"type": "string", "description": "Source root ref from list_source_roots."},
				"relative_path":            map[string]any{"type": "string", "description": "Path under source_root_ref."},
				"video_refs":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Video refs from browse_source."},
				"audio_refs":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Audio refs from browse_source."},
				"job_refs":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"job_ref":                  map[string]any{"type": "string"},
				"transcript_ref":           map[string]any{"type": "string"},
				"source_fingerprint":       map[string]any{"type": "string", "description": "Source fingerprint."},
				"analysis_ref":             map[string]any{"type": "string", "description": "Audio analysis reference."},
				"waveform_resolution_ms":   map[string]any{"type": "integer"},
				"focus_notes":              map[string]any{"type": "string", "maxLength": videotranscription.MaxFocusNotesBytes, "description": "Optional job-specific instructions from the initiating user or AI for start_transcription only, for example: 'Silent software demo; produce a dense play-by-play of cursor actions, navigation, text changes, and visible results.' Guidance cannot change the multimodal schema, factuality rules, or source authority."},
				"max_bytes":                map[string]any{"type": "integer"},
				"max_segments":             map[string]any{"type": "integer"},
				"start_ms":                 map[string]any{"type": "integer"},
				"end_ms":                   map[string]any{"type": "integer"},
				"timestamps_ms":            map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"ranges":                   map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Ranges for inspect_frames ({start_ms, end_ms, count})."},
				"max_width":                map[string]any{"type": "integer"},
				"include_index":            map[string]any{"type": "boolean"},
				"index_only":               map[string]any{"type": "boolean"},
				"project_id":               map[string]any{"type": "string"},
				"revision_id":              map[string]any{"type": "string"},
				"source_revision_id":       map[string]any{"type": "string"},
				"render_job_id":            map[string]any{"type": "string"},
				"render_fps":               map[string]any{"type": "integer", "enum": []int{30, 60}},
				"queue_grace_ms":           map[string]any{"type": "integer"},
				"title":                    map[string]any{"type": "string"},
				"description":              map[string]any{"type": "string"},
				"output_preset":            map[string]any{"type": "string"},
				"change_summary":           map[string]any{"type": "string"},
				"timeline":                 map[string]any{"type": "object", "description": "Structured timeline. Call action='help' for schema."},
				"initial_timeline":         map[string]any{"type": "object", "description": "Optional initial structured timeline when creating a video project. Omit it for a new visual plan without accepted media. When registered soundtrack audio must share the initial part playhead, include one exact trimmed source_audio clip here; the returned base revision owns that audio and a subsequent propose_plan preserves it. Call action='help' for schema."},
				"metadata":                 map[string]any{"type": "object"},
				"artifact_v3_session_id":   map[string]any{"type": "string"},
				"artifact_v3_artifact_id":  map[string]any{"type": "string"},
				"artifact_v3_revision_ref": map[string]any{"type": "string"},
				"expected_revision_id":     map[string]any{"type": "string"},
				"part_id":                  map[string]any{"type": "string"},
				"selected_candidate_id":    map[string]any{"type": "string"},
				"selected_source":          manageVideoArtifactReferenceSchema(),
				"derivative":               manageVideoArtifactReferenceSchema(),
				"artifact_reference":       manageVideoArtifactReferenceSchema(),
				"media_inspect_reference":  map[string]any{"type": "object", "description": "Media inspect reference containing session_id, collection_id, variant_id, event_seq or artifact_id."},
				"artifact_id":              map[string]any{"type": "string", "description": "Artifact ID or variant ID of audio artifact to import."},
				"directory":                map[string]any{"type": "string", "description": "Optional destination directory for the imported audio source."},
				"destination_dir":          map[string]any{"type": "string", "description": "Alias for directory."},
				"session_id":               map[string]any{"type": "string", "description": "Optional session ID for the audio artifact."},
				"collection_id":            map[string]any{"type": "string", "description": "Optional collection ID for the audio artifact."},
				"variant_id":               map[string]any{"type": "string", "description": "Optional variant ID for the audio artifact."},
				"event_seq":                map[string]any{"type": "integer", "description": "Optional event sequence for the audio artifact."},
				"base_revision_id":         map[string]any{"type": "string"},
				"plan": map[string]any{
					"type":        "object",
					"description": "Visual video-plan proposal with kind ('initial'|'revision'), summary, and parts array. Call action='help' for schema and copyable examples.",
					"properties": map[string]any{
						"kind":    map[string]any{"type": "string", "enum": []string{pebblestore.VideoPlanKindInitial, pebblestore.VideoPlanKindRevision}, "description": "Plan kind: 'initial' for whole initial cut, 'revision' for updating specific parts."},
						"summary": map[string]any{"type": "string", "description": "Concise summary of the plan."},
						"parts": map[string]any{
							"type":        "array",
							"description": "Ordered timeline parts. Each part requires id, title, duration_ms, and visual artifact reference.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id":               map[string]any{"type": "string", "description": "Unique stable part ID."},
									"title":            map[string]any{"type": "string", "description": "Human-readable part title."},
									"duration_ms":      map[string]any{"type": "integer", "minimum": 1, "description": "Part duration in ms."},
									"visual":           manageVideoArtifactReferenceSchema(),
									"source_start_ms":  map[string]any{"type": "integer", "minimum": 0, "description": "Source start in ms for video/mp4 visual."},
									"source_end_ms":    map[string]any{"type": "integer", "minimum": 1, "description": "Source end in ms for video/mp4 visual."},
									"narration":        map[string]any{"type": "string", "description": "Optional spoken narration text."},
									"on_screen_text":   map[string]any{"type": "string", "description": "Optional on-screen text/titles."},
									"visual_direction": map[string]any{"type": "string", "description": "Optional visual direction notes."},
									"transition_in":    map[string]any{"type": "string", "description": "Optional transition."},
								},
								"required": []string{"id", "title"},
							},
						},
					},
					"required": []string{"parts"},
				},
				"operations": map[string]any{
					"type":        "array",
					"description": "Bounded typed add, update, replace, and remove operations. Call action='help' for schema and examples.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          map[string]any{"type": "string"},
							"type":        map[string]any{"type": "string", "enum": []string{"add_clip", "update_clip", "replace_clip", "remove_clip"}},
							"source_kind": map[string]any{"type": "string", "enum": []string{"source_audio"}},
							"audio_source": map[string]any{
								"type":        "object",
								"description": "Complete exact trusted audio reference returned by browse_source.",
								"properties": map[string]any{
									"source_fingerprint":  map[string]any{"type": "string"},
									"fingerprint_version": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
				"affected_ranges": map[string]any{"type": "array", "maxItems": pebblestore.MaxVideoEditProposalOperations, "items": map[string]any{"type": "object", "properties": map[string]any{"start_ms": map[string]any{"type": "integer", "minimum": 0}, "end_ms": map[string]any{"type": "integer", "minimum": 1}}, "required": []string{"start_ms", "end_ms"}, "additionalProperties": false}},
			},
			"required": []string{"action"}, "additionalProperties": true,
		},
	}
}

func manageVideoArtifactReferenceSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "string"},
			"collection_id": map[string]any{"type": "string"},
			"variant_id":    map[string]any{"type": "string"},
			"event_seq":     map[string]any{"type": "integer", "minimum": 1},
			"label":         map[string]any{"type": "string"},
			"description":   map[string]any{"type": "string"},
			"action":        map[string]any{"type": "string"},
			"part_id":       map[string]any{"type": "string"},
		},
		"required":             []string{"session_id", "collection_id", "variant_id", "event_seq"},
		"additionalProperties": false,
	}
}

func (r *Runtime) executeManageVideo(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "help" {
		response := map[string]any{"tool": "manage_video", "action": "help", "status": "ok", "instructions": videoHelpText()}
		raw, _ := json.Marshal(response)
		return string(raw), nil
	}
	if r == nil || r.sessions == nil {
		return "", errors.New("manage_video service is not configured")
	}
	if !scope.Principal.Valid() || strings.TrimSpace(scope.SessionID) == "" {
		return "", errors.New("manage_video requires authenticated session run context")
	}
	action = strings.ToLower(strings.TrimSpace(asString(args["action"])))
	requestedAction := action
	if action == "propose_plan" || action == "propose_html_iteration" {
		// Providers get purpose-specific actions while storage continues to use
		// the revision-gated edit proposal authority.
		action = "create_edit_proposal"
	}
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
	projectSessionID, studio, studioErr := r.manageVideoProjectSession(scope.Principal, session)
	if studioErr != nil {
		return "", studioErr
	}
	allowedActions := manageVideoActionNames(studio)
	spec, validAction := manageVideoAction(requestedAction)
	if !validAction {
		return "", fmt.Errorf("unsupported manage_video action %q; nearest valid actions: %s", requestedAction, strings.Join(nearestManageVideoActionsFrom(requestedAction, allowedActions, 3), ", "))
	}
	if studio && !spec.StudioAllowed {
		return "", fmt.Errorf("Video Studio AI cannot use manage_video action %q; allowed actions: %s", requestedAction, strings.Join(allowedActions, ", "))
	}
	if action == "convert_artifact_v2" {
		if r.artifactV2Video == nil {
			return "", errors.New("artifact v2 video conversion is not configured")
		}
		projectID, baseRevisionID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["base_revision_id"]))
		proposal, convertErr := r.artifactV2Video.ConvertToPendingProposal(ctx, scope.Principal, artifactv2.ConvertToVideoInput{RequestID: firstNonEmptyManagedPartID(strings.TrimSpace(asString(args["proposal_id"])), "artifact-v2-video:"+run.RunID), VideoSessionID: projectSessionID, ProjectID: projectID, BaseRevisionID: baseRevisionID, ArtifactID: strings.TrimSpace(asString(args["artifact_v2_artifact_id"])), PublishedHeadID: strings.TrimSpace(asString(args["artifact_v2_published_head_id"])), Title: strings.TrimSpace(asString(args["title"])), Rationale: strings.TrimSpace(asString(args["rationale"]))})
		if convertErr != nil {
			return "", convertErr
		}
		payload, _ := json.Marshal(map[string]any{"tool": "manage_video", "action": "convert_artifact_v2", "status": "ok", "proposal": proposal, "proposal_status": "pending"})
		return string(payload), nil
	}
	if action == "convert_artifact_v3" {
		if r.artifactV3Video == nil {
			return "", errors.New("artifact v3 video conversion is not configured")
		}
		proposal, convertErr := r.artifactV3Video.ConvertToPendingProposal(ctx, scope.Principal, ArtifactV3VideoConversionInput{
			RequestID:      firstNonEmptyManagedPartID(strings.TrimSpace(asString(args["proposal_id"])), "artifact-v3-video:"+run.RunID),
			VideoSessionID: projectSessionID, ProjectID: strings.TrimSpace(asString(args["project_id"])), BaseRevisionID: strings.TrimSpace(asString(args["base_revision_id"])),
			PartID: strings.TrimSpace(asString(args["part_id"])), CaptureStateID: strings.TrimSpace(asString(args["capture_state_id"])),
			ArtifactSessionID: strings.TrimSpace(asString(args["artifact_v3_session_id"])), ArtifactID: strings.TrimSpace(asString(args["artifact_v3_artifact_id"])), RevisionRef: strings.TrimSpace(asString(args["artifact_v3_revision_ref"])),
			Title: strings.TrimSpace(asString(args["title"])), Rationale: strings.TrimSpace(asString(args["rationale"])),
		})
		if convertErr != nil {
			return "", convertErr
		}
		response := map[string]any{
			"tool": "manage_video", "action": "convert_artifact_v3", "status": "ok",
			"proposal": safeVideoEditProposal(proposal), "proposal_id": proposal.ID, "project_id": proposal.ProjectID,
			"base_revision_id": proposal.BaseRevisionID, "working_revision_id": proposal.WorkingRevisionID,
			"proposal_status": proposal.Status, "requires_user_acceptance": true,
		}
		if proposal.Status != pebblestore.VideoEditProposalStatusPending || proposal.Intent != pebblestore.VideoEditProposalIntentArtifactV3Convert || proposal.AcceptedRevisionID != "" || proposal.ProjectID != strings.TrimSpace(asString(args["project_id"])) || proposal.BaseRevisionID != strings.TrimSpace(asString(args["base_revision_id"])) || proposal.WorkingRevisionID == "" || proposal.Plan == nil || len(proposal.Plan.Parts) < 1 || proposal.Plan.Parts[0].ArtifactV3Still == nil || proposal.Plan.Parts[0].ArtifactV3Visual == nil {
			return "", errors.New("artifact v3 video conversion returned an invalid pending native proposal")
		}
		derivatives := make([]map[string]any, 0, len(proposal.Plan.Parts)*2)
		refs := make([]*pebblestore.ArtifactV3VideoReference, 0, len(proposal.Plan.Parts)*2)
		for _, part := range proposal.Plan.Parts {
			if part.ArtifactV3Still == nil || part.ArtifactV3Visual == nil {
				return "", errors.New("native Artifact V3 section is missing exact media")
			}
			refs = append(refs, part.ArtifactV3Still)
			if *part.ArtifactV3Visual != *part.ArtifactV3Still {
				refs = append(refs, part.ArtifactV3Visual)
			}
		}
		for _, ref := range refs {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			body, readErr := r.artifactV3Video.ReadVideoReference(ctx, scope.Principal.AccountScopeID, scope.Principal.UserID, *ref)
			if readErr != nil {
				return "", fmt.Errorf("read native Artifact V3 conversion derivative: %w", readErr)
			}
			digest := sha256.Sum256(body)
			if hex.EncodeToString(digest[:]) != ref.DigestSHA256 {
				return "", errors.New("native Artifact V3 conversion derivative digest changed before response")
			}
			prefix := body
			if len(prefix) > manageVideoDerivativeEvidencePrefix {
				prefix = prefix[:manageVideoDerivativeEvidencePrefix]
			}
			if (ref.MediaType != "image/png" && ref.MediaType != "video/mp4") || (ref.MediaType == "image/png" && (len(prefix) < 8 || string(prefix[1:4]) != "PNG")) || (ref.MediaType == "video/mp4" && (len(prefix) < 12 || string(prefix[4:8]) != "ftyp")) {
				return "", errors.New("native Artifact V3 conversion derivative container evidence is invalid")
			}
			derivatives = append(derivatives, map[string]any{"derivative_id": ref.DerivativeID, "media_type": ref.MediaType, "digest_sha256": ref.DigestSHA256, "duration_ms": ref.DurationMs, "size_bytes": len(body), "container_prefix_base64": base64.StdEncoding.EncodeToString(prefix)})
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		response["derivatives"] = derivatives
		response["derivative_evidence"] = "digest_verified_container_prefix"
		payload, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return "", fmt.Errorf("encode native Artifact V3 conversion result: %w", marshalErr)
		}
		return string(payload), nil
	}
	response := map[string]any{"tool": "manage_video", "action": action, "status": "ok", "session_id": scope.SessionID, "path_id": toolPathID("manage_video"), "details_truncated": false}
	if run.MessageID != "" {
		response["message_id"] = run.MessageID
	}
	switch action {
	case "capabilities", "help":
		response["studio"] = studio
		response["allowed_actions"] = allowedActions
		response["read_only_actions"] = []string{"capabilities", "help", "inspect_context", "inspect_frames", "list_source_roots", "browse_source", "inspect_attachments", "status", "read_transcript", "read_audio_analysis", "read_project", "get_project", "list_projects", "inspect_accepted_cut", "inspect_composition", "proposal_status", "recommend_render_settings", "render_status"}
		response["instructions"] = videoHelpText()
	case "inspect_context":
		response["studio"] = studio
		response["allowed_actions"] = allowedActions
		contextMetadata := session.Metadata
		if run.MessageID != "" {
			if current, found, readErr := r.sessions.GetV3MessageByID(scope.SessionID, run.MessageID); readErr != nil {
				return "", readErr
			} else if found && current.Role == "user" {
				contextMetadata = mergeManageVideoMetadata(session.Metadata, current.Metadata)
			}
		}
		if projectSessionID != session.ID {
			if parent, found, readErr := r.sessions.GetSession(projectSessionID); readErr != nil {
				return "", readErr
			} else if found {
				contextMetadata = mergeManageVideoMetadata(parent.Metadata, contextMetadata)
			}
		}
		selectedProjectID := strings.TrimSpace(asString(contextMetadata["video_project_id"]))
		selectedRevisionID := strings.TrimSpace(asString(contextMetadata["video_revision_id"]))
		response["selected_project_id"] = selectedProjectID
		response["selected_revision_id"] = selectedRevisionID
		response["selection"] = manageVideoSelectionContext(contextMetadata)
		projectID := selectedProjectID
		if projectID == "" && studio {
			if r.videoProjects == nil {
				return "", errors.New("inspect_context requires configured video project authority")
			}
			projects, listErr := r.videoProjects.ListProjects(scope.Principal, projectSessionID, 2)
			if listErr != nil {
				return "", listErr
			}
			if len(projects) == 1 {
				projectID = projects[0].ID
			}
		}
		if projectID != "" {
			if r.videoProjects == nil {
				return "", errors.New("inspect_context requires configured video project authority")
			}
			project, found, readErr := r.videoProjects.GetProject(scope.Principal, projectSessionID, projectID)
			if readErr != nil {
				return "", readErr
			}
			if !found {
				return "", fmt.Errorf("selected video project %q not found", projectID)
			}
			response["project"] = safeVideoProject(project)
			response["project_id"] = project.ID
			if selectedRevisionID != "" {
				rev, revisionFound, revisionErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, project.ID, selectedRevisionID)
				if revisionErr != nil {
					return "", revisionErr
				}
				if !revisionFound {
					return "", fmt.Errorf("selected video revision %q not found", selectedRevisionID)
				}
				response["selected_revision"] = safeVideoProjectRevision(&rev)
			}
			if project.CurrentRevisionID != "" {
				rev, revisionFound, revisionErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, project.ID, project.CurrentRevisionID)
				if revisionErr != nil {
					return "", revisionErr
				}
				if !revisionFound {
					return "", fmt.Errorf("current video revision %q not found", project.CurrentRevisionID)
				}
				response["current_revision"] = safeVideoProjectRevision(&rev)
			}
			confirmedID := project.ConfirmedRevisionID
			if confirmedID == "" {
				confirmedID = project.CurrentRevisionID
			}
			if confirmedID != "" {
				rev, revisionFound, revisionErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, project.ID, confirmedID)
				if revisionErr != nil {
					return "", revisionErr
				}
				if !revisionFound {
					return "", fmt.Errorf("confirmed video revision %q not found", confirmedID)
				}
				response["confirmed_revision"] = safeVideoProjectRevision(&rev)
			}
			proposals, proposalErr := r.videoProjects.ListEditProposals(scope.Principal, projectSessionID, project.ID, 50)
			if proposalErr != nil {
				return "", proposalErr
			}
			pending := make([]map[string]any, 0)
			for _, proposal := range proposals {
				if proposal.Status == pebblestore.VideoEditProposalStatusPending {
					pending = append(pending, safeVideoEditProposal(proposal))
				}
			}
			response["pending_proposals"] = pending
		}
	case "inspect_frames":
		if r.videoProjects == nil || r.videoRender == nil {
			return "", errors.New("video frame inspection is unavailable: project or render service is not configured")
		}
		projectID, revisionID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["revision_id"]))
		if projectID == "" || revisionID == "" {
			return "", errors.New("inspect_frames requires exact project_id and revision_id")
		}
		timestamps, parseErr := parseFrameTimestamps(args["timestamps_ms"])
		if parseErr != nil {
			return "", parseErr
		}
		ranges, parseErr := parseFrameRanges(args["ranges"])
		if parseErr != nil {
			return "", parseErr
		}
		result, inspectErr := r.videoRender.InspectFrames(ctx, scope.Principal, videorender.FrameInspectionRequest{SessionID: projectSessionID, ArtifactSessionID: scope.SessionID, ProjectID: projectID, RevisionID: revisionID, WorkspacePath: manageVideoWorkspacePath(session), TimestampsMs: timestamps, Ranges: ranges, MaxWidth: asInt(args["max_width"], 0), RequestID: run.RunID})
		if inspectErr != nil {
			return "", inspectErr
		}
		frames := make([]map[string]any, 0, len(result.Frames))
		for _, frame := range result.Frames {
			frames = append(frames, map[string]any{"timestamp_ms": frame.TimestampMs, "artifact": frame.Artifact})
		}
		response["project_id"], response["revision_id"], response["revision_event_seq"] = result.ProjectID, result.RevisionID, result.RevisionEventSeq
		response["duration_ms"], response["width"], response["height"] = result.DurationMs, result.Width, result.Height
		response["frames"], response["count"] = frames, len(frames)

	case "import_audio_artifact", "register_audio_artifact":
		if r.artifactAuthority == nil {
			return "", errors.New("manage_video audio import requires configured artifact authority")
		}
		var ref *pebblestore.SessionArtifactSelectionReference
		var artifactID string
		if rawRef := args["artifact_reference"]; rawRef != nil {
			parsedRef, err := parseManageVideoArtifactReference(rawRef, "artifact_reference")
			if err != nil {
				return "", err
			}
			ref = parsedRef
		} else if rawInspect := args["media_inspect_reference"]; rawInspect != nil {
			if inspectObj, err := parseJSONEncodedObject(rawInspect, "media_inspect_reference"); err == nil {
				if inspectObj["session_id"] != nil && inspectObj["collection_id"] != nil && inspectObj["variant_id"] != nil && inspectObj["event_seq"] != nil {
					parsedRef, err := parseManageVideoArtifactReference(rawInspect, "media_inspect_reference")
					if err != nil {
						return "", err
					}
					ref = parsedRef
				} else if idStr := strings.TrimSpace(asString(inspectObj["artifact_id"])); idStr != "" {
					artifactID = idStr
				}
			}
		}
		if ref == nil && artifactID == "" {
			sessionIDVal := strings.TrimSpace(asString(args["session_id"]))
			collectionIDVal := strings.TrimSpace(asString(args["collection_id"]))
			variantIDVal := strings.TrimSpace(asString(args["variant_id"]))
			eventSeqVal := asUint64(args["event_seq"])
			if sessionIDVal != "" && collectionIDVal != "" && variantIDVal != "" && eventSeqVal != 0 {
				ref = &pebblestore.SessionArtifactSelectionReference{
					SessionID:    sessionIDVal,
					CollectionID: collectionIDVal,
					VariantID:    variantIDVal,
					EventSeq:     eventSeqVal,
				}
			} else if artIDVal := strings.TrimSpace(asString(args["artifact_id"])); artIDVal != "" {
				artifactID = artIDVal
			} else if variantIDVal != "" {
				artifactID = variantIDVal
			}
		}
		if ref == nil && artifactID == "" {
			return "", errors.New("import_audio_artifact requires an exact artifact reference ({session_id, collection_id, variant_id, event_seq}) or artifact_id")
		}

		targetSessionID := scope.SessionID
		if ref != nil && ref.SessionID != "" {
			targetSessionID = ref.SessionID
		}
		artifactPrincipal := artifact.Principal{
			SessionID:      targetSessionID,
			AccountScopeID: scope.Principal.AccountScopeID,
			UserID:         scope.Principal.UserID,
		}

		var body []byte
		var variant pebblestore.SessionArtifactVariant
		if ref != nil {
			b, v, readErr := r.artifactAuthority.ReadReference(ctx, artifactPrincipal, *ref, 512<<20)
			if readErr != nil {
				return "", fmt.Errorf("read audio artifact reference: %w", readErr)
			}
			body = b
			variant = v
		} else {
			v, getErr := r.artifactAuthority.Get(artifactPrincipal, artifactID)
			if getErr != nil {
				return "", fmt.Errorf("resolve audio artifact %q: %w", artifactID, getErr)
			}
			b, v, readErr := r.artifactAuthority.Read(ctx, artifactPrincipal, artifactID, 512<<20)
			if readErr != nil {
				return "", fmt.Errorf("read audio artifact %q: %w", artifactID, readErr)
			}
			body = b
			variant = v
		}

		if variant.Status != pebblestore.SessionArtifactStatusReady {
			return "", fmt.Errorf("audio artifact %q is not ready (status: %s)", variant.ID, variant.Status)
		}
		if len(body) == 0 {
			return "", fmt.Errorf("audio artifact %q contains no data", variant.ID)
		}

		mediaType := canonicalArtifactMediaType(variant.MediaType)
		if !strings.HasPrefix(mediaType, "audio/") && mediaType != "audio/mpeg" && mediaType != "audio/mp3" {
			return "", fmt.Errorf("artifact %q is not an audio artifact (media_type: %s)", variant.ID, variant.MediaType)
		}

		workspacePath := manageVideoWorkspacePath(session)
		if workspacePath == "" {
			workspacePath = scope.PrimaryPath
		}
		preferredDir := strings.TrimSpace(firstNonEmptyString(asString(args["directory"]), asString(args["destination_dir"])))

		displayName := strings.TrimSpace(firstNonEmptyString(asString(args["name"]), variant.Presentation.Label, variant.Filename))
		if displayName == "" {
			displayName = "audio_" + variant.ID
		}

		if r.videoSources == nil {
			return "", errors.New("manage_video source service is not configured")
		}
		workspaceID := ""
		wsIDs := pebblestore.SessionVideoWorkspaceIDs(session)
		if len(wsIDs) > 0 {
			workspaceID = wsIDs[0]
		}
		savedRecord, importErr := r.videoSources.ImportAudio(scope.Principal, workspacePath, workspaceID, displayName, mediaType, body, preferredDir)
		if importErr != nil {
			return "", importErr
		}
		if r.videoSources.Store() != nil {
			for _, secondaryWSID := range wsIDs {
				if secondaryWSID != "" && secondaryWSID != savedRecord.WorkspaceID {
					secRecord := savedRecord
					secRecord.WorkspaceID = secondaryWSID
					_, _ = r.videoSources.Store().PutAudioSourceRecord(secRecord)
				}
			}
		}

		exactRef := pebblestore.AudioSourceReference{
			Ref:                savedRecord.Ref,
			Name:               savedRecord.DisplayName,
			MIMEType:           savedRecord.MIMEType,
			SizeBytes:          savedRecord.SizeBytes,
			SourceFingerprint:  savedRecord.SourceFingerprint,
			FingerprintVersion: savedRecord.FingerprintVersion,
		}
		response["audio_source"] = exactRef
		response["audio_ref"] = savedRecord.Ref
		response["source_root_path"] = savedRecord.RootPath
		response["relative_path"] = savedRecord.RelativePath
		response["workspace_id"] = savedRecord.WorkspaceID
		response["name"] = savedRecord.DisplayName
		response["mime_type"] = savedRecord.MIMEType
		response["size_bytes"] = savedRecord.SizeBytes
		response["source_fingerprint"] = savedRecord.SourceFingerprint
		response["fingerprint_version"] = savedRecord.FingerprintVersion

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
		response["directories"], response["videos"], response["audio"] = result.Directories, result.Clips, result.AudioClips
		response["directory_count"], response["video_count"], response["audio_count"] = len(result.Directories), len(result.Clips), len(result.AudioClips)
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
		if parseErr != nil {
			return "", parseErr
		}
		audioRefs, parseErr := parseExactStringSlice(args["audio_refs"], "audio_refs")
		if parseErr != nil {
			return "", parseErr
		}
		if len(videoRefs) > 0 && len(audioRefs) > 0 {
			return "", errors.New("start_transcription does not allow mixed video_refs and audio_refs")
		}
		var started videotranscription.StartResult
		if len(videoRefs) > 0 {
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
		} else if len(audioRefs) > 0 {
			if r.videoSources == nil {
				return "", errors.New("manage_video source service is not configured")
			}
			_, records, resolveErr := r.videoSources.ResolveAudioClips(scope.Principal, manageVideoWorkspacePath(session), audioRefs)
			if resolveErr != nil {
				return "", resolveErr
			}
			sources := make([]pebblestore.AudioSourceReference, 0, len(records))
			for _, record := range records {
				sources = append(sources, pebblestore.AudioSourceReference{Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion})
			}
			started, err = r.video.StartRegisteredAudioSources(ctx, scope.Principal, scope.SessionID, sources, focusNotes)
		} else {
			started, err = r.video.StartWithFocus(ctx, scope.Principal, scope.SessionID, run.MessageID, focusNotes)
		}
		if err != nil {
			return "", err
		}
		response["jobs"], response["count"] = safeVideoJobs(started.Jobs), len(started.Jobs)
		if names := r.manageVideoSourceNames(scope.Principal, scope.SessionID, started.Jobs); len(names) > 0 {
			response["source_names"] = names
		}
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
		if names := r.manageVideoSourceNames(scope.Principal, scope.SessionID, jobs); len(names) > 0 {
			response["source_names"] = names
		}
	case "cancel":
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
		job, err := r.video.Cancel(scope.Principal, scope.SessionID, strings.TrimSpace(asString(args["job_ref"])))
		if err != nil {
			return "", err
		}
		response["job"] = safeVideoJob(job)
		if names := r.manageVideoSourceNames(scope.Principal, scope.SessionID, []pebblestore.TranscriptionJob{job}); len(names) > 0 {
			response["source_names"] = names
		}
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
		words := transcript.Words
		wordsTruncated := len(words) > manageVideoMaxWords
		if wordsTruncated {
			words = words[:manageVideoMaxWords]
		}
		if indexOnly {
			text, segments, words = "", []pebblestore.NormalizedTranscriptSegment{}, []pebblestore.NormalizedTranscriptWord{}
			textTruncated, segmentsTruncated, wordsTruncated = false, false, false
		}
		if sourceName, sourceErr := r.video.SourceName(scope.Principal, transcript.SessionID, transcript.AttachmentRef); sourceErr == nil && strings.TrimSpace(sourceName) != "" {
			response["source_names"] = []string{strings.TrimSpace(sourceName)}
		}
		response["transcript"] = map[string]any{
			"ref": transcript.Ref, "job_ref": transcript.JobRef, "message_id": transcript.MessageID,
			"attachment_ref": transcript.AttachmentRef, "source_fingerprint": transcript.SourceFingerprint,
			"schema_version": transcript.SchemaVersion, "model_generated": transcript.ModelGenerated, "timing_authority": "model_generated_semantic_timing",
			"text": text, "segments": segments, "words": words, "language": transcript.Metadata.Language, "duration_ms": transcript.Metadata.DurationMs,
			"summary": transcript.Metadata.Summary, "content_empty": transcript.Metadata.ContentEmpty,
			"validation": transcript.Validation.State, "content_digest": transcript.ContentDigest,
			"text_truncated": textTruncated, "segments_truncated": segmentsTruncated, "words_truncated": wordsTruncated,
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
			response["transcript"].(map[string]any)["words_omitted"] = true
		}
		response["details_truncated"] = textTruncated || segmentsTruncated || wordsTruncated

	case "read_audio_analysis":
		if r.video == nil {
			return "", errors.New("manage_video transcription service is not configured")
		}
		analysisRef, sourceFingerprint := strings.TrimSpace(asString(args["analysis_ref"])), strings.TrimSpace(asString(args["source_fingerprint"]))
		if analysisRef == "" && sourceFingerprint == "" {
			return "", errors.New("read_audio_analysis requires analysis_ref or source_fingerprint")
		}
		var analysis pebblestore.AudioAnalysisSnapshot
		var readErr error
		for _, workspaceID := range pebblestore.SessionVideoWorkspaceIDs(session) {
			analysis, readErr = r.video.ReadAudioAnalysisByWorkspace(scope.Principal, workspaceID, analysisRef, sourceFingerprint)
			if readErr == nil {
				break
			}
		}
		if readErr != nil {
			return "", readErr
		}
		startMs, endMs := int64(asInt(args["start_ms"], 0)), int64(asInt(args["end_ms"], 0))
		if endMs == 0 {
			endMs = analysis.DurationMs
		}
		if startMs < 0 || endMs <= startMs || endMs > analysis.DurationMs {
			return "", errors.New("read_audio_analysis requires a valid bounded start_ms/end_ms range")
		}
		resolution := int64(asInt(args["waveform_resolution_ms"], 0))
		if resolution < 0 || resolution > 60_000 {
			return "", errors.New("waveform_resolution_ms must be between 1 and 60000 when supplied")
		}
		levels := boundedAudioLevels(analysis.Levels, startMs, endMs, resolution)
		onsets := boundedAudioOnsets(analysis.Onsets, startMs, endMs)
		beats := boundedAudioBeats(analysis.Beats, startMs, endMs)
		sections := boundedAudioSections(analysis.Sections, startMs, endMs)
		levelsTruncated := len(levels) > manageVideoMaxAnalysisPoints
		onsetsTruncated := len(onsets) > manageVideoMaxAnalysisPoints
		beatsTruncated := len(beats) > manageVideoMaxAnalysisPoints
		sectionsTruncated := len(sections) > manageVideoMaxAnalysisPoints
		if levelsTruncated {
			levels = levels[:manageVideoMaxAnalysisPoints]
		}
		if onsetsTruncated {
			onsets = onsets[:manageVideoMaxAnalysisPoints]
		}
		if beatsTruncated {
			beats = beats[:manageVideoMaxAnalysisPoints]
		}
		if sectionsTruncated {
			sections = sections[:manageVideoMaxAnalysisPoints]
		}
		response["analysis"] = map[string]any{"ref": analysis.Ref, "schema_version": analysis.SchemaVersion, "source_ref": analysis.SourceRef, "source_fingerprint": analysis.SourceFingerprint, "analyzer_version": analysis.AnalyzerVersion, "duration_ms": analysis.DurationMs, "sample_interval_ms": analysis.SampleIntervalMs, "start_ms": startMs, "end_ms": endMs, "levels": levels, "onsets": onsets, "tempo": analysis.Tempo, "beats": beats, "sections": sections, "timing_authority": "deterministic_pcm_dsp", "model_generated": false, "content_digest": analysis.ContentDigest, "levels_truncated": levelsTruncated, "onsets_truncated": onsetsTruncated, "beats_truncated": beatsTruncated, "sections_truncated": sectionsTruncated}
		response["details_truncated"] = levelsTruncated || onsetsTruncated || beatsTruncated || sectionsTruncated

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
		if rawMeta, ok := args["metadata"]; ok && rawMeta != nil {
			meta, err = parseJSONEncodedObject(rawMeta, "metadata")
			if err != nil {
				return "", err
			}
		}
		workspaceID := ""
		for _, key := range []string{"workspace_id", "swarm_v3_source_workspace_id"} {
			if value, ok := session.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				workspaceID = strings.TrimSpace(value)
				break
			}
		}
		projectSessionID, primaryProject, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		createInput := videoproject.CreateProjectInput{
			SessionID:       projectSessionID,
			WorkspaceID:     workspaceID,
			ProjectID:       projectID,
			Title:           title,
			Description:     description,
			OutputPreset:    outputPreset,
			InitialTimeline: initialTimeline,
			Metadata:        meta,
		}
		var project pebblestore.VideoProjectSnapshot
		var revision *pebblestore.VideoProjectRevisionSnapshot
		if primaryProject && projectID == "" && initialTimeline == nil {
			project, revision, err = r.videoProjects.GetOrCreatePrimaryVideoToolProject(ctx, scope.Principal, createInput)
		} else {
			project, revision, err = r.videoProjects.CreateProject(ctx, scope.Principal, createInput)
		}
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
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projects, err := r.videoProjects.ListProjects(scope.Principal, projectSessionID, 50)
			if err != nil {
				return "", err
			}
			response["projects"] = safeVideoProjects(projects)
			response["count"] = len(projects)
		} else {
			project, ok, err := r.videoProjects.GetProject(scope.Principal, projectSessionID, projectID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("video project %q not found", projectID)
				}
				return "", err
			}
			response["project"] = safeVideoProject(project)
			response["project_id"] = project.ID
			if project.CurrentRevisionID != "" {
				if rev, revOK, revErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, projectID, project.CurrentRevisionID); revErr == nil && revOK {
					response["current_revision"] = safeVideoProjectRevision(&rev)
				}
			}
			confirmedRevisionID := project.ConfirmedRevisionID
			if confirmedRevisionID == "" {
				confirmedRevisionID = project.CurrentRevisionID
			}
			if confirmedRevisionID != "" {
				if rev, revOK, revErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, projectID, confirmedRevisionID); revErr == nil && revOK {
					response["confirmed_revision"] = safeVideoProjectRevision(&rev)
				}
			}
			if project.ActiveRenderJobID != "" {
				if job, jobOK, jobErr := r.videoProjects.GetRenderJob(scope.Principal, projectSessionID, project.ActiveRenderJobID); jobErr == nil && jobOK {
					response["active_render_job"] = safeVideoRenderJob(job)
				}
			}
			if revs, revsErr := r.videoProjects.ListRevisions(scope.Principal, projectSessionID, projectID, 10); revsErr == nil {
				response["revisions"] = safeVideoProjectRevisions(revs)
			}
		}

	case "list_projects":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projects, err := r.videoProjects.ListProjects(scope.Principal, projectSessionID, 50)
		if err != nil {
			return "", err
		}
		response["projects"] = safeVideoProjects(projects)
		response["count"] = len(projects)

	case "inspect_accepted_cut":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			return "", errors.New("inspect_accepted_cut requires project_id")
		}
		project, ok, err := r.videoProjects.GetProject(scope.Principal, projectSessionID, projectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", projectID)
			}
			return "", err
		}
		revisionID := strings.TrimSpace(asString(args["revision_id"]))
		if revisionID == "" {
			revisionID = project.CurrentRevisionID
		}
		revision, ok, err := r.videoProjects.GetRevision(scope.Principal, projectSessionID, projectID, revisionID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video revision %q not found", revisionID)
			}
			return "", err
		}
		maxClips := asInt(args["max_clips"], pebblestore.MaxClipsPerTimeline)
		if maxClips <= 0 || maxClips > pebblestore.MaxClipsPerTimeline {
			maxClips = pebblestore.MaxClipsPerTimeline
		}
		timeline := revision.Timeline
		clipsTruncated := len(timeline.Clips) > maxClips
		if clipsTruncated {
			timeline.Clips = timeline.Clips[:maxClips]
		}
		response["project_id"], response["revision_id"], response["revision_number"] = project.ID, revision.ID, revision.RevisionNumber
		response["accepted_cut"] = map[string]any{"timeline": timeline, "clip_count": len(revision.Timeline.Clips), "transition_count": len(revision.Timeline.Transitions), "clips_truncated": clipsTruncated}
		response["details_truncated"] = clipsTruncated

	case "create_edit_proposal":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID, baseRevisionID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["base_revision_id"]))
		projectSessionID, studio, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		if !studio {
			projectSessionID, session, err = r.upgradeManageVideoStudioSession(scope.Principal, session, projectID, baseRevisionID, run)
			if err != nil {
				return "", err
			}
			response["session_upgraded_to_video_studio"] = true
		} else if strings.TrimSpace(asString(session.Metadata["launch_source"])) == "chat_upgrade" {
			response["session_upgraded_to_video_studio"] = true
		}
		if projectID == "" || baseRevisionID == "" {
			return "", errors.New("create_edit_proposal requires project_id and exact base_revision_id")
		}
		plan, err := parseVideoPlanProposal(args["plan"])
		if err != nil {
			return "", err
		}
		if (requestedAction == "propose_plan" || requestedAction == "propose_html_iteration") && plan == nil {
			return "", fmt.Errorf("%s requires one atomic plan", requestedAction)
		}
		var operations []pebblestore.VideoEditOperation
		if plan == nil {
			baseRevision, found, readErr := r.videoProjects.GetRevision(scope.Principal, projectSessionID, projectID, baseRevisionID)
			if readErr != nil {
				return "", readErr
			}
			if !found {
				return "", fmt.Errorf("video revision %q not found", baseRevisionID)
			}
			operations, err = parseVideoEditOperations(args["operations"], baseRevision.Timeline)
			if err != nil {
				return "", err
			}
		} else if args["operations"] != nil {
			return "", errors.New("create_edit_proposal accepts either one atomic plan or timeline operations, not both")
		}
		affectedRanges, err := parseVideoTimelineRanges(args["affected_ranges"])
		if err != nil {
			return "", err
		}
		intent := pebblestore.VideoEditProposalIntentGeneral
		if requestedAction == "propose_html_iteration" {
			intent = pebblestore.VideoEditProposalIntentHTMLIteration
		}
		proposal, err := r.videoProjects.CreateEditProposal(ctx, scope.Principal, videoproject.CreateEditProposalInput{SessionID: projectSessionID, ProjectID: projectID, ProposalID: strings.TrimSpace(asString(args["proposal_id"])), BaseRevisionID: baseRevisionID, Title: strings.TrimSpace(asString(args["title"])), Rationale: strings.TrimSpace(asString(args["rationale"])), Intent: intent, Plan: plan, Operations: operations, AffectedRanges: affectedRanges})
		if err != nil {
			return "", err
		}
		response["proposal"] = safeVideoEditProposal(proposal)
		response["proposal_id"], response["project_id"], response["revision_id"] = proposal.ID, proposal.ProjectID, proposal.BaseRevisionID
		if requestedAction == "propose_plan" || requestedAction == "propose_html_iteration" {
			response["action"] = requestedAction
		}
		response["proposal_status"], response["stale_base"] = proposal.Status, false
		response["requires_user_acceptance"] = true
		response["working_revision_id"], response["working_revision_number"] = proposal.WorkingRevisionID, proposal.WorkingRevisionNumber
		response["change_notice"] = "A new change was added to the working cut. Review it in the player, restore any sections you do not want, then confirm when ready."

	case "inspect_composition":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID, proposalID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["proposal_id"]))
		if projectID == "" || proposalID == "" {
			return "", errors.New("inspect_composition requires project_id and proposal_id")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		proposal, found, err := r.videoProjects.GetEditProposal(scope.Principal, projectSessionID, projectID, proposalID)
		if err != nil || !found || proposal.Plan == nil || proposal.Status != pebblestore.VideoEditProposalStatusPending {
			if err == nil {
				err = errors.New("pending composition proposal not found")
			}
			return "", err
		}
		if expected := strings.TrimSpace(asString(args["expected_revision_id"])); expected != "" && expected != proposal.WorkingRevisionID {
			return "", errors.New("stale composition inspection: expected_revision_id does not match the pending working revision")
		}
		parts := make([]map[string]any, 0, len(proposal.Plan.Parts))
		width, height := 1920, 1080
		if working, found, _ := r.videoProjects.GetRevision(scope.Principal, projectSessionID, projectID, proposal.WorkingRevisionID); found && working.Timeline.Width > 0 && working.Timeline.Height > 0 {
			width, height = working.Timeline.Width, working.Timeline.Height
		}
		for _, part := range proposal.Plan.Parts {
			if partID := strings.TrimSpace(asString(args["part_id"])); partID != "" && partID != part.ID {
				continue
			}
			resolved, resolveErr := videocomposition.Resolve(proposal.Plan.CompositionCatalog, part.Composition, width, height, part.DurationMs)
			if resolveErr != nil {
				return "", resolveErr
			}
			if resolved == nil {
				resolved = []videocomposition.ResolvedSlot{}
			}
			unresolved := make([]map[string]any, 0)
			for _, slot := range resolved {
				if slot.Source == nil {
					unresolved = append(unresolved, map[string]any{"slot_id": slot.ID, "requirement": slot.Requirement})
				}
			}
			parts = append(parts, map[string]any{"part_id": part.ID, "production_state": part.ProductionState, "composition": part.Composition, "resolved_slots": resolved, "unresolved_requirements": unresolved})
		}
		response["project_id"], response["proposal_id"], response["working_revision_id"] = projectID, proposal.ID, proposal.WorkingRevisionID
		if strings.TrimSpace(asString(args["part_id"])) != "" && len(parts) == 0 {
			return "", errors.New("inspect_composition part_id is not present in the pending plan")
		}
		response["composition_catalog"], response["parts"] = proposal.Plan.CompositionCatalog, parts
		response["output_width"], response["output_height"] = width, height
		response["requires_user_acceptance"] = true

	case "update_composition":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID, proposalID, expectedRevisionID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["proposal_id"])), strings.TrimSpace(asString(args["expected_revision_id"]))
		if projectID == "" || proposalID == "" || expectedRevisionID == "" {
			return "", errors.New("update_composition requires project_id, proposal_id, and exact expected_revision_id")
		}
		plan, err := parseVideoPlanProposal(args["plan"])
		if err != nil || plan == nil {
			if err == nil {
				err = errors.New("update_composition requires one bounded atomic stable-part patch")
			}
			return "", err
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		proposal, err := r.videoProjects.UpdateComposition(ctx, scope.Principal, videoproject.UpdateCompositionInput{SessionID: projectSessionID, ProjectID: projectID, ProposalID: proposalID, ExpectedRevisionID: expectedRevisionID, Plan: plan, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return "", err
		}
		response["proposal"] = safeVideoEditProposal(proposal)
		response["project_id"], response["proposal_id"], response["working_revision_id"] = projectID, proposal.ID, proposal.WorkingRevisionID
		response["proposal_status"], response["requires_user_acceptance"] = proposal.Status, true

	case "select_animation_candidate", "promote_animation_derivative":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projectID, proposalID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["proposal_id"]))
		partID, candidateID := strings.TrimSpace(asString(args["part_id"])), strings.TrimSpace(asString(args["selected_candidate_id"]))
		if projectID == "" || proposalID == "" || partID == "" || candidateID == "" {
			return "", fmt.Errorf("%s requires project_id, proposal_id, part_id, and selected_candidate_id", action)
		}
		selectedSource, err := parseManageVideoArtifactReference(args["selected_source"], "selected_source")
		if err != nil {
			return "", err
		}
		var proposal pebblestore.VideoEditProposalSnapshot
		if action == "select_animation_candidate" {
			proposal, err = r.videoProjects.SelectAnimationCandidate(ctx, scope.Principal, videoproject.SelectAnimationCandidateInput{SessionID: projectSessionID, ProjectID: projectID, ProposalID: proposalID, PartID: partID, CandidateID: candidateID, SelectedSource: selectedSource})
		} else {
			derivative, parseErr := parseManageVideoArtifactReference(args["derivative"], "derivative")
			if parseErr != nil {
				return "", parseErr
			}
			proposal, err = r.videoProjects.PromoteAnimationDerivative(ctx, scope.Principal, videoproject.PromoteAnimationDerivativeInput{SessionID: projectSessionID, ProjectID: projectID, ProposalID: proposalID, PartID: partID, CandidateID: candidateID, SelectedSource: selectedSource, Derivative: derivative})
		}
		if err != nil {
			return "", err
		}
		response["proposal"] = safeVideoEditProposal(proposal)
		response["project_id"], response["proposal_id"], response["part_id"] = proposal.ProjectID, proposal.ID, partID
		response["proposal_status"] = proposal.Status
		response["requires_user_acceptance"] = true

	case "proposal_status":
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projectID, proposalID := strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["proposal_id"]))
		if projectID == "" {
			return "", errors.New("proposal_status requires project_id")
		}
		project, ok, err := r.videoProjects.GetProject(scope.Principal, projectSessionID, projectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", projectID)
			}
			return "", err
		}
		if proposalID != "" {
			proposal, ok, err := r.videoProjects.GetEditProposal(scope.Principal, projectSessionID, projectID, proposalID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("video edit proposal %q not found", proposalID)
				}
				return "", err
			}
			response["proposal"] = safeVideoEditProposal(proposal)
			response["proposal_id"], response["proposal_status"] = proposal.ID, proposal.Status
			response["stale_base"] = proposal.Status == pebblestore.VideoEditProposalStatusPending && proposal.WorkingRevisionID != project.CurrentRevisionID
		} else {
			proposals, err := r.videoProjects.ListEditProposals(scope.Principal, projectSessionID, projectID, 50)
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(proposals))
			for _, proposal := range proposals {
				item := safeVideoEditProposal(proposal)
				item["stale_base"] = proposal.Status == pebblestore.VideoEditProposalStatusPending && proposal.WorkingRevisionID != project.CurrentRevisionID
				items = append(items, item)
			}
			response["proposals"], response["count"] = items, len(items)
		}
		response["project_id"], response["revision_id"] = project.ID, project.CurrentRevisionID

	case "recommend_render_settings":
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			return "", errors.New("recommend_render_settings requires project_id")
		}
		project, ok, err := r.videoProjects.GetProject(scope.Principal, projectSessionID, projectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", projectID)
			}
			return "", err
		}
		response["project_id"], response["revision_id"] = project.ID, project.CurrentRevisionID
		response["recommended_settings"] = map[string]any{"output_preset": project.OutputPreset, "revision_id": project.CurrentRevisionID, "requires_explicit_user_start": true}

	case "restore_revision":
		if _, studio, studioErr := r.manageVideoProjectSession(scope.Principal, session); studioErr != nil {
			return "", studioErr
		} else if studio {
			return "", errors.New("Video Studio AI cannot restore revisions; revision changes require explicit user acceptance")
		}
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		sourceRevisionID := strings.TrimSpace(asString(args["source_revision_id"]))
		if projectID == "" || sourceRevisionID == "" {
			return "", errors.New("restore_revision requires project_id and source_revision_id")
		}
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		revision, project, err := r.videoProjects.RestoreRevision(ctx, scope.Principal, videoproject.RestoreRevisionInput{
			SessionID: projectSessionID, ProjectID: projectID, SourceRevisionID: sourceRevisionID,
			RevisionID: strings.TrimSpace(asString(args["revision_id"])), Description: strings.TrimSpace(asString(args["description"])),
			ChangeSummary: strings.TrimSpace(asString(args["change_summary"])), AuthorPrincipal: scope.Principal.UserID,
		})
		if err != nil {
			return "", err
		}
		response["project"] = safeVideoProject(project)
		response["project_id"] = project.ID
		response["revision"] = safeVideoProjectRevision(&revision)
		response["revision_id"] = revision.ID
		response["restored_from_revision_id"] = revision.RestoredFromRevisionID

	case "create_revision":
		if _, studio, studioErr := r.manageVideoProjectSession(scope.Principal, session); studioErr != nil {
			return "", studioErr
		} else if studio {
			return "", errors.New("Video Studio AI cannot create revisions; create an edit proposal for explicit user acceptance")
		}
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
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		revision, project, err := r.videoProjects.CreateRevision(ctx, scope.Principal, videoproject.CreateRevisionInput{
			SessionID:       projectSessionID,
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
		if _, studio, studioErr := r.manageVideoProjectSession(scope.Principal, session); studioErr != nil {
			return "", studioErr
		} else if studio {
			return "", errors.New("Video Studio AI cannot start final render; recommend settings and wait for explicit user start")
		}
		if r.videoProjects == nil {
			return "", errors.New("manage_video project service is not configured")
		}
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			return "", errors.New("start_render requires project_id")
		}
		revisionID := strings.TrimSpace(asString(args["revision_id"]))
		jobID := strings.TrimSpace(firstNonEmptyString(asString(args["render_job_id"]), asString(args["job_id"])))
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		job, err := r.videoProjects.StartRenderJob(ctx, scope.Principal, videoproject.StartRenderJobInput{
			SessionID:     projectSessionID,
			ProjectID:     projectID,
			RevisionID:    revisionID,
			JobID:         jobID,
			RenderQuality: strings.TrimSpace(asString(args["render_quality"])),
			RenderFPS:     asInt(args["render_fps"], 0),
		})
		if err != nil {
			return "", err
		}
		if r.videoRender != nil && job.Status == pebblestore.VideoRenderJobStatusQueued && (jobID == "" || job.ID == jobID) {
			queueGrace := time.Duration(asInt(args["queue_grace_ms"], 0)) * time.Millisecond
			r.videoRender.StartRenderJob(scope.Principal, videorender.RenderJobRequest{
				SessionID:     projectSessionID,
				ProjectID:     projectID,
				RevisionID:    job.RevisionID,
				JobID:         job.ID,
				WorkspacePath: manageVideoWorkspacePath(session),
				QueueGrace:    queueGrace,
			})
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
		projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
		if err != nil {
			return "", err
		}
		if jobID != "" {
			job, ok, err := r.videoProjects.GetRenderJob(scope.Principal, projectSessionID, jobID)
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
			jobs, err := r.videoProjects.ListRenderJobs(scope.Principal, projectSessionID, projectID, 50)
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
			projectSessionID, _, err := r.manageVideoProjectSession(scope.Principal, session)
			if err != nil {
				return "", err
			}
			job, err := r.videoRender.CancelRenderJob(ctx, scope.Principal, projectSessionID, jobID)
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
	presentationAction := action
	if requestedAction == "propose_plan" || requestedAction == "propose_html_iteration" {
		presentationAction = requestedAction
	}
	response["presentation"] = manageVideoPresentation(presentationAction, args, response)
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func manageVideoPresentation(action string, args, response map[string]any) map[string]any {
	copyByAction := map[string][2]string{
		"list_source_roots":         {"Media sources ready", "Finding media sources"},
		"browse_source":             {"Media source opened", "Browsing media sources"},
		"inspect_attachments":       {"Video attachments checked", "Checking video attachments"},
		"start_transcription":       {"Transcription started", "Starting media transcription"},
		"status":                    {"Transcription status checked", "Checking transcription progress"},
		"cancel":                    {"Transcription cancelled", "Cancelling video transcription"},
		"read_transcript":           {"Transcript ready", "Reading media transcript"},
		"read_audio_analysis":       {"Audio analysis ready", "Reading deterministic audio analysis"},
		"create_project":            {"Video project ready", "Setting up video project"},
		"read_project":              {"Video project loaded", "Loading video project"},
		"get_project":               {"Video project loaded", "Loading video project"},
		"list_projects":             {"Video projects loaded", "Loading video projects"},
		"inspect_accepted_cut":      {"Accepted cut loaded", "Inspecting accepted cut"},
		"create_edit_proposal":      {"New change added", "Preparing video working change"},
		"propose_plan":              {"New video change added", "Preparing visual video change"},
		"propose_html_iteration":    {"HTML iteration added", "Preparing live HTML iteration"},
		"inspect_composition":       {"Composition loaded", "Inspecting pending spatial composition"},
		"update_composition":        {"Composition updated", "Updating pending spatial composition"},
		"proposal_status":           {"Proposal status updated", "Checking edit proposal"},
		"recommend_render_settings": {"Render settings recommended", "Reviewing render settings"},
		"create_revision":           {"Video edit saved", "Saving video edit"},
		"restore_revision":          {"Video version restored", "Restoring video version"},
		"start_render":              {"Video render started", "Starting video render"},
		"render_status":             {"Render status updated", "Checking render progress"},
		"cancel_render":             {"Video render cancelled", "Cancelling video render"},
	}
	copy := copyByAction[action]
	if spec, ok := manageVideoAction(action); ok {
		copy = [2]string{spec.SuccessTitle, spec.ActivityLabel}
	}
	if copy[0] == "" {
		copy = [2]string{"Video task complete", "Working on video"}
	}
	presentation := map[string]any{
		"kind":           "video",
		"title":          copy[0],
		"activity_label": copy[1],
		"action":         action,
		"status":         strings.TrimSpace(asString(response["status"])),
	}
	project, _ := response["project"].(map[string]any)
	sourceNames := manageVideoSourceNameSlice(response)
	if action == "browse_source" {
		if audio, ok := response["audio"].([]videosource.AudioClip); ok {
			for _, clip := range audio {
				if name := strings.TrimSpace(clip.Name); name != "" {
					sourceNames = append(sourceNames, name)
				}
			}
		}
	}
	subject := strings.TrimSpace(firstNonEmptyString(asString(project["title"]), asString(args["title"]), singleManageVideoSourceName(sourceNames), asString(response["relative_path"]), asString(args["output_preset"])))
	if subject != "" {
		presentation["subject"] = subject
	}
	if len(sourceNames) > 0 {
		presentation["source_names"] = sourceNames
	}
	for _, key := range []string{"project_id", "revision_id", "proposal_id", "proposal_status", "requires_user_acceptance", "working_revision_id", "working_revision_number", "change_notice", "stale_base", "job_id", "progress", "count"} {
		if value, ok := response[key]; ok {
			presentation[key] = value
		}
	}
	if renderJob, ok := response["render_job"].(map[string]any); ok {
		for _, key := range []string{"output_preset", "output_width", "output_height", "output_duration_ms", "output_size_bytes"} {
			if value, exists := renderJob[key]; exists {
				presentation[key] = value
			}
		}
	}
	return presentation
}

func (r *Runtime) manageVideoSourceNames(principal identity.Principal, sessionID string, jobs []pebblestore.TranscriptionJob) []string {
	if r == nil || r.video == nil {
		return nil
	}
	names := make([]string, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		name, err := r.video.SourceName(principal, sessionID, job.AttachmentRef)
		name = strings.TrimSpace(name)
		if err != nil || name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func manageVideoSourceNameSlice(response map[string]any) []string {
	if raw, ok := response["source_names"].([]string); ok {
		return raw
	}
	var names []string
	for _, key := range []string{"videos", "attachments"} {
		switch items := response[key].(type) {
		case []map[string]any:
			for _, item := range items {
				if name := strings.TrimSpace(asString(item["name"])); name != "" {
					names = append(names, name)
				}
			}
		case []pebblestore.SessionVideoAttachmentReference:
			for _, item := range items {
				if name := strings.TrimSpace(item.Name); name != "" {
					names = append(names, name)
				}
			}
		case []videosource.Clip:
			for _, item := range items {
				if name := strings.TrimSpace(item.Name); name != "" {
					names = append(names, name)
				}
			}
		}
	}
	if len(names) > 8 {
		names = names[:8]
	}
	return names
}

func singleManageVideoSourceName(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

func parseVideoPlanProposal(raw any) (*pebblestore.VideoPlanProposal, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal plan: %w", err)
	}
	if text, ok := raw.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	}
	var plan pebblestore.VideoPlanProposal
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("invalid plan payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid plan payload: trailing data")
	}
	if len(plan.Parts) == 0 || len(plan.Parts) > pebblestore.MaxClipsPerTimeline {
		return nil, errors.New("plan parts must be non-empty and bounded")
	}
	return &plan, nil
}

type manageVideoEditOperationInput struct {
	ID, Type, ClipID, SourceKind, SourceRef, MediaType string
	Clip                                               *pebblestore.VideoTimelineClip
	TransitionID                                       string
	Transition                                         *pebblestore.VideoTimelineTransition
	SourceStartMs, SourceEndMs, TimelineStartMs        *int64
	Volume                                             *float64
	Muted                                              *bool
	Captions                                           *[]pebblestore.VideoTextOverlay
	AudioSource                                        *pebblestore.AudioSourceReference
	ArtifactRef                                        *pebblestore.SessionArtifactSelectionReference
	DesignInput                                        *pebblestore.VideoDesignInputReference
}

func (input *manageVideoEditOperationInput) UnmarshalJSON(data []byte) error {
	type wire struct {
		ID              string                                         `json:"id"`
		Type            string                                         `json:"type"`
		ClipID          string                                         `json:"clip_id"`
		Clip            *pebblestore.VideoTimelineClip                 `json:"clip"`
		TransitionID    string                                         `json:"transition_id"`
		Transition      *pebblestore.VideoTimelineTransition           `json:"transition"`
		SourceStartMs   *int64                                         `json:"source_start_ms"`
		SourceEndMs     *int64                                         `json:"source_end_ms"`
		TimelineStartMs *int64                                         `json:"timeline_start_ms"`
		Volume          *float64                                       `json:"volume"`
		Muted           *bool                                          `json:"muted"`
		Captions        *[]pebblestore.VideoTextOverlay                `json:"captions"`
		SourceKind      string                                         `json:"source_kind"`
		SourceRef       string                                         `json:"source_ref"`
		MediaType       string                                         `json:"media_type"`
		AudioSource     *pebblestore.AudioSourceReference              `json:"audio_source"`
		ArtifactRef     *pebblestore.SessionArtifactSelectionReference `json:"artifact_ref"`
		DesignInput     *pebblestore.VideoDesignInputReference         `json:"design_input"`
	}
	var value wire
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*input = manageVideoEditOperationInput{ID: value.ID, Type: value.Type, ClipID: value.ClipID, Clip: value.Clip, TransitionID: value.TransitionID, Transition: value.Transition, SourceStartMs: value.SourceStartMs, SourceEndMs: value.SourceEndMs, TimelineStartMs: value.TimelineStartMs, Volume: value.Volume, Muted: value.Muted, Captions: value.Captions, SourceKind: value.SourceKind, SourceRef: value.SourceRef, MediaType: value.MediaType, AudioSource: value.AudioSource, ArtifactRef: value.ArtifactRef, DesignInput: value.DesignInput}
	return nil
}

func parseVideoEditOperations(raw any, timeline pebblestore.VideoProjectTimeline) ([]pebblestore.VideoEditOperation, error) {
	if raw == nil {
		return nil, errors.New("create_edit_proposal requires operations")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal operations: %w", err)
	}
	if text, ok := raw.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	}
	var inputs []manageVideoEditOperationInput
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inputs); err != nil {
		return nil, fmt.Errorf("invalid operations payload: %w", err)
	}
	if len(inputs) == 0 || len(inputs) > pebblestore.MaxVideoEditProposalOperations {
		return nil, errors.New("operations must be non-empty and bounded")
	}
	clips := make(map[string]pebblestore.VideoTimelineClip, len(timeline.Clips))
	for _, clip := range timeline.Clips {
		clips[clip.ID] = clip
	}
	operations := make([]pebblestore.VideoEditOperation, 0, len(inputs))
	for _, input := range inputs {
		op := pebblestore.VideoEditOperation{ID: input.ID, Type: input.Type, ClipID: input.ClipID, Clip: input.Clip, TransitionID: input.TransitionID, Transition: input.Transition}
		switch input.Type {
		case "trim_clip", "move_clip", "set_volume", "set_mute", "set_captions", "replace_source":
			clip, ok := clips[input.ClipID]
			if !ok {
				return nil, fmt.Errorf("operation %q references unknown clip %q", input.ID, input.ClipID)
			}
			switch input.Type {
			case "trim_clip":
				if input.SourceStartMs != nil {
					clip.SourceStartMs = *input.SourceStartMs
				}
				if input.SourceEndMs != nil {
					clip.SourceEndMs = *input.SourceEndMs
				}
				if clip.SourceEndMs <= clip.SourceStartMs {
					return nil, fmt.Errorf("trim_clip %q requires source_end_ms after source_start_ms", input.ID)
				}
				clip.DurationMs = clip.SourceEndMs - clip.SourceStartMs
				clip.TimelineEndMs = clip.TimelineStartMs + clip.DurationMs
			case "move_clip":
				if input.TimelineStartMs == nil {
					return nil, fmt.Errorf("move_clip %q requires timeline_start_ms", input.ID)
				}
				clip.TimelineStartMs = *input.TimelineStartMs
				clip.TimelineEndMs = clip.TimelineStartMs + clip.DurationMs
			case "set_volume":
				if input.Volume == nil {
					return nil, fmt.Errorf("set_volume %q requires volume", input.ID)
				}
				clip.Volume = *input.Volume
			case "set_mute":
				if input.Muted == nil {
					return nil, fmt.Errorf("set_mute %q requires muted", input.ID)
				}
				clip.Muted = *input.Muted
			case "set_captions":
				if input.Captions == nil {
					return nil, fmt.Errorf("set_captions %q requires captions", input.ID)
				}
				clip.Captions = *input.Captions
			case "replace_source":
				if strings.TrimSpace(input.SourceKind) == "" {
					return nil, fmt.Errorf("replace_source %q requires source_kind", input.ID)
				}
				clip.SourceKind, clip.SourceRef, clip.MediaType, clip.AudioSource, clip.ArtifactRef, clip.DesignInput = input.SourceKind, input.SourceRef, input.MediaType, input.AudioSource, input.ArtifactRef, input.DesignInput
			}
			op.Type, op.Clip = pebblestore.VideoEditOperationUpdateClip, &clip
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func parseFrameTimestamps(raw any) ([]int64, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if text, ok := raw.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	}
	if err != nil {
		return nil, fmt.Errorf("marshal timestamps_ms: %w", err)
	}
	var values []int64
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, fmt.Errorf("invalid timestamps_ms payload: %w", err)
	}
	if len(values) > videorender.MaxInspectionFrames {
		return nil, errors.New("timestamps_ms exceeds bounded frame count")
	}
	return values, nil
}

func parseFrameRanges(raw any) ([]videorender.FrameInspectionRange, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if text, ok := raw.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	}
	if err != nil {
		return nil, fmt.Errorf("marshal ranges: %w", err)
	}
	var values []struct {
		StartMs int64 `json:"start_ms"`
		EndMs   int64 `json:"end_ms"`
		Count   int   `json:"count"`
	}
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, fmt.Errorf("invalid ranges payload: %w", err)
	}
	if len(values) > videorender.MaxInspectionFrames {
		return nil, errors.New("ranges exceeds bounded frame count")
	}
	result := make([]videorender.FrameInspectionRange, 0, len(values))
	for _, value := range values {
		result = append(result, videorender.FrameInspectionRange{StartMs: value.StartMs, EndMs: value.EndMs, Count: value.Count})
	}
	return result, nil
}

func parseVideoTimelineRanges(raw any) ([]pebblestore.VideoTimelineRange, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal affected_ranges: %w", err)
	}
	var ranges []pebblestore.VideoTimelineRange
	if text, ok := raw.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	}
	if err := json.Unmarshal(encoded, &ranges); err != nil {
		return nil, fmt.Errorf("invalid affected_ranges payload: %w", err)
	}
	if len(ranges) > pebblestore.MaxVideoEditProposalOperations {
		return nil, errors.New("affected_ranges must be bounded")
	}
	return ranges, nil
}

func safeVideoEditProposal(proposal pebblestore.VideoEditProposalSnapshot) map[string]any {
	return map[string]any{"id": proposal.ID, "project_id": proposal.ProjectID, "base_revision_id": proposal.BaseRevisionID, "base_revision_number": proposal.BaseRevisionNumber, "working_revision_id": proposal.WorkingRevisionID, "working_revision_number": proposal.WorkingRevisionNumber, "status": proposal.Status, "title": proposal.Title, "rationale": proposal.Rationale, "intent": proposal.Intent, "plan": proposal.Plan, "operations": proposal.Operations, "affected_ranges": proposal.AffectedRanges, "accepted_operation_ids": proposal.AcceptedOperationIDs, "accepted_revision_id": proposal.AcceptedRevisionID, "rejection_feedback": proposal.RejectionFeedback, "created_at": proposal.CreatedAt, "updated_at": proposal.UpdatedAt}
}

func parseTimeline(raw any) (*pebblestore.VideoProjectTimeline, error) {
	if raw == nil {
		return nil, nil
	}
	object, err := parseJSONEncodedObject(raw, "timeline")
	if err != nil {
		return nil, err
	}
	bytes, err := json.Marshal(object)
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
	if timeline.Clips == nil {
		timeline.Clips = []pebblestore.VideoTimelineClip{}
	}
	if timeline.Transitions == nil {
		timeline.Transitions = []pebblestore.VideoTimelineTransition{}
	}
	return &timeline, nil
}

func parseManageVideoArtifactReference(raw any, field string) (*pebblestore.SessionArtifactSelectionReference, error) {
	object, err := parseJSONEncodedObject(raw, field)
	if err != nil {
		return nil, err
	}
	ref := &pebblestore.SessionArtifactSelectionReference{
		SessionID: strings.TrimSpace(asString(object["session_id"])), CollectionID: strings.TrimSpace(asString(object["collection_id"])),
		VariantID: strings.TrimSpace(asString(object["variant_id"])), EventSeq: asUint64(object["event_seq"]),
	}
	if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		return nil, fmt.Errorf("%s requires exact session_id, collection_id, variant_id, and event_seq", field)
	}
	return ref, nil
}

func parseJSONEncodedObject(raw any, field string) (map[string]any, error) {
	if encoded, ok := raw.(string); ok {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			return nil, fmt.Errorf("%s requires a non-empty JSON object", field)
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(encoded), &object); err != nil {
			return nil, fmt.Errorf("invalid %s payload: %w", field, err)
		}
		if object == nil {
			return nil, fmt.Errorf("invalid %s payload: expected JSON object", field)
		}
		return object, nil
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", field, err)
	}
	var object map[string]any
	if err := json.Unmarshal(bytes, &object); err != nil {
		return nil, fmt.Errorf("invalid %s payload: expected JSON object: %w", field, err)
	}
	if object == nil {
		return nil, fmt.Errorf("invalid %s payload: expected JSON object", field)
	}
	return object, nil
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
		"id":                        project.ID,
		"session_id":                project.SessionID,
		"title":                     project.Title,
		"description":               project.Description,
		"output_preset":             project.OutputPreset,
		"current_revision_id":       project.CurrentRevisionID,
		"current_revision_number":   project.CurrentRevisionNumber,
		"confirmed_revision_id":     project.ConfirmedRevisionID,
		"confirmed_revision_number": project.ConfirmedRevisionNumber,
		"revision_count":            project.RevisionCount,
		"active_render_job_id":      project.ActiveRenderJobID,
		"created_at":                project.CreatedAt,
		"updated_at":                project.UpdatedAt,
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
		"id":                        rev.ID,
		"project_id":                rev.ProjectID,
		"revision_number":           rev.RevisionNumber,
		"session_id":                rev.SessionID,
		"parent_revision_id":        rev.ParentRevisionID,
		"restored_from_revision_id": rev.RestoredFromRevisionID,
		"origin_proposal_id":        rev.OriginProposalID,
		"description":               rev.Description,
		"change_summary":            rev.ChangeSummary,
		"timeline":                  rev.Timeline,
		"created_at":                rev.CreatedAt,
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
		"id":                     job.ID,
		"project_id":             job.ProjectID,
		"revision_id":            job.RevisionID,
		"revision_number":        job.RevisionNumber,
		"session_id":             job.SessionID,
		"status":                 job.Status,
		"progress":               job.Progress,
		"progress_stage":         job.ProgressStage,
		"render_quality":         job.RenderQuality,
		"render_fps":             job.RenderFPS,
		"elapsed_ms":             job.ElapsedMs,
		"estimated_remaining_ms": job.EstimatedRemainingMs,
		"created_at":             job.CreatedAt,
		"updated_at":             job.UpdatedAt,
	}
	if job.ReusedFromJobID != "" {
		res["reused_from_job_id"] = job.ReusedFromJobID
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

func nearestManageVideoActions(requested string, limit int) []string {
	return nearestManageVideoActionsFrom(requested, manageVideoActionNames(false), limit)
}

func nearestManageVideoActionsFrom(requested string, actions []string, limit int) []string {
	type candidate struct {
		name     string
		distance int
	}
	items := make([]candidate, 0, len(actions))
	for _, action := range actions {
		items = append(items, candidate{action, editDistance(requested, action)})
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].distance < items[i].distance || (items[j].distance == items[i].distance && items[j].name < items[i].name) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]string, limit)
	for i := range out {
		out[i] = items[i].name
	}
	return out
}

func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, l := range left {
		current := make([]int, len(right)+1)
		current[0] = i + 1
		j := 0
		for _, r := range right {
			j++
			cost := 0
			if l != r {
				cost = 1
			}
			current[j] = minInt(minInt(current[j-1]+1, previous[j]+1), previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

func mergeManageVideoMetadata(base, override map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func manageVideoSelectionContext(metadata map[string]any) map[string]any {
	selection := map[string]any{}
	for _, pair := range [][2]string{{"anchor_clip_id", "video_anchor_clip_id"}, {"kind", "video_selection_kind"}, {"transition_id", "video_transition_id"}, {"transition_kind", "video_transition_kind"}, {"transition_from_clip_id", "video_transition_from_clip_id"}, {"transition_to_clip_id", "video_transition_to_clip_id"}} {
		if value := strings.TrimSpace(asString(metadata[pair[1]])); value != "" {
			selection[pair[0]] = value
		}
	}
	for _, pair := range [][2]string{{"storyboard_part_id", "video_storyboard_part_id"}, {"storyboard_capture_state_id", "video_storyboard_capture_state_id"}, {"storyboard_production_state", "video_storyboard_production_state"}} {
		if value := strings.TrimSpace(asString(metadata[pair[1]])); value != "" {
			selection[pair[0]] = value
		}
	}
	if values, ok := metadata["video_storyboard_filming_requirements"].([]string); ok {
		selection["storyboard_filming_requirements"] = append([]string(nil), values...)
	} else if raw, ok := metadata["video_storyboard_filming_requirements"].([]any); ok {
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			if value := strings.TrimSpace(asString(item)); value != "" {
				values = append(values, value)
			}
		}
		selection["storyboard_filming_requirements"] = values
	}
	for _, pair := range [][2]string{{"storyboard_source", "video_storyboard_source"}, {"storyboard_still", "video_storyboard_still"}} {
		if ref, err := parseManageVideoArtifactReference(metadata[pair[1]], pair[1]); err == nil {
			selection[pair[0]] = ref
		}
	}
	for _, pair := range [][2]string{{"playhead_ms", "video_playhead_ms"}, {"transition_duration_ms", "video_transition_duration_ms"}} {
		if _, ok := metadata[pair[1]]; ok {
			selection[pair[0]] = asInt(metadata[pair[1]], 0)
		}
	}
	return selection
}

func actionRequiresVideoTriggeringMessage(action string, args map[string]any) bool {
	if action == "inspect_attachments" {
		return true
	}
	if action != "start_transcription" {
		return false
	}
	videoRefs, videoErr := parseExactStringSlice(args["video_refs"], "video_refs")
	audioRefs, audioErr := parseExactStringSlice(args["audio_refs"], "audio_refs")
	return videoErr == nil && audioErr == nil && len(videoRefs) == 0 && len(audioRefs) == 0
}

func (r *Runtime) upgradeManageVideoStudioSession(principal identity.Principal, session pebblestore.SessionSnapshot, projectID, revisionID string, run VideoRunContext) (string, pebblestore.SessionSnapshot, error) {
	projectID, revisionID = strings.TrimSpace(projectID), strings.TrimSpace(revisionID)
	if projectID == "" || revisionID == "" {
		return "", session, errors.New("create_edit_proposal requires project_id and exact base_revision_id")
	}
	if strings.TrimSpace(run.RunID) == "" {
		return "", session, errors.New("manage_video chat-to-Studio upgrade requires trusted active run authority")
	}
	project, ok, err := r.videoProjects.GetProject(principal, session.ID, projectID)
	if err != nil {
		return "", session, err
	}
	if !ok {
		return "", session, fmt.Errorf("video project %q not found", projectID)
	}
	if _, ok, err := r.videoProjects.GetRevision(principal, session.ID, projectID, revisionID); err != nil {
		return "", session, err
	} else if !ok {
		return "", session, fmt.Errorf("video revision %q not found", revisionID)
	}

	metadata := make(map[string]any, len(session.Metadata)+5)
	for key, value := range session.Metadata {
		metadata[key] = value
	}
	metadata["experience"] = "video_studio"
	metadata["launch_source"] = "chat_upgrade"
	metadata["lineage_kind"] = "video_project"
	metadata["creative_mode"] = "video"
	metadata["video_project_id"] = project.ID
	metadata["video_revision_id"] = revisionID
	session.Metadata = metadata

	requestID := "manage-video-upgrade:" + session.ID + ":" + project.ID
	result, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: session.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: requestID, RequestHash: requestID,
		Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &session,
		CausationID: strings.TrimSpace(run.RunID), CorrelationID: strings.TrimSpace(run.RunID),
	})
	if err != nil {
		return "", session, fmt.Errorf("upgrade chat session to Video Studio: %w", err)
	}
	if result.Session != nil {
		session = *result.Session
	}
	if result.RealtimeOutbox != nil && r.publishSessionOutbox != nil {
		if err := r.publishSessionOutbox(*result.RealtimeOutbox); err != nil {
			return "", session, fmt.Errorf("publish Video Studio session upgrade: %w", err)
		}
	}
	return session.ID, session, nil
}

func (r *Runtime) manageVideoProjectSession(principal identity.Principal, session pebblestore.SessionSnapshot) (string, bool, error) {
	if strings.TrimSpace(asString(session.Metadata["lineage_kind"])) == "video_project" {
		return session.ID, true, nil
	}
	parentID := strings.TrimSpace(asString(session.Metadata["parent_session_id"]))
	if parentID == "" {
		return session.ID, false, nil
	}
	lineageKind := strings.TrimSpace(asString(session.Metadata["lineage_kind"]))
	if lineageKind != "system_sidechat" {
		return session.ID, false, nil
	}
	parent, ok, err := r.sessions.GetSession(parentID)
	if err != nil {
		return "", false, err
	}
	if !ok || parent.AccountScopeID != principal.AccountScopeID || (parent.UserID != "" && parent.UserID != principal.UserID) {
		return "", false, errors.New("manage_video parent session ownership is invalid")
	}
	if strings.TrimSpace(asString(parent.Metadata["lineage_kind"])) != "video_project" {
		return session.ID, false, nil
	}
	return parent.ID, true, nil
}

func manageVideoWorkspacePath(session pebblestore.SessionSnapshot) string {
	if value, ok := session.Metadata["swarm_v3_source_workspace_path"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(session.WorkspacePath)
}

func boundedAudioLevels(levels []pebblestore.AudioAnalysisLevel, startMs, endMs, resolutionMs int64) []pebblestore.AudioAnalysisLevel {
	filtered := make([]pebblestore.AudioAnalysisLevel, 0)
	for _, level := range levels {
		if level.EndMs > startMs && level.StartMs < endMs {
			filtered = append(filtered, level)
		}
	}
	if resolutionMs <= 0 || len(filtered) < 2 {
		return filtered
	}
	out := make([]pebblestore.AudioAnalysisLevel, 0, len(filtered))
	for i := 0; i < len(filtered); {
		bucket := filtered[i]
		weightedRMS, duration := bucket.RMS*float64(bucket.EndMs-bucket.StartMs), bucket.EndMs-bucket.StartMs
		j := i + 1
		for j < len(filtered) && bucket.EndMs-bucket.StartMs < resolutionMs {
			next := filtered[j]
			span := next.EndMs - next.StartMs
			weightedRMS += next.RMS * float64(span)
			duration += span
			bucket.EndMs = next.EndMs
			if next.Peak > bucket.Peak {
				bucket.Peak = next.Peak
			}
			j++
		}
		if duration > 0 {
			bucket.RMS = weightedRMS / float64(duration)
		}
		out = append(out, bucket)
		i = j
	}
	return out
}

func boundedAudioOnsets(values []pebblestore.AudioAnalysisOnset, startMs, endMs int64) []pebblestore.AudioAnalysisOnset {
	out := make([]pebblestore.AudioAnalysisOnset, 0)
	for _, value := range values {
		if value.TimeMs >= startMs && value.TimeMs < endMs {
			out = append(out, value)
		}
	}
	return out
}
func boundedAudioBeats(values []pebblestore.AudioAnalysisBeat, startMs, endMs int64) []pebblestore.AudioAnalysisBeat {
	out := make([]pebblestore.AudioAnalysisBeat, 0)
	for _, value := range values {
		if value.TimeMs >= startMs && value.TimeMs < endMs {
			out = append(out, value)
		}
	}
	return out
}
func boundedAudioSections(values []pebblestore.AudioAnalysisSection, startMs, endMs int64) []pebblestore.AudioAnalysisSection {
	out := make([]pebblestore.AudioAnalysisSection, 0)
	for _, value := range values {
		if value.EndMs > startMs && value.StartMs < endMs {
			out = append(out, value)
		}
	}
	return out
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

func VideoHelpText() string {
	return videoHelpText()
}

func videoHelpText() string {
	return `Video Studio & Media Management Contracts:

Overview & Distinction:
- Video Studio (manage_video) manages multi-part video timeline projects, audio tracks, transitions, and rendering.
- Making a video in Video Studio is completely different from generating a single AI video via manage_artifact action="generate_video" or "generate_video_story".
- In Video Studio, videos are composed of ordered timeline parts (clips), each referencing an actual media artifact (still image or MP4 video clip), combined with background soundtrack audio clips, captions, and transitions within a persistent project.
- The AI does not generate raw video pixels in manage_video; instead, you generate or select real media assets (images or MP4 clips) using manage_artifact, then assemble them into the studio timeline via manage_video action="propose_plan" or action="create_edit_proposal".

1. Inspection & Discovery:
   - action="capabilities" or "help": list allowed actions and workflows.
   - Inspect trusted video and audio sources, browse registered source-media folders, and inspect selected opaque video or audio references or triggering-message video attachments.
   - action="inspect_context": inspects project, revision, and selection state.
   - action="inspect_frames": sample exact PNG frames (pass timestamps_ms or ranges).
   - list_source_roots and browse_source to discover registered audio and video sources.
   - action="import_audio_artifact" (or "register_audio_artifact"): import an AI-generated audio artifact (from manage_artifact generate_audio) into Video Studio as an authenticated audio source, returning an exact audio_source object with ref, name, mime_type, size_bytes, source_fingerprint, and fingerprint_version.
   - action="start_transcription", "read_transcript", "read_audio_analysis": word-timed speech transcripts and audio waveforms.

2. Projects & Timelines:
   - One-shot initial-plan workflow: call create_project without initial_timeline when no accepted media must precede the visual plan; use returned project_id and revision_id, then call propose_plan with base_revision_id and plan.kind="initial". propose_plan creates only a pending whole-plan review object.
   - action="create_project": create a project (pass title, optional initial_timeline with clips).
     When registered soundtrack audio must share the initial part playhead, browse it first, copy the complete exact audio object, and pass its complete exact trimmed source_audio clip in create_project initial_timeline; the returned base revision then owns that audio and a subsequent propose_plan preserves it.
   - action="read_project", "get_project", "list_projects": retrieve video project state.
   - action="restore_revision", "create_revision": manage timeline revisions.

3. Edit Proposals & Operations:
   - action="create_edit_proposal": submit typed add_clip, update_clip, replace_clip, or remove_clip operations with affected_ranges against the exact base revision.
     Operations: add_clip, update_clip, replace_clip, remove_clip, trim_clip, move_clip, set_volume, set_mute, set_captions, replace_source.
     Soundtrack clips: use source_kind="source_audio" with audio_source carrying source_fingerprint and fingerprint_version. Copy the complete exact audio object into a source_audio clip; never pass a host path. Registered soundtrack audio must share the initial part playhead in create_project initial_timeline. Soundtrack proposals remain pending for explicit user acceptance; AI must never accept them or start final rendering.
     Soundtrack workflow from generated audio:
       (1) Generate music or sound effects with manage_artifact action="generate_audio" prompt="..." duration_seconds=N. This creates a ready audio artifact with {session_id, collection_id, variant_id, event_seq}.
       (2) Ingest it into Video Studio using manage_video action="import_audio_artifact", passing the exact artifact reference. This persists an authenticated AudioSourceRecord and returns the exact audio_source object.
       (3) Add the soundtrack to the timeline with manage_video action="create_edit_proposal" operations: [{type: "add_clip", clip: {source_kind: "source_audio", audio_source: ...}}] or in create_project initial_timeline.
   - action="propose_plan": initial visual plan proposal (base_revision_id, plan.kind="initial", parts array with visuals and captions).
     Every newly proposed part must include an exact ready image/* or video/mp4 render-ready fallback. MP4 fallback parts require an explicit source range (source_start_ms, source_end_ms) where source_end_ms - source_start_ms == duration_ms.
     For image/* visual fallbacks, source_start_ms and source_end_ms must be 0 or omitted.
     Every part requires: id (unique stable string), title (human-readable string), duration_ms (positive integer ms), and visual (complete artifact reference: session_id, collection_id, variant_id, event_seq).
     Descriptive on_screen_text and transition_in never create timeline presentation; use typed caption and transition objects when presentation is intended.
     Proposing HTML iterations accepts one or more stable parts in one atomic proposal; every part requires 2 to 16 compatible ready text/html candidates with per-part image-only downgrade.
     composition_catalog defines reusable layout geometry. Parts support detached_slots, clear_source, and audio_policy.
   - Storyboard Pre-Production: for pre-production requests, prefer a self-contained HTML swarm.storyboard/v1 source; use export_html_stills, then import_storyboard with storyboard_source and exports so Video Studio receives filming requirements and production state. Use propose_html_iteration for live animation iteration proposals. Each imported still remains the visible placeholder until a later plan.kind=revision replaces that same part ID with finished media. Do not stop after HTML authoring or still export while storyboard parts remain pending.
   - Convert a compatible exact Artifact V2 storyboard or motion Published Head to Video Studio only with manage_video convert_artifact_v2. The server validates exact V2 composition/build/validation evidence and constructs storyboard stills or animation candidates and fallback media; callers must not export V1 HTML, reconstruct arrays, or mix collection/variant references into the V2 path. The resulting proposal remains pending for user review and cannot accept itself or start final rendering. Server owns the fallback and pending candidate set; do not derive V1 HTML, allocate replacement variants, or export MP4 merely for live preview.
   - Convert an exact selected native Artifact V3 HTML revision to Video Studio only with manage_video convert_artifact_v3. Supply its exact artifact_v3_session_id, artifact_v3_artifact_id, artifact_v3_revision_ref, project_id, and base_revision_id. The server authenticates the selected Git head plus build/validation evidence, injects deterministic animation timing only into ephemeral render bytes, creates the fallback and silent MP4, and submits exactly one pending artifact_v3_conversion proposal; callers must not author plan arrays or translate through V1/V2 identity.
     Native Artifact V3 HTML Animation Requirements:
       - Manifest schema: declare exactly one <script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":...,"fps":...}</script>. Unknown fields are strictly disallowed.
       - Semantic regions: requires at least one semantic region with an id attribute on <main id="...">.
       - Animation API: expose globalThis.__SWARM_ANIMATION_V1__ with version "swarm.animation/v1", ready() returning { duration_ms, fps } matching the manifest, and seek(ms) returning { time_ms: ms }. seek(ms) must pause all animations/timers and deterministically render the requested playhead timestamp.
       - Use manage_video action="convert_artifact_v3" to convert Artifact V3 HTML motion into Video Studio proposals with MP4 fallbacks.
   - For managed pre-production storyboards, use Artifact V2 storyboard section and catalog parts through managed Designer authoring. Stable ordered parts carry filming requirements, production state, capture-state identity, and optional spatial composition; the server owns capture HTML, state runtime, trusted rendering, exact still lineage, and the pending Video Studio adapter.
   - action="inspect_composition", "update_composition": inspect resolved slots and update spatial compositions.
   - action="select_animation_candidate", "promote_animation_derivative": manage HTML animation alternatives.
     immediate live Video Studio preview: selected HTML plays in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead; no HTML-to-MP4 export is needed for preview. Export only when durable acceptance/promotion or final rendering requires an MP4 derivative; never replace a durable timeline artifact_ref with text/html.

4. Renders:
   - action="recommend_render_settings": review server-allowlisted render qualities (preview, standard, high, master) and fps (30, 60).
   - action="start_render", "render_status", "cancel_render": server-owned final MP4 render execution. AI cannot accept a proposal or start a final render.

5. Detailed Call Schemas & Examples:

   a) Propose Initial Plan with Image and MP4 Parts:
      manage_video {
        "action": "propose_plan",
        "project_id": "vproj_12345678",
        "base_revision_id": "vrev_abcdef01",
        "plan": {
          "kind": "initial",
          "summary": "2-scene cut with opening title and video body",
          "parts": [
            {
              "id": "part-1",
              "title": "Scene 1 - Title Card",
              "duration_ms": 3000,
              "visual": {
                "session_id": "sess_abc",
                "collection_id": "collection-1",
                "variant_id": "variant-img1",
                "event_seq": 10
              },
              "narration": "Welcome to our presentation."
            },
            {
              "id": "part-2",
              "title": "Scene 2 - Main Action",
              "duration_ms": 5000,
              "visual": {
                "session_id": "sess_abc",
                "collection_id": "collection-2",
                "variant_id": "variant-vid1",
                "event_seq": 15
              },
              "source_start_ms": 0,
              "source_end_ms": 5000,
              "on_screen_text": "System Online"
            }
          ]
        }
      }

   b) Create Project with Initial Soundtrack Audio:
      manage_video {
        "action": "create_project",
        "title": "Campaign Video",
        "initial_timeline": {
          "output_preset": "landscape_1080p",
          "total_duration_ms": 8000,
          "clips": [
            {
              "id": "soundtrack-1",
              "track": 1,
              "sequence": 0,
              "source_kind": "source_audio",
              "audio_source": {
                "ref": "audiosrc_music_track",
                "name": "theme.mp3",
                "mime_type": "audio/mpeg",
                "size_bytes": 256000,
                "source_fingerprint": "fp_123456",
                "fingerprint_version": "v1"
              },
              "duration_ms": 8000,
              "timeline_start_ms": 0,
              "timeline_end_ms": 8000,
              "visible": false,
              "volume": 1.0,
              "muted": false
            }
          ]
        }
      }

   c) Add Soundtrack via Edit Proposal:
      manage_video {
        "action": "create_edit_proposal",
        "project_id": "vproj_12345678",
        "base_revision_id": "vrev_abcdef01",
        "rationale": "Layer continuous background soundtrack",
        "operations": [
          {
            "id": "op-soundtrack",
            "type": "add_clip",
            "clip": {
              "id": "bg-music",
              "track": 1,
              "sequence": 0,
              "source_kind": "source_audio",
              "audio_source": {
                "ref": "audiosrc_bg_music",
                "name": "ambient.mp3",
                "mime_type": "audio/mpeg",
                "size_bytes": 128000,
                "source_fingerprint": "fp_654321",
                "fingerprint_version": "v1"
              },
              "duration_ms": 8000,
              "timeline_start_ms": 0,
              "timeline_end_ms": 8000,
              "visible": false,
              "volume": 0.8
            }
          }
        ],
        "affected_ranges": [
          {
            "start_ms": 0,
            "end_ms": 8000
          }
        ]
      }

   d) Convert Native Artifact V3 HTML Motion to Studio:
      manage_video {
        "action": "convert_artifact_v3",
        "project_id": "vproj_12345678",
        "base_revision_id": "vrev_abcdef01",
        "artifact_v3_session_id": "sess_abc",
        "artifact_v3_artifact_id": "art_123",
        "artifact_v3_revision_ref": "rev-1"
      }

   e) Import Audio Artifact into Video Studio:
      manage_video {
        "action": "import_audio_artifact",
        "artifact_reference": {
          "session_id": "sess_abc",
          "collection_id": "col_123",
          "variant_id": "var_456",
          "event_seq": 1
        }
      }`
}
