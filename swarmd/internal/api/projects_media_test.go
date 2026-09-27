package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
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
		raw = imageGenerationTestPNGBytes()
	}
	return imagegen.GeminiImageGenerationResult{
		Images: []imagegen.GeminiGeneratedImage{
			{
				DecodedPNG: raw,
				MIMEType:   "image/png",
			},
		},
	}, nil
}

type fakeTestVideoGenService struct {
	mu          sync.Mutex
	shouldFail  bool
	failOnIndex int
	callCount   int
	err         error
	lastRequest videogen.ManagedVideoRequest
	result      videogen.ManagedVideoResult
}

func (f *fakeTestVideoGenService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.lastRequest = req
	if f.shouldFail || (f.failOnIndex > 0 && f.callCount == f.failOnIndex) {
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

	catalogStore := pebblestore.NewModelCatalogStore(db)
	pricing := json.RawMessage(`{"input_per_million":1.25,"output_per_million":5}`)
	googleImageProviderSpecific := json.RawMessage(`{"google":{"model_api_surface":"generate_content","image_generation":{"api_surface":"generate_content","status":"verified","managed_image_tool":{"supported":true,"client_setting_names":["aspect_ratio","image_size"]},"settings":{"aspect_ratio":{"status":"verified","default_value":"1:1","supported_values":["1:1","16:9","9:16","4:3","3:4"]},"image_size":{"status":"verified","default_value":"1K","supported_values":["1K","2K"]}}}}}`)
	googleGenerateContentMedia := &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent}

	googleVideoProviderSpecific := json.RawMessage(`{"google":{"model_api_surface":"predict","video_generation":{"status":"verified","settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16","1:1","4:3"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p","1080p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}}}}}`)
	googlePredictMedia := &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceGooglePredict}

	for _, record := range []pebblestore.ModelCatalogRecord{
		{Provider: "google", Model: "snapshot-image", DisplayName: "Snapshot Image", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"image"}}, Media: googleGenerateContentMedia, ProviderSpecific: googleImageProviderSpecific, Pricing: pricing, SourceSnapshotID: "snap-1", SourceSnapshotVersion: "1"},
		{Provider: "google", Model: "veo-3.1-generate-preview", DisplayName: "Veo 3.1", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "image"}, Outputs: []string{"video"}}, Media: googlePredictMedia, ProviderSpecific: googleVideoProviderSpecific, Pricing: pricing, SourceSnapshotID: "snap-1", SourceSnapshotVersion: "1"},
	} {
		if err := catalogStore.SetRecord(record); err != nil {
			t.Fatalf("seed catalog record: %v", err)
		}
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(db), nil, model.NewCatalogService(catalogStore))
	ss := pebblestore.NewSessionStore(db)
	el, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}

	server := &Server{
		sessions: sessionruntime.NewService(ss, el),
		model:    modelSvc,
	}
	defaultImageClient := &fakeTestGeminiClient{}
	defaultImageSvc := imagegen.NewService(nil, authStore, pebblestore.NewImageThreadStore(db), modelSvc)
	defaultImageSvc.SetGeminiImageClient(defaultImageClient)
	server.SetImageGenerationService(defaultImageSvc)
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
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()), server.model)
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
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()), server.model)
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
	imageSvc := imagegen.NewService(nil, pebblestore.NewAuthStore(ss.Underlying()), pebblestore.NewImageThreadStore(ss.Underlying()), server.model)
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

