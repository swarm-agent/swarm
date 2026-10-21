package videoproject

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
	if input.InitialProposal != nil {
		if rev == nil || input.InitialTimeline == nil {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("initial edit proposal requires initial revision timeline")
		}
		if input.InitialProposal.Status != pebblestore.VideoEditProposalStatusAccepted {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("initial edit proposal status must be accepted")
		}
		proposalID := pebblestore.VideoPlanRenderAuthorityProposalID(*input.InitialTimeline)
		if proposalID == "" || proposalID != input.InitialProposal.ID {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("initial revision timeline does not carry matching accepted_video_plan_proposal_id")
		}
		if input.InitialProposal.Plan == nil {
			return pebblestore.VideoProjectSnapshot{}, nil, errors.New("initial edit proposal requires plan")
		}
		if err := pebblestore.ValidateVideoPlanProposal(*input.InitialProposal.Plan); err != nil {
			return pebblestore.VideoProjectSnapshot{}, nil, fmt.Errorf("initial edit proposal plan invalid: %w", err)
		}
		prop := *input.InitialProposal
		prop.AccountScopeID = input.AccountScopeID
		prop.UserID = input.UserID
		prop.SessionID = input.SessionID
		prop.ProjectID = p.ID
		prop.SchemaVersion = 1
		prop.Status = pebblestore.VideoEditProposalStatusAccepted
		if rev != nil {
			prop.WorkingRevisionID = rev.ID
			prop.WorkingRevisionNumber = rev.RevisionNumber
			prop.AcceptedRevisionID = rev.ID
			prop.BaseRevisionID = rev.ID
			prop.BaseRevisionNumber = rev.RevisionNumber
		}
		f.proposals[prop.SessionID+":"+prop.ProjectID+":"+prop.ID] = prop
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
	key := sessionID + ":" + projectID + ":" + proposalID
	if proposal, ok := f.proposals[key]; ok {
		if proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			return proposal, true, nil
		}
	}
	if proposal, ok := f.proposals[proposalID]; ok {
		if proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			return proposal, true, nil
		}
	}
	for _, proposal := range f.proposals {
		if proposal.ID == proposalID && proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			return proposal, true, nil
		}
	}
	return pebblestore.VideoEditProposalSnapshot{}, false, nil
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
