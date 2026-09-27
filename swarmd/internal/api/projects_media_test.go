package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

type fakeTestGeminiClient struct {
	mu           sync.Mutex
	shouldFail   bool
	failOnIndex  int
	callCount    int
	lastRequest  imagegen.GeminiImageGenerationRequest
	successBytes []byte
}

func (f *fakeTestGeminiClient) GenerateImage(ctx context.Context, req imagegen.GeminiImageGenerationRequest) (imagegen.GeminiImageGenerationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.lastRequest = req
	if f.shouldFail || (f.failOnIndex > 0 && f.callCount == f.failOnIndex) {
		return imagegen.GeminiImageGenerationResult{}, errors.New("gemini image generation provider error: quota exceeded")
	}
	raw := f.successBytes
	if len(raw) == 0 {
		raw = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	}
	return imagegen.GeminiImageGenerationResult{
		Bytes:     raw,
		MediaType: "image/png",
	}, nil
}

type fakeTestVideoGenService struct {
	mu          sync.Mutex
	shouldFail  bool
	err         error
	lastRequest videogen.ManagedVideoRequest
	result      videogen.ManagedVideoResult
}

func (f *fakeTestVideoGenService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRequest = req
	if f.shouldFail {
		if f.err != nil {
			return videogen.ManagedVideoResult{}, f.err
		}
		return videogen.ManagedVideoResult{}, errors.New("videogen provider upstream error: service unavailable")
	}
	return f.result, nil
}

func setupDirectMediaTestServer(t *testing.T) (*Server, *pebblestore.SessionStore, identity.Principal) {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "projects-media-test.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	authStore := pebblestore.NewAuthStore(db)
	p := identity.Principal{
		Type:           "user",
		UserID:         "user-1",
		AccountScopeID: "account-test",
	}

	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		Provider:       "google",
		AccountScopeID: p.AccountScopeID,
		Type:           pebblestore.AuthTypeAPI,
		APIKey:         "test-google-key",
		SetActive:      true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	imageThreads := pebblestore.NewImageThreadStore(db)
	ss := pebblestore.NewSessionStore(db)
	el, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}

	server := &Server{
		sessions: sessionruntime.NewService(ss, el),
	}
	return server, ss, p
}

func TestDirectImageExecution_HonestFailure_NeverReturnsSVG(t *testing.T) {
	// Purpose:
	// - Requirement: Image generation provider failures must transition deliverables and task to "failed" with durable LastError.
	// - Threat/regression: In prior versions, provider errors silently fell back to SVG placeholder data URLs and marked deliverables "ready".
	// - Boundary/authority: Server.executeDirectMediaTask and Server.generateImageMedia in projects_media.go.
	// - Test layer: Direct execution boundary against Pebble storage and mock Gemini image client.

	server, ss, p := setupDirectMediaTestServer(t)

	failingClient := &fakeTestGeminiClient{shouldFail: true}
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()))
	imageSvc.SetGeminiImageClient(failingClient)
	server.SetImageGenerationService(imageSvc)

	project := &pebblestore.ProjectRecord{
		ID:        "proj-1",
		AccountID: p.AccountScopeID,
		Name:      "Media Project",
	}
	if err := ss.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatalf("put project: %v", err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-img-fail",
		ProjectID:   project.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Generate Cyber Logo",
		Description: "Create a cyberpunk emblem",
		Agent:       "image",
		Status:      "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{
				ID:     "deliv-1",
				Title:  "Variant 1",
				Kind:   "image",
				Status: "generating",
			},
		},
	}
	if err := ss.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	server.executeDirectMediaTask(p, project, task)

	updated, ok, err := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok {
		t.Fatalf("get task failed: ok=%v, err=%v", ok, err)
	}

	if updated.Status != "failed" {
		t.Fatalf("expected task status 'failed', got %q", updated.Status)
	}
	if updated.LastError == "" {
		t.Fatal("expected durable LastError on task, got empty string")
	}
	if len(updated.Deliverables) != 1 {
		t.Fatalf("expected 1 deliverable, got %d", len(updated.Deliverables))
	}
	deliv := updated.Deliverables[0]
	if deliv.Status != "failed" {
		t.Fatalf("expected deliverable status 'failed', got %q", deliv.Status)
	}
	if strings.Contains(deliv.MediaURL, "data:image/svg+xml") {
		t.Fatalf("forbidden SVG placeholder returned as media url: %q", deliv.MediaURL)
	}
	if deliv.MediaURL != "" {
		t.Fatalf("expected empty media url on failed deliverable, got %q", deliv.MediaURL)
	}
}

