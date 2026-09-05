package videoproject

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestTemporalAnimationDurationUsesOrderedSectionEnd(t *testing.T) {
	parts := []pebblestore.SessionArtifactPart{
		{ID: "part-1", Kind: "temporal", StartMs: 0, EndMs: 4000},
		{ID: "part-2", Kind: "temporal", StartMs: 4000, EndMs: 8000},
		{ID: "part-3", Kind: "temporal", StartMs: 8000, EndMs: 12000},
	}
	if got := temporalAnimationDuration(parts); got != 12000 {
		t.Fatalf("temporalAnimationDuration() = %d, want 12000", got)
	}
}

type fakeSessionStore struct {
	sessions            map[string]pebblestore.SessionSnapshot
	projects            map[string]pebblestore.VideoProjectSnapshot
	revisions           map[string]map[string]pebblestore.VideoProjectRevisionSnapshot
	jobs                map[string]pebblestore.VideoRenderJobSnapshot
	proposals           map[string]pebblestore.VideoEditProposalSnapshot
	artifacts           map[string]pebblestore.SessionArtifactVariant
	createProposalCalls int
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions:  make(map[string]pebblestore.SessionSnapshot),
		projects:  make(map[string]pebblestore.VideoProjectSnapshot),
		revisions: make(map[string]map[string]pebblestore.VideoProjectRevisionSnapshot),
		jobs:      make(map[string]pebblestore.VideoRenderJobSnapshot),
		proposals: make(map[string]pebblestore.VideoEditProposalSnapshot),
		artifacts: make(map[string]pebblestore.SessionArtifactVariant),
	}
}

func (f *fakeSessionStore) GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	s, ok := f.sessions[sessionID]
	return s, ok, nil
}

func (f *fakeSessionStore) CreateVideoProject(input pebblestore.CreateVideoProjectInput) (pebblestore.VideoProjectSnapshot, *pebblestore.VideoProjectRevisionSnapshot, error) {
	p := pebblestore.VideoProjectSnapshot{
		SchemaVersion:         1,
		ID:                    input.ProjectID,
		AccountScopeID:        input.AccountScopeID,
		UserID:                input.UserID,
		WorkspaceID:           input.WorkspaceID,
		SessionID:             input.SessionID,
		Title:                 input.Title,
		Description:           input.Description,
		OutputPreset:          input.OutputPreset,
		ProjectKind:           input.ProjectKind,
		Metadata:              input.Metadata,
		CurrentRevisionID:     "",
		CurrentRevisionNumber: 0,
		RevisionCount:         0,
		CreatedAt:             100,
		UpdatedAt:             100,
	}
	f.projects[p.ID] = p
	var rev *pebblestore.VideoProjectRevisionSnapshot
	if input.InitialTimeline != nil {
		r := pebblestore.VideoProjectRevisionSnapshot{
			SchemaVersion:  1,
			ID:             "vrev_1",
			ProjectID:      p.ID,
			RevisionNumber: 1,
			AccountScopeID: input.AccountScopeID,
			UserID:         input.UserID,
			SessionID:      input.SessionID,
			Timeline:       *input.InitialTimeline,
			CreatedAt:      100,
		}
		if f.revisions[p.ID] == nil {
			f.revisions[p.ID] = make(map[string]pebblestore.VideoProjectRevisionSnapshot)
		}
		f.revisions[p.ID][r.ID] = r
		p.CurrentRevisionID = r.ID
		p.CurrentRevisionNumber = 1
		p.RevisionCount = 1
		f.projects[p.ID] = p
		rev = &r
	}
	return p, rev, nil
}

func (f *fakeSessionStore) GetVideoProject(accountScopeID, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error) {
	p, ok := f.projects[projectID]
	if !ok || p.AccountScopeID != accountScopeID || p.SessionID != sessionID {
		return pebblestore.VideoProjectSnapshot{}, false, nil
	}
	return p, true, nil
}

func (f *fakeSessionStore) GetPrimaryVideoToolProject(accountScopeID, sessionID string) (pebblestore.VideoProjectSnapshot, bool, error) {
	for _, p := range f.projects {
		if p.AccountScopeID == accountScopeID && p.SessionID == sessionID && p.ProjectKind == pebblestore.VideoProjectKindVideoTool {
			return p, true, nil
		}
	}
	return pebblestore.VideoProjectSnapshot{}, false, nil
}

