package videoproject

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SessionStore interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	CreateVideoProject(input pebblestore.CreateVideoProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error)
	GetVideoProject(accountScopeID, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error)
	GetPrimaryVideoToolProject(accountScopeID, sessionID string) (pebblestore.VideoProjectSnapshot, bool, error)
	ListVideoProjects(accountScopeID, sessionID string, limit int) ([]pebblestore.VideoProjectSnapshot, error)
	ListVideoProjectsForAccount(accountScopeID string, limit int) ([]pebblestore.VideoProjectSnapshot, error)
	CreateVideoProjectRevision(input pebblestore.CreateVideoProjectRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error)
	GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error)
	GetVideoProjectRevisionByNumber(accountScopeID, sessionID, projectID string, revisionNumber int) (pebblestore.VideoProjectRevisionSnapshot, bool, error)
	ListVideoProjectRevisions(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoProjectRevisionSnapshot, error)
	CreateVideoEditProposal(input pebblestore.CreateVideoEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error)
	GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error)
	ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error)
	ResolveVideoEditProposal(input pebblestore.ResolveVideoEditProposalInput) (pebblestore.VideoEditProposalSnapshot, *pebblestore.VideoProjectRevisionSnapshot, *pebblestore.VideoProjectSnapshot, error)
	CreateVideoRenderJob(input pebblestore.CreateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error)
	GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
	UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error)
	ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
	ListRecoverableVideoRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
	GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.AudioSourceRecord, bool, error)
}

type Service struct {
	sessions SessionStore
}

func NewService(sessions SessionStore) *Service {
	return &Service{sessions: sessions}
}

type CreateProjectInput struct {
	SessionID       string
	WorkspaceID     string
	ProjectID       string
	Title           string
	Description     string
	OutputPreset    string
	InitialTimeline *pebblestore.VideoProjectTimeline
	Metadata        map[string]any
	ProjectKind     string
	NowUnixMs       int64
}

type CreateRevisionInput struct {
	SessionID       string
	ProjectID       string
	RevisionID      string
	Description     string
	ChangeSummary   string
	Timeline        pebblestore.VideoProjectTimeline
	AuthorPrincipal string
	NowUnixMs       int64
}

type ForkRevisionInput struct {
	SourceSessionID        string
	SourceProjectID        string
	SourceRevisionID       string
	DestinationSessionID   string
	DestinationWorkspaceID string
	ProjectID              string
	InitialRevisionID      string
	SessionMetadata        map[string]any
	AttachmentMessage      *pebblestore.MessageSnapshot
	NowUnixMs              int64
}

type RestoreRevisionInput struct {
	SessionID        string
	ProjectID        string
	SourceRevisionID string
	RevisionID       string
	Description      string
	ChangeSummary    string
	AuthorPrincipal  string
	NowUnixMs        int64
}

type CreateEditProposalInput struct {
	SessionID, ProjectID, ProposalID, BaseRevisionID, Title, Rationale, Intent string
	Plan                                                                       *pebblestore.VideoPlanProposal
	Operations                                                                 []pebblestore.VideoEditOperation
	AffectedRanges                                                             []pebblestore.VideoTimelineRange
	NowUnixMs                                                                  int64
}
type SelectAnimationCandidateInput struct {
	SessionID, ProjectID, ProposalID, PartID, CandidateID string
	SelectedSource                                        *pebblestore.SessionArtifactSelectionReference
	NowUnixMs                                             int64
}

type PromoteAnimationDerivativeInput struct {
	SessionID, ProjectID, ProposalID, PartID, CandidateID string
	SelectedSource, Derivative                            *pebblestore.SessionArtifactSelectionReference
	NowUnixMs                                             int64
}

type AcceptEditProposalInput struct {
	SessionID, ProjectID, ProposalID, RevisionID, Description, ChangeSummary, AuthorPrincipal string
	SelectedOperationIDs                                                                      []string
	NowUnixMs                                                                                 int64
}

func (s *Service) CreateEditProposal(ctx context.Context, principal identity.Principal, input CreateEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("authenticated principal is required")
	}
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, input.SessionID, input.ProjectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("video project not found")
	}
	if input.Plan != nil {
		if err := s.normalizeVisualPlanArtifacts(principal, input.SessionID, input.Plan); err != nil {
			return pebblestore.VideoEditProposalSnapshot{}, err
		}
	}
	for _, operation := range input.Operations {
		if operation.Clip == nil {
			continue
		}
		if err := s.validateTimelineSources(principal, input.SessionID, pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{*operation.Clip}}); err != nil {
			return pebblestore.VideoEditProposalSnapshot{}, err
		}
	}
	return s.sessions.CreateVideoEditProposal(pebblestore.CreateVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: input.SessionID, ProjectID: input.ProjectID, ProposalID: input.ProposalID, BaseRevisionID: input.BaseRevisionID, Title: input.Title, Rationale: input.Rationale, Intent: input.Intent, Plan: input.Plan, Operations: input.Operations, AffectedRanges: input.AffectedRanges, NowUnixMs: input.NowUnixMs})
}
func (s *Service) SelectAnimationCandidate(ctx context.Context, principal identity.Principal, input SelectAnimationCandidateInput) (pebblestore.VideoEditProposalSnapshot, error) {
	return s.mutateAnimationCandidate(principal, input.SessionID, input.ProjectID, input.ProposalID, pebblestore.V3SessionMutationSelectVideoAnimationCandidate, pebblestore.VideoAnimationSelectionMutation{PartID: input.PartID, SelectedCandidateID: input.CandidateID, SelectedSource: input.SelectedSource}, input.NowUnixMs)
}

