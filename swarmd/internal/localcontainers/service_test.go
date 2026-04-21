package localcontainers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentRuntimeMountFallsBackToSharedRuntimeFFFLibWhenRepoRuntimeMissing(t *testing.T) {
	repoRoot := t.TempDir()
	sharedRoot := t.TempDir()
	t.Chdir(repoRoot)
	t.Setenv("SWARM_SHARED_RUNTIME_ROOT", sharedRoot)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SWARM_ROOT", "")
	t.Setenv("SWARM_GO_ROOT", "")
	t.Setenv("STARTUP_CWD", "")
	t.Setenv("SWARM_WEB_DIR", "")

	repoLibPath := filepath.Join(repoRoot, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so")
	if err := os.MkdirAll(filepath.Dir(repoLibPath), 0o755); err != nil {
		t.Fatalf("mkdir repo lib dir: %v", err)
	}

	binDir := filepath.Join(sharedRoot, "bin")
	toolDir := filepath.Join(sharedRoot, "libexec")
	libPath := filepath.Join(sharedRoot, "lib", "libfff_c.so")

	writeTestFile(t, filepath.Join(binDir, "swarmd"), "bin")
	writeTestFile(t, filepath.Join(toolDir, "rebuild"), "tool")
	writeTestFile(t, libPath, "fff")

	mount := CurrentRuntimeMount()
	if mount == nil {
		t.Fatal("CurrentRuntimeMount returned nil")
	}
	if got, want := mount.FFFLibPath, libPath; got != want {
		t.Fatalf("FFFLibPath = %q, want %q", got, want)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
