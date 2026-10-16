package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: reviewGitMounts and scopeRunArgs must admit independent clones while
// shielding both their Git metadata and every shared source mount. This tests
// mount construction, not Docker/kernel enforcement, at its narrowest boundary.
func TestReviewCloneReadOnlyGitMounts(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(t.TempDir(), "review")
	for _, path := range []string{filepath.Join(root, ".git"), filepath.Join(checkout, ".git")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	guards, err := reviewGitMounts(checkout, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 2 || guards[1].HostPath != filepath.Join(checkout, ".git") {
		t.Fatalf("guards: %+v", guards)
	}
	m := &Manager{cfg: Config{Image: "review-test"}}
	bucket := filepath.Dir(checkout)
	args := strings.Join(m.scopeRunArgs("review", root, "spec", []string{root, bucket}, SecretSetup{}, EnvironmentImage{Image: "review-test"}, checkout), " ")
	for _, path := range []string{root, bucket, filepath.Join(root, ".git"), filepath.Join(checkout, ".git")} {
		if !strings.Contains(args, "type=bind,src="+path+",dst="+path+",readonly") {
			t.Fatalf("missing read-only mount %s: %s", path, args)
		}
	}
}

// Purpose: preserve linked-worktree mounts and reject foreign/symlink metadata
// rather than granting access to a repository outside the registered project.
func TestReviewLinkedGitMounts(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	if err := os.MkdirAll(common, 0700); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []bool{false, true} {
		checkout := t.TempDir()
		gitdir := filepath.Join(common, "worktrees", "review")
		if foreign {
			gitdir = t.TempDir()
		}
		if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: "+gitdir+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := reviewGitMounts(checkout, root)
		if (err != nil) != foreign {
			t.Fatalf("foreign=%v: %v", foreign, err)
		}
	}
	checkout := t.TempDir()
	if err := os.Symlink(common, filepath.Join(checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewGitMounts(checkout, root); err == nil {
		t.Fatal("accepted symlink metadata")
	}
}