func (s *Service) PromoteAnimationDerivative(ctx context.Context, principal identity.Principal, input PromoteAnimationDerivativeInput) (pebblestore.VideoEditProposalSnapshot, error) {
	if input.SelectedSource == nil || input.Derivative == nil {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("promotion requires exact selected HTML and MP4 derivative references")
	}
	if err := s.validateAnimationArtifact(principal, input.SelectedSource, "text/html"); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	if err := s.validateAnimationDerivative(principal, input.SelectedSource, input.Derivative); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	return s.mutateAnimationCandidate(principal, input.SessionID, input.ProjectID, input.ProposalID, pebblestore.V3SessionMutationPromoteVideoAnimationDerivative, pebblestore.VideoAnimationSelectionMutation{PartID: input.PartID, SelectedCandidateID: input.CandidateID, SelectedSource: input.SelectedSource, Derivative: input.Derivative}, input.NowUnixMs)
}

func (s *Service) mutateAnimationCandidate(principal identity.Principal, sessionID, projectID, proposalID, kind string, selection pebblestore.VideoAnimationSelectionMutation, now int64) (pebblestore.VideoEditProposalSnapshot, error) {
	if !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("authenticated principal is required")
	}
	proposal, ok, err := s.sessions.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
	if err != nil || !ok {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("video edit proposal not found")
	}
	if err := s.validateAnimationArtifact(principal, selection.SelectedSource, "text/html"); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	clientID := fmt.Sprintf("%s:%s:%s:%s:%d", kind, proposalID, selection.PartID, selection.SelectedCandidateID, now)
	payload, _ := json.Marshal(selection)
	sum := sha256.Sum256(payload)
	applier, ok := s.sessions.(interface {
		ApplyV3SessionMutation(pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error)
	})
	if !ok {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("video session mutation authority is not configured")
	}
	_, err = applier.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientID, IdempotencyKey: clientID, PayloadHash: hex.EncodeToString(sum[:]), Kind: kind, VideoProject: &pebblestore.V3VideoProjectMutation{EditProposal: &proposal, AnimationSelection: &selection}, NowUnixMs: now})
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	updated, ok, err := s.sessions.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
	if err != nil || !ok {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("updated video edit proposal not found")
	}
	return updated, nil
}

func (s *Service) validateAnimationArtifact(principal identity.Principal, ref *pebblestore.SessionArtifactSelectionReference, expected string) error {
	_, err := s.animationArtifactVariant(principal, ref, expected)
	return err
}

func (s *Service) animationArtifactVariant(principal identity.Principal, ref *pebblestore.SessionArtifactSelectionReference, expected string) (pebblestore.SessionArtifactVariant, error) {
	if ref == nil || ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("complete exact animation artifact reference is required")
	}
	variant, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
	if err != nil || !ok || variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq != ref.EventSeq {
		return pebblestore.SessionArtifactVariant{}, errors.New("animation artifact is stale, missing, or not ready")
	}
	if strings.ToLower(strings.TrimSpace(variant.MediaType)) != expected {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("animation artifact must be %s", expected)
	}
	return variant, nil
}

func temporalAnimationDuration(parts []pebblestore.SessionArtifactPart) int64 {
	var duration int64
	for _, part := range parts {
		if part.Kind == "temporal" && part.EndMs > duration {
			duration = part.EndMs
		}
	}
	return duration
}

func (s *Service) validateAnimationDerivative(principal identity.Principal, source, derivative *pebblestore.SessionArtifactSelectionReference) error {
	if err := s.validateAnimationArtifact(principal, derivative, "video/mp4"); err != nil {
		return err
	}
	variant, _, _ := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, derivative.SessionID, derivative.CollectionID, derivative.VariantID)
	lineage := variant.Lineage
	if lineage.SourceSessionID != source.SessionID || lineage.SourceCollectionID != source.CollectionID || lineage.SourceVariantID != source.VariantID || lineage.SourceEventSeq != source.EventSeq {
		return errors.New("MP4 derivative lineage does not exactly match the selected HTML animation")
	}
	return nil
}

