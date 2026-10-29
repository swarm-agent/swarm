package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// newCheckpointFixture is a reopen-capable server with a failed task whose
// retained worktree holds the edits its stopped run never committed: a
// changed tracked file and two new untracked files (one in a new folder).
func newCheckpointFixture(t *testing.T) (*confirmationFixture, retainedTask) {
	t.Helper()
	root := t.TempDir()
	f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "continued", StopReason: "stop"}, nil
	}
	retained := seedRetainedTask(t, f.server, f.sessions, testPrincipal(), filepath.Join(root, "repo"), "failed")
	for name, content := range map[string]string{"tracked.txt": "base\nhalf-done edit\n", "new_test.go": "package x\n", "pkg/added.go": "package pkg\n"} {
		path := filepath.Join(retained.child, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f, retained
}

func checkpointReopen(t *testing.T, f *confirmationFixture, r retainedTask, key string, revision int) (*pebblestore.ProjectTaskRecord, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return f.server.ReopenProjectTask(ctx, r.p, r.projectID, r.taskID, tool.ProjectTaskFollowupInput{Feedback: "finish the reviewer's send-back round", ClientRequestID: key, Revision: revision})
}

// commitsAhead counts the commits on branch beyond the task's base.
func commitsAhead(t *testing.T, r retainedTask, branch string) string {
	t.Helper()
	return integrationGit(t, r.source, "rev-list", "--count", r.base+".."+branch)
}

// Purpose: a task stopped mid-edit (a provider limit failed its run) can be
// continued. When reopen (or message_task, which routes to it for a failed
// task) finds the task's own retained worktree dirty and no run writing to
// it, the daemon commits every change, untracked files included, as one
// checkpoint commit on the task's agent branch with the repository's own
// identity (what the agent's git_commit uses), then continues as a
// retained_continuation: the new attempt is launched from that commit, and
// its receipt and prompt name the checkpoint. A retry with the same
// client_request_id makes no second checkpoint.
// Threat (forge, 2026-10-29): two swarm-role tasks failed with "You've hit
// your session limit" mid send-back round; every way to continue them was
// refused with "retained task worktree unavailable or dirty; preserve and
// commit its work before reopening", which no API, MCP or Orchestrator tool
// can do; only root on the machine could.
// Owners: Server.ReopenProjectTask (checkpointRetainedTaskWorktree,
// validateRetainedTaskSourceOwner, projectTaskFollowupPrompt),
// worktree.Service.CheckpointTaskWorkspace and the reservation
// (ReserveTaskFollowupWithRepositories) over real Git, store and executor.
// Narrowest layer: the canonical reopen authority with a real repository;
// the commit, the receipt and the launched attempt only meet there.
func TestReopenCheckpointsInterruptedTaskWorktree(t *testing.T) {
	f, r := newCheckpointFixture(t)
	defer f.close()

	task, err := checkpointReopen(t, f, r, "reopen-checkpoint-1", r.initialTaskRevision)
	if err != nil {
		t.Fatalf("reopen of a failed task with interrupted edits: %v", err)
	}
	if got := commitsAhead(t, r, r.branch); got != "1" {
		t.Fatalf("task branch gained %s commits, want exactly one checkpoint", got)
	}
	head := integrationGit(t, r.source, "rev-parse", r.branch)
	if files := integrationGit(t, r.source, "show", "--name-status", "--format=", head); !strings.Contains(files, "M\ttracked.txt") || !strings.Contains(files, "A\tnew_test.go") || !strings.Contains(files, "A\tpkg/added.go") {
		t.Fatalf("checkpoint commit files:\n%s", files)
	}
	if author := integrationGit(t, r.source, "log", "-1", "--format=%an <%ae>|%cn <%ce>", head); author != retainedTaskAuthor+"|"+retainedTaskAuthor {
		t.Fatalf("checkpoint identity %q, want the repository's own %q", author, retainedTaskAuthor)
	}
	if body := integrationGit(t, r.source, "log", "-1", "--format=%B", head); !strings.Contains(body, worktreeruntime.TaskCheckpointTrailer+": "+r.taskID) {
		t.Fatalf("checkpoint message lacks its trailer: %q", body)
	}
	if status := integrationGit(t, r.child, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("retained worktree still dirty after the checkpoint: %q", status)
	}
	attempt := task.ActiveAttempt()
	if attempt == nil || attempt.LaunchState != "launched" || attempt.ClientRequestID != "reopen-checkpoint-1" || len(task.Attempts) != r.initialAttemptsCount+1 {
		t.Fatalf("new attempt not launched: %+v (attempts %d -> %d)", attempt, r.initialAttemptsCount, len(task.Attempts))
	}
	if rec := attempt.Recovery; rec == nil || rec.Kind != "retained_continuation" || rec.HeadCommit != head || rec.CheckpointCommit != head || rec.Branch != r.branch {
		t.Fatalf("attempt receipt does not name the checkpoint: %+v", rec)
	}
	if _, ok, err := f.sessions.GetSessionRunIntent(task.SessionID, task.ExecutionRunID()); err != nil || !ok {
		t.Fatalf("follow-up run not recorded: %v %v", ok, err)
	}
	if prompt := projectTaskFollowupPrompt(attempt); !strings.HasPrefix(prompt, "finish the reviewer's send-back round") || !strings.Contains(prompt, "checkpoint commit "+head) {
		t.Fatalf("follow-up prompt does not name the checkpoint: %q", prompt)
	}

	// The same request again replays the reservation: no second checkpoint,
	// no second attempt.
	again, err := checkpointReopen(t, f, r, "reopen-checkpoint-1", r.initialTaskRevision)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := commitsAhead(t, r, r.branch); got != "1" {
		t.Fatalf("retry made another commit: %s ahead", got)
	}
	if len(again.Attempts) != len(task.Attempts) || again.ActiveAttemptID != task.ActiveAttemptID || again.ActiveAttempt().Recovery.CheckpointCommit != head {
		t.Fatalf("retry changed the follow-up: attempts %d -> %d, active %s -> %s", len(task.Attempts), len(again.Attempts), task.ActiveAttemptID, again.ActiveAttemptID)
	}
}

// Purpose: a reopen whose first try made the checkpoint commit but stopped
// before it reserved the attempt (a crash, a later refusal) is retried
// without a second commit, and the retry's receipt still names the
// checkpoint, found by its trailer at the clean worktree's HEAD.
// Threat: a retried reopen double-committing, or losing the record that a
// checkpoint holds the interrupted edits.
// Owners: worktree.Service.CheckpointTaskWorkspace (trailer detection) and
// Server.checkpointRetainedTaskWorktree's clean path.
func TestReopenRetryAfterCheckpointMakesNoSecondCommit(t *testing.T) {
	f, r := newCheckpointFixture(t)
	defer f.close()
	first, err := f.server.worktrees.(*worktreeruntime.Service).CheckpointTaskWorkspace(r.child, r.branch, r.taskID, "Checkpoint interrupted work of task "+r.taskID)
	if err != nil || !first.Created || first.Commit == "" {
		t.Fatalf("first try's checkpoint: %+v %v", first, err)
	}
	task, err := checkpointReopen(t, f, r, "reopen-after-crash", r.initialTaskRevision)
	if err != nil {
		t.Fatalf("retried reopen: %v", err)
	}
	if got := commitsAhead(t, r, r.branch); got != "1" {
		t.Fatalf("retry made another commit: %s ahead", got)
	}
	if rec := task.ActiveAttempt().Recovery; rec == nil || rec.CheckpointCommit != first.Commit || rec.HeadCommit != first.Commit {
		t.Fatalf("retry receipt does not name the first checkpoint %s: %+v", first.Commit, rec)
	}
}

// setRetainedTaskStatus sets the task's recorded status, as the scheduler
// leaves it while its run is live.
func setRetainedTaskStatus(t *testing.T, f *confirmationFixture, r retainedTask, status string) {
	t.Helper()
	if _, err := f.sessions.Store().UpdateProjectTask(r.p.AccountScopeID, r.projectID, r.taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// recordWaitingTasksRun records the task session's run as active and
// waiting on the tasks it delegated (waiting_tasks), at the durable run-state
// record every active-run reader uses (GetV3SessionRunState,
// GetV3SessionActiveRunIntent). The canonical wait_tasks mutation admits
// only project conversations, which carry no worktree, so this fixture
// writes the record the forge's interrupted swarm task had directly.
func recordWaitingTasksRun(t *testing.T, f *confirmationFixture, r retainedTask) {
	t.Helper()
	now := time.Now().UnixMilli()
	state := pebblestore.V3SessionRunState{SessionID: r.sessionID, UserID: r.p.UserID, AccountScopeID: r.p.AccountScopeID, RunID: "live-run", Active: true, Status: pebblestore.V3RunIntentWaitingTasks, CreatedAt: now, StartedAt: now, UpdatedAt: now}
	if err := f.sessions.Store().Underlying().PutJSON(pebblestore.KeyV3SessionRunIntentActive(r.sessionID), state); err != nil {
		t.Fatal(err)
	}
	if run, active, err := f.sessions.Store().GetV3SessionActiveRunIntent(r.sessionID); err != nil || !active || run.Status != pebblestore.V3RunIntentWaitingTasks {
		t.Fatalf("waiting_tasks run not active: %+v %v %v", run, active, err)
	}
}

// Purpose: the checkpoint never writes where a refusal protects work or
// where the follow-up is refused anyway: a retained worktree that is
// missing, a dirty worktree on another branch, a task session with an active
// run in any state (pending, running, or waiting_tasks on its delegated
// tasks, with the task left in_progress or already marked failed), an
// in_progress task whose run has not finished (the reservation's status
// rule), and a reopen at a stale revision are all refused with no commit on any
// branch, the edits still uncommitted in place, and the task unchanged. The
// repair path keeps its own clean-source rule.
// Threat: the daemon committing into a worktree a live run can still write
// to or resume into, onto a branch that is not the task's, or for a request
// the reservation then refuses (review finding: an in_progress task whose run
// was waiting_tasks passed the checkpoint's pending/running check, was
// committed, and only then refused by ReserveTaskFollowupWithRepositories).
// Owners: Server.ReopenProjectTask and checkpointRetainedTaskWorktree
// (before any Git write: CheckTaskFollowupAdmission, the any-active-run
// check, validateRetainedTaskSourceOwner), over real Git and the task store.
func TestReopenCheckpointKeepsUnsafeRefusals(t *testing.T) {
	type refusal struct {
		name    string
		prepare func(t *testing.T, f *confirmationFixture, r retainedTask) (branch string)
		want    string
		repair  bool
		stale   bool
	}
	for _, tc := range []refusal{
		{name: "missing worktree", want: "retained task worktree unavailable or dirty", prepare: func(t *testing.T, _ *confirmationFixture, r retainedTask) string {
			if err := os.RemoveAll(r.child); err != nil {
				t.Fatal(err)
			}
			return r.branch
		}},
		{name: "other branch", want: "not the task branch", prepare: func(t *testing.T, _ *confirmationFixture, r retainedTask) string {
			integrationGit(t, r.child, "checkout", "-q", "-b", "agent/elsewhere")
			return "agent/elsewhere"
		}},
		{name: "waiting on delegated tasks", want: "task is running or requires structured review", prepare: func(t *testing.T, f *confirmationFixture, r retainedTask) string {
			setRetainedTaskStatus(t, f, r, "in_progress")
			recordWaitingTasksRun(t, f, r)
			return r.branch
		}},
		{name: "waiting on delegated tasks after a failed mark", want: "still running (waiting_tasks)", prepare: func(t *testing.T, f *confirmationFixture, r retainedTask) string {
			recordWaitingTasksRun(t, f, r)
			return r.branch
		}},
		{name: "in progress without a finished run", want: "task is running or requires structured review", prepare: func(t *testing.T, f *confirmationFixture, r retainedTask) string {
			setRetainedTaskStatus(t, f, r, "in_progress")
			return r.branch
		}},
		{name: "running session", want: "task session still has an active run", prepare: func(t *testing.T, f *confirmationFixture, r retainedTask) string {
			for _, in := range []sessionruntime.SessionMutationInput{
				{ClientRequestID: "live-pending", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "live-run", Status: pebblestore.V3RunIntentPendingExecutor}},
				{ClientRequestID: "live-running", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "live-run", Status: pebblestore.V3RunIntentRunning}},
			} {
				in.Kind, in.SessionID, in.UserID, in.AccountScopeID = sessionruntime.SessionMutationRecordRunIntent, r.sessionID, r.p.UserID, r.p.AccountScopeID
				in.IdempotencyKey, in.PayloadHash = in.ClientRequestID, in.ClientRequestID
				if _, err := f.server.applySessionV3PrimaryMutation(in); err != nil {
					t.Fatal(err)
				}
			}
			return r.branch
		}},
		{name: "stale revision", want: "stale or missing task revision", stale: true, prepare: func(_ *testing.T, _ *confirmationFixture, r retainedTask) string {
			return r.branch
		}},
		{name: "repair path", want: "repair requires originating failed integration receipt", repair: true, prepare: func(_ *testing.T, _ *confirmationFixture, r retainedTask) string {
			return r.branch
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := newCheckpointFixture(t)
			defer f.close()
			branch := tc.prepare(t, f, r)
			baseline := r.task(t, f.server)
			revision := baseline.Revision
			if tc.stale {
				revision += 5
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, err := f.server.ReopenProjectTask(ctx, r.p, r.projectID, r.taskID, tool.ProjectTaskFollowupInput{Feedback: "continue", ClientRequestID: "refused-" + strings.ReplaceAll(tc.name, " ", "-"), Revision: revision, Repair: tc.repair})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reopen: %v, want refusal %q", err, tc.want)
			}
			for _, b := range []string{r.branch, branch} {
				if got := commitsAhead(t, r, b); got != "0" {
					t.Fatalf("refused reopen committed on %s: %s ahead", b, got)
				}
			}
			if tc.name != "missing worktree" {
				if status := integrationGit(t, r.child, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(status, "tracked.txt") || !strings.Contains(status, "new_test.go") {
					t.Fatalf("refused reopen changed the retained edits: %q", status)
				}
			}
			after := r.task(t, f.server)
			if after.Revision != baseline.Revision || len(after.Attempts) != len(baseline.Attempts) || after.Status != baseline.Status || after.ActiveAttemptID != baseline.ActiveAttemptID {
				t.Fatalf("refused reopen changed the task: revision %d -> %d, attempts %d -> %d, status %s -> %s", baseline.Revision, after.Revision, len(baseline.Attempts), len(after.Attempts), baseline.Status, after.Status)
			}
		})
	}
}
