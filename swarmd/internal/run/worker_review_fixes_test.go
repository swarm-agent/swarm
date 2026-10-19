package run

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: review_fixes is admitted only as "security", only for a reviewer
// worker and only from a job automation agent step (request source
// automation): a reviewer started any other way (swarm_give_job with
// review_job, the API, a schedule) stays read-only, and Swarm never decides
// on its own to let a reviewer write.
// Threat: a direct caller passing review_fixes in a job's input to get a
// writable reviewer, or fixes on a coder/finder job.
// Owner: validateReviewRequest (Dispatch and resolveReviewTarget call it).
func TestReviewFixesAreAdmittedOnlyFromAnAutomationReviewerStep(t *testing.T) {
	input := func(fixes any) map[string]any {
		return map[string]any{"review_job": "worker/coder-1/run-1", "review_fixes": fixes}
	}
	if err := validateReviewRequest(store.WorkerRoleReviewer, "automation", input("security")); err != nil {
		t.Fatalf("automation reviewer with fixes = %v", err)
	}
	if err := validateReviewRequest(store.WorkerRoleReviewer, "direct", map[string]any{"review_job": "worker/coder-1/run-1"}); err != nil {
		t.Fatalf("direct reviewer without fixes = %v", err)
	}
	for name, err := range map[string]error{
		"direct source":   validateReviewRequest(store.WorkerRoleReviewer, "direct", input("security")),
		"test run source": validateReviewRequest(store.WorkerRoleReviewer, "test_run", input("security")),
		"schedule source": validateReviewRequest(store.WorkerRoleReviewer, "schedule", input("security")),
		"coder worker":    validateReviewRequest(store.WorkerRoleCoder, "automation", map[string]any{"review_fixes": "security"}),
		"other value":     validateReviewRequest(store.WorkerRoleReviewer, "automation", input("all")),
		"not a string":    validateReviewRequest(store.WorkerRoleReviewer, "automation", input(true)),
	} {
		if err == nil {
			t.Fatalf("%s: review_fixes was admitted", name)
		}
	}
	if reviewFixesAllowed(map[string]any{"review_fixes": "all"}) || !reviewFixesAllowed(input("security")) {
		t.Fatal("reviewFixesAllowed does not match the admitted value")
	}
}

// Purpose: a reviewer without fixes must be registered as a read-only review
// checkout (read-only .git mounts, detached identity) and its checkout is
// cleaned up when it ends; a reviewer with fixes is never registered that
// way, its checkout is kept (no cleanup) and its per-call identity check is
// "own agent/review-<run> branch whose HEAD descends from the reviewed
// head", which admits its commits and refuses a rewritten branch. The same
// branch checkout never passes as a read-only reviewer.
// Threat: a fixing reviewer stuck read-only (or cleaned up before the
// delivery reads its head), a read-only reviewer admitted on a branch, or a
// fix branch that rewrote the reviewed history.
// Owners: WorkerExecutionService.registerReview, markReviewCleanupPending,
// WorkerExecutionService.cleanupReview and
// validateSessionExecutionRepositoryIdentity, over real Git.
func TestFixingReviewerSessionIsAWritableBranchCheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := t.TempDir()
	git(repo, "init", "-b", "dev")
	git(repo, "commit", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	git(repo, "commit", "--allow-empty", "-m", "coder work")
	head := git(repo, "rev-parse", "HEAD")
	git(repo, "checkout", "-q", base)
	lane := filepath.Join(t.TempDir(), "review-fix")
	git(repo, "worktree", "add", "-q", "-b", "agent/review-run1", lane, head)

	const sessionID = "worker-execution-run1"
	session := func(fixes bool) store.SessionSnapshot {
		meta := map[string]any{"worker_role": store.WorkerRoleReviewer, "worker_execution_run_id": "run1",
			"worker_review_target":               map[string]any{"head": head, "base": base, "job": "worker/coder-1/run-0"},
			"swarm_v3_worktree_owner_session_id": sessionID, "swarm_v3_source_workspace_path": repo, "swarm_v3_worktree_base_commit": head}
		if fixes {
			meta["worker_review_fixes"] = ReviewFixesSecurity
		}
		return store.SessionSnapshot{ID: sessionID, Metadata: meta, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/review-run1", WorktreeBaseBranch: head}
	}

	// Registration: the read-only reviewer needs the sandbox's read-only
	// review checkout; the fixing reviewer is never registered as one.
	execution := &WorkerExecutionService{}
	if sandbox.Default() == nil {
		if err := execution.registerReview(session(false)); !errors.Is(err, sandbox.ErrSandboxRequired) {
			t.Fatalf("read-only reviewer registration without a sandbox = %v", err)
		}
	}
	if err := execution.registerReview(session(true)); err != nil {
		t.Fatalf("fixing reviewer registration = %v", err)
	}

	// Cleanup: a fixing reviewer's checkout is kept like a coder's clone.
	fixRun := store.WorkerRunRecord{ID: "run1", Role: store.WorkerRoleReviewer, Status: "succeeded", Input: map[string]any{"review_fixes": ReviewFixesSecurity}}
	markReviewCleanupPending(&fixRun)
	if fixRun.Cleanup != nil {
		t.Fatal("a fixing reviewer was marked for checkout cleanup")
	}
	if err := execution.cleanupReview(fixRun); err != nil {
		t.Fatalf("cleanupReview of a fixing reviewer = %v", err)
	}
	if _, err := os.Stat(filepath.Join(lane, ".git")); err != nil {
		t.Fatal("the fixing reviewer's checkout was removed", err)
	}
	readOnlyRun := store.WorkerRunRecord{ID: "run2", Role: store.WorkerRoleReviewer, Status: "succeeded", Input: map[string]any{}}
	markReviewCleanupPending(&readOnlyRun)
	if readOnlyRun.Cleanup == nil || readOnlyRun.Cleanup.State != "pending" {
		t.Fatal("a read-only reviewer is no longer cleaned up")
	}

	// Identity: the branch at (and above) the reviewed head passes for the
	// fixing reviewer only.
	if err := validateSessionExecutionRepositoryIdentity(session(true)); err != nil {
		t.Fatalf("fixing reviewer at the reviewed head = %v", err)
	}
	if err := validateSessionExecutionRepositoryIdentity(session(false)); err == nil {
		t.Fatal("a branch checkout passed as a read-only reviewer")
	}
	git(lane, "commit", "--allow-empty", "-m", "escape the upload name")
	if err := validateSessionExecutionRepositoryIdentity(session(true)); err != nil {
		t.Fatalf("fixing reviewer after a fix commit = %v", err)
	}
	wrong := session(true)
	wrong.Metadata["worker_execution_run_id"] = "run-other"
	if err := validateSessionExecutionRepositoryIdentity(wrong); err == nil {
		t.Fatal("a fix branch of another run passed")
	}
	git(lane, "reset", "-q", "--hard", base)
	if err := validateSessionExecutionRepositoryIdentity(session(true)); err == nil {
		t.Fatal("a fix branch that no longer descends from the reviewed head passed")
	}
}
