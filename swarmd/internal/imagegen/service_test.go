package imagegen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeCodexImageClient struct {
	result codex.ImageGenerationResult
	err    error
}

func (f fakeCodexImageClient) GenerateImage(context.Context, codex.ImageGenerationRequest) (codex.ImageGenerationResult, error) {
	return f.result, f.err
}

func TestGenerateWorkspaceImageSessionWritesManagedStorageAndUpdatesThread(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	workspacePath := t.TempDir()
	threadID := "thread-image-1"
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", threadID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	threads := pebblestore.NewImageThreadStore(store)
	if _, err := threads.Create(pebblestore.ImageThreadSnapshot{
		ID:            threadID,
		WorkspacePath: workspacePath,
		Title:         "Images",
		ImageFolders:  []string{storagePath},
		Metadata:      map[string]any{"tool_storage_path": storagePath},
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	svc := NewService(fakeCodexImageClient{result: codex.ImageGenerationResult{
		CallID:     "ig_test/../bad",
		DecodedPNG: testPNGBytes(),
	}}, nil, threads)
	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider: ProviderCodexOpenAI,
		Model:    "gpt-5.5",
		Prompt:   "make a square",
		Count:    1,
		Target:   GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Assets) != 1 {
		t.Fatalf("assets len = %d, want 1", len(result.Assets))
	}
	asset := result.Assets[0]
	if !strings.HasPrefix(asset.URL, "/v1/image/assets?") {
		t.Fatalf("asset URL = %q", asset.URL)
	}
	if !pathWithinRoot(storagePath, asset.Path) {
		t.Fatalf("asset path %q is outside storage %q", asset.Path, storagePath)
	}
	if strings.Contains(filepath.Base(asset.Path), "..") || strings.Contains(filepath.Base(asset.Path), string(filepath.Separator)) {
		t.Fatalf("asset filename was not sanitized: %q", asset.Path)
	}
	if got, err := os.ReadFile(asset.Path); err != nil || !looksLikePNG(got) {
		t.Fatalf("written bytes = %q, %v", string(got), err)
	}
	updated, ok, err := threads.Get(threadID)
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	if len(updated.ImageAssets) != 1 || updated.ImageAssets[0].ID != asset.ID {
		t.Fatalf("thread assets = %#v, want generated asset", updated.ImageAssets)
	}
	if len(updated.ImageAssetOrder) != 1 || updated.ImageAssetOrder[0] != asset.ID {
		t.Fatalf("thread asset order = %#v, want generated asset", updated.ImageAssetOrder)
	}
}

func TestGenerateReturnsProviderResponseOnSuccess(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	workspacePath := t.TempDir()
	threadID := "thread-image-provider-response"
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", threadID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	threads := pebblestore.NewImageThreadStore(store)
	if _, err := threads.Create(pebblestore.ImageThreadSnapshot{
		ID:            threadID,
		WorkspacePath: workspacePath,
		Title:         "Images",
		Metadata:      map[string]any{"tool_storage_path": storagePath},
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	svc := NewService(fakeCodexImageClient{result: codex.ImageGenerationResult{
		CallID:           "ig",
		DecodedPNG:       testPNGBytes(),
		ProviderResponse: map[string]any{"id": "resp_raw", "status": "completed"},
	}}, nil, threads)
	result, err := svc.Generate(context.Background(), GenerateRequest{
		Provider: ProviderCodexOpenAI,
		Model:    "gpt-5.5",
		Prompt:   "make a square",
		Count:    1,
		Target:   GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.ProviderResponse == nil || result.ProviderResponse["id"] != "resp_raw" {
		t.Fatalf("provider response = %#v, want raw provider response", result.ProviderResponse)
	}
}

func TestGenerateWorkspaceImageSessionRejectsMismatchedStorage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	workspacePath := t.TempDir()
	threads := pebblestore.NewImageThreadStore(store)
	if _, err := threads.Create(pebblestore.ImageThreadSnapshot{
		ID:            "thread-image-2",
		WorkspacePath: workspacePath,
		Title:         "Images",
		Metadata:      map[string]any{"tool_storage_path": filepath.Join(workspacePath, ".swarm", "tools", "image", "sessions", "thread-image-2")},
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	svc := NewService(fakeCodexImageClient{result: codex.ImageGenerationResult{CallID: "ig", DecodedPNG: testPNGBytes()}}, nil, threads)
	_, err = svc.Generate(context.Background(), GenerateRequest{
		Provider: ProviderCodexOpenAI,
		Model:    "gpt-5.5",
		Prompt:   "make a square",
		Count:    1,
		Target:   GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: "thread-image-2"},
	})
	if err == nil || !strings.Contains(err.Error(), "legacy workspace .swarm") {
		t.Fatalf("Generate error = %v, want legacy storage rejection", err)
	}
}

func TestGenerateRejectsNonPNGPayload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	workspacePath := t.TempDir()
	threadID := "thread-image-non-png"
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", threadID)
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	threads := pebblestore.NewImageThreadStore(store)
	if _, err := threads.Create(pebblestore.ImageThreadSnapshot{
		ID:            threadID,
		WorkspacePath: workspacePath,
		Title:         "Images",
		Metadata:      map[string]any{"tool_storage_path": storagePath},
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	svc := NewService(fakeCodexImageClient{result: codex.ImageGenerationResult{CallID: "ig", DecodedPNG: []byte("not-a-png")}}, nil, threads)
	_, err = svc.Generate(context.Background(), GenerateRequest{
		Provider: ProviderCodexOpenAI,
		Model:    "gpt-5.5",
		Prompt:   "make a square",
		Count:    1,
		Target:   GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID},
	})
	if err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("Generate error = %v, want non-PNG rejection", err)
	}
}

func TestCodexUnavailableCapabilityRequiresOAuth(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.SetCodexAPIKey("sk-test"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	svc := NewService(fakeCodexImageClient{}, authStore, nil)
	caps, err := svc.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps.Providers) == 0 || caps.Providers[0].Ready {
		t.Fatalf("codex provider ready = %#v, want not ready for API key auth", caps.Providers)
	}
}

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