func (s *Service) GetEditProposal(principal identity.Principal, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	if !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, false, errors.New("authenticated principal is required")
	}
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) {
		return pebblestore.VideoEditProposalSnapshot{}, false, nil
	}
	return s.sessions.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
}
func (s *Service) ListEditProposals(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	if !principal.Valid() {
		return nil, errors.New("authenticated principal is required")
	}
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) {
		return nil, errors.New("video project not found")
	}
	return s.sessions.ListVideoEditProposals(principal.AccountScopeID, sessionID, projectID, limit)
}
func (s *Service) AcceptEditProposal(ctx context.Context, principal identity.Principal, input AcceptEditProposalInput) (pebblestore.VideoEditProposalSnapshot, pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("authenticated principal is required")
	}
	proposalSnapshot, ok, err := s.sessions.GetVideoEditProposal(principal.AccountScopeID, input.SessionID, input.ProjectID, input.ProposalID)
	if err != nil || !ok {
		return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("video edit proposal not found")
	}
	selected := make(map[string]struct{}, len(input.SelectedOperationIDs))
	for _, id := range input.SelectedOperationIDs {
		selected[strings.TrimSpace(id)] = struct{}{}
	}
	for _, operation := range proposalSnapshot.Operations {
		if _, ok := selected[operation.ID]; !ok || operation.Clip == nil {
			continue
		}
		if err := s.validateTimelineSources(principal, input.SessionID, pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{*operation.Clip}}); err != nil {
			return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
		}
	}
	proposal, revision, project, err := s.sessions.ResolveVideoEditProposal(pebblestore.ResolveVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: input.SessionID, ProjectID: input.ProjectID, ProposalID: input.ProposalID, RevisionID: input.RevisionID, Description: input.Description, ChangeSummary: input.ChangeSummary, AuthorPrincipal: input.AuthorPrincipal, SelectedOperationIDs: input.SelectedOperationIDs, NowUnixMs: input.NowUnixMs})
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
	}
	return proposal, *revision, *project, nil
}
func (s *Service) RejectEditProposal(ctx context.Context, principal identity.Principal, sessionID, projectID, proposalID, feedback string, now int64) (pebblestore.VideoEditProposalSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("authenticated principal is required")
	}
	proposal, _, _, err := s.sessions.ResolveVideoEditProposal(pebblestore.ResolveVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, ProjectID: projectID, ProposalID: proposalID, Reject: true, RejectionFeedback: feedback, NowUnixMs: now})
	return proposal, err
}

type StartRenderJobInput struct {
	SessionID  string
	ProjectID  string
	RevisionID string
	JobID      string
	NowUnixMs  int64
}

type CompleteRenderJobInput struct {
	SessionID          string
	JobID              string
	OutputPreset       string
	OutputWidth        int
	OutputHeight       int
	OutputFPS          float64
	OutputDurationMs   int64
	OutputSizeBytes    int64
	OutputDigestSHA256 string
	OutputArtifact     *pebblestore.SessionArtifactSelectionReference
	NowUnixMs          int64
}

func (s *Service) CreateProject(ctx context.Context, principal identity.Principal, input CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("authenticated principal is required")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.SessionID == "" {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("session id is required")
	}

	session, ok, err := s.sessions.GetSession(input.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return pebblestore.VideoProjectSnapshot{}, nil, err
	}
	if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("session ownership does not match authenticated principal")
	}

	ensureInitialVideoProjectTimeline(&input)
	if err := s.validateTimelineSources(principal, input.SessionID, *input.InitialTimeline); err != nil {
		return pebblestore.VideoProjectSnapshot{}, nil, err
	}

	return s.sessions.CreateVideoProject(pebblestore.CreateVideoProjectInput{
		AccountScopeID:  principal.AccountScopeID,
		UserID:          principal.UserID,
		SessionID:       input.SessionID,
		WorkspaceID:     input.WorkspaceID,
		ProjectID:       input.ProjectID,
		Title:           input.Title,
		Description:     input.Description,
		OutputPreset:    input.OutputPreset,
		InitialTimeline: input.InitialTimeline,
		Metadata:        input.Metadata,
		ProjectKind:     input.ProjectKind,
		NowUnixMs:       input.NowUnixMs,
	})
}

func (s *Service) GetOrCreatePrimaryVideoToolProject(ctx context.Context, principal identity.Principal, input CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("authenticated principal is required")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.SessionID == "" {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("session id is required")
	}
	ensureInitialVideoProjectTimeline(&input)
	if project, ok, err := s.sessions.GetPrimaryVideoToolProject(principal.AccountScopeID, input.SessionID); err != nil {
		return pebblestore.VideoProjectSnapshot{}, nil, err
	} else if ok {
		if project.UserID != "" && project.UserID != principal.UserID {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("video project ownership does not match authenticated principal")
		}
		if project.CurrentRevisionID == "" {
			revision, updated, createErr := s.CreateRevision(ctx, principal, CreateRevisionInput{
				SessionID:       input.SessionID,
				ProjectID:       project.ID,
				Description:     "Initial revision",
				ChangeSummary:   "Initialized video timeline",
				Timeline:        *input.InitialTimeline,
				AuthorPrincipal: principal.UserID,
				NowUnixMs:       input.NowUnixMs,
			})
			if createErr != nil {
				return pebblestore.VideoProjectSnapshot{}, nil, createErr
			}
			return updated, &revision, nil
		}
		revision, revisionOK, revisionErr := s.sessions.GetVideoProjectRevision(principal.AccountScopeID, input.SessionID, project.ID, project.CurrentRevisionID)
		if revisionErr != nil {
			return pebblestore.VideoProjectSnapshot{}, nil, revisionErr
		}
		if !revisionOK {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("current video project revision not found")
		}
		return project, &revision, nil
	}
	input.ProjectKind = pebblestore.VideoProjectKindVideoTool
	return s.CreateProject(ctx, principal, input)
}

func ensureInitialVideoProjectTimeline(input *CreateProjectInput) {
	if input == nil || input.InitialTimeline != nil {
		return
	}
	input.InitialTimeline = &pebblestore.VideoProjectTimeline{
		OutputPreset: strings.TrimSpace(input.OutputPreset),
		Clips:        []pebblestore.VideoTimelineClip{},
		Transitions:  []pebblestore.VideoTimelineTransition{},
	}
}

