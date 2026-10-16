package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: worker allocation and commits must not write the source Git
// repository or depend on its GC. Exercise the allocator, identity and removal
// boundaries with real local Git; no provider or daemon is needed to prove it.
func TestWorkerCloneIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	config, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	a, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.BaseCommit != base || git(a.WorkspacePath, "branch", "--show-current") != "agent/job" {
		t.Fatal("wrong clone lineage", a)
	}
	if info, err := os.Lstat(filepath.Join(a.WorkspacePath, ".git")); err != nil || !info.IsDir() {
		t.Fatal("clone has no independent .git", err)
	}
	if _, err := os.Stat(filepath.Join(a.WorkspacePath, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatal("clone depends on alternates", err)
	}
	git(a.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "job")
	head := git(a.WorkspacePath, "rev-parse", "HEAD")
	if err := ValidateOwnedIdentity(repo, a.WorkspacePath, a.BranchName, base); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(git(repo, "worktree", "list", "--porcelain"), a.WorkspacePath) {
		t.Fatal("job registered as worktree")
	}
	if git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("job changed folder HEAD")
	}
	if out, err := exec.Command("git", "-C", repo, "cat-file", "-e", head).CombinedOutput(); err == nil {
		t.Fatalf("job wrote source objects: %s", out)
	}
	after, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil || string(after) != string(config) {
		t.Fatal("source config changed", err)
	}
	review, err := s.allocateReviewWorkspace(repo, "review-fixture", head)
	if err != nil {
		t.Fatal(err)
	}
	if git(review.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("review missed clone commit")
	}
	if err := s.RemoveReviewWorkspace(repo, review.WorkspacePath, "review-fixture", head); err != nil {
		t.Fatal(err)
	}
	// Failed duplicate admission must not destroy an existing job or its commits.
	if _, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-fixture", true); err == nil {
		t.Fatal("duplicate admitted")
	}
	if git(a.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("duplicate admission changed job")
	}
	if err := removeManagedWorktree(repo, a.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.WorkspacePath); !os.IsNotExist(err) {
		t.Fatal("clone retained", err)
	}
	if git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("cleanup changed folder")
	}
}

// Requirement: allocateWorkerClone preserves folder-local authorship, while
// missing components remain governed by the sandbox home, not clone config.
// Real Git commits through the service helper and bash prove author/committer
// postconditions without a daemon/provider; sandbox home setup is tested separately.
func TestWorkerCloneGitIdentity(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_CONFIG_COUNT"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			git := func(path string, args ...string) string {
				t.Helper()
				out, err := runGit(path, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			repo := t.TempDir()
			git(repo, "init", "-b", "dev")
			git(repo, "config", "--global", "user.name", "Swarm")
			git(repo, "config", "--global", "user.email", "swarm@localhost")
			want := "Swarm <swarm@localhost>"
			if local {
				git(repo, "config", "--local", "user.name", "Folder Author")
				git(repo, "config", "--local", "user.email", "folder@example.invalid")
				want = "Folder Author <folder@example.invalid>"
			}
			git(repo, "commit", "--allow-empty", "-m", "base")
			before, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			a, err := (&Service{}).allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/identity", "worker-execution-identity", true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := removeManagedWorktree(repo, a.WorkspacePath); err != nil {
					t.Error(err)
				}
			})
			for _, shell := range []bool{false, true} {
				if shell {
					cmd := exec.Command("bash", "-c", "git commit --allow-empty -m bash")
					cmd.Dir = a.WorkspacePath
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("bash commit: %v %s", err, out)
					}
				} else {
					git(a.WorkspacePath, "commit", "--allow-empty", "-m", "service")
				}
				if got := git(a.WorkspacePath, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != want+"|"+want {
					t.Fatalf("identity = %q, want %q", got, want)
				}
			}
			after, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
			if err != nil || string(before) != string(after) {
				t.Fatal("source config changed", err)
			}
			if !local {
				out, err := exec.Command("git", "-C", a.WorkspacePath, "config", "--local", "--get", "user.name").CombinedOutput()
				if err == nil || len(out) != 0 {
					t.Fatalf("default pinned in clone: %s %v", out, err)
				}
			}
		})
	}
}
