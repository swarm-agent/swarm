package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: RepositoryIdentity/ValidateOwnedIdentity must distinguish independent
// nested repositories from aliases, and reject subpaths/non-Git/stale branches
// without modifying either repository. Real Git is the narrowest authority proof.
func TestRepositoryIdentityIsolation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		git(path, "init", "-b", "dev")
		git(path, "config", "user.name", "Fixture")
		git(path, "config", "user.email", "fixture@example.invalid")
		if err := os.WriteFile(filepath.Join(path, ".gitignore"), []byte("nested/\nsub/\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(path, "add", ".gitignore")
		git(path, "commit", "-m", "fixture")
	}
	parent := filepath.Join(root, "parent")
	nested := filepath.Join(parent, "nested")
	repo(parent)
	repo(nested)
	lane := filepath.Join(root, "lane")
	git(parent, "worktree", "add", "-b", "agent/owned", lane)
	base := git(parent, "rev-parse", "HEAD")
	nestedHead := git(nested, "rev-parse", "HEAD")
	inventory := git(parent, "worktree", "list", "--porcelain")
	if err := ValidateOwnedIdentity(parent, lane, "agent/owned", base); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	a, err := RepositoryIdentity(alias)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RepositoryIdentity(parent)
	if err != nil || a != b {
		t.Fatal("alias created second authority")
	}
	if err := os.Mkdir(filepath.Join(parent, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(parent, "sub"), t.TempDir(), filepath.Join(root, "missing")} {
		if _, err := RepositoryIdentity(path); err == nil {
			t.Fatalf("invalid root accepted: %s", path)
		}
	}
	for _, test := range []struct{ source, lane, branch, base string }{
		{nested, lane, "agent/owned", base}, {parent, parent, "dev", base}, {parent, lane, "wrong", base}, {parent, lane, "agent/owned", strings.Repeat("f", 40)},
	} {
		if err := ValidateOwnedIdentity(test.source, test.lane, test.branch, test.base); err == nil {
			t.Fatal("mismatched identity accepted")
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnedIdentity(alias, lane, "agent/owned", base); err == nil {
		t.Fatal("replaced alias accepted")
	}
	if git(parent, "worktree", "list", "--porcelain") != inventory || git(parent, "rev-parse", "HEAD") != base || git(nested, "rev-parse", "HEAD") != nestedHead || git(parent, "status", "--porcelain") != "" || git(nested, "status", "--porcelain") != "" {
		t.Fatal("rejected identity mutated repositories")
	}
}
