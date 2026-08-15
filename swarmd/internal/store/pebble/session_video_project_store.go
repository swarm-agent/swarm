package pebblestore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	VideoProjectSchemaVersion         = 1
	VideoProjectRevisionSchemaVersion = 1
	VideoTimelineSchemaVersion        = 1
	VideoRenderJobSchemaVersion       = 1

	VideoPresetLandscape1080p = "landscape_1080p"
	VideoPresetLandscape720p  = "landscape_720p"
	VideoPresetPortrait1080p  = "portrait_1080p"
	VideoPresetPortrait720p   = "portrait_720p"
	VideoPresetSquare1080p    = "square_1080p"
	VideoPresetSquare720p     = "square_720p"
	VideoPresetLandscapeVideo = "landscape_video"
	VideoPresetPortraitVideo  = "portrait_video"
	VideoPresetXHeader        = "x_header"

	VideoRenderJobStatusQueued    = "queued"
	VideoRenderJobStatusRendering = "rendering"
	VideoRenderJobStatusReady     = "ready"
	VideoRenderJobStatusFailed    = "failed"
	VideoRenderJobStatusCancelled = "cancelled"
	VideoRenderJobStatusStale     = "stale"

	VideoClipSourceKindSourceVideo     = "source_video"
	VideoClipSourceKindManagedArtifact = "managed_artifact"
	VideoClipSourceKindColor           = "color"
	VideoClipSourceKindText            = "text"

	V3SessionMutationCreateVideoProject         = "video.project.create"
	V3SessionMutationUpdateVideoProject         = "video.project.update"
	V3SessionMutationCreateVideoProjectRevision = "video.project.revision.create"
	V3SessionMutationCreateVideoRenderJob       = "video.render_job.create"
	V3SessionMutationUpdateVideoRenderJob       = "video.render_job.update"

	MaxVideoProjectsPerSession   = 32
	MaxVideoProjectRevisions     = 100
	MaxVideoRenderJobsPerProject = 50
	MaxClipsPerTimeline          = 100
	MaxCaptionsPerClip           = 50
	MaxVideoTimelineDurationMs   = 3600000 // 1 hour
	MaxTextOverlayLength         = 500
)

// VideoTextOverlay models safe, structured caption/title/subtitle overlays.
type VideoTextOverlay struct {
	ID        string `json:"id,omitempty"`
	Text      string `json:"text"`
	Position  string `json:"position,omitempty"` // "bottom", "top", "center"
	FontSize  int    `json:"font_size,omitempty"`
	FontColor string `json:"font_color,omitempty"`
	StartMs   int64  `json:"start_ms"`
	EndMs     int64  `json:"end_ms"`
	Style     string `json:"style,omitempty"`
}

// VideoDesignInputReference references a selected managed artifact variant (e.g. intro/graphic/watermark).
type VideoDesignInputReference struct {
	SessionID    string `json:"session_id,omitempty"`
	CollectionID string `json:"collection_id"`
	VariantID    string `json:"variant_id"`
	EventSeq     uint64 `json:"event_seq,omitempty"`
	Action       string `json:"action,omitempty"`
	OverlayMode  string `json:"overlay_mode,omitempty"` // "pip", "full", "intro", "outro", "watermark"
}

// VideoAudioPolicy contains structured volume, muting, and ducking rules.
type VideoAudioPolicy struct {
	MasterVolume    float64 `json:"master_volume,omitempty"` // 0.0 - 2.0 (default 1.0)
	Muted           bool    `json:"muted,omitempty"`
	DuckOtherTracks bool    `json:"duck_other_tracks,omitempty"`
	DuckingLevel    float64 `json:"ducking_level,omitempty"` // 0.0 - 1.0
}

// VideoTimelineClip represents one ordered clip in the video timeline.
type VideoTimelineClip struct {
	ID              string                             `json:"id"`
	Name            string                             `json:"name,omitempty"`
	Track           int                                `json:"track"`
	Sequence        int                                `json:"sequence"`
	SourceKind      string                             `json:"source_kind"` // "source_video", "managed_artifact", "color", "text"
	SourceRef       string                             `json:"source_ref,omitempty"`
	ArtifactRef     *SessionArtifactSelectionReference `json:"artifact_ref,omitempty"`
	SourceStartMs   int64                              `json:"source_start_ms"`
	SourceEndMs     int64                              `json:"source_end_ms"`
	TimelineStartMs int64                              `json:"timeline_start_ms"`
	TimelineEndMs   int64                              `json:"timeline_end_ms"`
	DurationMs      int64                              `json:"duration_ms"`
	Visible         bool                               `json:"visible"`
	Layer           int                                `json:"layer,omitempty"`
	Volume          float64                            `json:"volume,omitempty"`
	Muted           bool                               `json:"muted,omitempty"`
	AudioPolicy     *VideoAudioPolicy                  `json:"audio_policy,omitempty"`
	Captions        []VideoTextOverlay                 `json:"captions,omitempty"`
	DesignInput     *VideoDesignInputReference         `json:"design_input,omitempty"`
}

// VideoProjectTimeline defines the structured, versioned timeline contract.
type VideoProjectTimeline struct {
	SchemaVersion   int                 `json:"schema_version"`
	OutputPreset    string              `json:"output_preset"`
	Width           int                 `json:"width"`
	Height          int                 `json:"height"`
	FPS             float64             `json:"fps"`
	TotalDurationMs int64               `json:"total_duration_ms"`
	Clips           []VideoTimelineClip `json:"clips"`
	AudioPolicy     *VideoAudioPolicy   `json:"audio_policy,omitempty"`
	Metadata        map[string]any      `json:"metadata,omitempty"`
}

// VideoProjectSnapshot represents a durable session-owned video project.
type VideoProjectSnapshot struct {
	SchemaVersion         int            `json:"schema_version"`
	ID                    string         `json:"id"`
	AccountScopeID        string         `json:"account_scope_id"`
	UserID                string         `json:"user_id,omitempty"`
	WorkspaceID           string         `json:"workspace_id,omitempty"`
	SessionID             string         `json:"session_id"`
	Title                 string         `json:"title"`
	Description           string         `json:"description,omitempty"`
	OutputPreset          string         `json:"output_preset"`
	CurrentRevisionID     string         `json:"current_revision_id,omitempty"`
	CurrentRevisionNumber int            `json:"current_revision_number,omitempty"`
	RevisionCount         int            `json:"revision_count"`
	ActiveRenderJobID     string         `json:"active_render_job_id,omitempty"`
	Metadata              map[string]any `json:"metadata,omitempty"`
	CreatedAt             int64          `json:"created_at"`
	UpdatedAt             int64          `json:"updated_at"`
	EventSeq              uint64         `json:"event_seq,omitempty"`
}