func (s *Service) ForkRevision(ctx context.Context, principal identity.Principal, input ForkRevisionInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	_ = ctx
	if !principal.Valid() {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("authenticated principal is required")
	}
	sourceProject, ok, err := s.GetProject(principal, input.SourceSessionID, input.SourceProjectID)
	if err != nil || !ok {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("source video project not found")
	}
	sourceRevision, ok, err := s.GetRevision(principal, input.SourceSessionID, input.SourceProjectID, input.SourceRevisionID)
	if err != nil || !ok {
		return pebblestore.VideoProjectSnapshot{}, nil, errors.New("source video revision not found")
	}
	metadata := map[string]any{
		"source_session_id":             input.SourceSessionID,
		"source_project_id":             input.SourceProjectID,
		"source_revision_id":            input.SourceRevisionID,
		"video_lineage_root_session_id": firstNonEmptyMetadataString(sourceProject.Metadata, "video_lineage_root_session_id", input.SourceSessionID),
		"video_lineage_root_project_id": firstNonEmptyMetadataString(sourceProject.Metadata, "video_lineage_root_project_id", input.SourceProjectID),
	}
	project, revision, err := s.sessions.CreateVideoProject(pebblestore.CreateVideoProjectInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		SessionID: input.DestinationSessionID, WorkspaceID: input.DestinationWorkspaceID, ProjectID: input.ProjectID, InitialRevisionID: input.InitialRevisionID,
		Title: sourceProject.Title, Description: sourceProject.Description, OutputPreset: sourceProject.OutputPreset,
		InitialTimeline: &sourceRevision.Timeline, Metadata: metadata, ProjectKind: pebblestore.VideoProjectKindVideoTool,
		SessionMetadata: input.SessionMetadata, AttachmentMessage: input.AttachmentMessage, NowUnixMs: input.NowUnixMs,
	})
	if err != nil {
		return pebblestore.VideoProjectSnapshot{}, nil, err
	}
	return project, revision, nil
}

func firstNonEmptyMetadataString(metadata map[string]any, key, fallback string) string {
	if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func sameWorkspacePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (s *Service) CreateRevision(ctx context.Context, principal identity.Principal, input CreateRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("authenticated principal is required")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	if input.SessionID == "" || input.ProjectID == "" {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("session id and project id are required")
	}

	if err := s.validateTimelineSources(principal, input.SessionID, input.Timeline); err != nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
	}

	return s.sessions.CreateVideoProjectRevision(pebblestore.CreateVideoProjectRevisionInput{
		AccountScopeID:  principal.AccountScopeID,
		UserID:          principal.UserID,
		SessionID:       input.SessionID,
		ProjectID:       input.ProjectID,
		RevisionID:      input.RevisionID,
		Description:     input.Description,
		ChangeSummary:   input.ChangeSummary,
		Timeline:        input.Timeline,
		AuthorPrincipal: input.AuthorPrincipal,
		NowUnixMs:       input.NowUnixMs,
	})
}

func (s *Service) RestoreRevision(ctx context.Context, principal identity.Principal, input RestoreRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("authenticated principal is required")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.SourceRevisionID = strings.TrimSpace(input.SourceRevisionID)
	if input.SessionID == "" || input.ProjectID == "" || input.SourceRevisionID == "" {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("session id, project id, and source revision id are required")
	}
	source, ok, err := s.sessions.GetVideoProjectRevision(principal.AccountScopeID, input.SessionID, input.ProjectID, input.SourceRevisionID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("video project revision %q not found", input.SourceRevisionID)
		}
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
	}
	if err := s.validateTimelineSources(principal, input.SessionID, source.Timeline); err != nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
	}
	return s.sessions.CreateVideoProjectRevision(pebblestore.CreateVideoProjectRevisionInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		SessionID: input.SessionID, ProjectID: input.ProjectID, RevisionID: input.RevisionID,
		Description: input.Description, ChangeSummary: input.ChangeSummary, Timeline: source.Timeline,
		AuthorPrincipal: input.AuthorPrincipal, RestoredFromRevisionID: source.ID, NowUnixMs: input.NowUnixMs,
	})
}

func (s *Service) videoPlanRenderAuthority(principal identity.Principal, revision pebblestore.VideoProjectRevisionSnapshot) (*pebblestore.VideoPlanProposal, error) {
	proposalID := pebblestore.VideoPlanRenderAuthorityProposalID(revision.Timeline)
	if proposalID == "" {
		return pebblestore.ResolveVideoPlanRenderAuthority(revision, nil)
	}
	proposal, ok, err := s.sessions.GetVideoEditProposal(principal.AccountScopeID, revision.SessionID, revision.ProjectID, proposalID)
	if err != nil {
		return nil, fmt.Errorf("resolve video plan render authority: %w", err)
	}
	if !ok || (proposal.UserID != "" && proposal.UserID != principal.UserID) {
		return pebblestore.ResolveVideoPlanRenderAuthority(revision, nil)
	}
	return pebblestore.ResolveVideoPlanRenderAuthority(revision, &proposal)
}

