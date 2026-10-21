package videorender

import (
	"context"

	"errors"
	"fmt"
	"os"

	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type fakeCommandRunner struct {
	lookPathErr error
	runHook     func(ctx context.Context, name string, args ...string) ([]byte, error)
	calls       [][]string
}

func (f *fakeCommandRunner) LookPath(file string) (string, error) {
	if f.lookPathErr != nil {
		return "", f.lookPathErr
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeCommandRunner) RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	full := append([]string{name}, args...)
	f.calls = append(f.calls, full)
	if f.runHook != nil {
		return f.runHook(ctx, name, args...)
	}
	// By default simulate ffmpeg creating output file if it's the last arg
	if len(args) > 0 {
		outPath := args[len(args)-1]
		if strings.HasSuffix(outPath, ".mp4") {
			_ = os.WriteFile(outPath, []byte("fake valid mp4 output"), 0o600)
		}
	}
	return []byte("ok"), nil
}

type fakeSessionStore struct {
	sessions      map[string]pebblestore.SessionSnapshot
	projects      map[string]pebblestore.VideoProjectSnapshot
	revisions     map[string]pebblestore.VideoProjectRevisionSnapshot
	jobs          map[string]pebblestore.VideoRenderJobSnapshot
	proposals     map[string]pebblestore.VideoEditProposalSnapshot
	sources       map[string]pebblestore.VideoSourceRecord
	audioSources  map[string]pebblestore.AudioSourceRecord
	variants      map[string]pebblestore.SessionArtifactVariant
	jobUpdates    []pebblestore.UpdateVideoRenderJobInput
	updateJobHook func(input pebblestore.UpdateVideoRenderJobInput)
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions:     make(map[string]pebblestore.SessionSnapshot),
		projects:     make(map[string]pebblestore.VideoProjectSnapshot),
		revisions:    make(map[string]pebblestore.VideoProjectRevisionSnapshot),
		jobs:         make(map[string]pebblestore.VideoRenderJobSnapshot),
		proposals:    make(map[string]pebblestore.VideoEditProposalSnapshot),
		sources:      make(map[string]pebblestore.VideoSourceRecord),
		audioSources: make(map[string]pebblestore.AudioSourceRecord),
		variants:     make(map[string]pebblestore.SessionArtifactVariant),
	}
}

func (f *fakeSessionStore) GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	s, ok := f.sessions[sessionID]
	return s, ok, nil
}

func (f *fakeSessionStore) GetVideoProject(accountScopeID, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error) {
	p, ok := f.projects[projectID]
	if !ok || p.AccountScopeID != accountScopeID || p.SessionID != sessionID {
		return pebblestore.VideoProjectSnapshot{}, false, nil
	}
	return p, true, nil
}

func (f *fakeSessionStore) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	r, ok := f.revisions[revisionID]
	if !ok || r.AccountScopeID != accountScopeID || r.SessionID != sessionID || r.ProjectID != projectID {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
	}
	return r, true, nil
}

func (f *fakeSessionStore) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	proposal, ok := f.proposals[proposalID]
	if !ok || proposal.AccountScopeID != accountScopeID || proposal.SessionID != sessionID || proposal.ProjectID != projectID {
		return pebblestore.VideoEditProposalSnapshot{}, false, nil
	}
	return proposal, true, nil
}

func (f *fakeSessionStore) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	proposals := make([]pebblestore.VideoEditProposalSnapshot, 0, len(f.proposals))
	for _, proposal := range f.proposals {
		if proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			proposals = append(proposals, proposal)
		}
	}
	if limit > 0 && len(proposals) > limit {
		proposals = proposals[:limit]
	}
	return proposals, nil
}

func (f *fakeSessionStore) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	j, ok := f.jobs[jobID]
	if !ok || j.AccountScopeID != accountScopeID || j.SessionID != sessionID {
		return pebblestore.VideoRenderJobSnapshot{}, false, nil
	}
	return j, true, nil
}

