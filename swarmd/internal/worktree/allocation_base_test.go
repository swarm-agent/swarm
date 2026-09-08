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

// Requirement: allocation base is the allocated checkout's exact commit, even
// when requested branch differs from source HEAD. Threat: false provenance and
// runtime ancestry rejection. Real Git allocation is the narrowest proof; failed
// branch allocation must leave the source and worktree inventory unchanged.
func TestSessionAllocationCapturesSelectedBase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(p string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", p}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
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
	base := git(repo, "rev-parse", "HEAD")
	git(repo, "branch", "chosen")
	git(repo, "commit", "--allow-empty", "-m", "later source")
	head := git(repo, "rev-parse", "HEAD")
	s := &Service{}
	a, err := s.allocateSessionWorkspaceWithBranchMode(repo, false, "chosen", "agent/base-proof", "base-proof", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.BaseCommit != base || git(a.WorkspacePath, "rev-parse", "HEAD") != base {
		t.Fatalf("wrong base: %+v", a)
	}
	if err := ValidateOwnedIdentity(repo, a.WorkspacePath, a.BranchName, a.BaseCommit); err != nil {
		t.Fatal(err)
	}
	transitionOwner := "abcdef0123456789"
	transition, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "", "session-"+transitionOwner[:12], false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateSessionRepositoryLaneForRead(repo, transition.WorkspacePath, transitionOwner, transition.BranchName); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateSessionRepositoryLaneForRead(repo, transition.WorkspacePath, "different-owner", transition.BranchName); err == nil {
		t.Fatal("foreign transition owner accepted")
	}
	inventory := git(repo, "worktree", "list", "--porcelain")
	if _, err := s.allocateSessionWorkspaceWithBranchMode(repo, false, "missing-branch", "agent/missing", "missing", true); err == nil {
		t.Fatal("missing branch accepted")
	}
	if git(repo, "rev-parse", "HEAD") != head || git(repo, "status", "--porcelain") != "" || git(repo, "worktree", "list", "--porcelain") != inventory {
		t.Fatal("rejection changed source or worktree inventory")
	}
}