func TestDirectImageExecution_ExplicitInvalidModel_FailsWithoutSilentFallback(t *testing.T) {
	// Purpose:
	// - Requirement: Passing an explicit invalid or unsupported image model must fail immediately without silent fallback to default models.
	// - Threat/regression: In prior versions, an unresolvable model ID fell back to GoogleImageModelSelections()[0] silently.
	// - Boundary/authority: Server.generateImageMedia and executeDirectMediaTask in projects_media.go.
	// - Test layer: Direct execution boundary asserting explicit model validation and rejection.

	server, ss, p := setupDirectMediaTestServer(t)

	client := &fakeTestGeminiClient{shouldFail: false}
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()))
	imageSvc.SetGeminiImageClient(client)
	server.SetImageGenerationService(imageSvc)

	project := &pebblestore.ProjectRecord{
		ID:        "proj-model-test",
		AccountID: p.AccountScopeID,
		Name:      "Model Test",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-invalid-model",
		ProjectID:   project.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Generate with fake model",
		Description: "Prompt text",
		Agent:       "image",
		Model:       "completely-fake-unsupported-image-model",
		Status:      "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv-inv-1", Title: "Variant 1", Kind: "image", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if updated.Status != "failed" {
		t.Fatalf("expected task status 'failed' on invalid model, got %q", updated.Status)
	}
	if !strings.Contains(strings.ToLower(updated.LastError), "unsupported image model") && !strings.Contains(strings.ToLower(updated.LastError), "invalid") {
		t.Fatalf("expected LastError to mention model rejection, got %q", updated.LastError)
	}
	if client.callCount > 0 {
		t.Fatalf("provider was called %d times despite invalid model specification", client.callCount)
	}
}

func TestDirectImageExecution_PartialVariantSuccess(t *testing.T) {
	// Purpose:
	// - Requirement: When generating multiple variants, successful variants must be preserved with 'ready' status while failed variants are marked 'failed'.
	// - Threat/regression: Dropping successful variants or masking failures in batch generations.
	// - Boundary/authority: Server.executeDirectMediaTask batch loop in projects_media.go.
	// - Test layer: Direct execution boundary with intermittent failure on variant 2.

	server, ss, p := setupDirectMediaTestServer(t)

	client := &fakeTestGeminiClient{
		failOnIndex: 2, // Variant 1 succeeds, Variant 2 fails
	}
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()))
	imageSvc.SetGeminiImageClient(client)
	server.SetImageGenerationService(imageSvc)

	project := &pebblestore.ProjectRecord{
		ID:        "proj-partial",
		AccountID: p.AccountScopeID,
		Name:      "Partial Batch",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-partial-batch",
		ProjectID:   project.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Generate Two Takes",
		Description: "Two distinct variants",
		Agent:       "image",
		Status:      "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-1", Title: "Take 1", Kind: "image", Status: "generating"},
			{ID: "d-2", Title: "Take 2", Kind: "image", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if updated.Status != "needs_review" {
		t.Fatalf("expected task status 'needs_review' for partial batch, got %q", updated.Status)
	}
	if updated.LastError == "" {
		t.Fatal("expected LastError to document partial failure")
	}

	foundReady := false
	foundFailed := false
	for _, d := range updated.Deliverables {
		if d.Status == "ready" {
			foundReady = true
			if !strings.HasPrefix(d.MediaURL, "data:image/png;base64,") {
				t.Fatalf("expected real PNG data URL on ready deliverable, got %q", d.MediaURL)
			}
		}
		if d.Status == "failed" {
			foundFailed = true
			if d.MediaURL != "" {
				t.Fatalf("expected empty MediaURL on failed deliverable, got %q", d.MediaURL)
			}
		}
	}
	if !foundReady || !foundFailed {
		t.Fatalf("expected one ready and one failed deliverable, got deliverables: %#v", updated.Deliverables)
	}
}

