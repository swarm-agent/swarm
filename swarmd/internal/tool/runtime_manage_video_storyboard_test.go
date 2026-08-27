package tool

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

type storyboardArtifactAuthority struct {
	*fakeArtifactAuthority
	variants map[string]pebblestore.SessionArtifactVariant
	bodies   map[string][]byte
}

func storyboardRefKey(ref pebblestore.SessionArtifactSelectionReference) string {
	return ref.SessionID + "\x00" + ref.CollectionID + "\x00" + ref.VariantID
}

func (a *storyboardArtifactAuthority) GetReference(_ artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	variant, ok := a.variants[storyboardRefKey(ref)]
	if !ok || variant.EventSeq != ref.EventSeq {
		return pebblestore.SessionArtifactVariant{}, context.Canceled
	}
	return variant, nil
}

func (a *storyboardArtifactAuthority) ReadReference(_ context.Context, _ artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := a.GetReference(artifact.Principal{}, ref)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	return append([]byte(nil), a.bodies[storyboardRefKey(ref)]...), variant, nil
}

type storyboardProjectService struct {
	project pebblestore.VideoProjectSnapshot
}

func (s *storyboardProjectService) GetProject(identity.Principal, string, string) (pebblestore.VideoProjectSnapshot, bool, error) {
	return s.project, true, nil
}
func (*storyboardProjectService) CreateProject(context.Context, identity.Principal, videoproject.CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	return pebblestore.VideoProjectSnapshot{}, nil, nil
}
func (*storyboardProjectService) GetOrCreatePrimaryVideoToolProject(context.Context, identity.Principal, videoproject.CreateProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	return pebblestore.VideoProjectSnapshot{}, nil, nil
}
func (*storyboardProjectService) CreateRevision(context.Context, identity.Principal, videoproject.CreateRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, nil
}
func (*storyboardProjectService) RestoreRevision(context.Context, identity.Principal, videoproject.RestoreRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	return pebblestore.VideoProjectRevisionSnapshot{}, pebblestore.VideoProjectSnapshot{}, nil
}
func (*storyboardProjectService) StartRenderJob(context.Context, identity.Principal, videoproject.StartRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	return pebblestore.VideoRenderJobSnapshot{}, nil
}
func (*storyboardProjectService) GetRevision(identity.Principal, string, string, string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
}
func (*storyboardProjectService) ListProjects(identity.Principal, string, int) ([]pebblestore.VideoProjectSnapshot, error) {
	return nil, nil
}
func (*storyboardProjectService) ListRevisions(identity.Principal, string, string, int) ([]pebblestore.VideoProjectRevisionSnapshot, error) {
	return nil, nil
}
func (*storyboardProjectService) GetRenderJob(identity.Principal, string, string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	return pebblestore.VideoRenderJobSnapshot{}, false, nil
}
func (*storyboardProjectService) ListRenderJobs(identity.Principal, string, string, int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	return nil, nil
}
func (*storyboardProjectService) CreateEditProposal(context.Context, identity.Principal, videoproject.CreateEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error) {
	return pebblestore.VideoEditProposalSnapshot{}, nil
}
func (*storyboardProjectService) GetEditProposal(identity.Principal, string, string, string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	return pebblestore.VideoEditProposalSnapshot{}, false, nil
}
func (*storyboardProjectService) ListEditProposals(identity.Principal, string, string, int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	return nil, nil
}
func (*storyboardProjectService) SelectAnimationCandidate(context.Context, identity.Principal, videoproject.SelectAnimationCandidateInput) (pebblestore.VideoEditProposalSnapshot, error) {
	return pebblestore.VideoEditProposalSnapshot{}, nil
}
func (*storyboardProjectService) PromoteAnimationDerivative(context.Context, identity.Principal, videoproject.PromoteAnimationDerivativeInput) (pebblestore.VideoEditProposalSnapshot, error) {
	return pebblestore.VideoEditProposalSnapshot{}, nil
}

