package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"
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
// provider is used; a real sandbox Manager checks native cleanup and exclusion.
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
	if _, err := service.AllocateTaskReviewCheckout(p, &pebblestore.ProjectTaskRecord{}); err == nil || !strings.Contains(err.Error(), "review target required") {
		t.Fatalf("missing target accepted: %v", err)
	}
	// Cleanup failure must preserve the verdict and prevent successful settlement.
	cleanupSession := reviewRun("cleanup-fixture", nil).SessionID
	cleanupTask := &pebblestore.ProjectTaskRecord{ID: "cleanup-failure", Title: "Cleanup retry", ProjectID: "project-a", AccountID: account, SessionID: cleanupSession, Agent: "reviewer", Status: "needs_review", StartCommit: head}
	if err := db2.PutProjectTask(account, cleanupTask); err != nil {
		t.Fatal(err)
	}
	checked, err := service.FinishTaskReview(cleanupTask, pebblestore.JobReview{Verdict: "ok", Head: head})
	if err == nil || checked.Verdict != "ok" {
		t.Fatalf("cleanup failure not returned with review %+v %v", checked, err)
	}
	savedCleanup, _, err := db2.GetProjectTask(account, "project-a", cleanupTask.ID)
	if err != nil || savedCleanup.ReviewCleanup == nil || savedCleanup.ReviewCleanup.State != "failed" || savedCleanup.ReviewCleanup.NextAttemptAt == 0 {
		t.Fatalf("cleanup retry missing %+v %v", savedCleanup, err)
	}
	if err := service.CleanupTaskReview(cleanupTask); err == nil {
		t.Fatal("unfinished cleanup reported success")
	}
	afterCleanup, _, _ := db2.GetProjectTask(account, "project-a", cleanupTask.ID)
	if afterCleanup.ReviewCleanup.Attempts != 1 {
		t.Fatal("cleanup ignored backoff")
	}
	if _, err := db2.UpdateProjectTask(account, "project-a", cleanupTask.ID, func(current *pebblestore.ProjectTaskRecord) error {
		current.ReviewCleanup.NextAttemptAt = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.CleanupTaskReview(cleanupTask); err == nil {
		t.Fatal("unfinished cleanup reported success")
	}
	retriedCleanup, _, _ := db2.GetProjectTask(account, "project-a", cleanupTask.ID)
	if retriedCleanup.ReviewCleanup.Attempts != 2 || retriedCleanup.Review.Verdict != "ok" {
		t.Fatal("cleanup retry lost saved verdict")
	}
	// Native task review must use the same worker-job target resolver and reject
	// another principal before allocating anything. This real-store/Git layer
	// is the narrowest boundary proving ResolveTaskReviewTarget's authorization.
	worker, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: "coder", Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: "native-target", WorkerID: worker.ID, UserID: user, SessionID: taskSession, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := db2.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "task_native-target", Title: "Worker delivery", ProjectID: "project-a", AccountID: account, WorkerID: worker.ID, WorkerRunID: "native-target", SessionID: taskSession, Agent: "coder", Status: "needs_review", WorkspacePath: coder.WorkspacePath, WorktreeBranch: coder.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: sourceRef}); err != nil {
		t.Fatal(err)
	}
	native := &pebblestore.ProjectTaskRecord{SessionID: "native-review", ProjectID: "project-a", SourceWorkspace: sourceRef}
	job := "worker/" + worker.ID + "/native-target"
	target, err := service.ResolveTaskReviewTarget(context.Background(), p, native, job, "same", head)
	if err != nil {
		t.Fatal(err)
	}
	native.ReviewTarget = target
	allocation, err := service.AllocateTaskReviewCheckout(p, native)
	if err != nil || allocation.WorkspacePath != coder.WorkspacePath || git(allocation.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatalf("native worker review: %+v %v", allocation, err)
	}
	// Purpose: resolveRunWorkspaceScope must admit a task-bound fixing reviewer
	// without permitting a different branch, task, or unadmitted fix capability.
	// Real allocation and durable task lookup are the narrowest admission boundary;
	// cleanup must retain the writable lane and settle without retrying forever.
	fixTask := &pebblestore.ProjectTaskRecord{ID: "native-fixer", Title: "Security review", ProjectID: "project-a", AccountID: account, SessionID: "native-fixer-session", Agent: "reviewer", AutomationID: "workflow", ReviewFixes: ReviewFixesSecurity, ReviewTarget: map[string]any{"head": head}, SourceWorkspace: sourceRef}
	fixAllocation, err := service.AllocateTaskReviewCheckout(p, fixTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.PutProjectTask(account, fixTask); err != nil {
		t.Fatal(err)
	}
	fixSession := pebblestore.SessionSnapshot{ID: fixTask.SessionID, UserID: user, AccountScopeID: account, WorkspacePath: fixAllocation.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: fixAllocation.WorkspacePath, WorktreeBranch: fixAllocation.BranchName, WorktreeBaseBranch: head, Metadata: map[string]any{"project_id": fixTask.ProjectID, "task_id": fixTask.ID, "swarm_v3_source_workspace_path": source, "swarm_v3_worktree_base_commit": head, "swarm_v3_worktree_owner_session_id": fixTask.SessionID}}
	if err := service.PrepareTaskReviewSession(&fixSession, fixTask); err != nil {
		t.Fatal(err)
	}
	if scope, err := service.host.runs.resolveRunWorkspaceScope(fixSession, p); err != nil || scope.PrimaryPath != fixAllocation.WorkspacePath {
		t.Fatalf("native fix scope rejected: %+v %v", scope, err)
	}
	for _, field := range []string{"branch", "task", "fixes"} {
		bad := fixSession
		bad.Metadata = make(map[string]any)
		for k, v := range fixSession.Metadata {
			bad.Metadata[k] = v
		}
		switch field {
		case "branch":
			bad.WorktreeBranch = reviewFixBranch("other-task")
		case "task":
			bad.Metadata["task_id"] = "other-task"
		case "fixes":
			delete(bad.Metadata, "worker_review_fixes")
		}
		if _, err := service.host.runs.resolveRunWorkspaceScope(bad, p); err == nil {
			t.Fatalf("accepted mismatched native fix %s", field)
		}
	}
	if err := service.CleanupTaskReview(fixTask); err != nil {
		t.Fatal(err)
	}
	savedFix, _, err := db2.GetProjectTask(account, fixTask.ProjectID, fixTask.ID)
	if err != nil || savedFix.ReviewCleanup == nil || savedFix.ReviewCleanup.State != "done" || savedFix.ReviewCleanup.NextAttemptAt != 0 {
		t.Fatalf("fix cleanup did not settle: %+v %v", savedFix, err)
	}
	if git(fixAllocation.WorkspacePath, "rev-parse", "HEAD") != head || git(fixAllocation.WorkspacePath, "branch", "--show-current") != fixAllocation.BranchName {
		t.Fatal("admission rejection or cleanup changed the fix lane")
	}
	worktreesBefore = git(repo, "worktree", "list", "--porcelain")
	// PrepareTaskReviewSession must register the worker-job review as a
	// borrowed read-only identity, never as a security-fix checkout.
	manager, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, Backend: sandbox.BackendDocker, StateDir: t.TempDir(), Memory: "4g", MaxWorkerRuns: 2, UID: 10001, GID: 10001}, reviewRegistrationEngine{})
	if err != nil {
		t.Fatal(err)
	}
	previous := sandbox.Default()
	sandbox.SetDefault(manager)
	t.Cleanup(func() { sandbox.SetDefault(previous) })
	manager.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{source}, nil }, WorktreesRoot: filepath.Dir(filepath.Dir(coder.WorkspacePath)), Bucket: func(string) (string, error) { return filepath.Base(filepath.Dir(coder.WorkspacePath)), nil }})
	nativeSnapshot := pebblestore.SessionSnapshot{ID: native.SessionID, AccountScopeID: account, UserID: user, WorktreeRootPath: allocation.WorkspacePath, Metadata: map[string]any{"runtime_mode": "read"}}
	if err := service.PrepareTaskReviewSession(&nativeSnapshot, native); err != nil {
		t.Fatal(err)
	}
	if nativeSnapshot.Metadata["runtime_mode"] != "read" || sessionReviewFixes(nativeSnapshot.Metadata) || sandbox.ReviewSessionCheckout(native.SessionID) != allocation.WorkspacePath {
		t.Fatal("worker-job review not registered read-only")
	}
	cleanup, found, err := db2.GetReviewCleanup(account, native.SessionID)
	if err != nil || !found || !cleanup.Borrowed {
		t.Fatalf("review registration %+v %v", cleanup, err)
	}
	if err := manager.EndReview(context.Background(), allocation.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	manager.ForgetReview(allocation.WorkspacePath)
	sandbox.SetDefault(previous)
	other := p
	other.UserID = "other-user"
	if _, err := service.ResolveTaskReviewTarget(context.Background(), other, native, job, "same", head); err == nil || !strings.Contains(err.Error(), "review job not found") {
		t.Fatalf("cross-user review = %v", err)
	}
	if got := git(repo, "worktree", "list", "--porcelain"); got != worktreesBefore {
		t.Fatal("target resolution allocated an unauthorized worktree")
	}
	ctx := context.Background()
	// Native task reviews share target capture/allocation, without consulting
	// the reviewer worker. A stale delivery head must be rejected.
	native = &pebblestore.ProjectTaskRecord{ID: "native-review", ProjectID: "project-a", AccountID: account, SessionID: "native-session", SourceWorkspace: sourceRef}
	native.ReviewTarget, err = service.ResolveTaskReviewTarget(ctx, p, native, "task/project-a/task_coder", ReviewCheckoutSame, head)
	if err != nil {
		t.Fatal(err)
	}
	if alloc, err := service.AllocateTaskReviewCheckout(p, native); err != nil || alloc.WorkspacePath != coder.WorkspacePath || alloc.BaseCommit != head {
		t.Fatalf("native allocation: %+v %v", alloc, err)
	}
	if _, err := service.ResolveTaskReviewTarget(ctx, p, native, "task/project-a/task_coder", ReviewCheckoutSame, base); err == nil {
		t.Fatal("accepted stale review head")
	}

	// Native task settlement must release a borrowed coder lane through
	// CleanupTaskReview and the shared durable Manager cleanup. This real-store
	// boundary catches leaked registrations without launching a provider. A
	// competing live reviewer must be rejected without changing A's identity.
	sandbox.SetDefault(manager)
	for _, status := range []string{"needs_review", "failed", "cancelled"} {
		t.Run("native-cleanup-"+status, func(t *testing.T) {
			a := *native
			a.ID, a.SessionID = "cleanup-"+status, "session-"+status
			a.Title, a.Agent, a.Status, a.StartCommit = "Review", "reviewer", status, head
			a.AutomationID = "workflow:review"
			if err := db2.PutProjectTask(account, &a); err != nil {
				t.Fatal(err)
			}
			snapshot := pebblestore.SessionSnapshot{ID: a.SessionID, AccountScopeID: account, UserID: user, WorkspacePath: coder.WorkspacePath, WorktreeEnabled: true, WorktreeBranch: coder.BranchName, WorktreeRootPath: coder.WorkspacePath, Metadata: map[string]any{}}
			if err := service.PrepareTaskReviewSession(&snapshot, &a); err != nil {
				t.Fatal(err)
			}
			snapshot.Metadata["swarm_v3_source_workspace_path"] = repo
			snapshot.WorktreeBaseBranch = head
			snapshot.Metadata["swarm_v3_worktree_base_commit"] = head
			snapshot.Metadata["swarm_v3_worktree_owner_session_id"] = sessionReviewLaneOwner(snapshot.Metadata)
			snapshot.Metadata["project_id"] = a.ProjectID
			snapshot.Metadata["task_id"] = a.ID
			if err := db2.CreateSession(snapshot); err != nil {
				t.Fatal(err)
			}
			b := a
			b.SessionID += "-next"
			next := pebblestore.SessionSnapshot{ID: b.SessionID, AccountScopeID: account, WorktreeRootPath: coder.WorkspacePath, Metadata: map[string]any{}}
			if err := service.PrepareTaskReviewSession(&next, &b); err == nil || !strings.Contains(err.Error(), "the worktree is being reviewed by another session") {
				t.Fatalf("live reviewer exclusion failed %v", err)
			}
			if status == "cancelled" {
				// Resume durable closing state without re-registering an active
				// review or requiring its execution environment to be rebuilt.
				state, found, err := db2.GetReviewCleanup(account, a.SessionID)
				if err != nil || !found {
					t.Fatalf("missing recovery identity %v", err)
				}
				state.Phase = "closing"
				if err := db2.PutReviewCleanup(account, state); err != nil {
					t.Fatal(err)
				}
			}
			// Cleanup must reject mismatched durable identity without releasing A.
			identity, _, err := db2.GetReviewCleanup(account, a.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			badSnapshot := snapshot
			badSnapshot.ID += "-mismatch"
			if err := db2.CreateSession(badSnapshot); err != nil {
				t.Fatal(err)
			}
			badIdentity := identity
			badIdentity.Session = badSnapshot.ID
			badIdentity.Checkout += "-mismatch"
			if err := db2.PutReviewCleanup(account, badIdentity); err != nil {
				t.Fatal(err)
			}
			if err := service.cleanupReview(pebblestore.WorkerRunRecord{SessionID: badSnapshot.ID, Role: pebblestore.WorkerRoleReviewer}); err == nil || !strings.Contains(err.Error(), "review identity mismatch") {
				t.Fatalf("mismatched cleanup identity accepted %v", err)
			}
			if err := service.PrepareTaskReviewSession(&next, &b); err == nil {
				t.Fatal("mismatched cleanup released lane")
			}
			if err := db2.PutReviewCleanup(account, identity); err != nil {
				t.Fatal(err)
			}
			if _, err := service.FinishTaskReview(&a, pebblestore.JobReview{Verdict: "block", Head: head}); err != nil {
				t.Fatal(err)
			}
			saved, _, err := db2.GetProjectTask(account, a.ProjectID, a.ID)
			if err != nil || saved.ReviewCleanup == nil || saved.ReviewCleanup.State != "done" {
				t.Fatalf("native cleanup did not finish %+v %v", saved.ReviewCleanup, err)
			}
			// Runtime scope resolution must not resurrect A's completed lease.
			resolvedSnapshot, _, err := service.host.runs.sessions.GetSession(a.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			// The fixture engine cannot execute routed Git; scope resolution must
			// get past registration to that separate Git validation boundary.
			if err := service.host.runs.validateRunRepositoryIdentity(resolvedSnapshot, p); err == nil || !strings.Contains(err.Error(), "resolve git common dir") {
				t.Fatalf("unexpected scope validation result %v", err)
			}
			state, _, err := db2.GetReviewCleanup(account, a.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.RestoreReviewIdentity(state); err != nil {
				t.Fatal(err)
			}
			if err := service.PrepareTaskReviewSession(&next, &b); err != nil {
				t.Fatalf("next review could not borrow lane %v", err)
			}
			if err := manager.EndReview(ctx, coder.WorkspacePath); err != nil {
				t.Fatal(err)
			}
			manager.ForgetReview(coder.WorkspacePath)
			// Purpose: interrupted admission without a durable identity must
			// recover via registerReview and release the lane on both entry paths.
			for _, nativeCleanup := range []bool{false, true} {
				recovery := snapshot
				recovery.ID = fmt.Sprintf("%s-recovery-%t", snapshot.ID, nativeCleanup)
				if err := db2.CreateSession(recovery); err != nil {
					t.Fatal(err)
				}
				var cleanupErr error
				if nativeCleanup {
					recoveryTask := a
					recoveryTask.ID, recoveryTask.SessionID = recovery.ID, recovery.ID
					if err := db2.PutProjectTask(account, &recoveryTask); err != nil {
						t.Fatal(err)
					}
					cleanupErr = service.CleanupTaskReview(&recoveryTask)
				} else {
					cleanupErr = service.cleanupReview(pebblestore.WorkerRunRecord{SessionID: recovery.ID, Role: pebblestore.WorkerRoleReviewer})
				}
				if cleanupErr != nil {
					t.Fatal(cleanupErr)
				}
				recovered, found, err := db2.GetReviewCleanup(account, recovery.ID)
				if err != nil || !found || recovered.Phase != "volume_removed" {
					t.Fatalf("admission recovery not cleaned %+v %v", recovered, err)
				}
			}
		})
	}
	sandbox.SetDefault(previous)
	if got := git(repo, "worktree", "list", "--porcelain"); got != worktreesBefore || git(coder.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("native cleanup changed the coder worktree")
	}

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

// Only the container engine boundary is replaced, matching sandbox package
// fixtures. Review target resolution and identity registration remain real.
type reviewRegistrationEngine struct{}

func (reviewRegistrationEngine) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
		return []byte("sha256:img\n"), nil
	}
	if len(args) > 1 && args[0] == "info" {
		return []byte(`{"runc":{}}`), nil
	}
	return nil, nil
}
func (r reviewRegistrationEngine) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (reviewRegistrationEngine) RunWithIO(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
	return nil
}