func TestDirectVideoExecution_MultiVariantBatch(t *testing.T) {
	// Purpose:
	// - Requirement: Video tasks with VariantCount > 1 must generate multiple independent deliverables in parallel via videogen service, marking each as ready with unique media URLs and titles (Take 1, Take 2, etc.), transitioning task to needs_review.
	// - Threat/regression: Only generating Deliverables[0] and ignoring VariantCount or leaving other deliverables in 'generating' state indefinitely.
	// - Boundary/authority: Server.executeDirectMediaTask video multi-variant loop in projects_media.go.
	// - Test layer: Direct execution boundary against mock videogen service with 3 variants.

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
		ID:        "proj-video-multi",
		AccountID: p.AccountScopeID,
		Name:      "Multi Video Project",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:              "task-video-multi-3",
		ProjectID:       project.ID,
		AccountID:       p.AccountScopeID,
		Title:           "Autonomous Action Scene",
		Description:     "Fast paced camera run through neon alley",
		Agent:           "video",
		Model:           "veo-3.1-generate-preview",
		DurationSeconds: 8,
		Resolution:      "720p",
		AspectRatio:     "16:9",
		VariantCount:    3,
		Status:          "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-vid-1", Title: "Take 1", Kind: "video", Status: "generating"},
			{ID: "d-vid-2", Title: "Take 2", Kind: "video", Status: "generating"},
			{ID: "d-vid-3", Title: "Take 3", Kind: "video", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if updated.Status != "needs_review" {
		t.Fatalf("expected task status 'needs_review', got %q (lastError: %s)", updated.Status, updated.LastError)
	}
	if updated.LastError != "" {
		t.Fatalf("expected empty LastError for full success, got %q", updated.LastError)
	}
	if len(updated.Deliverables) != 3 {
		t.Fatalf("expected 3 deliverables, got %d", len(updated.Deliverables))
	}
	for i, d := range updated.Deliverables {
		if d.Status != "ready" {
			t.Fatalf("deliverable %d status = %q, want 'ready'", i, d.Status)
		}
		if !strings.HasPrefix(d.MediaURL, "data:video/mp4;base64,") {
			t.Fatalf("deliverable %d media URL missing mp4 base64 prefix: %q", i, d.MediaURL)
		}
		if !strings.Contains(d.Title, fmt.Sprintf("Take %d", i+1)) {
			t.Fatalf("deliverable %d title %q does not mention Take %d", i, d.Title, i+1)
		}
	}
}

func TestDirectVideoExecution_PartialVariantSuccess(t *testing.T) {
	// Purpose:
	// - Requirement: When generating multiple video variants, successful variants must be preserved with 'ready' status while failed variants are marked 'failed'. Task must transition to 'needs_review' with durable LastError noting partial failure.
	// - Threat/regression: Entire batch marked failed when one variant fails, or partial failures masked as complete success.
	// - Boundary/authority: Server.executeDirectMediaTask video batch loop in projects_media.go.
	// - Test layer: Direct execution boundary with failOnIndex=2 on 2-variant video task.

	server, ss, p := setupDirectMediaTestServer(t)

	fakeMP4 := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	mockVideoSvc := &fakeTestVideoGenService{
		failOnIndex: 2, // Take 1 succeeds, Take 2 fails
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
		ID:        "proj-video-partial",
		AccountID: p.AccountScopeID,
		Name:      "Partial Video Batch",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:              "task-video-partial-2",
		ProjectID:       project.ID,
		AccountID:       p.AccountScopeID,
		Title:           "Two Takes Scene",
		Description:     "Generate two takes of neon street",
		Agent:           "video",
		Model:           "veo-3.1-generate-preview",
		DurationSeconds: 8,
		Resolution:      "720p",
		AspectRatio:     "16:9",
		VariantCount:    2,
		Status:          "in_progress",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d-vid-p1", Title: "Take 1", Kind: "video", Status: "generating"},
			{ID: "d-vid-p2", Title: "Take 2", Kind: "video", Status: "generating"},
		},
	}
	_ = ss.PutProjectTask(p.AccountScopeID, task)

	server.executeDirectMediaTask(p, project, task)

	updated, ok, _ := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if updated.Status != "needs_review" {
		t.Fatalf("expected task status 'needs_review' on partial success, got %q", updated.Status)
	}
	if !strings.Contains(updated.LastError, "1 of 2") {
		t.Fatalf("expected LastError to document 1 of 2 failed deliverables, got %q", updated.LastError)
	}
	if len(updated.Deliverables) != 2 {
		t.Fatalf("expected 2 deliverables, got %d", len(updated.Deliverables))
	}
	d0 := updated.Deliverables[0]
	if d0.Status != "ready" {
		t.Fatalf("expected deliverable 0 status 'ready', got %q", d0.Status)
	}
	if !strings.HasPrefix(d0.MediaURL, "data:video/mp4;base64,") {
		t.Fatalf("expected real MP4 URL on deliverable 0, got %q", d0.MediaURL)
	}
	d1 := updated.Deliverables[1]
	if d1.Status != "failed" {
		t.Fatalf("expected deliverable 1 status 'failed', got %q", d1.Status)
	}
	if d1.MediaURL != "" {
		t.Fatalf("expected empty MediaURL on failed deliverable 1, got %q", d1.MediaURL)
	}
}

