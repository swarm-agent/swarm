package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveCreatesManagedWorkspaceStorageMetadata(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	workspacePath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	store := openWorkspaceTestStore(t)
	svc := NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := svc.Add(workspacePath, "Repo", "", true); err != nil {
		t.Fatalf("Add workspace: %v", err)
	}
	resolution, err := svc.Resolve(workspacePath)
	if err != nil {
		t.Fatalf("Resolve workspace: %v", err)
	}

	if resolution.ManagedWorkspaceBucket == "" {
		t.Fatalf("managed workspace bucket was not populated")
	}
	assertUnder(t, resolution.ManagedDataPath, filepath.Join(dataRoot, "workspaces", resolution.ManagedWorkspaceBucket))
	assertUnder(t, resolution.ManagedCachePath, filepath.Join(cacheRoot, "workspaces", resolution.ManagedWorkspaceBucket))
	assertUnder(t, resolution.ManagedStatePath, filepath.Join(dataRoot, "workspaces", resolution.ManagedWorkspaceBucket))
	for _, path := range []string{resolution.ManagedDataPath, resolution.ManagedCachePath, resolution.ManagedStatePath} {
		if strings.Contains(path, filepath.Join(workspacePath, ".swarm")) {
			t.Fatalf("managed path uses workspace .swarm: %q", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat managed path %q: %v", path, err)
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Fatalf("managed path %q mode = %v, want 0700", path, mode)
		}
	}
}

func openWorkspaceTestStore(t *testing.T) *pebblestore.Store {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "db.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertUnder(t *testing.T, path, prefix string) {
	t.Helper()
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if path != prefix && !strings.HasPrefix(path, prefix+string(filepath.Separator)) {
		t.Fatalf("path %q is not under %q", path, prefix)
	}
}
