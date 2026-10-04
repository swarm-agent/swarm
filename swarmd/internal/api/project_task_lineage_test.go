package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: join canonical project-chat creation, task admission/deployment,
// reopen and report persistence. Hand-built matching parent/primary fixtures
// hid the stale legacy primary pointer used by deployment. Real Git allocation
// and V3 mutations prove the boundary without running providers or claiming delivery.
func TestProjectTaskReportDeploymentAndReopenLineage(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	catalog := pebblestore.NewWorkspaceStore(f.db)
	entry, err := catalog.AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(catalog)
	f.server.workspace = ws
	f.server.worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), ws, nil)
	source := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	seedTaskSessionBinding(t, f, source)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	db := f.server.sessions.Store()
	project := &pebblestore.ProjectRecord{ID: "report-project", Name: "Reports", PrimarySessionID: "missing-legacy-primary", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	w := projectConversationRequest(t, f.server, p, http.MethodPost, ProjectsPath+"/"+project.ID+"/sessions", map[string]any{"client_request_id": "origin"})
	if w.Code != http.StatusOK {
		t.Fatalf("conversation: %d %s", w.Code, w.Body)
	}
	parents, err := db.ListProjectConversations(p.AccountScopeID, p.UserID, project.ID, 10)
	if err != nil || len(parents) != 1 {
		t.Fatalf("conversations: %+v %v", parents, err)
	}
	parent := parents[0]
	setRun := func(session, run, status string) {
		t.Helper()
		intent, _, err := db.GetV3SessionRunIntent(session, run)
		if err != nil {
			t.Fatal(err)
		}
		intent.RunID, intent.Status = run, status
		_, err = db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: run + status, PayloadHash: status, RunIntent: &intent})
		if err != nil {
			t.Fatal(err)
		}
	}
	setRun(parent.ID, "origin-run", pebblestore.V3RunIntentPendingExecutor)
	setRun(parent.ID, "origin-run", pebblestore.V3RunIntentRunning)
	parentRun := "origin-run"
	ctx := tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: parent.ID, RunID: parentRun})
	// Suppress provider execution only; admission, allocation and persistence are real.
	f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey("report-child", "desktop-v3-run:task-report-task")] = true
	task, err := f.server.CreateProjectTask(ctx, p, project.ID, tool.ProjectTaskCreateInput{ID: "report-task", SessionID: "report-child", Title: "Report lineage", Prompt: "Report lineage", Agent: "coder", FeatureSize: "small", WorkspacePath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if task.OriginSessionID != parent.ID {
		t.Fatalf("admission lost origin: %+v", task)
	}
	project.PrimarySessionID = "another-missing-primary"
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		task, _, err = db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		child, found, err := db.GetSession(task.SessionID)
		if err != nil || !found || child.Metadata["parent_session_id"] != parent.ID {
			t.Fatalf("deployment lineage: %+v %v", child, err)
		}
		setRun(child.ID, task.ExecutionRunID(), pebblestore.V3RunIntentRunning)
		for _, kind := range []pebblestore.ProjectTaskUpdateKind{pebblestore.ProjectTaskUpdateProgress, pebblestore.ProjectTaskUpdateWakeRequest} {
			in := pebblestore.V3SessionMutationInput{SessionID: child.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationReportTask, ClientRequestID: string(kind), TaskReport: &pebblestore.V3ProjectTaskReportMutation{RunID: task.ExecutionRunID(), ProjectID: project.ID, TaskID: task.ID, Kind: kind, Summary: "Update"}}
			result, err := db.ApplyV3SessionMutation(in)
			if err != nil {
				t.Fatal(err)
			}
			var update pebblestore.ProjectTaskUpdate
			if result.RealtimeOutbox == nil || json.Unmarshal(result.RealtimeOutbox.Event.Payload, &update) != nil || update.ParentSessionID != parent.ID || update.EventID == "" {
				t.Fatalf("report destination: %+v", update)
			}
			retry, err := db.ApplyV3SessionMutation(in)
			if err != nil || !retry.Replayed || retry.PrimarySeq != result.PrimarySeq {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			if attempt == 1 && kind == pebblestore.ProjectTaskUpdateProgress {
				_, err := db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: parent.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "wait", PayloadHash: "wait", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: parentRun, ProjectID: project.ID, TaskIDs: []string{task.ID}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.ReconcileProjectTaskWaits(p.AccountScopeID, project.ID, nil); err != nil {
					t.Fatal(err)
				}
				state, _, _ := db.GetV3SessionRunState(parent.ID)
				if state.Status != pebblestore.V3RunIntentWaitingTasks {
					t.Fatal("progress woke the waiting conversation")
				}
			}
			in.TaskReport.Summary = "Changed"
			if _, err := db.ApplyV3SessionMutation(in); !errors.Is(err, pebblestore.ErrV3IdempotencyConflict) {
				t.Fatalf("changed retry: %v", err)
			}
		}
		state, _, err := db.GetV3SessionRunState(parent.ID)
		wantStatus := pebblestore.V3RunIntentRunning
		if attempt == 1 {
			wantStatus = pebblestore.V3RunIntentWaitingTasks
		}
		if err != nil || state.RunID != parentRun || state.Status != wantStatus {
			t.Fatalf("recording falsely woke parent: %+v %v", state, err)
		}
		if attempt == 1 {
			if err := db.ReconcileProjectTaskWaits(p.AccountScopeID, project.ID, nil); err != nil {
				t.Fatal(err)
			}
			resume, found, err := db.GetV3SessionRunIntent(parent.ID, pebblestore.ProjectTaskWaitResumeID(parentRun))
			if err != nil || !found || resume.Status != pebblestore.V3RunIntentPendingExecutor {
				t.Fatalf("explicit wake did not enter authorized flow: %+v %v", resume, err)
			}
		}
		setRun(child.ID, task.ExecutionRunID(), pebblestore.V3RunIntentCompleted)
		if attempt == 0 {
			setRun(parent.ID, parentRun, pebblestore.V3RunIntentCompleted)
			parentRun = "followup-origin-run"
			setRun(parent.ID, parentRun, pebblestore.V3RunIntentPendingExecutor)
			setRun(parent.ID, parentRun, pebblestore.V3RunIntentRunning)
			ctx = tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: parent.ID, RunID: parentRun})
			// The first call reserves/allocates but cannot enqueue without an executor.
			f.server.v3SessionExecutor = nil
			current, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			req := tool.ProjectTaskFollowupInput{Feedback: "Follow up", ClientRequestID: "followup", Revision: current.Revision}
			if _, err := f.server.ReopenProjectTask(ctx, p, project.ID, task.ID, req); err == nil {
				t.Fatal("absent executor reported launch")
			}
			reserved, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			f.server.v3SessionExecutor = newSessionV3Executor(f.server)
			f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(reserved.SessionID, reserved.ExecutionRunID())] = true
			if _, err := f.server.ReopenProjectTask(ctx, p, project.ID, task.ID, req); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Purpose: authenticated origin capture rejects foreign principals, projects,
// stale runs before task reservation. Canonical chat
// creation plus shared lifecycle admission is the narrowest producer boundary.
func TestProjectTaskOriginRejectsForgery(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	w := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "origin"})
	if w.Code != http.StatusOK {
		t.Fatalf("conversation: %d %s", w.Code, w.Body)
	}
	parents, err := db.ListProjectConversations(p.AccountScopeID, p.UserID, "project", 10)
	if err != nil || len(parents) != 1 {
		t.Fatalf("parents: %+v %v", parents, err)
	}
	parent := parents[0]
	for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning} {
		if _, err := db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: parent.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run", Status: status}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ account, user, project, session, run string }{
		{"foreign", p.UserID, "project", parent.ID, "run"},
		{p.AccountScopeID, "foreign", "project", parent.ID, "run"},
		{p.AccountScopeID, p.UserID, "other", parent.ID, "run"},
		{p.AccountScopeID, p.UserID, "project", "missing", "run"},
		{p.AccountScopeID, p.UserID, "project", parent.ID, "stale"},
	} {
		ctx := tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: tc.session, RunID: tc.run})
		principal := identity.Principal{Type: "user", AccountScopeID: tc.account, UserID: tc.user}
		if _, err := s.CreateProjectTask(ctx, principal, tc.project, tool.ProjectTaskCreateInput{ID: "forged", Title: "Forged"}); err == nil {
			t.Fatal("forged origin accepted")
		}
		if _, found, err := db.GetProjectTask(p.AccountScopeID, "project", "forged"); err != nil || found {
			t.Fatalf("rejection reserved task: %v", err)
		}
	}
}
