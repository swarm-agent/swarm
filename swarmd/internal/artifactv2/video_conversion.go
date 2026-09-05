package artifactv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

type VideoProjectAuthority interface {
	GetProject(identity.Principal, string, string) (pebblestore.VideoProjectSnapshot, bool, error)
	CreateEditProposal(context.Context, identity.Principal, videoproject.CreateEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error)
}

type VideoConversionService struct {
	artifacts *Service
	projects  VideoProjectAuthority
	renderer  MotionRenderer
	now       func() time.Time
}

func NewVideoConversionService(artifacts *Service, projects VideoProjectAuthority, renderer MotionRenderer) *VideoConversionService {
	return &VideoConversionService{artifacts: artifacts, projects: projects, renderer: renderer, now: time.Now}
}

type ConvertToVideoInput struct {
	RequestID       string
	VideoSessionID  string
	ProjectID       string
	BaseRevisionID  string
	ArtifactID      string
	PublishedHeadID string
	Title           string
	Rationale       string
}

// ConvertToPendingProposal is the only Artifact V2 -> Video Studio construction
// boundary. The caller supplies one exact V2 head and one exact project base;
// parts, candidates, fallbacks and lineage are assembled server-side.
func (s *VideoConversionService) ConvertToPendingProposal(ctx context.Context, principal identity.Principal, input ConvertToVideoInput) (pebblestore.VideoEditProposalSnapshot, error) {
	if s == nil || s.artifacts == nil || s.projects == nil || !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v2 video conversion authority is unavailable")
	}
	input.RequestID, input.VideoSessionID, input.ProjectID, input.BaseRevisionID, input.ArtifactID, input.PublishedHeadID = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.VideoSessionID), strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.BaseRevisionID), strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.PublishedHeadID)
	if input.RequestID == "" || input.VideoSessionID == "" || input.ProjectID == "" || input.BaseRevisionID == "" || input.ArtifactID == "" || input.PublishedHeadID == "" {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v2 video conversion requires request, project base, and exact published head")
	}
	project, ok, err := s.projects.GetProject(principal, input.VideoSessionID, input.ProjectID)
	if err != nil || !ok || project.CurrentRevisionID != input.BaseRevisionID {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v2 video conversion project base is stale or unavailable")
	}
	artifactPrincipal := Principal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: projectSourceSession(s.artifacts, principal, input.ArtifactID)}
	if artifactPrincipal.SessionID == "" {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v2 video conversion source is not owned")
	}
	working, found, err := s.artifacts.store.GetArtifactV2Working(principal.AccountScopeID, input.ArtifactID)
	if err != nil || !found || working.UserID != principal.UserID || working.SessionID != artifactPrincipal.SessionID {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v2 video conversion source is not owned")
	}
	ready, err := s.artifacts.ResolveReady(artifactPrincipal, input.ArtifactID, input.PublishedHeadID)
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	var plan pebblestore.VideoPlanProposal
	if working.Kind == ArtifactKindStoryboard {
		plan, err = s.storyboardPlan(ctx, artifactPrincipal, ready)
	} else {
		plan, err = s.animationPlan(ctx, artifactPrincipal, ready, input.RequestID)
	}
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	proposalID := deterministicID("videopropv2", input.VideoSessionID, input.ProjectID, input.BaseRevisionID, input.ArtifactID, input.PublishedHeadID, input.RequestID)
	intent := pebblestore.VideoEditProposalIntentArtifactV2Convert
	return s.projects.CreateEditProposal(ctx, principal, videoproject.CreateEditProposalInput{SessionID: input.VideoSessionID, ProjectID: input.ProjectID, ProposalID: proposalID, BaseRevisionID: input.BaseRevisionID, Title: firstNonEmpty(input.Title, "Artifact V2 video proposal"), Rationale: input.Rationale, Intent: intent, Plan: &plan, NowUnixMs: s.now().UnixMilli()})
}

func projectSourceSession(artifacts *Service, principal identity.Principal, artifactID string) string {
	if artifacts == nil {
		return ""
	}
	working, ok, _ := artifacts.store.GetArtifactV2Working(principal.AccountScopeID, strings.TrimSpace(artifactID))
	if !ok || working.UserID != principal.UserID {
		return ""
	}
	return working.SessionID
}

