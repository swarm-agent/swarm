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
	VideoEditProposalSchemaVersion    = 1

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

	VideoProjectKindVideoTool = "video_tool"

	VideoClipSourceKindSourceVideo     = "source_video"
	VideoClipSourceKindSourceAudio     = "source_audio"
	VideoClipSourceKindManagedArtifact = "managed_artifact"
	VideoClipSourceKindColor           = "color"
	VideoClipSourceKindText            = "text"

	VideoTransitionKindCut              = "cut"
	VideoTransitionKindFadeThroughBlack = "fade_through_black"
	VideoTransitionKindCrossfade        = "crossfade"
	VideoTransitionKindFadeToBlack      = "fade_to_black"
	VideoTransitionKindFadeFromBlack    = "fade_from_black"

	VideoEditProposalStatusPending  = "pending"
	VideoEditProposalStatusAccepted = "accepted"
	VideoEditProposalStatusRejected = "rejected"

	VideoEditOperationAddClip          = "add_clip"
	VideoEditOperationUpdateClip       = "update_clip"
	VideoEditOperationReplaceClip      = "replace_clip"
	VideoEditOperationRemoveClip       = "remove_clip"
	VideoEditOperationAddTransition    = "add_transition"
	VideoEditOperationUpdateTransition = "update_transition"
	VideoEditOperationRemoveTransition = "remove_transition"

	V3SessionMutationCreateVideoProject              = "video.project.create"
	V3SessionMutationUpdateVideoProject              = "video.project.update"
	V3SessionMutationCreateVideoProjectRevision      = "video.project.revision.create"
	V3SessionMutationCreateVideoRenderJob            = "video.render_job.create"
	V3SessionMutationUpdateVideoRenderJob            = "video.render_job.update"
	V3SessionMutationCreateVideoEditProposal         = "video.edit_proposal.create"
	V3SessionMutationSelectVideoAnimationCandidate   = "video.edit_proposal.animation.select"
	V3SessionMutationPromoteVideoAnimationDerivative = "video.edit_proposal.animation.promote"
	V3SessionMutationAcceptVideoEditProposal         = "video.edit_proposal.accept"
	V3SessionMutationRejectVideoEditProposal         = "video.edit_proposal.reject"

	MaxVideoProjectsPerSession     = 32
	MaxVideoProjectRevisions       = 100
	MaxVideoRenderJobsPerProject   = 50
	MaxRecoverableVideoRenderJobs  = 500
	MaxClipsPerTimeline            = 100
	MaxCaptionsPerClip             = 50
	MaxVideoTimelineDurationMs     = 3600000 // 1 hour
	MaxTextOverlayLength           = 500
	MaxTransitionsPerTimeline      = 100
	MaxVideoEditProposalOperations = 200
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

// VideoAudioPolicy contains the timeline-level audio controls honored by the renderer.
type VideoAudioPolicy struct {
	MasterVolume float64 `json:"master_volume,omitempty"` // 0.0 means default 1.0; supported range is (0, 2.0].
	Muted        bool    `json:"muted,omitempty"`
}

// VideoTimelineClip represents one ordered clip in the video timeline.
type VideoTimelineClip struct {
	ID              string                             `json:"id"`
	Name            string                             `json:"name,omitempty"`
	Track           int                                `json:"track"`
	Sequence        int                                `json:"sequence"`
	SourceKind      string                             `json:"source_kind"` // "source_video", "source_audio", "managed_artifact", "color", "text"
	SourceRef       string                             `json:"source_ref,omitempty"`
	AudioSource     *AudioSourceReference              `json:"audio_source,omitempty"`
	ArtifactRef     *SessionArtifactSelectionReference `json:"artifact_ref,omitempty"`
	MediaType       string                             `json:"media_type,omitempty"`
	SourceStartMs   int64                              `json:"source_start_ms"`
	SourceEndMs     int64                              `json:"source_end_ms"`
	TimelineStartMs int64                              `json:"timeline_start_ms"`
	TimelineEndMs   int64                              `json:"timeline_end_ms"`
	DurationMs      int64                              `json:"duration_ms"`
	Visible         bool                               `json:"visible"`
	Layer           int                                `json:"layer,omitempty"`
	Volume          float64                            `json:"volume,omitempty"`
	Muted           bool                               `json:"muted,omitempty"`
	Captions        []VideoTextOverlay                 `json:"captions,omitempty"`
	DesignInput     *VideoDesignInputReference         `json:"design_input,omitempty"`
}

// VideoTimelineTransition is a first-class relationship between clips, never a clip attribute.
type VideoTimelineTransition struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	FromClipID string `json:"from_clip_id"`
	ToClipID   string `json:"to_clip_id"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// VideoProjectTimeline defines the structured, versioned timeline contract.
type VideoProjectTimeline struct {
	SchemaVersion   int                       `json:"schema_version"`
	OutputPreset    string                    `json:"output_preset"`
	Width           int                       `json:"width"`
	Height          int                       `json:"height"`
	FPS             float64                   `json:"fps"`
	TotalDurationMs int64                     `json:"total_duration_ms"`
	Clips           []VideoTimelineClip       `json:"clips"`
	Transitions     []VideoTimelineTransition `json:"transitions,omitempty"`
	AudioPolicy     *VideoAudioPolicy         `json:"audio_policy,omitempty"`
	Metadata        map[string]any            `json:"metadata,omitempty"`
}

// VideoProjectSnapshot represents a durable session-owned video project.
type VideoProjectSnapshot struct {
	SchemaVersion           int            `json:"schema_version"`
	ID                      string         `json:"id"`
	AccountScopeID          string         `json:"account_scope_id"`
	UserID                  string         `json:"user_id,omitempty"`
	WorkspaceID             string         `json:"workspace_id,omitempty"`
	SessionID               string         `json:"session_id"`
	Title                   string         `json:"title"`
	Description             string         `json:"description,omitempty"`
	OutputPreset            string         `json:"output_preset"`
	CurrentRevisionID       string         `json:"current_revision_id,omitempty"`
	CurrentRevisionNumber   int            `json:"current_revision_number,omitempty"`
	ConfirmedRevisionID     string         `json:"confirmed_revision_id,omitempty"`
	ConfirmedRevisionNumber int            `json:"confirmed_revision_number,omitempty"`
	RevisionCount           int            `json:"revision_count"`
	ActiveRenderJobID       string         `json:"active_render_job_id,omitempty"`
	Metadata                map[string]any `json:"metadata,omitempty"`
	ProjectKind             string         `json:"project_kind,omitempty"`
	CreatedAt               int64          `json:"created_at"`
	UpdatedAt               int64          `json:"updated_at"`
	EventSeq                uint64         `json:"event_seq,omitempty"`
}

// VideoProjectRevisionSnapshot is an immutable revision of a video project timeline.
type VideoProjectRevisionSnapshot struct {
	SchemaVersion          int                  `json:"schema_version"`
	ID                     string               `json:"id"`
	ProjectID              string               `json:"project_id"`
	RevisionNumber         int                  `json:"revision_number"`
	AccountScopeID         string               `json:"account_scope_id"`
	UserID                 string               `json:"user_id,omitempty"`
	WorkspaceID            string               `json:"workspace_id,omitempty"`
	SessionID              string               `json:"session_id"`
	ParentRevisionID       string               `json:"parent_revision_id,omitempty"`
	RestoredFromRevisionID string               `json:"restored_from_revision_id,omitempty"`
	AcceptedProposalID     string               `json:"accepted_proposal_id,omitempty"`
	OriginProposalID       string               `json:"origin_proposal_id,omitempty"`
	Description            string               `json:"description,omitempty"`
	ChangeSummary          string               `json:"change_summary,omitempty"`
	Timeline               VideoProjectTimeline `json:"timeline"`
	AuthorPrincipal        string               `json:"author_principal,omitempty"`
	CreatedAt              int64                `json:"created_at"`
	EventSeq               uint64               `json:"event_seq,omitempty"`
}

// VideoAnimationCandidate is one exact ready HTML animation that can be reviewed
// live through swarm-player/v1 without becoming render-ready timeline media.
type VideoAnimationCandidate struct {
	ID     string                             `json:"id"`
	Source *SessionArtifactSelectionReference `json:"source"`
	Label  string                             `json:"label,omitempty"`
}

// VideoAnimationCandidateSet keeps one-of-many HTML review authority separate
// from the selected MP4 derivative used by the durable timeline and renderer.
type VideoAnimationCandidateSet struct {
	Candidates          []VideoAnimationCandidate          `json:"candidates"`
	SelectedCandidateID string                             `json:"selected_candidate_id,omitempty"`
	SelectedSource      *SessionArtifactSelectionReference `json:"selected_source,omitempty"`
	Derivative          *SessionArtifactSelectionReference `json:"derivative,omitempty"`
	Status              string                             `json:"status"` // awaiting_selection, awaiting_export, ready, failed
	FailureReason       string                             `json:"failure_reason,omitempty"`
}

// VideoPlanPart is one stable pre-production section. Visual is the render-ready
// image/MP4 authority; AnimationCandidates optionally supplies live HTML choices
// before a selected HTML source is exported and promoted into Visual.
type VideoPlanPart struct {
	ID                  string                             `json:"id"`
	Title               string                             `json:"title"`
	DurationMs          int64                              `json:"duration_ms"`
	Narration           string                             `json:"narration,omitempty"`
	OnScreenText        string                             `json:"on_screen_text,omitempty"`
	VisualDirection     string                             `json:"visual_direction,omitempty"`
	TransitionIn        string                             `json:"transition_in,omitempty"`
	Caption             *VideoTextOverlay                  `json:"caption,omitempty"`
	Transition          *VideoTimelineTransition           `json:"transition,omitempty"`
	Visual              *SessionArtifactSelectionReference `json:"visual"`
	VisualMediaType     string                             `json:"visual_media_type,omitempty"`
	SourceStartMs       int64                              `json:"source_start_ms,omitempty"`
	SourceEndMs         int64                              `json:"source_end_ms,omitempty"`
	AnimationCandidates *VideoAnimationCandidateSet        `json:"animation_candidates,omitempty"`
}

const (
	VideoPlanKindInitial  = "initial"
	VideoPlanKindRevision = "revision"
)

// VideoPlanProposal is a visual review object. Initial plans are accepted as one
// complete structure; revision plans may replace selected stable parts.
type VideoPlanProposal struct {
	Kind    string          `json:"kind,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Parts   []VideoPlanPart `json:"parts"`
}

