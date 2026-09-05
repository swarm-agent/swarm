package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testImagePrincipal() identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
}

type fakeCodexImageClient struct {
	result        codex.ImageGenerationResult
	results       []codex.ImageGenerationResult
	err           error
	request       codex.ImageGenerationRequest
	requests      []codex.ImageGenerationRequest
	onGenerate    func(req codex.ImageGenerationRequest)
	onGenerateSeq []func(req codex.ImageGenerationRequest)
}

func (f *fakeCodexImageClient) GenerateImage(ctx context.Context, req codex.ImageGenerationRequest) (codex.ImageGenerationResult, error) {
	_ = ctx
	f.request = req
	f.requests = append(f.requests, req)
	if len(f.onGenerateSeq) > 0 {
		onGenerate := f.onGenerateSeq[0]
		f.onGenerateSeq = f.onGenerateSeq[1:]
		if onGenerate != nil {
			onGenerate(req)
		}
	} else if f.onGenerate != nil {
		f.onGenerate(req)
	}
	if f.err != nil {
		return codex.ImageGenerationResult{}, f.err
	}
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	return f.result, nil
}

func TestGenerateManagedImageUsesCanonicalSelectionAndExactlyOneProviderCall(t *testing.T) {
	client := &fakeCodexImageClient{result: codex.ImageGenerationResult{CallID: "managed", DecodedPNG: testPNGBytes()}}
	svc, _, _, _ := newImageServiceTestHarnessWithClient(t, client)

	result, err := svc.GenerateManagedImage(context.Background(), ManagedGenerateRequest{
		SelectionID: "codex-image-gen", Prompt: "make one square", Size: "1024x1024", Principal: testImagePrincipal(),
	})
	if err != nil {
		t.Fatalf("GenerateManagedImage: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("provider calls = %d, want exactly one", len(client.requests))
	}
	if request := client.requests[0]; request.Model != defaultCodexImageModel || request.Count != 1 || request.Prompt != "make one square" || request.Size != "1024x1024" {
		t.Fatalf("managed provider request = %#v", request)
	}
	if len(result.Bytes) == 0 || result.MediaType != "image/png" {
		t.Fatalf("managed result = %#v", result)
	}
	if _, err := ResolveModelSelection("model-authored-unknown"); err == nil {
		t.Fatal("unsupported configured image model was accepted")
	}
}

type staticModelCatalog struct {
	records []pebblestore.ModelCatalogRecord
}

func (c staticModelCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return append([]pebblestore.ModelCatalogRecord(nil), c.records...), nil
}

func googleImageProfileRecord(t *testing.T, model string, managed bool, surface string, settings string) pebblestore.ModelCatalogRecord {
	t.Helper()
	providerSpecific := json.RawMessage(`{"google":{"model_api_surface":"` + surface + `","image_generation":{"api_surface":"` + surface + `","status":"verified","managed_image_tool":{"supported":` + fmt.Sprint(managed) + `,"client_setting_names":["aspect_ratio","image_size"]},"settings":` + settings + `}}}`)
	return pebblestore.ModelCatalogRecord{
		Provider: "google", Model: model, ProviderSpecific: providerSpecific,
		CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"image"}},
		Media:             &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent},
		SourceSnapshotID:  "snapshot", SourceSnapshotVersion: "version",
	}
}

func TestManagedGoogleImageCapabilitiesAreModelSpecificAndRequireFreshToken(t *testing.T) {
	settings := `{"aspect_ratio":{"status":"verified","default_value":"1:1","supported_values":["1:1","16:9"]},"image_size":{"status":"verified","default_value":"1K","supported_values":["1K"]}}`
	svc := &Service{modelCatalog: staticModelCatalog{records: []pebblestore.ModelCatalogRecord{
		googleImageProfileRecord(t, "gemini-image", false, "generate_content", settings),
		googleImageProfileRecord(t, "imagen-4.0-generate-001", true, "predict", settings),
	}}}
	capabilities, err := svc.ManagedImageCapabilities("gemini-image")
	if err != nil || !capabilities.Available || capabilities.CapabilityToken == "" {
		t.Fatalf("capabilities = %+v err=%v", capabilities, err)
	}
	if _, _, err := capabilities.validateRequest(ManagedGenerateRequest{Settings: map[string]any{"aspect_ratio": "16:9"}}); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("missing token error = %v", err)
	}
	ratio, size, err := capabilities.validateRequest(ManagedGenerateRequest{CapabilityToken: capabilities.CapabilityToken, Settings: map[string]any{"aspect_ratio": "16:9"}})
	if err != nil || ratio != "16:9" || size != "1K" {
		t.Fatalf("validated ratio=%q size=%q err=%v", ratio, size, err)
	}
	if _, _, err := capabilities.validateRequest(ManagedGenerateRequest{CapabilityToken: capabilities.CapabilityToken, Settings: map[string]any{"image_size": "2K"}}); err == nil {
		t.Fatal("unsupported model-specific image size was accepted")
	}
	imagen, err := svc.ManagedImageCapabilities("imagen-4.0-generate-001")
	if err != nil || imagen.Available {
		t.Fatalf("Imagen capabilities = %+v err=%v, want unavailable", imagen, err)
	}
}

