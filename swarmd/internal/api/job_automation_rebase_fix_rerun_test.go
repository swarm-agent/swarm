package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// workflowGitRepo is a real folder repository with worktrees, as the
// daemon keeps a task's worktree and a fix step's worktree of one folder.
type workflowGitRepo struct {
	t   *testing.T
	dir string
}

func (r workflowGitRepo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + r.dir, "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes name in dir and commits it there, returning the head.
func (r workflowGitRepo) commit(dir, name, body string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(dir, "add", name)
	r.git(dir, "commit", "-q", "-m", "change "+name)
	return r.git(dir, "rev-parse", "HEAD")
}

// fixStepAgent is the provider boundary of a fix step: its job "committed"
// head in worktree.
type fixStepAgent struct{ head, worktree string }

func (a fixStepAgent) Start(context.Context, jobautomation.AgentRequest) (string, error) {
	return "task/proj-ff/task_wf_fix", nil
}

func (a fixStepAgent) Wait(_ context.Context, _, jobID string) (jobautomation.AgentOutcome, error) {
	return jobautomation.AgentOutcome{JobID: jobID, Status: jobautomation.StatusSucceeded, Head: a.head,
		Result: &jobautomation.Result{Git: &jobautomation.ResultGit{Head: a.head, Worktree: a.worktree}}}, nil
}

// Requirement: when a fresh fix step's commits move a task's delivery head,
// the task's own branch and worktree are fast-forwarded to the new head, so
// the task's worktree HEAD equals the delivery head and a later follow-up
// starts from the fix; the outcome is recorded on the delivery and on the
// task. A dirty worktree, a move that is not a fast-forward (a rebase) or
// another branch moves nothing, and the reason is recorded on the task.
// Threat: a follow-up (message_task, reopen) of the task starting from the
// stale head so the person cherry-picks the fix by hand; or a fast-forward
// that discards uncommitted work or rewrites the task's branch.
// Owners: jobautomation.Runner (moveHead, syncTaskBranch) with the daemon's
// VerifyJobHead and jobAutomationAgents.AdvanceTaskBranch over a real Git
// folder with real worktrees and a real Pebble task store; only the fix
// job itself (the provider) is stubbed. Layer: AdvanceTaskBranch is the
// daemon boundary that owns both the task record and its worktree's Git.
func TestWorkflowFixStepFastForwardsTheTaskWorktreeToTheDeliveryHead(t *testing.T) {
	f := newFleetTestSwarm(t, "ff")
	t.Cleanup(f.cleanup)
	db := f.s.sessions.Store()
	folder := t.TempDir()
	repo := workflowGitRepo{t: t, dir: folder}
	repo.git(folder, "init", "-q", "-b", "main")
	base := repo.commit(folder, "README", "base\n")
	taskWorktree := filepath.Join(t.TempDir(), "task")
	repo.git(folder, "worktree", "add", "-q", "-b", "agent/task-1", taskWorktree, "main")
	taskHead := repo.commit(taskWorktree, "a.txt", "task\n")
	fixWorktree := filepath.Join(t.TempDir(), "fix")
	repo.git(folder, "worktree", "add", "-q", "-b", "agent/fix", fixWorktree, taskHead)
	fixHead := repo.commit(fixWorktree, "a.txt", "task, fixed\n")

	if err := db.PutProject(f.account, &pebblestore.ProjectRecord{ID: "proj-ff", Name: "FF"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutProjectTask(f.account, &pebblestore.ProjectTaskRecord{ID: "task-1", ProjectID: "proj-ff", Title: "Task", Agent: "coder", Status: "completed",
		WorktreeBranch: "agent/task-1", WorkspacePath: taskWorktree, BaseBranch: "main", BaseCommit: base,
		SourceWorkspace: pebblestore.ProjectTaskSource{Path: folder, WorkspaceID: "ws-ff", WorkspaceGeneration: 1, Provenance: "test"}}); err != nil {
		t.Fatal(err)
	}
	store := jobautomation.NewStore(db.Underlying())
	agents := jobAutomationAgents{s: f.s}
	runner := jobautomation.NewRunner(jobautomation.Options{Store: store, Agent: fixStepAgent{head: fixHead, worktree: fixWorktree}, Verify: VerifyJobHead, TaskBranch: agents})
	auto, _, err := store.Put(f.account, jobautomation.Automation{Name: "fix", Scope: jobautomation.Scope{Folder: folder}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "fix", Type: jobautomation.StepAgent, Worker: "coder", Brief: "Fix it."}}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := taskRunJobID("proj-ff", "task-1")
	d, created, err := store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, RunID: "task-1", ProjectID: "proj-ff", Folder: folder,
		Status: jobautomation.StatusSucceeded, Git: &jobautomation.ResultGit{Branch: "agent/task-1", BaseBranch: "main", BaseCommit: base, Head: taskHead, Worktree: taskWorktree,
			Commits: []jobautomation.ResultCommit{{SHA: taskHead}}}})
	if err != nil || !created {
		t.Fatal(created, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = store.GetDelivery(f.account, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != jobautomation.StateSucceeded || d.Head != fixHead || d.TaskBranch == nil || !d.TaskBranch.Advanced || d.TaskBranch.Head != fixHead {
		t.Fatalf("delivery = %s head %s task branch %+v steps %+v", d.State, d.Head, d.TaskBranch, d.Steps)
	}
	if got := repo.git(taskWorktree, "rev-parse", "HEAD"); got != d.Head {
		t.Fatalf("task worktree HEAD = %s, want the delivery head %s", got, d.Head)
	}
	if got := repo.git(folder, "rev-parse", "refs/heads/agent/task-1"); got != fixHead {
		t.Fatalf("task branch = %s, want %s", got, fixHead)
	}
	task, _, err := db.GetProjectTask(f.account, "proj-ff", "task-1")
	if err != nil || task.WorkflowBranch == nil || !task.WorkflowBranch.Advanced || task.WorkflowBranch.Head != fixHead || task.WorkflowBranch.DeliveryID != d.ID || task.WorkflowBranch.Step != "fix" {
		t.Fatalf("task workflow branch = %+v, %v", task.WorkflowBranch, err)
	}

	// Nothing moves a branch it must not: each refusal leaves the task's
	// worktree at fixHead and says why on the task.
	nextFix := repo.commit(fixWorktree, "a.txt", "task, fixed twice\n")
	repo.git(folder, "checkout", "-q", "-b", "rebased", "main")
	rebased := repo.commit(folder, "a.txt", "task, rebased\n")
	repo.git(folder, "checkout", "-q", "main")
	for _, tc := range []struct {
		name, branch, to, why string
		dirty                 bool
	}{
		{name: "dirty worktree", branch: "agent/task-1", to: nextFix, dirty: true, why: "uncommitted changes"},
		{name: "not a fast-forward", branch: "agent/task-1", to: rebased, why: "not a fast-forward"},
		{name: "another branch", branch: "agent/other", to: nextFix, why: "not the delivery's branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := filepath.Join(taskWorktree, "scratch.txt")
			if tc.dirty {
				if err := os.WriteFile(scratch, []byte("work in progress\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(scratch)
			}
			sync := agents.AdvanceTaskBranch(ctx, jobautomation.TaskBranchAdvance{Account: f.account, JobID: jobID, DeliveryID: "delivery-x", Step: "fix",
				Branch: tc.branch, From: fixHead, To: jobautomation.HeadRef{Commit: tc.to}})
			if sync.Advanced || !strings.Contains(sync.Reason, tc.why) {
				t.Fatalf("sync = %+v, want refused for %q", sync, tc.why)
			}
			if got := repo.git(taskWorktree, "rev-parse", "HEAD"); got != fixHead {
				t.Fatalf("task worktree moved to %s", got)
			}
			if tc.dirty {
				if data, err := os.ReadFile(scratch); err != nil || string(data) != "work in progress\n" {
					t.Fatalf("uncommitted work changed: %q %v", data, err)
				}
			}
			task, _, err := db.GetProjectTask(f.account, "proj-ff", "task-1")
			if err != nil || task.WorkflowBranch == nil || task.WorkflowBranch.Advanced || task.WorkflowBranch.Reason != sync.Reason || task.WorkflowBranch.Head != tc.to {
				t.Fatalf("task workflow branch = %+v, %v", task.WorkflowBranch, err)
			}
		})
	}
}

// Requirement: manage_projects rerun_delivery's service re-runs the newest
// workflow delivery of a task, found by the task id, exactly as POST
// /v3/job-deliveries/{id}/rerun does (its failed steps run again); older
// deliveries of the task are left alone. A task of another account, an
// unknown task and a task without deliveries are refused with nothing
// re-run. Threat: an AI re-running deliveries it cannot see, or the wrong
// (older) delivery. Owners: Server.RerunTaskDelivery over
// jobautomation.Store.NewestJobDelivery and Store.Rerun, with a real Pebble
// task and delivery store.
func TestRerunTaskDeliveryRerunsTheTasksNewestDelivery(t *testing.T) {
	f := newFleetTestSwarm(t, "rerun")
	t.Cleanup(f.cleanup)
	db := f.s.sessions.Store()
	store := jobautomation.NewStore(db.Underlying())
	f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	if err := db.PutProject(f.account, &pebblestore.ProjectRecord{ID: "proj-r", Name: "R"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-1", "task-empty"} {
		if err := db.PutProjectTask(f.account, &pebblestore.ProjectTaskRecord{ID: id, ProjectID: "proj-r", Title: id, Agent: "coder", Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	folder := t.TempDir()
	auto, _, err := store.Put(f.account, jobautomation.Automation{Name: "gate", Scope: jobautomation.Scope{Folder: folder}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "gate", Type: jobautomation.StepRun, Command: "gate.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := taskRunJobID("proj-r", "task-1")
	ids := []string{}
	for i, head := range []string{strings.Repeat("1", 40), strings.Repeat("2", 40)} {
		followup := ""
		if i > 0 {
			followup = "followup-2"
		}
		d, created, err := store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindTask, RunID: "task-1", Followup: followup, Folder: folder,
			Status: jobautomation.StatusSucceeded, Git: &jobautomation.ResultGit{Head: head, Commits: []jobautomation.ResultCommit{{SHA: head}}}})
		if err != nil || !created {
			t.Fatal(created, err)
		}
		d.State, d.Steps[0].State, d.Steps[0].Reason = jobautomation.StateFailed, jobautomation.StateFailed, "exited with code 1: the gate script is broken"
		if err := store.SaveDelivery(d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.user, AccountScopeID: f.account}

	for _, tc := range []struct {
		name    string
		p       identity.Principal
		task    string
		refused string
	}{
		{name: "another account", p: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "intruder", AccountScopeID: "acct-other"}, task: "task-1", refused: "task not found"},
		{name: "unknown task", p: p, task: "task-missing", refused: "task not found"},
		{name: "no delivery", p: p, task: "task-empty", refused: "no workflow delivery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.s.RerunTaskDelivery(tc.p, "proj-r", tc.task); err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("err = %v, want %q", err, tc.refused)
			}
			for _, id := range ids {
				if d, err := store.GetDelivery(f.account, id); err != nil || d.State != jobautomation.StateFailed || d.Reruns != 0 {
					t.Fatalf("delivery %s changed: %+v %v", id, d, err)
				}
			}
		})
	}

	out, err := f.s.RerunTaskDelivery(p, "proj-r", "task-1")
	if err != nil || !out.Rerun || out.Delivery.ID != ids[1] || out.Delivery.State != jobautomation.StatePending || out.Delivery.Steps[0].State != jobautomation.StatePending {
		t.Fatalf("rerun = %+v, %v", out, err)
	}
	if older, err := store.GetDelivery(f.account, ids[0]); err != nil || older.State != jobautomation.StateFailed || older.Reruns != 0 {
		t.Fatalf("the older delivery was touched: %+v %v", older, err)
	}
}

// Requirement: the Git a rebase step's checks read is the folder's: the tip
// a branch names, and the files the tree at a head changes against a base;
// a branch name that is an option or not a plain branch is refused before
// Git runs. Owners: jobAutomationAgents.TargetTip and ChangedFiles over a
// real Git folder.
func TestJobAutomationRebaseGitReadsTheFolder(t *testing.T) {
	folder := t.TempDir()
	repo := workflowGitRepo{t: t, dir: folder}
	repo.git(folder, "init", "-q", "-b", "main")
	base := repo.commit(folder, "README", "base\n")
	head := repo.commit(folder, "a.txt", "a\n")
	agents := jobAutomationAgents{}
	ctx := context.Background()
	tip, err := agents.TargetTip(ctx, folder, "main")
	if err != nil || tip != head {
		t.Fatalf("TargetTip = %s, %v; want %s", tip, err, head)
	}
	for _, branch := range []string{"", "-x", "a..b", "a b", "main@{1}", "refs/../x"} {
		if _, err := agents.TargetTip(ctx, folder, branch); err == nil {
			t.Fatalf("TargetTip(%q) accepted", branch)
		}
	}
	files, err := agents.ChangedFiles(ctx, folder, jobautomation.HeadRef{Commit: base}, jobautomation.HeadRef{Commit: head})
	if err != nil || strings.Join(files, ",") != "a.txt" {
		t.Fatalf("ChangedFiles = %v, %v", files, err)
	}
	if _, err := agents.ChangedFiles(ctx, folder, jobautomation.HeadRef{Commit: "main"}, jobautomation.HeadRef{Commit: head}); err == nil {
		t.Fatal("ChangedFiles accepted a ref that is not a commit id")
	}
}
