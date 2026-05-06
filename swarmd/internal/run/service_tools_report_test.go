package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
)

func TestPersistTaskReportUsesPrivateWorkspaceDataDir(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	workspace := filepath.Join(t.TempDir(), "repo")
	gotRef, err := persistTaskReport(workspace, "Session 1", "task_call/launch 2", "Clone B", "move reports", "full report body")
	if err != nil {
		t.Fatalf("persistTaskReport: %v", err)
	}

	wantRef := filepath.ToSlash(filepath.Join("reports", "session-1", "task-call-launch-2.md"))
	if gotRef != wantRef {
		t.Fatalf("report reference = %q, want %q", gotRef, wantRef)
	}
	if filepath.IsAbs(gotRef) || strings.Contains(gotRef, workspace) || strings.Contains(gotRef, dataRoot) {
		t.Fatalf("report reference should be controlled and non-absolute, got %q", gotRef)
	}

	reportDir, err := appstorage.WorkspaceDataDir(workspace, "reports", "session-1")
	if err != nil {
		t.Fatalf("WorkspaceDataDir: %v", err)
	}
	if !strings.HasSuffix(reportDir, filepath.Join("reports", "session-1")) {
		t.Fatalf("report dir = %q, want reports/session bucket", reportDir)
	}
	reportPath := filepath.Join(reportDir, "task-call-launch-2.md")
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(content), "full report body") || !strings.Contains(string(content), "- subagent: Clone B") {
		t.Fatalf("report content missing expected data: %q", string(content))
	}
	assertFileMode(t, reportPath, appstorage.PrivateFilePerm)
	assertFileMode(t, reportDir, appstorage.PrivateDirPerm)
	assertPathUnderRunTest(t, reportPath, filepath.Join(dataRoot, appstorage.WorkspacesDir))
	if _, err := os.Stat(filepath.Join(workspace, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf("persistTaskReport should not create workspace .swarm, stat err=%v", err)
	}
}

func assertPathUnderRunTest(t *testing.T, got, prefix string) {
	t.Helper()
	got = filepath.Clean(got)
	prefix = filepath.Clean(prefix)
	if got != prefix && !strings.HasPrefix(got, prefix+string(filepath.Separator)) {
		t.Fatalf("path %q is not under %q", got, prefix)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %v, want %v", path, got, want)
	}
}
