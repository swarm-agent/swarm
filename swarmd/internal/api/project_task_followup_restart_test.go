package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: registered reopen/history and canonical V3 completion must retain the
// original request, summaries, sessions and integration evidence through two
// follow-ups and a real store reopen. Threat: isolated happy paths miss loss at
// reservation/hydration/restart boundaries. API + temporary Pebble + real Git is
// the narrow joined layer. Executor receipts are fixtures; no provider executes.
func TestProjectTaskFollowupJoinedRestart(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() { f.db.Close() }()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	wire := func() {
		ss := pebblestore.NewSessionStore(f.db)
		el, err := pebblestore.NewEventLog(f.db)
		if err != nil {
			t.Fatal(err)
		}
		f.server.sessions = sessionruntime.NewService(ss, el)
		f.server.planLifecycle = sessionruntime.NewPlanLifecycleService(f.server.sessions)
		f.server.planLifecycle.SetApplySessionMutation(f.server.applySessionV3PrimaryMutation)
		f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
		f.server.worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
		f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
		f.server.agents = agentruntime.NewService(pebblestore.NewAgentStore(f.db), el)
		f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
		f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	}
	wire()
	db := f.server.sessions.Store()
	project := &pebblestore.ProjectRecord{ID: "restart-project", Name: "Restart", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	mutate := func(sessionID, key string, kind string, snapshot *pebblestore.SessionSnapshot, run *pebblestore.V3SessionRunIntent) {
		t.Helper()
		_, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: kind, Session: snapshot, RunIntent: run})
		if err != nil {
			t.Fatal(err)
		}
	}
	original := pebblestore.SessionSnapshot{ID: "original", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"project_id": project.ID, "task_id": "task"}}
	mutate(original.ID, "original-create", sessionruntime.SessionMutationCreateSession, &original, nil)
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Task", Description: "Original requirements", Agent: "swarm", Status: "in_progress", SessionID: original.ID, Revision: 1, WorkspacePath: repo, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}, Integration: &pebblestore.ProjectTaskIntegration{State: "failed", SessionID: original.ID, SourceHead: base, TargetBranch: "dev", Error: "Fixture receipt; no promotion claimed"}, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "original-output", Title: "Original output", Kind: "report", Status: "ready"}}}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	complete := func(sessionID, runID, summary string) {
		t.Helper()
		run := &pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Status: pebblestore.V3RunIntentCompleted}
		if _, found, err := f.server.sessions.Store().GetV3SessionRunState(sessionID); err != nil {
			t.Fatal(err)
		} else if !found {
			pending := *run
			pending.Status = pebblestore.V3RunIntentPendingExecutor
			mutate(sessionID, runID+":pending", sessionruntime.SessionMutationRecordRunIntent, nil, &pending)
		}
		mutate(sessionID, runID+":completed", sessionruntime.SessionMutationRecordRunIntent, nil, run)
		sess, ok, err := f.server.sessions.Store().GetSession(sessionID)
		if err != nil || !ok {
			t.Fatalf("session missing: %v", err)
		}
		sess.Metadata["lifecycle_signal"], sess.Metadata["lifecycle_summary"], sess.Metadata["lifecycle_summary_run_id"] = "needs_review", summary, runID
		mutate(sessionID, runID+":summary", sessionruntime.SessionMutationUpdateMetadata, &sess, nil)
		if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{SessionID: sessionID, RunID: runID, Principal: p}, pebblestore.V3RunIntentCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	complete(original.ID, "original-run", "Original outcome; integration pending")
	before, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || before.Status != "needs_review" || before.ActiveAttempt().Summary == "" {
		t.Fatalf("original not ready: %+v %v", before, err)
	}
	path := "/" + project.ID + "/tasks/task"
	reopen := func(key, request string) *pebblestore.ProjectTaskRecord {
		t.Helper()
		current, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"client_request_id": key, "revision": current.Revision, "feedback": request}
		// Persist the real reservation/session/seed/intent, but don't run an executor.
		// Absence is deliberately reported as failure until a fixture wake is present.
		f.server.v3SessionExecutor = nil
		response := f.callAPI(http.MethodPost, path+"/reopen", body, p)
		if response.Code != 503 {
			t.Fatalf("expected absent executor: %d %s", response.Code, response.Body)
		}
		reserved, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.server.v3SessionExecutor = newSessionV3Executor(f.server)
		f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(reserved.SessionID, reserved.ExecutionRunID())] = true
		response = f.callAPI(http.MethodPost, path+"/reopen", body, p)
		if response.Code != 200 {
			t.Fatalf("reopen: %d %s", response.Code, response.Body)
		}
		result, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || result.ActiveAttempt().LaunchState != "launched" {
			t.Fatalf("launch state: %+v %v", result, err)
		}
		if response := f.callAPI(http.MethodPost, path+"/reopen", body, p); response.Code != 200 {
			t.Fatalf("retry: %d %s", response.Code, response.Body)
		}
		return result
	}
	first := reopen("first", "  First follow-up\n")
	messages, err := db.ListMessages(first.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, before.ActiveAttempt().Summary) || !strings.Contains(messages[0].Content, "manage_projects get_task") || !strings.Contains(messages[0].Content, first.ActiveAttempt().Request) {
		t.Fatal("seed lost prior context/request")
	}
	complete(first.SessionID, first.ExecutionRunID(), "First follow-up outcome; validation pending")
	firstReady, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || firstReady.Status != "needs_review" || firstReady.ActiveAttempt().SummaryRunID != first.ExecutionRunID() {
		t.Fatal("follow-up summary missing")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = pebblestore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	wire()
	db = f.server.sessions.Store()
	restored, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !reflect.DeepEqual(firstReady, restored) {
		t.Fatal("restart changed task lineage")
	}
	second := reopen("second", "Second day follow-up")
	if len(second.Attempts) != 3 || second.SessionID == first.SessionID || second.SessionID == original.ID || second.Attempts[0].Integration == nil || second.Attempts[0].Integration.Error != before.Integration.Error || len(second.Attempts[0].Deliverables) != 1 || second.Attempts[1].Summary != firstReady.ActiveAttempt().Summary || second.ActiveAttempt().Summary != "" {
		t.Fatalf("lost original/first evidence: %+v", second)
	}
	history := f.callAPI(http.MethodGet, path+"/history?cursor=0&limit=2", nil, p)
	var page struct {
		Attempts []pebblestore.ProjectTaskAttempt `json:"attempts"`
		Next     int                              `json:"next_cursor"`
	}
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &page) != nil || len(page.Attempts) != 2 || page.Next != 2 || page.Attempts[1].Request != first.ActiveAttempt().Request {
		t.Fatalf("history: %d %s", history.Code, history.Body)
	}
	complete(second.SessionID, second.ExecutionRunID(), "Second follow-up outcome; not promoted")
	secondReady, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || secondReady.Status != "needs_review" || secondReady.ActiveAttempt().Summary != "Second follow-up outcome; not promoted" || secondReady.Description != "Original requirements" {
		t.Fatal("second completion lost summary or original requirements")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = pebblestore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	wire()
	db = f.server.sessions.Store()
	final, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !reflect.DeepEqual(secondReady, final) {
		t.Fatal("second restart changed retained outcomes")
	}
	history = f.callAPI(http.MethodGet, path+"/history?cursor=2&limit=2", nil, p)
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &page) != nil || len(page.Attempts) != 1 || page.Next != 0 || page.Attempts[0].Summary != final.ActiveAttempt().Summary || page.Attempts[0].Request != "Second day follow-up" {
		t.Fatalf("trailing history: %d %s", history.Code, history.Body)
	}
	for _, id := range []string{original.ID, first.SessionID, second.SessionID} {
		if _, ok, err := db.GetSession(id); err != nil || !ok {
			t.Fatalf("retained session %s missing", id)
		}
	}
	if git("rev-parse", "HEAD") != base || git("status", "--porcelain") != "" {
		t.Fatal("follow-ups modified captured target")
	}
}
