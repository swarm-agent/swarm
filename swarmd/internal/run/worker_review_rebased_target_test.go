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

// Purpose: a reviewer job of a coder job that was rebased onto a newer
// target branch after it was created reviews base..head from that newer
// base, so its range holds only the job's own commits, and its goal names
// the folder's target branch at its current tip ("Target branch now:").
// Threat (forge run): the reviewer reviewed recorded-base..HEAD, saw commits
// that had already landed on dev and blocked the job for "unrequested
// commits" it never made, and could not tell what dev contained.
// Owners: WorkerExecutionService.resolveReviewTarget (worktree.EffectiveTaskBase,
// reviewComparisonCommits, worktree.TargetBranchTip) and
// workerDirectRunMessage, over a real Pebble store (workers, runs, project,
// project task, sessions), the real workspace and worktree services and a
// real managed job clone rebased with real Git. No model provider is used.
// The narrowest layer that exercises the reviewer job's own target capture.
func TestReviewerJobOfARebasedJobReviewsOnlyItsOwnCommits(t *testing.T) {
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
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "rebased-review.pebble"))
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
	created := git(repo, "rev-parse", "HEAD")
	added, err := workspaces.AddForPrincipal(p, repo, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	source := added.WorkspacePath
	trees := worktree.NewService(pebblestore.NewWorktreeStore(db), workspaces, nil)
	coder, err := trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, source, "worker-execution-coderrun", "", "agent/worker-coderrun")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coder.WorkspacePath, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "upload.go")
	git(coder.WorkspacePath, "commit", "-q", "-m", "coder work")

	// dev moves on in the folder; the coder is told to rebase onto it.
	if err := os.WriteFile(filepath.Join(repo, "landed.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "landed.txt")
	git(repo, "commit", "-q", "-m", "landed elsewhere")
	landed := git(repo, "rev-parse", "HEAD")
	git(coder.WorkspacePath, "fetch", "-q", "origin")
	git(coder.WorkspacePath, "rebase", "-q", "origin/dev")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	db2 := sessions.Store()
	ws := db2.WorkerStore()
	coderWorker, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewerWorker, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.PutProject(account, &pebblestore.ProjectRecord{ID: "project-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: "coderrun", WorkerID: coderWorker.ID, UserID: user, Role: pebblestore.WorkerRoleCoder,
		SessionID: "worker-execution-coderrun", Status: "succeeded", RequestSource: "direct", CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db2.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "task_coderrun", Title: "Coder job", Description: "Add upload.", ProjectID: "project-a", AccountID: account,
		SessionID: "worker-execution-coderrun", Agent: "coder", Status: "needs_review", WorkerID: coderWorker.ID, WorkerRunID: "coderrun",
		WorkspacePath: coder.WorkspacePath, WorktreeBranch: coder.BranchName, BaseBranch: "dev", BaseCommit: created,
		SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: added.WorkspaceID, WorkspaceGeneration: max(added.WorkspaceGeneration, 1), Path: source, Provenance: "explicit"}}); err != nil {
		t.Fatal(err)
	}
	reviewSession := "worker-execution-revrun"
	if err := db2.CreateSession(pebblestore.SessionSnapshot{ID: reviewSession, UserID: user, AccountScopeID: account, WorkspacePath: source, Title: "Reviewer job",
		Metadata: map[string]any{"worker_id": reviewerWorker.ID, "worker_execution_run_id": "revrun", "project_id": "project-a"}, CreatedAt: 3, UpdatedAt: 3}); err != nil {
		t.Fatal(err)
	}
	job := "worker/" + coderWorker.ID + "/coderrun"
	r := pebblestore.WorkerRunRecord{ID: "revrun", WorkerID: reviewerWorker.ID, UserID: user, AccountScopeID: account, WorkspaceID: added.WorkspaceID,
		SessionID: reviewSession, Role: pebblestore.WorkerRoleReviewer, RequestSource: "automation", Input: map[string]any{"review_job": job}}
	service := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}, trees: trees}}

	captured, err := service.resolveReviewTarget(context.Background(), r, reviewerWorker)
	if err != nil {
		t.Fatalf("resolveReviewTarget: %v", err)
	}
	if captured["head"] != head || captured["base"] != landed {
		t.Fatalf("review target head %v base %v, want head %s and the new dev %s (created on %s)", captured["head"], captured["base"], head, landed, created)
	}
	commits, _ := captured["commits"].([]string)
	if len(commits) != 1 || commits[0] != landed {
		t.Fatalf("comparison commits = %v, want only the new base %s", captured["commits"], landed)
	}
	if captured["target"] != "dev at "+landed {
		t.Fatalf("captured target = %v, want dev at %s", captured["target"], landed)
	}
	// The reviewer's range holds only the job's own commit.
	if rangeCommits := git(coder.WorkspacePath, "rev-list", landed+".."+head); rangeCommits != head {
		t.Fatalf("review range %s..%s = %q, want only the job's commit", landed, head, rangeCommits)
	}

	// The reviewer's goal names the base and the target branch's current tip.
	r.AutomationID = "automation-1"
	r.Input["captured_review_target"] = captured
	message, err := workerDirectRunMessage(r, reviewerWorker, pebblestore.SessionPlanDocument{Title: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Head: " + head, "Base: " + landed, "Target branch now: dev at " + landed} {
		if !strings.Contains(message, want) {
			t.Fatalf("reviewer message lacks %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "Base: "+created) {
		t.Fatalf("reviewer message reviews from the stale base:\n%s", message)
	}
}
