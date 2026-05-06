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

func TestWorkspaceDirsUsePlatformRootsAndPrivatePermissions(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("STATE_DIRECTORY", stateRoot)
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

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

	wantDataPrefix := filepath.Join(stateRoot, WorkspacesDir, bucket)
	wantCachePrefix := filepath.Join(cacheRoot, WorkspacesDir, bucket)
	wantStatePrefix := filepath.Join(stateRoot, WorkspacesDir, bucket)
	assertPathUnder(t, dataDir, wantDataPrefix)
	assertPathUnder(t, cacheDir, wantCachePrefix)
	assertPathUnder(t, stateDir, wantStatePrefix)
	if strings.Contains(dataDir, filepath.Join(workspace, ".swarm")) || strings.Contains(cacheDir, filepath.Join(workspace, ".swarm")) || strings.Contains(stateDir, filepath.Join(workspace, ".swarm")) {
		t.Fatalf("workspace app-storage path must not use workspace .swarm: data=%q cache=%q state=%q", dataDir, cacheDir, stateDir)
	}

	assertMode(t, filepath.Join(stateRoot, WorkspacesDir), PrivateDirPerm)
	assertMode(t, wantDataPrefix, PrivateDirPerm)
	assertMode(t, filepath.Join(cacheRoot, WorkspacesDir), PrivateDirPerm)
	assertMode(t, wantCachePrefix, PrivateDirPerm)
	assertMode(t, wantStatePrefix, PrivateDirPerm)
	assertMode(t, dataDir, PrivateDirPerm)
	assertMode(t, cacheDir, PrivateDirPerm)
	assertMode(t, stateDir, PrivateDirPerm)
}

func TestStateDirUsesDurableDataRoot(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	runtimeRoot := filepath.Join(t.TempDir(), "run")
	t.Setenv("STATE_DIRECTORY", stateRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", runtimeRoot)
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	got, err := StateDir("metadata")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	assertPathUnder(t, got, filepath.Join(stateRoot, "metadata"))
	if strings.HasPrefix(got, runtimeRoot+string(filepath.Separator)) || got == runtimeRoot {
		t.Fatalf("StateDir used volatile runtime root: %q", got)
	}
}

func TestExistingAppParentDirectoriesAreHardened(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	workspacesDir := filepath.Join(stateRoot, WorkspacesDir)
	if err := os.MkdirAll(workspacesDir, 0o775); err != nil {
		t.Fatalf("mkdir existing app dirs: %v", err)
	}
	if err := os.Chmod(stateRoot, 0o775); err != nil {
		t.Fatalf("chmod app dir: %v", err)
	}
	if err := os.Chmod(workspacesDir, 0o775); err != nil {
		t.Fatalf("chmod workspaces dir: %v", err)
	}

	t.Setenv("STATE_DIRECTORY", stateRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	workspace := filepath.Join(t.TempDir(), "repo")
	if _, err := WorkspaceDataDir(workspace, "reports"); err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}

	assertMode(t, stateRoot, PrivateDirPerm)
	assertMode(t, workspacesDir, PrivateDirPerm)
}

func TestWritePrivateFileUses0600(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
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

func TestPathPartsCannotEscapeStorageRoot(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
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