func (f *fakeSessionStore) UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	f.jobUpdates = append(f.jobUpdates, input)
	if f.updateJobHook != nil {
		f.updateJobHook(input)
	}
	j, ok := f.jobs[input.JobID]
	if !ok {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("job not found")
	}
	if input.ExpectedStatus != "" && j.Status != input.ExpectedStatus {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("render job status conflict: expected %s, actual %s", input.ExpectedStatus, j.Status)
	}
	if input.Status != "" {
		j.Status = input.Status
	}
	if input.Progress > j.Progress {
		j.Progress = input.Progress
	}
	if input.ProgressStage != "" {
		j.ProgressStage = input.ProgressStage
	}
	if input.FailureCode != "" {
		j.FailureCode = input.FailureCode
	}
	if input.FailureReason != "" {
		j.FailureReason = input.FailureReason
	}
	if input.OutputPreset != "" {
		j.OutputPreset = input.OutputPreset
	}
	if input.OutputWidth > 0 {
		j.OutputWidth = input.OutputWidth
	}
	if input.OutputHeight > 0 {
		j.OutputHeight = input.OutputHeight
	}
	if input.OutputFPS > 0 {
		j.OutputFPS = input.OutputFPS
	}
	if input.OutputDurationMs > 0 {
		j.OutputDurationMs = input.OutputDurationMs
	}
	if input.OutputSizeBytes > 0 {
		j.OutputSizeBytes = input.OutputSizeBytes
	}
	if input.OutputDigestSHA256 != "" {
		j.OutputDigestSHA256 = input.OutputDigestSHA256
	}
	if input.OutputArtifact != nil {
		j.OutputArtifact = input.OutputArtifact
	}
	j.UpdatedAt = input.NowUnixMs
	f.jobs[input.JobID] = j
	return j, nil
}

func (f *fakeSessionStore) GetVideoSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.VideoSourceRecord, bool, error) {
	s, ok := f.sources[ref]
	if !ok || s.AccountScopeID != accountScopeID {
		return pebblestore.VideoSourceRecord{}, false, nil
	}
	return s, true, nil
}

func (f *fakeSessionStore) GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.AudioSourceRecord, bool, error) {
	s, ok := f.audioSources[ref]
	if !ok || s.AccountScopeID != accountScopeID || s.WorkspaceID != workspaceID {
		return pebblestore.AudioSourceRecord{}, false, nil
	}
	return s, true, nil
}

func (f *fakeSessionStore) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", accountScopeID, sessionID, collectionID, variantID)
	v, ok := f.variants[key]
	return v, ok, nil
}

func (f *fakeSessionStore) ListRecoverableVideoRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	var list []pebblestore.VideoRenderJobSnapshot
	for _, job := range f.jobs {
		if job.Status == pebblestore.VideoRenderJobStatusQueued || job.Status == pebblestore.VideoRenderJobStatusRendering {
			list = append(list, job)
			if len(list) == limit {
				break
			}
		}
	}
	return list, nil
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

type fakeArtifactAuthority struct {
	createdVariants []pebblestore.SessionArtifactVariant
	createInputs    []artifact.CreateFileInput
	createErr       error
	body            []byte
	readVariant     pebblestore.SessionArtifactVariant
	readVariants    map[string]pebblestore.SessionArtifactVariant
	readRefs        []pebblestore.SessionArtifactSelectionReference
}

type fakeArtifactV3Authority struct {
	allowed       map[pebblestore.ArtifactV3VideoReference][]byte
	validateErr   error
	readErr       error
	validateCalls []pebblestore.ArtifactV3VideoReference
	readCalls     []pebblestore.ArtifactV3VideoReference
}

func (f *fakeArtifactV3Authority) ValidateVideoReference(accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) error {
	f.validateCalls = append(f.validateCalls, ref)
	if accountScopeID != "acc" || userID != "user" {
		return errors.New("owner mismatch")
	}
	if f.validateErr != nil {
		return f.validateErr
	}
	if _, ok := f.allowed[ref]; !ok {
		return errors.New("stale native V3 reference")
	}
	return nil
}