func TestManagedImageSettingsAdaptToSelectedProvider(t *testing.T) {
	portable := ManagedGenerateRequest{Size: "1536x1024", Settings: map[string]any{"image_size": "2K"}}
	if got := managedCodexImageSize(portable); got != "1536x1024" {
		t.Fatalf("managedCodexImageSize = %q, want 1536x1024", got)
	}
	if got := managedGeminiImageSize(portable); got != "2K" {
		t.Fatalf("managedGeminiImageSize = %q, want 2K", got)
	}
	if got := managedGeminiAspectRatio(portable); got != "3:2" {
		t.Fatalf("managedGeminiAspectRatio = %q, want 3:2", got)
	}

	tierOnly := ManagedGenerateRequest{Settings: map[string]any{"image_size": "2K"}}
	if got := managedCodexImageSize(tierOnly); got != "2048x2048" {
		t.Fatalf("managedCodexImageSize tier = %q, want 2048x2048", got)
	}
}

func TestGenerateWorkspaceImageSessionBackendWritesOnePNGBeforeSuccess(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	svc, threads, threadID, storagePath := newImageServiceTestHarnessWithDataHome(t, dataHome, &fakeCodexImageClient{result: codex.ImageGenerationResult{
		CallID:     "ig_test/../bad",
		DecodedPNG: testPNGBytes(),
	}})
	logPath := filepath.Join(dataHome, "main", "imagegen.log")

	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Assets) != 1 {
		t.Fatalf("assets len = %d, want 1", len(result.Assets))
	}
	asset := result.Assets[0]
	assertSavedPNG(t, storagePath, asset.Path)
	if !strings.HasPrefix(asset.URL, "/v1/image/assets?") {
		t.Fatalf("asset URL = %q", asset.URL)
	}
	if strings.Contains(filepath.Base(asset.Path), "..") || strings.Contains(filepath.Base(asset.Path), string(filepath.Separator)) {
		t.Fatalf("asset filename was not sanitized: %q", asset.Path)
	}
	updated, ok, err := threads.GetForAccount("account-1", threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	if len(updated.ImageAssets) != 1 || updated.ImageAssets[0].ID != asset.ID {
		t.Fatalf("thread assets = %#v, want generated asset", updated.ImageAssets)
	}
	if len(updated.ImageAssetOrder) != 1 || updated.ImageAssetOrder[0] != asset.ID {
		t.Fatalf("thread asset order = %#v, want generated asset", updated.ImageAssetOrder)
	}
	if updated.ImageFolders[0] != storagePath {
		t.Fatalf("image folders = %#v, want app-managed storage first", updated.ImageFolders)
	}
	if got, ok := updated.Metadata[assetPathMetadataKey].(string); !ok || got != storagePath {
		t.Fatalf("metadata.%s = %#v, want %q", assetPathMetadataKey, updated.Metadata[assetPathMetadataKey], storagePath)
	}
	assertImageGenerationLogContains(t, logPath,
		"[swarmd.imagegen] stage=start",
		"[swarmd.imagegen] stage=file_write_done",
		"[swarmd.imagegen] stage=db_update_done",
		"[swarmd.imagegen] stage=success",
	)
}

