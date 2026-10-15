package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
)

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

// Requirement: delegated integration must recognize a child commit after
// cherry-pick even though the parent commit has a different identity.
// Threat: identical author/committer metadata within Git's one-second timestamp
// resolution can reproduce the child OID, hiding an ancestry-only test contract
// that flakes when the timestamp crosses a second. This service-level test is the
// narrowest layer that proves the production preflight/apply/classifier boundary.
func TestPrepareAndApplyTaskIntegrationIsDeterministic(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "integrator@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Integration User")
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
	childCommitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Child Author",
		"GIT_AUTHOR_EMAIL=child@example.invalid",
		"GIT_COMMITTER_NAME=Child Author",
		"GIT_COMMITTER_EMAIL=child@example.invalid",
	)
	if _, err := runGitWithEnv(childPath, childCommitEnv, "commit", "-m", "child"); err != nil {
		t.Fatal(err)
	}
	head, _ := runGit(childPath, "rev-parse", "HEAD")

	svc := &Service{}
	plan, err := svc.PrepareTaskIntegration(repo, "dev", base, []TaskIntegrationChild{{SessionID: "child-session", BaseCommit: base, HeadCommit: head}})
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
	if result.ResultingParentHead == head {
		t.Fatalf("integration unexpectedly reused child commit identity: child=%s parent=%s", head, result.ResultingParentHead)
	}
	if descends, err := svc.TaskCommitDescendsFrom(repo, head, result.ResultingParentHead); err != nil || descends {
		t.Fatalf("cherry-picked child should not be reachable by ancestry: descends=%t err=%v", descends, err)
	}
	if integrated, err := svc.TaskCommitRangeIntegratedInto(repo, base, head, result.ResultingParentHead); err != nil || !integrated {
		t.Fatalf("cherry-picked child not classified integrated: integrated=%t err=%v", integrated, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "child.txt")); err != nil {
		t.Fatalf("integrated file: %v", err)
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

	_, err := (&Service{}).PrepareTaskIntegration(repo, "dev", base, []TaskIntegrationChild{
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
	_, err := (&Service{}).PrepareTaskIntegration(repo, "dev", strings.Repeat("a", 40), []TaskIntegrationChild{{SessionID: "child", BaseCommit: strings.Repeat("b", 40), HeadCommit: strings.Repeat("c", 40)}})
	if err == nil || !strings.Contains(err.Error(), "stale parent HEAD") {
		t.Fatalf("error = %v", err)
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