func (s *VideoConversionService) storyboardPlan(ctx context.Context, principal Principal, ready ReadyReference) (pebblestore.VideoPlanProposal, error) {
	head, err := s.artifacts.ResolveStoryboardHead(ctx, principal, ready.ArtifactID, ready.PublishedHeadID)
	if err != nil {
		return pebblestore.VideoPlanProposal{}, err
	}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Summary: "Artifact V2 storyboard", CompositionCatalog: head.Catalog}
	for _, section := range head.Sections {
		still, err := s.findStoryboardStill(principal, head.Reference, section)
		if err != nil {
			return pebblestore.VideoPlanProposal{}, err
		}
		stillRef, err := s.artifacts.DerivativeVideoReference(principal, ready.ArtifactID, still.ID)
		if err != nil {
			return pebblestore.VideoPlanProposal{}, err
		}
		sourceRef, err := s.artifacts.BuildVideoReference(principal, ready)
		if err != nil {
			return pebblestore.VideoPlanProposal{}, err
		}
		sourceRef.PartID, sourceRef.PartRevisionID, sourceRef.CaptureStateID = section.Part.ID, section.Revision.ID, section.Section.CaptureStateID
		plan.Parts = append(plan.Parts, pebblestore.VideoPlanPart{ID: section.Section.ID, Title: section.Section.Title, DurationMs: int64(section.Section.DurationMS), Narration: section.Section.Narration, OnScreenText: section.Section.OnScreenText, VisualDirection: section.Section.CreativeDirection, CaptureStateID: section.Section.CaptureStateID, FilmingRequirements: append([]string(nil), section.Section.FilmingRequirements...), ProductionState: section.Section.ProductionState, ArtifactV2Source: &sourceRef, ArtifactV2Still: &stillRef, ArtifactV2Visual: &stillRef, VisualMediaType: "image/png", Composition: section.Section.Composition})
	}
	return plan, nil
}

func (s *VideoConversionService) findStoryboardStill(principal Principal, ready ReadyReference, section StoryboardHeadSection) (pebblestore.ArtifactV2Derivative, error) {
	derivatives, err := s.listDerivatives(principal, ready.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2Derivative{}, err
	}
	for _, derivative := range derivatives {
		if derivative.Kind == "storyboard_still" && derivative.Status == "ready" && derivative.CompositionID == ready.CompositionID && derivative.CompositionDigest == ready.DigestSHA256 && derivative.SourcePartID == section.Part.ID && derivative.SourcePartRevisionID == section.Revision.ID && derivative.CaptureStateID == section.Section.CaptureStateID {
			return derivative, nil
		}
	}
	return pebblestore.ArtifactV2Derivative{}, fmt.Errorf("artifact v2 storyboard part %q has no exact rendered still", section.Section.ID)
}

func (s *VideoConversionService) listDerivatives(principal Principal, artifactID string) ([]pebblestore.ArtifactV2Derivative, error) {
	working, ok, err := s.artifacts.store.GetArtifactV2Working(principal.AccountScopeID, artifactID)
	if err != nil || !ok {
		return nil, errors.New("artifact v2 source unavailable")
	}
	if working.SessionID != principal.SessionID || working.UserID != principal.UserID {
		return nil, errors.New("artifact v2 source ownership mismatch")
	}
	return s.artifacts.store.ListArtifactV2Derivatives(principal.AccountScopeID, artifactID, 256)
}

func (s *VideoConversionService) animationPlan(ctx context.Context, principal Principal, ready ReadyReference, requestID string) (pebblestore.VideoPlanProposal, error) {
	source, err := s.artifacts.BuildVideoReference(principal, ready)
	if err != nil {
		return pebblestore.VideoPlanProposal{}, err
	}
	if source.MediaType != "text/html" || source.DurationMs <= 0 || source.AnimationProfile == "" {
		return pebblestore.VideoPlanProposal{}, errors.New("artifact v2 animation source lacks compatible HTML duration or profile")
	}
	derivatives, err := s.listDerivatives(principal, ready.ArtifactID)
	if err != nil {
		return pebblestore.VideoPlanProposal{}, err
	}
	var fallback pebblestore.ArtifactV2VideoReference
	for _, derivative := range derivatives {
		if derivative.Kind == "fallback" && derivative.Status == "ready" && derivative.CompositionID == ready.CompositionID && derivative.CompositionDigest == ready.DigestSHA256 {
			fallback, err = s.artifacts.DerivativeVideoReference(principal, ready.ArtifactID, derivative.ID)
			break
		}
	}
	if err != nil {
		return pebblestore.VideoPlanProposal{}, err
	}
	if fallback.ArtifactID == "" {
		working, ok, readErr := s.artifacts.store.GetArtifactV2Working(principal.AccountScopeID, ready.ArtifactID)
		if readErr != nil || !ok || s.renderer == nil {
			return pebblestore.VideoPlanProposal{}, errors.New("artifact v2 animation fallback rendering is unavailable")
		}
		created, createErr := s.artifacts.CreateDerivative(ctx, principal, CreateDerivativeInput{RequestID: strings.TrimSpace(requestID) + ":fallback", ArtifactID: ready.ArtifactID, ExpectedWorkingRevision: working.Revision, Kind: "fallback", Renderer: s.renderer})
		if createErr != nil || created.Status != "ready" {
			return pebblestore.VideoPlanProposal{}, errors.New("artifact v2 animation fallback could not be created")
		}
		fallback, err = s.artifacts.DerivativeVideoReference(principal, ready.ArtifactID, created.ID)
		if err != nil {
			return pebblestore.VideoPlanProposal{}, err
		}
	}
	candidates := []pebblestore.VideoAnimationCandidate{{ID: "head", V2Source: &source, Label: "Artifact V2 head"}}
	return pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Summary: "Artifact V2 animation", Parts: []pebblestore.VideoPlanPart{{ID: "animation", Title: "Animation", DurationMs: source.DurationMs, ArtifactV2Visual: &fallback, VisualMediaType: "image/png", AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Candidates: candidates, Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection}}}}, nil
}
