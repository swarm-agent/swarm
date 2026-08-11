package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestResolveTaskBaseUsesExactHEADFromLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := runGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatalf("configure email: %v", err)
	}
	if _, err := runGit(repo, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("configure name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatalf("add fixture: %v", err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/refactor", linked, "HEAD"); err != nil {
		t.Fatalf("add linked worktree: %v", err)
	}
	base, err := (&Service{}).ResolveTaskBase(linked)
	if err != nil {
		t.Fatalf("ResolveTaskBase: %v", err)
	}
	head, err := runGit(linked, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve fixture HEAD: %v", err)
	}
	if base.RepoRoot != repo || base.ParentBranch != "agent/refactor" || base.BaseCommit != head {
		t.Fatalf("task base = %#v, want root=%q branch=agent/refactor commit=%q", base, repo, head)
	}
}

func TestResolveTaskBaseRejectsDirtyParentBeforeAllocation(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := runGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatalf("configure email: %v", err)
	}
	if _, err := runGit(repo, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("configure name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(repo, "add", "tracked.txt"); err != nil {
		t.Fatalf("add fixture: %v", err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty fixture: %v", err)
	}

	_, err := (&Service{}).ResolveTaskBase(repo)
	if err == nil || !strings.Contains(err.Error(), "parent worktree has uncommitted changes") || !strings.Contains(err.Error(), "commit or checkpoint") {
		t.Fatalf("ResolveTaskBase dirty error = %v", err)
	}
}

func TestTaskCommitDescendsFromRecordedBase(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := runGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatalf("configure email: %v", err)
	}
	if _, err := runGit(repo, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("configure name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if _, err := runGit(repo, "add", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-am", "child"); err != nil {
		t.Fatal(err)
	}
	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	ok, err := (&Service{}).TaskCommitDescendsFrom(repo, base, head)
	if err != nil || !ok {
		t.Fatalf("TaskCommitDescendsFrom = %t, %v", ok, err)
	}
	ok, err = (&Service{}).TaskCommitDescendsFrom(repo, head, base)
	if err != nil || ok {
		t.Fatalf("reverse TaskCommitDescendsFrom = %t, %v", ok, err)
	}
}

func TestVerifyTaskIntegrationWorkspaceRejectsPathOutsideManagedRepository(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "base.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	base, _ := runGit(repo, "rev-parse", "HEAD")

	const sessionID = "child-security"
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspace(repo, true, "", "agent", sessionID)
	if err != nil {
		t.Fatalf("allocate managed child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(allocation.WorkspacePath, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(allocation.WorkspacePath, "add", "child.txt")
	_, _ = runGit(allocation.WorkspacePath, "commit", "-m", "child")
	head, _ := runGit(allocation.WorkspacePath, "rev-parse", "HEAD")

	if _, err := svc.VerifyTaskIntegrationWorkspace(repo, allocation.WorkspacePath, sessionID, allocation.BranchName, base, head); err != nil {
		t.Fatalf("verify managed child: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/outside", outside, base); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyTaskIntegrationWorkspace(repo, outside, sessionID, "agent/outside", base, base); err == nil || !strings.Contains(err.Error(), "expected private managed path") {
		t.Fatalf("outside path rejection = %v", err)
	}
}

func TestApplyTaskIntegrationRejectsConcurrentRepositoryOwner(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "base.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	base, _ := runGit(repo, "rev-parse", "HEAD")
	owner, err := acquireIntegrationLock(repo)
	if err != nil {
		t.Fatalf("acquire fixture integration lock: %v", err)
	}
	defer owner.Release()

	_, err = (&Service{}).ApplyTaskIntegration(repo, TaskIntegrationPlan{ParentHead: base, Entries: []TaskIntegrationEntry{{SessionID: "child", BaseCommit: base, HeadCommit: strings.Repeat("a", 40)}}})
	if err == nil || !strings.Contains(err.Error(), "another Swarm integration owns this repository") {
		t.Fatalf("concurrent owner rejection = %v", err)
	}
}

func TestPrepareAndApplyTaskIntegrationIsDeterministic(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "add", "base.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	base, _ := runGit(repo, "rev-parse", "HEAD")
	childPath := filepath.Join(t.TempDir(), "child")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/child", childPath, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(childPath, "add", "child.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(childPath, "commit", "-m", "child"); err != nil {
		t.Fatal(err)
	}
	head, _ := runGit(childPath, "rev-parse", "HEAD")

	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, base, []TaskIntegrationChild{{SessionID: "child-session", BaseCommit: base, HeadCommit: head}})
	if err != nil {
		t.Fatalf("PrepareTaskIntegration: %v", err)
	}
	if len(plan.Commits) != 1 || plan.Commits[0] != head || len(plan.Entries) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err != nil {
		t.Fatalf("ApplyTaskIntegration: %v", err)
	}
	if result.ResultingParentHead == "" || result.ResultingParentHead == base {
		t.Fatalf("result = %#v", result)
	}
	if descends, err := svc.TaskCommitDescendsFrom(repo, head, result.ResultingParentHead); err != nil || descends {
		t.Fatalf("cherry-picked child unexpectedly reachable by ancestry: descends=%t err=%v", descends, err)
	}
	if integrated, err := svc.TaskCommitRangeIntegratedInto(repo, base, head, result.ResultingParentHead); err != nil || !integrated {
		t.Fatalf("cherry-picked child not classified integrated: integrated=%t err=%v", integrated, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "child.txt")); err != nil {
		t.Fatalf("integrated file: %v", err)
	}
}

func TestTaskIntegrationSkipsAlreadyIntegratedCommitAndAppliesRemainingCommit(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "base.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	base, _ := runGit(repo, "rev-parse", "HEAD")

	childPath := filepath.Join(t.TempDir(), "child")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/partial-batch", childPath, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "first.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(childPath, "add", "first.txt")
	_, _ = runGit(childPath, "commit", "-m", "first child commit")
	first, _ := runGit(childPath, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(childPath, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(childPath, "add", "second.txt")
	_, _ = runGit(childPath, "commit", "-m", "second child commit")
	second, _ := runGit(childPath, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent context\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "parent.txt")
	_, _ = runGit(repo, "commit", "-m", "parent context")
	if _, err := runGit(repo, "cherry-pick", first); err != nil {
		t.Fatalf("integrate first child commit fixture: %v", err)
	}
	parentHead, _ := runGit(repo, "rev-parse", "HEAD")
	if parentHead == first {
		t.Fatal("fixture must use a patch-equivalent cherry-pick with a distinct commit id")
	}

	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, parentHead, []TaskIntegrationChild{{SessionID: "partial-batch", BaseCommit: base, HeadCommit: second}})
	if err != nil {
		t.Fatalf("PrepareTaskIntegration: %v", err)
	}
	if got := strings.Join(plan.Commits, " "); got != second {
		t.Fatalf("required commits = %q, want only %s", got, second)
	}
	if got := strings.Join(plan.AlreadyIntegratedCommits, " "); got != first {
		t.Fatalf("already integrated commits = %q, want %s", got, first)
	}
	if len(plan.Entries) != 1 || strings.Join(plan.Entries[0].Commits, " ") != second || strings.Join(plan.Entries[0].AlreadyIntegratedCommits, " ") != first {
		t.Fatalf("integration entry = %#v", plan.Entries)
	}

	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err != nil {
		t.Fatalf("ApplyTaskIntegration: %v", err)
	}
	if result.ResultingParentHead == parentHead {
		t.Fatalf("parent HEAD did not advance: %#v", result)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "second.txt")); err != nil || string(content) != "second\n" {
		t.Fatalf("remaining commit content = %q, %v", content, err)
	}
	status, _ := runGit(repo, "status", "--porcelain=v1")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("parent left dirty after integration: %q", status)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "CHERRY_PICK_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cherry-pick state remains after integration: %v", err)
	}
	if integrated, err := svc.TaskCommitRangeIntegratedInto(repo, base, second, result.ResultingParentHead); err != nil || !integrated {
		t.Fatalf("multi-commit child range not classified integrated: integrated=%t err=%v", integrated, err)
	}
}

func TestApplyTaskIntegrationPreservesAuthorAndUsesConfiguredCommitter(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.name", "Parent Committer")
	_, _ = runGit(repo, "config", "user.email", "parent@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "base.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	base, _ := runGit(repo, "rev-parse", "HEAD")

	childPath := filepath.Join(t.TempDir(), "child")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/identity-child", childPath, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(childPath, "add", "child.txt")
	if _, err := runGit(childPath,
		"-c", "user.name=Child Author",
		"-c", "user.email=child@example.invalid",
		"commit", "-m", "child"); err != nil {
		t.Fatal(err)
	}
	childHead, _ := runGit(childPath, "rev-parse", "HEAD")
	childAuthorDate, _ := runGit(childPath, "show", "-s", "--format=%aI", childHead)

	t.Setenv("GIT_AUTHOR_NAME", "Injected Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "injected-author@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2001-02-03T04:05:06Z")
	t.Setenv("GIT_COMMITTER_NAME", "Injected Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "injected-committer@example.invalid")
	t.Setenv("GIT_COMMITTER_DATE", "2002-03-04T05:06:07Z")

	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, base, []TaskIntegrationChild{{SessionID: "identity-child", BaseCommit: base, HeadCommit: childHead}})
	if err != nil {
		t.Fatalf("PrepareTaskIntegration: %v", err)
	}
	if _, err := svc.ApplyTaskIntegration(repo, plan); err != nil {
		t.Fatalf("ApplyTaskIntegration: %v", err)
	}

	identity, _ := runGit(repo, "show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD")
	if identity != "Child Author|child@example.invalid|Parent Committer|parent@example.invalid" {
		t.Fatalf("integrated commit identity = %q", identity)
	}
	authorDate, _ := runGit(repo, "show", "-s", "--format=%aI", "HEAD")
	if authorDate != childAuthorDate {
		t.Fatalf("integrated author date = %q, want source author date %q", authorDate, childAuthorDate)
	}
	committerDate, _ := runGit(repo, "show", "-s", "--format=%cI", "HEAD")
	if committerDate == "2002-03-04T05:06:07+00:00" {
		t.Fatalf("integrated committer date used inherited override: %q", committerDate)
	}
}

func TestPrepareTaskIntegrationPreflightsCompleteStackAndLeavesParentUnchangedOnConflict(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "shared.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	base, _ := runGit(repo, "rev-parse", "HEAD")

	makeChild := func(branch, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), branch)
		if _, err := runGit(repo, "worktree", "add", "-b", "agent/"+branch, path, base); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "shared.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(path, "commit", "-am", branch); err != nil {
			t.Fatal(err)
		}
		head, _ := runGit(path, "rev-parse", "HEAD")
		return head
	}
	first := makeChild("first", "first\n")
	second := makeChild("second", "second\n")

	_, err := (&Service{}).PrepareTaskIntegration(repo, base, []TaskIntegrationChild{
		{SessionID: "first", BaseCommit: base, HeadCommit: first},
		{SessionID: "second", BaseCommit: base, HeadCommit: second},
	})
	var conflict *TaskIntegrationConflictError
	if !errors.As(err, &conflict) || conflict.SessionID != "second" || conflict.Commit != second {
		t.Fatalf("conflict = %#v, err = %v", conflict, err)
	}
	head, _ := runGit(repo, "rev-parse", "HEAD")
	status, _ := runGit(repo, "status", "--short")
	if head != base || strings.TrimSpace(status) != "" {
		t.Fatalf("parent mutated during preflight: head=%s status=%q", head, status)
	}
}