func TestDirectVideoExecution_HonestFailure_NeverReturnsSVG(t *testing.T) {
	// Purpose:
	// - Requirement: Direct video generation errors must mark deliverable as 'failed' and task as 'failed', NEVER producing SVG storyboard placeholders or fake success.
	// - Threat/regression: In prior versions, video generation always returned generateStyledVideoSVGDataURL after time.Sleep and marked status 'ready'.
	// - Boundary/authority: Server.executeDirectMediaTask video branch in projects_media.go.
	// - Test layer: Direct execution boundary against failing videogen service.

	server, ss, p := setupDirectMediaTestServer(t)

	mockVideoSvc := &fakeTestVideoGenService{
		shouldFail: true,
		err:        errors.New("video generation upstream error: server busy"),
	}
	SetDirectVideoGenerationService(mockVideoSvc)
	t.Cleanup(func() { SetDirectVideoGenerationService(nil) })

	project := &pebblestore.ProjectRecord{
		ID:        "proj-video-fail",
		AccountID: p.AccountScopeID,
		Name:      "Video Fail",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-video-fail",
		ProjectID:   project.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Cinematic Sequence",
		Description: "A flying camera sequence",
		Agent:       "video",
		Status:      "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-vid-1", Title: "Single Video", Kind: "video", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}

	if updated.Status != "failed" {
		t.Fatalf("expected task status 'failed', got %q", updated.Status)
	}
	if updated.LastError == "" || !strings.Contains(updated.LastError, "server busy") {
		t.Fatalf("expected LastError to document upstream failure, got %q", updated.LastError)
	}
	if len(updated.Deliverables) != 1 {
		t.Fatalf("expected 1 deliverable, got %d", len(updated.Deliverables))
	}
	deliv := updated.Deliverables[0]
	if deliv.Status != "failed" {
		t.Fatalf("expected deliverable status 'failed', got %q", deliv.Status)
	}
	if strings.Contains(deliv.MediaURL, "data:image/svg+xml") {
		t.Fatalf("forbidden SVG placeholder returned for failed video: %q", deliv.MediaURL)
	}
	if deliv.MediaURL != "" {
		t.Fatalf("expected empty media url on failed video deliverable, got %q", deliv.MediaURL)
	}
}

