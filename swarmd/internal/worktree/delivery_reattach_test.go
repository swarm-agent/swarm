package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: ReattachDeliveryLane accepts canonical-equivalent symlink paths but
// surfaces Git lock failures without changing HEAD. Real temporary Git lanes
// are the narrowest layer proving path normalization and mutation failure.
func TestReattachDeliveryLanePathsAndLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := runGit(path, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	repo := t.TempDir()
	git(repo, "init", "-b", "dev")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "base")
	lane := filepath.Join(t.TempDir(), "lane")
	git(repo, "worktree", "add", "-b", "review", lane)
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(lane, link); err != nil {
		t.Fatal(err)
	}
	git(lane, "checkout", "--detach", "HEAD")
	lock := filepath.Join(git(lane, "rev-parse", "--absolute-git-dir"), "HEAD.lock")
	if err := os.WriteFile(lock, []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReattachDeliveryLane(ctx, link, "review", repo, "dev"); err == nil || !strings.Contains(err.Error(), "HEAD.lock") {
		t.Fatalf("lock error: %v", err)
	}
	if git(lane, "rev-parse", "--symbolic-full-name", "HEAD") != "HEAD" {
		t.Fatal("lock failure changed HEAD")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	warning, err := ReattachDeliveryLane(ctx, link, "review", repo, "dev")
	if err != nil || warning == "" {
		t.Fatalf("reattach symlink: %s %v", warning, err)
	}
	if git(lane, "symbolic-ref", "--short", "HEAD") != "review" {
		t.Fatal("not reattached")
	}
}