func TestImportStoryboardPlanAuthenticatesManifestExportsLineageAndRevision(t *testing.T) {
	sourceRef := pebblestore.SessionArtifactSelectionReference{SessionID: "artifact-session", CollectionID: "source-collection", VariantID: "storyboard", EventSeq: 7}
	openingRef := pebblestore.SessionArtifactSelectionReference{SessionID: "artifact-session", CollectionID: "captures", VariantID: "opening-png", EventSeq: 11}
	proofRef := pebblestore.SessionArtifactSelectionReference{SessionID: "artifact-session", CollectionID: "captures", VariantID: "proof-png", EventSeq: 12}
	html := []byte(`<!doctype html><script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"opening"},{"id":"proof"}]}</script><script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","sections":[{"id":"intro","capture_state_id":"opening","title":"Intro","duration_ms":2500,"narration":"Meet Swarm.","creative_direction":"Slow push.","filming_requirements":["Locked camera"],"production_state":"pending"},{"id":"proof","capture_state_id":"proof","title":"Proof","duration_ms":3000,"creative_direction":"Over shoulder.","filming_requirements":["Readable screen"],"production_state":"ready"}]}</script>`)
	lineage := pebblestore.SessionArtifactLineage{SourceSessionID: sourceRef.SessionID, SourceCollectionID: sourceRef.CollectionID, SourceVariantID: sourceRef.VariantID, SourceEventSeq: sourceRef.EventSeq}
	authority := &storyboardArtifactAuthority{fakeArtifactAuthority: &fakeArtifactAuthority{}, variants: map[string]pebblestore.SessionArtifactVariant{
		storyboardRefKey(sourceRef):  {ID: sourceRef.VariantID, CollectionID: sourceRef.CollectionID, SessionID: sourceRef.SessionID, EventSeq: sourceRef.EventSeq, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"},
		storyboardRefKey(openingRef): {ID: openingRef.VariantID, CollectionID: openingRef.CollectionID, SessionID: openingRef.SessionID, EventSeq: openingRef.EventSeq, Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png", Lineage: lineage},
		storyboardRefKey(proofRef):   {ID: proofRef.VariantID, CollectionID: proofRef.CollectionID, SessionID: proofRef.SessionID, EventSeq: proofRef.EventSeq, Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png", Lineage: lineage},
	}, bodies: map[string][]byte{storyboardRefKey(sourceRef): html}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.videoProjects = &storyboardProjectService{project: pebblestore.VideoProjectSnapshot{ID: "project", CurrentRevisionID: "revision-1"}}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	args := map[string]any{"action": "import_storyboard", "project_id": "project", "base_revision_id": "revision-1", "storyboard_source": map[string]any{"session_id": sourceRef.SessionID, "collection_id": sourceRef.CollectionID, "variant_id": sourceRef.VariantID, "event_seq": sourceRef.EventSeq}, "exports": []any{
		map[string]any{"state_id": "opening", "reference": map[string]any{"session_id": openingRef.SessionID, "collection_id": openingRef.CollectionID, "variant_id": openingRef.VariantID, "event_seq": openingRef.EventSeq}},
		map[string]any{"state_id": "proof", "reference": map[string]any{"session_id": proofRef.SessionID, "collection_id": proofRef.CollectionID, "variant_id": proofRef.VariantID, "event_seq": proofRef.EventSeq}},
	}}
	plan, err := runtime.importStoryboardPlan(context.Background(), principal, "video-session", "project", "revision-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != pebblestore.VideoPlanKindInitial || len(plan.Parts) != 2 || plan.Parts[0].ID != "intro" || plan.Parts[0].Visual.VariantID != openingRef.VariantID || plan.Parts[0].StoryboardStill == nil || *plan.Parts[0].StoryboardStill != *plan.Parts[0].Visual || plan.Parts[0].StoryboardSource.EventSeq != sourceRef.EventSeq || plan.Parts[0].ProductionState != "pending" {
		t.Fatalf("plan = %#v", plan)
	}

	runtime.videoProjects = &storyboardProjectService{project: pebblestore.VideoProjectSnapshot{ID: "project", CurrentRevisionID: "revision-2"}}
	if _, err := runtime.importStoryboardPlan(context.Background(), principal, "video-session", "project", "revision-1", args); err == nil || !strings.Contains(err.Error(), "storyboard_revision_stale") {
		t.Fatalf("stale revision error = %v", err)
	}
	runtime.videoProjects = &storyboardProjectService{project: pebblestore.VideoProjectSnapshot{ID: "project", CurrentRevisionID: "revision-1"}}
	bad := authority.variants[storyboardRefKey(proofRef)]
	bad.Lineage.SourceVariantID = "other"
	authority.variants[storyboardRefKey(proofRef)] = bad
	if _, err := runtime.importStoryboardPlan(context.Background(), principal, "video-session", "project", "revision-1", args); err == nil || !strings.Contains(err.Error(), "storyboard_export_lineage_mismatch") {
		t.Fatalf("lineage error = %v", err)
	}
}