func (s *Service) StartRenderJob(ctx context.Context, principal identity.Principal, input StartRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	if input.SessionID == "" || input.ProjectID == "" {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("session id and project id are required")
	}

	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, input.SessionID, input.ProjectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) {
		if err == nil {
			err = fmt.Errorf("video project %q not found", input.ProjectID)
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}

	revID := strings.TrimSpace(input.RevisionID)
	if revID == "" {
		revID = project.CurrentRevisionID
	}
	if revID == "" {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("video project has no revision to render")
	}
	revision, ok, err := s.sessions.GetVideoProjectRevision(principal.AccountScopeID, input.SessionID, input.ProjectID, revID)
	if err != nil || !ok {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("video project revision not found")
	}
	if revision.Timeline.Metadata["accepted_video_plan"] != nil {
		if plan, err := s.videoPlanRenderAuthority(principal, revision); err != nil {
			return pebblestore.VideoRenderJobSnapshot{}, err
		} else if plan != nil {
			pendingParts := make([]string, 0)
			for _, part := range plan.Parts {
				if part.ProductionState == pebblestore.VideoProductionStatePending {
					pendingParts = append(pendingParts, part.ID)
				}
				candidates := part.AnimationCandidates
				if candidates == nil {
					continue
				}
				if len(candidates.Candidates) > 1 && (candidates.SelectedCandidateID == "" || candidates.SelectedSource == nil) {
					return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("HTML animation part %q must have one durably locked variant before rendering", part.ID)
				}
				if len(candidates.Candidates) == 1 && candidates.Candidates[0].Source == nil {
					return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("HTML animation part %q has no exact source to render", part.ID)
				}
				if candidates.Status == pebblestore.VideoAnimationCandidateStatusFailed {
					reason := strings.TrimSpace(candidates.FailureReason)
					if reason == "" {
						reason = "the selected HTML animation export failed"
					}
					return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("HTML animation part %q is not renderable: %s", part.ID, reason)
				}
			}
			if len(pendingParts) > 0 {
				return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("final render blocked: %d storyboard part(s) remain pending (%s); replace each pending part with finished still, MP4, or promoted HTML animation media", len(pendingParts), strings.Join(pendingParts, ", "))
			}
		}
		for _, clip := range revision.Timeline.Clips {
			if clip.SourceKind == pebblestore.VideoClipSourceKindText || (clip.SourceKind == pebblestore.VideoClipSourceKindManagedArtifact && clip.ArtifactRef == nil) {
				return pebblestore.VideoRenderJobSnapshot{}, errors.New("accepted video plan still contains unresolved sections; replace them with renderable sources before rendering")
			}
		}
	}

	return s.sessions.CreateVideoRenderJob(pebblestore.CreateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      input.SessionID,
		ProjectID:      input.ProjectID,
		RevisionID:     revID,
		JobID:          input.JobID,
		NowUnixMs:      input.NowUnixMs,
	})
}

func (s *Service) UpdateRenderProgress(ctx context.Context, principal identity.Principal, sessionID, jobID string, progress float64, nowUnixMs int64) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}
	if progress < 0 {
		progress = 0
	} else if progress > 1.0 {
		progress = 1.0
	}
	return s.sessions.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		JobID:          jobID,
		Status:         pebblestore.VideoRenderJobStatusRendering,
		Progress:       progress,
		NowUnixMs:      nowUnixMs,
	})
}

func (s *Service) FailRenderJob(ctx context.Context, principal identity.Principal, sessionID, jobID, code, reason string, nowUnixMs int64) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}
	return s.sessions.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		JobID:          jobID,
		Status:         pebblestore.VideoRenderJobStatusFailed,
		FailureCode:    code,
		FailureReason:  reason,
		NowUnixMs:      nowUnixMs,
	})
}

func (s *Service) CompleteRenderJob(ctx context.Context, principal identity.Principal, input CompleteRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}
	return s.sessions.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID:     principal.AccountScopeID,
		UserID:             principal.UserID,
		SessionID:          input.SessionID,
		JobID:              input.JobID,
		Status:             pebblestore.VideoRenderJobStatusReady,
		Progress:           1.0,
		OutputPreset:       input.OutputPreset,
		OutputWidth:        input.OutputWidth,
		OutputHeight:       input.OutputHeight,
		OutputFPS:          input.OutputFPS,
		OutputDurationMs:   input.OutputDurationMs,
		OutputSizeBytes:    input.OutputSizeBytes,
		OutputDigestSHA256: input.OutputDigestSHA256,
		OutputArtifact:     input.OutputArtifact,
		NowUnixMs:          input.NowUnixMs,
	})
}

func (s *Service) GetProject(principal identity.Principal, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectSnapshot{}, false, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoProjectSnapshot{}, false, errors.New("authenticated principal is required")
	}
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) {
		return pebblestore.VideoProjectSnapshot{}, false, err
	}
	return project, true, nil
}

func (s *Service) GetRevision(principal identity.Principal, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, errors.New("videoproject service is not configured")
	}
	if _, ok, err := s.GetProject(principal, sessionID, projectID); err != nil || !ok {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, err
	}
	revision, ok, err := s.sessions.GetVideoProjectRevision(principal.AccountScopeID, sessionID, projectID, revisionID)
	if err != nil || !ok || (revision.UserID != "" && revision.UserID != principal.UserID) {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, err
	}
	return revision, true, nil
}

type WorkspaceVideoRelatedSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Archived  bool   `json:"archived,omitempty"`
}

type WorkspaceVideoCatalogItem struct {
	Project            pebblestore.VideoProjectSnapshot           `json:"project"`
	Revisions          []pebblestore.VideoProjectRevisionSnapshot `json:"revisions"`
	SourceArchived     bool                                       `json:"source_archived,omitempty"`
	SourceSessionID    string                                     `json:"source_session_id"`
	SourceSessionTitle string                                     `json:"source_session_title,omitempty"`
	RelatedSessions    []WorkspaceVideoRelatedSession             `json:"related_sessions"`
}

