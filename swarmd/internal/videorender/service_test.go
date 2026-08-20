package videorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	sources       map[string]pebblestore.VideoSourceRecord
	variants      map[string]pebblestore.SessionArtifactVariant
	updateJobHook func(input pebblestore.UpdateVideoRenderJobInput)
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions:  make(map[string]pebblestore.SessionSnapshot),
		projects:  make(map[string]pebblestore.VideoProjectSnapshot),
		revisions: make(map[string]pebblestore.VideoProjectRevisionSnapshot),
		jobs:      make(map[string]pebblestore.VideoRenderJobSnapshot),
		sources:   make(map[string]pebblestore.VideoSourceRecord),
		variants:  make(map[string]pebblestore.SessionArtifactVariant),
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

func (f *fakeSessionStore) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	j, ok := f.jobs[jobID]
	if !ok || j.AccountScopeID != accountScopeID || j.SessionID != sessionID {
		return pebblestore.VideoRenderJobSnapshot{}, false, nil
	}
	return j, true, nil
}

func (f *fakeSessionStore) UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
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
	if input.Progress > 0 {
		j.Progress = input.Progress
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
	createErr       error
}

func (f *fakeArtifactAuthority) GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	return pebblestore.SessionArtifactVariant{
		ID:           ref.VariantID,
		CollectionID: ref.CollectionID,
		Status:       pebblestore.SessionArtifactStatusReady,
	}, nil
}

func (f *fakeArtifactAuthority) CreateFromFile(ctx context.Context, principal artifact.Principal, input artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
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

func testVideoFingerprint(root, relative string, size, modAt int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", root, relative, size, modAt)))
	return hex.EncodeToString(sum[:])
}

func TestRenderJobSuccessfulFlow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "swarm-render-test-")
	if err != nil {
		t.Fatalf("temp dir err: %v", err)
	}
	defer os.RemoveAll(tempDir)

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "acc_1",
		UserID:         "usr_1",
	}
	sessionID := "sess_1"
	projectID := "vproj_1"
	revID := "vrev_1"
	jobID := "vjob_1"

	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Metadata: map[string]any{
			"workspace_id": "ws_1",
		},
	}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{
		ID:                projectID,
		AccountScopeID:    principal.AccountScopeID,
		UserID:            principal.UserID,
		SessionID:         sessionID,
		Title:             "Test Intro Video",
		CurrentRevisionID: revID,
	}

	// Create a dummy video file in tempDir
	srcFilePath := filepath.Join(tempDir, "source.mp4")
	_ = os.WriteFile(srcFilePath, []byte("ftypisomfakevideodata"), 0o600)
	srcStat, _ := os.Stat(srcFilePath)

	srcRef := "videosrc_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	store.sources[srcRef] = pebblestore.VideoSourceRecord{
		Ref:               srcRef,
		AccountScopeID:    principal.AccountScopeID,
		WorkspaceID:       "ws_1",
		RootPath:          tempDir,
		RelativePath:      "source.mp4",
		DisplayName:       "source.mp4",
		MIMEType:          "video/mp4",
		SizeBytes:         srcStat.Size(),
		ModifiedAt:        srcStat.ModTime().UnixMilli(),
		SourceFingerprint: testVideoFingerprint(tempDir, "source.mp4", srcStat.Size(), srcStat.ModTime().UnixMilli()),
	}

	// Managed artifact variant
	artVariantKey := fmt.Sprintf("%s/%s/col_intro/var_intro_1", principal.AccountScopeID, sessionID)
	store.variants[artVariantKey] = pebblestore.SessionArtifactVariant{
		ID:           "var_intro_1",
		CollectionID: "col_intro",
		Filename:     "intro_card.png",
		MediaType:    "image/png",
		Status:       pebblestore.SessionArtifactStatusReady,
		EventSeq:     7,
		Size:         500,
	}

	store.revisions[revID] = pebblestore.VideoProjectRevisionSnapshot{
		ID:             revID,
		ProjectID:      projectID,
		RevisionNumber: 1,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		Timeline: pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			FPS:          30,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "clip_art",
					Sequence:   0,
					SourceKind: pebblestore.VideoClipSourceKindManagedArtifact,
					ArtifactRef: &pebblestore.SessionArtifactSelectionReference{
						SessionID:    sessionID,
						CollectionID: "col_intro",
						VariantID:    "var_intro_1",
						EventSeq:     7,
					},
					DurationMs: 2000,
					Visible:    true,
				},
				{
					ID:         "clip_src",
					Sequence:   1,
					SourceKind: pebblestore.VideoClipSourceKindSourceVideo,
					SourceRef:  srcRef,
					DurationMs: 4000,
					Visible:    true,
					Captions: []pebblestore.VideoTextOverlay{
						{
							Text:     "First chapter",
							Position: "bottom",
						},
					},
				},
			},
		},
	}

	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{
		ID:             jobID,
		ProjectID:      projectID,
		RevisionID:     revID,
		RevisionNumber: 1,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	runner := &fakeCommandRunner{}
	artAuth := &fakeArtifactAuthority{}

	svc := NewService(Config{}, store, artAuth, nil, nil, runner)

	ctx := context.Background()
	result, err := svc.RenderJob(ctx, principal, RenderJobRequest{
		SessionID:  sessionID,
		ProjectID:  projectID,
		RevisionID: revID,
		JobID:      jobID,
	})
	if err != nil {
		t.Fatalf("render job failed: %v", err)
	}

	if result.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("expected job status ready, got: %s", result.Status)
	}
	if result.Progress != 1.0 {
		t.Fatalf("expected progress 1.0, got: %f", result.Progress)
	}
	if result.OutputWidth != 1920 || result.OutputHeight != 1080 {
		t.Fatalf("expected 1920x1080 output dimensions, got %dx%d", result.OutputWidth, result.OutputHeight)
	}
	if result.OutputArtifact == nil || result.OutputArtifact.VariantID == "" {
		t.Fatalf("expected non-nil output artifact reference, got: %+v", result.OutputArtifact)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 ffmpeg call, got: %d", len(runner.calls))
	}
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