func (f *fakeSessionStore) ListVideoProjectsForAccount(accountScopeID string, limit int) ([]pebblestore.VideoProjectSnapshot, error) {
	var list []pebblestore.VideoProjectSnapshot
	for _, project := range f.projects {
		if project.AccountScopeID == accountScopeID {
			list = append(list, project)
		}
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (f *fakeSessionStore) ListVideoProjects(accountScopeID, sessionID string, limit int) ([]pebblestore.VideoProjectSnapshot, error) {
	var list []pebblestore.VideoProjectSnapshot
	for _, p := range f.projects {
		if p.AccountScopeID == accountScopeID && p.SessionID == sessionID {
			list = append(list, p)
		}
	}
	return list, nil
}

func (f *fakeSessionStore) CreateVideoProjectRevision(input pebblestore.CreateVideoProjectRevisionInput) (pebblestore.VideoProjectRevisionSnapshot, pebblestore.VideoProjectSnapshot, error) {
	p := f.projects[input.ProjectID]
	nextNum := p.RevisionCount + 1
	revID := input.RevisionID
	if revID == "" {
		revID = fmt.Sprintf("vrev_%d", nextNum)
	}
	r := pebblestore.VideoProjectRevisionSnapshot{
		SchemaVersion:          1,
		ID:                     revID,
		ProjectID:              input.ProjectID,
		RevisionNumber:         nextNum,
		AccountScopeID:         input.AccountScopeID,
		UserID:                 input.UserID,
		SessionID:              input.SessionID,
		ParentRevisionID:       p.CurrentRevisionID,
		RestoredFromRevisionID: input.RestoredFromRevisionID,
		Description:            input.Description,
		ChangeSummary:          input.ChangeSummary,
		Timeline:               input.Timeline,
		AuthorPrincipal:        input.AuthorPrincipal,
		CreatedAt:              200,
	}
	if f.revisions[input.ProjectID] == nil {
		f.revisions[input.ProjectID] = make(map[string]pebblestore.VideoProjectRevisionSnapshot)
	}
	f.revisions[input.ProjectID][r.ID] = r
	p.CurrentRevisionID = r.ID
	p.CurrentRevisionNumber = nextNum
	p.RevisionCount = nextNum
	f.projects[p.ID] = p
	return r, p, nil
}

func (f *fakeSessionStore) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	revMap, ok := f.revisions[projectID]
	if !ok {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
	}
	r, ok := revMap[revisionID]
	if !ok || r.AccountScopeID != accountScopeID || r.SessionID != sessionID {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
	}
	return r, true, nil
}

func (f *fakeSessionStore) GetVideoProjectRevisionByNumber(accountScopeID, sessionID, projectID string, revisionNumber int) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	revMap, ok := f.revisions[projectID]
	if !ok {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
	}
	for _, r := range revMap {
		if r.RevisionNumber == revisionNumber && r.AccountScopeID == accountScopeID && r.SessionID == sessionID {
			return r, true, nil
		}
	}
	return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
}

func (f *fakeSessionStore) ListVideoProjectRevisions(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoProjectRevisionSnapshot, error) {
	var list []pebblestore.VideoProjectRevisionSnapshot
	revMap := f.revisions[projectID]
	for _, r := range revMap {
		if r.AccountScopeID == accountScopeID && r.SessionID == sessionID {
			list = append(list, r)
		}
	}
	return list, nil
}

func (f *fakeSessionStore) CreateVideoEditProposal(input pebblestore.CreateVideoEditProposalInput) (pebblestore.VideoEditProposalSnapshot, error) {
	f.createProposalCalls++
	return pebblestore.VideoEditProposalSnapshot{}, nil
}
func (f *fakeSessionStore) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	proposal, ok := f.proposals[proposalID]
	if !ok || proposal.AccountScopeID != accountScopeID || proposal.SessionID != sessionID || proposal.ProjectID != projectID {
		return pebblestore.VideoEditProposalSnapshot{}, false, nil
	}
	return proposal, true, nil
}
func (f *fakeSessionStore) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	var proposals []pebblestore.VideoEditProposalSnapshot
	for _, proposal := range f.proposals {
		if proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			proposals = append(proposals, proposal)
		}
	}
	return proposals, nil
}
func (f *fakeSessionStore) ResolveVideoEditProposal(input pebblestore.ResolveVideoEditProposalInput) (pebblestore.VideoEditProposalSnapshot, *pebblestore.VideoProjectRevisionSnapshot, *pebblestore.VideoProjectSnapshot, error) {
	return pebblestore.VideoEditProposalSnapshot{}, nil, nil, nil
}

func (f *fakeSessionStore) CreateVideoRenderJob(input pebblestore.CreateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	j := pebblestore.VideoRenderJobSnapshot{
		SchemaVersion:  1,
		ID:             input.JobID,
		ProjectID:      input.ProjectID,
		RevisionID:     input.RevisionID,
		AccountScopeID: input.AccountScopeID,
		UserID:         input.UserID,
		SessionID:      input.SessionID,
		Status:         pebblestore.VideoRenderJobStatusQueued,
		CreatedAt:      300,
	}
	f.jobs[j.ID] = j
	return j, nil
}

func (f *fakeSessionStore) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	j, ok := f.jobs[jobID]
	if !ok || j.AccountScopeID != accountScopeID || j.SessionID != sessionID {
		return pebblestore.VideoRenderJobSnapshot{}, false, nil
	}
	return j, true, nil
}

func (f *fakeSessionStore) UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	j := f.jobs[input.JobID]
	j.Status = input.Status
	j.Progress = input.Progress
	j.OutputArtifact = input.OutputArtifact
	j.FailureCode = input.FailureCode
	j.FailureReason = input.FailureReason
	f.jobs[j.ID] = j
	return j, nil
}

func (f *fakeSessionStore) ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	var list []pebblestore.VideoRenderJobSnapshot
	for _, j := range f.jobs {
		if j.AccountScopeID == accountScopeID && j.SessionID == sessionID && (projectID == "" || j.ProjectID == projectID) {
			list = append(list, j)
		}
	}
	return list, nil
}

func (f *fakeSessionStore) ListRecoverableVideoRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	var list []pebblestore.VideoRenderJobSnapshot
	for _, job := range f.jobs {
		if job.Status == pebblestore.VideoRenderJobStatusQueued || job.Status == pebblestore.VideoRenderJobStatusRendering {
			list = append(list, job)
		}
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (f *fakeSessionStore) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", accountScopeID, sessionID, collectionID, variantID)
	v, ok := f.artifacts[key]
	return v, ok, nil
}

func (f *fakeSessionStore) GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.AudioSourceRecord, bool, error) {
	return pebblestore.AudioSourceRecord{}, false, nil
}

type fakeArtifactV3Authority struct {
	accountScopeID string
	userID         string
	allowed        map[pebblestore.ArtifactV3VideoReference]bool
	calls          []pebblestore.ArtifactV3VideoReference
}

