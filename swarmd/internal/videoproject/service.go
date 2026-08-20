package videoproject

import (
	"context"
	"errors"
	"fmt"
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

type CreateEditProposalInput struct { SessionID, ProjectID, ProposalID, BaseRevisionID, Title, Rationale string; Operations []pebblestore.VideoEditOperation; AffectedRanges []pebblestore.VideoTimelineRange; NowUnixMs int64 }
type AcceptEditProposalInput struct { SessionID, ProjectID, ProposalID, RevisionID, Description, ChangeSummary, AuthorPrincipal string; SelectedOperationIDs []string; NowUnixMs int64 }

func (s *Service) CreateEditProposal(ctx context.Context, principal identity.Principal, input CreateEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error) {
	if s == nil || s.sessions == nil { return pebblestore.VideoEditProposalSnapshot{}, errors.New("videoproject service is not configured") }
	if !principal.Valid() { return pebblestore.VideoEditProposalSnapshot{}, errors.New("authenticated principal is required") }
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, input.SessionID, input.ProjectID); if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) { return pebblestore.VideoEditProposalSnapshot{}, errors.New("video project not found") }
	return s.sessions.CreateVideoEditProposal(pebblestore.CreateVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: input.SessionID, ProjectID: input.ProjectID, ProposalID: input.ProposalID, BaseRevisionID: input.BaseRevisionID, Title: input.Title, Rationale: input.Rationale, Operations: input.Operations, AffectedRanges: input.AffectedRanges, NowUnixMs: input.NowUnixMs})
}
func (s *Service) GetEditProposal(principal identity.Principal, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	if !principal.Valid() { return pebblestore.VideoEditProposalSnapshot{}, false, errors.New("authenticated principal is required") }
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) { return pebblestore.VideoEditProposalSnapshot{}, false, nil }
	return s.sessions.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
}
func (s *Service) ListEditProposals(principal identity.Principal, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	if !principal.Valid() { return nil, errors.New("authenticated principal is required") }
	project, ok, err := s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok || (project.UserID != "" && project.UserID != principal.UserID) { return nil, errors.New("video project not found") }
	return s.sessions.ListVideoEditProposals(principal.AccountScopeID, sessionID, projectID, limit)
}
func (s *Service) AcceptEditProposal(ctx context.Context, principal identity.Principal, input AcceptEditProposalInput) (pebblestore.VideoEditProposalSnapshot, pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	if s == nil || s.sessions == nil { return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("videoproject service is not configured") }
	if !principal.Valid() { return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("authenticated principal is required") }
	proposalSnapshot, ok, err := s.sessions.GetVideoEditProposal(principal.AccountScopeID, input.SessionID, input.ProjectID, input.ProposalID)
	if err != nil || !ok { return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, errors.New("video edit proposal not found") }
	selected := make(map[string]struct{}, len(input.SelectedOperationIDs))
	for _, id := range input.SelectedOperationIDs { selected[strings.TrimSpace(id)] = struct{}{} }
	for _, operation := range proposalSnapshot.Operations {
		if _, ok := selected[operation.ID]; !ok || operation.Clip == nil { continue }
		if err := s.validateTimelineArtifacts(principal, input.SessionID, pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{*operation.Clip}}); err != nil { return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err }
	}
	proposal, revision, project, err := s.sessions.ResolveVideoEditProposal(pebblestore.ResolveVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: input.SessionID, ProjectID: input.ProjectID, ProposalID: input.ProposalID, RevisionID: input.RevisionID, Description: input.Description, ChangeSummary: input.ChangeSummary, AuthorPrincipal: input.AuthorPrincipal, SelectedOperationIDs: input.SelectedOperationIDs, NowUnixMs: input.NowUnixMs})
	if err != nil { return pebblestore.VideoEditProposalSnapshot{}, pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err }; return proposal, *revision, *project, nil
}
func (s *Service) RejectEditProposal(ctx context.Context, principal identity.Principal, sessionID, projectID, proposalID string, now int64) (pebblestore.VideoEditProposalSnapshot, error) {
	if s == nil || s.sessions == nil { return pebblestore.VideoEditProposalSnapshot{}, errors.New("videoproject service is not configured") }
	if !principal.Valid() { return pebblestore.VideoEditProposalSnapshot{}, errors.New("authenticated principal is required") }
	proposal, _, _, err := s.sessions.ResolveVideoEditProposal(pebblestore.ResolveVideoEditProposalInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, ProjectID: projectID, ProposalID: proposalID, Reject: true, NowUnixMs: now})
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

	if input.InitialTimeline != nil {
		if err := s.validateTimelineArtifacts(principal, input.SessionID, *input.InitialTimeline); err != nil {
			return pebblestore.VideoProjectSnapshot{}, nil, err
		}
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
	if project, ok, err := s.sessions.GetPrimaryVideoToolProject(principal.AccountScopeID, input.SessionID); err != nil {
		return pebblestore.VideoProjectSnapshot{}, nil, err
	} else if ok {
		if project.UserID != "" && project.UserID != principal.UserID {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("video project ownership does not match authenticated principal")
		}
		return project, nil, nil
	}
	input.ProjectKind = pebblestore.VideoProjectKindVideoTool
	return s.CreateProject(ctx, principal, input)
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

	if err := s.validateTimelineArtifacts(principal, input.SessionID, input.Timeline); err != nil {
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
	if err := s.validateTimelineArtifacts(principal, input.SessionID, source.Timeline); err != nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, err
	}
	return s.sessions.CreateVideoProjectRevision(pebblestore.CreateVideoProjectRevisionInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		SessionID: input.SessionID, ProjectID: input.ProjectID, RevisionID: input.RevisionID,
		Description: input.Description, ChangeSummary: input.ChangeSummary, Timeline: source.Timeline,
		AuthorPrincipal: input.AuthorPrincipal, RestoredFromRevisionID: source.ID, NowUnixMs: input.NowUnixMs,
	})
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
	if err != nil || !ok {
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
	return s.sessions.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
}

func (s *Service) GetRevision(principal identity.Principal, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, errors.New("videoproject service is not configured")
	}
	return s.sessions.GetVideoProjectRevision(principal.AccountScopeID, sessionID, projectID, revisionID)
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
	return s.sessions.ListVideoProjectRevisions(principal.AccountScopeID, sessionID, projectID, limit)
}

func (s *Service) GetRenderJob(principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.VideoRenderJobSnapshot{}, false, errors.New("videoproject service is not configured")
	}
	return s.sessions.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
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
	return s.sessions.ListVideoRenderJobs(principal.AccountScopeID, sessionID, projectID, limit)
}

func (s *Service) validateTimelineArtifacts(principal identity.Principal, sessionID string, timeline pebblestore.VideoProjectTimeline) error {
	for _, clip := range timeline.Clips {
		if clip.ArtifactRef != nil {
			ref := clip.ArtifactRef
			targetSessionID := ref.SessionID
			if targetSessionID == "" {
				targetSessionID = sessionID
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
