package tool

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Requirement: a file or directory the write tool creates is group-writable
// (0664 / 0775), whatever the daemon's umask, so the agent's own commands in
// a project sandbox running as the folder's owner in Swarm's group can change
// it (gofmt -w, package managers); an existing file keeps its mode, and a
// file the group may not read gets no group write. Threat: the coder writes
// a file it then cannot format or update (observed: "gofmt -w: permission
// denied"). Owner: rootedWorkspacePath.mkdirParent and openMutable via
// executeWrite; a real temporary filesystem is the narrowest layer.
func TestWriteToolCreatesGroupWritableFiles(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	workspace := t.TempDir()
	scope := normalizeWorkspaceScope(workspace, nil)
	if _, err := executeWrite(scope, map[string]any{"path": filepath.Join("pkg", "sub", "new.go"), "content": "package sub\n"}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(workspace, "pkg"):                  0o775,
		filepath.Join(workspace, "pkg", "sub"):           0o775,
		filepath.Join(workspace, "pkg", "sub", "new.go"): 0o664,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}

	existing := filepath.Join(workspace, "kept.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWrite(scope, map[string]any{"path": "kept.txt", "content": "y"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(existing); info.Mode().Perm() != 0o600 {
		t.Fatalf("existing file mode changed to %o", info.Mode().Perm())
	}
	if got := sharedFileMode(0o600); got != 0o600 {
		t.Fatalf("private file got group write: %o", got)
	}
}