// VideoEditOperation is a bounded typed change. Exactly the payload required by Type is used.
type VideoEditOperation struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	ClipID       string                   `json:"clip_id,omitempty"`
	Clip         *VideoTimelineClip       `json:"clip,omitempty"`
	TransitionID string                   `json:"transition_id,omitempty"`
	Transition   *VideoTimelineTransition `json:"transition,omitempty"`
}

// VideoTimelineRange identifies an affected half-open range in the accepted cut.
type VideoTimelineRange struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// VideoEditProposalSnapshot is durable review state bound to one exact immutable base revision.
type VideoEditProposalSnapshot struct {
	SchemaVersion         int                  `json:"schema_version"`
	ID                    string               `json:"id"`
	ProjectID             string               `json:"project_id"`
	BaseRevisionID        string               `json:"base_revision_id"`
	BaseRevisionNumber    int                  `json:"base_revision_number"`
	AccountScopeID        string               `json:"account_scope_id"`
	UserID                string               `json:"user_id,omitempty"`
	SessionID             string               `json:"session_id"`
	Status                string               `json:"status"`
	Title                 string               `json:"title,omitempty"`
	Rationale             string               `json:"rationale,omitempty"`
	Plan                  *VideoPlanProposal   `json:"plan,omitempty"`
	Operations            []VideoEditOperation `json:"operations,omitempty"`
	AffectedRanges        []VideoTimelineRange `json:"affected_ranges,omitempty"`
	RejectionFeedback     string               `json:"rejection_feedback,omitempty"`
	AcceptedOperationIDs  []string             `json:"accepted_operation_ids,omitempty"`
	AcceptedRevisionID    string               `json:"accepted_revision_id,omitempty"`
	WorkingRevisionID     string               `json:"working_revision_id,omitempty"`
	WorkingRevisionNumber int                  `json:"working_revision_number,omitempty"`
	CreatedAt             int64                `json:"created_at"`
	UpdatedAt             int64                `json:"updated_at"`
	EventSeq              uint64               `json:"event_seq,omitempty"`
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
	Project              *VideoProjectSnapshot            `json:"project,omitempty"`
	Revision             *VideoProjectRevisionSnapshot    `json:"revision,omitempty"`
	RenderJob            *VideoRenderJobSnapshot          `json:"render_job,omitempty"`
	EditProposal         *VideoEditProposalSnapshot       `json:"edit_proposal,omitempty"`
	SelectedOperationIDs []string                         `json:"selected_operation_ids,omitempty"`
	AnimationSelection   *VideoAnimationSelectionMutation `json:"animation_selection,omitempty"`
	ExpectedStatus       string                           `json:"expected_status,omitempty"`
}

// VideoAnimationSelectionMutation updates one stable plan part. Promotion carries
// the exact selected HTML source and its ready MP4 derivative in one mutation.
type VideoAnimationSelectionMutation struct {
	PartID              string                             `json:"part_id"`
	SelectedCandidateID string                             `json:"selected_candidate_id"`
	SelectedSource      *SessionArtifactSelectionReference `json:"selected_source,omitempty"`
	Derivative          *SessionArtifactSelectionReference `json:"derivative,omitempty"`
}