func TestGenerateWorkspaceImageSessionBackendWritesExactlyThreePNGs(t *testing.T) {
	client := &fakeCodexImageClient{result: codex.ImageGenerationResult{
		ProviderResponse: map[string]any{"id": "resp_1"},
		PartialImages: []codex.ImageGenerationPartialImage{{
			ItemID:            "ig_1",
			OutputIndex:       0,
			PartialImageIndex: 2,
			Base64Image:       "preview",
		}},
		Results: []codex.ImageGenerationResult{
			{CallID: "ig_1", OutputIndex: 0, RevisedPrompt: "first", DecodedPNG: testPNGBytes(), ProviderResponse: map[string]any{"id": "resp_1"}},
			{CallID: "ig_2", OutputIndex: 1, RevisedPrompt: "second", DecodedPNG: testPNGBytes(), ProviderResponse: map[string]any{"id": "resp_1"}},
			{CallID: "ig_3", OutputIndex: 2, RevisedPrompt: "third", DecodedPNG: testPNGBytes(), ProviderResponse: map[string]any{"id": "resp_1"}},
		},
	}}
	svc, threads, threadID, storagePath := newImageServiceTestHarnessWithClient(t, client)

	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:      ProviderCodexOpenAI,
		Model:         "gpt-5.5",
		Prompt:        "make three squares",
		Count:         3,
		PartialImages: 9,
		Principal:     testImagePrincipal(),
		Target:        GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1 parallel-capable request", len(client.requests))
	}
	if client.requests[0].Count != 3 || client.requests[0].PartialImages != 3 {
		t.Fatalf("provider request = %#v, want count=3 partial_images=3", client.requests[0])
	}
	if len(result.Assets) != 3 {
		t.Fatalf("assets len = %d, want 3", len(result.Assets))
	}
	entries, err := os.ReadDir(storagePath)
	if err != nil {
		t.Fatalf("read storage dir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("storage dir file count = %d, want 3", len(entries))
	}
	for _, asset := range result.Assets {
		assertSavedPNG(t, storagePath, asset.Path)
	}
	updated, ok, err := threads.GetForAccount("account-1", threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	if len(updated.ImageAssets) != 3 || len(updated.ImageAssetOrder) != 3 {
		t.Fatalf("thread assets/order = %#v / %#v, want 3 saved assets", updated.ImageAssets, updated.ImageAssetOrder)
	}
	for i, asset := range result.Assets {
		if updated.ImageAssetOrder[i] != asset.ID {
			t.Fatalf("asset order[%d] = %q, want %q", i, updated.ImageAssetOrder[i], asset.ID)
		}
	}
	if len(result.Partials) != 1 {
		t.Fatalf("partials len = %d, want 1 streamed preview record", len(result.Partials))
	}
}

func TestGenerateRejectsPartialOnlyResultWithoutSaving(t *testing.T) {
	svc, _, threadID, storagePath := newImageServiceTestHarness(t, codex.ImageGenerationResult{
		PartialImages: []codex.ImageGenerationPartialImage{{Base64Image: "preview", OutputIndex: 0}},
	})

	_, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err == nil || !strings.Contains(err.Error(), "no final PNG") {
		t.Fatalf("Generate error = %v, want no final PNG error", err)
	}
	entries, readErr := os.ReadDir(storagePath)
	if readErr != nil {
		t.Fatalf("read storage dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("storage dir file count = %d, want 0 after failure", len(entries))
	}
}

func TestGenerateUsesProviderResultWithGeneratingStatusWithoutStreamRecovery(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	client := &fakeCodexImageClient{result: codex.ImageGenerationResult{
		CallID:           "ig_generating",
		DecodedPNG:       testPNGBytes(),
		ProviderResponse: map[string]any{"id": "resp_generating", "status": "generating"},
	}}
	svc, _, threadID, storagePath := newImageServiceTestHarnessWithDataHome(t, dataHome, client)
	logPath := filepath.Join(dataHome, "main", "imagegen.log")

	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Assets) != 1 {
		t.Fatalf("assets len = %d, want 1 provider-result asset", len(result.Assets))
	}
	assertSavedPNG(t, storagePath, result.Assets[0].Path)
	assertImageGenerationLogContains(t, logPath,
		"[swarmd.imagegen] stage=provider_call_done",
		"result_count=0 decoded_png_bytes=12",
		"[swarmd.imagegen] stage=file_write_done",
		"[swarmd.imagegen] stage=success",
	)
	assertImageGenerationLogExcludes(t, logPath, "stage=final_validation_stream_recovery")
}

func TestGenerateRecoversLatestValidStreamFrameWhenFinalResultMissing(t *testing.T) {
	preview := base64.StdEncoding.EncodeToString(testPNGBytes())
	client := &fakeCodexImageClient{
		result: codex.ImageGenerationResult{PartialImages: []codex.ImageGenerationPartialImage{{Base64Image: preview, OutputIndex: 0}}},
		onGenerate: func(req codex.ImageGenerationRequest) {
			if req.OnEvent == nil {
				return
			}
			req.OnEvent(codex.ImageGenerationStreamEvent{
				Type:              codex.ImageGenerationStreamEventPartialImage,
				ItemID:            "ig_stream",
				OutputIndex:       0,
				PartialImageIndex: 1,
				PartialImageB64:   preview,
				OutputFormat:      "png",
			})
		},
	}
	svc, threads, threadID, storagePath := newImageServiceTestHarnessWithClient(t, client)

	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Assets) != 1 {
		t.Fatalf("assets len = %d, want 1 stream recovery asset", len(result.Assets))
	}
	assertSavedPNG(t, storagePath, result.Assets[0].Path)
	updated, ok, err := threads.GetForAccount("account-1", threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	if len(updated.ImageAssets) != 1 {
		t.Fatalf("thread assets = %#v, want recovered stream frame persisted", updated.ImageAssets)
	}
}

func TestGenerateRejectsIncompleteMultiImageResponseWithoutSaving(t *testing.T) {
	client := &fakeCodexImageClient{result: codex.ImageGenerationResult{Results: []codex.ImageGenerationResult{
		{CallID: "ig_1", DecodedPNG: testPNGBytes()},
		{CallID: "ig_2", DecodedPNG: testPNGBytes()},
	}}}
	svc, threads, threadID, storagePath := newImageServiceTestHarnessWithClient(t, client)

	_, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make three squares",
		Count:     3,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err == nil || !strings.Contains(err.Error(), "returned 2 final image") {
		t.Fatalf("Generate error = %v, want multi-image count mismatch", err)
	}
	entries, readErr := os.ReadDir(storagePath)
	if readErr != nil {
		t.Fatalf("read storage dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("storage dir file count = %d, want 0 after failure", len(entries))
	}
	updated, ok, err := threads.GetForAccount("account-1", threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	if len(updated.ImageAssets) != 0 || len(updated.ImageAssetOrder) != 0 {
		t.Fatalf("thread assets/order = %#v / %#v, want no persisted assets", updated.ImageAssets, updated.ImageAssetOrder)
	}
}

func TestGenerateRejectsProviderReturningMultipleFinalsForOneSlotWithoutSaving(t *testing.T) {
	svc, _, threadID, storagePath := newImageServiceTestHarness(t, codex.ImageGenerationResult{
		Results: []codex.ImageGenerationResult{
			{CallID: "ig_1", OutputIndex: 0, DecodedPNG: testPNGBytes()},
			{CallID: "ig_2", OutputIndex: 1, DecodedPNG: testPNGBytes()},
		},
	})

	_, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make one square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err == nil || !strings.Contains(err.Error(), "returned 2 final image") {
		t.Fatalf("Generate error = %v, want one-slot mismatch", err)
	}
	entries, readErr := os.ReadDir(storagePath)
	if readErr != nil {
		t.Fatalf("read storage dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("storage dir file count = %d, want 0 after failure", len(entries))
	}
}

// TestGenerateRejectsNonPNGPayloadBeforeSaving proves Generate validates the
// provider payload before any managed file is written. The imagegen package
// test is the narrowest layer that observes both rejection and no-write state.
func TestGenerateRejectsNonPNGPayloadBeforeSaving(t *testing.T) {
	svc, _, threadID, storagePath := newImageServiceTestHarness(t, codex.ImageGenerationResult{CallID: "ig", DecodedPNG: []byte("not-a-png")})

	_, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("Generate error = %v, want non-PNG rejection", err)
	}
	entries, readErr := os.ReadDir(storagePath)
	if readErr != nil {
		t.Fatalf("read storage dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("storage dir file count = %d, want 0 after failure", len(entries))
	}
}

// TestResolveAssetPathRejectsPathOutsideManagedStorage proves stored asset
// metadata cannot make resolution escape app-managed workspace storage. The
// package test directly observes the service containment rejection.
func TestResolveAssetPathRejectsPathOutsideManagedStorage(t *testing.T) {
	svc, threads, threadID, _ := newImageServiceTestHarness(t, codex.ImageGenerationResult{CallID: "ig", DecodedPNG: testPNGBytes()})
	thread, ok, err := threads.GetForAccount("account-1", threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	thread.ImageAssets = []pebblestore.ImageAssetSnapshot{{ID: "asset_escape", Name: "bad.png", Path: filepath.Join(t.TempDir(), "bad.png"), Extension: "png"}}
	thread.ImageAssetOrder = []string{"asset_escape"}
	if _, err := threads.UpdateForAccount("account-1", thread); err != nil {
		t.Fatalf("update thread: %v", err)
	}

	_, _, err = svc.ResolveAssetPathForPrincipal(testImagePrincipal(), threadID, "asset_escape")
	if err == nil || !strings.Contains(err.Error(), "outside managed session storage") {
		t.Fatalf("ResolveAssetPath error = %v, want managed storage rejection", err)
	}
}

func TestGenerateReturnsProviderResponseOnSuccess(t *testing.T) {
	svc, _, threadID, _ := newImageServiceTestHarness(t, codex.ImageGenerationResult{
		CallID:           "ig",
		DecodedPNG:       testPNGBytes(),
		ProviderResponse: map[string]any{"id": "resp_raw", "status": "completed"},
	})
	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider:  ProviderCodexOpenAI,
		Model:     "gpt-5.5",
		Prompt:    "make a square",
		Count:     1,
		Principal: testImagePrincipal(),
		Target:    GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.ProviderResponse == nil || result.ProviderResponse["id"] != "resp_raw" {
		t.Fatalf("provider response = %#v, want raw provider response", result.ProviderResponse)
	}
}

func TestCodexUnavailableCapabilityRequiresOAuth(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.SetCodexAPIKeyForAccount("account-1", "sk-test"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	svc := NewService(&fakeCodexImageClient{}, authStore, nil)
	caps, err := svc.Capabilities(identity.ContextWithPrincipal(context.Background(), testImagePrincipal()))
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps.Providers) == 0 || caps.Providers[0].Ready {
		t.Fatalf("codex provider ready = %#v, want not ready for API key auth", caps.Providers)
	}
}

func newImageServiceTestHarness(t *testing.T, result codex.ImageGenerationResult) (*Service, *pebblestore.ImageThreadStore, string, string) {
	t.Helper()
	return newImageServiceTestHarnessWithClient(t, &fakeCodexImageClient{result: result})
}

func newImageServiceTestHarnessWithClient(t *testing.T, client *fakeCodexImageClient) (*Service, *pebblestore.ImageThreadStore, string, string) {
	t.Helper()
	return newImageServiceTestHarnessWithDataHome(t, filepath.Join(t.TempDir(), "data"), client)
}

func newImageServiceTestHarnessWithDataHome(t *testing.T, dataHome string, client *fakeCodexImageClient) (*Service, *pebblestore.ImageThreadStore, string, string) {
	t.Helper()
	home := filepath.Join(filepath.Dir(dataHome), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create test home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", dataHome)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspacePath := t.TempDir()
	threadID := "thread-image-test"
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", threadID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	threads := pebblestore.NewImageThreadStore(store)
	if _, err := threads.CreateForAccount("account-1", "user-1", pebblestore.ImageThreadSnapshot{
		ID:            threadID,
		WorkspacePath: workspacePath,
		Title:         "Images",
		ImageFolders:  []string{filepath.Join(workspacePath, ".swarm", "old")},
		Metadata:      map[string]any{"tool_storage_path": filepath.Join(workspacePath, ".swarm", "old")},
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	authStore := pebblestore.NewAuthStoreWithSecretStore(store, store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "codex", AccountScopeID: "account-1", ID: "test", Type: pebblestore.CodexAuthTypeOAuth, AccessToken: "test-access", RefreshToken: "test-refresh", SetActive: true}); err != nil {
		t.Fatalf("upsert codex oauth: %v", err)
	}
	return NewService(client, authStore, threads), threads, threadID, storagePath
}

func assertSavedPNG(t *testing.T, storagePath string, assetPath string) {
	t.Helper()
	if !pathWithinRoot(storagePath, assetPath) {
		t.Fatalf("asset path %q is outside storage %q", assetPath, storagePath)
	}
	info, err := os.Stat(assetPath)
	if err != nil {
		t.Fatalf("stat asset: %v", err)
	}
	if info.IsDir() || info.Size() <= 0 {
		t.Fatalf("asset info = %#v, want non-empty file", info)
	}
	got, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if !looksLikePNG(got) {
		t.Fatalf("written bytes are not png: %q", string(got))
	}
}

func assertImageGenerationLogExcludes(t *testing.T, logPath string, substrings ...string) {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read imagegen log %q: %v", logPath, err)
	}
	for _, substring := range substrings {
		if strings.Contains(string(content), substring) {
			t.Fatalf("imagegen log unexpectedly contains %q\nlog:\n%s", substring, string(content))
		}
	}
}

func assertImageGenerationLogContains(t *testing.T, logPath string, substrings ...string) {
	t.Helper()
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat imagegen log %q: %v", logPath, err)
	}
	if info.IsDir() || info.Size() <= 0 {
		t.Fatalf("imagegen log info = %#v, want non-empty file", info)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read imagegen log %q: %v", logPath, err)
	}
	for _, substring := range substrings {
		if !strings.Contains(string(content), substring) {
			t.Fatalf("imagegen log missing %q\nlog:\n%s", substring, string(content))
		}
	}
}

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