func TestPrepareTaskIntegrationRejectsStaleParentHead(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "add", "base.txt")
	_, _ = runGit(repo, "commit", "-m", "base")
	_, err := (&Service{}).PrepareTaskIntegration(repo, strings.Repeat("a", 40), []TaskIntegrationChild{{SessionID: "child", BaseCommit: strings.Repeat("b", 40), HeadCommit: strings.Repeat("c", 40)}})
	if err == nil || !strings.Contains(err.Error(), "stale parent HEAD") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeterministicSessionWorktreePathUsesPrivateWorktreeDataDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	repoRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	got, err := deterministicSessionWorktreePath(repoRoot, "ws_abc123")
	if err != nil {
		t.Fatalf("deterministicSessionWorktreePath: %v", err)
	}
	wantRoot, err := appstorage.WorktreeDataDir(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeDataDir: %v", err)
	}
	bucket, err := appstorage.WorktreeBucketName(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeBucketName: %v", err)
	}
	if wantRoot != filepath.Join(dataHome, "swarm", appstorage.WorktreesDir, bucket) {
		t.Fatalf("worktree root = %q, want user-local bucket under XDG data home", wantRoot)
	}
	want := filepath.Join(wantRoot, "ws_abc123")
	if got != want {
		t.Fatalf("worktree path = %q, want %q", got, want)
	}
	if strings.Contains(got, filepath.Join(repoRoot, ".swarm", "worktrees")) {
		t.Fatalf("worktree path uses workspace-local .swarm path: %q", got)
	}
	info, err := os.Stat(wantRoot)
	if err != nil {
		t.Fatalf("stat worktree data root: %v", err)
	}
	if gotPerm := info.Mode().Perm(); gotPerm != appstorage.PrivateDirPerm {
		t.Fatalf("worktree data root permissions = %#o, want %#o", gotPerm, appstorage.PrivateDirPerm)
	}
}

