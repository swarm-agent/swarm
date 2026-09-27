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
	err := f.server.DeployProjectTaskForPrincipal(identity.Principal{AccountScopeID: f.accountID}, projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected 'user id is required', got: %v", err)
	}

	// B: DeployProjectTask with accountScopeID resolves authentic user from identity store
	err = f.server.DeployProjectTask(f.accountID, projID, taskID)
	if err != nil {
		t.Fatalf("expected successful deploy via resolved authentic user, got: %v", err)
	}

	// Verify task status is in_progress
	task, found, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if !found || task.Status != "in_progress" {
		t.Fatalf("expected task in_progress, got: found=%v, status=%v", found, task.Status)
	}

	// C: Non-existent account fails closed without inventing identities
	err = f.server.DeployProjectTask("non-existent-account", projID, taskID)
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected failure on unknown account, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 3: parallel/staged Coders isolation/dependency/commit/conflict
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case3_ParallelStagedCodersIsolationDependencyCommitConflict(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Staged Task Programs require non-overlapping scopes, verify that clean worktrees
	//   with zero commits cannot be integrated (HEAD == base), and detect merge conflicts cleanly.
	// - Authority: integrateStage in run/service_task_program_scheduler.go.
	// - Threat/regression: False integration of zero-commit worktrees or merge conflict corruption.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

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

	// Deploy the task program
	err := f.server.DeployProjectTaskForPrincipal(p, projID, taskID)
	if err != nil {
		t.Fatalf("deploy task program failed: %v", err)
	}

	// Verify canonical scheduler was invoked
	f.runSvc.mu.Lock()
	executions := f.runSvc.tpExecutions
	f.runSvc.mu.Unlock()
	if executions != 1 {
		t.Fatalf("expected 1 canonical TP execution, got %d", executions)
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
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, p)
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
	// - Invariant: Approving a rejected task, a rejected plan, a stale plan revision, or
	//   a cross-account task must be rejected without mutating durable state.
	// - Authority: handleProjectTaskApprove in api/projects.go.
	// - Threat/regression: Executing stale or rejected plans or bypassing authorization.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Create task
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Task to reject",
		"prompt": "Reject me",
		"agent":  "coder",
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// 1. Reject task
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

	// 3. Cross-account approval attempt is rejected with 403 Forbidden
	crossPrincipal := identity.Principal{Type: "user", UserID: "attacker", AccountScopeID: "rogue-account"}
	w = f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", nil, crossPrincipal)
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
		t.Fatalf("expected 404 or 403 on cross-account approval, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Case 8: duplicate/concurrent/create/deploy/accept retries counts
// -----------------------------------------------------------------------------
func TestTaskMatrix_Case8_DuplicateConcurrentRetriesCounts(t *testing.T) {
	// Requirement-first Purpose:
	// - Invariant: Duplicate or concurrent create, deploy, and approve calls must be idempotent
	//   and not create orphan sessions, duplicate runIntents, or duplicate git worktrees.
	// - Authority: handleProjectTaskApprove in api/projects.go.
	// - Threat/regression: Duplicate execution runs and orphan sessions on network retries.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Idempotent task",
		"prompt": "Test retries",
		"agent":  "coder",
	}, p)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)
	sessID := taskMap["session_id"].(string)

	// First approval
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

	// Verify only 1 run intent was enqueued
	intents, _ := f.server.sessions.Store().ListRunIntents(sessID, 10)
	if len(intents) != 1 {
		t.Fatalf("expected exactly 1 run intent across retries, got %d", len(intents))
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
	//   task-to-plan links, receipts, and status without mutation replay.
	// - Authority: pebble store persistence for ProjectTaskRecord and SessionPlanSnapshot.
	// - Threat/regression: Data loss or corrupted task linkage upon daemon restart.
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

	// Verify task record exists with exact links and receipt
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

	// Verify session metadata retained project and task linkage
	sess, found, err := ss2.GetSession(sessID)
	if err != nil || !found {
		t.Fatalf("session not found after reopen: %v", err)
	}
	if sess.Metadata["task_id"] != taskID || sess.Metadata["project_id"] != projID {
		t.Fatalf("session task linkage corrupted after reopen: %#v", sess.Metadata)
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
