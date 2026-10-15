package imagegen

import (
	"context"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"

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

type staticModelCatalog struct {
	records []pebblestore.ModelCatalogRecord
}

func (c staticModelCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return append([]pebblestore.ModelCatalogRecord(nil), c.records...), nil
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

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