func TestDirectMediaExecution_Validation_RejectsInvalidSettings(t *testing.T) {
	// Purpose:
	// - Requirement: Explicit settings (unsupported aspect ratio, unsupported resolution, unsupported duration, or duration/resolution mismatches) on direct media tasks must be rejected rather than silently normalized away.
	// - Threat/regression: Silently normalizing invalid user choices to defaults without warning or error.
	// - Boundary/authority: validateProjectMediaTaskSettings and Server.executeDirectMediaTask in projects_media.go.
	// - Test layer: Direct execution boundary verifying rejection of:
	//   1) Video invalid aspect ratio "21:9"
	//   2) Video invalid resolution "8k"
	//   3) Video invalid duration 15s
	//   4) Video 1080p resolution with 4s duration (requires 8s)
	//   5) Image invalid aspect ratio "32:9"
	//   6) Image invalid resolution "8k"

	server, ss, p := setupDirectMediaTestServer(t)

	cases := []struct {
		name          string
		task          pebblestore.ProjectTaskRecord
		wantErrSubstr string
	}{
		{
			name: "video unsupported aspect ratio",
			task: pebblestore.ProjectTaskRecord{
				ID:          "task-val-vid-ar",
				Agent:       "video",
				AspectRatio: "21:9",
				Status:      "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "unsupported video aspect ratio",
		},
		{
			name: "video unsupported resolution",
			task: pebblestore.ProjectTaskRecord{
				ID:          "task-val-vid-res",
				Agent:       "video",
				Resolution:  "8k",
				Status:      "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "unsupported video resolution",
		},
		{
			name: "video unsupported duration",
			task: pebblestore.ProjectTaskRecord{
				ID:              "task-val-vid-dur",
				Agent:           "video",
				DurationSeconds: 15,
				Status:          "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "unsupported video duration",
		},
		{
			name: "video 1080p requires 8s duration",
			task: pebblestore.ProjectTaskRecord{
				ID:              "task-val-vid-1080p-4s",
				Agent:           "video",
				Resolution:      "1080p",
				DurationSeconds: 4,
				Status:          "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "requires 8s duration",
		},
		{
			name: "image unsupported aspect ratio",
			task: pebblestore.ProjectTaskRecord{
				ID:          "task-val-img-ar",
				Agent:       "image",
				AspectRatio: "32:9",
				Status:      "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "unsupported image aspect ratio",
		},
		{
			name: "image unsupported resolution",
			task: pebblestore.ProjectTaskRecord{
				ID:          "task-val-img-res",
				Agent:       "image",
				Resolution:  "8k",
				Status:      "in_progress",
				Deliverables: []pebblestore.ProjectTaskDeliverable{
					{ID: "d-1", Status: "generating"},
				},
			},
			wantErrSubstr: "unsupported image resolution",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := &pebblestore.ProjectRecord{
				ID:        "proj-" + tc.task.ID,
				AccountID: p.AccountScopeID,
				Name:      "Val Test Proj",
			}
			_ = ss.PutProject(p.AccountScopeID, proj)
			tc.task.ProjectID = proj.ID
			tc.task.AccountID = p.AccountScopeID
			_ = ss.PutProjectTask(p.AccountScopeID, &tc.task)

			server.executeDirectMediaTask(p, proj, &tc.task)

			updated, ok, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, tc.task.ID)
			if err != nil || !ok {
				t.Fatalf("get task failed: ok=%v, err=%v", ok, err)
			}
			if updated.Status != "failed" {
				t.Fatalf("expected task status 'failed', got %q", updated.Status)
			}
			if !strings.Contains(strings.ToLower(updated.LastError), strings.ToLower(tc.wantErrSubstr)) {
				t.Fatalf("expected LastError to contain %q, got %q", tc.wantErrSubstr, updated.LastError)
			}
			for i, d := range updated.Deliverables {
				if d.Status != "failed" {
					t.Fatalf("deliverable %d status = %q, want 'failed'", i, d.Status)
				}
			}
		})
	}
}

func TestDirectMediaTask_DeployProjectTaskExecution_MultiVideoDeliverables(t *testing.T) {
	// Purpose:
	// - Requirement: deployProjectTaskExecution for video agent with VariantCount > 1 must allocate VariantCount deliverables in "generating" state with unique IDs and Take numbers.
	// - Threat/regression: Only 1 deliverable allocated in deployProjectTaskExecution regardless of VariantCount.
	// - Boundary/authority: Server.deployProjectTaskExecution in projects.go.
	// - Test layer: Direct deployment boundary checking Pebble persistence and deliverable count.

	server, ss, p := setupDirectMediaTestServer(t)

	// Block video generation so we observe initial persisted state
	mockVideoSvc := &fakeTestVideoGenService{
		shouldFail: true,
		err:        errors.New("blocked"),
	}
	SetDirectVideoGenerationService(mockVideoSvc)
	t.Cleanup(func() { SetDirectVideoGenerationService(nil) })

	project := &pebblestore.ProjectRecord{
		ID:        "proj-deploy-multi-vid",
		AccountID: p.AccountScopeID,
		Name:      "Deploy Multi Vid",
	}
	_ = ss.PutProject(p.AccountScopeID, project)

	task := &pebblestore.ProjectTaskRecord{
		ID:              "task-deploy-vid-4",
		ProjectID:       project.ID,
		AccountID:       p.AccountScopeID,
		Title:           "Cyberpunk Flying Vehicle",
		Agent:           "video",
		Model:           "veo-3.1-generate-preview",
		DurationSeconds: 8,
		Resolution:      "720p",
		AspectRatio:     "16:9",
		VariantCount:    4,
	}

	err := server.deployProjectTaskExecution(p, project, task, "in_progress", "Generate 4 takes of flying vehicle")
	if err != nil {
		t.Fatalf("deployProjectTaskExecution failed: %v", err)
	}

	// Verify initial persisted task in Pebble has 4 deliverables in generating state
	persisted, ok, err := ss.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok {
		t.Fatalf("get persisted task failed: ok=%v, err=%v", ok, err)
	}
	if len(persisted.Deliverables) != 4 {
		t.Fatalf("expected 4 deliverables allocated, got %d", len(persisted.Deliverables))
	}
	for i, d := range persisted.Deliverables {
		if !strings.Contains(d.Title, fmt.Sprintf("Take %d", i+1)) {
			t.Fatalf("deliverable %d title %q does not contain 'Take %d'", i, d.Title, i+1)
		}
		if d.Kind != "video" {
			t.Fatalf("deliverable %d kind = %q, want 'video'", i, d.Kind)
		}
		if d.Duration != "8s" {
			t.Fatalf("deliverable %d duration = %q, want '8s'", i, d.Duration)
		}
	}
}
