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

// Purpose: a reviewer worker reviews a task Swarm's own coder finished
// (review_job task/<project>/<task>), the way it reviews a worker job: the
// target is the task's head in its own session's worktree of the folder,
// from its base, in the reviewer's project and folder; its verdict is stored
// against the task and read back by the reviewer job that recorded it. A task
// in another project, a worker job's task named as a task, or a reviewer that
// does not own the target is refused.
// Threat: a workflow that cannot put a reviewer after the coder that comes
// with Swarm, a reviewer pointed at another project's task, or a verdict
// recorded for a task the reviewer never reviewed.
// Owners: validateReviewRequest, WorkerExecutionService.resolveReviewTarget
// and WorkerStore.RecordTaskReview / GetJobReviewByReviewer over a real
// Pebble store, the real workspace and worktree services and real Git; no
// model provider is used.
func TestReviewerReviewsATaskOfSwarmsOwnCoder(t *testing.T) {
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
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-review.pebble"))
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
	// The coder task's session worktree, allocated as task execution does.
	const taskSession = "task-session-coder"
	coder, err := trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, source, taskSession, "", "agent/task-coder")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coder.WorkspacePath, "limit.go"), []byte("package api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "limit.go")
	git(coder.WorkspacePath, "commit", "-q", "-m", "add the rate limit")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	db2 := sessions.Store()
	ws := db2.WorkerStore()
	reviewerWorker, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"project-a", "project-b"} {
		if err := db2.PutProject(account, &pebblestore.ProjectRecord{ID: project, Name: project}); err != nil {
			t.Fatal(err)
		}
	}
	sourceRef := pebblestore.ProjectTaskSource{WorkspaceID: added.WorkspaceID, WorkspaceGeneration: max(added.WorkspaceGeneration, 1), Path: source, Provenance: "explicit"}
	if err := db2.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "task_coder", Title: "Rate limit", Description: "Add a rate limit.", ProjectID: "project-a", AccountID: account,
		SessionID: taskSession, Agent: "coder", Status: "needs_review", WorkspacePath: coder.WorkspacePath, WorktreeBranch: coder.BranchName, BaseBranch: "dev", BaseCommit: base,
		SourceWorkspace: sourceRef}); err != nil {
		t.Fatal(err)
	}
	reviewRun := func(id, job string) pebblestore.WorkerRunRecord {
		t.Helper()
		if err := db2.CreateSession(pebblestore.SessionSnapshot{ID: "worker-execution-" + id, UserID: user, AccountScopeID: account, WorkspacePath: source, Title: "Reviewer job",
			Metadata: map[string]any{"worker_id": reviewerWorker.ID, "worker_execution_run_id": id, "project_id": "project-a"}, CreatedAt: 3, UpdatedAt: 3}); err != nil {
			t.Fatal(err)
		}
		return pebblestore.WorkerRunRecord{ID: id, WorkerID: reviewerWorker.ID, UserID: user, AccountScopeID: account, WorkspaceID: added.WorkspaceID,
			SessionID: "worker-execution-" + id, Role: pebblestore.WorkerRoleReviewer, RequestSource: "automation", Input: map[string]any{"review_job": job, "project_id": "project-a"}}
	}
	service := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}, trees: trees}}

	r := reviewRun("revtask", "task/project-a/task_coder")
	captured, err := service.resolveReviewTarget(context.Background(), r, reviewerWorker)
	if err != nil {
		t.Fatalf("resolveReviewTarget: %v", err)
	}
	if captured["head"] != head || captured["base"] != base || captured["job"] != "task/project-a/task_coder" || captured["brief"] != "Add a rate limit." {
		t.Fatalf("review target = %+v, want head %s base %s", captured, head, base)
	}

	// Refused: another project's task, a malformed target.
	for name, job := range map[string]string{"other project": "task/project-b/task_coder", "malformed": "task/project-a"} {
		if _, err := service.resolveReviewTarget(context.Background(), reviewRun("rev-"+strings.ReplaceAll(name, " ", "-"), job), reviewerWorker); err == nil {
			t.Fatalf("%s target %q was accepted", name, job)
		}
	}

	// The verdict is stored against the task, for the reviewer job that
	// recorded it; a reviewer that does not own the target cannot record one.
	if _, err := ws.RecordWorkerRun(account, r); err != nil {
		t.Fatal(err)
	}
	reviewerJob := "worker/" + reviewerWorker.ID + "/revtask"
	review := pebblestore.JobReview{Verdict: "block", Head: head, ReviewerJobID: reviewerJob, RecordedAt: 10, Findings: "1. high limit.go:1 nothing is limited"}
	if err := ws.RecordTaskReview(account, "project-a", "task_coder", review); err != nil {
		t.Fatal(err)
	}
	got, err := ws.GetJobReviewByReviewer(account, pebblestore.TaskReviewWorker("project-a"), "task_coder", reviewerJob)
	if err != nil || got == nil || got.Verdict != "block" || got.Findings != review.Findings {
		t.Fatalf("stored task review = %+v %v", got, err)
	}
	stranger := reviewRun("revstranger", "task/project-a/other_task")
	if _, err := ws.RecordWorkerRun(account, stranger); err != nil {
		t.Fatal(err)
	}
	if err := ws.RecordTaskReview(account, "project-a", "task_coder", pebblestore.JobReview{Verdict: "ok", Head: head, ReviewerJobID: "worker/" + reviewerWorker.ID + "/revstranger", RecordedAt: 11}); err == nil {
		t.Fatal("a reviewer of another task recorded a verdict on this one")
	}
	if got, _ := ws.GetJobReviewByReviewer(account, pebblestore.TaskReviewWorker("project-a"), "task_coder", "worker/"+reviewerWorker.ID+"/revstranger"); got != nil {
		t.Fatalf("refused review was stored: %+v", got)
	}
}