func (s *Service) ListWorkspaceCatalog(principal identity.Principal, workspacePath string, limit int) ([]WorkspaceVideoCatalogItem, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return nil, errors.New("authenticated principal is required")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	projects, err := s.sessions.ListVideoProjectsForAccount(principal.AccountScopeID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]WorkspaceVideoCatalogItem, 0, len(projects))
	for _, project := range projects {
		if project.UserID != "" && project.UserID != principal.UserID {
			continue
		}
		session, active, readErr := s.sessions.GetSession(project.SessionID)
		archived := false
		if readErr != nil {
			return nil, readErr
		}
		if !active {
			tombstoneReader, ok := s.sessions.(interface {
				GetV3SessionTombstone(string) (pebblestore.V3SessionTombstone, bool, error)
			})
			if !ok {
				continue
			}
			tombstone, found, tombstoneErr := tombstoneReader.GetV3SessionTombstone(project.SessionID)
			if tombstoneErr != nil {
				return nil, tombstoneErr
			}
			if !found || tombstone.Deleted || !tombstone.Archived {
				continue
			}
			session, archived = tombstone.Session, true
		}
		if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) || !sameWorkspacePath(session.WorkspacePath, workspacePath) {
			continue
		}
		revisions, revisionErr := s.sessions.ListVideoProjectRevisions(principal.AccountScopeID, project.SessionID, project.ID, pebblestore.MaxVideoProjectRevisions)
		if revisionErr != nil {
			return nil, revisionErr
		}
		items = append(items, WorkspaceVideoCatalogItem{Project: project, Revisions: revisions, SourceArchived: archived, SourceSessionID: project.SessionID, SourceSessionTitle: session.Title})
	}
	for index := range items {
		rootSession := firstNonEmptyMetadataString(items[index].Project.Metadata, "video_lineage_root_session_id", items[index].SourceSessionID)
		rootProject := firstNonEmptyMetadataString(items[index].Project.Metadata, "video_lineage_root_project_id", items[index].Project.ID)
		seen := map[string]bool{}
		for _, candidate := range items {
			candidateRootSession := firstNonEmptyMetadataString(candidate.Project.Metadata, "video_lineage_root_session_id", candidate.SourceSessionID)
			candidateRootProject := firstNonEmptyMetadataString(candidate.Project.Metadata, "video_lineage_root_project_id", candidate.Project.ID)
			if candidateRootSession != rootSession || candidateRootProject != rootProject || seen[candidate.SourceSessionID] {
				continue
			}
			seen[candidate.SourceSessionID] = true
			items[index].RelatedSessions = append(items[index].RelatedSessions, WorkspaceVideoRelatedSession{SessionID: candidate.SourceSessionID, Title: candidate.SourceSessionTitle, Archived: candidate.SourceArchived})
		}
	}
	return items, nil
}

func (s *Service) ListProjects(principal identity.Principal, sessionID string, limit int) ([]pebblestore.VideoProjectSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return nil, errors.New("authenticated principal is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return nil, err
	}
	if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return nil, errors.New("session ownership does not match authenticated principal")
	}
	return s.sessions.ListVideoProjects(principal.AccountScopeID, sessionID, limit)
}

func (s *Service) ListRevisions(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoProjectRevisionSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("videoproject service is not configured")
	}
	if _, ok, err := s.GetProject(principal, sessionID, projectID); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("video project not found")
	}
	return s.sessions.ListVideoProjectRevisions(principal.AccountScopeID, sessionID, projectID, limit)
}

func (s *Service) GetRenderJob(principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, false, errors.New("videoproject service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, false, errors.New("authenticated principal is required")
	}
	job, ok, err := s.sessions.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if err != nil || !ok || (job.UserID != "" && job.UserID != principal.UserID) {
		return pebblestore.VideoRenderJobSnapshot{}, false, err
	}
	return job, true, nil
}

func (s *Service) ListRecoverableRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("videoproject service is not configured")
	}
	return s.sessions.ListRecoverableVideoRenderJobs(limit)
}

func (s *Service) TransitionRecoverableRenderJob(job pebblestore.VideoRenderJobSnapshot, expectedStatus, nextStatus, code, reason string, nowUnixMs int64) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videoproject service is not configured")
	}
	if job.AccountScopeID == "" || job.SessionID == "" || job.ID == "" || job.UserID == "" {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("recoverable render job ownership is incomplete")
	}
	return s.sessions.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: job.AccountScopeID, UserID: job.UserID, SessionID: job.SessionID, JobID: job.ID,
		Status: nextStatus, ExpectedStatus: expectedStatus, Progress: job.Progress,
		FailureCode: code, FailureReason: reason,
		ClientRequestID: fmt.Sprintf("recover_render_job:%s:%s:%s", job.ID, expectedStatus, nextStatus), NowUnixMs: nowUnixMs,
	})
}

func (s *Service) ListRenderJobs(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("videoproject service is not configured")
	}
	if _, ok, err := s.GetProject(principal, sessionID, projectID); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("video project not found")
	}
	return s.sessions.ListVideoRenderJobs(principal.AccountScopeID, sessionID, projectID, limit)
}

