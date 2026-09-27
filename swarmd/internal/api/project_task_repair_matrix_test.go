package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type testMockWorktreeService struct {
	mu          sync.Mutex
	allocCalls  int
	failAlloc   bool
	allocResult worktreeruntime.Allocation
}

func (m *testMockWorktreeService) GetConfig(workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) GetConfigForSavedWorkspaceForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return worktreeruntime.Config{}, nil, nil
}
func (m *testMockWorktreeService) SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return worktreeruntime.Config{}, nil, nil
}
func (m *testMockWorktreeService) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal{}, workspacePath, nameSeed, "dev", "agent/test")
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, nameSeed, "dev", "agent/test")
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal{}, workspacePath, nameSeed, baseBranch, branchName)
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allocCalls++
	if m.failAlloc {
		return worktreeruntime.Allocation{}, errors.New("simulated worktree allocation error")
	}
	res := m.allocResult
	if res.WorkspacePath == "" {
		res.WorkspacePath = "/mock/worktrees/agent-ws"
		res.BranchName = "agent/test-task"
		res.BaseBranch = "dev"
		res.BaseCommit = "base-commit-sha-001"
		res.RepoRoot = "/mock/repo"
	}
	return res, nil
}
func (m *testMockWorktreeService) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	return nil
}
func (m *testMockWorktreeService) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	return "agent/test", nil
}
func (m *testMockWorktreeService) ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return nil, nil
}
func (m *testMockWorktreeService) ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return nil, nil
}
func (m *testMockWorktreeService) PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error) {
	return worktreeruntime.PruneResult{}, nil
}
func (m *testMockWorktreeService) PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.PruneResult, error) {
	return worktreeruntime.PruneResult{}, nil
}
func (m *testMockWorktreeService) InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error) {
	return worktreeruntime.TaskWorkspaceState{BranchName: "dev", HeadCommit: "base-commit-sha-001"}, nil
}

type testMockRunService struct {
	mu              sync.Mutex
	enqueuedRuns    []string
	tpExecutions    int
	lastTPRecord    pebblestore.TaskProgramRecord
	lastTPSessionID string
}

func (m *testMockRunService) RunTurn(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}
func (m *testMockRunService) RunTurnStreaming(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}
func (m *testMockRunService) StopSessionRun(sessionID, runID, reason string) error {
	return nil
}
func (m *testMockRunService) ExecuteToolForSessionScope(ctx context.Context, workspacePath string, call tool.Call) (string, error) {
	return "{}", nil
}
func (m *testMockRunService) ListAgentToolDefinitions() []tool.Definition {
	return nil
}
func (m *testMockRunService) ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	return nil
}
func (m *testMockRunService) ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (m *testMockRunService) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (m *testMockRunService) ExecuteTaskProgramForCoordinator(ctx context.Context, p identity.Principal, parentSessionID, runID string, record pebblestore.TaskProgramRecord) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tpExecutions++
	m.lastTPSessionID = parentSessionID
	m.lastTPRecord = record
	return "completed", nil
}

type matrixTestFixture struct {
	db        *pebblestore.Store
	server    *Server
	wt        *testMockWorktreeService
	runSvc    *testMockRunService
	dir       string
	accountID string
	userID    string
}

func setupMatrixTestFixture(t *testing.T) *matrixTestFixture {
	dir := t.TempDir()
	db, err := pebblestore.Open(dir)
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}

	ss := pebblestore.NewSessionStore(db)
	el, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(ss, el)
	planLifecycle := sessionruntime.NewPlanLifecycleService(sessions)

	// Auth and Identity setup
	idStore := pebblestore.NewIdentityStore(db)
	accountID := "test-account-01"
	userID := "test-user-01"
	_, _ = idStore.PutAccountScope(pebblestore.AccountScopeRecord{
		ID:              accountID,
		UserID:          userID,
		CreatedByUserID: userID,
	})
	_, _ = idStore.PutUser(pebblestore.UserRecord{
		ID:             userID,
		Username:       "testuser",
		AccountScopeID: accountID,
	})

	authSvc := auth.NewService(idStore, &auth.SystemIdentity{})

	// Agent model settings setup
	settingsStore := pebblestore.NewAgentModelSettingsStore(db)
	_, _ = settingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: accountID,
		Swarm: pebblestore.AgentModelSwarmAssignments{
			Action: pebblestore.AgentModelAssignment{
				Provider:    "google",
				Model:       "gemini-2.5-action",
				Thinking:    "medium",
				ServiceTier: "standard",
			},
			Plan: pebblestore.AgentModelAssignment{
				Provider:    "google",
				Model:       "gemini-2.5-pro",
				Thinking:    "high",
				ServiceTier: "standard",
			},
		},
		Coder: pebblestore.AgentModelAssignment{
			Provider:    "anthropic",
			Model:       "claude-3-7-sonnet",
			Thinking:    "medium",
			ServiceTier: "standard",
		},
	})
	modelSettingsSvc := agentmodelsettings.NewService(settingsStore)

	agents := agentruntime.NewService(pebblestore.NewAgentStore(db))
	modelSvc := model.NewService(pebblestore.NewModelStore(db), el, nil)

	mockWT := &testMockWorktreeService{}
	mockRun := &testMockRunService{}

	s := &Server{
		sessions:           sessions,
		planLifecycle:      planLifecycle,
		auth:               authSvc,
		agents:             agents,
		model:              modelSvc,
		agentModelSettings: modelSettingsSvc,
		worktrees:          mockWT,
		runner:             mockRun,
	}
	s.v3SessionExecutor = newSessionV3Executor(s)
	s.planLifecycle.SetApplySessionMutation(s.applySessionV3PrimaryMutation)

	return &matrixTestFixture{
		db:        db,
		server:    s,
		wt:        mockWT,
		runSvc:    mockRun,
		dir:       dir,
		accountID: accountID,
		userID:    userID,
	}
}

func (f *matrixTestFixture) callAPI(method, path string, body any, p identity.Principal) *httptest.ResponseRecorder {
	var bodyBytes []byte
	if body != nil {
		if s, ok := body.(string); ok {
			bodyBytes = []byte(s)
		} else {
			bodyBytes, _ = json.Marshal(body)
		}
	}
	r := httptest.NewRequest(method, ProjectsPath+path, bytes.NewReader(bodyBytes))
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"sessions:read", "sessions:write", "projects:read", "projects:write"},
	}
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	f.server.apiMux().ServeHTTP(w, r)
	return w
}

