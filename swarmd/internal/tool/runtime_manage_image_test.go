package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDefinitionsExposeCanonicalManageImageOnly(t *testing.T) {
	rt := NewRuntime(1)
	found := false
	for _, definition := range rt.Definitions() {
		switch definition.Name {
		case "manage-image":
			found = true
		case "manage_image":
			t.Fatalf("manage_image must not be exposed as a documented tool definition")
		}
	}
	if !found {
		t.Fatal("manage-image definition not found")
	}
}

func TestManageImageInspectShape(t *testing.T) {
	rt := NewRuntime(1)
	rt.SetManageImageServices(fakeManageImageService{caps: imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{
		ID:           imagegen.ProviderGoogleGemini,
		Label:        "Gemini",
		Ready:        true,
		DefaultModel: "gemini-test",
		Models:       []string{"gemini-test"},
	}}}}, nil)
	output, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: t.TempDir()}, Call{
		CallID:    "manage-image-inspect",
		Name:      "manage-image",
		Arguments: `{"action":"inspect"}`,
	})
	if err != nil {
		t.Fatalf("inspect: %v output=%s", err, output)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal inspect: %v", err)
	}
	if payload["tool"] != "manage-image" || payload["action"] != "inspect" || payload["ready"] != true || payload["default_provider"] != imagegen.ProviderGoogleGemini {
		t.Fatalf("unexpected inspect payload: %#v", payload)
	}
	if _, ok := payload["providers"].([]any); !ok {
		t.Fatalf("providers missing from inspect payload: %#v", payload)
	}
}

func TestManageImageGenerateCompactRefsNoBase64(t *testing.T) {
	storageRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-run"))
	t.Setenv("STATE_DIRECTORY", storageRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	oldNewThreadID := newManageImageThreadID
	t.Cleanup(func() { newManageImageThreadID = oldNewThreadID })
	newManageImageThreadID = func() string { return "thread_1" }
	rt := NewRuntime(1)
	workspaceDir := t.TempDir()
	fakeSvc := &fakeManageImageGenerateService{caps: imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{
		ID:           imagegen.ProviderGoogleGemini,
		Ready:        true,
		DefaultModel: "gemini-test",
		Models:       []string{"gemini-test"},
	}}}}
	rt.SetManageImageServices(fakeSvc, &fakeManageImageThreadStore{})
	output, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: workspaceDir}, Call{
		CallID:    "manage-image-generate",
		Name:      "manage-image",
		Arguments: `{"action":"generate","prompt":"make one wallpaper","count":1}`,
	})
	if err != nil {
		t.Fatalf("generate: %v output=%s", err, output)
	}
	if strings.Contains(strings.ToLower(output), "base64") || strings.Contains(output, "iVBOR") {
		t.Fatalf("generate output contains raw/base64 image data: %s", output)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal generate: %v", err)
	}
	if payload["status"] != "completed" || payload["thread_id"] != "thread_1" || payload["saved_count"].(float64) != 1 {
		t.Fatalf("unexpected generate payload: %#v", payload)
	}
	assets, ok := payload["assets"].([]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("assets payload = %#v", payload["assets"])
	}
	asset := assets[0].(map[string]any)
	if asset["asset_id"] != "asset_1" || asset["url"] != "/v1/image/assets?thread_id=thread_1&asset_id=asset_1" {
		t.Fatalf("unexpected compact asset: %#v", asset)
	}
}

type fakeManageImageService struct {
	caps imagegen.Capabilities
}

func (f fakeManageImageService) Capabilities(context.Context) (imagegen.Capabilities, error) {
	return f.caps, nil
}

func (f fakeManageImageService) Generate(context.Context, imagegen.GenerateRequest) (imagegen.GenerateResult, error) {
	return imagegen.GenerateResult{}, nil
}

type fakeManageImageGenerateService struct {
	caps imagegen.Capabilities
}

func (f *fakeManageImageGenerateService) Capabilities(context.Context) (imagegen.Capabilities, error) {
	return f.caps, nil
}