func (s *Service) normalizeVisualPlanArtifacts(principal identity.Principal, sessionID string, plan *pebblestore.VideoPlanProposal) error {
	for index := range plan.Parts {
		part := &plan.Parts[index]
		if part.Visual == nil {
			return fmt.Errorf("video plan part %q is missing its actual visual", part.ID)
		}
		ref := part.Visual
		targetSessionID := strings.TrimSpace(ref.SessionID)
		if targetSessionID == "" {
			targetSessionID = sessionID
			ref.SessionID = sessionID
		}
		sourceSession, owned, err := s.sessions.GetSession(targetSessionID)
		if err != nil || !owned || sourceSession.AccountScopeID != principal.AccountScopeID || sourceSession.UserID != principal.UserID {
			return fmt.Errorf("visual artifact source session %q is not owned by the authenticated principal", targetSessionID)
		}
		variant, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, targetSessionID, ref.CollectionID, ref.VariantID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("visual artifact variant %q in collection %q was not found", ref.VariantID, ref.CollectionID)
			}
			return err
		}
		if variant.Status != pebblestore.SessionArtifactStatusReady {
			return fmt.Errorf("visual artifact variant %q is not ready", variant.ID)
		}
		if ref.EventSeq == 0 || ref.EventSeq != variant.EventSeq {
			return fmt.Errorf("visual artifact variant %q event sequence is stale or missing", variant.ID)
		}
		mediaType := strings.ToLower(strings.TrimSpace(variant.MediaType))
		if !strings.HasPrefix(mediaType, "image/") && mediaType != "video/mp4" {
			return fmt.Errorf("visual artifact variant %q must be an image or video/mp4", variant.ID)
		}
		part.VisualMediaType = variant.MediaType
		if part.StoryboardSource != nil {
			storyboardRef := part.StoryboardSource
			if part.StoryboardStill == nil {
				return fmt.Errorf("video plan part %q must retain its exact exported storyboard still", part.ID)
			}
			storyboardSession, owned, readErr := s.sessions.GetSession(storyboardRef.SessionID)
			if readErr != nil || !owned || storyboardSession.AccountScopeID != principal.AccountScopeID || (storyboardSession.UserID != "" && storyboardSession.UserID != principal.UserID) {
				return fmt.Errorf("storyboard source session %q is not owned by the authenticated principal", storyboardRef.SessionID)
			}
			storyboardVariant, found, readErr := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, storyboardRef.SessionID, storyboardRef.CollectionID, storyboardRef.VariantID)
			if readErr != nil || !found || storyboardVariant.Status != pebblestore.SessionArtifactStatusReady || storyboardVariant.EventSeq != storyboardRef.EventSeq {
				return fmt.Errorf("storyboard source for video plan part %q is stale, missing, or not ready", part.ID)
			}
			stillRef := part.StoryboardStill
			stillVariant, found, readErr := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, stillRef.SessionID, stillRef.CollectionID, stillRef.VariantID)
			if readErr != nil || !found || stillVariant.Status != pebblestore.SessionArtifactStatusReady || stillVariant.EventSeq != stillRef.EventSeq || !strings.HasPrefix(strings.ToLower(stillVariant.MediaType), "image/") {
				return fmt.Errorf("storyboard still for video plan part %q is stale, missing, or not ready", part.ID)
			}
			lineage := stillVariant.Lineage
			if lineage.SourceSessionID != storyboardRef.SessionID || lineage.SourceCollectionID != storyboardRef.CollectionID || lineage.SourceVariantID != storyboardRef.VariantID || lineage.SourceEventSeq != storyboardRef.EventSeq {
				return fmt.Errorf("video plan part %q storyboard still does not descend from its exact storyboard source", part.ID)
			}
			if part.ProductionState == pebblestore.VideoProductionStatePending && *part.Visual != *stillRef {
				return fmt.Errorf("pending video plan part %q must use its exact storyboard still as the visual", part.ID)
			}
		}
		if candidates := part.AnimationCandidates; candidates != nil {
			var canonicalRequirements *pebblestore.SessionArtifactOutputRequirements
			var canonicalProfile *pebblestore.SessionArtifactAnimationProfile
			var canonicalDurationMs int64
			for _, candidate := range candidates.Candidates {
				variant, err := s.animationArtifactVariant(principal, candidate.Source, "text/html")
				if err != nil {
					return fmt.Errorf("video plan part %q candidate %q: %w", part.ID, candidate.ID, err)
				}
				if variant.AnimationProfile == nil {
					return fmt.Errorf("video plan part %q candidate %q requires a reviewed animation profile", part.ID, candidate.ID)
				}
				durationMs := temporalAnimationDuration(variant.Parts)
				if durationMs == 0 || durationMs != part.DurationMs {
					return fmt.Errorf("video plan part %q candidate %q duration does not match part duration_ms", part.ID, candidate.ID)
				}
				if canonicalRequirements == nil {
					canonicalRequirements = variant.OutputRequirements
					canonicalProfile = variant.AnimationProfile
					canonicalDurationMs = durationMs
				} else if !reflect.DeepEqual(canonicalRequirements, variant.OutputRequirements) || !reflect.DeepEqual(canonicalProfile, variant.AnimationProfile) || canonicalDurationMs != durationMs {
					return fmt.Errorf("video plan part %q animation candidates have incompatible output requirements, animation profile, or duration", part.ID)
				}
			}
			if candidates.SelectedSource != nil {
				if err := s.validateAnimationArtifact(principal, candidates.SelectedSource, "text/html"); err != nil {
					return err
				}
			}
			if candidates.Derivative != nil {
				if candidates.SelectedSource == nil {
					return fmt.Errorf("video plan part %q derivative requires selected HTML source", part.ID)
				}
				if err := s.validateAnimationDerivative(principal, candidates.SelectedSource, candidates.Derivative); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) validateTimelineSources(principal identity.Principal, sessionID string, timeline pebblestore.VideoProjectTimeline) error {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok || session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return errors.New("video project session not found")
	}
	for _, clip := range timeline.Clips {
		if clip.SourceKind != pebblestore.VideoClipSourceKindSourceAudio {
			continue
		}
		if clip.AudioSource == nil {
			return fmt.Errorf("source_audio clip %q requires audio_source", clip.ID)
		}
		matched := false
		for _, workspaceID := range pebblestore.SessionVideoWorkspaceIDs(session) {
			record, found, readErr := s.sessions.GetAudioSourceRecord(principal.AccountScopeID, workspaceID, clip.AudioSource.Ref)
			if readErr != nil {
				return readErr
			}
			if !found {
				continue
			}
			if err := pebblestore.ValidateAudioSourceRecord(record); err != nil {
				return fmt.Errorf("audio source %q is stale or unavailable: %w", clip.AudioSource.Ref, err)
			}
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(clip.AudioSource.MIMEType)), "audio/") {
				return fmt.Errorf("audio source %q has unsupported media type %q", clip.AudioSource.Ref, clip.AudioSource.MIMEType)
			}
			if record.SourceFingerprint != clip.AudioSource.SourceFingerprint || record.FingerprintVersion != clip.AudioSource.FingerprintVersion {
				return fmt.Errorf("audio source %q fingerprint mismatch: exact reference is stale", clip.AudioSource.Ref)
			}
			if record.DisplayName != clip.AudioSource.Name || record.MIMEType != clip.AudioSource.MIMEType || record.SizeBytes != clip.AudioSource.SizeBytes {
				return fmt.Errorf("audio source %q exact reference metadata is stale or inconsistent", clip.AudioSource.Ref)
			}
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("audio source %q is not registered in the video project workspace scope", clip.AudioSource.Ref)
		}
	}
	return s.validateTimelineArtifacts(principal, sessionID, timeline)
}

