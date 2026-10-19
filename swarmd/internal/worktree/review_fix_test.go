package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a reviewer an automation step allowed to commit security fixes
// gets a writable branch checkout (agent/review-<run>) started at the
// reviewed head, branched from the reviewed job's own clone, in which it can
// commit; its identity check is "HEAD descends from the reviewed head". A
// reviewer without the option keeps the detached disposable checkout, whose
// identity refuses any commit (its head must stay the reviewed head) and
// which the fix-branch identity never admits. The folder and the reviewed
// job's clone are never written.
// Threat: a writable reviewer checkout for a reviewer that did not opt in, a
// fix branch outside the job's repository (unverifiable ancestry), or a fix
// branch that rewrote the reviewed history.
// Owners: Service.allocateReviewFixWorkspace, ValidateReviewFixExecutionIdentity,
// Service.allocateReviewWorkspace and ValidateReviewWorkspaceRemoval, with
// real local Git (no provider, daemon or sandbox is needed to prove them).
func TestReviewFixCheckoutIsABranchAtTheReviewedHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "commit", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	s := &Service{}
	coder, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-coder", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coder.WorkspacePath, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "upload.go")
	git(coder.WorkspacePath, "commit", "-m", "coder work")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")

	// Without the option: detached at the head; a commit there is refused by
	// its identity check, and the fix-branch identity never admits it.
	review, err := s.allocateReviewWorkspace(repo, "worker-execution-review-ro", head)
	if err != nil {
		t.Fatal(err)
	}
	if git(review.WorkspacePath, "branch", "--show-current") != "" || git(review.WorkspacePath, "rev-parse", "HEAD") != head || review.BranchName != "HEAD" {
		t.Fatal("a reviewer without fixes is not detached at the exact head")
	}
	if err := ValidateReviewFixExecutionIdentity(repo, review.WorkspacePath, "HEAD", head); err == nil {
		t.Fatal("a detached review checkout passed the fix-branch identity")
	}
	git(review.WorkspacePath, "commit", "--allow-empty", "-m", "a read-only reviewer must not commit")
	if err := s.ValidateReviewWorkspaceRemoval(repo, review.WorkspacePath, "worker-execution-review-ro", head); err == nil || !strings.Contains(err.Error(), "review head changed") {
		t.Fatalf("a commit in a read-only review checkout = %v, want review head changed", err)
	}

	// With the option: a branch at the head, from the coder's clone.
	fix, err := s.allocateReviewFixWorkspace(repo, "worker-execution-review-fx", coder.WorkspacePath, head, "agent/review-run1")
	if err != nil {
		t.Fatal(err)
	}
	if fix.BranchName != "agent/review-run1" || fix.BaseCommit != head || git(fix.WorkspacePath, "branch", "--show-current") != "agent/review-run1" || git(fix.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatalf("fix checkout = %+v", fix)
	}
	if info, err := os.Lstat(filepath.Join(fix.WorkspacePath, ".git")); err != nil || !info.IsDir() {
		t.Fatal("fix checkout has no own .git", err)
	}
	if !managedCloneIdentity(repo, fix.WorkspacePath) || git(fix.WorkspacePath, "config", "--get", "remote.origin.url") != repo {
		t.Fatal("fix checkout is not a managed clone of the folder")
	}
	if err := ValidateReviewFixExecutionIdentity(repo, fix.WorkspacePath, fix.BranchName, head); err != nil {
		t.Fatal(err)
	}
	// The session lane check job admission runs after allocation.
	if err := s.ValidateSessionRepositoryLaneForRead(repo, fix.WorkspacePath, "worker-execution-review-fx", fix.BranchName); err != nil {
		t.Fatalf("session lane of the fix checkout = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fix.WorkspacePath, "upload.go"), []byte("package upload\n\n// escaped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(fix.WorkspacePath, "add", "upload.go")
	git(fix.WorkspacePath, "commit", "-m", "escape the upload name")
	fixHead := git(fix.WorkspacePath, "rev-parse", "HEAD")
	if err := ValidateReviewFixExecutionIdentity(repo, fix.WorkspacePath, fix.BranchName, head); err != nil {
		t.Fatalf("a fix commit on the branch = %v", err)
	}
	git(fix.WorkspacePath, "merge-base", "--is-ancestor", head, fixHead)
	if err := ValidateReviewFixExecutionIdentity(repo, fix.WorkspacePath, "agent/job", head); err == nil {
		t.Fatal("the fix checkout passed with another branch name")
	}
	// Rewriting the branch below the reviewed head breaks its identity.
	git(fix.WorkspacePath, "reset", "-q", "--hard", base)
	if err := ValidateReviewFixExecutionIdentity(repo, fix.WorkspacePath, fix.BranchName, head); err == nil {
		t.Fatal("a fix branch that no longer descends from the reviewed head passed")
	}
	// The folder and the reviewed job's clone are unchanged.
	if git(coder.WorkspacePath, "rev-parse", "HEAD") != head || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("source changed")
	}
	for _, path := range []string{repo, coder.WorkspacePath} {
		if out, err := exec.Command("git", "-C", path, "cat-file", "-e", fixHead).CombinedOutput(); err == nil {
			t.Fatalf("the fix commit was written into %s: %s", path, out)
		}
	}

	// Refused: a source that is not the job's managed clone, a head it
	// lacks, a branch outside agent/review-, an existing checkout.
	stranger := filepath.Join(t.TempDir(), "stranger")
	git(filepath.Dir(stranger), "clone", "-q", coder.WorkspacePath, stranger)
	for name, call := range map[string]func() (Allocation, error){
		"unmanaged clone": func() (Allocation, error) {
			return s.allocateReviewFixWorkspace(repo, "worker-execution-review-x1", stranger, head, "agent/review-x1")
		},
		"missing head": func() (Allocation, error) {
			return s.allocateReviewFixWorkspace(repo, "worker-execution-review-x2", coder.WorkspacePath, strings.Repeat("f", 40), "agent/review-x2")
		},
		"wrong branch": func() (Allocation, error) {
			return s.allocateReviewFixWorkspace(repo, "worker-execution-review-x3", coder.WorkspacePath, head, "agent/worker-x3")
		},
		"existing checkout": func() (Allocation, error) {
			return s.allocateReviewFixWorkspace(repo, "worker-execution-review-fx", coder.WorkspacePath, head, "agent/review-run1")
		},
	} {
		got, err := call()
		if err == nil || got.WorkspacePath != "" {
			t.Fatalf("%s admitted: %+v %v", name, got, err)
		}
	}
	for _, owner := range []string{"worker-execution-review-x1", "worker-execution-review-x2", "worker-execution-review-x3"} {
		path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID(owner))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a refused fix checkout left %s: %v", path, err)
		}
	}
}