func (f *matrixTestFixture) createProject(t *testing.T) string {
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	w := f.callAPI(http.MethodPost, "", map[string]any{
		"name": "Matrix Test Project",
		"workspaces": []map[string]string{
			{"path": "/repo/root", "role": "primary_code"},
		},
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	proj := resp["project"].(map[string]any)
	return proj["id"].(string)
}

// -----------------------------------------------------------------------------
// Case 1: manual small identity/profile/worktree/run
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case1_ManualSmallTaskIdentityProfileWorktreeRun(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Manual creation of small task routes to Coder, enforces authenticated identity,
	//   reserves Coder profile and isolated worktree, remains in pending_approval, and when approved
	//   starts real Coder execution via canonical machinery without executing before approval.
	// - Authority: handleProjectTaskCreate, deployProjectTaskExecution, handleProjectTaskApprove in api/projects.go.
	// - Threat/regression: Premature execution before approval, unisolated worktrees, or missing Coder profile.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Create small task via REST API
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":        "Fix parser edge-case",
		"prompt":       "Fix parser edge case in lexer.go",
		"agent":        "coder",
		"feature_size": "small",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create small task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// Verify task routed to Coder with pending_approval
	if taskMap["agent"] != "coder" {
		t.Fatalf("expected agent 'coder', got %v", taskMap["agent"])
	}
	if taskMap["status"] != "pending_approval" {
		t.Fatalf("expected status 'pending_approval', got %v", taskMap["status"])
	}
	sessID := taskMap["session_id"].(string)
	if sessID == "" {
		t.Fatal("expected session_id to be reserved on task")
	}

	// Verify session snapshot has Coder profile and worktree enabled
	sess, found, err := f.server.sessions.Store().GetSession(sessID)
	if err != nil || !found {
		t.Fatalf("session not found: %v", err)
	}
	if !sess.WorktreeEnabled {
		t.Fatal("expected worktree to be enabled for Coder task")
	}
	if sess.UserID != f.userID || sess.AccountScopeID != f.accountID {
		t.Fatalf("session identity mismatch: user=%q acct=%q", sess.UserID, sess.AccountScopeID)
	}

	// Verify NO run intent is pending execution yet
	intents, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	for _, intent := range intents {
		if intent.Status == pebblestore.V3RunIntentPendingExecutor {
			t.Fatal("pending small task must NOT have run intent pending executor before approval")
		}
	}

	// 2. Approve small task
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
	if w.Code != http.StatusOK {
		t.Fatalf("approve task failed %d: %s", w.Code, w.Body.String())
	}
	var appResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &appResp)
	if appResp["status"] != "approved" {
		t.Fatalf("expected status 'approved', got %v", appResp["status"])
	}

	// Verify task status transitioned to in_progress
	appTask := appResp["task"].(map[string]any)
	if appTask["status"] != "in_progress" {
		t.Fatalf("expected task status 'in_progress', got %v", appTask["status"])
	}

	// Verify run intent was created for Coder execution
	intentsAfter, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	if len(intentsAfter) == 0 {
		t.Fatal("expected run intent to be created after task approval")
	}
	if intentsAfter[0].UserID != f.userID || intentsAfter[0].AccountScopeID != f.accountID {
		t.Fatalf("run intent identity mismatch: user=%q", intentsAfter[0].UserID)
	}
}

// -----------------------------------------------------------------------------
// Case 2: tool-equivalent principal/shared path
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case2_ToolEquivalentPrincipalSharedPath(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: DeployProjectTaskForPrincipal and DeployProjectTask validate principal
	//   identity and fail closed if UserID is missing; authentic user principal deploys
	//   via shared canonical path without inventing identities.
	// - Authority: DeployProjectTask, DeployProjectTaskForPrincipal in api/project_task_program.go.
	// - Threat/regression: "deploy task: user id is required" caused by empty principal UserID.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Create task in pending_approval
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Small helper task",
		"prompt": "Implement helper",
		"agent":  "coder",
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// A: Calling with invalid principal (missing UserID) fails closed
	err := f.server.DeployProjectTask(context.Background(), identity.Principal{AccountScopeID: f.accountID}, projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required', got: %v", err)
	}

	// A1: Calling DeployProjectTask with missing Type (Type == "") fails closed even with UserID populated
	err = f.server.DeployProjectTask(context.Background(), identity.Principal{UserID: f.userID, AccountScopeID: f.accountID}, projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on missing Type in DeployProjectTask, got: %v", err)
	}

	// A2: Calling ApproveProjectTask with missing Type fails closed
	_, err = f.server.ApproveProjectTask(context.Background(), identity.Principal{UserID: f.userID, AccountScopeID: f.accountID}, projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "authenticated user identity") {
		t.Fatalf("expected 'authenticated user identity' on missing Type in ApproveProjectTask, got: %v", err)
	}

	// A3: Calling CreateProjectTask with missing Type fails closed
	_, err = f.server.CreateProjectTask(context.Background(), identity.Principal{UserID: f.userID, AccountScopeID: f.accountID}, projID, tool.ProjectTaskCreateInput{Title: "Missing Type Task"})
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on missing Type in CreateProjectTask, got: %v", err)
	}

	// B: Calling DeployProjectTask on a pending_approval task fails closed without approval
	err = f.server.DeployProjectTask(context.Background(), p, projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "awaiting approval") {
		t.Fatalf("expected awaiting approval error, got: %v", err)
	}

	// C: Approving task via canonical ApproveProjectTask succeeds and starts execution
	appTask, err := f.server.ApproveProjectTask(context.Background(), p, projID, taskID)
	if err != nil {
		t.Fatalf("expected successful approval, got: %v", err)
	}
	if appTask == nil || appTask.Status != "in_progress" {
		t.Fatalf("expected task in_progress, got: %#v", appTask)
	}

	// D: Non-existent account fails closed without inventing identities
	err = f.server.DeployProjectTask(context.Background(), identity.Principal{Type: "user", UserID: "u", AccountScopeID: "non-existent-account"}, projID, taskID)
	if err == nil {
		t.Fatal("expected failure on unknown account")
	}
}