func (f *fakeArtifactV3Authority) ValidateVideoReference(accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) error {
	f.calls = append(f.calls, ref)
	if accountScopeID != f.accountScopeID || userID != f.userID {
		return fmt.Errorf("Artifact V3 reference owner does not match authenticated principal")
	}
	if !f.allowed[ref] {
		return fmt.Errorf("Artifact V3 reference is stale, foreign, or missing")
	}
	return nil
}

func testArtifactV3VideoPlan() (pebblestore.VideoPlanProposal, []pebblestore.ArtifactV3VideoReference) {
	digest := func(char byte) string { return strings.Repeat(string(char), 64) }
	oid := func(char byte) string { return strings.Repeat(string(char), 40) }
	source := pebblestore.ArtifactV3VideoReference{
		SessionID: "artifact-session", ArtifactID: "artifact", RevisionID: "revision", CommitOID: oid('a'), TreeOID: oid('b'),
		ManifestDigestSHA256: digest('c'), BuildID: "build", ValidationID: "validation", PartID: "motion", CaptureStateID: "capture",
		EventSeq: 7, DigestSHA256: digest('d'), MediaType: "text/html", DurationMs: 2000, FPS: 30, AnimationProfile: "motion_ui",
	}
	still := source
	still.DerivativeID, still.DigestSHA256, still.MediaType = "still", digest('e'), "image/png"
	visual := source
	visual.DerivativeID, visual.DigestSHA256, visual.MediaType = "mp4", digest('f'), "video/mp4"
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{
		ID: "motion", Title: "Motion", DurationMs: 2000, CaptureStateID: "capture", FilmingRequirements: []string{"Capture the complete motion state"}, ProductionState: pebblestore.VideoProductionStateReady,
		ArtifactV3Source: &source, ArtifactV3Still: &still, ArtifactV3Visual: &visual, VisualMediaType: "video/mp4", SourceStartMs: 0, SourceEndMs: 2000,
		AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusReady, SelectedCandidateID: "native", V3SelectedSource: &source, V3Derivative: &visual, Candidates: []pebblestore.VideoAnimationCandidate{{ID: "native", V3Source: &source}}},
	}}}
	return plan, []pebblestore.ArtifactV3VideoReference{source, still, visual, source, source, visual}
}

// Requirement: native Artifact V3 conversion must authenticate every exact Git
// revision and derivative while remaining a pending Video Studio proposal.
// Threat: stale, foreign, incomplete, or mixed identities could otherwise be
// persisted and later rendered through a weaker legacy authority. This service
// test is the narrowest layer covering authority injection and pre-persistence rejection.
func TestCreateArtifactV3ConversionProposalRejectsForeignProjectOwner(t *testing.T) {
	store := newFakeSessionStore()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: "other", SessionID: "studio"}
	plan, _ := testArtifactV3VideoPlan()
	authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{}}
	svc := NewService(store)
	svc.SetArtifactV3Authority(authority)
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "studio", ProjectID: "project", Intent: pebblestore.VideoEditProposalIntentArtifactV3Convert, Plan: &plan}); err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("foreign project error = %v", err)
	}
	if len(authority.calls) != 0 || store.createProposalCalls != 0 {
		t.Fatalf("foreign project reached V3 validation or persistence: calls=%d creates=%d", len(authority.calls), store.createProposalCalls)
	}
}

func TestCreateArtifactV3ConversionProposalRequiresInjectedAuthority(t *testing.T) {
	store := newFakeSessionStore()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "studio"}
	plan, _ := testArtifactV3VideoPlan()
	svc := NewService(store)
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "studio", ProjectID: "project", Intent: pebblestore.VideoEditProposalIntentArtifactV3Convert, Plan: &plan}); err == nil || !strings.Contains(err.Error(), "authority is not configured") {
		t.Fatalf("missing Artifact V3 authority error = %v", err)
	}
	if store.createProposalCalls != 0 {
		t.Fatalf("missing authority reached proposal mutation: %d", store.createProposalCalls)
	}
}

func TestCreateEditProposalRejectsArtifactV3IdentityWithoutConversionIntent(t *testing.T) {
	store := newFakeSessionStore()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "studio"}
	plan, refs := testArtifactV3VideoPlan()
	authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{}}
	for _, ref := range refs {
		authority.allowed[ref] = true
	}
	svc := NewService(store)
	svc.SetArtifactV3Authority(authority)
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "studio", ProjectID: "project", Intent: pebblestore.VideoEditProposalIntentGeneral, Plan: &plan}); err == nil || (!strings.Contains(err.Error(), "missing its actual visual") && !strings.Contains(err.Error(), "without artifact_v3_conversion intent")) {
		t.Fatalf("wrong-intent V3 error = %v", err)
	}
	if store.createProposalCalls != 0 {
		t.Fatalf("wrong-intent V3 proposal reached persistence: %d", store.createProposalCalls)
	}
}

func TestCreateArtifactV3ConversionProposalValidatesEveryNativeReference(t *testing.T) {
	store := newFakeSessionStore()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "studio"}
	plan, refs := testArtifactV3VideoPlan()
	authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{}}
	for _, ref := range refs {
		authority.allowed[ref] = true
	}
	svc := NewService(store)
	svc.SetArtifactV3Authority(authority)
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "studio", ProjectID: "project", Intent: pebblestore.VideoEditProposalIntentArtifactV3Convert, Plan: &plan}); err != nil {
		t.Fatalf("create native Artifact V3 conversion proposal: %v", err)
	}
	if len(authority.calls) != len(refs) {
		t.Fatalf("validated %d Artifact V3 references, want %d", len(authority.calls), len(refs))
	}
	for index, want := range refs {
		if authority.calls[index] != want {
			t.Fatalf("validated reference %d = %+v, want %+v", index, authority.calls[index], want)
		}
	}
	if store.createProposalCalls != 1 {
		t.Fatalf("create proposal calls = %d, want 1", store.createProposalCalls)
	}
}

