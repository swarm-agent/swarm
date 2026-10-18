package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
)

// Requirement: daemon file tools preserve Bubblewrap's read-only Git metadata
// boundary. Threat: rewriting a linked .git pointer or repository config via
// write/edit grants later canonical Git access outside the intended metadata.
// Owners: openRootedWorkspacePath, localGitMetadataMutable, executeWrite.
// Narrowest proof: actual file tool writes against private files with the real
// active local manager; refusal must leave bytes unchanged and source writable.
func TestLocalFileToolsPreserveGitMetadata(t *testing.T) {
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if !m.Active() {
		t.Fatalf("Bubblewrap required for this isolated test: %s", m.Status().Reason)
	}
	previous := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() { sandbox.SetDefault(previous) })
	root := t.TempDir()
	scope := WorkspaceScope{PrimaryPath: root, Roots: []string{root}}
	for _, entry := range []string{"linked/.git", "repo/.git/config"} {
		p := filepath.Join(root, entry)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := executeWrite(scope, map[string]any{"path": entry, "content": "replacement"}); err == nil {
			t.Fatal("Git metadata write accepted")
		}
		got, err := os.ReadFile(p)
		if err != nil || string(got) != "original" {
			t.Fatal("refused Git metadata changed")
		}
	}
	if _, err := executeWrite(scope, map[string]any{"path": "source.txt", "content": "allowed"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("repo/.git/config", filepath.Join(root, "config-alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWrite(scope, map[string]any{"path": "config-alias", "content": "replacement"}); err == nil {
		t.Fatal("symlink alias to metadata accepted")
	}
	got, err := os.ReadFile(filepath.Join(root, "repo/.git/config"))
	if err != nil || string(got) != "original" {
		t.Fatal("aliased metadata changed")
	}
	if err := os.Symlink("repo/.git", filepath.Join(root, "git-alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWrite(scope, map[string]any{"path": "git-alias/newdir/newfile", "content": "replacement"}); err == nil {
		t.Fatal("missing nested path through metadata alias accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "repo/.git/newdir")); !os.IsNotExist(err) {
		t.Fatal("refused mutation created Git administration directory")
	}
	if err := os.Mkdir(filepath.Join(root, "safe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("safe", filepath.Join(root, "swapped")); err != nil {
		t.Fatal(err)
	}
	target, err := openRootedWorkspacePath(scope, "swapped/newdir/file")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	// Deterministically exchange the alias after the authority/path precheck,
	// before mkdir/open. Anchored no-follow traversal must still refuse it.
	if err := os.Remove(filepath.Join(root, "swapped")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("repo/.git", filepath.Join(root, "swapped")); err != nil {
		t.Fatal(err)
	}
	if err := target.mkdirParent(); err == nil {
		t.Fatal("post-validation symlink swap accepted")
	}
	if _, err := target.openMutable(os.O_CREATE|os.O_WRONLY, 0600); err == nil {
		t.Fatal("post-validation open accepted symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "repo/.git/newdir")); !os.IsNotExist(err) {
		t.Fatal("swap refusal modified metadata")
	}
	if _, err := executeWrite(scope, map[string]any{"path": "safe/newdir/source.txt", "content": "allowed"}); err != nil {
		t.Fatal(err)
	}
}