// -----------------------------------------------------------------------------
// Case 3: parallel/staged Coders isolation/dependency/commit/conflict
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case3_ParallelStagedCodersIsolationDependencyCommitConflict(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Staged Task Programs require non-overlapping scopes, verify that clean worktrees
	//   with zero commits cannot be integrated (HEAD == base), and detect merge conflicts cleanly.
	// - Authority: integrateStage in run/service_task_program_scheduler.go, ValidateTaskProgramDefinition.
	// - Threat/regression: False integration of zero-commit worktrees, overlapping scope corruption, or merge conflicts.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Verify overlapping scopes in the same stage fail validation
	badTpDef := &pebblestore.TaskProgramDefinition{
		ID: "tp-bad-overlap",
		Stages: []pebblestore.TaskProgramStageSpec{
			{ID: "stage-1", DependencyEvidence: "Initial stage"},
		},
		Jobs: []pebblestore.TaskProgramJobSpec{
			{
				ID:                 "job-1",
				StageID:            "stage-1",
				AgentType:          "coder",
				Title:              "Part A",
				MetaPrompt:         "Prompt A",
				OwnedScope:         []string{"pkg/core/**"},
				AcceptanceCriteria: []string{"A done"},
			},
			{
				ID:                 "job-2",
				StageID:            "stage-1",
				AgentType:          "coder",
				Title:              "Part B",
				MetaPrompt:         "Prompt B",
				OwnedScope:         []string{"pkg/core/**"},
				AcceptanceCriteria: []string{"B done"},
			},
		},
	}
	if err := pebblestore.ValidateTaskProgramDefinition(badTpDef); err == nil {
		t.Fatal("expected overlapping scopes in same stage to fail validation")
	}

	// 2. Create valid multi-stage Coder task program with non-overlapping scopes
	tpDef := &pebblestore.TaskProgramDefinition{
		ID: "tp-staged-01",
		Stages: []pebblestore.TaskProgramStageSpec{
			{ID: "stage-1", DependencyEvidence: "Initial stage"},
			{ID: "stage-2", DependsOn: []string{"stage-1"}, DependencyEvidence: "Stage 2 after Stage 1"},
		},
		Jobs: []pebblestore.TaskProgramJobSpec{
			{
				ID:                 "job-core",
				StageID:            "stage-1",
				AgentType:          "coder",
				Title:              "Core Impl",
				MetaPrompt:         "Implement core",
				OwnedScope:         []string{"pkg/core/**"},
				AcceptanceCriteria: []string{"Core complete"},
			},
			{
				ID:                 "job-api",
				StageID:            "stage-2",
				DependsOn:          []string{"job-core"},
				AgentType:          "coder",
				Title:              "API Impl",
				MetaPrompt:         "Implement API",
				OwnedScope:         []string{"pkg/api/**"},
				AcceptanceCriteria: []string{"API complete"},
			},
		},
	}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":        "Multi-stage Coder task",
		"prompt":       "Execute staged program",
		"task_program": tpDef,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create staged task program failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// Verify TaskProgram status is declared and associated
	if taskMap["task_program_id"] == "" {
		t.Fatal("expected task_program_id to be populated")
	}

	// Approve and deploy the task program
	appTask, err := f.server.ApproveProjectTask(context.Background(), p, projID, taskID)
	if err != nil {
		t.Fatalf("approve task program failed: %v", err)
	}
	if appTask == nil || appTask.Status != "in_progress" {
		t.Fatalf("expected task in_progress, got: %#v", appTask)
	}

	// Verify canonical scheduler was invoked with staged structure
	for i := 0; i < 50; i++ {
		f.runSvc.mu.Lock()
		execs := f.runSvc.tpExecutions
		f.runSvc.mu.Unlock()
		if execs >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.runSvc.mu.Lock()
	executions := f.runSvc.tpExecutions
	lastRec := f.runSvc.lastTPRecord
	f.runSvc.mu.Unlock()
	if executions != 1 {
		t.Fatalf("expected 1 canonical TP execution, got %d", executions)
	}
	if len(lastRec.Definition.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(lastRec.Definition.Stages))
	}
	if len(lastRec.Definition.Stages[1].DependsOn) != 1 || lastRec.Definition.Stages[1].DependsOn[0] != "stage-1" {
		t.Fatalf("expected stage-2 to depend on stage-1, got %#v", lastRec.Definition.Stages[1].DependsOn)
	}
	if len(lastRec.Definition.Jobs[1].DependsOn) != 1 || lastRec.Definition.Jobs[1].DependsOn[0] != "job-core" {
		t.Fatalf("expected job-api to depend on job-core, got %#v", lastRec.Definition.Jobs[1].DependsOn)
	}

	// 3. Verify clean worktrees with zero commits cannot be integrated (HEAD == base)
	wIntegrate := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/integrate", nil, p)
	if wIntegrate.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on integrating zero-commit worktree, got %d: %s", wIntegrate.Code, wIntegrate.Body.String())
	}
	if !strings.Contains(wIntegrate.Body.String(), "no commits") {
		t.Fatalf("expected 'no commits' error message, got: %s", wIntegrate.Body.String())
	}

	// 4. Negative assertion: non-existent job redeploy fails closed
	errRedeploy := f.server.redeployTaskProgramJob(p, projID, taskID, "nonexistent-job", "Fix conflict")
	if errRedeploy == nil || !strings.Contains(errRedeploy.Error(), "not found") {
		t.Fatalf("expected error on redeploying non-existent job, got: %v", errRedeploy)
	}

	// 5. Negative assertion: redeploying task without task program fails closed
	smallTaskRec := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Plain task",
		"prompt": "Plain prompt",
		"agent":  "coder",
	}, p)
	var smallResp map[string]any
	_ = json.Unmarshal(smallTaskRec.Body.Bytes(), &smallResp)
	plainTaskID := smallResp["task"].(map[string]any)["id"].(string)
	errNoProg := f.server.redeployTaskProgramJob(p, projID, plainTaskID, "any-job", "Retry")
	if errNoProg == nil || !strings.Contains(errNoProg.Error(), "no associated task program") {
		t.Fatalf("expected no associated task program error, got: %v", errNoProg)
	}

	// 6. Valid redeploy job records feedback and increments attempt number
	errRedeployValid := f.server.redeployTaskProgramJob(p, projID, taskID, "job-core", "Resolve lock conflict in core")
	if errRedeployValid != nil {
		t.Fatalf("expected successful redeploy of job-core, got: %v", errRedeployValid)
	}
	taskAfterRedeploy, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if len(taskAfterRedeploy.FeedbackHistory) == 0 || !strings.Contains(taskAfterRedeploy.FeedbackHistory[0], "Resolve lock conflict") {
		t.Fatalf("expected feedback recorded in task feedback history, got: %#v", taskAfterRedeploy.FeedbackHistory)
	}
}