func TestDeterministicSessionWorktreePathRejectsUnsafeWorkspaceID(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if _, err := deterministicSessionWorktreePath(repoRoot, "../escape"); err == nil {
		t.Fatal("expected unsafe workspace id to fail")
	}
	if _, err := deterministicSessionWorktreePath(repoRoot, "ws_escape/path"); err == nil {
		t.Fatal("expected workspace id with slash to fail")
	}
	if _, err := deterministicSessionWorktreePath(repoRoot, "ws_"); err == nil {
		t.Fatal("expected empty workspace slug to fail")
	}
}

func TestAllocateRequestedWorktreeReturnsTypedConflictForExactBranch(t *testing.T) {
	repo := newAllocatorTestRepository(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := runGit(repo, "branch", "agent/existing"); err != nil {
		t.Fatalf("create conflicting branch: %v", err)
	}

	_, err := (&Service{}).allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/existing", "unused", true)
	var conflict *RequestedWorktreeNameConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("allocation error = %v, want RequestedWorktreeNameConflictError", err)
	}
	if conflict.WorktreeName != "agent/existing" || !IsRequestedWorktreeNameConflict(err) {
		t.Fatalf("typed conflict = %#v, err = %v", conflict, err)
	}
	path, pathErr := deterministicSessionWorktreePath(repo, "agent-existing")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("conflicting allocation left target path %q: %v", path, statErr)
	}
}

