package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: a reviewer reads the reviewed job's own worktree in place by
// default (review_checkout same): its target names that worktree, its owning
// session and branch, and its checkout is that worktree, allocated nowhere
// else. It starts only at exactly the reviewed head with nothing
// uncommitted; review_checkout new keeps the reviewer's own checkout. A
// verdict given while the worktree moved or changed is a block. Invalid
// review_checkout requests are refused.
// Threat: a reviewer in a copy of the work instead of the work, a reviewer
// reading uncommitted or newer code than the head its verdict names, an OK
// pushed for code nobody reviewed, or a coder's worktree removed or claimed
// by its reviewer.
// Owners: validateReviewRequest, WorkerExecutionService.resolveReviewTarget,
// allocateReviewerCheckout and withBorrowedReviewHead over a real Pebble
// store, the real workspace and worktree services and real Git; no model
// provider or sandbox is used (the sandbox side has its own test).
func TestReviewerReadsTheJobsOwnWorktreeInPlace(t *testing.T) {
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
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "in-place.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	const account, user = "acct-1", "user-1"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	workspaces := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(db))
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "dev")
	git(repo, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	added, err := workspaces.AddForPrincipal(p, repo, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	source := added.WorkspacePath
	trees := worktree.NewService(pebblestore.NewWorktreeStore(db), workspaces, nil)
	const taskSession = "task-session-coder"
	coder, err := trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, source, taskSession, "", "agent/task-coder")
	if err != nil {
		t.Fatal(err)
	}
	limit := filepath.Join(coder.WorkspacePath, "limit.go")
	if err := os.WriteFile(limit, []byte("package api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "limit.go")
	git(coder.WorkspacePath, "commit", "-q", "-m", "add the rate limit")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")
	worktreesBefore := git(repo, "worktree", "list", "--porcelain")

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	db2 := sessions.Store()
	ws := db2.WorkerStore()
	reviewerWorker, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.PutProject(account, &pebblestore.ProjectRecord{ID: "project-a", Name: "project-a"}); err != nil {
		t.Fatal(err)
	}
	sourceRef := pebblestore.ProjectTaskSource{WorkspaceID: added.WorkspaceID, WorkspaceGeneration: max(added.WorkspaceGeneration, 1), Path: source, Provenance: "explicit"}
	if err := db2.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "task_coder", Title: "Rate limit", Description: "Add a rate limit.", ProjectID: "project-a", AccountID: account,
		SessionID: taskSession, Agent: "coder", Status: "needs_review", WorkspacePath: coder.WorkspacePath, WorktreeBranch: coder.BranchName, BaseBranch: "dev", BaseCommit: base,
		SourceWorkspace: sourceRef}); err != nil {
		t.Fatal(err)
	}
	reviewRun := func(id string, input map[string]any) pebblestore.WorkerRunRecord {
		t.Helper()
		if err := db2.CreateSession(pebblestore.SessionSnapshot{ID: "worker-execution-" + id, UserID: user, AccountScopeID: account, WorkspacePath: source, Title: "Reviewer job",
			Metadata: map[string]any{"worker_id": reviewerWorker.ID, "worker_execution_run_id": id, "project_id": "project-a"}, CreatedAt: 3, UpdatedAt: 3}); err != nil {
			t.Fatal(err)
		}
		in := map[string]any{"review_job": "task/project-a/task_coder", "project_id": "project-a"}
		for k, v := range input {
			in[k] = v
		}
		return pebblestore.WorkerRunRecord{ID: id, WorkerID: reviewerWorker.ID, UserID: user, AccountScopeID: account, WorkspaceID: added.WorkspaceID,
			SessionID: "worker-execution-" + id, Role: pebblestore.WorkerRoleReviewer, RequestSource: "automation", Input: in}
	}
	service := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}, trees: trees}}
	ctx := context.Background()

	// Default: the coder's own worktree, its session and branch.
	r := reviewRun("inplace", nil)
	captured, err := service.resolveReviewTarget(ctx, r, reviewerWorker)
	if err != nil {
		t.Fatalf("resolveReviewTarget: %v", err)
	}
	if captured["head"] != head || captured["lane"] != coder.WorkspacePath || captured["lane_owner"] != taskSession || captured["lane_branch"] != coder.BranchName {
		t.Fatalf("in-place target = %+v", captured)
	}
	r.Input["captured_review_target"] = captured
	alloc, err := allocateReviewerCheckout(trees, p, source, r, false)
	if err != nil || alloc.WorkspacePath != coder.WorkspacePath || alloc.BranchName != coder.BranchName || alloc.BaseCommit != head {
		t.Fatalf("in-place checkout = %+v %v", alloc, err)
	}
	if after := git(repo, "worktree", "list", "--porcelain"); after != worktreesBefore {
		t.Fatalf("reviewing in place allocated a worktree:\n%s", after)
	}
	meta := map[string]any{"worker_role": pebblestore.WorkerRoleReviewer}
	pinReviewerMetadata(meta, r.Input, false)
	if sessionReviewLaneOwner(meta) != taskSession {
		t.Fatalf("pinned lane owner = %q", sessionReviewLaneOwner(meta))
	}

	// checkout new: the reviewer's own checkout, nothing borrowed.
	own, err := service.resolveReviewTarget(ctx, reviewRun("own", map[string]any{"review_checkout": "new"}), reviewerWorker)
	if err != nil || own["lane"] != nil || own["lane_owner"] != nil {
		t.Fatalf("checkout new target = %+v %v", own, err)
	}

	// Uncommitted changes: not the reviewed head, so not reviewed in place.
	if err := os.WriteFile(limit, []byte("package api\n\nvar Limit = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveReviewTarget(ctx, reviewRun("dirty", nil), reviewerWorker); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree reviewed in place: %v", err)
	}
	if _, err := service.resolveReviewTarget(ctx, reviewRun("dirty-own", map[string]any{"review_checkout": "new"}), reviewerWorker); err != nil {
		t.Fatalf("checkout new refused a dirty job worktree: %v", err)
	}
	snapshot := pebblestore.SessionSnapshot{WorktreeEnabled: true, WorktreeRootPath: coder.WorkspacePath}
	if got := withBorrowedReviewHead(pebblestore.JobReview{Verdict: "ok", Head: head}, snapshot, trees); got.Verdict != "block" || !strings.Contains(got.Reason, "uncommitted") {
		t.Fatalf("verdict over a changed worktree = %+v", got)
	}
	git(coder.WorkspacePath, "checkout", "--", "limit.go")
	if got := withBorrowedReviewHead(pebblestore.JobReview{Verdict: "ok", Head: head}, snapshot, trees); got.Verdict != "ok" {
		t.Fatalf("verdict at the reviewed head = %+v", got)
	}

	// The worktree moved past the captured head: no checkout, and the
	// verdict over it is a block.
	if err := os.WriteFile(limit, []byte("package api\n\nvar Limit = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "commit", "-q", "-am", "raise the limit")
	if _, err := allocateReviewerCheckout(trees, p, source, r, false); err == nil {
		t.Fatal("a moved worktree was given to the reviewer of an older head")
	}
	if got := withBorrowedReviewHead(pebblestore.JobReview{Verdict: "ok", Head: head}, snapshot, trees); got.Verdict != "block" || !strings.Contains(got.Reason, "changed during the review") {
		t.Fatalf("verdict over a moved worktree = %+v", got)
	}

	// Invalid requests.
	for name, input := range map[string]map[string]any{
		"unknown value":   {"review_checkout": "maybe"},
		"same with fixes": {"review_checkout": "same", "review_fixes": "security"},
		"not a string":    {"review_checkout": true},
	} {
		input["review_job"] = "task/project-a/task_coder"
		if err := validateReviewRequest(pebblestore.WorkerRoleReviewer, "automation", input); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := validateReviewRequest(pebblestore.WorkerRoleCoder, "automation", map[string]any{"review_checkout": "same"}); err == nil {
		t.Fatal("review_checkout accepted for a coder worker")
	}
	if reviewSharesCheckout(map[string]any{"review_fixes": "security"}) {
		t.Fatal("a reviewer with fixes reads in place")
	}
}
