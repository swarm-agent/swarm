package videoproject

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeSessionStore struct {
	sessions  map[string]pebblestore.SessionSnapshot
	projects  map[string]pebblestore.VideoProjectSnapshot
	revisions map[string]map[string]pebblestore.VideoProjectRevisionSnapshot
	jobs      map[string]pebblestore.VideoRenderJobSnapshot
	artifacts map[string]pebblestore.SessionArtifactVariant
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions:  make(map[string]pebblestore.SessionSnapshot),
		projects:  make(map[string]pebblestore.VideoProjectSnapshot),
		revisions: make(map[string]map[string]pebblestore.VideoProjectRevisionSnapshot),
		jobs:      make(map[string]pebblestore.VideoRenderJobSnapshot),
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
		CurrentRevisionID:     "vrev_1",
		CurrentRevisionNumber: 1,
		RevisionCount:         1,
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
	return pebblestore.VideoEditProposalSnapshot{}, nil
}
func (f *fakeSessionStore) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	return pebblestore.VideoEditProposalSnapshot{}, false, nil
}
func (f *fakeSessionStore) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	return nil, nil
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

func TestCreateEditProposalNormalizesAndValidatesVisualPlanReferences(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "session"}
	store.artifacts["acc/session/slides/slide-1"] = pebblestore.SessionArtifactVariant{ID: "slide-1", CollectionID: "slides", SessionID: "session", AccountScopeID: "acc", Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png", EventSeq: 7}
	plan := &pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{ID: "part-1", Title: "Hook", DurationMs: 3000, Visual: &pebblestore.SessionArtifactSelectionReference{CollectionID: "slides", VariantID: "slide-1", EventSeq: 7}}}}
	if _, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{SessionID: "session", ProjectID: "project", Plan: plan}); err != nil {
		t.Fatalf("create visual plan proposal: %v", err)
	}
	if plan.Parts[0].Visual.SessionID != "session" || plan.Parts[0].VisualMediaType != "image/png" {
		t.Fatalf("visual plan reference was not normalized: %+v", plan.Parts[0])
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
