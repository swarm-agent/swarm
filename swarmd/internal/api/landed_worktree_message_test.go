package api

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Requirement: a message to a finished project task's session (a person
// continuing a finished Coder session, POST /v3/sessions/{id}/messages) never
// starts a run in a checkout Swarm removed after its work landed. Before the
// message is appended and its run admitted, acceptSessionsV3Message ->
// prepareWorkerJobFollowup -> WorkerExecutionService.PrepareSessionMessage
// claims the checkout and recreates it from its kept branch; a checkout that
// cannot be recreated (its branch moved since the removal) refuses the
// message with a conflict and nothing is appended.
// Threat (review finding): prepareWorkerJobFollowup returned at once for any
// session not named worker-execution-*, so the run was admitted against a
// WorktreeRootPath that no longer existed.
// Owners: acceptSessionsV3Message and PrepareSessionMessage, over the real
// API server, session store, run service, worker execution service, worktree
// service and Git.
func TestSessionMessageRecreatesARemovedTaskCheckoutBeforeTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	raw := sessions.Store().Underlying()
	runs, ok := server.runner.(*runruntime.Service)
	if !ok {
		t.Fatalf("fixture: runner is %T", server.runner)
	}
	workspaces := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(raw))
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "dev")
	git(repo, "commit", "-q", "--allow-empty", "-m", "base")
	added, err := workspaces.AddForPrincipal(p, repo, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	source := added.WorkspacePath
	trees := worktree.NewService(pebblestore.NewWorktreeStore(raw), workspaces, nil)
	t.Cleanup(trees.Close)
	execution, err := runruntime.NewWorkerExecutionService(runs, sessions.Store(), trees,
		func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			return sessionruntime.SessionMutationResult{}, errors.New("not used")
		},
		func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	runs.SetWorkerExecutionService(execution)
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project-m", Name: "project-m"}); err != nil {
		t.Fatal(err)
	}

	// task creates a finished project task whose session's checkout holds one
	// landed commit, and removes the checkout the way Swarm does.
	task := func(id string) (sessionID, path, branch, head string) {
		sessionID, branch = "task-session-"+id, "agent/task-"+id
		alloc, err := trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, source, sessionID, "", branch)
		if err != nil {
			t.Fatal(err)
		}
		path = alloc.WorkspacePath
		if err := os.WriteFile(filepath.Join(path, id+".txt"), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
		git(path, "add", id+".txt")
		git(path, "commit", "-q", "-m", "work "+id)
		head = git(path, "rev-parse", "HEAD")
		git(repo, "merge", "-q", "--no-ff", "-m", "merge "+id, branch)
		if err := sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: source, Title: "Coder task",
			Preference:      pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
			Metadata:        map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": path, "swarm_v3_worktree_owner_session_id": sessionID, "project_id": "project-m", "task_id": id},
			WorktreeEnabled: true, WorktreeRootPath: path, WorktreeBranch: branch, WorktreeBaseBranch: "dev", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		// The task's own run settled, as a finished task's has.
		settled := pebblestore.V3SessionRunState{SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, RunID: "run-" + id, Active: false, Status: "completed", CreatedAt: 1, StartedAt: 1, UpdatedAt: 2}
		if err := raw.PutJSON(pebblestore.KeyV3SessionRunIntentActive(sessionID), settled); err != nil {
			t.Fatal(err)
		}
		if err := sessions.Store().PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: id, Title: "Task " + id, ProjectID: "project-m", AccountID: p.AccountScopeID,
			SessionID: sessionID, Agent: "coder", Status: "needs_review", WorkspacePath: path, WorktreeBranch: branch, BaseBranch: "dev"}); err != nil {
			t.Fatal(err)
		}
		if outcome, err := execution.RemoveLandedWorktree(sessionID); err != nil || !outcome.Removed {
			t.Fatalf("removal of %s = %+v, %v", id, outcome, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fixture: checkout of %s still present: %v", id, err)
		}
		return sessionID, path, branch, head
	}

	sessionID, path, branch, head := task("taskmsg1")
	if _, _, err := server.acceptSessionsV3Message(p, sessionID, sessionsV3MessageRequest{ClientRequestID: "continue-1", Role: "user", Content: "Also add a test."}); err != nil {
		t.Fatalf("message to the removed task session: %v", err)
	}
	state, err := trees.InspectTaskWorkspace(path)
	if err != nil || state.BranchName != branch || state.HeadCommit != head || !state.Clean {
		t.Fatalf("checkout before the run = %+v, %v; want recreated clean on %s at %s", state, err, branch, head)
	}
	if _, appended, err := sessions.Store().GetV3SessionOperationIdempotencyRecord(p.AccountScopeID, sessionID, sessionruntime.SessionMutationAppendMessage, "continue-1"); err != nil || !appended {
		t.Fatalf("message not appended: %v %v", appended, err)
	}
	record, _, err := sessions.Store().GetProjectTask(p.AccountScopeID, "project-m", "taskmsg1")
	if err != nil || record.WorktreeRemoval == nil || record.WorktreeRemoval.State != pebblestore.WorktreeRemovalRestored {
		t.Fatalf("task record = %+v, %v; want restored", record, err)
	}
	// The recreated checkout is claimed: a removal now keeps it.
	if outcome, err := execution.RemoveLandedWorktree(sessionID); err != nil || outcome.Removed {
		t.Fatalf("removal right after the message = %+v, %v; want kept", outcome, err)
	}

	// A branch moved since the removal: the message is refused before anything
	// is appended, never run in a missing checkout.
	moved, movedPath, movedBranch, _ := task("taskmsg2")
	git(repo, "branch", "-f", movedBranch, "dev")
	_, _, err = server.acceptSessionsV3Message(p, moved, sessionsV3MessageRequest{ClientRequestID: "continue-2", Role: "user", Content: "Also add a test."})
	if !errors.Is(err, pebblestore.ErrWorkerConflict) {
		t.Fatalf("message with an unrecoverable checkout = %v, want ErrWorkerConflict", err)
	}
	if _, appended, err := sessions.Store().GetV3SessionOperationIdempotencyRecord(p.AccountScopeID, moved, sessionruntime.SessionMutationAppendMessage, "continue-2"); err != nil || appended {
		t.Fatalf("refused message was appended: %v %v", appended, err)
	}
	if _, err := os.Stat(movedPath); !os.IsNotExist(err) {
		t.Fatalf("refused message left a checkout: %v", err)
	}

	// A message the task rules refuse (a completed or integrated task, which
	// must be reopened) recreates nothing and records no restored state: the
	// admission rule (pebblestore.TaskContinuationStateRefusal) is applied
	// before the checkout is claimed.
	// Threat (review finding): the checkout was recreated and recorded
	// restored, then the message was refused.
	for _, tc := range []struct {
		id     string
		modify func(*pebblestore.ProjectTaskRecord)
	}{
		{"taskmsg3", func(t *pebblestore.ProjectTaskRecord) { t.Status = "completed" }},
		{"taskmsg4", func(t *pebblestore.ProjectTaskRecord) { t.IsIntegrated = true }},
	} {
		refusedSession, refusedPath, _, _ := task(tc.id)
		if _, err := sessions.Store().UpdateProjectTask(p.AccountScopeID, "project-m", tc.id, func(t *pebblestore.ProjectTaskRecord) error {
			tc.modify(t)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := server.acceptSessionsV3Message(p, refusedSession, sessionsV3MessageRequest{ClientRequestID: "continue-" + tc.id, Role: "user", Content: "Also add a test."}); err == nil {
			t.Fatalf("%s: message to a task that must be reopened was accepted", tc.id)
		}
		if _, err := os.Stat(refusedPath); !os.IsNotExist(err) {
			t.Fatalf("%s: refused message recreated the checkout: %v", tc.id, err)
		}
		record, _, err := sessions.Store().GetProjectTask(p.AccountScopeID, "project-m", tc.id)
		if err != nil || record.WorktreeRemoval == nil || record.WorktreeRemoval.State != pebblestore.WorktreeRemovalRemoved {
			t.Fatalf("%s: task record = %+v, %v; want still removed", tc.id, record.WorktreeRemoval, err)
		}
		if _, appended, err := sessions.Store().GetV3SessionOperationIdempotencyRecord(p.AccountScopeID, refusedSession, sessionruntime.SessionMutationAppendMessage, "continue-"+tc.id); err != nil || appended {
			t.Fatalf("%s: refused message appended: %v %v", tc.id, appended, err)
		}
	}
}