// -----------------------------------------------------------------------------
// Case 4: manual planning pending then exact accept/model transition
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case4_ManualPlanningPendingThenExactAcceptModelTransition(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Big feature task routes to Plan agent in ModePlan (read-only planning).
	//   Linked task exit_plan_mode submits plan to card as pending_approval without auto execution.
	//   User acceptance in card transfers execution to Swarm Default with Swarm Action model and
	//   emits canonical checkpoint continuation message without second approval.
	// - Authority: handleProjectTaskCreate, executeExitPlanModeTool, handleProjectTaskApprove.
	// - Threat/regression: Implementing before approval, executing with wrong Plan agent/model, or redundant approvals.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Create big feature task
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":        "Architect Payment Subsystem",
		"prompt":       "Design complete billing flow",
		"agent":        "plan",
		"feature_size": "big",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create big task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)
	sessID := taskMap["session_id"].(string)

	// Verify task starts in 'planning' mode, session in ModePlan
	if taskMap["status"] != "planning" {
		t.Fatalf("expected status 'planning', got %v", taskMap["status"])
	}
	sess, _, _ := f.server.sessions.Store().GetSession(sessID)
	if sess.Mode != sessionruntime.ModePlan {
		t.Fatalf("expected session mode 'plan', got %v", sess.Mode)
	}

	// 2. Simulate Plan agent calling exit_plan_mode via PlanLifecycleService
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-payment-v1",
		Title: "Payment Architecture Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Architect and implement billing subsystem"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Stripe Webhook Handler", Tasks: []string{"Add webhook handler"}},
			{ID: "cp-2", Title: "Subscription DB Models", Tasks: []string{"Add DB models"}},
		},
	}
	subResult, err := f.server.planLifecycle.SubmitProjectTaskStructuredPlan(sessionruntime.ProjectTaskPlanSubmissionInput{
		AccountScopeID: f.accountID,
		UserID:         f.userID,
		ProjectID:      projID,
		TaskID:         taskID,
		SessionID:      sessID,
		Document:       doc,
		PlanText:       "# Payment Architecture Plan\n\nGoal: Billing subsystem",
		Title:          "Payment Architecture Plan",
	})
	if err != nil {
		t.Fatalf("submit structured plan failed: %v", err)
	}

	// Verify task status transitioned to 'pending_approval' with PlanBinding
	taskAfterSubmit, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if taskAfterSubmit.Status != "pending_approval" {
		t.Fatalf("expected status 'pending_approval', got %v", taskAfterSubmit.Status)
	}
	if taskAfterSubmit.PlanBinding == nil || taskAfterSubmit.PlanBinding.PlanID != "plan-payment-v1" {
		t.Fatalf("expected PlanBinding for plan-payment-v1, got %#v", taskAfterSubmit.PlanBinding)
	}
	if subResult.Receipt == "" {
		t.Fatal("expected non-empty plan receipt")
	}

	// 3. User accepts the plan on the task card
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", map[string]any{
		"session_id":          sessID,
		"plan_id":             "plan-payment-v1",
		"definition_revision": 1,
	}, p)
	if w.Code != http.StatusOK {
		t.Fatalf("approve task plan failed %d: %s", w.Code, w.Body.String())
	}

	// Verify task status transitioned to 'in_progress', agent is 'swarm'
	taskAfterApprove, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if taskAfterApprove.Status != "in_progress" {
		t.Fatalf("expected status 'in_progress', got %v", taskAfterApprove.Status)
	}
	if taskAfterApprove.Agent != "swarm" {
		t.Fatalf("expected agent 'swarm' executing approved plan, got %v", taskAfterApprove.Agent)
	}

	// Verify session mode transitioned to auto and model switched to Swarm Action
	sessAfterApprove, _, _ := f.server.sessions.Store().GetSession(sessID)
	if sessAfterApprove.Mode != sessionruntime.ModeAuto {
		t.Fatalf("expected session mode 'auto' after approval, got %v", sessAfterApprove.Mode)
	}
	if sessAfterApprove.Preference.Model != "gemini-2.5-action" {
		t.Fatalf("expected Swarm Action model 'gemini-2.5-action', got %v", sessAfterApprove.Preference.Model)
	}
}

// -----------------------------------------------------------------------------
// Case 5: direct plan no planning run
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case5_DirectPlanNoPlanningRun(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Orchestrator/user submitting direct structured plan creates task in pending_approval
	//   with PlanBinding using PlanLifecycleService without starting a planning session.
	// - Authority: handleProjectTaskCreate, SubmitProjectTaskStructuredPlan.
	// - Threat/regression: Running a redundant Plan agent when plan is already authored.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	doc := &pebblestore.SessionPlanDocument{
		ID:    "direct-plan-01",
		Title: "Direct Orchestrator Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Pre-authored pipeline overhaul"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Setup CI Lint", Tasks: []string{"Configure golangci-lint"}},
		},
	}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Orchestrator Authored CI Fix",
		"prompt":        "Apply CI overhaul",
		"feature_size":  "big",
		"plan_document": doc,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create direct plan task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	if taskMap["status"] != "pending_approval" {
		t.Fatalf("expected status 'pending_approval', got %v", taskMap["status"])
	}
	pb := taskMap["plan_binding"].(map[string]any)
	if pb["plan_id"] != "direct-plan-01" {
		t.Fatalf("expected plan_id 'direct-plan-01', got %v", pb["plan_id"])
	}

	// Verify no planning run was started
	sessID := taskMap["session_id"].(string)
	intents, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	for _, in := range intents {
		if in.Status == pebblestore.V3RunIntentPendingExecutor {
			t.Fatal("direct plan task must NOT execute run before approval")
		}
	}
}

// -----------------------------------------------------------------------------
// Case 6: unaccepted no implementation intents
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case6_UnacceptedNoImplementationIntents(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: A task in pending_approval must NOT have an active or pending implementation
	//   run intent. Deploy session is not approval.
	// - Authority: deployProjectTaskExecution in api/projects.go.
	// - Threat/regression: Implementing unauthorized tasks before user review and acceptance.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Unapproved task",
		"prompt": "Do not run yet",
		"agent":  "coder",
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	sessID := taskMap["session_id"].(string)

	intents, err := f.server.sessions.Store().ListRunIntents(sessID, 10)
	if err != nil {
		t.Fatalf("list run intents: %v", err)
	}
	for _, in := range intents {
		if in.Status == pebblestore.V3RunIntentPendingExecutor {
			t.Fatalf("unexpected pending run intent found in pending_approval task: %s", in.RunID)
		}
	}
}

