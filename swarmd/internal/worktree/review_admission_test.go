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
	// An unrelated incomplete registration cannot block owned cleanup; a
	// missing gitdir is attributable only through the checkout's reverse link.
	unknown := filepath.Join(repo, ".git", "worktrees", "unknown")
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	command("worktree", "add", "--detach", path, head)
	pointer, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	registration := strings.TrimSpace(strings.TrimPrefix(string(pointer), "gitdir: "))
	if err := os.Remove(filepath.Join(registration, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupReviewAdmission(repo, "timeout-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registration); !os.IsNotExist(err) {
		t.Fatal("incomplete owned registration retained", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unowned registration changed", err)
	}
	// Interrupted recursive pruning retains its exact owner in the tombstone.
	tombstone := filepath.Join(repo, ".git", "worktrees", ".swarm-review-cleanup-"+sessionWorkspaceID("timeout-review"))
	if err := os.Mkdir(tombstone, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupReviewAdmission(repo, "timeout-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatal("interrupted pruning retained", err)
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

// Purpose: allocation must wait through container timeout's termination grace,
// not kill only the client while Git can still write. A local engine-protocol
// fixture injects a stalled writer and a TERM handler; this is not Docker proof.
func TestAllocationWaitsForContainerWriter(t *testing.T) {
	root := t.TempDir()
	engine := filepath.Join(root, "engine")
	script := "#!/bin/sh\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "stopped")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, engine, "exec", "timeout", "--kill-after=2", "1", "sh", "-c", `trap 'echo stopped > "$1"; exit 0' TERM; while :; do sleep 0.1; done`, "writer", marker)
	waitForAllocationGit(cmd)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected timeout")
	}
	if ctx.Err() == nil {
		t.Fatal("did not cross client deadline")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("returned before writer termination", err)
	}
}
