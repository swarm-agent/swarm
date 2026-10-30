package run

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// landedFixture is a real Pebble store, the real session, workspace and
// worktree services and a real Git folder on dev, with a worker whose jobs
// each get their own managed checkout on their own agent branch, recorded the
// way worker admission records them (session snapshot + run receipt).
type landedJobsFixture struct {
	t       *testing.T
	raw     *pebblestore.Store
	db      *pebblestore.SessionStore
	repo    string
	source  string
	trees   *worktree.Service
	worker  pebblestore.WorkerRecord
	service *WorkerExecutionService
	p       identity.Principal
}

const landedAccount, landedUser = "acct-landed", "user-landed"

func newLandedJobsFixture(t *testing.T) *landedJobsFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &landedJobsFixture{t: t}
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "landed.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.raw = store
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	f.p = identity.Principal{Type: identity.PrincipalTypeUser, UserID: landedUser, AccountScopeID: landedAccount, AccountScopeSource: identity.AccountScopeSourceServerState}
	workspaces := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	f.repo = t.TempDir()
	f.git(f.repo, "init", "-q", "-b", "dev")
	f.git(f.repo, "commit", "-q", "--allow-empty", "-m", "base")
	added, err := workspaces.AddForPrincipal(f.p, f.repo, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f.source = added.WorkspacePath
	f.trees = worktree.NewService(pebblestore.NewWorktreeStore(store), workspaces, nil)
	t.Cleanup(f.trees.Close)
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	f.db = sessions.Store()
	f.worker, err = f.db.WorkerStore().CreateWorker(landedAccount, landedUser, pebblestore.CreateWorkerRequest{Name: "Builder", Instructions: "Build.", IdempotencyKey: "builder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service = &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}, trees: f.trees}}
	return f
}

func (f *landedJobsFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type landedJob struct {
	id, session, path, branch, head string
}

// job admits one worker run with its own checkout, commits one file there and
// records the run with status.
func (f *landedJobsFixture) job(id, status string) landedJob {
	f.t.Helper()
	j := landedJob{id: id, session: "worker-execution-" + id, branch: "agent/worker-" + id}
	alloc, err := f.trees.AllocateDetachedWorkspaceRequestedForPrincipal(f.p, f.source, j.session, "", j.branch)
	if err != nil {
		f.t.Fatal(err)
	}
	j.path = alloc.WorkspacePath
	if err := os.WriteFile(filepath.Join(j.path, id+".txt"), []byte(id), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git(j.path, "add", id+".txt")
	f.git(j.path, "commit", "-q", "-m", "work "+id)
	j.head = f.git(j.path, "rev-parse", "HEAD")
	f.session(j.session, j.path, j.branch, map[string]any{"worker_id": f.worker.ID, "worker_execution_run_id": id})
	run := pebblestore.WorkerRunRecord{ID: id, WorkerID: f.worker.ID, UserID: landedUser, AccountScopeID: landedAccount, SessionID: j.session, RequestSource: "direct", Status: status}
	if pebblestore.AutomationV2Terminal(status) {
		run.CompletedAt = time.Now().UnixMilli()
	}
	if _, err := f.db.WorkerStore().RecordWorkerRun(landedAccount, run); err != nil {
		f.t.Fatal(err)
	}
	return j
}

func (f *landedJobsFixture) session(id, path, branch string, extra map[string]any) {
	f.t.Helper()
	meta := map[string]any{"swarm_v3_source_workspace_path": f.source, "swarm_v3_runtime_workspace_path": path, "swarm_v3_worktree_owner_session_id": id}
	for k, v := range extra {
		meta[k] = v
	}
	if err := f.db.CreateSession(pebblestore.SessionSnapshot{ID: id, UserID: landedUser, AccountScopeID: landedAccount, WorkspacePath: f.source, Title: "Job",
		Metadata: meta, WorktreeEnabled: true, WorktreeRootPath: path, WorktreeBranch: branch, WorktreeBaseBranch: "dev", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *landedJobsFixture) merge(j landedJob) {
	f.t.Helper()
	f.git(f.repo, "merge", "-q", "--no-ff", "-m", "merge "+j.id, j.branch)
}

func (f *landedJobsFixture) run(id string) pebblestore.WorkerRunRecord {
	f.t.Helper()
	r, found, err := f.db.WorkerStore().GetWorkerRun(landedAccount, f.worker.ID, id)
	if err != nil || !found {
		f.t.Fatalf("run %s: %v %v", id, found, err)
	}
	return r
}

func (f *landedJobsFixture) branchTip(branch string) string {
	return f.git(f.repo, "rev-parse", "refs/heads/"+branch)
}

func (f *landedJobsFixture) signals(kind string) []pebblestore.Signal {
	f.t.Helper()
	page, err := pebblestore.NewSignalStore(f.raw).ListAfter(0, 200, pebblestore.SignalFilter{Account: landedAccount, KindPrefixes: []string{kind}})
	if err != nil {
		f.t.Fatal(err)
	}
	return page.Signals
}

func (f *landedJobsFixture) assertRemoved(j landedJob, trigger string) {
	f.t.Helper()
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		f.t.Fatalf("job %s checkout still on disk: %v", j.id, err)
	}
	if tip := f.branchTip(j.branch); tip != j.head {
		f.t.Fatalf("job %s branch at %s, want kept at %s", j.id, tip, j.head)
	}
	r := f.run(j.id)
	rm := r.WorktreeRemoval
	if rm == nil || rm.State != pebblestore.WorktreeRemovalRemoved || rm.Path != j.path || rm.Branch != j.branch || rm.Head != j.head || rm.TargetBranch != "dev" || rm.Trigger != trigger {
		f.t.Fatalf("job %s removal record = %+v", j.id, rm)
	}
	if r.Status != "succeeded" && r.Status != "failed" && r.Status != "cancelled" {
		f.t.Fatalf("job %s result changed to %s", j.id, r.Status)
	}
	found := false
	for _, sig := range f.signals("worktree.removed") {
		if sig.Refs["run_id"] == j.id && sig.Attrs["path"] == j.path && sig.Attrs["branch"] == j.branch && sig.Attrs["head"] == j.head {
			found = true
		}
	}
	if !found {
		f.t.Fatalf("no worktree.removed activity event for job %s", j.id)
	}
}

func (f *landedJobsFixture) assertKept(j landedJob) {
	f.t.Helper()
	if _, err := os.Stat(j.path); err != nil {
		f.t.Fatalf("job %s checkout removed: %v", j.id, err)
	}
	if tip := f.branchTip(j.branch); tip == "" {
		f.t.Fatalf("job %s branch missing", j.id)
	}
	if rm := f.run(j.id).WorktreeRemoval; rm != nil {
		f.t.Fatalf("job %s recorded a removal while kept: %+v", j.id, rm)
	}
}

// Requirement: right after Swarm lands a finished run's work, its merged,
// clean checkout is removed, its branch, result and commits stay, and the
// removal is in the run's history and the activity feed.
// Threat: landed checkouts (0.6-1.2 GB each) filling the disk; a removal
// nobody can see. Owner: WorkerExecutionService.RemoveLandedWorktree (called
// by the API after IntegrateProjectTask) over a real store and real Git.
func TestLandedRunWorktreeIsRemovedAfterItsWorkLands(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("landed01", "succeeded")
	f.merge(j)

	outcome, err := f.service.RemoveLandedWorktree(j.session)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Removed || outcome.Removal.Head != j.head {
		t.Fatalf("outcome = %+v", outcome)
	}
	f.assertRemoved(j, LandedWorktreeTriggerLanded)
	if got := f.git(f.repo, "log", "--format=%H", "-1", j.branch); got != j.head {
		t.Fatalf("commits of the removed run unreadable: %s", got)
	}
	// Repeating it (a retried landing) is a no-op, not a failure.
	again, err := f.service.RemoveLandedWorktree(j.session)
	if err != nil || again.Removed {
		t.Fatalf("second removal = %+v, %v", again, err)
	}
	if n := len(f.signals("worktree.removed")); n != 1 {
		t.Fatalf("worktree.removed events = %d, want 1", n)
	}
}

// Requirement: the periodic sweep removes the backlog of finished runs whose
// work is in the target branch (merged by Swarm or outside it) and keeps every
// unsafe checkout: uncommitted or untracked changes, commits not in the
// target, an unfinished run (running, or waiting on a provider limit), the
// folder itself, and a checkout another run works in.
// Threat: deleting work that exists nowhere else or a checkout in use.
// Owner: WorkerExecutionService.SweepLandedWorktrees.
func TestLandedWorktreeSweepRemovesBacklogAndKeepsUnsafeCheckouts(t *testing.T) {
	f := newLandedJobsFixture(t)
	merged := f.job("merged01", "succeeded")
	f.merge(merged)
	failedMerged := f.job("failmrg1", "failed")
	f.merge(failedMerged)

	dirty := f.job("dirty001", "succeeded")
	f.merge(dirty)
	if err := os.WriteFile(filepath.Join(dirty.path, "notes.txt"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmerged := f.job("unmerged", "succeeded")
	running := f.job("running1", "running")
	f.merge(running)
	waiting := f.job("waiting1", pebblestore.WorkerRunStatusWaiting)
	f.merge(waiting)
	inUse := f.job("inuse001", "succeeded")
	f.merge(inUse)
	// Another unfinished run's session works in inUse's checkout (a review
	// reading it in place).
	f.session("worker-execution-reviewer", inUse.path, inUse.branch, map[string]any{"worker_id": f.worker.ID, "worker_execution_run_id": "reviewer", "swarm_v3_worktree_owner_session_id": inUse.session})
	if _, err := f.db.WorkerStore().RecordWorkerRun(landedAccount, pebblestore.WorkerRunRecord{ID: "reviewer", WorkerID: f.worker.ID, UserID: landedUser, AccountScopeID: landedAccount, SessionID: "worker-execution-reviewer", RequestSource: "direct", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	// A finished run whose recorded checkout is the folder itself.
	f.session("worker-execution-folder01", f.source, "dev", map[string]any{"worker_id": f.worker.ID, "worker_execution_run_id": "folder01"})
	if _, err := f.db.WorkerStore().RecordWorkerRun(landedAccount, pebblestore.WorkerRunRecord{ID: "folder01", WorkerID: f.worker.ID, UserID: landedUser, AccountScopeID: landedAccount, SessionID: "worker-execution-folder01", RequestSource: "direct", Status: "succeeded", CompletedAt: 1}); err != nil {
		t.Fatal(err)
	}

	removed, err := f.service.SweepLandedWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("sweep removed %d checkouts, want 2", removed)
	}
	f.assertRemoved(merged, LandedWorktreeTriggerSweep)
	f.assertRemoved(failedMerged, LandedWorktreeTriggerSweep)
	for _, j := range []landedJob{dirty, unmerged, running, waiting, inUse} {
		f.assertKept(j)
	}
	if _, err := os.Stat(filepath.Join(f.source, ".git")); err != nil {
		t.Fatalf("folder damaged: %v", err)
	}
	if rm := f.run("folder01").WorktreeRemoval; rm != nil {
		t.Fatalf("folder run recorded a removal: %+v", rm)
	}
	if got := f.git(dirty.path, "status", "--porcelain"); !strings.Contains(got, "notes.txt") {
		t.Fatalf("dirty checkout lost its untracked file: %q", got)
	}
	// Each checkout is kept for its own reason, not by accident.
	for _, tc := range []struct {
		session, reason string
	}{
		{dirty.session, "uncommitted or untracked"},
		{unmerged.session, "not in dev"},
		{running.session, "the run is running"},
		{waiting.session, "the run is waiting"},
		{inUse.session, "in use by worker-execution-reviewer"},
		{"worker-execution-folder01", "folder itself"},
	} {
		outcome, err := f.service.RemoveLandedWorktree(tc.session)
		if err != nil || outcome.Removed || !strings.Contains(outcome.Kept, tc.reason) {
			t.Fatalf("%s: outcome %+v, %v; want kept because %q", tc.session, outcome, err, tc.reason)
		}
	}
}

// Requirement: a task of Swarm's own agents keeps its checkout while it waits
// on a provider limit, and once finished and landed its checkout is removed
// with the removal in the task's history and the activity feed.
// Owner: WorkerExecutionService.RemoveLandedWorktree (task-owned sessions).
func TestLandedTaskWorktreeKeptWhileLimitWaitingAndRemovedOnceFinished(t *testing.T) {
	f := newLandedJobsFixture(t)
	const session, branch = "task-session-landed", "agent/task-landed"
	alloc, err := f.trees.AllocateDetachedWorkspaceRequestedForPrincipal(f.p, f.source, session, "", branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "task.txt"), []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(alloc.WorkspacePath, "add", "task.txt")
	f.git(alloc.WorkspacePath, "commit", "-q", "-m", "task work")
	head := f.git(alloc.WorkspacePath, "rev-parse", "HEAD")
	f.git(f.repo, "merge", "-q", "--ff-only", branch)
	f.session(session, alloc.WorkspacePath, branch, map[string]any{"project_id": "project-l", "task_id": "task-l"})
	if err := f.db.PutProject(landedAccount, &pebblestore.ProjectRecord{ID: "project-l", Name: "project-l"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PutProjectTask(landedAccount, &pebblestore.ProjectTaskRecord{ID: "task-l", Title: "Landed task", ProjectID: "project-l", AccountID: landedAccount,
		SessionID: session, Agent: "coder", Status: pebblestore.ProjectTaskStatusLimitWaiting, WorkspacePath: alloc.WorkspacePath, WorktreeBranch: branch, BaseBranch: "dev"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.RemoveLandedWorktree(session)
	if err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "in use by task task-l") {
		t.Fatalf("limit-waiting task: %+v, %v; want kept", outcome, err)
	}
	if _, err := os.Stat(alloc.WorkspacePath); err != nil {
		t.Fatalf("limit-waiting task's checkout removed: %v", err)
	}

	if _, err := f.db.UpdateProjectTask(landedAccount, "project-l", "task-l", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "completed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err = f.service.RemoveLandedWorktree(session)
	if err != nil || !outcome.Removed {
		t.Fatalf("finished task: %+v, %v; want removed", outcome, err)
	}
	if _, err := os.Stat(alloc.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("finished task's checkout still on disk: %v", err)
	}
	task, _, err := f.db.GetProjectTask(landedAccount, "project-l", "task-l")
	if err != nil {
		t.Fatal(err)
	}
	if rm := task.WorktreeRemoval; rm == nil || rm.State != pebblestore.WorktreeRemovalRemoved || rm.Head != head || rm.Branch != branch || rm.Path != alloc.WorkspacePath {
		t.Fatalf("task removal record = %+v", rm)
	}
	if task.Status != "completed" || task.WorktreeBranch != branch {
		t.Fatalf("task result changed: %+v", task)
	}
	events := f.signals("worktree.removed")
	if len(events) != 1 || events[0].Refs["task_id"] != "task-l" || events[0].Attrs["head"] != head {
		t.Fatalf("worktree.removed events = %+v", events)
	}
	if tip := f.branchTip(branch); tip != head {
		t.Fatalf("task branch at %s, want kept at %s", tip, head)
	}
}

// Requirement: a finished run whose follow-up is queued or running keeps its
// checkout. PrepareFollowup recreates a removed checkout and marks the
// follow-up queued before its V3 run exists, so in that window the run is
// terminal and its session idle; only WorkerRunRecord.FollowupActive shows
// the checkout is about to be used.
// Threat (review finding): a landed removal or sweep in that window removed
// the recreated checkout again and the follow-up started in a missing
// directory. Owner: WorkerExecutionService.removeLandedSessionWorktree.
func TestLandedWorktreeKeptWhileAFollowupIsQueuedOrRunning(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("queued01", "succeeded")
	f.merge(j)
	for _, status := range []string{"queued", "running"} {
		r := f.run(j.id)
		r.FollowupStatus = status
		if _, err := f.db.WorkerStore().RecordWorkerRun(landedAccount, r); err != nil {
			t.Fatal(err)
		}
		outcome, err := f.service.RemoveLandedWorktree(j.session)
		if err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "follow-up is "+status) {
			t.Fatalf("follow-up %s: %+v, %v; want kept", status, outcome, err)
		}
		f.service.landed.nextCheck = nil
		if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
			t.Fatalf("sweep with a %s follow-up removed %d: %v", status, removed, err)
		}
		f.assertKept(j)
	}
	// Once the follow-up is over the checkout goes.
	r := f.run(j.id)
	r.FollowupStatus = "succeeded"
	if _, err := f.db.WorkerStore().RecordWorkerRun(landedAccount, r); err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("after the follow-up: %+v, %v; want removed", outcome, err)
	}
	f.assertRemoved(j, LandedWorktreeTriggerLanded)
}

// Requirement: a checkout recreated for new work is never removed again
// before that work is marked active. PrepareFollowup recreates a run's
// checkout before the follow-up is reserved (and without a sandbox manager it
// is never reserved: the run is marked only once its V3 run is appended);
// ReopenProjectTask recreates a task's checkout before the task moves to
// in_progress. In that window the run or task still reads as finished and
// landed. ClaimSessionWorktree recreates under the evaluation slot
// and records the checkout as restored, which keeps it for landedRestoreGrace.
// Threat (review finding): a sweep passed its checks in that window and
// removed the freshly recreated checkout, so the follow-up or reopened task
// started in a missing directory. Owners: restoreRemovedRunWorktree,
// ClaimSessionWorktree and removeLandedSessionWorktree, over a real
// store and Git.
func TestRecreatedCheckoutIsKeptBetweenRecreateAndMarking(t *testing.T) {
	f := newLandedJobsFixture(t)
	now := time.Unix(1_800_000_000, 0)
	f.service.landed.nowForTest = func() time.Time { return now }
	j := f.job("recreat1", "succeeded")
	f.merge(j)
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("removal = %+v, %v", outcome, err)
	}
	snap, _, err := f.db.GetSession(j.session)
	if err != nil {
		t.Fatal(err)
	}
	// PrepareFollowup's recreate, with no reservation after it.
	if err := f.service.restoreRemovedRunWorktree(snap, f.run(j.id)); err != nil {
		t.Fatal(err)
	}
	if rm := f.run(j.id).WorktreeRemoval; rm == nil || rm.State != pebblestore.WorktreeRemovalRestored || rm.Path != j.path || rm.Head != j.head {
		t.Fatalf("restore record = %+v", rm)
	}
	if r := f.run(j.id); r.FollowupActive() {
		t.Fatalf("fixture: the follow-up is already marked %s", r.FollowupStatus)
	}
	// A sweep and a landed removal in the window before the follow-up is
	// marked keep the checkout.
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
		t.Fatalf("sweep after recreate removed %d: %v", removed, err)
	}
	outcome, err := f.service.RemoveLandedWorktree(j.session)
	if err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "reserved for new work") {
		t.Fatalf("landed removal after recreate = %+v, %v", outcome, err)
	}
	if state, err := f.trees.InspectTaskWorkspace(j.path); err != nil || state.HeadCommit != j.head || state.BranchName != j.branch {
		t.Fatalf("recreated checkout = %+v, %v", state, err)
	}
	// Past the grace, with no follow-up started, the ordinary rules apply.
	now = now.Add(landedRestoreGrace + time.Minute)
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("after the grace = %+v, %v; want removed", outcome, err)
	}
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		t.Fatalf("checkout still present after the grace: %v", err)
	}
}

// Requirement: the recreation waits for an evaluation already in flight
// (which holds the slot from its state checks to its Git removal), so an
// evaluation can never see the run as idle and then remove a checkout
// recreated while it waited. Owner: ClaimSessionWorktree's slot.
func TestRecreateWaitsForAnEvaluationInFlight(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("inflight", "succeeded")
	f.merge(j)
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("removal = %+v, %v", outcome, err)
	}
	f.service.landed.acquire(-1) // an evaluation holds the slot
	done := make(chan error, 1)
	go func() {
		_, err := f.service.ClaimSessionWorktree(j.session)
		done <- err
	}()
	select {
	case err := <-done:
		f.service.landed.release()
		t.Fatalf("recreate did not wait for the evaluation in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		f.service.landed.release()
		t.Fatalf("checkout recreated while an evaluation held the slot: %v", err)
	}
	f.service.landed.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("recreate did not finish after the slot was released")
	}
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
		t.Fatalf("sweep removed the recreated checkout: %d %v", removed, err)
	}
	if _, err := os.Stat(j.path); err != nil {
		t.Fatalf("recreated checkout missing: %v", err)
	}
}

// Requirement: a follow-up that arrives while a sweep is evaluating the
// job's never-removed checkout (the sweep holds the slot from its state
// checks through the Git removal) still runs in a checkout: its claim waits
// for the slot, re-reads the removal record the sweep wrote, recreates the
// checkout from the kept branch and claims it, so no later evaluation removes
// it before the follow-up is marked. Without a sandbox manager PrepareFollowup
// never reserves the follow-up, so the claim is the only marker.
// Threat (review finding): PrepareFollowup read no removal record before the
// sweep finished, skipped the restore, and the follow-up started in a deleted
// directory. Owners: ClaimSessionWorktree (called by PrepareFollowup through
// restoreRemovedRunWorktree) and removeLandedSessionWorktree, over a real
// store and Git. The sweep is held inside its Git removal by a remover that
// waits for the test, then runs the real worktree.Service removal.
func TestFollowupArrivingDuringAnEvaluationGetsItsCheckout(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("arrive01", "succeeded")
	f.merge(j)
	entered, proceed := make(chan struct{}), make(chan struct{})
	f.service.landed.removerForTest = landedRemoverFunc(func(in worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error) {
		close(entered)
		<-proceed
		return f.trees.RemoveLandedWorktree(in)
	})
	swept := make(chan error, 1)
	go func() {
		_, err := f.service.SweepLandedWorktrees(context.Background())
		swept <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("sweep never reached its removal")
	}
	// The follow-up arrives now, with the run as PrepareFollowup reads it:
	// finished, no follow-up marked, no removal recorded yet.
	snap, _, err := f.db.GetSession(j.session)
	if err != nil {
		t.Fatal(err)
	}
	stale := f.run(j.id)
	if stale.WorktreeRemoval != nil || stale.FollowupActive() {
		t.Fatalf("fixture: run already marked: %+v", stale)
	}
	claimed := make(chan error, 1)
	go func() { claimed <- f.service.restoreRemovedRunWorktree(snap, stale) }()
	select {
	case err := <-claimed:
		t.Fatalf("follow-up claim did not wait for the evaluation in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(proceed)
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-claimed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("follow-up claim never finished")
	}
	// The follow-up's checkout is there, on its branch at its head, clean.
	state, err := f.trees.InspectTaskWorkspace(j.path)
	if err != nil || state.BranchName != j.branch || state.HeadCommit != j.head || !state.Clean {
		t.Fatalf("follow-up checkout = %+v, %v", state, err)
	}
	if rm := f.run(j.id).WorktreeRemoval; rm == nil || rm.State != pebblestore.WorktreeRemovalRestored {
		t.Fatalf("run record = %+v, want restored", rm)
	}
	// Later evaluations keep it while the follow-up starts.
	f.service.landed.removerForTest = nil
	f.service.landed.nextCheck = nil
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
		t.Fatalf("sweep after the claim removed %d: %v", removed, err)
	}
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "reserved for new work") {
		t.Fatalf("landed removal after the claim = %+v, %v", outcome, err)
	}
	if _, err := os.Stat(j.path); err != nil {
		t.Fatalf("follow-up checkout removed: %v", err)
	}
}

// Requirement: a message to a finished task's session that arrives while an
// evaluation of its never-removed checkout holds the slot (from its state
// checks through the Git removal) still gets its checkout: the message's
// preparation (PrepareSessionMessage, called by POST /v3/sessions/{id}/messages
// before the run is admitted) waits for the slot, re-reads the record the
// evaluation wrote, recreates the checkout and claims it. A session no
// evaluation could remove (no task or worker run behind it) never waits.
// Threat (review finding): a message admitted between the evaluation's
// intent/lifecycle check and its removal started a run in a checkout being
// deleted. Owner: WorkerExecutionService.PrepareSessionMessage over a real
// store and Git; the evaluation is held inside the real removal.
func TestSessionMessageArrivingDuringAnEvaluationKeepsItsCheckout(t *testing.T) {
	f := newLandedJobsFixture(t)
	const session, branch = "task-session-msg", "agent/task-msg"
	alloc, err := f.trees.AllocateDetachedWorkspaceRequestedForPrincipal(f.p, f.source, session, "", branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "task.txt"), []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(alloc.WorkspacePath, "add", "task.txt")
	f.git(alloc.WorkspacePath, "commit", "-q", "-m", "task work")
	head := f.git(alloc.WorkspacePath, "rev-parse", "HEAD")
	f.git(f.repo, "merge", "-q", "--ff-only", branch)
	f.session(session, alloc.WorkspacePath, branch, map[string]any{"project_id": "project-msg", "task_id": "task-msg"})
	if err := f.db.PutProject(landedAccount, &pebblestore.ProjectRecord{ID: "project-msg", Name: "project-msg"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PutProjectTask(landedAccount, &pebblestore.ProjectTaskRecord{ID: "task-msg", Title: "Task", ProjectID: "project-msg", AccountID: landedAccount,
		SessionID: session, Agent: "coder", Status: "needs_review", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: branch, BaseBranch: "dev"}); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	f.service.landed.removerForTest = landedRemoverFunc(func(in worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error) {
		close(entered)
		<-proceed
		return f.trees.RemoveLandedWorktree(in)
	})
	evaluated := make(chan error, 1)
	go func() {
		_, err := f.service.RemoveLandedWorktree(session)
		evaluated <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("evaluation never reached its removal")
	}
	// An interactive session with its own checkout but no task or run behind
	// it is never removed, so its message does not wait for the slot.
	f.session("plain-session", filepath.Join(t.TempDir(), "plain"), "agent/plain", nil)
	if err := f.service.PrepareSessionMessage(landedAccount, landedUser, "plain-session"); err != nil {
		t.Fatalf("plain session message: %v", err)
	}
	prepared := make(chan error, 1)
	go func() { prepared <- f.service.PrepareSessionMessage(landedAccount, landedUser, session) }()
	select {
	case err := <-prepared:
		t.Fatalf("message preparation did not wait for the evaluation in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(proceed)
	if err := <-evaluated; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-prepared:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("message preparation never finished")
	}
	state, err := f.trees.InspectTaskWorkspace(alloc.WorkspacePath)
	if err != nil || state.BranchName != branch || state.HeadCommit != head || !state.Clean {
		t.Fatalf("checkout for the message's run = %+v, %v", state, err)
	}
	f.service.landed.removerForTest = nil
	if outcome, err := f.service.RemoveLandedWorktree(session); err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "reserved for new work") {
		t.Fatalf("removal after the message = %+v, %v; want kept", outcome, err)
	}
	if _, err := os.Stat(alloc.WorkspacePath); err != nil {
		t.Fatalf("message's checkout removed: %v", err)
	}
}

// Requirement: a claim on a checkout that was never removed (nothing to
// recreate) still keeps it from every evaluation for the claim window, and
// writes no removal record. Owner: ClaimSessionWorktree.
func TestClaimKeepsANeverRemovedCheckout(t *testing.T) {
	f := newLandedJobsFixture(t)
	now := time.Unix(1_800_000_000, 0)
	f.service.landed.nowForTest = func() time.Time { return now }
	j := f.job("claim001", "succeeded")
	f.merge(j)
	if recreated, err := f.service.ClaimSessionWorktree(j.session); err != nil || recreated {
		t.Fatalf("claim = %v, %v", recreated, err)
	}
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
		t.Fatalf("sweep removed a claimed checkout: %d %v", removed, err)
	}
	f.assertKept(j)
	now = now.Add(landedRestoreGrace + time.Minute)
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("after the claim window = %+v, %v; want removed", outcome, err)
	}
}

// Requirement: a reopened task's recreated checkout is kept the same way
// while the task still reads completed (ReopenProjectTask recreates it
// through ClaimSessionWorktree before moving it to in_progress).
func TestReopenedTaskCheckoutIsKeptBeforeTheTaskIsActive(t *testing.T) {
	f := newLandedJobsFixture(t)
	const session, branch = "task-session-reopen", "agent/task-reopen"
	alloc, err := f.trees.AllocateDetachedWorkspaceRequestedForPrincipal(f.p, f.source, session, "", branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "task.txt"), []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(alloc.WorkspacePath, "add", "task.txt")
	f.git(alloc.WorkspacePath, "commit", "-q", "-m", "task work")
	f.git(f.repo, "merge", "-q", "--ff-only", branch)
	f.session(session, alloc.WorkspacePath, branch, map[string]any{"project_id": "project-r", "task_id": "task-r"})
	if err := f.db.PutProject(landedAccount, &pebblestore.ProjectRecord{ID: "project-r", Name: "project-r"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PutProjectTask(landedAccount, &pebblestore.ProjectTaskRecord{ID: "task-r", Title: "Reopened task", ProjectID: "project-r", AccountID: landedAccount,
		SessionID: session, Agent: "coder", Status: "completed", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: branch, BaseBranch: "dev"}); err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.service.RemoveLandedWorktree(session); err != nil || !outcome.Removed {
		t.Fatalf("removal = %+v, %v", outcome, err)
	}
	if restored, err := f.service.ClaimSessionWorktree(session); err != nil || !restored {
		t.Fatalf("restore = %v, %v", restored, err)
	}
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 0 {
		t.Fatalf("sweep removed the reopened task's checkout: %d %v", removed, err)
	}
	if _, err := os.Stat(alloc.WorkspacePath); err != nil {
		t.Fatalf("reopened task's checkout missing: %v", err)
	}
	task, _, err := f.db.GetProjectTask(landedAccount, "project-r", "task-r")
	if err != nil || task.WorktreeRemoval == nil || task.WorktreeRemoval.State != pebblestore.WorktreeRemovalRestored {
		t.Fatalf("task restore record = %+v, %v", task, err)
	}
}

// Requirement: a removal right after a landing never waits behind a sweep:
// while another evaluation holds the slot it waits at most its bound, keeps
// the checkout without error or record, and the sweep removes it later.
// Threat (review finding): IntegrateProjectTask's response waited for a
// whole sweep pass over a large backlog. Owners:
// WorkerExecutionService.RemoveLandedWorktree and SweepLandedWorktrees
// (landedWorktreeSweep.acquire); the slot is held directly so the test is
// deterministic.
func TestLandedRemovalDoesNotWaitBehindAnotherEvaluation(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("busy0001", "succeeded")
	f.merge(j)
	f.service.landed.landingWaitForTest = 50 * time.Millisecond
	f.service.landed.acquire(-1) // a sweep is evaluating another checkout
	start := time.Now()
	outcome, err := f.service.RemoveLandedWorktree(j.session)
	elapsed := time.Since(start)
	f.service.landed.release()
	if err != nil || outcome.Removed || !strings.Contains(outcome.Kept, "sweep removes this one later") {
		t.Fatalf("landing while busy: %+v, %v", outcome, err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("landing waited %s behind another evaluation", elapsed)
	}
	f.assertKept(j)
	if removed, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || removed != 1 {
		t.Fatalf("sweep removed %d: %v", removed, err)
	}
	f.assertRemoved(j, LandedWorktreeTriggerSweep)
}

// Requirement: a daemon stop never waits for a whole sweep pass. Once the
// sweep's context ends it stops between candidates (never in the middle of a
// checkout's removal), so the checkout it was removing is finished and
// recorded, and the rest are left for the next start.
// Threat (review finding): WorkerScheduler.Run waits for the sweep goroutine
// on shutdown and SweepLandedWorktrees ignored cancellation, so a stop with a
// large backlog blocked for minutes. Owner: SweepLandedWorktrees(ctx); the
// context is cancelled from inside the first real removal.
func TestLandedWorktreeSweepStopsBetweenCandidatesWhenCancelled(t *testing.T) {
	f := newLandedJobsFixture(t)
	jobs := []landedJob{f.job("cancel01", "succeeded"), f.job("cancel02", "succeeded"), f.job("cancel03", "succeeded")}
	for _, j := range jobs {
		f.merge(j)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	f.service.landed.removerForTest = landedRemoverFunc(func(in worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error) {
		calls++
		cancel() // the daemon stops while this removal runs
		return f.trees.RemoveLandedWorktree(in)
	})
	removed, err := f.service.SweepLandedWorktrees(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sweep error = %v, want context.Canceled", err)
	}
	if calls != 1 || removed != 1 {
		t.Fatalf("cancelled sweep made %d removals and removed %d, want 1 and 1", calls, removed)
	}
	gone, kept := 0, 0
	for _, j := range jobs {
		if _, statErr := os.Stat(j.path); os.IsNotExist(statErr) {
			f.assertRemoved(j, LandedWorktreeTriggerSweep) // finished and recorded
			gone++
		} else {
			f.assertKept(j)
			kept++
		}
	}
	if gone != 1 || kept != 2 {
		t.Fatalf("after cancel: %d removed, %d kept; want 1 and 2", gone, kept)
	}
	// A context already ended evaluates nothing.
	if n, err := f.service.SweepLandedWorktrees(ctx); n != 0 || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("sweep with an ended context = %d, %v, %d removals", n, err, calls)
	}
	// The next pass finishes the rest.
	f.service.landed.removerForTest = nil
	if n, err := f.service.SweepLandedWorktrees(context.Background()); err != nil || n != 2 {
		t.Fatalf("next pass removed %d: %v", n, err)
	}
}

// Requirement: a failed removal keeps the checkout, is recorded on the run
// and reported once, and is not retried until its backoff passes.
// Threat: a silent failure, or a tight retry loop hammering the disk.
func TestLandedWorktreeRemovalFailureIsRecordedAndBackedOff(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("broken01", "succeeded")
	f.merge(j)
	calls := 0
	f.service.landed.removerForTest = landedRemoverFunc(func(worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error) {
		calls++
		return worktree.LandedWorktreeRemoval{}, errors.New("disk refused")
	})
	now := time.Unix(1_800_000_000, 0)
	f.service.landed.nowForTest = func() time.Time { return now }

	if _, err := f.service.RemoveLandedWorktree(j.session); err == nil {
		t.Fatal("removal failure was not reported")
	}
	rm := f.run(j.id).WorktreeRemoval
	if rm == nil || rm.State != pebblestore.WorktreeRemovalFailed || rm.Attempts != 1 || rm.LastError != "disk refused" || rm.NextAttemptAt <= now.UnixMilli() {
		t.Fatalf("failure record = %+v", rm)
	}
	if _, err := f.service.SweepLandedWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("removal retried before its backoff: %d attempts", calls)
	}
	if n := len(f.signals("worktree.remove_failed")); n != 1 {
		t.Fatalf("worktree.remove_failed events = %d, want 1", n)
	}
	if _, err := os.Stat(j.path); err != nil {
		t.Fatalf("checkout removed after a failed removal: %v", err)
	}
	if tip := f.branchTip(j.branch); tip != j.head {
		t.Fatalf("branch moved to %s", tip)
	}
}

type landedRemoverFunc func(worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error)

func (fn landedRemoverFunc) RemoveLandedWorktree(in worktree.LandedWorktree) (worktree.LandedWorktreeRemoval, error) {
	return fn(in)
}

// Requirement: anything that needs a removed run's checkout again (a
// follow-up message) recreates it from the kept branch at the same path, as
// PrepareFollowup does before the follow-up runs.
// Owner: WorkerExecutionService.restoreRemovedRunWorktree.
func TestFollowupRecreatesARemovedRunWorktreeFromItsBranch(t *testing.T) {
	f := newLandedJobsFixture(t)
	j := f.job("follow01", "succeeded")
	f.merge(j)
	if outcome, err := f.service.RemoveLandedWorktree(j.session); err != nil || !outcome.Removed {
		t.Fatalf("removal = %+v, %v", outcome, err)
	}
	snap, _, err := f.db.GetSession(j.session)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.restoreRemovedRunWorktree(snap, f.run(j.id)); err != nil {
		t.Fatal(err)
	}
	state, err := f.trees.InspectTaskWorkspace(j.path)
	if err != nil {
		t.Fatal(err)
	}
	if state.BranchName != j.branch || state.HeadCommit != j.head || !state.Clean {
		t.Fatalf("recreated checkout = %+v", state)
	}
	if err := f.trees.ValidateSessionRepositoryLaneForRead(f.source, j.path, j.session, j.branch); err != nil {
		t.Fatalf("recreated checkout is not the session's lane: %v", err)
	}
}
