package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: primary-session recovery must recognize the exact named allocator,
// not infer a task-seed path. Program ownership must remain seed-bound. Real Git
// proves both accepted allocation forms and unchanged state on forged/dirty paths.
func TestSessionRepositoryLaneNamedAllocation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	source := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := runGit(path, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git(source, "init", "-b", "dev")
	git(source, "config", "user.name", "Fixture")
	git(source, "config", "user.email", "fixture@example.invalid")
	git(source, "commit", "--allow-empty", "-m", "base")
	svc := &Service{}
	named, err := svc.allocateSessionWorkspaceWithBranchMode(source, true, "", "agent/named-primary", "owner-one", true)
	if err != nil {
		t.Fatal(err)
	}
	base, err := svc.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	task, err := svc.AllocateTaskWorkspace(source, base, "owner-two", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := git(source, "worktree", "list", "--porcelain")
	if err := svc.ValidateSessionRepositoryLane(source, named.WorkspacePath, "owner-one", named.BranchName); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateSessionRepositoryLane(source, task.WorkspacePath, "owner-two", task.BranchName); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTaskRepositoryLane(source, named.WorkspacePath, "owner-one", named.BranchName); err == nil {
		t.Fatal("program validator accepted named path with wrong seed")
	}
	for _, test := range []struct{ lane, owner, branch string }{{named.WorkspacePath, "", "agent/named-primary"}, {named.WorkspacePath, "owner-one", "agent/wrong"}, {source, "owner-one", "dev"}} {
		if err := svc.ValidateSessionRepositoryLane(source, test.lane, test.owner, test.branch); err == nil {
			t.Fatal("invalid session lane accepted")
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(named.WorkspacePath, alias); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateSessionRepositoryLane(source, alias, "owner-one", named.BranchName); err == nil {
		t.Fatal("alias substituted allocation path")
	}
	if before != git(source, "worktree", "list", "--porcelain") {
		t.Fatal("validation changed inventory")
	}
	dirty := filepath.Join(named.WorkspacePath, "dirty")
	if err := os.WriteFile(dirty, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateSessionRepositoryLane(source, named.WorkspacePath, "owner-one", named.BranchName); err == nil {
		t.Fatal("dirty recovery accepted")
	}
	// Read-only inventory must inspect dirty owned lanes without weakening the
	// transition validator above, or accepting aliases, foreign seeds or branches.
	if err := svc.ValidateSessionRepositoryLaneForRead(source, named.WorkspacePath, "owner-one", named.BranchName); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTaskRepositoryLaneForRead(source, task.WorkspacePath, "owner-two", task.BranchName); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTaskRepositoryLaneForRead(source, task.WorkspacePath, "foreign", task.BranchName); err == nil {
		t.Fatal("foreign read seed accepted")
	}
	if err := svc.ValidateSessionRepositoryLaneForRead(source, alias, "owner-one", named.BranchName); err == nil {
		t.Fatal("read alias accepted")
	}
	if err := svc.ValidateSessionRepositoryLaneForRead(source, named.WorkspacePath, "owner-one", "agent/wrong"); err == nil {
		t.Fatal("read branch mismatch accepted")
	}
	if before != git(source, "worktree", "list", "--porcelain") {
		t.Fatal("read validation changed inventory")
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "preserve" {
		t.Fatal("dirty data lost")
	}
}