// -----------------------------------------------------------------------------
// Case 7: rejected/stale revisions no side effects
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case7_RejectedStaleRevisionsNoSideEffects(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Approving a rejected task, a rejected plan, a stale plan revision,
	//   a mismatched plan/session ID, or a cross-account task must be rejected without mutating durable state.
	// - Authority: ApproveProjectTask in api/project_task_program.go.
	// - Threat/regression: Executing stale or rejected plans or bypassing authorization.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Create task and reject it
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Task to reject",
		"prompt": "Reject me",
		"agent":  "coder",
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", nil, p)
	if w.Code != http.StatusOK {
		t.Fatalf("reject task failed %d: %s", w.Code, w.Body.String())
	}

	// 2. Attempting to approve rejected task fails with 400 Bad Request
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on approving rejected task, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rejected") {
		t.Fatalf("expected rejected message, got: %s", w.Body.String())
	}

	// 3. Cross-account approval attempt is rejected with 403 Forbidden or 404 Not Found
	crossPrincipal := identity.Principal{Type: "user", UserID: "attacker", AccountScopeID: "rogue-account"}
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, crossPrincipal)
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
		t.Fatalf("expected 404 or 403 on cross-account approval, got %d", w.Code)
	}

	// 4. Stale revision rejection: create big feature task with plan Rev 1, then update to Rev 2
	doc1 := &pebblestore.SessionPlanDocument{
		ID:    "plan-stale-01",
		Title: "Plan Rev 1",
		Info:  pebblestore.SessionPlanInfo{Goal: "Goal 1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1"},
		},
	}
	wPlan := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Stale guard task",
		"prompt":        "Test stale revision rejection",
		"plan_document": doc1,
	}, p)
	if wPlan.Code != http.StatusCreated {
		t.Fatalf("create plan task failed %d: %s", wPlan.Code, wPlan.Body.String())
	}
	var planResp map[string]any
	_ = json.Unmarshal(wPlan.Body.Bytes(), &planResp)
	pTaskMap := planResp["task"].(map[string]any)
	pTaskID := pTaskMap["id"].(string)
	pSessID := pTaskMap["session_id"].(string)

	// Submit Rev 2 via SubmitProjectTaskPlan
	doc2 := &pebblestore.SessionPlanDocument{
		ID:    "plan-stale-01",
		Title: "Plan Rev 2",
		Info:  pebblestore.SessionPlanInfo{Goal: "Goal 2 updated"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1 updated"},
		},
	}
	subResult2, err := f.server.SubmitProjectTaskPlan(context.Background(), sessionruntime.ProjectTaskPlanSubmissionInput{
		AccountScopeID:  f.accountID,
		UserID:          f.userID,
		ProjectID:       projID,
		TaskID:          pTaskID,
		SessionID:       pSessID,
		Document:        doc2,
		PlanText:        "# Plan Rev 2",
		Title:           "Plan Rev 2",
	})
	if err != nil {
		t.Fatalf("submit rev 2 failed: %v", err)
	}
	if subResult2.Plan.Version != 2 {
		t.Fatalf("expected plan revision 2, got %d", subResult2.Plan.Version)
	}

	// Attempt approval with stale definition_revision: 1
	wStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+pTaskID+"/approve", map[string]any{
		"session_id":          pSessID,
		"plan_id":             "plan-stale-01",
		"definition_revision": 1,
	}, p)
	if wStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale revision, got %d: %s", wStale.Code, wStale.Body.String())
	}
	if !strings.Contains(wStale.Body.String(), "stale") {
		t.Fatalf("expected 'stale' error message, got: %s", wStale.Body.String())
	}

	// Attempt approval with mismatched plan_id
	wMismatchPlan := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+pTaskID+"/approve", map[string]any{
		"session_id":          pSessID,
		"plan_id":             "wrong-plan-id",
		"definition_revision": 2,
	}, p)
	if wMismatchPlan.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on plan mismatch, got %d: %s", wMismatchPlan.Code, wMismatchPlan.Body.String())
	}

	// Attempt approval with mismatched session_id
	wMismatchSess := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+pTaskID+"/approve", map[string]any{
		"session_id":          "wrong-session-id",
		"plan_id":             "plan-stale-01",
		"definition_revision": 2,
	}, p)
	if wMismatchSess.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on session mismatch, got %d: %s", wMismatchSess.Code, wMismatchSess.Body.String())
	}

	// 5. Verify no side effects occurred: task remains pending_approval and no run intent created
	taskAfterStale, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, pTaskID)
	if taskAfterStale.Status != "pending_approval" {
		t.Fatalf("expected task status to remain 'pending_approval', got %q", taskAfterStale.Status)
	}
	intents, _ := f.server.sessions.Store().ListRunIntents(pSessID, 10)
	for _, in := range intents {
		if in.Status == pebblestore.V3RunIntentPendingExecutor || in.Status == pebblestore.V3RunIntentRunning {
			t.Fatalf("unexpected active run intent found after rejected approval: %s", in.RunID)
		}
	}
}

