package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement (AGENTS.md): a task is integrated only when it produced commits
// beyond its base that are in the target branch. A zero-commit task's head is
// its base, trivially contained in the target, so RemoveLandedWorktree may
// remove its clean checkout; afterwards neither the task's delivery state
// (inspectTaskGitStateContext) nor a repeated integration
// (integratedAfterWorktreeRemoval) may report it integrated from the removal
// record. A task whose removed head carries a landed commit still does.
// Threat (review finding): failed or cancelled tasks with no commits showed as
// integrated once their checkout was removed. Owners: removedWorktreeHasCommits
// and its two callers, over a real Git folder.
func TestRemovedZeroCommitTaskIsNeverReportedIntegrated(t *testing.T) {
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := outboxPinGit(t, folder, "rev-parse", "HEAD")
	outboxPinGit(t, folder, "branch", "agent/empty", base)
	outboxPinGit(t, folder, "checkout", "-q", "-b", "agent/work")
	writeRebaseFile(t, folder, "work.go", "package work\n")
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "work")
	workHead := outboxPinGit(t, folder, "rev-parse", "HEAD")
	outboxPinGit(t, folder, "checkout", "-q", "dev")
	outboxPinGit(t, folder, "merge", "-q", "--ff-only", "agent/work")
	gone := filepath.Join(t.TempDir(), "removed")

	task := func(branch, head string) *pebblestore.ProjectTaskRecord {
		return &pebblestore.ProjectTaskRecord{ID: "task-z", Status: "failed", BaseCommit: base, BaseBranch: "dev", WorktreeBranch: branch, WorkspacePath: gone,
			SourceWorkspace: pebblestore.ProjectTaskSource{Path: folder},
			WorktreeRemoval: &pebblestore.WorktreeRemoval{State: pebblestore.WorktreeRemovalRemoved, Path: gone, Branch: branch, Head: head, TargetBranch: "dev"}}
	}
	session := pebblestore.SessionSnapshot{WorktreeRootPath: gone, Metadata: map[string]any{"base_commit": base}}
	ctx := context.Background()

	empty := task("agent/empty", base)
	state := inspectTaskGitStateContext(ctx, *empty, nil)
	if state.isIntegrated || state.deliveryAssessment.State == "integrated" || state.deliveryAssessment.ReasonCode == "worktree_removed" {
		t.Fatalf("zero-commit removed task reads as integrated: %+v %+v", state, state.deliveryAssessment)
	}
	if result, ok := integratedAfterWorktreeRemoval(ctx, empty, session, folder, tool.ProjectTaskIntegrationSelection{SourceBranch: "agent/empty", TargetBranch: "dev"}); ok {
		t.Fatalf("zero-commit removed task answered %v", result)
	}
	// No recorded base: never integrated from the record either.
	unknown := task("agent/work", workHead)
	unknown.BaseCommit = ""
	if state := inspectTaskGitStateContext(ctx, *unknown, nil); state.isIntegrated {
		t.Fatalf("removed task without a base reads as integrated")
	}

	landed := task("agent/work", workHead)
	state = inspectTaskGitStateContext(ctx, *landed, nil)
	if !state.isIntegrated || state.deliveryAssessment.State != "integrated" || state.deliveryAssessment.ReasonCode != "worktree_removed" {
		t.Fatalf("landed removed task = %+v %+v, want integrated", state, state.deliveryAssessment)
	}
	if result, ok := integratedAfterWorktreeRemoval(ctx, landed, session, folder, tool.ProjectTaskIntegrationSelection{SourceBranch: "agent/work", TargetBranch: "dev"}); !ok || result["status"] != "already_integrated" {
		t.Fatalf("landed removed task = %v %v, want already_integrated", result, ok)
	}
}