func (f *fakeArtifactV3Authority) ReadVideoReference(_ context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	f.readCalls = append(f.readCalls, ref)
	if accountScopeID != "acc" || userID != "user" {
		return nil, errors.New("owner mismatch")
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	body, ok := f.allowed[ref]
	if !ok {
		return nil, errors.New("missing native V3 derivative")
	}
	return append([]byte(nil), body...), nil
}

type fakeAnimationRenderer struct {
	requests    []htmlcapture.AnimationRequest
	result      htmlcapture.AnimationResult
	err         error
	afterRender func()
}

func (f *fakeAnimationRenderer) RenderAnimation(_ context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	f.requests = append(f.requests, req)
	if req.Progress != nil && f.result.FrameCount > 0 {
		req.Progress(htmlcapture.AnimationProgress{Stage: "frame_capture", Completed: f.result.FrameCount, Total: f.result.FrameCount})
	}
	if f.afterRender != nil {
		f.afterRender()
	}
	return f.result, f.err
}

func (f *fakeArtifactAuthority) GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	if exact, ok := f.readVariants[ref.SessionID+"/"+ref.CollectionID+"/"+ref.VariantID]; ok {
		return exact, nil
	}
	if f.readVariant.ID != "" {
		return f.readVariant, nil
	}
	return pebblestore.SessionArtifactVariant{
		ID:           ref.VariantID,
		CollectionID: ref.CollectionID,
		Status:       pebblestore.SessionArtifactStatusReady,
	}, nil
}

func (f *fakeArtifactAuthority) ReadReference(_ context.Context, _ artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.readRefs = append(f.readRefs, ref)
	if f.body != nil {
		variant := f.readVariant
		if exact, ok := f.readVariants[ref.SessionID+"/"+ref.CollectionID+"/"+ref.VariantID]; ok {
			variant = exact
		}
		return append([]byte(nil), f.body...), variant, nil
	}
	return []byte("fake artifact content"), pebblestore.SessionArtifactVariant{}, nil
}

func (f *fakeArtifactAuthority) CreateFromFile(ctx context.Context, principal artifact.Principal, input artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	f.createInputs = append(f.createInputs, input)
	if f.createErr != nil {
		return pebblestore.SessionArtifactVariant{}, f.createErr
	}
	v := pebblestore.SessionArtifactVariant{
		ID:           input.VariantID,
		CollectionID: input.CollectionID,
		Filename:     input.Filename,
		MediaType:    input.MediaType,
		Status:       pebblestore.SessionArtifactStatusReady,
		Presentation: input.Presentation,
		EventSeq:     42,
		Size:         1024,
	}
	f.createdVariants = append(f.createdVariants, v)
	return v, nil
}

type fakeWorkspaceAuthority struct {
	resolution workspaceruntime.Resolution
}

func (f fakeWorkspaceAuthority) ListSourceMediaDirectoriesForPrincipal(identity.Principal, string) (workspaceruntime.Resolution, error) {
	return f.resolution, nil
}

func TestRenderJobSecurityAndRejections(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_alpha", UserID: "usr_alpha"}
	otherPrincipal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_beta", UserID: "usr_beta"}

	store := newFakeSessionStore()
	store.sessions["sess_alpha"] = pebblestore.SessionSnapshot{
		ID:             "sess_alpha",
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_1",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_alpha",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	runner := &fakeCommandRunner{}
	artAuth := &fakeArtifactAuthority{}
	svc := NewService(Config{}, store, artAuth, nil, nil, runner)
	ctx := context.Background()

	// 1. Cross-account rejected
	_, err := svc.RenderJob(ctx, otherPrincipal, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected ownership rejection for other principal, got: %v", err)
	}

	// 2. Missing principal
	_, err = svc.RenderJob(ctx, identity.Principal{}, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated principal") {
		t.Fatalf("expected authenticated principal required, got: %v", err)
	}

	// 3. Nonexistent job
	_, err = svc.RenderJob(ctx, principal, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "nonexistent_job",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found for nonexistent job, got: %v", err)
	}
}