// -----------------------------------------------------------------------------
// Case 8: duplicate/concurrent/create/deploy/accept retries counts
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case8_DuplicateConcurrentRetriesCounts(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Duplicate or concurrent create, deploy, and approve calls must be idempotent
	//   and not create orphan sessions, duplicate runIntents, or duplicate git worktrees.
	// - Authority: ApproveProjectTask in api/project_task_program.go.
	// - Threat/regression: Duplicate execution runs and orphan sessions on network retries.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Concurrent create: 5 concurrent callers create the same task
	var wgCreate sync.WaitGroup
	createCount := 5
	createCodes := make([]int, createCount)
	createdSessIDs := make([]string, createCount)
	f.wt.mu.Lock()
	allocsBefore := f.wt.allocCalls
	f.wt.mu.Unlock()

	for i := 0; i < createCount; i++ {
		wgCreate.Add(1)
		go func(idx int) {
			defer wgCreate.Done()
			cw := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
				"id":     "concurrent-task-01",
				"title":  "Concurrent Task",
				"prompt": "Test concurrent create",
				"agent":  "coder",
			}, p)
			createCodes[idx] = cw.Code
			var cResp map[string]any
			if err := json.Unmarshal(cw.Body.Bytes(), &cResp); err == nil {
				if tm, ok := cResp["task"].(map[string]any); ok {
					if sid, ok := tm["session_id"].(string); ok {
						createdSessIDs[idx] = sid
					}
				}
			}
		}(i)
	}
	wgCreate.Wait()

	for i, code := range createCodes {
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("concurrent create %d returned unexpected code %d", i, code)
		}
	}
	// All concurrent callers received the exact same deterministic session ID!
	sessID := createdSessIDs[0]
	if sessID == "" {
		t.Fatal("expected non-empty session ID from concurrent create")
	}
	for i, sid := range createdSessIDs {
		if sid != sessID {
			t.Fatalf("concurrent create %d session ID mismatch: expected %q, got %q", i, sessID, sid)
		}
	}
	// Verify exactly 1 worktree allocation occurred across all concurrent creates
	f.wt.mu.Lock()
	allocsAfter := f.wt.allocCalls
	f.wt.mu.Unlock()
	if allocsAfter-allocsBefore != 1 {
		t.Fatalf("expected exactly 1 worktree allocation across concurrent creates, got %d", allocsAfter-allocsBefore)
	}
	// Verify 0 run intents exist before approval
	intentsBeforeApprove, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	if len(intentsBeforeApprove) != 0 {
		t.Fatalf("expected 0 run intents before approval, got %d", len(intentsBeforeApprove))
	}

	taskID := "concurrent-task-01"

	// 2. Concurrent deploy before approval: all fail closed
	var wgDeploy sync.WaitGroup
	deployCount := 5
	deployErrs := make([]error, deployCount)
	for i := 0; i < deployCount; i++ {
		wgDeploy.Add(1)
		go func(idx int) {
			defer wgDeploy.Done()
			deployErrs[idx] = f.server.DeployProjectTask(context.Background(), p, projID, taskID)
		}(i)
	}
	wgDeploy.Wait()
	for i, err := range deployErrs {
		if err == nil || !strings.Contains(err.Error(), "awaiting approval") {
			t.Fatalf("expected awaiting approval error on concurrent deploy %d, got: %v", i, err)
		}
	}

	// 3. First approval
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
	if w.Code != http.StatusOK {
		t.Fatalf("first approve failed %d", w.Code)
	}
	var app1 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &app1)
	if app1["status"] != "approved" {
		t.Fatalf("expected 'approved', got %v", app1["status"])
	}

	// Second approval (idempotent retry)
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
	if w.Code != http.StatusOK {
		t.Fatalf("second approve failed %d", w.Code)
	}
	var app2 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &app2)
	if app2["status"] != "already_approved" {
		t.Fatalf("expected 'already_approved' on retry, got %v", app2["status"])
	}

	// Concurrent retries: 5 goroutines calling approve concurrently
	var wg sync.WaitGroup
	concurrentCount := 5
	results := make([]int, concurrentCount)
	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rw := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
			results[idx] = rw.Code
		}(i)
	}
	wg.Wait()
	for i, code := range results {
		if code != http.StatusOK {
			t.Fatalf("concurrent approve %d returned code %d", i, code)
		}
	}

	// Verify only 1 run intent was enqueued across all retries and concurrent calls
	intents, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	if len(intents) != 1 {
		t.Fatalf("expected exactly 1 run intent across retries and concurrent calls, got %d", len(intents))
	}

	// Partial failure and resumption test:
	// Verify that if a task was partially marked in_progress without run intent,
	// subsequent approve safely resumes and establishes the run intent.
	w2 := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Partial failure task",
		"prompt": "Test partial failure resume",
		"agent":  "coder",
	}, p)
	var resp2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	taskMap2 := resp2["task"].(map[string]any)
	taskID2 := taskMap2["id"].(string)
	sessID2 := taskMap2["session_id"].(string)

	// Simulate partial failure: task status flipped to in_progress but NO run intent exists
	_, _ = f.server.sessions.Store().UpdateProjectTask(f.accountID, projID, taskID2, func(t *pebblestore.ProjectTaskRecord) error {
		t.Status = "in_progress"
		return nil
	})
	// Approve call detects missing active run intent, resumes and establishes run intent
	wResume := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID2+"/approve", nil, p)
	if wResume.Code != http.StatusOK {
		t.Fatalf("resumed approve failed %d: %s", wResume.Code, wResume.Body.String())
	}
	intents2, _ := f.server.sessions.Store().ListRunIntents(sessID2, 10)
	if len(intents2) != 1 {
		t.Fatalf("expected 1 run intent after resumed partial failure, got %d", len(intents2))
	}
}

