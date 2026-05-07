package appstorage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBucketNameStableDistinctAndNonLeaky(t *testing.T) {
	root := t.TempDir()
	workspaceA := filepath.Join(root, "private", "Users", "example-user", "Secret Project")
	workspaceB := filepath.Join(root, "private", "Users", "example-user", "Other Project")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspaceA: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspaceB: %v", err)
	}

	bucket1, err := WorkspaceBucketName(workspaceA)
	if err != nil {
		t.Fatalf("WorkspaceBucketName workspaceA: %v", err)
	}
	bucket2, err := WorkspaceBucketName(workspaceA)
	if err != nil {
		t.Fatalf("WorkspaceBucketName workspaceA second call: %v", err)
	}
	if bucket1 != bucket2 {
		t.Fatalf("bucket is not stable: %q != %q", bucket1, bucket2)
	}
	bucketB, err := WorkspaceBucketName(workspaceB)
	if err != nil {
		t.Fatalf("WorkspaceBucketName workspaceB: %v", err)
	}
	if bucket1 == bucketB {
		t.Fatalf("different workspaces mapped to the same bucket %q", bucket1)
	}
	if !strings.HasPrefix(bucket1, "secret-project-") {
		t.Fatalf("bucket should keep a short sanitized display slug, got %q", bucket1)
	}
	if strings.Contains(bucket1, string(filepath.Separator)) || strings.Contains(bucket1, "Users") || strings.Contains(bucket1, "example-user") || strings.Contains(bucket1, "private") {
		t.Fatalf("bucket leaks raw path details: %q", bucket1)
	}
}

func TestWorkspaceDirsUseStorageContractWorkspaceBucketsAndPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	cacheRoot := filepath.Join(root, "cache")
	runtimeRoot := filepath.Join(root, "runtime")
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", runtimeRoot)

	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(workspace, ".swarm"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	bucket, err := WorkspaceBucketName(workspace)
	if err != nil {
		t.Fatalf("WorkspaceBucketName: %v", err)
	}

	dataDir, err := WorkspaceDataDir(workspace, "reports", "session-1")
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	cacheDir, err := WorkspaceCacheDir(workspace, "worktrees")
	if err != nil {
		t.Fatalf("WorkspaceCacheDir: %v", err)
	}
	stateDir, err := WorkspaceStateDir(workspace, "metadata")
	if err != nil {
		t.Fatalf("WorkspaceStateDir: %v", err)
	}

	wantDataPrefix := filepath.Join(dataRoot, WorkspacesDir, bucket)
	wantCachePrefix := filepath.Join(cacheRoot, WorkspacesDir, bucket)
	wantStatePrefix := filepath.Join(runtimeRoot, WorkspacesDir, bucket)
	assertPathUnder(t, dataDir, wantDataPrefix)
	assertPathUnder(t, cacheDir, wantCachePrefix)
	assertPathUnder(t, stateDir, wantStatePrefix)
	if strings.Contains(dataDir, filepath.Join(workspace, ".swarm")) || strings.Contains(cacheDir, filepath.Join(workspace, ".swarm")) || strings.Contains(stateDir, filepath.Join(workspace, ".swarm")) {
		t.Fatalf("workspace app-storage path must not use workspace .swarm: data=%q cache=%q state=%q", dataDir, cacheDir, stateDir)
	}

	assertMode(t, dataRoot, PrivateDirPerm)
	assertMode(t, filepath.Join(dataRoot, WorkspacesDir), PrivateDirPerm)
	assertMode(t, wantDataPrefix, PrivateDirPerm)
	assertMode(t, cacheRoot, PrivateDirPerm)
	assertMode(t, filepath.Join(cacheRoot, WorkspacesDir), PrivateDirPerm)
	assertMode(t, wantCachePrefix, PrivateDirPerm)
	assertMode(t, runtimeRoot, PrivateDirPerm)
	assertMode(t, filepath.Join(runtimeRoot, WorkspacesDir), PrivateDirPerm)
	assertMode(t, wantStatePrefix, PrivateDirPerm)
	assertMode(t, dataDir, PrivateDirPerm)
	assertMode(t, cacheDir, PrivateDirPerm)
	assertMode(t, stateDir, PrivateDirPerm)
}

func TestExistingAppParentDirectoriesAreHardened(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	workspacesDir := filepath.Join(dataRoot, WorkspacesDir)
	if err := os.MkdirAll(workspacesDir, 0o775); err != nil {
		t.Fatalf("mkdir existing app dirs: %v", err)
	}
	if err := os.Chmod(dataRoot, 0o775); err != nil {
		t.Fatalf("chmod app dir: %v", err)
	}
	if err := os.Chmod(workspacesDir, 0o775); err != nil {
		t.Fatalf("chmod workspaces dir: %v", err)
	}

	t.Setenv("STATE_DIRECTORY", dataRoot)
	workspace := filepath.Join(t.TempDir(), "repo")
	if _, err := WorkspaceDataDir(workspace, "reports"); err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}

	assertMode(t, dataRoot, PrivateDirPerm)
	assertMode(t, workspacesDir, PrivateDirPerm)
}

func TestWritePrivateFileUses0600(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	workspace := filepath.Join(t.TempDir(), "repo")
	path, err := WorkspaceDataDir(workspace, "reports", "session-1")
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	filePath := filepath.Join(path, "launch.md")
	if err := WritePrivateFile(filePath, []byte("report")); err != nil {
		t.Fatalf("WritePrivateFile: %v", err)
	}
	assertMode(t, filePath, PrivateFilePerm)
}

func TestTempDirUsesCacheRoot(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	path, err := TempDir("unit-*", "remote-deploy")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(path)
	assertPathUnder(t, path, filepath.Join(cacheRoot, "tmp", "remote-deploy"))
}

func TestPathPartsCannotEscapeAppDirectory(t *testing.T) {
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	if _, err := CacheDir("workspaces", "..", "escape"); err == nil {
		t.Fatalf("CacheDir accepted escaping path part")
	}
	if _, err := CacheDir(filepath.Join(string(filepath.Separator), "tmp", "escape")); err == nil {
		t.Fatalf("CacheDir accepted absolute path part")
	}
}

func assertPathUnder(t *testing.T, got, prefix string) {
	t.Helper()
	got = filepath.Clean(got)
	prefix = filepath.Clean(prefix)
	if got != prefix && !strings.HasPrefix(got, prefix+string(filepath.Separator)) {
		t.Fatalf("path %q is not under %q", got, prefix)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %v, want %v", path, got, want)
	}
}
