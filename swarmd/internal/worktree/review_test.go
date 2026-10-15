package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: allocateReviewWorkspace and RemoveReviewWorkspace must isolate an
// exact immutable review head and discard dirty outputs without changing source
// refs. Real temporary Git worktrees are the narrowest observable proof.
func TestReviewerExactHeadAndCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(root, "source")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-b", "dev")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	git(repo, "commit", "--allow-empty", "-m", "base")
	selected := git(repo, "rev-parse", "HEAD")
	git(repo, "commit", "--allow-empty", "-m", "later")
	head := git(repo, "rev-parse", "HEAD")
	s := &Service{}
	a, err := s.allocateReviewWorkspace(repo, "review-owner", selected)
	if err != nil {
		t.Fatal(err)
	}
	if a.BaseCommit != selected || git(a.WorkspacePath, "rev-parse", "HEAD") != selected {
		t.Fatal(a)
	}
	if err := s.ValidateSessionRepositoryLaneForRead(repo, a.WorkspacePath, "review-owner", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.allocateReviewWorkspace(repo, "bad-owner", "dev"); err == nil {
		t.Fatal("non-exact head accepted")
	}
	if err := os.WriteFile(filepath.Join(a.WorkspacePath, "output"), []byte("build"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "foreign-owner", selected); err == nil {
		t.Fatal("foreign cleanup accepted")
	}
	if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "review-owner", head); err == nil {
		t.Fatal("wrong head cleanup accepted")
	}
	if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "review-owner", selected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.WorkspacePath); !os.IsNotExist(err) {
		t.Fatal("checkout retained", err)
	}
	if git(repo, "rev-parse", "HEAD") != head || git(repo, "status", "--porcelain") != "" {
		t.Fatal("source changed")
	}
	if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "review-owner", selected); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
	// Torn cleanup removes only its target registration. Another unavailable
	// job must remain registered: real Git proves this isolation boundary.
	other, err := s.allocateReviewWorkspace(repo, "other-owner", selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(other.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"pointer", "directory"} {
		t.Run(missing, func(t *testing.T) {
			a, err := s.allocateReviewWorkspace(repo, "review-owner", selected)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.WorkspacePath, "leftover"), []byte("must be deleted"), 0600); err != nil {
				t.Fatal(err)
			}
			if missing == "pointer" {
				err = os.Remove(filepath.Join(a.WorkspacePath, ".git"))
			} else {
				err = os.RemoveAll(a.WorkspacePath)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "foreign", selected); err == nil {
				t.Fatal("foreign removal accepted")
			}
			if err := s.RemoveReviewWorkspace(repo, a.WorkspacePath, "review-owner", selected); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(git(repo, "worktree", "list", "--porcelain"), other.WorkspacePath) {
				t.Fatal("unrelated registration removed")
			}
			if strings.Contains(git(repo, "worktree", "list", "--porcelain"), a.WorkspacePath) {
				t.Fatal("stale registration retained")
			}
			if _, err := os.Lstat(a.WorkspacePath); !os.IsNotExist(err) {
				t.Fatal("torn checkout retained", err)
			}
		})
	}
}
