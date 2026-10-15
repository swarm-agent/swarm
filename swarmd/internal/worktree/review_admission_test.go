package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: allocateReviewWorkspace must report deadline causes and remove both
// checkout and registration after interrupted Git. Real Git plus a deliberately
// stalled command wrapper is a failure-injection test, not a load benchmark.
func TestReviewAdmissionTimeoutCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	command("init", "-b", "dev")
	command("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	head := command("rev-parse", "HEAD")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", git)
	script := "#!/bin/sh\n\"$REAL_GIT\" \"$@\" || exit $?\nif [ \"$3\" = worktree ] && [ \"$4\" = add ]; then exec sleep 10; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := &Service{}
	_, err = s.allocateReviewWorkspaceTimeout(repo, "timeout-review", head, 500*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "review checkout admission") {
		t.Fatalf("missing cause: %v", err)
	}
	path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID("timeout-review"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover checkout: %v", err)
	}
	if strings.Contains(command("worktree", "list", "--porcelain"), path) {
		t.Fatal("leftover registration")
	}
	if err := s.CleanupReviewAdmission(repo, "timeout-review"); err != nil {
		t.Fatal("not idempotent", err)
	}
	// A crash after folder removal but before registration pruning must recover.
	command("worktree", "add", "--detach", path, head)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupReviewAdmission(repo, "timeout-review"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command("worktree", "list", "--porcelain"), path) {
		t.Fatal("missing-folder registration retained")
	}
	// Refuse a substituted path without deleting its target.
	target := t.TempDir()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupReviewAdmission(repo, "timeout-review"); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("target changed", err)
	}
}