func (s *Service) validateTimelineArtifacts(principal identity.Principal, sessionID string, timeline pebblestore.VideoProjectTimeline) error {
	for _, clip := range timeline.Clips {
		if clip.ArtifactRef != nil {
			ref := clip.ArtifactRef
			targetSessionID := ref.SessionID
			if targetSessionID == "" {
				targetSessionID = sessionID
			}
			sourceSession, owned, err := s.sessions.GetSession(targetSessionID)
			if err != nil || !owned || sourceSession.AccountScopeID != principal.AccountScopeID || (sourceSession.UserID != "" && sourceSession.UserID != principal.UserID) {
				return fmt.Errorf("referenced artifact source session %q is not owned by the authenticated principal", targetSessionID)
			}
			variant, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, targetSessionID, ref.CollectionID, ref.VariantID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("referenced artifact variant %q in collection %q not found", ref.VariantID, ref.CollectionID)
				}
				return err
			}
			if variant.Status != pebblestore.SessionArtifactStatusReady {
				return fmt.Errorf("referenced artifact variant %q is not in ready status (status: %s)", ref.VariantID, variant.Status)
			}
			if ref.EventSeq == 0 || ref.EventSeq != variant.EventSeq {
				return fmt.Errorf("referenced artifact variant %q event sequence is stale or missing", ref.VariantID)
			}
			mediaType := strings.ToLower(strings.TrimSpace(variant.MediaType))
			if !strings.HasPrefix(mediaType, "image/") && mediaType != "video/mp4" {
				return fmt.Errorf("referenced artifact variant %q must be an image or video/mp4", ref.VariantID)
			}
			if declared := strings.ToLower(strings.TrimSpace(clip.MediaType)); declared != "" && declared != mediaType {
				return fmt.Errorf("managed_artifact clip %q media_type does not match its exact artifact", clip.ID)
			}
			if mediaType == "video/mp4" {
				if clip.SourceStartMs < 0 || clip.SourceEndMs <= clip.SourceStartMs {
					return fmt.Errorf("managed video clip %q requires a non-empty source range", clip.ID)
				}
				if clip.DurationMs != clip.SourceEndMs-clip.SourceStartMs {
					return fmt.Errorf("managed video clip %q duration must match its source range", clip.ID)
				}
			}
		}
		if clip.DesignInput != nil {
			input := clip.DesignInput
			targetSessionID := input.SessionID
			if targetSessionID == "" {
				targetSessionID = sessionID
			}
			variant, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, targetSessionID, input.CollectionID, input.VariantID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("referenced design input variant %q in collection %q not found", input.VariantID, input.CollectionID)
				}
				return err
			}
			if variant.Status != pebblestore.SessionArtifactStatusReady {
				return fmt.Errorf("referenced design input variant %q is not ready (status: %s)", input.VariantID, variant.Status)
			}
			if input.EventSeq == 0 || input.EventSeq != variant.EventSeq {
				return fmt.Errorf("referenced design input variant %q event sequence is stale or missing", input.VariantID)
			}
		}
	}
	return nil
}
