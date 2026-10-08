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

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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
	testProjectTaskRetainedSource(t, true)
}

// Purpose: ReopenProjectTask must preserve unintegrated Git content without a
// repair receipt; joined Git/Pebble tests prove exact source and retry durability.
func TestProjectTaskContinuationCommittedSourceProvenance(t *testing.T) {
	testProjectTaskRetainedSource(t, false)
}

// Purpose: actual Git ancestry, not stale delivery flags, selects current dev
// after integration so an ordinary follow-up cannot rewind newer target commits.
func TestProjectTaskContinuationDeliveredSourceUsesCurrentTarget(t *testing.T) {
	testProjectTaskRetainedSource(t, false, true)
}

func testProjectTaskRetainedSource(t *testing.T, repair bool, delivered ...bool) {
	// Purpose: backend repair admission preserves exact Git head/captured target,
	// requires real owned session/catalog evidence, and rejects forged source or
	// project before allocation. Real Git/session mutations are the narrow joined layer.
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
	if err := os.WriteFile(filepath.Join(alloc.WorkspacePath, "feature.txt"), []byte("retained feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(alloc.WorkspacePath, "add", "feature.txt")
	git(alloc.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "work")
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
	if !repair {
		source.Kind = "retained_continuation"
		git(repo, "merge", "--ff-only", head)
		git(repo, "reset", "--hard", base)
	}
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
	// Requirement: a previously failed reserved repair retries the same session
	// across real binding validation and Git allocation, retaining exact recovery.
	task.Title = "Repair task"
	task.Revision = 1
	task.Status = "needs_review"
	if repair {
		task.Integration = &pebblestore.ProjectTaskIntegration{State: "failed", SessionID: "origin", SourceHead: head, SourceBranch: alloc.BranchName, TargetBranch: "dev", PreviousTargetHead: base}
	}
	if err := f.server.sessions.Store().PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	// Dirty source rejection must leave durable task and Git untouched.
	dirty := filepath.Join(alloc.WorkspacePath, "dirty.txt")
	if err := os.WriteFile(dirty, []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeOrdinary, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	if _, err := f.server.ReopenProjectTask(context.Background(), p, "project", "task", tool.ProjectTaskFollowupInput{Feedback: "continue", ClientRequestID: "ordinary", Revision: 1}); err == nil {
		t.Fatalf("ordinary follow-up abandoned source: %v", err)
	}
	afterOrdinary, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	if !reflect.DeepEqual(beforeOrdinary, afterOrdinary) || git(repo, "rev-parse", "HEAD") != base || git(alloc.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("rejected continuation changed task or retained Git")
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	foreign := p
	foreign.UserID = "foreign-user"
	if _, err := f.server.ReopenProjectTask(context.Background(), foreign, "project", "task", tool.ProjectTaskFollowupInput{Feedback: "continue", ClientRequestID: "foreign", Revision: 1}); err == nil {
		t.Fatal("foreign retained source accepted")
	}
	afterForeign, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	if !reflect.DeepEqual(beforeOrdinary, afterForeign) || git(repo, "worktree", "list", "--porcelain") != before {
		t.Fatal("foreign rejection mutated task or allocated worktree")
	}
	if len(delivered) != 0 && delivered[0] {
		git(repo, "merge", "--ff-only", head)
		git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "newer target")
		target := git(repo, "rev-parse", "HEAD")
		seedTaskSessionBinding(t, f, binding)
		f.server.v3SessionExecutor = nil
		_, err := f.server.ReopenProjectTask(context.Background(), p, "project", "task", tool.ProjectTaskFollowupInput{Feedback: "continue delivered", ClientRequestID: "delivered", Revision: 1})
		if err == nil {
			t.Fatal("missing executor unexpectedly launched")
		}
		reserved, _, readErr := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
		if readErr != nil || reserved.ActiveAttempt().Recovery != nil || reserved.ActiveAttempt().AllocationHead != target {
			t.Fatalf("integrated source rewound reservation: %+v %v", reserved, readErr)
		}
		sess, found, readErr := f.server.sessions.Store().GetSession(reserved.SessionID)
		if readErr != nil || !found || git(sess.WorkspacePath, "rev-parse", "HEAD") != target || git(repo, "rev-parse", "HEAD") != target || git(alloc.WorkspacePath, "rev-parse", "HEAD") != head {
			t.Fatal("integrated follow-up changed source or lost newer target")
		}
		return
	}
	body := map[string]any{"client_request_id": "repair-binding", "revision": task.Revision, "feedback": "Repair retained source", "repair": repair}
	path := "/project/tasks/task/reopen"
	response := f.callAPI("POST", path, body, p)
	if response.Code != 503 {
		t.Fatalf("missing binding: %d %s", response.Code, response.Body)
	}
	reserved, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	if err != nil || reserved.ActiveAttempt().LaunchState != "launch_failed" || !reflect.DeepEqual(reserved.ActiveAttempt().Recovery, source) {
		t.Fatalf("lost repair reservation: %+v %v", reserved, err)
	}
	if _, found, _ := f.server.sessions.Store().GetSession(reserved.SessionID); found {
		t.Fatal("missing binding created session")
	}
	originHead := head
	if !repair {
		if reserved.ActiveAttempt().AllocationHead != head || reserved.BaseCommit != base || reserved.BaseBranch != "dev" {
			t.Fatal("reservation did not atomically pin continuation")
		}
		git(alloc.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "later source work")
		originHead = git(alloc.WorkspacePath, "rev-parse", "HEAD")
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		f.db, err = pebblestore.Open(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		el, err := pebblestore.NewEventLog(f.db)
		if err != nil {
			t.Fatal(err)
		}
		f.server.sessions = sessionruntime.NewService(pebblestore.NewSessionStore(f.db), el)
		f.server.planLifecycle = sessionruntime.NewPlanLifecycleService(f.server.sessions)
		f.server.planLifecycle.SetApplySessionMutation(f.server.applySessionV3PrimaryMutation)
		f.server.v3SessionExecutor = nil
		f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
		worktrees = worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
		f.server.worktrees = &failOnceFollowupAllocator{Service: worktrees}
		f.server.agents = agent.NewService(pebblestore.NewAgentStore(f.db), el)
		f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
		f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
	}
	seedTaskSessionBinding(t, f, binding)
	if !repair {
		response = f.callAPI("POST", path, body, p)
		if response.Code != 503 || !strings.Contains(response.Body.String(), "injected allocation failure") {
			t.Fatalf("allocation failure: %d %s", response.Code, response.Body)
		}
		response = f.callAPI("POST", path, body, p)
		if response.Code != 503 {
			t.Fatalf("absent executor: %d %s", response.Code, response.Body)
		}
	}
	f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(reserved.SessionID, reserved.ExecutionRunID())] = true
	for i := 0; i < 2; i++ {
		response = f.callAPI("POST", path, body, p)
		if response.Code != 200 {
			t.Fatalf("repair retry: %d %s", response.Code, response.Body)
		}
	}
	launched, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	sess, found, err := f.server.sessions.Store().GetSession(reserved.SessionID)
	if err != nil || !found || !sess.WorktreeEnabled || sess.WorkspacePath == repo || sess.WorkspacePath == alloc.WorkspacePath || sess.Metadata["swarm_v3_workspace_binding_id"] != "task-binding" || sess.Metadata["swarm_v3_runtime_workspace_path"] != sess.WorktreeRootPath || git(sess.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatalf("repair binding/isolation: %+v %v", sess, err)
	}
	if launched.SessionID != reserved.SessionID || len(launched.Attempts) != 2 || launched.ActiveAttempt().LaunchState != "launched" || launched.ActiveAttempt().Request != "Repair retained source" || !reflect.DeepEqual(launched.ActiveAttempt().Recovery, source) || launched.BaseBranch != "dev" || launched.BaseCommit != base || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("retry changed retained request/source/target")
	}
	if content, err := os.ReadFile(filepath.Join(sess.WorkspacePath, "feature.txt")); err != nil || string(content) != "retained feature\n" {
		t.Fatalf("retained feature missing: %q %v", content, err)
	}
	if git(alloc.WorkspacePath, "rev-parse", "HEAD") != originHead || git(repo, "status", "--porcelain") != "" {
		t.Fatal("continuation mutated original source or target")
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatal("continuation leaked feature into target")
	}
	intents, err := f.server.sessions.Store().ListRunIntents(sess.ID, 10)
	if err != nil || len(intents) != 1 || intents[0].RunID != reserved.ExecutionRunID() {
		t.Fatal("repair retry duplicated run")
	}
	messages, err := f.server.sessions.Store().ListMessages(sess.ID, 0, 10)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Repair retained source") {
		t.Fatal("repair retry lost or duplicated feedback")
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
	seedTaskSessionBinding(t, f, original.SourceWorkspace)
	body := map[string]any{"client_request_id": "followup", "revision": 1, "feedback": "Additional request"}
	path := "/" + project.ID + "/tasks/task/reopen"
	// Exact task revision failures must reject before reservation/allocation; the
	// previous review-guard fixture incorrectly used retired definition_revision.
	before, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	stale := f.callAPI("POST", path, map[string]any{"client_request_id": "stale", "revision": 99, "feedback": "Additional request"}, p)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "revision") {
		t.Fatalf("stale revision: %d %s", stale.Code, stale.Body)
	}
	after, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stale reopen changed attempts")
	}
	if _, err := db.UpdateProjectTask(f.accountID, project.ID, "task", func(task *pebblestore.ProjectTaskRecord) error { task.Status = "pending_approval"; return nil }); err != nil {
		t.Fatal(err)
	}
	approvalBefore, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	blocked := f.callAPI("POST", path, map[string]any{"client_request_id": "approval-bypass", "revision": approvalBefore.Revision, "feedback": "Skip approval"}, p)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("approval bypass: %d %s", blocked.Code, blocked.Body)
	}
	approvalAfter, _, _ := db.GetProjectTask(f.accountID, project.ID, "task")
	if !reflect.DeepEqual(approvalBefore, approvalAfter) {
		t.Fatal("rejected approval bypass mutated attempts")
	}
	if err := db.PutProjectTask(f.accountID, before); err != nil {
		t.Fatal(err)
	}
	body["revision"] = before.Revision
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
	// A pre-fix failed reservation may already own the isolated session. Remove
	// only the missing route fields through V3, then require same-session repair.
	legacy, found, err := db.GetSession(pending.SessionID)
	if err != nil || !found {
		t.Fatal("missing pending session")
	}
	forged := legacy
	forged.Metadata = cloneSessionsV3Metadata(legacy.Metadata)
	forged.Metadata["swarm_v3_workspace_binding_id"] = "foreign-binding"
	if err := f.server.reconcileProjectTaskSessionBinding(p, pending, &forged); err == nil {
		t.Fatal("conflicting retained binding accepted")
	}
	unchanged, _, err := db.GetSession(legacy.ID)
	if err != nil || !reflect.DeepEqual(unchanged, legacy) {
		t.Fatal("binding rejection mutated owned session")
	}
	delete(legacy.Metadata, "swarm_v3_workspace_binding_id")
	delete(legacy.Metadata, "local_workspace_binding_id")
	key := "legacy-missing-binding"
	if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: legacy.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &legacy}); err != nil {
		t.Fatal(err)
	}
	// Hermetic executor receipt fixture: an already accepted wake is deduplicated.
	// No executor goroutine or provider run is started by this deterministic test.
	f.server.v3SessionExecutor = &sessionV3Executor{
		server:       f.server,
		ctx:          context.Background(),
		inFlightRuns: map[string]bool{sessionV3ExecutorRunKey(pending.SessionID, pending.ExecutionRunID()): true},
	}
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
	if session.Metadata["swarm_v3_workspace_binding_id"] != "task-binding" || session.Metadata["swarm_v3_runtime_swarm_id"] != "task-host" || session.Metadata["swarm_v3_runtime_workspace_path"] != session.WorktreeRootPath || session.Metadata["swarm_v3_source_workspace_path"] != repo {
		t.Fatal("follow-up lost canonical binding or isolated runtime")
	}
	// Purpose: the actual reopen-created metadata must resolve provider-visible
	// history through the same session-bound overlay used by the V3 executor.
	// This closes the launch-to-inventory gap without starting a provider run.
	runtime := tool.NewRuntime(1)
	runtime.SetManageProjectStore(db)
	realRunner := runruntime.NewService(f.server.sessions, nil, nil, runtime, nil, nil, nil, nil)
	oldRunner := f.server.runner
	f.server.runner = realRunner
	executor := newSessionV3Executor(f.server)
	baseProfile := agent.SwarmAgentProfileForContext(pebblestore.AgentProfile{})
	definitions, toolErr := executor.resolveSessionV3ProviderTools(p.AccountScopeID, baseProfile)
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	_, definitions, toolErr = executor.resolveSessionV3TaskHistoryTools(tool.WorkspaceScope{SessionID: session.ID, Principal: p}, baseProfile, definitions)
	f.server.runner = oldRunner
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	historyExposed := false
	for _, definition := range definitions {
		if definition.Name == "manage_projects" {
			historyExposed = true
		}
	}
	if !historyExposed {
		t.Fatal("actual follow-up session lacks provider history tool")
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
	after, _, _ = db.GetProjectTask(f.accountID, project.ID, "task")
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

// Purpose: shared AI continuation admission rejects archived and foreign tasks
// before source inspection, reservation or V3 writes. Real temporary store is
// the narrow account/archive authority and exact snapshots prove no partial write.
func TestProjectTaskReopenServiceAdmissionNoWrites(t *testing.T) {
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	db := pebblestore.NewSessionStore(store)
	p := testPrincipal()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Retained", Agent: "swarm", Status: "completed", SessionID: "retained", Revision: 1, Archived: true}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.GetProjectTask(p.AccountScopeID, "project", "task")
	foreign := p
	foreign.AccountScopeID = "foreign"
	for _, principal := range []identity.Principal{p, foreign} {
		if _, err := server.ReopenProjectTask(context.Background(), principal, "project", "task", tool.ProjectTaskFollowupInput{Feedback: "continue", ClientRequestID: "key", Revision: 1}); err == nil {
			t.Fatal("inadmissible continuation accepted")
		}
	}
	after, _, _ := db.GetProjectTask(p.AccountScopeID, "project", "task")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected admission changed task")
	}
	if intents, err := db.ListRunIntents("retained", 10); err != nil || len(intents) != 0 {
		t.Fatal("rejected admission created intent")
	}
}