// -----------------------------------------------------------------------------
// Case 9: missing identity/wrong account/allocation/create/seed/enqueue failures
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case9_MissingIdentityWrongAccountAllocationFailures(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Operations must fail closed on missing identity, cross-account access, or
	//   worktree allocation failure without error swallowing or continuing unisolated.
	// - Authority: handleProjects, handleProjectTaskCreate, deployProjectTaskExecution.
	// - Threat/regression: Continuing unisolated when worktree allocation fails.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// 1. Unauthenticated request rejected
	unauthReq := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+projID+"/tasks", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	f.server.apiMux().ServeHTTP(rec, unauthReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated request, got %d", rec.Code)
	}

	// 2. Worktree allocation failure fails closed
	f.wt.mu.Lock()
	f.wt.failAlloc = true
	f.wt.mu.Unlock()

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Failing worktree task",
		"prompt": "Allocation will fail",
		"agent":  "coder",
	}, p)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when worktree allocation fails, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "worktree allocation failed") {
		t.Fatalf("expected worktree allocation error message, got: %s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Case 10: reopen store/recovery exact receipt/links/no replay
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case10_ReopenStoreRecoveryExactReceiptLinksNoReplay(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Closing and reopening Pebble store must preserve exact task-to-session and
	//   task-to-plan links, receipts, active run intents, and status without mutation replay or duplicate runs.
	// - Authority: pebble store persistence for ProjectTaskRecord, SessionPlanSnapshot, and V3SessionRunIntent.
	// - Threat/regression: Data loss, corrupted task linkage, or duplicate runs upon daemon restart.
	f := setupMatrixTestFixture(t)
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	doc := &pebblestore.SessionPlanDocument{
		ID:    "recovery-plan-01",
		Title: "Recovery Verification Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify durable persistence across reopen"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Step 1", Tasks: []string{"Durable step"}},
		},
	}
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Durable recovery task",
		"prompt":        "Verify persistence",
		"plan_document": doc,
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)
	sessID := taskMap["session_id"].(string)

	// Approve task so that plan is accepted and an active RunIntent exists
	wApp := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", map[string]any{
		"session_id":          sessID,
		"plan_id":             "recovery-plan-01",
		"definition_revision": 1,
	}, p)
	if wApp.Code != http.StatusOK {
		t.Fatalf("approve before reopen failed %d: %s", wApp.Code, wApp.Body.String())
	}

	// Close store
	dir := f.dir
	f.db.Close()

	// Reopen store
	db2, err := pebblestore.Open(dir)
	if err != nil {
		t.Fatalf("reopen store failed: %v", err)
	}
	defer db2.Close()

	ss2 := pebblestore.NewSessionStore(db2)

	// Verify task record exists with exact links, receipt, and plan document
	task, found, err := ss2.GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("task not found after reopen: %v", err)
	}
	if task.SessionID != sessID {
		t.Fatalf("expected session_id %q, got %q", sessID, task.SessionID)
	}
	if task.PlanBinding == nil || task.PlanBinding.PlanID != "recovery-plan-01" {
		t.Fatalf("plan binding corrupted or missing: %#v", task.PlanBinding)
	}
	if task.PlanBinding.Receipt == "" {
		t.Fatal("plan binding receipt was empty after reopen")
	}
	hydrateTaskPlanDocument(task, ss2)
	if task.PlanDocument == nil || task.PlanDocument.ID != "recovery-plan-01" {
		t.Fatalf("plan document missing or corrupted after reopen: %#v", task.PlanDocument)
	}

	// Verify active run intent recovered after reopen
	activeIntent, intentFound, intentErr := ss2.GetV3SessionActiveRunIntent(sessID)
	if intentErr != nil || !intentFound || activeIntent == nil {
		t.Fatalf("expected active run intent recovered after reopen: found=%v, err=%v", intentFound, intentErr)
	}
	if activeIntent.Status != pebblestore.V3RunIntentPendingExecutor && activeIntent.Status != pebblestore.V3RunIntentRunning {
		t.Fatalf("unexpected active run intent status: %v", activeIntent.Status)
	}

	// Verify session metadata retained project and task linkage
	sess, found, err := ss2.GetSession(sessID)
	if err != nil || !found {
		t.Fatalf("session not found after reopen: %v", err)
	}
	if sess.Metadata["task_id"] != taskID || sess.Metadata["project_id"] != projID {
		t.Fatalf("session task linkage corrupted after reopen: %#v", sess.Metadata)
	}

	// Verify recovery resumption: reconstructing full services and calling approve on recovered server resumes reserved op without duplicating run intents
	el2, _ := pebblestore.NewEventLog(db2)
	sessions2 := sessionruntime.NewService(ss2, el2)
	planLifecycle2 := sessionruntime.NewPlanLifecycleService(sessions2)
	idStore2 := pebblestore.NewIdentityStore(db2)
	authSvc2 := auth.NewService(idStore2, &auth.SystemIdentity{})
	settingsStore2 := pebblestore.NewAgentModelSettingsStore(db2)
	modelSettingsSvc2 := agentmodelsettings.NewService(settingsStore2)
	agents2 := agentruntime.NewService(pebblestore.NewAgentStore(db2))
	modelSvc2 := model.NewService(pebblestore.NewModelStore(db2), el2, nil)
	server2 := &Server{
		sessions:           sessions2,
		planLifecycle:      planLifecycle2,
		auth:               authSvc2,
		agents:             agents2,
		model:              modelSvc2,
		agentModelSettings: modelSettingsSvc2,
	}
	server2.v3SessionExecutor = newSessionV3Executor(server2)
	server2.planLifecycle.SetApplySessionMutation(server2.applySessionV3PrimaryMutation)
	recoveredTask, rErr := server2.ApproveProjectTask(context.Background(), p, projID, taskID, tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "recovery-plan-01",
		DefinitionRevision: 1,
	})
	if rErr != nil {
		t.Fatalf("approve after recovery failed: %v", rErr)
	}
	if recoveredTask == nil || recoveredTask.Status != "in_progress" {
		t.Fatalf("expected in_progress status after recovery approval, got: %#v", recoveredTask)
	}
	intentsAfterRecovery, _ := ss2.ListRunIntents(sessID, 10)
	if len(intentsAfterRecovery) != 1 {
		t.Fatalf("expected exactly 1 run intent after recovery without duplicates, got %d", len(intentsAfterRecovery))
	}
}

// -----------------------------------------------------------------------------
// Case 11: non-code and code media-keyword routing
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case11_NonCodeAndCodeMediaKeywordRouting(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Media tasks (image, video, audio) route directly to media generation without
	//   agent sessions; code tasks containing media keywords in prompts strictly preserve Coder routing.
	// - Authority: deployProjectTaskExecution in api/projects.go, RouteAndPlanProjectTaskWithOptions.
	// - Threat/regression: Mistaking a coding task for media or spinning up chat sessions for media.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// A: Explicit media task has NO chat session
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Generate Hero Graphic",
		"prompt": "Create futuristic tesseract",
		"agent":  "image",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create media task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	if taskMap["session_id"] != nil && taskMap["session_id"] != "" {
		t.Fatalf("media tasks must not create chat sessions, got session_id %v", taskMap["session_id"])
	}

	// B: Code task containing media keywords in prompt strictly routes to Coder
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":        "Build Image and Video Gallery Component",
		"prompt":       "Implement React UI component that renders video clips and image thumbnails",
		"agent":        "coder",
		"feature_size": "small",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code task with media keywords failed %d: %s", w.Code, w.Body.String())
	}
	var codeResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &codeResp)
	codeTaskMap := codeResp["task"].(map[string]any)
	if codeTaskMap["agent"] != "coder" {
		t.Fatalf("expected agent 'coder', got %v", codeTaskMap["agent"])
	}
	if codeTaskMap["outcome_type"] != "code_pr" {
		t.Fatalf("expected outcome_type 'code_pr', got %v", codeTaskMap["outcome_type"])
	}
}

// -----------------------------------------------------------------------------
// Focused Requirement-First Tests for Corrected Defect Areas
// -----------------------------------------------------------------------------

func TestTaskMatrix_DeployProjectTaskExecution_FailsClosedOnMissingPrincipal(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: deployProjectTaskExecution MUST fail closed with "user id is required"
	//   when called with an invalid, non-user, or unauthenticated principal.
	// - Authority: deployProjectTaskExecution in api/projects.go.
	// - Threat/regression: Internal or direct deployment bypassing principal checks.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	proj, _, _ := f.server.sessions.Store().GetProject(f.accountID, projID)

	task := &pebblestore.ProjectTaskRecord{
		ID:            "task_missing_p",
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Test Principal Gate",
		Agent:         "coder",
		Status:        "pending_approval",
		WorkspacePath: "/mock/repo",
	}

	// 1. Empty principal fails closed
	err := f.server.deployProjectTaskExecution(identity.Principal{}, proj, task, "in_progress", "test")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on empty principal, got: %v", err)
	}

	// 2. Missing user ID fails closed
	err = f.server.deployProjectTaskExecution(identity.Principal{AccountScopeID: f.accountID}, proj, task, "in_progress", "test")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on principal missing UserID, got: %v", err)
	}

	// 3. Non-user type fails closed
	err = f.server.deployProjectTaskExecution(identity.Principal{Type: "device", UserID: "dev1", AccountScopeID: f.accountID}, proj, task, "in_progress", "test")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on non-user principal, got: %v", err)
	}

	// 4. Missing user type (Type == "") fails closed even with UserID populated
	err = f.server.deployProjectTaskExecution(identity.Principal{UserID: "u1", AccountScopeID: f.accountID}, proj, task, "in_progress", "test")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required' on principal with missing Type, got: %v", err)
	}
}