func TestCreateArtifactV3ConversionProposalRejectsInvalidAuthorityWithoutMutation(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	cases := []struct {
		name   string
		mutate func(*pebblestore.VideoPlanProposal, []pebblestore.ArtifactV3VideoReference)
		want   string
	}{
		{name: "stale exact reference", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			stale := *plan.Parts[0].ArtifactV3Visual
			stale.DigestSHA256 = strings.Repeat("1", 64)
			plan.Parts[0].ArtifactV3Visual = &stale
			plan.Parts[0].AnimationCandidates.V3Derivative = &stale
		}, want: "stale"},
		{name: "foreign exact reference", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			plan.Parts[0].ArtifactV3Source.SessionID = "foreign"
		}, want: "foreign"},
		{name: "mixed authority", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			plan.Parts[0].Visual = &pebblestore.SessionArtifactSelectionReference{SessionID: "legacy", CollectionID: "legacy", VariantID: "legacy", EventSeq: 1}
		}, want: "exactly one complete render-ready visual authority"},
		{name: "mixed candidate authority", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			plan.Parts[0].AnimationCandidates.Candidates[0].Source = &pebblestore.SessionArtifactSelectionReference{SessionID: "legacy", CollectionID: "legacy", VariantID: "legacy", EventSeq: 1}
		}, want: "exactly one complete"},
		{name: "missing fallback", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			plan.Parts[0].ArtifactV3Still = nil
		}, want: "still requires native Artifact V3 authority"},
		{name: "missing render derivative", mutate: func(plan *pebblestore.VideoPlanProposal, refs []pebblestore.ArtifactV3VideoReference) {
			plan.Parts[0].ArtifactV3Visual = nil
		}, want: "visual requires native Artifact V3 authority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeSessionStore()
			store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "studio"}
			plan, refs := testArtifactV3VideoPlan()
			authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{}}
			for _, ref := range refs {
				authority.allowed[ref] = true
			}
			tc.mutate(&plan, refs)
			svc := NewService(store)
			svc.SetArtifactV3Authority(authority)
			if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "studio", ProjectID: "project", Intent: pebblestore.VideoEditProposalIntentArtifactV3Convert, Plan: &plan}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
			if store.createProposalCalls != 0 || len(store.proposals) != 0 {
				t.Fatalf("rejected conversion changed proposal state: calls=%d proposals=%+v", store.createProposalCalls, store.proposals)
			}
		})
	}
}