func TestRenderJobRejectsRevisionDifferentFromPinnedJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "sess_1", CurrentRevisionID: "rev_new"}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_pinned", Status: pebblestore.VideoRenderJobStatusQueued}

	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, &fakeCommandRunner{})
	_, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{
		SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_new", JobID: "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), `pinned to revision "rev_pinned"`) {
		t.Fatalf("RenderJob() error = %v, want pinned revision rejection", err)
	}
	if got := store.jobs["job_1"].Status; got != pebblestore.VideoRenderJobStatusQueued {
		t.Fatalf("job status = %s, want queued after rejected revision override", got)
	}
}

func TestRenderJobFailureHandling(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	sessionID := "sess_fail"
	projectID := "proj_fail"
	revID := "rev_fail"
	jobID := "job_fail"

	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{
		ID:                projectID,
		AccountScopeID:    principal.AccountScopeID,
		SessionID:         sessionID,
		CurrentRevisionID: revID,
	}
	store.revisions[revID] = pebblestore.VideoProjectRevisionSnapshot{
		ID:             revID,
		ProjectID:      projectID,
		AccountScopeID: principal.AccountScopeID,
		SessionID:      sessionID,
		Timeline: pebblestore.VideoProjectTimeline{
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "c1",
					SourceKind: pebblestore.VideoClipSourceKindColor,
					DurationMs: 1000,
					Visible:    true,
				},
			},
		},
	}
	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{
		ID:             jobID,
		ProjectID:      projectID,
		RevisionID:     revID,
		AccountScopeID: principal.AccountScopeID,
		SessionID:      sessionID,
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	// Command runner fails
	runner := &fakeCommandRunner{
		runHook: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("simulated ffmpeg error: invalid codec")
		},
	}
	artAuth := &fakeArtifactAuthority{}
	svc := NewService(Config{}, store, artAuth, nil, nil, runner)

	_, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{
		SessionID:  sessionID,
		ProjectID:  projectID,
		RevisionID: revID,
		JobID:      jobID,
	})
	if err == nil {
		t.Fatalf("expected error from failed ffmpeg run")
	}

	updatedJob, _, _ := store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if updatedJob.Status != pebblestore.VideoRenderJobStatusFailed {
		t.Fatalf("expected job status failed, got: %s", updatedJob.Status)
	}
	if updatedJob.FailureCode != "ffmpeg_execution_error" {
		t.Fatalf("expected ffmpeg_execution_error failure code, got: %s", updatedJob.FailureCode)
	}
}

func TestRenderJobDaemonInterruptionRequeuesPinnedJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: "acc_1", UserID: "usr_1"}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", CurrentRevisionID: "rev_1"}
	store.revisions["rev_1"] = pebblestore.VideoProjectRevisionSnapshot{
		ID: "rev_1", ProjectID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1",
		Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{
			ID: "clip", SourceKind: pebblestore.VideoClipSourceKindColor,
			DurationMs: 1000, TimelineEndMs: 1000, Visible: true,
		}}},
	}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", Status: pebblestore.VideoRenderJobStatusQueued}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeCommandRunner{runHook: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, runner)
	if _, err := svc.RenderJob(ctx, principal, RenderJobRequest{SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", JobID: "job_1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderJob() error = %v, want context canceled", err)
	}
	job := store.jobs["job_1"]
	if job.Status != pebblestore.VideoRenderJobStatusQueued || job.FailureCode != "" {
		t.Fatalf("interrupted job = status %s failure %s, want durable queued retry", job.Status, job.FailureCode)
	}
}

func TestRecoverJobsResumesPinnedRevisionAndWorkspace(t *testing.T) {
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: "acc_1", UserID: "usr_1", WorkspacePath: "/trusted/workspace"}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", CurrentRevisionID: "rev_new"}
	store.revisions["rev_old"] = pebblestore.VideoProjectRevisionSnapshot{ID: "rev_old", ProjectID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "clip", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 1000, Visible: true}}}}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_old", Status: pebblestore.VideoRenderJobStatusRendering}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, &fakeCommandRunner{})
	count, err := svc.RecoverJobs(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RecoverJobs() = %d, %v", count, err)
	}
	job := store.jobs["job_1"]
	if job.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("status = %s, want ready", job.Status)
	}
}