// Requirement: a job whose checkout Swarm removed after its work landed
// (worktree.Service.RemoveLandedWorktree keeps its branch) still shows its
// commits, files, diff and head in Swarm's own views: swarm_get_changes, the
// task/job view built from it (its keys and controlMCPFillWorktreeHead) and
// the task diff route's range. They are read from the kept branch in the
// folder, never from the folder's own working tree.
// Threat (review finding): after removal the views answered "the session
// folder is not available" with no commits, files or head, so a landed job's
// result was unreadable through Swarm.
// Owners: controlMCPGetChanges and resolveChangesRange
// (changesFolderOrKeptBranch) over the real session store and real Git. The
// checkout is removed with `git worktree remove`, the removal primitive's
// own Git operation (tested in the worktree package).
func TestRemovedJobCheckoutChangesAreReadFromItsKeptBranch(t *testing.T) {
	r := newRebaseFixture(t)
	base := outboxPinGit(t, r.folder, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "landed")
	outboxPinGit(t, r.folder, "worktree", "add", "-q", "-b", "agent/landed", dir, base)
	writeRebaseFile(t, dir, "landed.go", "package landed\n")
	outboxPinGit(t, dir, "add", ".")
	outboxPinGit(t, dir, "commit", "-q", "-m", "landed work")
	head := outboxPinGit(t, dir, "rev-parse", "HEAD")
	p := testPrincipal()
	if err := r.f.server.sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: "sess-landed", UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		WorkspacePath: r.folder, Title: "Coder job", WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/landed", WorktreeBaseBranch: "dev",
		Metadata: map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": r.folder}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, r.folder, "merge", "-q", "--no-ff", "-m", "merge landed", "agent/landed")
	outboxPinGit(t, r.folder, "worktree", "remove", dir)
	// The folder's own untracked file is never shown as the job's.
	writeRebaseFile(t, r.folder, "scratch.txt", "not the job's\n")

	changes := r.changes("landed")
	onlyCommit(t, changes, head, "landed work")
	if changes["head"] != head || changes["base"] != base || changes["branch"] != "agent/landed" {
		t.Fatalf("changes head/base/branch = %v %v %v, want %s %s", changes["head"], changes["base"], changes["branch"], head, base)
	}
	files, _ := changes["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "landed.go" || files[0]["status"] != "added" {
		t.Fatalf("files = %v, want only the job's landed.go", files)
	}
	if note, _ := changes["checkout_note"].(string); !strings.Contains(note, "agent/landed") {
		t.Fatalf("checkout_note = %q", note)
	}
	if diff, _ := changes["diff"].(string); !strings.Contains(diff, "+package landed") || strings.Contains(diff, "scratch") {
		t.Fatalf("diff = %q", diff)
	}
	// The task/job view takes its head from these changes.
	view := map[string]any{}
	controlMCPFillWorktreeHead(view, changes)
	if view["head"] != head {
		t.Fatalf("task view head = %v, want %s", view["head"], head)
	}
	// The task diff route reads the same range in the folder.
	span, err := resolveChangesRange(context.Background(), sessionChangesTarget(r.folder, true, dir, "dev", "agent/landed", map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": r.folder}))
	if err != nil || span.Head != head || span.Base != base || span.KeptBranch != "agent/landed" {
		t.Fatalf("diff range = %+v, %v", span, err)
	}
	if resolved, _ := filepath.EvalSymlinks(r.folder); span.Dir != resolved {
		t.Fatalf("diff range read in %s, want the folder %s", span.Dir, resolved)
	}

	// A missing checkout whose branch is not in the folder stays unavailable.
	gone := filepath.Join(t.TempDir(), "gone")
	if _, err := resolveChangesRange(context.Background(), sessionChangesTarget(r.folder, true, gone, "dev", "agent/never", map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": r.folder})); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing checkout without a kept branch = %v, want not available", err)
	}
	// A session with no worktree of its own is never read from a branch.
	if _, err := resolveChangesRange(context.Background(), sessionChangesTarget(gone, false, "", "dev", "agent/landed", map[string]any{"swarm_v3_source_workspace_path": r.folder})); err == nil {
		t.Fatal("a folder session with a missing folder was read from a branch")
	}
}

// Requirement: a fixing reviewer's result (head, fix commits) stays readable
// after Swarm removed its checkout once its work landed: it is read from the
// kept agent/review-<run> branch in the folder, which also holds the commits
// for later steps to fetch (ResultGit.Worktree is the folder).
// Threat (review finding): fillReviewFixResult dropped the head and reported
// fix_error for every landed fixing reviewer.
// Owner: Server.reviewFixCheckout (readReviewFixCheckout), real store and Git.
func TestRemovedFixingReviewerCheckoutIsReadFromItsKeptBranch(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	reviewer, err := f.server.sessions.CreateWorker(ctx, p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Security reviewer", IdempotencyKey: "removed-fix-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	writeRebaseFile(t, folder, "upload.go", "package upload\n")
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "coder work")
	reviewed := outboxPinGit(t, folder, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "runrm")
	outboxPinGit(t, folder, "worktree", "add", "-q", "-b", "agent/review-runrm", dir, reviewed)
	writeRebaseFile(t, dir, "upload.go", "package upload\n\n// escaped\n")
	outboxPinGit(t, dir, "commit", "-q", "-am", "escape the upload name")
	fixHead := outboxPinGit(t, dir, "rev-parse", "HEAD")
	store := f.server.sessions.Store()
	if err := store.CreateSession(pebblestore.SessionSnapshot{ID: "sess-review-rm", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: folder, Title: "Reviewer",
		WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/review-runrm", WorktreeBaseBranch: "main",
		Metadata: map[string]any{"swarm_v3_source_workspace_path": folder}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: "runrm", WorkerID: reviewer.ID, UserID: p.UserID,
		Role: pebblestore.WorkerRoleReviewer, SessionID: "sess-review-rm", Status: "succeeded", RequestSource: jobautomation.RequestSourceAutomation,
		Input: map[string]any{"prompt": "Review.", "review_fixes": jobautomation.FixesSecurity, "captured_review_target": map[string]any{"head": reviewed}}, CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "merge", "-q", "--ff-only", "agent/review-runrm")
	outboxPinGit(t, folder, "worktree", "remove", dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("checkout still present: %v", err)
	}

	g, fixing, err := f.server.reviewFixCheckout(ctx, p.AccountScopeID, reviewer.ID, "runrm")
	if err != nil || !fixing || g == nil {
		t.Fatalf("reviewFixCheckout after removal = %+v %v %v", g, fixing, err)
	}
	if g.Head != fixHead || g.BaseCommit != reviewed || g.Branch != "agent/review-runrm" || g.Worktree != folder ||
		len(g.Commits) != 1 || g.Commits[0].SHA != fixHead || g.Commits[0].Subject != "escape the upload name" {
		t.Fatalf("removed reviewer git = %+v", g)
	}

	// The kept branch rewritten below the reviewed head is still refused.
	outboxPinGit(t, folder, "branch", "-f", "agent/review-runrm", reviewed+"~0")
	outboxPinGit(t, folder, "checkout", "-q", "--orphan", "other")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "unrelated")
	outboxPinGit(t, folder, "branch", "-f", "agent/review-runrm", "HEAD")
	if g, _, err := f.server.reviewFixCheckout(ctx, p.AccountScopeID, reviewer.ID, "runrm"); err == nil || g != nil {
		t.Fatalf("a rewritten kept branch was read: %+v %v", g, err)
	}
}