// VideoProjectRevisionSnapshot is an immutable revision of a video project timeline.
type VideoProjectRevisionSnapshot struct {
	SchemaVersion    int                  `json:"schema_version"`
	ID               string               `json:"id"`
	ProjectID        string               `json:"project_id"`
	RevisionNumber   int                  `json:"revision_number"`
	AccountScopeID   string               `json:"account_scope_id"`
	UserID           string               `json:"user_id,omitempty"`
	WorkspaceID      string               `json:"workspace_id,omitempty"`
	SessionID        string               `json:"session_id"`
	ParentRevisionID string               `json:"parent_revision_id,omitempty"`
	Description      string               `json:"description,omitempty"`
	ChangeSummary    string               `json:"change_summary,omitempty"`
	Timeline         VideoProjectTimeline `json:"timeline"`
	AuthorPrincipal  string               `json:"author_principal,omitempty"`
	CreatedAt        int64                `json:"created_at"`
	EventSeq         uint64               `json:"event_seq,omitempty"`
}

// VideoRenderJobSnapshot tracks the lifecycle and output of a render operation.
type VideoRenderJobSnapshot struct {
	SchemaVersion      int                                `json:"schema_version"`
	ID                 string                             `json:"id"`
	ProjectID          string                             `json:"project_id"`
	RevisionID         string                             `json:"revision_id"`
	RevisionNumber     int                                `json:"revision_number"`
	AccountScopeID     string                             `json:"account_scope_id"`
	UserID             string                             `json:"user_id,omitempty"`
	WorkspaceID        string                             `json:"workspace_id,omitempty"`
	SessionID          string                             `json:"session_id"`
	Status             string                             `json:"status"`
	Progress           float64                            `json:"progress"`
	FailureCode        string                             `json:"failure_code,omitempty"`
	FailureReason      string                             `json:"failure_reason,omitempty"`
	OutputPreset       string                             `json:"output_preset,omitempty"`
	OutputWidth        int                                `json:"output_width,omitempty"`
	OutputHeight       int                                `json:"output_height,omitempty"`
	OutputFPS          float64                            `json:"output_fps,omitempty"`
	OutputDurationMs   int64                              `json:"output_duration_ms,omitempty"`
	OutputSizeBytes    int64                              `json:"output_size_bytes,omitempty"`
	OutputDigestSHA256 string                             `json:"output_digest_sha256,omitempty"`
	OutputArtifact     *SessionArtifactSelectionReference `json:"output_artifact,omitempty"`
	CreatedAt          int64                              `json:"created_at"`
	UpdatedAt          int64                              `json:"updated_at"`
	StartedAt          int64                              `json:"started_at,omitempty"`
	CompletedAt        int64                              `json:"completed_at,omitempty"`
	EventSeq           uint64                             `json:"event_seq,omitempty"`
}

// V3VideoProjectMutation wraps payloads for video project mutations.
type V3VideoProjectMutation struct {
	Project        *VideoProjectSnapshot         `json:"project,omitempty"`
	Revision       *VideoProjectRevisionSnapshot `json:"revision,omitempty"`
	RenderJob      *VideoRenderJobSnapshot       `json:"render_job,omitempty"`
	ExpectedStatus string                        `json:"expected_status,omitempty"`
}

