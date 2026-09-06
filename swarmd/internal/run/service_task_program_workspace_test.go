package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	worktree "swarm/packages/swarmd/internal/worktree"
	"testing"
)

func programFixtureGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func programFixtureRepo(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	programFixtureGit(t, path, "init", "-b", "dev")
	programFixtureGit(t, path, "config", "user.name", "Test")
	programFixtureGit(t, path, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "source.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, path, "add", "source.txt")
	programFixtureGit(t, path, "commit", "-m", "fixture")
	return path
}

// Purpose: the scheduler must preflight without allocating, then persist and
// reuse a distinct per-repository lane. Real Git and temporary Pebble exercise
// captured-checkout immutability, unauthorized source denial and dirty refusal.
func TestTaskProgramRepositoryLanePreflightReuseAndIsolation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	target := programFixtureRepo(t)
	sourceHead := programFixtureGit(t, target, "rev-parse", "HEAD")
	parent.TemporaryWorkspaceRoots = []string{target}
	svc.worktrees = &worktree.Service{}
	spec := &taskProgramSpec{ID: "lane-one", Stages: []taskProgramStage{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []taskProgramJob{{ID: "build", StageID: "build", RequestedSubagentType: "coder", OwnedScope: []string{"source.txt"}}}}
	initial, err := taskProgramInitialRecord(parentID, "run", "call", spec)
	if err != nil {
		t.Fatal(err)
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: initial}
	before := programFixtureGit(t, target, "worktree", "list", "--porcelain")
	if _, err := p.repositoryLane(target); err != nil {
		t.Fatal(err)
	}
	if got := programFixtureGit(t, target, "worktree", "list", "--porcelain"); got != before {
		t.Fatal("preflight allocated a lane")
	}
	p.record, _, err = svc.sessions.CreateTaskProgram(initial)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := p.repositoryLane(target)
	if err != nil {
		t.Fatal(err)
	}
	if lane == target || p.record.RepositoryLane == nil {
		t.Fatal("missing isolated durable binding")
	}
	programFixtureGit(t, lane, "config", "user.name", "Test")
	programFixtureGit(t, lane, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(lane, "source.txt"), []byte("integrated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, lane, "add", "source.txt")
	programFixtureGit(t, lane, "commit", "-m", "lane fixture")
	next := initial
	next.ProgramID = "lane-two"
	next.Definition.ID = "lane-two"
	next, _, err = svc.sessions.CreateTaskProgram(next)
	if err != nil {
		t.Fatal(err)
	}
	p.record = next
	reused, err := p.repositoryLane(target)
	if err != nil || reused != lane {
		t.Fatalf("reuse: %q %v", reused, err)
	}
	if got := programFixtureGit(t, target, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatal("captured checkout advanced")
	}
	denied := programFixtureRepo(t)
	before = programFixtureGit(t, denied, "worktree", "list", "--porcelain")
	if _, err := p.repositoryLane(denied); err == nil {
		t.Fatal("unauthorized source accepted")
	}
	if got := programFixtureGit(t, denied, "worktree", "list", "--porcelain"); got != before {
		t.Fatal("unauthorized source mutated")
	}
	if err := os.WriteFile(filepath.Join(lane, "source.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.repositoryLane(target); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty lane: %v", err)
	}
}