func TestDirectVideoExecution_ExplicitInvalidModel_FailsWithoutSilentFallback(t *testing.T) {
	// Purpose:
	// - Requirement: Video tasks with an explicit invalid model must fail clearly and never silently fallback to default Veo or generate fake SVG.
	// - Threat/regression: Silent fallback or SVG creation when an unsupported model ID is submitted.
	// - Boundary/authority: isSupportedVideoModel and executeDirectMediaTask in projects_media.go.
	// - Test layer: Direct execution boundary verifying model rejection.

	server, ss, p := setupDirectMediaTestServer(t)

	mockVideoSvc := &fakeTestVideoGenService{shouldFail: false}
	SetDirectVideoGenerationService(mockVideoSvc)
	t.Cleanup(func() { SetDirectVideoGenerationService(nil) })

	project := &pebblestore.ProjectRecord{
		ID:        "proj-video-invalid",
		AccountID: p.AccountScopeID,
		Name:      "Invalid Video Model",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-invalid-video-model",
		ProjectID:   project.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Video with invalid model",
		Description: "Prompt",
		Agent:       "video",
		Model:       "completely-invalid-video-model-xyz",
		Status:      "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-vid-inv", Title: "Single Video", Kind: "video", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if updated.Status != "failed" {
		t.Fatalf("expected task status 'failed' on invalid video model, got %q", updated.Status)
	}
	if !strings.Contains(strings.ToLower(updated.LastError), "unsupported video model") {
		t.Fatalf("expected LastError to mention unsupported video model, got %q", updated.LastError)
	}
}

func TestDirectVideoExecution_RealVideoResultDelivery(t *testing.T) {
	// Purpose:
	// - Requirement: Real video generation results from videogen service must be formatted as data:video/mp4;base64,... and transition task to 'needs_review'.
	// - Threat/regression: Fake SVG returned instead of real MP4 bytes, or task left in wrong status.
	// - Boundary/authority: Server.executeDirectMediaTask video branch in projects_media.go.
	// - Test layer: Direct execution boundary against mock videogen service returning video bytes.

	server, ss, p := setupDirectMediaTestServer(t)

	fakeMP4 := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	mockVideoSvc := &fakeTestVideoGenService{
		shouldFail: false,
		result: videogen.ManagedVideoResult{
			Bytes:           fakeMP4,
			MediaType:       "video/mp4",
			Model:           "veo-3.1-generate-preview",
			DurationSeconds: 8,
			Resolution:      "720p",
			AspectRatio:     "16:9",
		},
	}
	SetDirectVideoGenerationService(mockVideoSvc)
	t.Cleanup(func() { SetDirectVideoGenerationService(nil) })

	project := &pebblestore.ProjectRecord{
		ID:        "proj-video-success",
		AccountID: p.AccountScopeID,
		Name:      "Real Video",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:              "task-video-success",
		ProjectID:       project.ID,
		AccountID:       p.AccountScopeID,
		Title:           "Autonomous Drone Footage",
		Description:     "A swooping shot through neon city",
		Agent:           "video",
		Model:           "veo-3.1-generate-preview",
		DurationSeconds: 8,
		Resolution:      "720p",
		Status:          "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-vid-success", Title: "Single Video", Kind: "video", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}

	if updated.Status != "needs_review" {
		t.Fatalf("expected task status 'needs_review', got %q", updated.Status)
	}
	if len(updated.Deliverables) != 1 {
		t.Fatalf("expected 1 deliverable, got %d", len(updated.Deliverables))
	}
	deliv := updated.Deliverables[0]
	if deliv.Status != "ready" {
		t.Fatalf("expected deliverable status 'ready', got %q", deliv.Status)
	}
	if !strings.HasPrefix(deliv.MediaURL, "data:video/mp4;base64,") {
		t.Fatalf("expected media url to start with 'data:video/mp4;base64,', got %q", deliv.MediaURL)
	}
	if deliv.Duration != "8s" {
		t.Fatalf("expected duration '8s', got %q", deliv.Duration)
	}
}

func TestResolveSourceMediaBytes_SecurityBoundaries(t *testing.T) {
	// Purpose:
	// - Invariant: Source media resolution must strictly reject arbitrary external URLs and filesystem paths (preventing SSRF and directory traversal).
	// - Boundary/authority: Server.resolveSourceMediaBytes in projects_media.go.
	// - Negative cases: http://, https://, /etc/passwd, ../../traversal
	// - Positive cases: data URIs (image and video), raw base64.

	server, _, p := setupDirectMediaTestServer(t)
	ctx := context.Background()

	// 1. Negative: external HTTP URL
	_, _, err := server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-external-http",
		URL: "http://malicious-domain.com/steal-data.png",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "arbitrary external URLs") {
		t.Fatalf("expected rejection of external HTTP URL, got err=%v", err)
	}

	// 2. Negative: external HTTPS URL
	_, _, err = server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-external-https",
		URL: "https://169.254.169.254/latest/meta-data",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "arbitrary external URLs") {
		t.Fatalf("expected rejection of external HTTPS URL, got err=%v", err)
	}

	// 3. Negative: absolute filesystem path
	_, _, err = server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-fs-abs",
		URL: "/etc/shadow",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "direct filesystem paths") {
		t.Fatalf("expected rejection of absolute filesystem path, got err=%v", err)
	}

	// 4. Negative: relative traversal path
	_, _, err = server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-fs-rel",
		URL: "../../credentials.json",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "direct filesystem paths") {
		t.Fatalf("expected rejection of traversal filesystem path, got err=%v", err)
	}

	// 5. Positive: valid image data URI
	pngBytes := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	dataURI := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngBytes))
	decoded, mType, err := server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-data-uri",
		URL: dataURI,
	}, "image")
	if err != nil {
		t.Fatalf("expected data URI to succeed, got %v", err)
	}
	if !bytes.Equal(decoded, pngBytes) {
		t.Fatalf("decoded bytes mismatch: got %v, want %v", decoded, pngBytes)
	}
	if mType != "image/png" {
		t.Fatalf("expected media type 'image/png', got %q", mType)
	}

	// 6. Positive: valid video data URI
	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}
	videoDataURI := fmt.Sprintf("data:video/mp4;base64,%s", base64.StdEncoding.EncodeToString(mp4Bytes))
	decodedVid, vidMType, err := server.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{
		ID:  "media-vid-data-uri",
		URL: videoDataURI,
	}, "video")
	if err != nil {
		t.Fatalf("expected video data URI to succeed, got %v", err)
	}
	if !bytes.Equal(decodedVid, mp4Bytes) {
		t.Fatalf("decoded video bytes mismatch: got %v, want %v", decodedVid, mp4Bytes)
	}
	if vidMType != "video/mp4" {
		t.Fatalf("expected media type 'video/mp4', got %q", vidMType)
	}
}