func TestValidateTimelineArtifactsRejectsMixedOrStaleArtifactV3Authority(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store := newFakeSessionStore()
	store.sessions["studio"] = pebblestore.SessionSnapshot{ID: "studio", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	plan, _ := testArtifactV3VideoPlan()
	visual := *plan.Parts[0].ArtifactV3Visual
	authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{visual: true}}
	svc := NewService(store)
	svc.SetArtifactV3Authority(authority)
	clip := pebblestore.VideoTimelineClip{ID: "motion", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactV3Ref: &visual, MediaType: "video/mp4", SourceStartMs: 0, SourceEndMs: 2000, DurationMs: 2000}
	if err := svc.validateTimelineArtifacts(principal, "studio", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err != nil {
		t.Fatalf("valid native Artifact V3 clip rejected: %v", err)
	}
	clip.ArtifactRef = &pebblestore.SessionArtifactSelectionReference{SessionID: "legacy", CollectionID: "legacy", VariantID: "legacy", EventSeq: 1}
	if err := svc.validateTimelineArtifacts(principal, "studio", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("mixed authority error = %v", err)
	}
	clip.ArtifactRef = nil
	stale := visual
	stale.DigestSHA256 = strings.Repeat("1", 64)
	clip.ArtifactV3Ref = &stale
	if err := svc.validateTimelineArtifacts(principal, "studio", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale authority error = %v", err)
	}
}

// Requirement: candidate mutations revalidate native V3 source and derivative
// references before entering the durable mutation boundary.
func TestSelectAndPromoteArtifactV3AnimationValidateExactReferencesBeforeMutation(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	plan, _ := testArtifactV3VideoPlan()
	source := *plan.Parts[0].ArtifactV3Source
	visual := *plan.Parts[0].ArtifactV3Visual
	store := newFakeSessionStore()
	store.proposals["proposal"] = pebblestore.VideoEditProposalSnapshot{ID: "proposal", ProjectID: "project", SessionID: "studio", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Intent: pebblestore.VideoEditProposalIntentHTMLIteration}
	authority := &fakeArtifactV3Authority{accountScopeID: principal.AccountScopeID, userID: principal.UserID, allowed: map[pebblestore.ArtifactV3VideoReference]bool{source: true, visual: true}}
	svc := NewService(store)
	svc.SetArtifactV3Authority(authority)
	_, err := svc.SelectAnimationCandidate(context.Background(), principal, SelectAnimationCandidateInput{SessionID: "studio", ProjectID: "project", ProposalID: "proposal", PartID: "motion", CandidateID: "native", V3SelectedSource: &source})
	if err == nil || !strings.Contains(err.Error(), "mutation authority") {
		t.Fatalf("selection did not reach mutation boundary after exact V3 validation: %v", err)
	}
	stale := visual
	stale.DigestSHA256 = strings.Repeat("1", 64)
	callsBefore := len(authority.calls)
	if _, err := svc.PromoteAnimationDerivative(context.Background(), principal, PromoteAnimationDerivativeInput{SessionID: "studio", ProjectID: "project", ProposalID: "proposal", PartID: "motion", CandidateID: "native", V3SelectedSource: &source, V3Derivative: &stale}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale V3 derivative error = %v", err)
	}
	if len(authority.calls) != callsBefore+2 {
		t.Fatalf("stale promotion did not validate source and derivative exactly: calls=%d before=%d", len(authority.calls), callsBefore)
	}
	if len(store.proposals) != 1 {
		t.Fatalf("rejected stale promotion changed proposal state: %+v", store.proposals)
	}
	foreignDerivative := visual
	foreignDerivative.RevisionID = "other-revision"
	authority.allowed[foreignDerivative] = true
	if _, err := svc.PromoteAnimationDerivative(context.Background(), principal, PromoteAnimationDerivativeInput{SessionID: "studio", ProjectID: "project", ProposalID: "proposal", PartID: "motion", CandidateID: "native", V3SelectedSource: &source, V3Derivative: &foreignDerivative}); err == nil || !strings.Contains(err.Error(), "descend") {
		t.Fatalf("foreign derivative lineage error = %v", err)
	}
	if len(store.proposals) != 1 {
		t.Fatalf("rejected foreign derivative changed proposal state: %+v", store.proposals)
	}
}

func TestNormalizeVisualPlanArtifactsRejectsIncompatibleAnimationCandidates(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.sessions["studio"] = pebblestore.SessionSnapshot{ID: "studio", AccountScopeID: "account", UserID: "user"}
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "landscape_video", Width: 1920, Height: 1080}
	profile := &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui"}
	for index, duration := range []int64{10000, 9000} {
		variantID := fmt.Sprintf("candidate-%d", index+1)
		store.artifacts[fmt.Sprintf("account/studio/candidates/%s", variantID)] = pebblestore.SessionArtifactVariant{ID: variantID, CollectionID: "candidates", SessionID: "studio", EventSeq: uint64(index + 1), Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html", OutputRequirements: requirements, AnimationProfile: profile, Parts: []pebblestore.SessionArtifactPart{{ID: "intro", Kind: "temporal", EndMs: duration}}}
	}
	store.artifacts["account/studio/fallback/still"] = pebblestore.SessionArtifactVariant{ID: "still", CollectionID: "fallback", SessionID: "studio", EventSeq: 3, Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png"}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "intro", Title: "Intro", DurationMs: 10000, Visual: &pebblestore.SessionArtifactSelectionReference{SessionID: "studio", CollectionID: "fallback", VariantID: "still", EventSeq: 3}, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection, Candidates: []pebblestore.VideoAnimationCandidate{{ID: "a", Source: &pebblestore.SessionArtifactSelectionReference{SessionID: "studio", CollectionID: "candidates", VariantID: "candidate-1", EventSeq: 1}}, {ID: "b", Source: &pebblestore.SessionArtifactSelectionReference{SessionID: "studio", CollectionID: "candidates", VariantID: "candidate-2", EventSeq: 2}}}}}}}
	if err := svc.normalizeVisualPlanArtifacts(principal, "studio", &plan); err == nil || !strings.Contains(err.Error(), "duration does not match") {
		t.Fatalf("incompatible candidate error = %v", err)
	}
}

func TestListWorkspaceCatalogFiltersScopeAndGroupsRelatedSessions(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	store.sessions["source"] = pebblestore.SessionSnapshot{ID: "source", AccountScopeID: "account", UserID: "user", WorkspacePath: "/workspace/project", Title: "Original"}
	store.sessions["follow-up"] = pebblestore.SessionSnapshot{ID: "follow-up", AccountScopeID: "account", UserID: "user", WorkspacePath: "/workspace/project/../project", Title: "Follow-up"}
	store.sessions["other-workspace"] = pebblestore.SessionSnapshot{ID: "other-workspace", AccountScopeID: "account", UserID: "user", WorkspacePath: "/workspace/other", Title: "Other"}
	store.projects["source-project"] = pebblestore.VideoProjectSnapshot{ID: "source-project", AccountScopeID: "account", UserID: "user", SessionID: "source", Title: "Launch", CurrentRevisionID: "source-revision"}
	store.projects["fork-project"] = pebblestore.VideoProjectSnapshot{ID: "fork-project", AccountScopeID: "account", UserID: "user", SessionID: "follow-up", Title: "Launch", CurrentRevisionID: "fork-revision", Metadata: map[string]any{"video_lineage_root_session_id": "source", "video_lineage_root_project_id": "source-project"}}
	store.projects["excluded-project"] = pebblestore.VideoProjectSnapshot{ID: "excluded-project", AccountScopeID: "account", UserID: "user", SessionID: "other-workspace", Title: "Excluded", CurrentRevisionID: "excluded-revision"}
	store.revisions["source-project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"source-revision": {ID: "source-revision", ProjectID: "source-project", SessionID: "source", AccountScopeID: "account", UserID: "user", RevisionNumber: 1}}
	store.revisions["fork-project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"fork-revision": {ID: "fork-revision", ProjectID: "fork-project", SessionID: "follow-up", AccountScopeID: "account", UserID: "user", RevisionNumber: 1}}

	items, err := svc.ListWorkspaceCatalog(principal, "/workspace/project", 20)
	if err != nil {
		t.Fatalf("list workspace catalog: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("catalog=%+v, want two videos from requested workspace", items)
	}
	for _, item := range items {
		if len(item.RelatedSessions) != 2 {
			t.Fatalf("related sessions=%+v, want source and follow-up", item.RelatedSessions)
		}
	}
}

func TestSameWorkspacePathNormalizesEquivalentPaths(t *testing.T) {
	if !sameWorkspacePath("/workspace/project/../project", "/workspace/project") {
		t.Fatal("expected equivalent workspace paths to match")
	}
	if sameWorkspacePath("/workspace/project", "/workspace/other") {
		t.Fatal("expected distinct workspace paths not to match")
	}
	if sameWorkspacePath("", "/workspace/project") {
		t.Fatal("expected empty workspace path not to match")
	}
}

func TestCreateProjectWithoutTimelineCreatesEmptyBaseRevision(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}

	project, revision, err := svc.CreateProject(context.Background(), principal, CreateProjectInput{
		SessionID: "session", ProjectID: "project", Title: "How to make dubstep music", OutputPreset: pebblestore.VideoPresetLandscape1080p,
	})
	if err != nil {
		t.Fatalf("create project without timeline: %v", err)
	}
	if revision == nil || revision.ID == "" || project.CurrentRevisionID != revision.ID || project.RevisionCount != 1 {
		t.Fatalf("project did not receive exact empty base revision: project=%+v revision=%+v", project, revision)
	}
	if revision.Timeline.OutputPreset != pebblestore.VideoPresetLandscape1080p || len(revision.Timeline.Clips) != 0 || len(revision.Timeline.Transitions) != 0 {
		t.Fatalf("unexpected initial timeline: %+v", revision.Timeline)
	}
}

func TestCreateEditProposalNormalizesAndValidatesVisualPlanReferences(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "session"}
	store.artifacts["acc/session/slides/slide-1"] = pebblestore.SessionArtifactVariant{ID: "slide-1", CollectionID: "slides", SessionID: "session", AccountScopeID: "acc", Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png", EventSeq: 7}
	store.artifacts["acc/session/motion/clip-1"] = pebblestore.SessionArtifactVariant{ID: "clip-1", CollectionID: "motion", SessionID: "session", AccountScopeID: "acc", Status: pebblestore.SessionArtifactStatusReady, MediaType: "video/mp4", EventSeq: 8}
	plan := &pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{
		{ID: "part-1", Title: "Hook", DurationMs: 3000, Visual: &pebblestore.SessionArtifactSelectionReference{CollectionID: "slides", VariantID: "slide-1", EventSeq: 7}},
		{ID: "part-2", Title: "Motion", DurationMs: 2000, SourceStartMs: 500, SourceEndMs: 2500, Visual: &pebblestore.SessionArtifactSelectionReference{CollectionID: "motion", VariantID: "clip-1", EventSeq: 8}},
	}}
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "session", ProjectID: "project", Plan: plan}); err != nil {
		t.Fatalf("create visual plan proposal: %v", err)
	}
	if plan.Parts[0].Visual.SessionID != "session" || plan.Parts[0].VisualMediaType != "image/png" || plan.Parts[1].Visual.SessionID != "session" || plan.Parts[1].VisualMediaType != "video/mp4" {
		t.Fatalf("visual plan references were not normalized: %+v", plan.Parts)
	}
}

func TestStartRenderJobBlocksPendingStoryboardPartsWithActionableCount(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "storyboard"}
	ref := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "stills", VariantID: "opening", EventSeq: 1}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{
		{ID: "opening", DurationMs: 1000, ProductionState: "pending", Visual: ref, StoryboardStill: ref, VisualMediaType: "image/png"},
		{ID: "proof", DurationMs: 1000, ProductionState: "pending", Visual: ref, StoryboardStill: ref, VisualMediaType: "image/png"},
	}}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"storyboard": {ID: "storyboard", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "opening", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000}, {ID: "proof", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": plan}}}}
	_, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "storyboard", JobID: "job"})
	if err == nil || !strings.Contains(err.Error(), "2 storyboard part(s) remain pending") || !strings.Contains(err.Error(), "opening, proof") {
		t.Fatalf("pending storyboard render error = %v", err)
	}
}