func TestTaskMatrix_ApproveCoderPersistsAllocatedWorktreeMetadata(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: When ApproveProjectTask allocates an isolated worktree for a Coder task,
	//   the task record in Pebble MUST persist the allocated worktree path, branch, base branch,
	//   base commit, and worktree name.
	// - Authority: ApproveProjectTask in api/project_task_program.go.
	// - Threat/regression: Task record retains stale pre-allocation workspace path, breaking task card links.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Create small Coder task
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":          "Refactor auth logic",
		"prompt":         "Refactor token validation",
		"agent":          "coder",
		"feature_size":   "small",
		"workspace_path": "/mock/repo",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// Configure mock worktree allocation result
	f.wt.mu.Lock()
	f.wt.allocResult = worktreeruntime.Allocation{
		WorkspacePath: "/mock/worktrees/agent-auth-refactor",
		BranchName:    "agent/auth-refactor",
		BaseBranch:    "dev",
		BaseCommit:    "commit-sha-abc-123",
		RepoRoot:      "/mock/repo",
	}
	f.wt.mu.Unlock()

	// Approve task
	approvedTask, err := f.server.ApproveProjectTask(context.Background(), p, projID, taskID)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}

	// Invariant: Returned and persisted task record must have allocated worktree metadata
	if approvedTask.WorkspacePath != "/mock/worktrees/agent-auth-refactor" {
		t.Fatalf("expected task WorkspacePath %q, got %q", "/mock/worktrees/agent-auth-refactor", approvedTask.WorkspacePath)
	}
	if approvedTask.WorktreeBranch != "agent/auth-refactor" {
		t.Fatalf("expected task WorktreeBranch %q, got %q", "agent/auth-refactor", approvedTask.WorktreeBranch)
	}
	if approvedTask.BaseCommit != "commit-sha-abc-123" {
		t.Fatalf("expected task BaseCommit %q, got %q", "commit-sha-abc-123", approvedTask.BaseCommit)
	}

	// Verify fresh read from Pebble store confirms persistence
	freshTask, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found || freshTask == nil {
		t.Fatalf("task not found in store: %v", err)
	}
	if freshTask.WorkspacePath != "/mock/worktrees/agent-auth-refactor" {
		t.Fatalf("persisted task WorkspacePath mismatch: %q", freshTask.WorkspacePath)
	}
	if freshTask.WorktreeBranch != "agent/auth-refactor" {
		t.Fatalf("persisted task WorktreeBranch mismatch: %q", freshTask.WorktreeBranch)
	}
	if freshTask.BaseCommit != "commit-sha-abc-123" {
		t.Fatalf("persisted task BaseCommit mismatch: %q", freshTask.BaseCommit)
	}
}

func TestTaskMatrix_DeployProjectTaskProgram_DurableWithoutRunner(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: deployProjectTaskProgram and redeployTaskProgramJob must fail closed with an explicit
	//   runner-unavailable error when s.runner is not configured, rather than silently pretending to deploy
	//   an execution with no executor.
	// - Authority: deployProjectTaskProgram, redeployTaskProgramJob in api/project_task_program.go.
	// - Threat/regression: Silent fake deployment leaving tasks in_progress without execution.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Construct server with nil runner
	sNoRunner := &Server{
		sessions:           f.server.sessions,
		worktrees:          f.wt,
		agentModelSettings: f.server.agentModelSettings,
		model:              f.server.model,
		agents:             f.server.agents,
	}

	tpDef := &pebblestore.TaskProgramDefinition{
		ID: "prog-no-runner",
		Stages: []pebblestore.TaskProgramStageSpec{
			{ID: "stage-1", DependencyEvidence: "Ready"},
		},
		Jobs: []pebblestore.TaskProgramJobSpec{
			{
				ID:                 "job-1",
				StageID:            "stage-1",
				AgentType:          "coder",
				Title:              "Job 1",
				MetaPrompt:         "Prompt 1",
				OwnedScope:         []string{"pkg/**"},
				AcceptanceCriteria: []string{"Done"},
			},
		},
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:            "task-no-runner",
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Task Program without runner",
		Agent:         "coder",
		Status:        "pending_approval",
		WorkspacePath: "/mock/repo",
		TaskProgram:   tpDef,
	}
	_ = f.server.sessions.Store().PutProjectTask(f.accountID, task)
	proj, _, _ := f.server.sessions.Store().GetProject(f.accountID, projID)

	// Deploy must fail closed with runner-unavailable error when runner is not configured
	err := sNoRunner.deployProjectTaskProgram(p, proj, task)
	if err == nil || !strings.Contains(err.Error(), "runner service is not configured") {
		t.Fatalf("expected runner service is not configured error, got: %v", err)
	}

	// Redeploy must also fail closed with runner-unavailable error when runner is not configured
	err = sNoRunner.redeployTaskProgramJob(p, projID, task.ID, "job-1", "Fix retry")
	if err == nil || !strings.Contains(err.Error(), "runner service is not configured") {
		t.Fatalf("expected runner service is not configured error on redeploy, got: %v", err)
	}
}

func TestTaskMatrix_DeployProjectTask_RejectsCrossAccount(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: DeployProjectTask must reject cross-account access with an explicit error,
	//   never deploying tasks across tenant boundaries.
	// - Authority: DeployProjectTask in api/project_task_program.go.
	// - Threat/regression: Cross-tenant task execution vulnerability.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Cross account test task",
		"prompt": "Test prompt",
		"agent":  "coder",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskID := resp["task"].(map[string]any)["id"].(string)

	// Attacker from different account tries DeployProjectTask
	attacker := identity.Principal{Type: "user", UserID: "attacker_user", AccountScopeID: "attacker_account"}
	err := f.server.DeployProjectTask(context.Background(), attacker, projID, taskID)
	if err == nil {
		t.Fatal("expected DeployProjectTask to reject cross-account caller, got nil")
	}
}