// V3VideoProjectProjection summarizes the projection update resulting from a mutation.
type V3VideoProjectProjection struct {
	ProjectID         string `json:"project_id,omitempty"`
	RevisionID        string `json:"revision_id,omitempty"`
	RevisionNumber    int    `json:"revision_number,omitempty"`
	RenderJobID       string `json:"render_job_id,omitempty"`
	Status            string `json:"status,omitempty"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
}

type preparedV3VideoProjectMutation struct {
	Project    *VideoProjectSnapshot
	Revision   *VideoProjectRevisionSnapshot
	RenderJob  *VideoRenderJobSnapshot
	Projection V3VideoProjectProjection
}

// Key functions for pebble storage
func KeyVideoProject(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/project/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
}

func VideoProjectPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/video_project/project/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeyVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) string {
	return fmt.Sprintf("v3/video_project/revision/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID), keyPart(revisionID))
}

func KeyVideoProjectRevisionByNumber(accountScopeID, sessionID, projectID string, revisionNumber int) string {
	return fmt.Sprintf("v3/video_project/revision_by_num/%s/%s/%s/%06d", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID), revisionNumber)
}

func VideoProjectRevisionPrefix(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/revision/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
}

func KeyVideoRenderJob(accountScopeID, sessionID, jobID string) string {
	return fmt.Sprintf("v3/video_project/render_job/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(jobID))
}

func VideoRenderJobPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/video_project/render_job/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeyVideoRenderJobByProject(accountScopeID, sessionID, projectID, jobID string) string {
	return fmt.Sprintf("v3/video_project/render_job_by_project/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID), keyPart(jobID))
}

func VideoRenderJobByProjectPrefix(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/render_job_by_project/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
}

func isV3VideoProjectMutationKind(kind string) bool {
	switch kind {
	case V3SessionMutationCreateVideoProject,
		V3SessionMutationUpdateVideoProject,
		V3SessionMutationCreateVideoProjectRevision,
		V3SessionMutationCreateVideoRenderJob,
		V3SessionMutationUpdateVideoRenderJob:
		return true
	default:
		return false
	}
}

func normalizeV3VideoProjectMutation(input *V3SessionMutationInput) {
	if input == nil || input.VideoProject == nil {
		return
	}
	if input.VideoProject.Project != nil {
		p := input.VideoProject.Project
		p.ID = strings.TrimSpace(p.ID)
		p.AccountScopeID = strings.TrimSpace(p.AccountScopeID)
		p.UserID = strings.TrimSpace(p.UserID)
		p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
		p.SessionID = strings.TrimSpace(p.SessionID)
		p.Title = strings.TrimSpace(p.Title)
		p.Description = strings.TrimSpace(p.Description)
		p.OutputPreset = normalizeVideoPreset(p.OutputPreset)
		p.CurrentRevisionID = strings.TrimSpace(p.CurrentRevisionID)
		p.ActiveRenderJobID = strings.TrimSpace(p.ActiveRenderJobID)
	}
	if input.VideoProject.Revision != nil {
		r := input.VideoProject.Revision
		r.ID = strings.TrimSpace(r.ID)
		r.ProjectID = strings.TrimSpace(r.ProjectID)
		r.AccountScopeID = strings.TrimSpace(r.AccountScopeID)
		r.UserID = strings.TrimSpace(r.UserID)
		r.WorkspaceID = strings.TrimSpace(r.WorkspaceID)
		r.SessionID = strings.TrimSpace(r.SessionID)
		r.ParentRevisionID = strings.TrimSpace(r.ParentRevisionID)
		r.Description = strings.TrimSpace(r.Description)
		r.ChangeSummary = strings.TrimSpace(r.ChangeSummary)
		r.AuthorPrincipal = strings.TrimSpace(r.AuthorPrincipal)
		normalizeVideoTimeline(&r.Timeline)
	}
	if input.VideoProject.RenderJob != nil {
		j := input.VideoProject.RenderJob
		j.ID = strings.TrimSpace(j.ID)
		j.ProjectID = strings.TrimSpace(j.ProjectID)
		j.RevisionID = strings.TrimSpace(j.RevisionID)
		j.AccountScopeID = strings.TrimSpace(j.AccountScopeID)
		j.UserID = strings.TrimSpace(j.UserID)
		j.WorkspaceID = strings.TrimSpace(j.WorkspaceID)
		j.SessionID = strings.TrimSpace(j.SessionID)
		j.Status = strings.ToLower(strings.TrimSpace(j.Status))
		j.FailureCode = strings.ToLower(strings.TrimSpace(j.FailureCode))
		j.FailureReason = strings.TrimSpace(j.FailureReason)
		j.OutputPreset = normalizeVideoPreset(j.OutputPreset)
		j.OutputDigestSHA256 = strings.ToLower(strings.TrimSpace(j.OutputDigestSHA256))
	}
}

func normalizeVideoPreset(preset string) string {
	preset = strings.ToLower(strings.TrimSpace(preset))
	switch preset {
	case VideoPresetLandscapeVideo, VideoPresetLandscape1080p, "16:9", "landscape":
		return VideoPresetLandscape1080p
	case VideoPresetLandscape720p:
		return VideoPresetLandscape720p
	case VideoPresetPortraitVideo, VideoPresetPortrait1080p, "9:16", "portrait", "vertical":
		return VideoPresetPortrait1080p
	case VideoPresetPortrait720p:
		return VideoPresetPortrait720p
	case VideoPresetSquare1080p, "1:1", "square":
		return VideoPresetSquare1080p
	case VideoPresetSquare720p:
		return VideoPresetSquare720p
	case VideoPresetXHeader:
		return VideoPresetXHeader
	case "":
		return VideoPresetLandscape1080p
	default:
		return preset
	}
}

func resolvePresetDimensions(preset string) (width int, height int, fps float64) {
	switch normalizeVideoPreset(preset) {
	case VideoPresetLandscape1080p, VideoPresetLandscapeVideo:
		return 1920, 1080, 30.0
	case VideoPresetLandscape720p:
		return 1280, 720, 30.0
	case VideoPresetPortrait1080p, VideoPresetPortraitVideo:
		return 1080, 1920, 30.0
	case VideoPresetPortrait720p:
		return 720, 1280, 30.0
	case VideoPresetSquare1080p:
		return 1080, 1080, 30.0
	case VideoPresetSquare720p:
		return 720, 720, 30.0
	case VideoPresetXHeader:
		return 1500, 500, 30.0
	default:
		return 1920, 1080, 30.0
	}
}

func normalizeVideoTimeline(timeline *VideoProjectTimeline) {
	if timeline == nil {
		return
	}
	timeline.OutputPreset = normalizeVideoPreset(timeline.OutputPreset)
	defW, defH, defFPS := resolvePresetDimensions(timeline.OutputPreset)
	if timeline.Width <= 0 {
		timeline.Width = defW
	}
	if timeline.Height <= 0 {
		timeline.Height = defH
	}
	if timeline.FPS <= 0 {
		timeline.FPS = defFPS
	}
	if timeline.SchemaVersion == 0 {
		timeline.SchemaVersion = VideoTimelineSchemaVersion
	}
	var totalDuration int64
	for i := range timeline.Clips {
		clip := &timeline.Clips[i]
		clip.ID = strings.TrimSpace(clip.ID)
		clip.Name = strings.TrimSpace(clip.Name)
		clip.SourceKind = strings.ToLower(strings.TrimSpace(clip.SourceKind))
		clip.SourceRef = strings.TrimSpace(clip.SourceRef)
		if clip.Volume <= 0 && !clip.Muted {
			clip.Volume = 1.0
		} else if clip.Volume > 2.0 {
			clip.Volume = 2.0
		}
		if clip.DurationMs <= 0 && clip.SourceEndMs > clip.SourceStartMs {
			clip.DurationMs = clip.SourceEndMs - clip.SourceStartMs
		}
		if clip.TimelineEndMs <= 0 && clip.TimelineStartMs >= 0 && clip.DurationMs > 0 {
			clip.TimelineEndMs = clip.TimelineStartMs + clip.DurationMs
		}
		if clip.TimelineEndMs > totalDuration {
			totalDuration = clip.TimelineEndMs
		}
		for j := range clip.Captions {
			cap := &clip.Captions[j]
			cap.ID = strings.TrimSpace(cap.ID)
			cap.Text = strings.TrimSpace(cap.Text)
			cap.Position = strings.ToLower(strings.TrimSpace(cap.Position))
			if cap.Position == "" {
				cap.Position = "bottom"
			}
		}
		if clip.DesignInput != nil {
			clip.DesignInput.SessionID = strings.TrimSpace(clip.DesignInput.SessionID)
			clip.DesignInput.CollectionID = strings.TrimSpace(clip.DesignInput.CollectionID)
			clip.DesignInput.VariantID = strings.TrimSpace(clip.DesignInput.VariantID)
			clip.DesignInput.Action = strings.ToLower(strings.TrimSpace(clip.DesignInput.Action))
			clip.DesignInput.OverlayMode = strings.ToLower(strings.TrimSpace(clip.DesignInput.OverlayMode))
		}
	}
	if timeline.TotalDurationMs <= 0 {
		timeline.TotalDurationMs = totalDuration
	}
}

func validateV3VideoProjectMutationInput(input V3SessionMutationInput) error {
	if !isV3VideoProjectMutationKind(input.Kind) {
		if input.VideoProject != nil {
			return errors.New("video project payload requires a video project mutation kind")
		}
		return nil
	}
	if input.VideoProject == nil {
		return errors.New("video project mutation payload is required")
	}
	switch input.Kind {
	case V3SessionMutationCreateVideoProject:
		if input.VideoProject.Project == nil {
			return errors.New("create video project mutation requires project snapshot")
		}
		p := input.VideoProject.Project
		if p.ID == "" {
			return errors.New("video project id is required")
		}
		if p.Title == "" {
			return errors.New("video project title is required")
		}
		if len(p.Title) > 256 || len(p.Description) > 2048 {
			return errors.New("video project title or description exceeds length limits")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video project", p.SessionID, p.UserID, p.AccountScopeID); err != nil {
			return err
		}
		if input.VideoProject.Revision != nil {
			r := input.VideoProject.Revision
			if r.ProjectID != p.ID {
				return errors.New("initial revision project id does not match project")
			}
			if err := validateVideoTimeline(r.Timeline); err != nil {
				return fmt.Errorf("initial revision timeline invalid: %w", err)
			}
		}
	case V3SessionMutationUpdateVideoProject:
		if input.VideoProject.Project == nil {
			return errors.New("update video project mutation requires project snapshot")
		}
		p := input.VideoProject.Project
		if p.ID == "" {
			return errors.New("video project id is required")
		}
		if len(p.Title) > 256 || len(p.Description) > 2048 {
			return errors.New("video project title or description exceeds length limits")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video project", p.SessionID, p.UserID, p.AccountScopeID); err != nil {
			return err
		}
	case V3SessionMutationCreateVideoProjectRevision:
		if input.VideoProject.Revision == nil {
			return errors.New("create video project revision requires revision snapshot")
		}
		r := input.VideoProject.Revision
		if r.ID == "" {
			return errors.New("video project revision id is required")
		}
		if r.ProjectID == "" {
			return errors.New("video project id is required on revision")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video project revision", r.SessionID, r.UserID, r.AccountScopeID); err != nil {
			return err
		}
		if err := validateVideoTimeline(r.Timeline); err != nil {
			return fmt.Errorf("video timeline validation failed: %w", err)
		}
	case V3SessionMutationCreateVideoRenderJob:
		if input.VideoProject.RenderJob == nil {
			return errors.New("create video render job requires render job snapshot")
		}
		j := input.VideoProject.RenderJob
		if j.ID == "" {
			return errors.New("render job id is required")
		}
		if j.ProjectID == "" || j.RevisionID == "" {
			return errors.New("render job requires project id and revision id")
		}
		if j.Status != VideoRenderJobStatusQueued {
			return errors.New("new render job must have queued status")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video render job", j.SessionID, j.UserID, j.AccountScopeID); err != nil {
			return err
		}
	case V3SessionMutationUpdateVideoRenderJob:
		if input.VideoProject.RenderJob == nil {
			return errors.New("update video render job requires render job snapshot")
		}
		j := input.VideoProject.RenderJob
		if j.ID == "" {
			return errors.New("render job id is required")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video render job", j.SessionID, j.UserID, j.AccountScopeID); err != nil {
			return err
		}
		if !isValidRenderJobStatus(j.Status) {
			return fmt.Errorf("invalid render job status %q", j.Status)
		}
	}
	return nil
}

func isValidRenderJobStatus(status string) bool {
	switch status {
	case VideoRenderJobStatusQueued, VideoRenderJobStatusRendering, VideoRenderJobStatusReady,
		VideoRenderJobStatusFailed, VideoRenderJobStatusCancelled, VideoRenderJobStatusStale:
		return true
	default:
		return false
	}
}

func validateVideoTimeline(timeline VideoProjectTimeline) error {
	if len(timeline.Clips) > MaxClipsPerTimeline {
		return fmt.Errorf("timeline clip count %d exceeds maximum %d", len(timeline.Clips), MaxClipsPerTimeline)
	}
	if timeline.TotalDurationMs > MaxVideoTimelineDurationMs {
		return fmt.Errorf("timeline duration %d ms exceeds maximum %d ms", timeline.TotalDurationMs, MaxVideoTimelineDurationMs)
	}
	if timeline.Width <= 0 || timeline.Height <= 0 {
		return errors.New("timeline dimensions must be positive")
	}
	seenClipIDs := make(map[string]struct{}, len(timeline.Clips))
	for i, clip := range timeline.Clips {
		if clip.ID == "" {
			return fmt.Errorf("clip at index %d has empty id", i)
		}
		if _, duplicate := seenClipIDs[clip.ID]; duplicate {
			return fmt.Errorf("duplicate clip id %q in timeline", clip.ID)
		}
		seenClipIDs[clip.ID] = struct{}{}

		if clip.Track < 0 || clip.Sequence < 0 {
			return fmt.Errorf("clip %q has negative track or sequence", clip.ID)
		}
		switch clip.SourceKind {
		case VideoClipSourceKindSourceVideo:
			if clip.SourceRef == "" {
				return fmt.Errorf("source_video clip %q requires non-empty source_ref", clip.ID)
			}
			if clip.SourceStartMs < 0 || clip.SourceEndMs <= clip.SourceStartMs {
				return fmt.Errorf("clip %q has invalid source range [%d, %d]", clip.ID, clip.SourceStartMs, clip.SourceEndMs)
			}
		case VideoClipSourceKindManagedArtifact:
			if clip.ArtifactRef == nil && clip.DesignInput == nil {
				return fmt.Errorf("managed_artifact clip %q requires artifact_ref or design_input", clip.ID)
			}
		case VideoClipSourceKindColor, VideoClipSourceKindText:
			// valid synthetic sources
		default:
			return fmt.Errorf("clip %q has unsupported source kind %q", clip.ID, clip.SourceKind)
		}
		if clip.DurationMs <= 0 {
			return fmt.Errorf("clip %q duration must be positive", clip.ID)
		}
		if clip.TimelineStartMs < 0 || clip.TimelineEndMs <= clip.TimelineStartMs {
			return fmt.Errorf("clip %q has invalid timeline range [%d, %d]", clip.ID, clip.TimelineStartMs, clip.TimelineEndMs)
		}
		if len(clip.Captions) > MaxCaptionsPerClip {
			return fmt.Errorf("clip %q caption count %d exceeds maximum %d", clip.ID, len(clip.Captions), MaxCaptionsPerClip)
		}
		for cIdx, caption := range clip.Captions {
			if len(caption.Text) > MaxTextOverlayLength {
				return fmt.Errorf("clip %q caption %d text exceeds maximum %d characters", clip.ID, cIdx, MaxTextOverlayLength)
			}
			if caption.StartMs < 0 || (caption.EndMs > 0 && caption.EndMs <= caption.StartMs) {
				return fmt.Errorf("clip %q caption %d has invalid range [%d, %d]", clip.ID, cIdx, caption.StartMs, caption.EndMs)
			}
		}
	}
	return nil
}

func (s *SessionStore) prepareV3VideoProjectMutation(input V3SessionMutationInput, now int64) (preparedV3VideoProjectMutation, error) {
	if !isV3VideoProjectMutationKind(input.Kind) {
		return preparedV3VideoProjectMutation{}, nil
	}
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return preparedV3VideoProjectMutation{}, err
	}
	if session.AccountScopeID != input.AccountScopeID || (session.UserID != "" && session.UserID != input.UserID) {
		return preparedV3VideoProjectMutation{}, errors.New("session ownership does not match authenticated principal")
	}

	switch input.Kind {
	case V3SessionMutationCreateVideoProject:
		p := *input.VideoProject.Project
		p.AccountScopeID = input.AccountScopeID
		p.SessionID = input.SessionID
		p.UserID = input.UserID
		p.SchemaVersion = VideoProjectSchemaVersion
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
		p.UpdatedAt = now

		// Check if project already exists
		if _, exists, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, p.ID); err != nil {
			return preparedV3VideoProjectMutation{}, err
		} else if exists {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("video project %q already exists", p.ID)
		}

		projects, err := s.ListVideoProjects(input.AccountScopeID, input.SessionID, MaxVideoProjectsPerSession+1)
		if err != nil {
			return preparedV3VideoProjectMutation{}, err
		}
		if len(projects) >= MaxVideoProjectsPerSession {
			return preparedV3VideoProjectMutation{}, errors.New("maximum video projects per session reached")
		}

		var rev *VideoProjectRevisionSnapshot
		if input.VideoProject.Revision != nil {
			r := *input.VideoProject.Revision
			r.AccountScopeID = input.AccountScopeID
			r.SessionID = input.SessionID
			r.UserID = input.UserID
			r.ProjectID = p.ID
			r.RevisionNumber = 1
			r.SchemaVersion = VideoProjectRevisionSchemaVersion
			if r.ID == "" {
				r.ID = generateDeterministicOrRandomID("vrev")
			}
			if r.CreatedAt == 0 {
				r.CreatedAt = now
			}
			p.CurrentRevisionID = r.ID
			p.CurrentRevisionNumber = 1
			p.RevisionCount = 1
			rev = &r
		}

		return preparedV3VideoProjectMutation{
			Project:  &p,
			Revision: rev,
			Projection: V3VideoProjectProjection{
				ProjectID:         p.ID,
				CurrentRevisionID: p.CurrentRevisionID,
				RevisionNumber:    p.CurrentRevisionNumber,
			},
		}, nil

	case V3SessionMutationUpdateVideoProject:
		incoming := *input.VideoProject.Project
		existing, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, incoming.ID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", incoming.ID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		existing.Title = incoming.Title
		existing.Description = incoming.Description
		if incoming.OutputPreset != "" {
			existing.OutputPreset = incoming.OutputPreset
		}
		if incoming.ActiveRenderJobID != "" {
			existing.ActiveRenderJobID = incoming.ActiveRenderJobID
		}
		if incoming.Metadata != nil {
			existing.Metadata = incoming.Metadata
		}
		existing.UpdatedAt = now

		return preparedV3VideoProjectMutation{
			Project: &existing,
			Projection: V3VideoProjectProjection{
				ProjectID:         existing.ID,
				CurrentRevisionID: existing.CurrentRevisionID,
				RevisionNumber:    existing.CurrentRevisionNumber,
				RenderJobID:       existing.ActiveRenderJobID,
			},
		}, nil

	case V3SessionMutationCreateVideoProjectRevision:
		incomingRev := *input.VideoProject.Revision
		project, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, incomingRev.ProjectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", incomingRev.ProjectID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		if project.RevisionCount >= MaxVideoProjectRevisions {
			return preparedV3VideoProjectMutation{}, errors.New("maximum revisions per video project reached")
		}

		nextRevNumber := project.RevisionCount + 1
		incomingRev.AccountScopeID = input.AccountScopeID
		incomingRev.SessionID = input.SessionID
		incomingRev.UserID = input.UserID
		incomingRev.RevisionNumber = nextRevNumber
		incomingRev.ParentRevisionID = project.CurrentRevisionID
		incomingRev.SchemaVersion = VideoProjectRevisionSchemaVersion
		if incomingRev.ID == "" {
			incomingRev.ID = generateDeterministicOrRandomID("vrev")
		}
		if incomingRev.CreatedAt == 0 {
			incomingRev.CreatedAt = now
		}

		project.CurrentRevisionID = incomingRev.ID
		project.CurrentRevisionNumber = nextRevNumber
		project.RevisionCount = nextRevNumber
		project.UpdatedAt = now

		return preparedV3VideoProjectMutation{
			Project:  &project,
			Revision: &incomingRev,
			Projection: V3VideoProjectProjection{
				ProjectID:         project.ID,
				RevisionID:        incomingRev.ID,
				RevisionNumber:    incomingRev.RevisionNumber,
				CurrentRevisionID: project.CurrentRevisionID,
			},
		}, nil

	case V3SessionMutationCreateVideoRenderJob:
		incomingJob := *input.VideoProject.RenderJob
		project, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, incomingJob.ProjectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", incomingJob.ProjectID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		rev, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, incomingJob.ProjectID, incomingJob.RevisionID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project revision %q not found", incomingJob.RevisionID)
			}
			return preparedV3VideoProjectMutation{}, err
		}

		incomingJob.AccountScopeID = input.AccountScopeID
		incomingJob.SessionID = input.SessionID
		incomingJob.UserID = input.UserID
		incomingJob.RevisionNumber = rev.RevisionNumber
		incomingJob.SchemaVersion = VideoRenderJobSchemaVersion
		incomingJob.Status = VideoRenderJobStatusQueued
		incomingJob.Progress = 0.0
		if incomingJob.CreatedAt == 0 {
			incomingJob.CreatedAt = now
		}
		incomingJob.UpdatedAt = now

		project.ActiveRenderJobID = incomingJob.ID
		project.UpdatedAt = now

		return preparedV3VideoProjectMutation{
			Project:   &project,
			RenderJob: &incomingJob,
			Projection: V3VideoProjectProjection{
				ProjectID:   project.ID,
				RevisionID:  rev.ID,
				RenderJobID: incomingJob.ID,
				Status:      incomingJob.Status,
			},
		}, nil

	case V3SessionMutationUpdateVideoRenderJob:
		incomingJob := *input.VideoProject.RenderJob
		existing, ok, err := s.GetVideoRenderJob(input.AccountScopeID, input.SessionID, incomingJob.ID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video render job %q not found", incomingJob.ID)
			}
			return preparedV3VideoProjectMutation{}, err
		}

		if input.VideoProject.ExpectedStatus != "" && existing.Status != input.VideoProject.ExpectedStatus {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("render job status conflict: expected %s, actual %s", input.VideoProject.ExpectedStatus, existing.Status)
		}

		// State transition check
		if isTerminalRenderJobStatus(existing.Status) && incomingJob.Status != existing.Status {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("cannot transition terminal render job from %s to %s", existing.Status, incomingJob.Status)
		}

		existing.Status = incomingJob.Status
		if incomingJob.Progress > 0 {
			existing.Progress = incomingJob.Progress
		}
		if incomingJob.FailureCode != "" {
			existing.FailureCode = incomingJob.FailureCode
		}
		if incomingJob.FailureReason != "" {
			existing.FailureReason = incomingJob.FailureReason
		}
		if incomingJob.OutputPreset != "" {
			existing.OutputPreset = incomingJob.OutputPreset
		}
		if incomingJob.OutputWidth > 0 {
			existing.OutputWidth = incomingJob.OutputWidth
		}
		if incomingJob.OutputHeight > 0 {
			existing.OutputHeight = incomingJob.OutputHeight
		}
		if incomingJob.OutputFPS > 0 {
			existing.OutputFPS = incomingJob.OutputFPS
		}
		if incomingJob.OutputDurationMs > 0 {
			existing.OutputDurationMs = incomingJob.OutputDurationMs
		}
		if incomingJob.OutputSizeBytes > 0 {
			existing.OutputSizeBytes = incomingJob.OutputSizeBytes
		}
		if incomingJob.OutputDigestSHA256 != "" {
			existing.OutputDigestSHA256 = incomingJob.OutputDigestSHA256
		}
		if incomingJob.OutputArtifact != nil {
			existing.OutputArtifact = incomingJob.OutputArtifact
		}

		if incomingJob.Status == VideoRenderJobStatusRendering && existing.StartedAt == 0 {
			existing.StartedAt = now
		}
		if isTerminalRenderJobStatus(incomingJob.Status) && existing.CompletedAt == 0 {
			existing.CompletedAt = now
		}
		existing.UpdatedAt = now

		return preparedV3VideoProjectMutation{
			RenderJob: &existing,
			Projection: V3VideoProjectProjection{
				ProjectID:   existing.ProjectID,
				RevisionID:  existing.RevisionID,
				RenderJobID: existing.ID,
				Status:      existing.Status,
			},
		}, nil
	}

	return preparedV3VideoProjectMutation{}, errors.New("unhandled video project mutation kind")
}

func isTerminalRenderJobStatus(status string) bool {
	switch status {
	case VideoRenderJobStatusReady, VideoRenderJobStatusFailed, VideoRenderJobStatusCancelled:
		return true
	default:
		return false
	}
}

func setV3VideoProjectMutationInBatch(batch *pebble.Batch, prepared preparedV3VideoProjectMutation) error {
	if prepared.Project != nil {
		p := prepared.Project
		payload, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("marshal video project snapshot: %w", err)
		}
		if err := batch.Set([]byte(KeyVideoProject(p.AccountScopeID, p.SessionID, p.ID)), payload, nil); err != nil {
			return err
		}
	}
	if prepared.Revision != nil {
		r := prepared.Revision
		payload, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal video project revision snapshot: %w", err)
		}
		if err := batch.Set([]byte(KeyVideoProjectRevision(r.AccountScopeID, r.SessionID, r.ProjectID, r.ID)), payload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyVideoProjectRevisionByNumber(r.AccountScopeID, r.SessionID, r.ProjectID, r.RevisionNumber)), payload, nil); err != nil {
			return err
		}
	}
	if prepared.RenderJob != nil {
		j := prepared.RenderJob
		payload, err := json.Marshal(j)
		if err != nil {
			return fmt.Errorf("marshal video render job snapshot: %w", err)
		}
		if err := batch.Set([]byte(KeyVideoRenderJob(j.AccountScopeID, j.SessionID, j.ID)), payload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyVideoRenderJobByProject(j.AccountScopeID, j.SessionID, j.ProjectID, j.ID)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func generateDeterministicOrRandomID(prefix string) string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(buf[:8]))
}

// Store Query Methods

type CreateVideoProjectInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	WorkspaceID     string
	ProjectID       string
	Title           string
	Description     string
	OutputPreset    string
	InitialTimeline *VideoProjectTimeline
	Metadata        map[string]any
	ClientRequestID string
	NowUnixMs       int64
}

func (s *SessionStore) CreateVideoProject(input CreateVideoProjectInput) (VideoProjectSnapshot, *VideoProjectRevisionSnapshot, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	if input.ProjectID == "" {
		input.ProjectID = generateDeterministicOrRandomID("vproj")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	project := VideoProjectSnapshot{
		SchemaVersion:  VideoProjectSchemaVersion,
		ID:             input.ProjectID,
		AccountScopeID: input.AccountScopeID,
		UserID:         input.UserID,
		WorkspaceID:    input.WorkspaceID,
		SessionID:      input.SessionID,
		Title:          input.Title,
		Description:    input.Description,
		OutputPreset:   normalizeVideoPreset(input.OutputPreset),
		Metadata:       input.Metadata,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	var revision *VideoProjectRevisionSnapshot
	if input.InitialTimeline != nil {
		timeline := *input.InitialTimeline
		normalizeVideoTimeline(&timeline)
		rev := VideoProjectRevisionSnapshot{
			SchemaVersion:  VideoProjectRevisionSchemaVersion,
			ID:             generateDeterministicOrRandomID("vrev"),
			ProjectID:      project.ID,
			RevisionNumber: 1,
			AccountScopeID: input.AccountScopeID,
			UserID:         input.UserID,
			WorkspaceID:    input.WorkspaceID,
			SessionID:      input.SessionID,
			Description:    "Initial revision",
			ChangeSummary:  "Created initial timeline",
			Timeline:       timeline,
			CreatedAt:      now,
		}
		revision = &rev
	}

	clientReqID := input.ClientRequestID
	if clientReqID == "" {
		clientReqID = "create_video_project:" + project.ID
	}

	mutPayload, _ := json.Marshal(map[string]any{"project_id": project.ID, "title": project.Title})
	hash := sha256.Sum256(mutPayload)
	payloadHash := hex.EncodeToString(hash[:])

	mutation := V3SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          input.UserID,
		AccountScopeID:  input.AccountScopeID,
		ClientRequestID: clientReqID,
		IdempotencyKey:  clientReqID,
		PayloadHash:     payloadHash,
		Kind:            V3SessionMutationCreateVideoProject,
		VideoProject: &V3VideoProjectMutation{
			Project:  &project,
			Revision: revision,
		},
		NowUnixMs: now,
	}

	if _, err := s.ApplyV3SessionMutation(mutation); err != nil {
		return VideoProjectSnapshot{}, nil, err
	}

	storedProject, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, project.ID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("created video project could not be read")
		}
		return VideoProjectSnapshot{}, nil, err
	}

	var storedRev *VideoProjectRevisionSnapshot
	if storedProject.CurrentRevisionID != "" {
		r, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, storedProject.ID, storedProject.CurrentRevisionID)
		if err == nil && ok {
			storedRev = &r
		}
	}

	return storedProject, storedRev, nil
}

func (s *SessionStore) GetVideoProject(accountScopeID, sessionID, projectID string) (VideoProjectSnapshot, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || sessionID == "" || projectID == "" {
		return VideoProjectSnapshot{}, false, errors.New("account scope, session id, and project id are required")
	}
	var project VideoProjectSnapshot
	ok, err := s.store.GetJSON(KeyVideoProject(accountScopeID, sessionID, projectID), &project)
	if err != nil || !ok {
		return VideoProjectSnapshot{}, ok, err
	}
	return project, true, nil
}

func (s *SessionStore) ListVideoProjects(accountScopeID, sessionID string, limit int) ([]VideoProjectSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	if accountScopeID == "" || sessionID == "" {
		return nil, errors.New("account scope and session id are required")
	}
	if limit <= 0 || limit > MaxVideoProjectsPerSession {
		limit = MaxVideoProjectsPerSession
	}
	prefix := VideoProjectPrefix(accountScopeID, sessionID)
	projects := make([]VideoProjectSnapshot, 0)
	err := s.store.IteratePrefix(prefix, limit+1, func(_ string, value []byte) error {
		var p VideoProjectSnapshot
		if err := json.Unmarshal(value, &p); err != nil {
			return err
		}
		projects = append(projects, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].CreatedAt < projects[j].CreatedAt
	})
	if len(projects) > limit {
		projects = projects[:limit]
	}
	return projects, nil
}

type CreateVideoProjectRevisionInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	ProjectID       string
	RevisionID      string
	Description     string
	ChangeSummary   string
	Timeline        VideoProjectTimeline
	AuthorPrincipal string
	ClientRequestID string
	NowUnixMs       int64
}

func (s *SessionStore) CreateVideoProjectRevision(input CreateVideoProjectRevisionInput) (VideoProjectRevisionSnapshot, VideoProjectSnapshot, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	if input.RevisionID == "" {
		input.RevisionID = generateDeterministicOrRandomID("vrev")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	normalizeVideoTimeline(&input.Timeline)

	rev := VideoProjectRevisionSnapshot{
		SchemaVersion:   VideoProjectRevisionSchemaVersion,
		ID:              input.RevisionID,
		ProjectID:       input.ProjectID,
		AccountScopeID:  input.AccountScopeID,
		UserID:          input.UserID,
		SessionID:       input.SessionID,
		Description:     input.Description,
		ChangeSummary:   input.ChangeSummary,
		Timeline:        input.Timeline,
		AuthorPrincipal: input.AuthorPrincipal,
		CreatedAt:       now,
	}

	clientReqID := input.ClientRequestID
	if clientReqID == "" {
		clientReqID = fmt.Sprintf("create_revision:%s:%s", input.ProjectID, rev.ID)
	}

	mutPayload, _ := json.Marshal(map[string]any{"project_id": input.ProjectID, "revision_id": rev.ID})
	hash := sha256.Sum256(mutPayload)
	payloadHash := hex.EncodeToString(hash[:])

	mutation := V3SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          input.UserID,
		AccountScopeID:  input.AccountScopeID,
		ClientRequestID: clientReqID,
		IdempotencyKey:  clientReqID,
		PayloadHash:     payloadHash,
		Kind:            V3SessionMutationCreateVideoProjectRevision,
		VideoProject: &V3VideoProjectMutation{
			Revision: &rev,
		},
		NowUnixMs: now,
	}

	if _, err := s.ApplyV3SessionMutation(mutation); err != nil {
		return VideoProjectRevisionSnapshot{}, VideoProjectSnapshot{}, err
	}

	storedRev, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, input.ProjectID, rev.ID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("created revision could not be read")
		}
		return VideoProjectRevisionSnapshot{}, VideoProjectSnapshot{}, err
	}

	storedProject, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, input.ProjectID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("updated video project could not be read")
		}
		return VideoProjectRevisionSnapshot{}, VideoProjectSnapshot{}, err
	}

	return storedRev, storedProject, nil
}

func (s *SessionStore) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (VideoProjectRevisionSnapshot, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	revisionID = strings.TrimSpace(revisionID)
	if accountScopeID == "" || sessionID == "" || projectID == "" || revisionID == "" {
		return VideoProjectRevisionSnapshot{}, false, errors.New("account scope, session id, project id, and revision id are required")
	}
	var rev VideoProjectRevisionSnapshot
	ok, err := s.store.GetJSON(KeyVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID), &rev)
	if err != nil || !ok {
		return VideoProjectRevisionSnapshot{}, ok, err
	}
	return rev, true, nil
}

func (s *SessionStore) GetVideoProjectRevisionByNumber(accountScopeID, sessionID, projectID string, revisionNumber int) (VideoProjectRevisionSnapshot, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || sessionID == "" || projectID == "" || revisionNumber <= 0 {
		return VideoProjectRevisionSnapshot{}, false, errors.New("account scope, session id, project id, and positive revision number are required")
	}
	var rev VideoProjectRevisionSnapshot
	ok, err := s.store.GetJSON(KeyVideoProjectRevisionByNumber(accountScopeID, sessionID, projectID, revisionNumber), &rev)
	if err != nil || !ok {
		return VideoProjectRevisionSnapshot{}, ok, err
	}
	return rev, true, nil
}

func (s *SessionStore) ListVideoProjectRevisions(accountScopeID, sessionID, projectID string, limit int) ([]VideoProjectRevisionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || sessionID == "" || projectID == "" {
		return nil, errors.New("account scope, session id, and project id are required")
	}
	if limit <= 0 || limit > MaxVideoProjectRevisions {
		limit = MaxVideoProjectRevisions
	}
	prefix := VideoProjectRevisionPrefix(accountScopeID, sessionID, projectID)
	revisions := make([]VideoProjectRevisionSnapshot, 0)
	err := s.store.IteratePrefix(prefix, limit+1, func(_ string, value []byte) error {
		var r VideoProjectRevisionSnapshot
		if err := json.Unmarshal(value, &r); err != nil {
			return err
		}
		revisions = append(revisions, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].RevisionNumber < revisions[j].RevisionNumber
	})
	if len(revisions) > limit {
		revisions = revisions[:limit]
	}
	return revisions, nil
}

type CreateVideoRenderJobInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	ProjectID       string
	RevisionID      string
	JobID           string
	ClientRequestID string
	NowUnixMs       int64
}

func (s *SessionStore) CreateVideoRenderJob(input CreateVideoRenderJobInput) (VideoRenderJobSnapshot, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RevisionID = strings.TrimSpace(input.RevisionID)
	if input.JobID == "" {
		input.JobID = generateDeterministicOrRandomID("vren")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	job := VideoRenderJobSnapshot{
		SchemaVersion:  VideoRenderJobSchemaVersion,
		ID:             input.JobID,
		ProjectID:      input.ProjectID,
		RevisionID:     input.RevisionID,
		AccountScopeID: input.AccountScopeID,
		UserID:         input.UserID,
		SessionID:      input.SessionID,
		Status:         VideoRenderJobStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	clientReqID := input.ClientRequestID
	if clientReqID == "" {
		clientReqID = fmt.Sprintf("create_render_job:%s:%s", input.ProjectID, job.ID)
	}

	mutPayload, _ := json.Marshal(map[string]any{"job_id": job.ID, "project_id": input.ProjectID, "revision_id": input.RevisionID})
	hash := sha256.Sum256(mutPayload)
	payloadHash := hex.EncodeToString(hash[:])

	mutation := V3SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          input.UserID,
		AccountScopeID:  input.AccountScopeID,
		ClientRequestID: clientReqID,
		IdempotencyKey:  clientReqID,
		PayloadHash:     payloadHash,
		Kind:            V3SessionMutationCreateVideoRenderJob,
		VideoProject: &V3VideoProjectMutation{
			RenderJob: &job,
		},
		NowUnixMs: now,
	}

	if _, err := s.ApplyV3SessionMutation(mutation); err != nil {
		return VideoRenderJobSnapshot{}, err
	}

	storedJob, ok, err := s.GetVideoRenderJob(input.AccountScopeID, input.SessionID, job.ID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("created video render job could not be read")
		}
		return VideoRenderJobSnapshot{}, err
	}

	return storedJob, nil
}

type UpdateVideoRenderJobInput struct {
	AccountScopeID     string
	UserID             string
	SessionID          string
	JobID              string
	Status             string
	ExpectedStatus     string
	Progress           float64
	FailureCode        string
	FailureReason      string
	OutputPreset       string
	OutputWidth        int
	OutputHeight       int
	OutputFPS          float64
	OutputDurationMs   int64
	OutputSizeBytes    int64
	OutputDigestSHA256 string
	OutputArtifact     *SessionArtifactSelectionReference
	ClientRequestID    string
	NowUnixMs          int64
}

func (s *SessionStore) UpdateVideoRenderJob(input UpdateVideoRenderJobInput) (VideoRenderJobSnapshot, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.JobID = strings.TrimSpace(input.JobID)
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	job := VideoRenderJobSnapshot{
		ID:                 input.JobID,
		AccountScopeID:     input.AccountScopeID,
		UserID:             input.UserID,
		SessionID:          input.SessionID,
		Status:             strings.ToLower(strings.TrimSpace(input.Status)),
		Progress:           input.Progress,
		FailureCode:        strings.ToLower(strings.TrimSpace(input.FailureCode)),
		FailureReason:      strings.TrimSpace(input.FailureReason),
		OutputPreset:       normalizeVideoPreset(input.OutputPreset),
		OutputWidth:        input.OutputWidth,
		OutputHeight:       input.OutputHeight,
		OutputFPS:          input.OutputFPS,
		OutputDurationMs:   input.OutputDurationMs,
		OutputSizeBytes:    input.OutputSizeBytes,
		OutputDigestSHA256: strings.ToLower(strings.TrimSpace(input.OutputDigestSHA256)),
		OutputArtifact:     input.OutputArtifact,
		UpdatedAt:          now,
	}

	clientReqID := input.ClientRequestID
	if clientReqID == "" {
		clientReqID = fmt.Sprintf("update_render_job:%s:%s:%d", input.JobID, job.Status, now)
	}

	mutPayload, _ := json.Marshal(map[string]any{"job_id": job.ID, "status": job.Status, "progress": job.Progress})
	hash := sha256.Sum256(mutPayload)
	payloadHash := hex.EncodeToString(hash[:])

	mutation := V3SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          input.UserID,
		AccountScopeID:  input.AccountScopeID,
		ClientRequestID: clientReqID,
		IdempotencyKey:  clientReqID,
		PayloadHash:     payloadHash,
		Kind:            V3SessionMutationUpdateVideoRenderJob,
		VideoProject: &V3VideoProjectMutation{
			RenderJob:      &job,
			ExpectedStatus: input.ExpectedStatus,
		},
		NowUnixMs: now,
	}

	if _, err := s.ApplyV3SessionMutation(mutation); err != nil {
		return VideoRenderJobSnapshot{}, err
	}

	storedJob, ok, err := s.GetVideoRenderJob(input.AccountScopeID, input.SessionID, job.ID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("updated video render job could not be read")
		}
		return VideoRenderJobSnapshot{}, err
	}

	return storedJob, nil
}

func (s *SessionStore) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (VideoRenderJobSnapshot, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if accountScopeID == "" || sessionID == "" || jobID == "" {
		return VideoRenderJobSnapshot{}, false, errors.New("account scope, session id, and job id are required")
	}
	var job VideoRenderJobSnapshot
	ok, err := s.store.GetJSON(KeyVideoRenderJob(accountScopeID, sessionID, jobID), &job)
	if err != nil || !ok {
		return VideoRenderJobSnapshot{}, ok, err
	}
	return job, true, nil
}

func (s *SessionStore) ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]VideoRenderJobSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || sessionID == "" {
		return nil, errors.New("account scope and session id are required")
	}
	if limit <= 0 || limit > MaxVideoRenderJobsPerProject {
		limit = MaxVideoRenderJobsPerProject
	}
	prefix := VideoRenderJobPrefix(accountScopeID, sessionID)
	if projectID != "" {
		prefix = VideoRenderJobByProjectPrefix(accountScopeID, sessionID, projectID)
	}
	jobs := make([]VideoRenderJobSnapshot, 0)
	err := s.store.IteratePrefix(prefix, limit+1, func(_ string, value []byte) error {
		var j VideoRenderJobSnapshot
		if err := json.Unmarshal(value, &j); err != nil {
			return err
		}
		jobs = append(jobs, j)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt < jobs[j].CreatedAt
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}