func TestStartRenderJobAllowsLegacyPlansWithoutProductionState(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "legacy"}
	ref := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "stills", VariantID: "legacy", EventSeq: 1}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "legacy", DurationMs: 1000, Visual: ref, VisualMediaType: "image/png"}}}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"legacy": {ID: "legacy", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "legacy", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": plan}}}}
	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "legacy", JobID: "job"}); err != nil {
		t.Fatalf("legacy video plan should remain renderable: %v", err)
	}
}

func TestStartRenderJobBlocksPendingWorkingRevision(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "working"}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"working": {
		ID: "working", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user",
		Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 1000, TimelineEndMs: 1000, Visible: true}}},
	}}
	store.proposals["pending"] = pebblestore.VideoEditProposalSnapshot{ID: "pending", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", WorkingRevisionID: "working", Status: pebblestore.VideoEditProposalStatusPending}

	_, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "working", JobID: "job"})
	if err == nil || !strings.Contains(err.Error(), "pending working cut") || !strings.Contains(err.Error(), "confirm or reject") {
		t.Fatalf("pending working revision render error = %v", err)
	}
	if len(store.jobs) != 0 {
		t.Fatalf("blocked pending render created job: %+v", store.jobs)
	}
}

func TestStartRenderJobAllowsConfirmedLockedHTMLAnimation(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "working"}
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallback := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{
		ID: "intro", DurationMs: 1000, Visual: fallback, VisualMediaType: "image/png", AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport, SelectedCandidateID: "a", SelectedSource: htmlRef,
		},
	}}}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"working": {
		ID: "working", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user",
		Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallback, MediaType: "image/png", DurationMs: 1000, SourceEndMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": plan}},
	}}
	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "working", JobID: "job"}); err != nil {
		t.Fatalf("confirmed locked HTML revision should be renderable: %v", err)
	}
}