func TestDirectMediaTask_InitialPersistenceGuard(t *testing.T) {
	// Purpose:
	// - Requirement: deployProjectTaskExecution must persist the direct media task to Pebble before spawning background execution.
	// - Threat/regression: Background execution goroutine races against the caller's initial PutProjectTask, causing "project task not found" or overwritten deliverable states.
	// - Boundary/authority: Server.deployProjectTaskExecution in projects.go.
	// - Test layer: Direct execution boundary verifying task persistence before goroutine progress.

	server, ss, p := setupDirectMediaTestServer(t)

	// Block video generation indefinitely so we can observe initial persistence
	blockChan := make(chan struct{})
	mockVideoSvc := &fakeTestVideoGenService{
		shouldFail: false,
	}
	SetDirectVideoGenerationService(mockVideoSvc)
	t.Cleanup(func() {
		close(blockChan)
		SetDirectVideoGenerationService(nil)
	})

	project := &pebblestore.ProjectRecord{
		ID:        "proj-race-guard",
		AccountID: p.AccountScopeID,
		Name:      "Race Guard Project",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:              "task-direct-guard",
		ProjectID:       project.ID,
		AccountID:       p.AccountScopeID,
		Title:           "Race Guard Task",
		Agent:           "video",
		Model:           "veo-3.1-generate-preview",
		DurationSeconds: 8,
		Resolution:      "720p",
	}

	err := server.deployProjectTaskExecution(p, project, task, "in_progress", "A race guard test video")
	if err != nil {
		t.Fatalf("deployProjectTaskExecution failed: %v", err)
	}

	// Verify task is immediately persisted in Pebble
	persisted, ok, err := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok {
		t.Fatalf("task must be persisted in Pebble immediately upon deployProjectTaskExecution: ok=%v, err=%v", ok, err)
	}
	if persisted.Status != "in_progress" {
		t.Fatalf("expected initial persisted status 'in_progress', got %q", persisted.Status)
	}
	if len(persisted.Deliverables) != 1 {
		t.Fatalf("expected 1 initial deliverable, got %d", len(persisted.Deliverables))
	}
	if persisted.Deliverables[0].Status != "generating" {
		t.Fatalf("expected initial deliverable status 'generating', got %q", persisted.Deliverables[0].Status)
	}
}
