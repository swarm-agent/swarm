package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
	"testing"
	"time"
)

func programFixtureGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
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
	// The exemption is exact and internal, not a bypass available to tools or
	// a stale scheduler copy. Every denial must leave the persisted row intact.
	admitted := p.record
	for _, kind := range []string{"revision", "run", "parent", "lane"} {
		bad := admitted
		switch kind {
		case "revision":
			bad.Revision++
		case "run":
			bad.ReservationRunID = "foreign-run"
		case "parent":
			bad.ParentSessionID = "foreign-parent"
		case "lane":
			copy := *bad.RepositoryLane
			copy.Branch = "foreign"
			bad.RepositoryLane = &copy
		}
		if _, err := svc.sessions.TaskProgramRepositoryLanesForAdmission(bad); err == nil {
			t.Fatalf("accepted %s admission", kind)
		}
	}
	if _, err := svc.sessions.TaskProgramRepositoryLanes(parentID); err == nil {
		t.Fatal("ordinary lookup bypassed active scheduler")
	}
	if err := svc.sessions.EnsureWorkspaceTransitionIdle(parentID); err == nil {
		t.Fatal("workspace transition bypassed active scheduler")
	}
	if saved, _, err := svc.sessions.GetTaskProgram(parentID, admitted.ProgramID); err != nil || saved.Revision != admitted.Revision || *saved.RepositoryLane != *admitted.RepositoryLane {
		t.Fatalf("admission checks mutated record: %+v %v", saved, err)
	}
	programFixtureGit(t, lane, "config", "user.name", "Test")
	programFixtureGit(t, lane, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(lane, "source.txt"), []byte("integrated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, lane, "add", "source.txt")
	programFixtureGit(t, lane, "commit", "-m", "lane fixture")
	// A competing admitted scheduler must not reuse the live owner's lane.
	next := initial
	next.ProgramID = "lane-two"
	next.Definition.ID = "lane-two"
	next, _, err = svc.sessions.CreateTaskProgram(next)
	if err != nil {
		t.Fatal(err)
	}
	first := p.record
	p.record = next
	before = programFixtureGit(t, target, "worktree", "list", "--porcelain")
	if _, err := p.repositoryLane(target); err == nil {
		t.Fatal("competing active owner accepted")
	}
	owner := taskProgramScheduler{service: svc, parentSession: parent, record: first}
	if _, err := owner.programWorkspacePath(); err == nil {
		t.Fatal("bound scheduler ignored competing admission")
	}
	if saved, _, err := svc.sessions.GetTaskProgram(parentID, next.ProgramID); err != nil || saved.RepositoryLane != nil || saved.Revision != next.Revision || programFixtureGit(t, target, "worktree", "list", "--porcelain") != before {
		t.Fatalf("competing rejection mutated admission or Git: %+v %v", saved, err)
	}
	// A competing declaration arriving after lookup must also be rejected by
	// the atomic publication boundary, not only the scheduler preflight.
	if _, _, err := svc.sessions.TransitionTaskProgram(parentID, next.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: next.Revision, MutationID: "competing-bind", RepositoryLane: first.RepositoryLane}); err == nil {
		t.Fatal("store published competing lane binding")
	}
	if saved, _, err := svc.sessions.GetTaskProgram(parentID, next.ProgramID); err != nil || saved.RepositoryLane != nil || saved.Revision != next.Revision {
		t.Fatalf("competing publication changed row: %+v %v", saved, err)
	}
	blocked := pebblestore.TaskProgramStateBlocked
	if _, _, err := svc.sessions.TransitionTaskProgram(parentID, first.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: first.Revision, MutationID: "stop-first", State: &blocked}); err != nil {
		t.Fatal(err)
	}
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
	// Recovery must reject before binding a new program to dirty retained work.
	if _, _, err := svc.sessions.TransitionTaskProgram(parentID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: "stop-second", State: &blocked}); err != nil {
		t.Fatal(err)
	}
	third := initial
	third.ProgramID = "lane-three"
	third.Definition.ID = "lane-three"
	third, _, err = svc.sessions.CreateTaskProgram(third)
	if err != nil {
		t.Fatal(err)
	}
	p.record = third
	before = programFixtureGit(t, target, "worktree", "list", "--porcelain")
	if _, err := p.repositoryLane(target); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty recovery: %v", err)
	}
	saved, ok, err := svc.sessions.GetTaskProgram(parentID, third.ProgramID)
	if err != nil || !ok || saved.RepositoryLane != nil || saved.Revision != third.Revision {
		t.Fatalf("rejected recovery persisted binding: %+v %v", saved, err)
	}
	if p.record.RepositoryLane != nil || programFixtureGit(t, target, "worktree", "list", "--porcelain") != before || programFixtureGit(t, target, "rev-parse", "HEAD") != sourceHead {
		t.Fatal("rejected recovery mutated inventory, source or scheduler binding")
	}
	if content, err := os.ReadFile(filepath.Join(lane, "source.txt")); err != nil || string(content) != "dirty\n" {
		t.Fatalf("dirty work changed: %q %v", content, err)
	}
}