func TestStartRenderJobRecoversExactLegacyLockedHTMLAuthority(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "accepted"}
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallback := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	unlocked := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "signal", DurationMs: 1000, Visual: fallback, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection, Candidates: []pebblestore.VideoAnimationCandidate{{ID: "orbit", Source: htmlRef}, {ID: "pulse", Source: &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "other", EventSeq: 8}}}}}}}
	locked := unlocked
	locked.Parts = append([]pebblestore.VideoPlanPart(nil), unlocked.Parts...)
	lockedCandidates := *unlocked.Parts[0].AnimationCandidates
	lockedCandidates.SelectedCandidateID = "orbit"
	lockedCandidates.SelectedSource = htmlRef
	lockedCandidates.Status = pebblestore.VideoAnimationCandidateStatusAwaitingExport
	locked.Parts[0].AnimationCandidates = &lockedCandidates
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"accepted": {ID: "accepted", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", CreatedAt: 200, Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "signal", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallback, MediaType: "image/png", DurationMs: 1000, SourceEndMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": unlocked, "accepted_video_plan_proposal_id": "initial-proposal"}}}}
	store.proposals["initial-proposal"] = pebblestore.VideoEditProposalSnapshot{ID: "initial-proposal", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", Plan: &locked, WorkingRevisionID: "working", UpdatedAt: 150}

	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "accepted", JobID: "legacy-job"}); err != nil {
		t.Fatalf("legacy exact proposal selection should remain renderable: %v", err)
	}
}

func TestStartRenderJobDoesNotUseProposalSelectionNewerThanRevision(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "history"}
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallback := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	unlocked := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "signal", DurationMs: 1000, Visual: fallback, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection, Candidates: []pebblestore.VideoAnimationCandidate{{ID: "orbit", Source: htmlRef}, {ID: "pulse", Source: &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "other", EventSeq: 8}}}}}}}
	locked := unlocked
	locked.Parts = append([]pebblestore.VideoPlanPart(nil), unlocked.Parts...)
	lockedCandidates := *unlocked.Parts[0].AnimationCandidates
	lockedCandidates.SelectedCandidateID = "orbit"
	lockedCandidates.SelectedSource = htmlRef
	locked.Parts[0].AnimationCandidates = &lockedCandidates
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"history": {ID: "history", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", CreatedAt: 100, Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "signal", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallback, MediaType: "image/png", DurationMs: 1000, SourceEndMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": unlocked, "accepted_video_plan_proposal_id": "initial-proposal"}}}}
	store.proposals["initial-proposal"] = pebblestore.VideoEditProposalSnapshot{ID: "initial-proposal", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", Plan: &locked, WorkingRevisionID: "working", UpdatedAt: 150}

	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "history", JobID: "history-job"}); err == nil || !strings.Contains(err.Error(), "durably locked") {
		t.Fatalf("newer proposal selection must not rewrite historical render authority: %v", err)
	}
}

func TestStartRenderJobRejectsFailedHTMLAnimation(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "working"}
	ref := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	plan := pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, Visual: ref, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusFailed, SelectedCandidateID: "a", SelectedSource: ref, FailureReason: "animation_seek_unstable"}}}}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"working": {ID: "working", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": plan}}}}
	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "working", JobID: "job"}); err == nil || !strings.Contains(err.Error(), "animation_seek_unstable") {
		t.Fatalf("failed HTML render error = %v", err)
	}
}

func TestValidateTimelineArtifactsRejectsManagedMP4RangeAndMediaMismatches(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.artifacts["acc/session/motion/clip-1"] = pebblestore.SessionArtifactVariant{ID: "clip-1", CollectionID: "motion", SessionID: "session", AccountScopeID: "acc", Status: pebblestore.SessionArtifactStatusReady, MediaType: "video/mp4", EventSeq: 8}
	clip := pebblestore.VideoTimelineClip{
		ID: "motion", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, MediaType: "video/mp4",
		ArtifactRef:   &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "clip-1", EventSeq: 8},
		SourceStartMs: 500, SourceEndMs: 2500, DurationMs: 2000,
	}
	if err := svc.validateTimelineArtifacts(principal, "session", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err != nil {
		t.Fatalf("valid managed MP4 range rejected: %v", err)
	}
	clip.SourceEndMs = 2400
	if err := svc.validateTimelineArtifacts(principal, "session", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err == nil || !strings.Contains(err.Error(), "duration must match") {
		t.Fatalf("expected managed MP4 duration mismatch, got %v", err)
	}
	clip.SourceEndMs, clip.MediaType = 2500, "image/png"
	if err := svc.validateTimelineArtifacts(principal, "session", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err == nil || !strings.Contains(err.Error(), "media_type does not match") {
		t.Fatalf("expected managed artifact media mismatch, got %v", err)
	}
}

func TestValidateTimelineArtifactsRejectsUnownedCrossSessionReference(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.sessions["other"] = pebblestore.SessionSnapshot{ID: "other", AccountScopeID: "acc", UserID: "other-user"}
	store.artifacts["acc/other/slides/slide-1"] = pebblestore.SessionArtifactVariant{ID: "slide-1", CollectionID: "slides", SessionID: "other", AccountScopeID: "acc", Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png", EventSeq: 3}
	clip := pebblestore.VideoTimelineClip{ID: "still", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, MediaType: "image/png", DurationMs: 1000, ArtifactRef: &pebblestore.SessionArtifactSelectionReference{SessionID: "other", CollectionID: "slides", VariantID: "slide-1", EventSeq: 3}}
	if err := svc.validateTimelineArtifacts(principal, "session", pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}}); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected cross-session ownership rejection, got %v", err)
	}
}