func TestAllocateRequestedWorktreeReturnsTypedConflictForExactPath(t *testing.T) {
	repo := newAllocatorTestRepository(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := deterministicSessionWorktreePath(repo, "agent-requested")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err = (&Service{}).allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/requested", "unused", true)
	var conflict *RequestedWorktreeNameConflictError
	if !errors.As(err, &conflict) || conflict.WorktreeName != "agent/requested" {
		t.Fatalf("allocation error = %v, conflict = %#v", err, conflict)
	}
}

func TestAllocateRequestedWorktreeDoesNotTypeNonConflictGitFailure(t *testing.T) {
	repo := newAllocatorTestRepository(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := (&Service{}).allocateSessionWorkspaceWithBranchMode(repo, false, "missing-base", "agent/requested", "unused", true)
	if err == nil {
		t.Fatal("expected missing base allocation to fail")
	}
	if IsRequestedWorktreeNameConflict(err) {
		t.Fatalf("non-conflict failure was typed as requested-name conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "create session worktree") {
		t.Fatalf("allocation error = %v, want explicit creation failure", err)
	}
	path, pathErr := deterministicSessionWorktreePath(repo, "agent-requested")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed allocation left target path %q: %v", path, statErr)
	}
	registered, registeredErr := worktreePathRegistered(repo, path)
	if registeredErr != nil {
		t.Fatalf("inspect worktree metadata: %v", registeredErr)
	}
	if registered {
		t.Fatalf("failed allocation left registered worktree metadata for %q", path)
	}
	if exists, existsErr := localBranchExists(repo, "agent/requested"); existsErr != nil {
		t.Fatalf("inspect partial branch: %v", existsErr)
	} else if exists {
		t.Fatal("failed allocation left partial requested branch")
	}
}

func TestAllocateGeneratedWorktreePathCollisionIsOrdinaryError(t *testing.T) {
	repo := newAllocatorTestRepository(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID("session-collision"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err = (&Service{}).allocateSessionWorkspace(repo, true, "", "agent", "session-collision")
	if err == nil || !strings.Contains(err.Error(), "target worktree path") {
		t.Fatalf("generated collision error = %v", err)
	}
	if IsRequestedWorktreeNameConflict(err) {
		t.Fatalf("generated collision incorrectly typed as requested-name conflict: %v", err)
	}
}

func newAllocatorTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := runGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatalf("configure email: %v", err)
	}
	if _, err := runGit(repo, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("configure name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(repo, "add", "base.txt"); err != nil {
		t.Fatalf("add fixture: %v", err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	return repo
}

func TestWorkspaceIdentityForRequestedBranchUsesLiteralRequestSlug(t *testing.T) {
	got, err := WorkspaceIdentityForRequestedBranch("agent/client-side-request")
	if err != nil {
		t.Fatalf("WorkspaceIdentityForRequestedBranch: %v", err)
	}
	if got != "agent-client-side-request" {
		t.Fatalf("workspace id = %q, want branch-derived slug", got)
	}
	if strings.Contains(got, "da56285170") || strings.Contains(got, "session") {
		t.Fatalf("workspace id fell back to session/random identity: %q", got)
	}

	got, err = WorkspaceIdentityForRequestedBranch(" Feature.Client Request ")
	if err != nil {
		t.Fatalf("WorkspaceIdentityForRequestedBranch mixed separators: %v", err)
	}
	if got != "feature-client-request" {
		t.Fatalf("workspace id = %q, want sanitized literal branch slug", got)
	}

	if _, err := WorkspaceIdentityForRequestedBranch("../..."); err == nil {
		t.Fatal("expected branch without filesystem-safe slug to fail")
	}
}

func TestParseWorktreeListAndManagedPathFilter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	inside := filepath.Join(root, "ws_abc123")
	outside := filepath.Join(t.TempDir(), "repo")
	output := strings.Join([]string{
		"worktree " + outside,
		"HEAD 1111111",
		"branch refs/heads/dev",
		"",
		"worktree " + inside,
		"HEAD 2222222",
		"branch refs/heads/agent/abc123",
		"",
	}, "\n")

	entries := parseWorktreeList(output)
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if got := normalizeGitWorktreeBranch(entries[1].Branch); got != "agent/abc123" {
		t.Fatalf("normalized branch = %q, want agent/abc123", got)
	}
	if !pathWithinRoot(root, entries[1].Path) {
		t.Fatalf("expected managed path under root: %q in %q", entries[1].Path, root)
	}
	if pathWithinRoot(root, entries[0].Path) {
		t.Fatalf("unexpected arbitrary repo path accepted as managed: %q", entries[0].Path)
	}
}

func TestEnsureWorktreeParentUsesPrivatePermissions(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if err := ensureWorktreeParent(repoRoot); err != nil {
		t.Fatalf("ensureWorktreeParent: %v", err)
	}
	parent, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		t.Fatalf("worktreeCacheRoot: %v", err)
	}
	if !strings.HasPrefix(parent, filepath.Join(dataHome, "swarm", appstorage.WorktreesDir)+string(filepath.Separator)) {
		t.Fatalf("worktree parent = %q, want under user-local swarm worktrees root", parent)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat worktree parent: %v", err)
	}
	if got := info.Mode().Perm(); got != appstorage.PrivateDirPerm {
		t.Fatalf("worktree parent permissions = %#o, want %#o", got, appstorage.PrivateDirPerm)
	}
}

func TestGetConfigForPrincipalAllowsUnmatchedDirectory(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	svc := NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, nil)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	unmatched := t.TempDir()

	cfg, err := svc.GetConfigForPrincipal(principal, unmatched)
	if err != nil {
		t.Fatalf("GetConfigForPrincipal: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("Enabled = true, want false for unmatched directory")
	}
	if cfg.WorkspacePath != unmatched {
		t.Fatalf("WorkspacePath = %q, want %q", cfg.WorkspacePath, unmatched)
	}
}

func TestSetConfigForPrincipalRequiresAccountOwnedWorkspace(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	svc := NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, nil)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}

	if _, _, err := svc.SetConfigForPrincipal(principal, t.TempDir(), true, true, "", ""); err == nil || !strings.Contains(err.Error(), errAccountOwnedWorkspaceRequired.Error()) {
		t.Fatalf("SetConfigForPrincipal error = %v, want %v", err, errAccountOwnedWorkspaceRequired)
	}
}
