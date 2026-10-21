package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: the production wiring startPlan uses for a reviewer run. With
// review_fixes admitted, it starts the code-owned fixer agent, allocates the
// writable agent/review-<run> branch at the reviewed head from the reviewed
// job's own clone, and pins worker_review_fixes on the session; without it,
// the read-only reviewer agent, a detached checkout at the reviewed head and
// no worker_review_fixes. A coder run never gets the fixer agent, even with
// review_fixes in its input. The read-only reviewer's checkout is refused by
// the per-call identity check every tool call passes through until the
// sandbox has registered it as a read-only review checkout, and the fixer's
// identity never admits it.
// Threat: startPlan choosing the fixer (or a writable checkout) for a
// reviewer that did not opt in, losing the pin the sandbox and identity
// checks read, or a read-only reviewer whose commit goes unnoticed.
// Owners: workerExecutionAgentID, allocateReviewerCheckout and
// pinReviewerMetadata (called by startPlan), over the real worktree and
// workspace services with a temp Pebble store and real Git, and
// validateSessionExecutionRepositoryIdentity.
// Not proven here: that the sandbox mounts the read-only reviewer's .git
// read-only, so its commit itself fails, and that the registered identity
// check refuses a moved head; both need a real sandbox runtime (this test
// has none, so registration is absent and every call is refused).
func TestReviewerRunWiringChoosesAgentCheckoutAndPin(t *testing.T) {
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
	db, err := store.Open(filepath.Join(t.TempDir(), "plan.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "acct-1", AccountScopeSource: identity.AccountScopeSourceServerState}
	workspaces := workspaceruntime.NewService(store.NewWorkspaceStore(db))
	repo := t.TempDir()
	git(repo, "init", "-b", "dev")
	git(repo, "commit", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	added, err := workspaces.AddForPrincipal(p, repo, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	source := added.WorkspacePath
	trees := worktree.NewService(store.NewWorktreeStore(db), workspaces, nil)
	coder, err := trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, source, "worker-execution-coderrun", "", "agent/worker-coderrun")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coder.WorkspacePath, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "upload.go")
	git(coder.WorkspacePath, "commit", "-m", "coder work")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")

	target := map[string]any{"head": head, "base": base, "commits": []any{base}, "job": "worker/coder-1/coderrun", "checkout": coder.WorkspacePath}
	run := func(id string, fixes bool) store.WorkerRunRecord {
		input := map[string]any{"review_job": "worker/coder-1/coderrun", "captured_review_target": target}
		if fixes {
			input["review_fixes"] = ReviewFixesSecurity
		}
		return store.WorkerRunRecord{ID: id, SessionID: "worker-execution-" + id, Role: store.WorkerRoleReviewer, RequestSource: "automation", Input: input}
	}

	// Agent choice.
	if agent, fixes := workerExecutionAgentID(store.WorkerRoleReviewer, run("x", true).Input); agent != agentruntime.ReviewerFixerAgentID || !fixes {
		t.Fatalf("fixing reviewer agent = %s %v", agent, fixes)
	}
	if agent, fixes := workerExecutionAgentID(store.WorkerRoleReviewer, run("x", false).Input); agent != agentruntime.ReviewerAgentID || fixes {
		t.Fatalf("read-only reviewer agent = %s %v", agent, fixes)
	}
	if agent, fixes := workerExecutionAgentID(store.WorkerRoleCoder, map[string]any{"review_fixes": ReviewFixesSecurity}); agent != agentruntime.WorkerCoderAgentID || fixes {
		t.Fatalf("coder with review_fixes agent = %s %v", agent, fixes)
	}

	// With fixes: a branch at the reviewed head from the coder's clone.
	fixRun := run("fixrun", true)
	_, fixes := workerExecutionAgentID(store.WorkerRoleReviewer, fixRun.Input)
	fix, err := allocateReviewerCheckout(trees, p, source, fixRun, fixes)
	if err != nil {
		t.Fatal(err)
	}
	if fix.BranchName != "agent/review-fixrun" || git(fix.WorkspacePath, "branch", "--show-current") != "agent/review-fixrun" || git(fix.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatalf("fix checkout = %+v", fix)
	}
	meta := map[string]any{}
	pinReviewerMetadata(meta, fixRun.Input, fixes)
	if meta["worker_review_fixes"] != ReviewFixesSecurity || meta["worker_review_target"] == nil {
		t.Fatalf("fixing reviewer metadata = %+v", meta)
	}

	// Without fixes: detached at the reviewed head, no pin.
	roRun := run("rorun", false)
	_, fixes = workerExecutionAgentID(store.WorkerRoleReviewer, roRun.Input)
	ro, err := allocateReviewerCheckout(trees, p, source, roRun, fixes)
	if err != nil {
		t.Fatal(err)
	}
	if ro.BranchName != "HEAD" || git(ro.WorkspacePath, "branch", "--show-current") != "" || git(ro.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatalf("read-only checkout = %+v", ro)
	}
	roMeta := map[string]any{"worker_role": store.WorkerRoleReviewer, "worker_execution_run_id": roRun.ID,
		"swarm_v3_worktree_owner_session_id": roRun.SessionID, "swarm_v3_source_workspace_path": source, "swarm_v3_worktree_base_commit": head}
	pinReviewerMetadata(roMeta, roRun.Input, fixes)
	if _, pinned := roMeta["worker_review_fixes"]; pinned {
		t.Fatal("a read-only reviewer was pinned with worker_review_fixes")
	}
	roSession := store.SessionSnapshot{ID: roRun.SessionID, Metadata: roMeta, WorktreeEnabled: true, WorktreeRootPath: ro.WorkspacePath, WorktreeBranch: ro.BranchName, WorktreeBaseBranch: head}
	// With a sandbox that should run but has not registered the read-only
	// review checkout, no tool call runs.
	if sandbox.Default() == nil {
		withUnavailableSandbox(t)
	}
	if err := validateSessionExecutionRepositoryIdentity(roSession); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered read-only reviewer tool call = %v", err)
	}
	// The fixer's identity never admits the detached read-only checkout.
	pinned := map[string]any{}
	for k, v := range roMeta {
		pinned[k] = v
	}
	pinned["worker_review_fixes"] = ReviewFixesSecurity
	if err := validateSessionExecutionRepositoryIdentity(store.SessionSnapshot{ID: roRun.SessionID, Metadata: pinned, WorktreeEnabled: true,
		WorktreeRootPath: ro.WorkspacePath, WorktreeBranch: ro.BranchName, WorktreeBaseBranch: head}); err == nil {
		t.Fatal("a detached review checkout passed the fixing reviewer's identity")
	}
	// A missing captured target allocates nothing.
	if _, err := allocateReviewerCheckout(trees, p, source, store.WorkerRunRecord{ID: "none", SessionID: "worker-execution-none", Input: map[string]any{}}, true); err == nil {
		t.Fatal("a reviewer without a captured target was allocated a checkout")
	}
}