func (f *fakeManageImageGenerateService) Generate(_ context.Context, req imagegen.GenerateRequest) (imagegen.GenerateResult, error) {
	return imagegen.GenerateResult{
		Assets: []imagegen.GeneratedAsset{{
			ImageAssetSnapshot: pebblestore.ImageAssetSnapshot{ID: "asset_1", Name: "image-01.png", Path: "/host/private/image-01.png", Extension: "png", SizeBytes: 123},
			URL:                "/v1/image/assets?thread_id=" + req.Target.ThreadID + "&asset_id=asset_1",
			Provider:           req.Provider,
			Model:              req.Model,
		}},
		Target: &imagegen.WorkspaceImageSessionTargetInfo{Kind: imagegen.TargetWorkspaceImage, Thread: pebblestore.ImageThreadSnapshot{ID: req.Target.ThreadID}},
	}, nil
}

type fakeManageImageThreadStore struct{}

func (f *fakeManageImageThreadStore) Create(thread pebblestore.ImageThreadSnapshot) (pebblestore.ImageThreadSnapshot, error) {
	return thread, nil
}

func (f *fakeManageImageThreadStore) Get(threadID string) (pebblestore.ImageThreadSnapshot, bool, error) {
	return pebblestore.ImageThreadSnapshot{ID: threadID}, true, nil
}

func TestManageImageWithRealImagegenServiceSavesCompactRefsNoBase64(t *testing.T) {
	oldNewThreadID := newManageImageThreadID
	t.Cleanup(func() { newManageImageThreadID = oldNewThreadID })
	newManageImageThreadID = func() string { return "thread_real" }
	workspaceDir := t.TempDir()
	storageRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-run"))
	t.Setenv("STATE_DIRECTORY", storageRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	storePath := t.TempDir()
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	secretStore, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	defer secretStore.Close()
	threadStore := pebblestore.NewImageThreadStore(store)
	authStore := pebblestore.NewAuthStoreWithSecretStore(store, secretStore)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "codex", ID: "test", Type: pebblestore.CodexAuthTypeOAuth, AccessToken: "test-access-token", RefreshToken: "test-refresh-token", AccountID: "test-account", SetActive: true}); err != nil {
		t.Fatalf("put codex auth: %v", err)
	}
	svc := imagegen.NewService(fakeCodexImageClient{}, authStore, threadStore)
	rt := NewRuntime(1)
	rt.SetManageImageServices(svc, threadStore)
	output, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: workspaceDir}, Call{
		CallID:    "manage-image-real-generate",
		Name:      "manage-image",
		Arguments: `{"action":"generate","prompt":"make one wallpaper","provider":"codex","count":1}`,
	})
	if err != nil {
		t.Fatalf("generate: %v output=%s", err, output)
	}
	if strings.Contains(strings.ToLower(output), "base64") || strings.Contains(output, "iVBOR") {
		t.Fatalf("generate output contains raw/base64 image data: %s", output)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal generate: %v", err)
	}
	if payload["thread_id"] != "thread_real" || payload["saved_count"].(float64) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	thread, ok, err := threadStore.Get("thread_real")
	if err != nil || !ok {
		t.Fatalf("get thread: ok=%v err=%v", ok, err)
	}
	wantSuffix := filepath.Join("tools", "image", "sessions", "thread_real")
	if len(thread.ImageAssets) != 1 || len(thread.ImageFolders) != 1 || !strings.Contains(thread.ImageFolders[0], wantSuffix) {
		t.Fatalf("thread storage/assets not updated: %#v", thread)
	}
	if !strings.HasPrefix(filepath.Clean(thread.ImageFolders[0]), filepath.Clean(storageRoot)+string(filepath.Separator)) {
		t.Fatalf("image storage path = %q, want under daemon data root %q", thread.ImageFolders[0], storageRoot)
	}
}

type fakeCodexImageClient struct{}

func (fakeCodexImageClient) GenerateImage(context.Context, codex.ImageGenerationRequest) (codex.ImageGenerationResult, error) {
	return codex.ImageGenerationResult{
		CallID:      "ig_test",
		OutputIndex: 0,
		DecodedPNG:  testPNGBytes(),
	}, nil
}

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