func TestVideoprojectServiceWorkflow(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "acc_1",
		UserID:         "usr_1",
	}

	sessionID := "sess_proj_test"
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}

	// Register ready artifact for design input
	store.artifacts[fmt.Sprintf("%s/%s/col_intro/var_intro_v1", principal.AccountScopeID, sessionID)] = pebblestore.SessionArtifactVariant{
		ID:           "var_intro_v1",
		CollectionID: "col_intro",
		Status:       pebblestore.SessionArtifactStatusReady,
		MediaType:    "video/mp4",
		EventSeq:     1,
	}

	// 1. Create project with design input clip
	ctx := context.Background()
	project, rev, err := svc.CreateProject(ctx, principal, CreateProjectInput{
		SessionID:    sessionID,
		ProjectID:    "vproj_1",
		Title:        "My Workflow Video",
		OutputPreset: pebblestore.VideoPresetLandscape1080p,
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "clip_intro",
					Track:      0,
					Sequence:   0,
					SourceKind: pebblestore.VideoClipSourceKindManagedArtifact,
					ArtifactRef: &pebblestore.SessionArtifactSelectionReference{
						SessionID:    sessionID,
						CollectionID: "col_intro",
						VariantID:    "var_intro_v1",
						EventSeq:     1,
					},
					SourceStartMs:   0,
					SourceEndMs:     4000,
					DurationMs:      4000,
					TimelineStartMs: 0,
					TimelineEndMs:   4000,
					Visible:         true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create project failed: %v", err)
	}
	if project.ID != "vproj_1" || rev == nil || rev.RevisionNumber != 1 {
		t.Fatalf("unexpected project or initial rev: %+v, %+v", project, rev)
	}

	// 2. Create Revision 2
	rev2, updatedProj, err := svc.CreateRevision(ctx, principal, CreateRevisionInput{
		SessionID:       sessionID,
		ProjectID:       project.ID,
		RevisionID:      "vrev_2",
		Description:     "Added voiceover track",
		ChangeSummary:   "Adjusted audio policy and clip volume",
		AuthorPrincipal: "swarm",
		Timeline: pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:              "clip_intro",
					Track:           0,
					Sequence:        0,
					SourceKind:      pebblestore.VideoClipSourceKindManagedArtifact,
					DurationMs:      4000,
					TimelineStartMs: 0,
					TimelineEndMs:   4000,
					Visible:         true,
					Volume:          0.5,
				},
			},
			AudioPolicy: &pebblestore.VideoAudioPolicy{
				MasterVolume: 1.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("create revision 2 failed: %v", err)
	}
	if rev2.RevisionNumber != 2 || rev2.ParentRevisionID != rev.ID || updatedProj.CurrentRevisionNumber != 2 {
		t.Fatalf("revision 2 lineage mismatch: %+v", rev2)
	}

	restored, restoredProject, err := svc.RestoreRevision(ctx, principal, RestoreRevisionInput{
		SessionID: sessionID, ProjectID: project.ID, SourceRevisionID: rev.ID, RevisionID: "vrev_restore", AuthorPrincipal: "user",
	})
	if err != nil {
		t.Fatalf("restore revision failed: %v", err)
	}
	if restored.ParentRevisionID != rev2.ID || restored.RestoredFromRevisionID != rev.ID || restored.Timeline.Clips[0].Volume != rev.Timeline.Clips[0].Volume || restoredProject.CurrentRevisionID != restored.ID {
		t.Fatalf("restore lineage mismatch: %+v", restored)
	}

	store.sessions["destination"] = pebblestore.SessionSnapshot{ID: "destination", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: "/workspace"}
	forked, forkedRevision, err := svc.ForkRevision(ctx, principal, ForkRevisionInput{SourceSessionID: sessionID, SourceProjectID: project.ID, SourceRevisionID: rev.ID, DestinationSessionID: "destination", ProjectID: "forked"})
	if err != nil {
		t.Fatalf("fork revision failed: %v", err)
	}
	if forkedRevision == nil || forkedRevision.Timeline.Clips[0].Volume != rev.Timeline.Clips[0].Volume || forked.Metadata["source_revision_id"] != rev.ID {
		t.Fatalf("fork did not preserve exact source and lineage: project=%+v revision=%+v", forked, forkedRevision)
	}
	if source, ok := store.projects[project.ID]; !ok || source.CurrentRevisionID != restored.ID {
		t.Fatalf("fork mutated source project: %+v", source)
	}

	// 3. Start render job
	job, err := svc.StartRenderJob(ctx, principal, StartRenderJobInput{
		SessionID: sessionID,
		ProjectID: project.ID,
		JobID:     "vren_1",
	})
	if err != nil {
		t.Fatalf("start render job failed: %v", err)
	}
	if job.Status != pebblestore.VideoRenderJobStatusQueued {
		t.Fatalf("expected queued status, got %s", job.Status)
	}

	// 4. Update progress
	job, err = svc.UpdateRenderProgress(ctx, principal, sessionID, job.ID, 0.5, 400)
	if err != nil || job.Progress != 0.5 || job.Status != pebblestore.VideoRenderJobStatusRendering {
		t.Fatalf("update progress unexpected: %+v, err=%v", job, err)
	}

	// 5. Complete render job
	renderedRef := &pebblestore.SessionArtifactSelectionReference{
		SessionID:    sessionID,
		CollectionID: "col_renders",
		VariantID:    "var_final_mp4",
		Action:       "use",
	}
	job, err = svc.CompleteRenderJob(ctx, principal, CompleteRenderJobInput{
		SessionID:          sessionID,
		JobID:              job.ID,
		OutputPreset:       pebblestore.VideoPresetLandscape1080p,
		OutputWidth:        1920,
		OutputHeight:       1080,
		OutputFPS:          30.0,
		OutputDurationMs:   4000,
		OutputSizeBytes:    2048000,
		OutputDigestSHA256: strings.Repeat("d", 64),
		OutputArtifact:     renderedRef,
		NowUnixMs:          500,
	})
	if err != nil || job.Status != pebblestore.VideoRenderJobStatusReady || job.OutputArtifact == nil {
		t.Fatalf("complete render job unexpected: %+v, err=%v", job, err)
	}
}
