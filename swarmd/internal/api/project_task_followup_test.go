package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestProjectTaskFollowupHistoryAccountBoundary(t *testing.T) {
	// Purpose: handleProjects/history must return bounded chronological requests
	// only to the owning account. HTTP + real store is the narrow registered read
	// boundary; rejected foreign and malformed reads must not mutate task state.
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	db := pebblestore.NewSessionStore(store)
	p := testPrincipal()
	project := &pebblestore.ProjectRecord{Name: "History"}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task-history", ProjectID: project.ID, Title: "History", Agent: "swarm", Status: "needs_review", SessionID: "original", Revision: 1}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveTaskFollowup(p.AccountScopeID, project.ID, task.ID, p.UserID, "key", "full request", 1, 200); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	call := func(principal identity.Principal, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ProjectsPath+"/"+project.ID+"/tasks/"+task.ID+"/history"+query, nil)
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Scopes: []string{"projects:read", "sessions:read"}})
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		server.handleProjects(w, r)
		return w
	}
	response := call(p, "?cursor=1&limit=1")
	if response.Code != 200 {
		t.Fatalf("history: %d %s", response.Code, response.Body)
	}
	var page struct {
		Attempts []pebblestore.ProjectTaskAttempt `json:"attempts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Attempts) != 1 || page.Attempts[0].Request != "full request" || page.Attempts[0].SessionID == "original" {
		t.Fatal("lost exact history")
	}
	foreign := p
	foreign.AccountScopeID = "foreign"
	if response := call(foreign, ""); response.Code == 200 || strings.Contains(response.Body.String(), "full request") {
		t.Fatal("foreign history leaked")
	}
	if response := call(p, "?limit=51"); response.Code != 400 {
		t.Fatal("unbounded history accepted")
	}
	after, _, _ := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history read mutated task")
	}
}

func TestProjectTaskRepairRejectsUnassociatedSource(t *testing.T) {
	// Purpose: validateProjectTaskRecovery must reject account/source/association
	// forgery before any Git/allocation effect. Pure admission is the narrowest
	// negative layer; real committed-source success is separately allocator tested.
	server := &Server{}
	p := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "owned", WorkspacePath: "/owned", WorktreeBranch: "agent/owned", BaseBranch: "dev", BaseCommit: "base", Agent: "swarm"}
	task.EnsureTaskAttempts()
	before := *task
	for _, source := range []*pebblestore.ProjectTaskRecoverySource{
		nil,
		{SessionID: "foreign", WorkspacePath: "/foreign", Branch: "agent/foreign", HeadCommit: "head", BaseCommit: "base", TargetBranch: "dev", TargetHead: "target"},
		{SessionID: "owned", WorkspacePath: "/owned", Branch: "agent/owned", HeadCommit: "base", BaseCommit: "base", TargetBranch: "dev", TargetHead: "target"},
	} {
		if err := server.validateProjectTaskRecovery(p, task, source); err == nil {
			t.Fatal("forged recovery accepted")
		}
	}
	foreign := p
	foreign.AccountScopeID = "other"
	if err := server.validateProjectTaskRecovery(foreign, task, &pebblestore.ProjectTaskRecoverySource{HeadCommit: "head", BaseCommit: "base"}); err == nil {
		t.Fatal("foreign recovery accepted")
	}
	if !reflect.DeepEqual(before, *task) {
		t.Fatal("rejection mutated lineage")
	}
}

func TestProjectTaskRepairCommittedSourceProvenance(t *testing.T) {
	// Purpose: backend repair admission preserves exact Git head/captured target,
	// requires real owned session/catalog evidence, and rejects forged source or
	// project before allocation. Real Git/session mutations are the narrow joined layer.
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
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	catalog := pebblestore.NewWorkspaceStore(f.db)
	entry, err := catalog.AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(catalog)
	f.server.workspace = ws
	worktrees := worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), ws, nil)
	f.server.worktrees = worktrees
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	alloc, err := worktrees.AllocateProjectTaskFollowup(p, repo, "origin", "agent/task-repair-origin-proof", base, "dev")
	if err != nil {
		t.Fatal(err)
	}
	git(alloc.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "work")
	head := git(alloc.WorkspacePath, "rev-parse", "HEAD")
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	if err := f.server.sessions.Store().PutProject(f.accountID, &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: f.accountID, SessionID: "origin", Agent: "swarm", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: binding}
	task.EnsureTaskAttempts()
	snapshot := pebblestore.SessionSnapshot{ID: "origin", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": "task", "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "origin", "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: "origin", UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "create-origin", IdempotencyKey: "create-origin", PayloadHash: "create-origin", RequestHash: "create-origin", Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: "origin", Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	source := &pebblestore.ProjectTaskRecoverySource{SessionID: "origin", WorkspacePath: alloc.WorkspacePath, Branch: alloc.BranchName, HeadCommit: head, BaseCommit: base, TargetBranch: "dev", TargetHead: base}
	if err := f.server.validateProjectTaskRecovery(p, task, source); err != nil {
		t.Fatal(err)
	}
	before := git(repo, "worktree", "list", "--porcelain")
	for _, mutate := range []func(*pebblestore.ProjectTaskRecord, *pebblestore.ProjectTaskRecoverySource){
		func(t *pebblestore.ProjectTaskRecord, s *pebblestore.ProjectTaskRecoverySource) {
			t.ProjectID = "other"
		},
		func(t *pebblestore.ProjectTaskRecord, s *pebblestore.ProjectTaskRecoverySource) {
			t.SourceWorkspace.WorkspaceGeneration++
		},
		func(t *pebblestore.ProjectTaskRecord, s *pebblestore.ProjectTaskRecoverySource) { s.HeadCommit = base },
		func(t *pebblestore.ProjectTaskRecord, s *pebblestore.ProjectTaskRecoverySource) { s.TargetHead = head },
	} {
		copyTask, copySource := *task, *source
		mutate(&copyTask, &copySource)
		if err := f.server.validateProjectTaskRecovery(p, &copyTask, &copySource); err == nil {
			t.Fatal("forged repair accepted")
		}
	}
	if git(repo, "worktree", "list", "--porcelain") != before || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("rejection mutated Git")
	}
}

type failOnceFollowupAllocator struct {
	*worktreeruntime.Service
	failed bool
}

func (s *failOnceFollowupAllocator) AllocateProjectTaskFollowup(p identity.Principal, source, owner, branch, head, target string) (worktreeruntime.Allocation, error) {
	if !s.failed {
		s.failed = true
		return worktreeruntime.Allocation{}, errors.New("injected allocation failure")
	}
	return s.Service.AllocateProjectTaskFollowup(p, source, owner, branch, head, target)
}

func TestProjectTaskFollowupCreatesNewAutoSwarm(t *testing.T) {
	// Purpose: the registered reopen route creates a new task-linked auto Swarm
	// with real isolated Git allocation and canonical V3 seed/run mutations.
	// Threat: resuming old session, duplicate retries, changed payload or stale
	// old plan writes. Joined API/store/Git is the narrowest end-to-end launch layer.
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
	f.server.worktrees = &failOnceFollowupAllocator{Service: worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), ws, nil)}
	db := f.server.sessions.Store()
	project := &pebblestore.ProjectRecord{ID: "followup-project", Name: "Followup", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(f.accountID, project); err != nil {
		t.Fatal(err)
	}
	original := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Task", Agent: "coder", Status: "needs_review", SessionID: "original", Revision: 1, WorkspacePath: repo, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}}
	if err := db.PutProjectTask(f.accountID, original); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	body := map[string]any{"client_request_id": "followup", "revision": 1, "feedback": "Additional request"}
	path := "/" + project.ID + "/tasks/task/reopen"
	response := f.callAPI("POST", path, body, p)
	if response.Code != 503 {
		t.Fatalf("injected allocation failure: %d %s", response.Code, response.Body)
	}
	reserved, _, err := db.GetProjectTask(f.accountID, project.ID, "task")
	if err != nil || len(reserved.Attempts) != 2 || reserved.ActiveAttempt().LaunchState != "launch_failed" || reserved.ActiveAttempt().Request != "Additional request" {
		t.Fatal("launch failure lost durable reservation")
	}
	if _, found, _ := db.GetSession(reserved.SessionID); found {
		t.Fatal("failed allocation created a session")
	}
	// Model distinct crash windows with canonical mutation injection, not providers.
	for _, kind := range []string{sessionruntime.SessionMutationCreateSession, pebblestore.V3SessionMutationAppendMessage} {
		f.server.beforeProjectTaskFollowupMutation = func(input sessionruntime.SessionMutationInput) error {
			if input.Kind == kind {
				return errors.New("injected follow-up mutation failure")
			}
			return nil
		}
		response = f.callAPI("POST", path, body, p)
		if response.Code != 503 {
			t.Fatalf("mutation failure %s: %d %s", kind, response.Code, response.Body)
		}
		failed, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
		if failed.SessionID != reserved.SessionID || len(failed.Attempts) != 2 || failed.ActiveAttempt().LaunchState != "launch_failed" {
			t.Fatal("mutation failure lost reservation")
		}
		if intents, err := db.ListRunIntents(failed.SessionID, 10); err != nil || len(intents) != 0 {
			t.Fatal("failed seed created a run")
		}
	}
	f.server.beforeProjectTaskFollowupMutation = nil
	response = f.callAPI("POST", path, body, p)
	if response.Code != 503 {
		t.Fatalf("absent executor reported launch: %d %s", response.Code, response.Body)
	}
	pending, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if pending.ActiveAttempt().LaunchState != "launch_failed" {
		t.Fatal("enqueue failure falsely marked launched")
	}
	// Hermetic executor receipt fixture: an already accepted wake is deduplicated.
	// No executor goroutine or provider run is started by this deterministic test.
	f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(pending.SessionID, pending.ExecutionRunID())] = true
	response = f.callAPI("POST", path, body, p)
	if response.Code != 200 {
		t.Fatalf("launch: %d %s", response.Code, response.Body)
	}
	task, _, err := db.GetProjectTask(f.accountID, project.ID, "task")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID == "original" || len(task.Attempts) != 2 || task.ActiveAttempt().LaunchState != "launched" {
		t.Fatal("did not create new linked attempt")
	}
	session, found, err := db.GetSession(task.SessionID)
	if err != nil || !found || session.Mode != "auto" || !session.WorktreeEnabled || session.WorktreeRootPath == repo || session.Metadata["resolved_agent_name"] != "swarm" || session.Metadata["task_attempt_id"] != task.ActiveAttemptID {
		t.Fatalf("invalid coordinator: %+v %v", session, err)
	}
	if response := f.callAPI("POST", path, body, p); response.Code != 200 {
		t.Fatalf("retry: %d %s", response.Code, response.Body)
	}
	intents, err := db.ListRunIntents(task.SessionID, 10)
	if err != nil || len(intents) != 1 || intents[0].RunID != task.ExecutionRunID() {
		t.Fatal("retry duplicated run")
	}
	messages, err := db.ListMessages(task.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Additional request") {
		t.Fatal("seed request lost or duplicated")
	}
	body["feedback"] = "Changed request"
	if response := f.callAPI("POST", path, body, p); response.Code != 409 {
		t.Fatal("changed retry accepted")
	}
	after, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if after.SessionID != task.SessionID || len(after.Attempts) != 2 {
		t.Fatal("changed retry mutated lineage")
	}
	// Late events from an original coordinator never regress the new attempt.
	old := pebblestore.SessionSnapshot{ID: "original", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Metadata: map[string]any{"project_id": project.ID, "task_id": "task"}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: old.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "old-create", IdempotencyKey: "old-create", PayloadHash: "old-create", RequestHash: "old-create", Kind: sessionruntime.SessionMutationCreateSession, Session: &old}); err != nil {
		t.Fatal(err)
	}
	beforeEvent, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{SessionID: old.ID, RunID: "old-run", Principal: p}, pebblestore.V3RunIntentFailed, "stale failure"); err != nil {
		t.Fatal(err)
	}
	afterEvent, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if !reflect.DeepEqual(beforeEvent, afterEvent) {
		t.Fatal("stale original event changed active attempt")
	}
	if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{SessionID: task.SessionID, RunID: "wrong-current-run", Principal: p}, pebblestore.V3RunIntentFailed, "stale same-session failure"); err != nil {
		t.Fatal(err)
	}
	afterEvent, _, _ = db.GetProjectTask(f.accountID, project.ID, "task")
	if !reflect.DeepEqual(beforeEvent, afterEvent) {
		t.Fatal("stale run event changed active attempt")
	}
}

func TestProjectTaskFollowupSummaryRunProvenance(t *testing.T) {
	// Purpose: persisted/read task outcomes must not reuse a previous run's text.
	// Threat: stale metadata looks like the result of new work. Canonical V3
	// session/run mutation plus task read is the narrow provenance boundary.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	snapshot := pebblestore.SessionSnapshot{ID: "summary-session", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"lifecycle_signal": "needs_review", "lifecycle_summary": "Old result", "lifecycle_summary_run_id": "old-run"}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "summary-create", IdempotencyKey: "summary-create", PayloadHash: "summary-create", RequestHash: "summary-create", Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "summary-run", IdempotencyKey: "summary-run", PayloadHash: "summary-run", RequestHash: "summary-run", Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{SessionID: snapshot.ID, RunID: "new-run", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Status: pebblestore.V3RunIntentCompleted}}); err != nil {
		t.Fatal(err)
	}
	db := f.server.sessions.Store()
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "needs_review", SessionID: snapshot.ID}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	stored, _, err := db.GetProjectTask(p.AccountScopeID, "project", "task")
	if err != nil || stored.ActiveAttempt().Summary != "" {
		t.Fatal("stale summary attributed to new run")
	}
	snapshot.Metadata["lifecycle_summary"] = "New result; validation pending"
	snapshot.Metadata["lifecycle_summary_run_id"] = "new-run"
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "summary-update", IdempotencyKey: "summary-update", PayloadHash: "summary-update", RequestHash: "summary-update", Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &snapshot}); err != nil {
		t.Fatal(err)
	}
	stored, _, err = db.GetProjectTask(p.AccountScopeID, "project", "task")
	if err != nil || stored.ActiveAttempt().Summary != "New result; validation pending" || stored.ActiveAttempt().SummaryRunID != "new-run" {
		t.Fatal("matching ready summary missing provenance")
	}
}