func TestRecoverJobsPreservesExistingReadyArtifact(t *testing.T) {
	store := newFakeSessionStore()
	job := pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", Status: pebblestore.VideoRenderJobStatusRendering}
	store.jobs[job.ID] = job
	store.variants["acc_1/sess_1/vproj_proj_1/vrender_job_1"] = pebblestore.SessionArtifactVariant{ID: "vrender_job_1", CollectionID: "vproj_proj_1", SessionID: "sess_1", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 9, Size: 44}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{}, store, nil, nil, nil, runner)
	if count, err := svc.RecoverJobs(context.Background()); err != nil || count != 1 {
		t.Fatalf("RecoverJobs() = %d, %v", count, err)
	}
	if store.jobs[job.ID].Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatal("artifact recovery did not complete")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("ffmpeg calls = %d, want 0", len(runner.calls))
	}
	if store.jobs[job.ID].OutputArtifact == nil {
		t.Fatal("ready artifact reference was not restored")
	}
}

func TestRenderJobProcessAdmissionRejectsDuplicate(t *testing.T) {
	svc := NewService(Config{}, newFakeSessionStore(), nil, nil, nil, nil)
	if !svc.admit("job") {
		t.Fatal("first admission failed")
	}
	if svc.admit("job") {
		t.Fatal("duplicate admission succeeded")
	}
	svc.release("job")
	if !svc.admit("job") {
		t.Fatal("admission after release failed")
	}
	svc.release("job")
}

func TestReconcileInterruptedJobs(t *testing.T) {
	store := newFakeSessionStore()
	store.jobs["job_rendering_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_rendering_1",
		AccountScopeID: "acc_1",
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusRendering,
	}
	store.jobs["job_ready_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_ready_1",
		AccountScopeID: "acc_1",
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusReady,
	}

	svc := NewService(Config{}, store, nil, nil, nil, nil)
	reconciled, err := svc.ReconcileInterruptedJobs(context.Background(), "acc_1", "sess_1", "proj_1")
	if err != nil {
		t.Fatalf("reconcile err: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("expected 1 reconciled job, got %d", reconciled)
	}

	job1, _, _ := store.GetVideoRenderJob("acc_1", "sess_1", "job_rendering_1")
	if job1.Status != pebblestore.VideoRenderJobStatusFailed || job1.FailureCode != "recovery_metadata_invalid" {
		t.Fatalf("expected invalid legacy job to fail recovery, got status=%s code=%s", job1.Status, job1.FailureCode)
	}

	jobReady, _, _ := store.GetVideoRenderJob("acc_1", "sess_1", "job_ready_1")
	if jobReady.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("expected job_ready_1 to remain ready, got: %s", jobReady.Status)
	}
}

func TestStartRenderJobQueueGraceAllowsImmediateCancellation(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.jobs["job_cancel"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_cancel",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{}, store, nil, nil, nil, runner)

	svc.StartRenderJob(principal, RenderJobRequest{
		SessionID:  "sess_1",
		ProjectID:  "proj_1",
		JobID:      "job_cancel",
		QueueGrace: time.Second,
	})
	job, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_cancel")
	if err != nil {
		t.Fatalf("CancelRenderJob() error = %v", err)
	}
	if job.Status != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("CancelRenderJob() status = %s, want cancelled", job.Status)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := svc.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("WaitForIdle() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("render command calls = %d, want 0", len(runner.calls))
	}
	if got := store.jobs["job_cancel"].Status; got != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("durable status = %s, want cancelled", got)
	}
}

func TestCancelRenderJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.jobs["job_cancel"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_cancel",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusRendering,
	}

	svc := NewService(Config{}, store, nil, nil, nil, nil)
	job, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_cancel")
	if err != nil {
		t.Fatalf("cancel err: %v", err)
	}
	if job.Status != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("expected cancelled status, got: %s", job.Status)
	}
}

func TestCancelRenderJobRejectsTerminalStates(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	for _, status := range []string{
		pebblestore.VideoRenderJobStatusReady,
		pebblestore.VideoRenderJobStatusFailed,
	} {
		t.Run(status, func(t *testing.T) {
			store := newFakeSessionStore()
			store.jobs["job_terminal"] = pebblestore.VideoRenderJobSnapshot{
				ID:             "job_terminal",
				AccountScopeID: principal.AccountScopeID,
				SessionID:      "sess_1",
				ProjectID:      "proj_1",
				Status:         status,
			}
			svc := NewService(Config{}, store, nil, nil, nil, nil)

			if _, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_terminal"); err == nil || !strings.Contains(err.Error(), "cannot cancel terminal render job") {
				t.Fatalf("CancelRenderJob() error = %v, want terminal-state rejection", err)
			}
			if got := store.jobs["job_terminal"].Status; got != status {
				t.Fatalf("durable status = %s, want %s", got, status)
			}
		})
	}
}