// V3VideoProjectProjection summarizes the projection update resulting from a mutation.
type V3VideoProjectProjection struct {
	ProjectID         string `json:"project_id,omitempty"`
	RevisionID        string `json:"revision_id,omitempty"`
	RevisionNumber    int    `json:"revision_number,omitempty"`
	RenderJobID       string `json:"render_job_id,omitempty"`
	Status            string `json:"status,omitempty"`
	ProposalID        string `json:"proposal_id,omitempty"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
}

type preparedV3VideoProjectMutation struct {
	Project      *VideoProjectSnapshot
	Revision     *VideoProjectRevisionSnapshot
	RenderJob    *VideoRenderJobSnapshot
	EditProposal *VideoEditProposalSnapshot
	Projection   V3VideoProjectProjection
}

// Key functions for pebble storage
func KeyVideoProject(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/project/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
}

func VideoProjectPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/video_project/project/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func VideoProjectAccountPrefix(accountScopeID string) string {
	return fmt.Sprintf("v3/video_project/project/%s/", keyPart(accountScopeID))
}

func KeyPrimaryVideoToolProject(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/video_project/primary_video_tool/%s/%s", keyPart(accountScopeID), keyPart(sessionID))
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

func VideoProjectRevisionByNumberPrefix(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/revision_by_num/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
}

func KeyVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) string {
	return fmt.Sprintf("v3/video_project/edit_proposal/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID), keyPart(proposalID))
}

func VideoEditProposalPrefix(accountScopeID, sessionID, projectID string) string {
	return fmt.Sprintf("v3/video_project/edit_proposal/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(projectID))
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
		V3SessionMutationUpdateVideoRenderJob,
		V3SessionMutationCreateVideoEditProposal,
		V3SessionMutationSelectVideoAnimationCandidate,
		V3SessionMutationPromoteVideoAnimationDerivative,
		V3SessionMutationAcceptVideoEditProposal,
		V3SessionMutationRejectVideoEditProposal:
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
		p.ProjectKind = strings.ToLower(strings.TrimSpace(p.ProjectKind))
		p.CurrentRevisionID = strings.TrimSpace(p.CurrentRevisionID)
		p.ConfirmedRevisionID = strings.TrimSpace(p.ConfirmedRevisionID)
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
		r.RestoredFromRevisionID = strings.TrimSpace(r.RestoredFromRevisionID)
		r.AcceptedProposalID = strings.TrimSpace(r.AcceptedProposalID)
		r.OriginProposalID = strings.TrimSpace(r.OriginProposalID)
		r.Description = strings.TrimSpace(r.Description)
		r.ChangeSummary = strings.TrimSpace(r.ChangeSummary)
		r.AuthorPrincipal = strings.TrimSpace(r.AuthorPrincipal)
		normalizeVideoTimeline(&r.Timeline)
	}
	if input.VideoProject.EditProposal != nil {
		p := input.VideoProject.EditProposal
		p.ID = strings.TrimSpace(p.ID)
		p.ProjectID = strings.TrimSpace(p.ProjectID)
		p.BaseRevisionID = strings.TrimSpace(p.BaseRevisionID)
		p.AccountScopeID = strings.TrimSpace(p.AccountScopeID)
		p.UserID = strings.TrimSpace(p.UserID)
		p.SessionID = strings.TrimSpace(p.SessionID)
		p.Status = strings.ToLower(strings.TrimSpace(p.Status))
		p.Title = strings.TrimSpace(p.Title)
		p.Rationale = strings.TrimSpace(p.Rationale)
		p.RejectionFeedback = strings.TrimSpace(p.RejectionFeedback)
		p.AcceptedRevisionID = strings.TrimSpace(p.AcceptedRevisionID)
		p.WorkingRevisionID = strings.TrimSpace(p.WorkingRevisionID)
		if p.Plan != nil {
			p.Plan.Kind = strings.ToLower(strings.TrimSpace(p.Plan.Kind))
			p.Plan.Summary = strings.TrimSpace(p.Plan.Summary)
			for i := range p.Plan.Parts {
				part := &p.Plan.Parts[i]
				part.ID = strings.TrimSpace(part.ID)
				part.Title = strings.TrimSpace(part.Title)
				part.Narration = strings.TrimSpace(part.Narration)
				part.OnScreenText = strings.TrimSpace(part.OnScreenText)
				part.VisualDirection = strings.TrimSpace(part.VisualDirection)
				part.TransitionIn = strings.TrimSpace(part.TransitionIn)
				part.VisualMediaType = strings.ToLower(strings.TrimSpace(part.VisualMediaType))
				if part.Caption != nil {
					part.Caption.ID = strings.TrimSpace(part.Caption.ID)
					part.Caption.Text = strings.TrimSpace(part.Caption.Text)
					part.Caption.Position = strings.ToLower(strings.TrimSpace(part.Caption.Position))
				}
				if part.Transition != nil {
					normalizeVideoTransition(part.Transition)
				}
				if part.Visual != nil {
					normalizeVideoArtifactReference(part.Visual)
				}
				if candidates := part.AnimationCandidates; candidates != nil {
					candidates.SelectedCandidateID = strings.TrimSpace(candidates.SelectedCandidateID)
					candidates.Status = strings.ToLower(strings.TrimSpace(candidates.Status))
					candidates.FailureReason = strings.TrimSpace(candidates.FailureReason)
					if candidates.SelectedSource != nil {
						normalizeVideoArtifactReference(candidates.SelectedSource)
					}
					if candidates.Derivative != nil {
						normalizeVideoArtifactReference(candidates.Derivative)
					}
					for candidateIndex := range candidates.Candidates {
						candidate := &candidates.Candidates[candidateIndex]
						candidate.ID = strings.TrimSpace(candidate.ID)
						candidate.Label = strings.TrimSpace(candidate.Label)
						if candidate.Source != nil {
							normalizeVideoArtifactReference(candidate.Source)
						}
					}
				}
			}
		}
		for i := range p.Operations {
			op := &p.Operations[i]
			op.ID = strings.TrimSpace(op.ID)
			op.Type = strings.ToLower(strings.TrimSpace(op.Type))
			op.ClipID = strings.TrimSpace(op.ClipID)
			op.TransitionID = strings.TrimSpace(op.TransitionID)
			if op.Clip != nil {
				op.Clip.ID = strings.TrimSpace(op.Clip.ID)
				op.Clip.Name = strings.TrimSpace(op.Clip.Name)
				op.Clip.SourceKind = strings.ToLower(strings.TrimSpace(op.Clip.SourceKind))
				op.Clip.SourceRef = strings.TrimSpace(op.Clip.SourceRef)
			}
			if op.Transition != nil {
				normalizeVideoTransition(op.Transition)
			}
		}
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
	if timeline.AudioPolicy != nil && timeline.AudioPolicy.MasterVolume == 0 && !timeline.AudioPolicy.Muted {
		timeline.AudioPolicy.MasterVolume = 1.0
	}
	var totalDuration int64
	for i := range timeline.Transitions {
		normalizeVideoTransition(&timeline.Transitions[i])
	}
	for i := range timeline.Clips {
		clip := &timeline.Clips[i]
		clip.ID = strings.TrimSpace(clip.ID)
		clip.Name = strings.TrimSpace(clip.Name)
		clip.SourceKind = strings.ToLower(strings.TrimSpace(clip.SourceKind))
		clip.SourceRef = strings.TrimSpace(clip.SourceRef)
		clip.MediaType = strings.ToLower(strings.TrimSpace(clip.MediaType))
		if clip.AudioSource != nil {
			clip.AudioSource.Ref = strings.TrimSpace(clip.AudioSource.Ref)
			clip.AudioSource.Name = strings.TrimSpace(clip.AudioSource.Name)
			clip.AudioSource.MIMEType = strings.ToLower(strings.TrimSpace(clip.AudioSource.MIMEType))
			clip.AudioSource.SourceFingerprint = strings.ToLower(strings.TrimSpace(clip.AudioSource.SourceFingerprint))
			clip.AudioSource.FingerprintVersion = strings.TrimSpace(clip.AudioSource.FingerprintVersion)
			if clip.MediaType == "" {
				clip.MediaType = clip.AudioSource.MIMEType
			}
		}
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

func normalizeVideoArtifactReference(ref *SessionArtifactSelectionReference) {
	if ref == nil {
		return
	}
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
}

func normalizeVideoTransition(transition *VideoTimelineTransition) {
	if transition == nil {
		return
	}
	transition.ID = strings.TrimSpace(transition.ID)
	transition.Kind = strings.ToLower(strings.TrimSpace(transition.Kind))
	transition.FromClipID = strings.TrimSpace(transition.FromClipID)
	transition.ToClipID = strings.TrimSpace(transition.ToClipID)
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
	case V3SessionMutationCreateVideoEditProposal, V3SessionMutationSelectVideoAnimationCandidate, V3SessionMutationPromoteVideoAnimationDerivative, V3SessionMutationAcceptVideoEditProposal, V3SessionMutationRejectVideoEditProposal:
		proposal := input.VideoProject.EditProposal
		if proposal == nil {
			return errors.New("video edit proposal snapshot is required")
		}
		if proposal.ID == "" || proposal.ProjectID == "" || proposal.BaseRevisionID == "" {
			return errors.New("proposal id, project id, and base revision id are required")
		}
		if err := validateV3MutationEmbeddedOwnership(input, "video edit proposal", proposal.SessionID, proposal.UserID, proposal.AccountScopeID); err != nil {
			return err
		}
		if input.Kind == V3SessionMutationSelectVideoAnimationCandidate || input.Kind == V3SessionMutationPromoteVideoAnimationDerivative {
			if input.VideoProject.AnimationSelection == nil || input.VideoProject.AnimationSelection.PartID == "" || input.VideoProject.AnimationSelection.SelectedCandidateID == "" {
				return errors.New("animation candidate mutation requires part_id and selected_candidate_id")
			}
			if input.Kind == V3SessionMutationPromoteVideoAnimationDerivative && input.VideoProject.AnimationSelection.Derivative == nil {
				return errors.New("animation derivative promotion requires a derivative reference")
			}
		} else if input.Kind == V3SessionMutationCreateVideoEditProposal {
			if proposal.Status != VideoEditProposalStatusPending {
				return errors.New("new video edit proposal must be pending")
			}
			if proposal.Plan != nil {
				if len(proposal.Operations) != 0 {
					return errors.New("video plan proposal must be one atomic plan object without timeline operations")
				}
				if err := validateVideoPlanProposal(*proposal.Plan); err != nil {
					return err
				}
			} else if err := validateVideoEditOperations(proposal.Operations); err != nil {
				return err
			}
		} else if input.Kind == V3SessionMutationAcceptVideoEditProposal {
			if input.VideoProject.Revision == nil {
				return errors.New("accept video edit proposal requires revision snapshot")
			}
			if err := validateV3MutationEmbeddedOwnership(input, "accepted video project revision", input.VideoProject.Revision.SessionID, input.VideoProject.Revision.UserID, input.VideoProject.Revision.AccountScopeID); err != nil {
				return err
			}
		} else if proposal.Status != VideoEditProposalStatusRejected {
			return errors.New("rejected video edit proposal must have rejected status")
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

const (
	VideoAnimationCandidateStatusAwaitingSelection = "awaiting_selection"
	VideoAnimationCandidateStatusAwaitingExport    = "awaiting_export"
	VideoAnimationCandidateStatusReady             = "ready"
	VideoAnimationCandidateStatusFailed            = "failed"
)

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
	if len(timeline.Transitions) > MaxTransitionsPerTimeline {
		return fmt.Errorf("timeline transition count %d exceeds maximum %d", len(timeline.Transitions), MaxTransitionsPerTimeline)
	}
	if timeline.AudioPolicy != nil && (timeline.AudioPolicy.MasterVolume < 0 || timeline.AudioPolicy.MasterVolume > 2) {
		return errors.New("timeline master volume must be between 0 and 2")
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
			if clip.AudioSource != nil {
				return fmt.Errorf("source_video clip %q cannot carry an audio_source", clip.ID)
			}
		case VideoClipSourceKindSourceAudio:
			if err := validateAudioTimelineClip(clip); err != nil {
				return err
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
		if clip.SourceKind != VideoClipSourceKindSourceAudio && clip.AudioSource != nil {
			return fmt.Errorf("clip %q can only carry audio_source for source_audio", clip.ID)
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
	seenTransitionIDs := make(map[string]struct{}, len(timeline.Transitions))
	for i, transition := range timeline.Transitions {
		if transition.ID == "" {
			return fmt.Errorf("transition at index %d has empty id", i)
		}
		if _, exists := seenTransitionIDs[transition.ID]; exists {
			return fmt.Errorf("duplicate transition id %q in timeline", transition.ID)
		}
		seenTransitionIDs[transition.ID] = struct{}{}
		if _, ok := seenClipIDs[transition.FromClipID]; !ok {
			return fmt.Errorf("transition %q references missing from clip %q", transition.ID, transition.FromClipID)
		}
		if _, ok := seenClipIDs[transition.ToClipID]; !ok {
			return fmt.Errorf("transition %q references missing to clip %q", transition.ID, transition.ToClipID)
		}
		for _, clip := range timeline.Clips {
			if (clip.ID == transition.FromClipID || clip.ID == transition.ToClipID) && clip.SourceKind == VideoClipSourceKindSourceAudio {
				return fmt.Errorf("transition %q cannot reference audio-only clip %q", transition.ID, clip.ID)
			}
		}
		if transition.FromClipID == transition.ToClipID {
			return fmt.Errorf("transition %q must connect distinct clips", transition.ID)
		}
		switch transition.Kind {
		case VideoTransitionKindCut:
			if transition.DurationMs != 0 {
				return fmt.Errorf("cut transition %q must have zero duration", transition.ID)
			}
		case VideoTransitionKindFadeThroughBlack, VideoTransitionKindCrossfade, VideoTransitionKindFadeToBlack, VideoTransitionKindFadeFromBlack:
			if transition.DurationMs <= 0 {
				return fmt.Errorf("transition %q duration must be positive", transition.ID)
			}
		default:
			return fmt.Errorf("transition %q has unsupported kind %q", transition.ID, transition.Kind)
		}
	}
	return nil
}

func validateAudioTimelineClip(clip VideoTimelineClip) error {
	if clip.AudioSource == nil {
		return fmt.Errorf("source_audio clip %q requires audio_source", clip.ID)
	}
	ref := clip.AudioSource
	if clip.SourceRef != "" || clip.ArtifactRef != nil || clip.DesignInput != nil {
		return fmt.Errorf("source_audio clip %q must use only its exact audio_source reference", clip.ID)
	}
	refDigest := strings.TrimPrefix(ref.Ref, "audiosrc_")
	_, refDigestErr := hex.DecodeString(refDigest)
	_, fingerprintErr := hex.DecodeString(ref.SourceFingerprint)
	if !strings.HasPrefix(ref.Ref, "audiosrc_") || len(refDigest) != 64 || refDigestErr != nil ||
		ref.Name == "" || !strings.HasPrefix(ref.MIMEType, "audio/") || ref.SizeBytes <= 0 ||
		len(ref.SourceFingerprint) != 64 || fingerprintErr != nil || ref.FingerprintVersion != AudioSourceFingerprintV1 {
		return fmt.Errorf("source_audio clip %q requires a complete exact audio_source reference", clip.ID)
	}
	if clip.MediaType != "" && !strings.EqualFold(clip.MediaType, ref.MIMEType) {
		return fmt.Errorf("source_audio clip %q media_type does not match audio_source", clip.ID)
	}
	if clip.SourceStartMs < 0 || clip.SourceEndMs <= clip.SourceStartMs {
		return fmt.Errorf("source_audio clip %q has invalid source range [%d, %d]", clip.ID, clip.SourceStartMs, clip.SourceEndMs)
	}
	if clip.DurationMs != clip.SourceEndMs-clip.SourceStartMs {
		return fmt.Errorf("source_audio clip %q duration must match its source trim", clip.ID)
	}
	if clip.DurationMs != clip.TimelineEndMs-clip.TimelineStartMs {
		return fmt.Errorf("source_audio clip %q duration must match its timeline range", clip.ID)
	}
	if clip.Visible {
		return fmt.Errorf("source_audio clip %q must not be visible", clip.ID)
	}
	if len(clip.Captions) != 0 {
		return fmt.Errorf("source_audio clip %q cannot carry visual captions", clip.ID)
	}
	if clip.Volume < 0 || clip.Volume > 2 {
		return fmt.Errorf("source_audio clip %q gain must be between 0 and 2", clip.ID)
	}
	return nil
}

func validateVideoTimelineRanges(ranges []VideoTimelineRange, durationMs int64) error {
	if len(ranges) > MaxVideoEditProposalOperations {
		return errors.New("video edit proposal affected ranges must be bounded")
	}
	for i, timelineRange := range ranges {
		if timelineRange.StartMs < 0 || timelineRange.EndMs <= timelineRange.StartMs {
			return fmt.Errorf("affected range at index %d must be a non-empty half-open range", i)
		}
		if durationMs > 0 && timelineRange.EndMs > durationMs {
			return fmt.Errorf("affected range at index %d exceeds base revision duration", i)
		}
	}
	return nil
}

func validateVideoPlanProposal(plan VideoPlanProposal) error {
	if len(plan.Parts) == 0 || len(plan.Parts) > MaxClipsPerTimeline {
		return errors.New("video plan parts must be non-empty and bounded")
	}
	if plan.Kind != VideoPlanKindInitial && plan.Kind != VideoPlanKindRevision {
		return errors.New("video plan kind must be initial or revision")
	}
	seen := make(map[string]struct{}, len(plan.Parts))
	for index, part := range plan.Parts {
		if part.ID == "" || part.Title == "" {
			return fmt.Errorf("video plan part at index %d requires id and title", index)
		}
		if part.DurationMs <= 0 || part.DurationMs > MaxVideoTimelineDurationMs {
			return fmt.Errorf("video plan part %q requires a positive bounded duration_ms", part.ID)
		}
		if part.Visual == nil || part.Visual.SessionID == "" || part.Visual.CollectionID == "" || part.Visual.VariantID == "" || part.Visual.EventSeq == 0 {
			return fmt.Errorf("video plan part %q requires one complete exact render-ready visual reference", part.ID)
		}
		if candidates := part.AnimationCandidates; candidates != nil {
			if len(candidates.Candidates) < 2 || len(candidates.Candidates) > 16 {
				return fmt.Errorf("video plan part %q animation candidates must contain 2 to 16 choices", part.ID)
			}
			seenCandidates := make(map[string]struct{}, len(candidates.Candidates))
			for _, candidate := range candidates.Candidates {
				if candidate.ID == "" || candidate.Source == nil || candidate.Source.SessionID == "" || candidate.Source.CollectionID == "" || candidate.Source.VariantID == "" || candidate.Source.EventSeq == 0 {
					return fmt.Errorf("video plan part %q requires complete exact HTML animation candidates", part.ID)
				}
				if _, duplicate := seenCandidates[candidate.ID]; duplicate {
					return fmt.Errorf("video plan part %q has duplicate animation candidate %q", part.ID, candidate.ID)
				}
				seenCandidates[candidate.ID] = struct{}{}
			}
			switch candidates.Status {
			case VideoAnimationCandidateStatusAwaitingSelection:
				if candidates.SelectedCandidateID != "" || candidates.SelectedSource != nil || candidates.Derivative != nil {
					return fmt.Errorf("video plan part %q awaiting_selection cannot carry a selection or derivative", part.ID)
				}
			case VideoAnimationCandidateStatusAwaitingExport:
				if _, ok := seenCandidates[candidates.SelectedCandidateID]; !ok || candidates.SelectedSource == nil || candidates.Derivative != nil {
					return fmt.Errorf("video plan part %q awaiting_export requires one selected HTML source and no derivative", part.ID)
				}
			case VideoAnimationCandidateStatusReady:
				if _, ok := seenCandidates[candidates.SelectedCandidateID]; !ok || candidates.SelectedSource == nil || candidates.Derivative == nil {
					return fmt.Errorf("video plan part %q ready animation requires selected HTML source and MP4 derivative", part.ID)
				}
			case VideoAnimationCandidateStatusFailed:
				if candidates.FailureReason == "" {
					return fmt.Errorf("video plan part %q failed animation requires a failure reason", part.ID)
				}
			default:
				return fmt.Errorf("video plan part %q has unsupported animation candidate status %q", part.ID, candidates.Status)
			}
		}
		isImage := strings.HasPrefix(part.VisualMediaType, "image/")
		isVideo := part.VisualMediaType == "video/mp4"
		if !isImage && !isVideo {
			return fmt.Errorf("video plan part %q visual must resolve to an image or video/mp4 artifact", part.ID)
		}
		if isVideo {
			if part.SourceStartMs < 0 || part.SourceEndMs <= part.SourceStartMs {
				return fmt.Errorf("video plan part %q requires a non-empty MP4 source range", part.ID)
			}
			if part.SourceEndMs-part.SourceStartMs != part.DurationMs {
				return fmt.Errorf("video plan part %q duration_ms must match its MP4 source range", part.ID)
			}
		} else if part.SourceStartMs != 0 || part.SourceEndMs != 0 {
			return fmt.Errorf("video plan part %q image visual cannot declare an MP4 source range", part.ID)
		}
		if _, exists := seen[part.ID]; exists {
			return fmt.Errorf("duplicate video plan part id %q", part.ID)
		}
		seen[part.ID] = struct{}{}
		for _, value := range []string{part.Title, part.Narration, part.OnScreenText, part.VisualDirection, part.TransitionIn} {
			if len(value) > MaxTextOverlayLength {
				return fmt.Errorf("video plan part %q field exceeds maximum %d characters", part.ID, MaxTextOverlayLength)
			}
		}
		if part.Caption != nil {
			if part.Caption.ID == "" || part.Caption.Text == "" || part.Caption.StartMs < 0 || part.Caption.EndMs <= part.Caption.StartMs || part.Caption.EndMs > part.DurationMs {
				return fmt.Errorf("video plan part %q caption requires a stable id, text, and bounded part-relative range", part.ID)
			}
			if len(part.Caption.Text) > MaxTextOverlayLength {
				return fmt.Errorf("video plan part %q caption exceeds maximum %d characters", part.ID, MaxTextOverlayLength)
			}
		}
		if part.Transition != nil {
			if part.Transition.ID == "" || part.Transition.Kind == "" || part.Transition.FromClipID == "" || part.Transition.ToClipID != part.ID {
				return fmt.Errorf("video plan part %q transition requires a stable id, kind, from_clip_id, and matching to_clip_id", part.ID)
			}
			if part.Transition.Kind != VideoTransitionKindCut && part.Transition.Kind != VideoTransitionKindCrossfade {
				return fmt.Errorf("video plan part %q transition kind is unsupported", part.ID)
			}
			if part.Transition.DurationMs < 0 || (part.Transition.Kind == VideoTransitionKindCut && part.Transition.DurationMs != 0) || (part.Transition.Kind == VideoTransitionKindCrossfade && part.Transition.DurationMs <= 0) {
				return fmt.Errorf("video plan part %q transition duration is invalid", part.ID)
			}
		}
	}
	return nil
}

func acceptedVideoPlanFromTimeline(timeline VideoProjectTimeline) (*VideoPlanProposal, error) {
	raw := timeline.Metadata["accepted_video_plan"]
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode accepted video plan: %w", err)
	}
	var plan VideoPlanProposal
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, fmt.Errorf("decode accepted video plan: %w", err)
	}
	return &plan, nil
}

func mergeAcceptedVideoPlan(accepted *VideoPlanProposal, proposed VideoPlanProposal, selected []string) (VideoPlanProposal, error) {
	if accepted == nil {
		if proposed.Kind != VideoPlanKindInitial {
			return VideoPlanProposal{}, errors.New("the first visual video plan must have kind initial")
		}
		if len(selected) != 0 {
			return VideoPlanProposal{}, errors.New("the initial visual video plan must be accepted as one object")
		}
		return proposed, nil
	}
	if proposed.Kind != VideoPlanKindRevision {
		return VideoPlanProposal{}, errors.New("an accepted visual video plan can only be changed by a revision plan")
	}
	wanted := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		id = strings.TrimSpace(id)
		if id != "" {
			wanted[id] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return VideoPlanProposal{}, errors.New("select at least one proposed visual plan part")
	}
	proposedByID := make(map[string]VideoPlanPart, len(proposed.Parts))
	for _, part := range proposed.Parts {
		proposedByID[part.ID] = part
	}
	merged := *accepted
	merged.Kind = VideoPlanKindInitial
	merged.Parts = append([]VideoPlanPart(nil), accepted.Parts...)
	for index, part := range merged.Parts {
		if _, ok := wanted[part.ID]; !ok {
			continue
		}
		replacement, exists := proposedByID[part.ID]
		if !exists {
			return VideoPlanProposal{}, fmt.Errorf("selected visual plan part %q is absent from the proposal", part.ID)
		}
		merged.Parts[index] = replacement
		delete(wanted, part.ID)
	}
	for _, part := range proposed.Parts {
		if _, ok := wanted[part.ID]; !ok {
			continue
		}
		merged.Parts = append(merged.Parts, part)
		delete(wanted, part.ID)
	}
	if len(wanted) != 0 {
		for id := range wanted {
			return VideoPlanProposal{}, fmt.Errorf("selected visual plan part %q is absent from the proposal", id)
		}
	}
	if proposed.Summary != "" {
		merged.Summary = proposed.Summary
	}
	return merged, nil
}

func visualVideoPlanTimeline(base VideoProjectTimeline, plan VideoPlanProposal) VideoProjectTimeline {
	timeline := base
	if timeline.Width <= 0 || timeline.Height <= 0 {
		timeline.Width, timeline.Height = 1920, 1080
	}
	if timeline.FPS <= 0 {
		timeline.FPS = 30
	}
	if timeline.OutputPreset == "" {
		timeline.OutputPreset = VideoPresetLandscape1080p
	}
	planPartIDs := make(map[string]struct{}, len(plan.Parts))
	for _, part := range plan.Parts {
		planPartIDs[part.ID] = struct{}{}
	}
	preservedClips := make([]VideoTimelineClip, 0, len(base.Clips))
	for _, clip := range base.Clips {
		if _, isPlanPart := planPartIDs[clip.ID]; !isPlanPart {
			preservedClips = append(preservedClips, clip)
		}
	}
	preservedTransitions := make([]VideoTimelineTransition, 0, len(base.Transitions))
	for _, transition := range base.Transitions {
		_, fromPlanPart := planPartIDs[transition.FromClipID]
		_, toPlanPart := planPartIDs[transition.ToClipID]
		if !fromPlanPart || !toPlanPart {
			preservedTransitions = append(preservedTransitions, transition)
		}
	}
	timeline.Clips = make([]VideoTimelineClip, 0, len(plan.Parts)+len(preservedClips))
	timeline.Transitions = nil
	startMs := int64(0)
	for index, part := range plan.Parts {
		endMs := startMs + part.DurationMs
		clip := VideoTimelineClip{
			ID: part.ID, Name: part.Title + " | Narration: " + part.Narration + " | Planned still: " + part.VisualDirection,
			Track: 0, Sequence: index, SourceKind: VideoClipSourceKindManagedArtifact, ArtifactRef: part.Visual,
			MediaType: part.VisualMediaType, TimelineStartMs: startMs, TimelineEndMs: endMs, DurationMs: part.DurationMs,
			SourceStartMs: part.SourceStartMs, SourceEndMs: part.SourceEndMs, Visible: true,
		}
		if part.VisualMediaType != "video/mp4" {
			clip.SourceEndMs = part.DurationMs
		}
		if part.Caption != nil {
			caption := *part.Caption
			caption.StartMs += startMs
			caption.EndMs += startMs
			clip.Captions = []VideoTextOverlay{caption}
		}
		timeline.Clips = append(timeline.Clips, clip)
		if part.Transition != nil {
			timeline.Transitions = append(timeline.Transitions, *part.Transition)
		}
		startMs = endMs
	}
	timeline.Clips = append(timeline.Clips, preservedClips...)
	timeline.Transitions = append(timeline.Transitions, preservedTransitions...)
	for _, clip := range preservedClips {
		endMs := clip.TimelineEndMs
		if endMs == 0 {
			endMs = clip.TimelineStartMs + clip.DurationMs
		}
		if endMs > startMs {
			startMs = endMs
		}
	}
	timeline.TotalDurationMs = startMs
	return timeline
}

func videoProposalSelection(proposal VideoEditProposalSnapshot) []string {
	if proposal.Plan != nil {
		if proposal.Plan.Kind == VideoPlanKindInitial {
			return nil
		}
		selected := make([]string, 0, len(proposal.Plan.Parts))
		for _, part := range proposal.Plan.Parts {
			selected = append(selected, part.ID)
		}
		return selected
	}
	selected := make([]string, 0, len(proposal.Operations))
	for _, operation := range proposal.Operations {
		selected = append(selected, operation.ID)
	}
	return selected
}

func applyVideoProposal(base VideoProjectTimeline, proposal VideoEditProposalSnapshot, selected []string) (VideoProjectTimeline, error) {
	if proposal.Plan == nil {
		return applyVideoEditOperations(base, proposal.Operations, selected)
	}
	acceptedPlan, err := acceptedVideoPlanFromTimeline(base)
	if err != nil {
		return VideoProjectTimeline{}, err
	}
	mergedPlan, err := mergeAcceptedVideoPlan(acceptedPlan, *proposal.Plan, selected)
	if err != nil {
		return VideoProjectTimeline{}, err
	}
	timeline := visualVideoPlanTimeline(base, mergedPlan)
	metadata := make(map[string]any, len(timeline.Metadata)+2)
	for key, value := range timeline.Metadata {
		metadata[key] = value
	}
	metadata["accepted_video_plan"] = mergedPlan
	metadata["accepted_video_plan_proposal_id"] = proposal.ID
	timeline.Metadata = metadata
	return timeline, nil
}

func validateVideoEditOperations(operations []VideoEditOperation) error {
	if len(operations) == 0 || len(operations) > MaxVideoEditProposalOperations {
		return errors.New("video edit proposal operations must be non-empty and bounded")
	}
	seen := make(map[string]struct{}, len(operations))
	for i, op := range operations {
		if op.ID == "" {
			return fmt.Errorf("video edit operation at index %d has empty id", i)
		}
		if _, exists := seen[op.ID]; exists {
			return fmt.Errorf("duplicate video edit operation id %q", op.ID)
		}
		seen[op.ID] = struct{}{}
		switch op.Type {
		case VideoEditOperationAddClip, VideoEditOperationUpdateClip:
			if op.Clip == nil || op.Clip.ID == "" {
				return fmt.Errorf("operation %q requires clip payload with id", op.ID)
			}
		case VideoEditOperationReplaceClip:
			if op.ClipID == "" || op.Clip == nil || op.Clip.ID == "" {
				return fmt.Errorf("operation %q requires clip_id and replacement clip payload with id", op.ID)
			}
		case VideoEditOperationRemoveClip:
			if op.ClipID == "" {
				return fmt.Errorf("operation %q requires clip_id", op.ID)
			}
		case VideoEditOperationAddTransition, VideoEditOperationUpdateTransition:
			if op.Transition == nil || op.Transition.ID == "" {
				return fmt.Errorf("operation %q requires transition payload with id", op.ID)
			}
		case VideoEditOperationRemoveTransition:
			if op.TransitionID == "" {
				return fmt.Errorf("operation %q requires transition_id", op.ID)
			}
		default:
			return fmt.Errorf("operation %q has unsupported type %q", op.ID, op.Type)
		}
	}
	return nil
}

func applyVideoEditOperations(base VideoProjectTimeline, operations []VideoEditOperation, selected []string) (VideoProjectTimeline, error) {
	wanted := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	if len(wanted) == 0 {
		return VideoProjectTimeline{}, errors.New("at least one operation must be selected")
	}
	for _, op := range operations {
		if _, ok := wanted[op.ID]; !ok {
			continue
		}
		delete(wanted, op.ID)
		switch op.Type {
		case VideoEditOperationAddClip:
			base.Clips = append(base.Clips, *op.Clip)
		case VideoEditOperationUpdateClip:
			found := false
			for i := range base.Clips {
				if base.Clips[i].ID == op.Clip.ID {
					base.Clips[i] = *op.Clip
					found = true
					break
				}
			}
			if !found {
				return VideoProjectTimeline{}, fmt.Errorf("clip %q not found", op.Clip.ID)
			}
		case VideoEditOperationReplaceClip:
			found := false
			for i := range base.Clips {
				if base.Clips[i].ID == op.ClipID {
					base.Clips[i] = *op.Clip
					found = true
					break
				}
			}
			if !found {
				return VideoProjectTimeline{}, fmt.Errorf("clip %q not found", op.ClipID)
			}
			if op.Clip.ID != op.ClipID {
				for i := range base.Transitions {
					if base.Transitions[i].FromClipID == op.ClipID {
						base.Transitions[i].FromClipID = op.Clip.ID
					}
					if base.Transitions[i].ToClipID == op.ClipID {
						base.Transitions[i].ToClipID = op.Clip.ID
					}
				}
			}
		case VideoEditOperationRemoveClip:
			found := false
			clips := base.Clips[:0]
			for _, clip := range base.Clips {
				if clip.ID == op.ClipID {
					found = true
					continue
				}
				clips = append(clips, clip)
			}
			if !found {
				return VideoProjectTimeline{}, fmt.Errorf("clip %q not found", op.ClipID)
			}
			base.Clips = clips
			transitions := base.Transitions[:0]
			for _, transition := range base.Transitions {
				if transition.FromClipID == op.ClipID || transition.ToClipID == op.ClipID {
					continue
				}
				transitions = append(transitions, transition)
			}
			base.Transitions = transitions
		case VideoEditOperationAddTransition:
			base.Transitions = append(base.Transitions, *op.Transition)
		case VideoEditOperationUpdateTransition:
			found := false
			for i := range base.Transitions {
				if base.Transitions[i].ID == op.Transition.ID {
					base.Transitions[i] = *op.Transition
					found = true
					break
				}
			}
			if !found {
				return VideoProjectTimeline{}, fmt.Errorf("transition %q not found", op.Transition.ID)
			}
		case VideoEditOperationRemoveTransition:
			found := false
			transitions := base.Transitions[:0]
			for _, transition := range base.Transitions {
				if transition.ID == op.TransitionID {
					found = true
					continue
				}
				transitions = append(transitions, transition)
			}
			if !found {
				return VideoProjectTimeline{}, fmt.Errorf("transition %q not found", op.TransitionID)
			}
			base.Transitions = transitions
		}
	}
	if len(wanted) != 0 {
		return VideoProjectTimeline{}, errors.New("selected operation id does not belong to proposal")
	}
	normalizeVideoTimeline(&base)
	if err := validateVideoTimeline(base); err != nil {
		return VideoProjectTimeline{}, err
	}
	return base, nil
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

		if p.ProjectKind != "" && p.ProjectKind != VideoProjectKindVideoTool {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("unsupported video project kind %q", p.ProjectKind)
		}
		if p.ProjectKind == VideoProjectKindVideoTool {
			if existing, exists, err := s.GetPrimaryVideoToolProject(input.AccountScopeID, input.SessionID); err != nil {
				return preparedV3VideoProjectMutation{}, err
			} else if exists && existing.ID != p.ID {
				return preparedV3VideoProjectMutation{}, errors.New("primary Video Tool project already exists for session")
			}
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
			p.ConfirmedRevisionID = r.ID
			p.ConfirmedRevisionNumber = 1
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
		project.ConfirmedRevisionID = incomingRev.ID
		project.ConfirmedRevisionNumber = nextRevNumber
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

	case V3SessionMutationCreateVideoEditProposal:
		proposal := *input.VideoProject.EditProposal
		project, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, proposal.ProjectID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video project %q not found", proposal.ProjectID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		if project.UserID != "" && project.UserID != input.UserID {
			return preparedV3VideoProjectMutation{}, errors.New("video project ownership does not match authenticated principal")
		}
		base, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, proposal.ProjectID, proposal.BaseRevisionID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("base revision %q not found", proposal.BaseRevisionID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		if _, exists, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, proposal.ProjectID, proposal.ID); err != nil {
			return preparedV3VideoProjectMutation{}, err
		} else if exists {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("video edit proposal %q already exists", proposal.ID)
		}
		if project.CurrentRevisionID != proposal.BaseRevisionID {
			return preparedV3VideoProjectMutation{}, errors.New("video edit proposal base revision must be the current project revision")
		}
		if proposal.Plan != nil {
			acceptedPlan, planErr := acceptedVideoPlanFromTimeline(base.Timeline)
			if planErr != nil {
				return preparedV3VideoProjectMutation{}, planErr
			}
			if acceptedPlan == nil && proposal.Plan.Kind != VideoPlanKindInitial {
				return preparedV3VideoProjectMutation{}, errors.New("the first visual video plan must have kind initial")
			}
			if acceptedPlan != nil && proposal.Plan.Kind != VideoPlanKindRevision {
				return preparedV3VideoProjectMutation{}, errors.New("an accepted visual video plan requires kind revision")
			}
		}
		if err := validateVideoTimelineRanges(proposal.AffectedRanges, base.Timeline.TotalDurationMs); err != nil {
			return preparedV3VideoProjectMutation{}, err
		}
		proposal.SchemaVersion = VideoEditProposalSchemaVersion
		proposal.AccountScopeID = input.AccountScopeID
		proposal.UserID = input.UserID
		proposal.SessionID = input.SessionID
		proposal.BaseRevisionNumber = base.RevisionNumber
		proposal.Status = VideoEditProposalStatusPending
		proposal.CreatedAt = now
		proposal.UpdatedAt = now

		workingTimeline, err := applyVideoProposal(base.Timeline, proposal, videoProposalSelection(proposal))
		if err != nil {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("build video working revision: %w", err)
		}
		if err := validateVideoTimeline(workingTimeline); err != nil {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("video working revision validation failed: %w", err)
		}
		if project.RevisionCount >= MaxVideoProjectRevisions {
			return preparedV3VideoProjectMutation{}, errors.New("maximum revisions per video project reached")
		}
		if project.ConfirmedRevisionID == "" {
			project.ConfirmedRevisionID = project.CurrentRevisionID
			project.ConfirmedRevisionNumber = project.CurrentRevisionNumber
		}
		workingRevision := VideoProjectRevisionSnapshot{
			SchemaVersion: VideoProjectRevisionSchemaVersion, ID: generateDeterministicOrRandomID("vrev"), ProjectID: project.ID,
			RevisionNumber: project.RevisionCount + 1, AccountScopeID: input.AccountScopeID, UserID: input.UserID,
			WorkspaceID: project.WorkspaceID, SessionID: input.SessionID, ParentRevisionID: project.CurrentRevisionID,
			OriginProposalID: proposal.ID, Description: proposal.Rationale, ChangeSummary: proposal.Title,
			Timeline: workingTimeline, AuthorPrincipal: "swarm", CreatedAt: now,
		}
		proposal.WorkingRevisionID = workingRevision.ID
		proposal.WorkingRevisionNumber = workingRevision.RevisionNumber
		project.CurrentRevisionID = workingRevision.ID
		project.CurrentRevisionNumber = workingRevision.RevisionNumber
		project.RevisionCount = workingRevision.RevisionNumber
		project.UpdatedAt = now
		return preparedV3VideoProjectMutation{
			Project: &project, Revision: &workingRevision, EditProposal: &proposal,
			Projection: V3VideoProjectProjection{ProjectID: proposal.ProjectID, RevisionID: workingRevision.ID, RevisionNumber: workingRevision.RevisionNumber, CurrentRevisionID: workingRevision.ID, ProposalID: proposal.ID, Status: proposal.Status},
		}, nil

	case V3SessionMutationSelectVideoAnimationCandidate, V3SessionMutationPromoteVideoAnimationDerivative:
		incoming := input.VideoProject.EditProposal
		selection := input.VideoProject.AnimationSelection
		proposal, ok, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, incoming.ProjectID, incoming.ID)
		if err != nil || !ok {
			return preparedV3VideoProjectMutation{}, errors.New("video edit proposal not found")
		}
		if proposal.Status != VideoEditProposalStatusPending || proposal.Plan == nil {
			return preparedV3VideoProjectMutation{}, errors.New("animation candidates require a pending visual plan proposal")
		}
		partIndex, candidateIndex := -1, -1
		for index := range proposal.Plan.Parts {
			if proposal.Plan.Parts[index].ID != selection.PartID {
				continue
			}
			partIndex = index
			if proposal.Plan.Parts[index].AnimationCandidates != nil {
				for candidate := range proposal.Plan.Parts[index].AnimationCandidates.Candidates {
					if proposal.Plan.Parts[index].AnimationCandidates.Candidates[candidate].ID == selection.SelectedCandidateID {
						candidateIndex = candidate
						break
					}
				}
			}
			break
		}
		if partIndex < 0 || candidateIndex < 0 {
			return preparedV3VideoProjectMutation{}, errors.New("selected animation candidate was not found on the stable video part")
		}
		part := &proposal.Plan.Parts[partIndex]
		candidates := part.AnimationCandidates
		candidate := candidates.Candidates[candidateIndex]
		if selection.SelectedSource == nil || *selection.SelectedSource != *candidate.Source {
			return preparedV3VideoProjectMutation{}, errors.New("selected HTML source must exactly match the chosen candidate")
		}
		candidates.SelectedCandidateID = selection.SelectedCandidateID
		candidates.SelectedSource = selection.SelectedSource
		candidates.FailureReason = ""
		if input.Kind == V3SessionMutationSelectVideoAnimationCandidate {
			candidates.Status = VideoAnimationCandidateStatusAwaitingExport
			candidates.Derivative = nil
		} else {
			candidates.Status = VideoAnimationCandidateStatusReady
			candidates.Derivative = selection.Derivative
			part.Visual = selection.Derivative
			part.VisualMediaType = "video/mp4"
			part.SourceStartMs = 0
			part.SourceEndMs = part.DurationMs
		}
		proposal.UpdatedAt = now
		working, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, proposal.ProjectID, proposal.WorkingRevisionID)
		if err != nil || !ok {
			return preparedV3VideoProjectMutation{}, errors.New("video animation working revision not found")
		}
		if input.Kind == V3SessionMutationPromoteVideoAnimationDerivative {
			working.Timeline = visualVideoPlanTimeline(working.Timeline, *proposal.Plan)
			if working.Timeline.Metadata == nil {
				working.Timeline.Metadata = map[string]any{}
			}
			working.Timeline.Metadata["accepted_video_plan"] = *proposal.Plan
		}
		return preparedV3VideoProjectMutation{Revision: &working, EditProposal: &proposal, Projection: V3VideoProjectProjection{ProjectID: proposal.ProjectID, RevisionID: working.ID, RevisionNumber: working.RevisionNumber, CurrentRevisionID: working.ID, ProposalID: proposal.ID, Status: proposal.Status}}, nil

	case V3SessionMutationAcceptVideoEditProposal:
		incoming := input.VideoProject.EditProposal
		proposal, ok, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, incoming.ProjectID, incoming.ID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video edit proposal %q not found", incoming.ID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		if proposal.UserID != "" && proposal.UserID != input.UserID {
			return preparedV3VideoProjectMutation{}, errors.New("video edit proposal ownership does not match authenticated principal")
		}
		if proposal.Status != VideoEditProposalStatusPending {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("video edit proposal is %s", proposal.Status)
		}
		project, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, proposal.ProjectID)
		if err != nil || !ok {
			return preparedV3VideoProjectMutation{}, errors.New("video project not found")
		}
		if project.UserID != "" && project.UserID != input.UserID {
			return preparedV3VideoProjectMutation{}, errors.New("video project ownership does not match authenticated principal")
		}
		if project.CurrentRevisionID != proposal.WorkingRevisionID || project.CurrentRevisionNumber != proposal.WorkingRevisionNumber {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("stale video edit proposal: working revision %s is not current revision %s", proposal.WorkingRevisionID, project.CurrentRevisionID)
		}
		if proposal.Plan != nil {
			for _, part := range proposal.Plan.Parts {
				if part.AnimationCandidates != nil && part.AnimationCandidates.Status != VideoAnimationCandidateStatusReady {
					return preparedV3VideoProjectMutation{}, fmt.Errorf("video plan part %q animation derivative is not ready", part.ID)
				}
			}
		}
		base, ok, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, proposal.ProjectID, proposal.BaseRevisionID)
		if err != nil || !ok {
			return preparedV3VideoProjectMutation{}, errors.New("proposal base revision not found")
		}
		timeline, err := applyVideoProposal(base.Timeline, proposal, input.VideoProject.SelectedOperationIDs)
		if err != nil {
			return preparedV3VideoProjectMutation{}, err
		}
		if err := validateVideoTimeline(timeline); err != nil {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("accepted video timeline validation failed: %w", err)
		}
		revision := *input.VideoProject.Revision
		revision.AccountScopeID = input.AccountScopeID
		revision.UserID = input.UserID
		revision.SessionID = input.SessionID
		revision.ProjectID = proposal.ProjectID
		revision.ParentRevisionID = project.CurrentRevisionID
		revision.AcceptedProposalID = proposal.ID
		revision.Timeline = timeline
		revision.RevisionNumber = project.RevisionCount + 1
		revision.SchemaVersion = VideoProjectRevisionSchemaVersion
		if revision.ID == "" {
			revision.ID = generateDeterministicOrRandomID("vrev")
		}
		revision.CreatedAt = now
		project.CurrentRevisionID = revision.ID
		project.CurrentRevisionNumber = revision.RevisionNumber
		project.ConfirmedRevisionID = revision.ID
		project.ConfirmedRevisionNumber = revision.RevisionNumber
		project.RevisionCount = revision.RevisionNumber
		project.UpdatedAt = now
		proposal.Status = VideoEditProposalStatusAccepted
		proposal.AcceptedOperationIDs = append([]string(nil), input.VideoProject.SelectedOperationIDs...)
		proposal.AcceptedRevisionID = revision.ID
		proposal.UpdatedAt = now
		return preparedV3VideoProjectMutation{Project: &project, Revision: &revision, EditProposal: &proposal, Projection: V3VideoProjectProjection{ProjectID: project.ID, RevisionID: revision.ID, RevisionNumber: revision.RevisionNumber, CurrentRevisionID: revision.ID, ProposalID: proposal.ID, Status: proposal.Status}}, nil

	case V3SessionMutationRejectVideoEditProposal:
		incoming := input.VideoProject.EditProposal
		proposal, ok, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, incoming.ProjectID, incoming.ID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("video edit proposal %q not found", incoming.ID)
			}
			return preparedV3VideoProjectMutation{}, err
		}
		if proposal.UserID != "" && proposal.UserID != input.UserID {
			return preparedV3VideoProjectMutation{}, errors.New("video edit proposal ownership does not match authenticated principal")
		}
		if proposal.Status != VideoEditProposalStatusPending {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("video edit proposal is %s", proposal.Status)
		}
		project, ok, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, proposal.ProjectID)
		if err != nil || !ok {
			return preparedV3VideoProjectMutation{}, errors.New("video project not found")
		}
		if project.CurrentRevisionID != proposal.WorkingRevisionID {
			return preparedV3VideoProjectMutation{}, fmt.Errorf("stale video edit proposal: working revision %s is not current revision %s", proposal.WorkingRevisionID, project.CurrentRevisionID)
		}
		proposal.Status = VideoEditProposalStatusRejected
		proposal.RejectionFeedback = incoming.RejectionFeedback
		proposal.UpdatedAt = now
		project.CurrentRevisionID = proposal.BaseRevisionID
		project.CurrentRevisionNumber = proposal.BaseRevisionNumber
		project.UpdatedAt = now
		return preparedV3VideoProjectMutation{Project: &project, EditProposal: &proposal, Projection: V3VideoProjectProjection{ProjectID: proposal.ProjectID, RevisionID: proposal.BaseRevisionID, RevisionNumber: proposal.BaseRevisionNumber, CurrentRevisionID: proposal.BaseRevisionID, ProposalID: proposal.ID, Status: proposal.Status}}, nil

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
	case VideoRenderJobStatusReady, VideoRenderJobStatusFailed, VideoRenderJobStatusCancelled, VideoRenderJobStatusStale:
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
		if p.ProjectKind == VideoProjectKindVideoTool {
			if err := batch.Set([]byte(KeyPrimaryVideoToolProject(p.AccountScopeID, p.SessionID)), payload, nil); err != nil {
				return err
			}
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
	if prepared.EditProposal != nil {
		p := prepared.EditProposal
		payload, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("marshal video edit proposal snapshot: %w", err)
		}
		if err := batch.Set([]byte(KeyVideoEditProposal(p.AccountScopeID, p.SessionID, p.ProjectID, p.ID)), payload, nil); err != nil {
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
	AccountScopeID    string
	UserID            string
	SessionID         string
	WorkspaceID       string
	ProjectID         string
	InitialRevisionID string
	Title             string
	Description       string
	OutputPreset      string
	InitialTimeline   *VideoProjectTimeline
	Metadata          map[string]any
	ProjectKind       string
	ClientRequestID   string
	SessionMetadata   map[string]any
	AttachmentMessage *MessageSnapshot
	NowUnixMs         int64
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
		ProjectKind:    strings.ToLower(strings.TrimSpace(input.ProjectKind)),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	var revision *VideoProjectRevisionSnapshot
	if input.InitialTimeline != nil {
		timeline := *input.InitialTimeline
		normalizeVideoTimeline(&timeline)
		revisionID := strings.TrimSpace(input.InitialRevisionID)
		if revisionID == "" {
			revisionID = generateDeterministicOrRandomID("vrev")
		}
		rev := VideoProjectRevisionSnapshot{
			SchemaVersion:  VideoProjectRevisionSchemaVersion,
			ID:             revisionID,
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

	mutPayload, _ := json.Marshal(map[string]any{
		"project_id": project.ID, "initial_revision_id": input.InitialRevisionID, "title": project.Title,
		"project_metadata": input.Metadata, "session_metadata": input.SessionMetadata, "attachment_message": input.AttachmentMessage,
	})
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
	if len(input.SessionMetadata) > 0 || input.AttachmentMessage != nil {
		session, ok, err := s.GetSession(input.SessionID)
		if err != nil {
			return VideoProjectSnapshot{}, nil, err
		}
		if !ok {
			return VideoProjectSnapshot{}, nil, errors.New("video project session not found")
		}
		if len(input.SessionMetadata) > 0 {
			metadata := cloneSessionMetadataMap(session.Metadata)
			if metadata == nil {
				metadata = make(map[string]any, len(input.SessionMetadata))
			}
			for key, value := range input.SessionMetadata {
				metadata[key] = cloneSessionMetadataValue(value)
			}
			session.Metadata = metadata
		}
		if input.AttachmentMessage != nil {
			session.MessageCount++
			session.LastMessageAt = now
			message := *input.AttachmentMessage
			message.Metadata = cloneSessionMetadataMap(input.AttachmentMessage.Metadata)
			mutation.Message = &message
		}
		if len(input.SessionMetadata) > 0 || input.AttachmentMessage != nil {
			mutation.Session = &session
		}
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

func (s *SessionStore) GetPrimaryVideoToolProject(accountScopeID, sessionID string) (VideoProjectSnapshot, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	if accountScopeID == "" || sessionID == "" {
		return VideoProjectSnapshot{}, false, errors.New("account scope and session id are required")
	}
	var project VideoProjectSnapshot
	ok, err := s.store.GetJSON(KeyPrimaryVideoToolProject(accountScopeID, sessionID), &project)
	if err != nil || !ok {
		return VideoProjectSnapshot{}, ok, err
	}
	if project.ProjectKind != VideoProjectKindVideoTool {
		return VideoProjectSnapshot{}, false, errors.New("primary Video Tool project index is invalid")
	}
	return project, true, nil
}

// ListVideoProjectsForAccount enumerates retained projects without requiring an
// active source session. Callers must still apply user and workspace scope.
func (s *SessionStore) ListVideoProjectsForAccount(accountScopeID string, limit int) ([]VideoProjectSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	projects := make([]VideoProjectSnapshot, 0)
	err := s.store.IteratePrefix(VideoProjectAccountPrefix(accountScopeID), limit+1, func(_ string, value []byte) error {
		var project VideoProjectSnapshot
		if err := json.Unmarshal(value, &project); err != nil {
			return err
		}
		projects = append(projects, project)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].UpdatedAt == projects[j].UpdatedAt {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].UpdatedAt > projects[j].UpdatedAt
	})
	if len(projects) > limit {
		projects = projects[:limit]
	}
	return projects, nil
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

type CreateVideoEditProposalInput struct {
	AccountScopeID, UserID, SessionID, ProjectID, ProposalID, BaseRevisionID, Title, Rationale, ClientRequestID string
	Plan                                                                                                        *VideoPlanProposal
	Operations                                                                                                  []VideoEditOperation
	AffectedRanges                                                                                              []VideoTimelineRange
	NowUnixMs                                                                                                   int64
}

func (s *SessionStore) CreateVideoEditProposal(input CreateVideoEditProposalInput) (VideoEditProposalSnapshot, error) {
	if strings.TrimSpace(input.ProposalID) == "" {
		input.ProposalID = generateDeterministicOrRandomID("vprop")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	proposal := VideoEditProposalSnapshot{ID: input.ProposalID, ProjectID: input.ProjectID, BaseRevisionID: input.BaseRevisionID, AccountScopeID: input.AccountScopeID, UserID: input.UserID, SessionID: input.SessionID, Status: VideoEditProposalStatusPending, Title: input.Title, Rationale: input.Rationale, Plan: input.Plan, Operations: input.Operations, AffectedRanges: input.AffectedRanges, CreatedAt: now, UpdatedAt: now}
	clientID := input.ClientRequestID
	if clientID == "" {
		clientID = "create_video_edit_proposal:" + proposal.ID
	}
	payload, _ := json.Marshal(proposal)
	sum := sha256.Sum256(payload)
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, ClientRequestID: clientID, IdempotencyKey: clientID, PayloadHash: hex.EncodeToString(sum[:]), Kind: V3SessionMutationCreateVideoEditProposal, VideoProject: &V3VideoProjectMutation{EditProposal: &proposal}, NowUnixMs: now})
	if err != nil {
		return VideoEditProposalSnapshot{}, err
	}
	stored, ok, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, input.ProjectID, proposal.ID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("created video edit proposal could not be read")
		}
		return VideoEditProposalSnapshot{}, err
	}
	return stored, nil
}

func (s *SessionStore) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (VideoEditProposalSnapshot, bool, error) {
	if strings.TrimSpace(accountScopeID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(proposalID) == "" {
		return VideoEditProposalSnapshot{}, false, errors.New("account scope, session id, project id, and proposal id are required")
	}
	var proposal VideoEditProposalSnapshot
	ok, err := s.store.GetJSON(KeyVideoEditProposal(accountScopeID, sessionID, projectID, proposalID), &proposal)
	return proposal, ok, err
}

func (s *SessionStore) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]VideoEditProposalSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || sessionID == "" || projectID == "" {
		return nil, errors.New("account scope, session id, and project id are required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var proposals []VideoEditProposalSnapshot
	err := s.store.IteratePrefix(VideoEditProposalPrefix(accountScopeID, sessionID, projectID), limit+1, func(_ string, value []byte) error {
		var proposal VideoEditProposalSnapshot
		if err := json.Unmarshal(value, &proposal); err != nil {
			return err
		}
		proposals = append(proposals, proposal)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].CreatedAt < proposals[j].CreatedAt })
	if len(proposals) > limit {
		proposals = proposals[:limit]
	}
	return proposals, nil
}

type ResolveVideoEditProposalInput struct {
	AccountScopeID, UserID, SessionID, ProjectID, ProposalID, RevisionID, Description, ChangeSummary, AuthorPrincipal, ClientRequestID string
	SelectedOperationIDs                                                                                                               []string
	Reject                                                                                                                             bool
	RejectionFeedback                                                                                                                  string
	NowUnixMs                                                                                                                          int64
}

func (s *SessionStore) ResolveVideoEditProposal(input ResolveVideoEditProposalInput) (VideoEditProposalSnapshot, *VideoProjectRevisionSnapshot, *VideoProjectSnapshot, error) {
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	proposal, ok, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, input.ProjectID, input.ProposalID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("video edit proposal not found")
		}
		return VideoEditProposalSnapshot{}, nil, nil, err
	}
	kind := V3SessionMutationAcceptVideoEditProposal
	proposal.Status = VideoEditProposalStatusAccepted
	var revision *VideoProjectRevisionSnapshot
	if input.Reject {
		kind = V3SessionMutationRejectVideoEditProposal
		proposal.Status = VideoEditProposalStatusRejected
		proposal.RejectionFeedback = strings.TrimSpace(input.RejectionFeedback)
	} else {
		revision = &VideoProjectRevisionSnapshot{ID: input.RevisionID, ProjectID: input.ProjectID, AccountScopeID: input.AccountScopeID, UserID: input.UserID, SessionID: input.SessionID, Description: input.Description, ChangeSummary: input.ChangeSummary, AuthorPrincipal: input.AuthorPrincipal}
	}
	clientID := input.ClientRequestID
	if clientID == "" {
		clientID = fmt.Sprintf("resolve_video_edit_proposal:%s:%s", proposal.ID, kind)
	}
	payload, _ := json.Marshal(map[string]any{"proposal_id": proposal.ID, "kind": kind, "selected": input.SelectedOperationIDs, "feedback": strings.TrimSpace(input.RejectionFeedback)})
	sum := sha256.Sum256(payload)
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, ClientRequestID: clientID, IdempotencyKey: clientID, PayloadHash: hex.EncodeToString(sum[:]), Kind: kind, VideoProject: &V3VideoProjectMutation{EditProposal: &proposal, Revision: revision, SelectedOperationIDs: input.SelectedOperationIDs}, NowUnixMs: now})
	if err != nil {
		return VideoEditProposalSnapshot{}, nil, nil, err
	}
	stored, _, err := s.GetVideoEditProposal(input.AccountScopeID, input.SessionID, input.ProjectID, input.ProposalID)
	if err != nil {
		return VideoEditProposalSnapshot{}, nil, nil, err
	}
	if input.Reject {
		return stored, nil, nil, nil
	}
	storedRevision, _, err := s.GetVideoProjectRevision(input.AccountScopeID, input.SessionID, input.ProjectID, stored.AcceptedRevisionID)
	if err != nil {
		return VideoEditProposalSnapshot{}, nil, nil, err
	}
	project, _, err := s.GetVideoProject(input.AccountScopeID, input.SessionID, input.ProjectID)
	if err != nil {
		return VideoEditProposalSnapshot{}, nil, nil, err
	}
	return stored, &storedRevision, &project, nil
}

type CreateVideoProjectRevisionInput struct {
	AccountScopeID         string
	UserID                 string
	SessionID              string
	ProjectID              string
	RevisionID             string
	Description            string
	ChangeSummary          string
	Timeline               VideoProjectTimeline
	AuthorPrincipal        string
	RestoredFromRevisionID string
	AcceptedProposalID     string
	ClientRequestID        string
	NowUnixMs              int64
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
		SchemaVersion:          VideoProjectRevisionSchemaVersion,
		ID:                     input.RevisionID,
		ProjectID:              input.ProjectID,
		AccountScopeID:         input.AccountScopeID,
		UserID:                 input.UserID,
		SessionID:              input.SessionID,
		Description:            input.Description,
		ChangeSummary:          input.ChangeSummary,
		Timeline:               input.Timeline,
		AuthorPrincipal:        input.AuthorPrincipal,
		RestoredFromRevisionID: strings.TrimSpace(input.RestoredFromRevisionID),
		AcceptedProposalID:     strings.TrimSpace(input.AcceptedProposalID),
		CreatedAt:              now,
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
	prefix := VideoProjectRevisionByNumberPrefix(accountScopeID, sessionID, projectID)
	revisions := make([]VideoProjectRevisionSnapshot, 0, limit)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
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

func (s *SessionStore) ListRecoverableVideoRenderJobs(limit int) ([]VideoRenderJobSnapshot, error) {
	if limit <= 0 || limit > MaxRecoverableVideoRenderJobs {
		limit = MaxRecoverableVideoRenderJobs
	}
	jobs := make([]VideoRenderJobSnapshot, 0, limit)
	err := s.store.IteratePrefix("v3/video_project/render_job/", 0, func(_ string, value []byte) error {
		var job VideoRenderJobSnapshot
		if err := json.Unmarshal(value, &job); err != nil {
			return err
		}
		if job.Status == VideoRenderJobStatusQueued || job.Status == VideoRenderJobStatusRendering {
			jobs = append(jobs, job)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt == jobs[j].CreatedAt {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt < jobs[j].CreatedAt
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
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
