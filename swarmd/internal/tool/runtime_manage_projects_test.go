package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type mockProjectStore struct {
	mu       sync.Mutex
	projects map[string]*pebblestore.ProjectRecord
	tasks    map[string]*pebblestore.ProjectTaskRecord
	plans    map[string]*pebblestore.SessionPlanSnapshot
}

func newMockProjectStore() *mockProjectStore {
	return &mockProjectStore{
		projects: make(map[string]*pebblestore.ProjectRecord),
		tasks:    make(map[string]*pebblestore.ProjectTaskRecord),
		plans:    make(map[string]*pebblestore.SessionPlanSnapshot),
	}
}

func (m *mockProjectStore) PutProject(accountScopeID string, proj *pebblestore.ProjectRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proj.ID == "" {
		proj.ID = "proj_test_1"
	}
	proj.AccountID = accountScopeID
	m.projects[proj.ID] = proj
	return nil
}

func (m *mockProjectStore) GetProject(accountScopeID, id string) (*pebblestore.ProjectRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.AccountID != accountScopeID {
		return nil, false, nil
	}
	return p, true, nil
}

func (m *mockProjectStore) ListProjects(accountScopeID string, limit int) ([]pebblestore.ProjectRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []pebblestore.ProjectRecord
	for _, p := range m.projects {
		if p.AccountID == accountScopeID {
			list = append(list, *p)
		}
	}
	return list, nil
}

func (m *mockProjectStore) DeleteProject(accountScopeID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projects, id)
	return nil
}

func (m *mockProjectStore) UpdateProject(accountScopeID, id string, mutate func(*pebblestore.ProjectRecord) error) (*pebblestore.ProjectRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.AccountID != accountScopeID {
		return nil, nil
	}
	if err := mutate(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (m *mockProjectStore) PutProjectTask(accountScopeID string, task *pebblestore.ProjectTaskRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task.ID == "" {
		task.ID = fmt.Sprintf("task_test_%d", len(m.tasks)+1)
	}
	task.AccountID = accountScopeID
	m.tasks[task.ID] = task
	return nil
}

func (m *mockProjectStore) GetProjectTask(accountScopeID, projectID, taskID string) (*pebblestore.ProjectTaskRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok || t.AccountID != accountScopeID || t.ProjectID != projectID {
		return nil, false, nil
	}
	return t, true, nil
}

func (m *mockProjectStore) ListProjectTasks(accountScopeID, projectID string, limit int) ([]pebblestore.ProjectTaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []pebblestore.ProjectTaskRecord
	for _, t := range m.tasks {
		if t.AccountID == accountScopeID && t.ProjectID == projectID {
			list = append(list, *t)
		}
	}
	return list, nil
}

func (m *mockProjectStore) UpdateProjectTask(accountScopeID, projectID, taskID string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok || t.AccountID != accountScopeID || t.ProjectID != projectID {
		return nil, nil
	}
	if err := mutate(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (m *mockProjectStore) DeleteProjectTask(accountScopeID, projectID, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, taskID)
	return nil
}

func (m *mockProjectStore) GetPlan(sessionID, planID string) (pebblestore.SessionPlanSnapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionID + ":" + planID
	p, ok := m.plans[key]
	if !ok {
		return pebblestore.SessionPlanSnapshot{}, false, nil
	}
	return *p, true, nil
}

func (m *mockProjectStore) PutPlan(plan pebblestore.SessionPlanSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := plan.SessionID + ":" + plan.ID
	m.plans[key] = &plan
	return nil
}

// mockProjectTaskLifecycleService implements ProjectTaskLifecycleService for testing.
type mockProjectTaskLifecycleService struct {
	mu             sync.Mutex
	deployedTasks  []string
	approvedTasks  []string
	submittedPlans []sessionruntime.ProjectTaskPlanSubmissionInput
	lastPrincipal  identity.Principal
	lastContext    context.Context
	failDeploy     error
	failApprove    error
	failSubmit     error
	store          *mockProjectStore
}

func newMockProjectTaskLifecycleService(store *mockProjectStore) *mockProjectTaskLifecycleService {
	return &mockProjectTaskLifecycleService{
		store: store,
	}
}

func (m *mockProjectTaskLifecycleService) DeployProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastContext = ctx
	m.lastPrincipal = p
	m.deployedTasks = append(m.deployedTasks, projectID+":"+taskID)
	if m.failDeploy != nil {
		return m.failDeploy
	}
	if m.store != nil {
		_, _ = m.store.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.SessionID = "sess_" + taskID
			t.WorktreeBranch = "agent/" + taskID
			t.ActionNeeded = ""
			return nil
		})
	}
	return nil
}

func (m *mockProjectTaskLifecycleService) ApproveProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, guards ...ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastContext = ctx
	m.lastPrincipal = p
	m.approvedTasks = append(m.approvedTasks, projectID+":"+taskID)
	if m.failApprove != nil {
		return nil, m.failApprove
	}
	if m.store != nil {
		task, found, _ := m.store.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if !found || task == nil {
			return nil, fmt.Errorf("task %q not found", taskID)
		}
		if task.Status == "rejected" {
			return nil, errors.New("cannot approve rejected task")
		}
		if len(guards) > 0 {
			g := guards[0]
			if g.SessionID != "" && task.SessionID != "" && g.SessionID != task.SessionID {
				return nil, fmt.Errorf("session ID mismatch: expected %q, got %q", task.SessionID, g.SessionID)
			}
			if g.PlanID != "" && task.PlanBinding != nil && g.PlanID != task.PlanBinding.PlanID {
				return nil, fmt.Errorf("plan ID mismatch: expected %q, got %q", task.PlanBinding.PlanID, g.PlanID)
			}
			if g.DefinitionRevision > 0 && task.PlanBinding != nil && g.DefinitionRevision != task.PlanBinding.DefinitionRevision {
				return nil, fmt.Errorf("plan definition is stale (task revision %d, current %d)", task.PlanBinding.DefinitionRevision, g.DefinitionRevision)
			}
		}
		if task.PlanBinding != nil && task.PlanBinding.DefinitionRevision > 1 {
			return nil, errors.New("plan definition is stale (task revision 2, current 1)")
		}
		task.Status = "in_progress"
		task.ActionNeeded = ""
		if task.PlanBinding != nil {
			task.Agent = "swarm"
			task.WorkerName = "@Swarm Worker"
		} else {
			task.SessionID = "sess_" + taskID
			task.WorktreeBranch = "agent/" + taskID
		}
		_ = m.store.PutProjectTask(p.AccountScopeID, task)
		return task, nil
	}
	return nil, errors.New("store not available")
}

func (m *mockProjectTaskLifecycleService) SubmitProjectTaskPlan(ctx context.Context, input sessionruntime.ProjectTaskPlanSubmissionInput) (sessionruntime.ProjectTaskPlanSubmissionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastContext = ctx
	m.lastPrincipal = identity.Principal{Type: "user", UserID: input.UserID, AccountScopeID: input.AccountScopeID}
	m.submittedPlans = append(m.submittedPlans, input)
	if m.failSubmit != nil {
		return sessionruntime.ProjectTaskPlanSubmissionResult{}, m.failSubmit
	}
	if m.store != nil {
		task, found, _ := m.store.GetProjectTask(input.AccountScopeID, input.ProjectID, input.TaskID)
		if found && task != nil {
			task.Status = "pending_approval"
			task.PlanBinding = &pebblestore.ProjectTaskPlanBinding{
				PlanID:             input.Document.ID,
				SessionID:          "sess_plan_" + input.TaskID,
				DefinitionRevision: 1,
				Receipt:            "receipt_test_abc123",
			}
			task.SessionID = "sess_plan_" + input.TaskID
			task.ActionNeeded = "Review plan in task card and click Approve"
			_ = m.store.PutProjectTask(input.AccountScopeID, task)
			return sessionruntime.ProjectTaskPlanSubmissionResult{
				Task:    *task,
				Receipt: "receipt_test_abc123",
			}, nil
		}
	}
	return sessionruntime.ProjectTaskPlanSubmissionResult{}, errors.New("task not found in store")
}

func TestManageProjectsToolExecutionAndIsolation(t *testing.T) {
	// Purpose:
	// - Invariant: manage_projects tool must support list/get/create/update/delete/synthesize_context,
	//   and be strictly isolated: available to orchestrator, excluded from primary swarm agent.
	// - Boundary/authority: Runtime.executeManageProjects in runtime_manage_projects.go.
	// - Threat/regression: Primary swarm agent getting bloated with raw project management tools,
	//   or orchestrator failing to manipulate project boundaries.

	rt := NewRuntime(1)
	mockStore := newMockProjectStore()
	mockLifecycle := newMockProjectTaskLifecycleService(mockStore)
	rt.SetManageProjectStore(mockStore)
	rt.SetProjectTaskLifecycleService(mockLifecycle)
	ctx := context.Background()

	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "acct_test",
		},
	}

	execTool := func(s WorkspaceScope, callID, argsJSON string) (string, error) {
		return rt.ExecuteForWorkspaceScopeWithRuntime(ctx, s, Call{
			CallID:    callID,
			Name:      "manage_projects",
			Arguments: argsJSON,
		})
	}

	// 1. Create project without account scope (fails closed)
	_, err := execTool(WorkspaceScope{}, "call-1", `{"action":"create","name":"Test Platform"}`)
	if err == nil {
		t.Fatal("expected error on empty account scope")
	}

	// 2. Create project with valid scope
	createArgs := `{
		"action": "create",
		"name": "Test Platform",
		"description": "Orchestrated test platform",
		"workspaces": [
			{"path": "/path/to/test", "role": "primary_code", "label": "Engine"}
		],
		"project_context": "# Test Platform\nContext"
	}`
	createOut, err := execTool(scope, "call-2", createArgs)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	var createResp map[string]any
	if err := json.Unmarshal([]byte(createOut), &createResp); err != nil {
		t.Fatal(err)
	}
	if createResp["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", createResp["status"])
	}

	// 3. List projects
	listOut, err := execTool(scope, "call-3", `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var listResp map[string]any
	if err := json.Unmarshal([]byte(listOut), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp["count"].(float64) != 1 {
		t.Fatalf("expected count 1, got %v", listResp["count"])
	}

	// 4. Synthesize context
	synthOut, err := execTool(scope, "call-4", `{
		"action": "synthesize_context",
		"name": "Synthesized Project",
		"workspaces": [{"path": "/nonexistent/repo"}]
	}`)
	if err != nil {
		t.Fatalf("synthesize_context failed: %v", err)
	}
	var synthResp map[string]any
	if err := json.Unmarshal([]byte(synthOut), &synthResp); err != nil {
		t.Fatal(err)
	}
	ctxText, _ := synthResp["synthesized_context"].(string)
	if ctxText == "" {
		t.Fatal("expected non-empty synthesized_context")
	}

	// 5. Create task for project
	createTaskOut, err := execTool(scope, "call-5", `{
		"action": "create_task",
		"project_id": "proj_test_1",
		"title": "First Project Task",
		"description": "Inspect and verify onboarding",
		"agent": "coder",
		"worker_name": "@Code Verifier"
	}`)
	if err != nil {
		t.Fatalf("create_task failed: %v", err)
	}
	var taskResp map[string]any
	if err := json.Unmarshal([]byte(createTaskOut), &taskResp); err != nil {
		t.Fatal(err)
	}
	if taskResp["status"] != "ok" || taskResp["task_id"] == "" {
		t.Fatalf("unexpected create_task response: %v", taskResp)
	}

	// 5b. Propose task with plain English prompt (Fallback to Swarm default with alert)
	propTaskOut, err := execTool(scope, "call-5b", `{
		"action": "propose_task",
		"project_id": "proj_test_1",
		"prompt": "Random unspecified task without keywords"
	}`)
	if err != nil {
		t.Fatalf("propose_task failed: %v", err)
	}
	var propTaskResp map[string]any
	if err := json.Unmarshal([]byte(propTaskOut), &propTaskResp); err != nil {
		t.Fatal(err)
	}
	propTask, _ := propTaskResp["task"].(map[string]any)
	if propTask["tier"] != "direct" {
		t.Fatalf("expected direct tier, got %v", propTask["tier"])
	}
	if propTask["agent"] != "swarm" {
		t.Fatalf("expected swarm agent, got %v", propTask["agent"])
	}
	if propTask["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval status, got %v", propTask["status"])
	}
	if propTask["router_alert"] == nil || propTask["router_alert"] == "" {
		t.Fatalf("expected router_alert on proposed task fallback")
	}

	// 5c. Propose task with Task Program (Multi-coder cohort execution)
	propProgOut, err := execTool(scope, "call-5c", `{
		"action": "propose_task",
		"project_id": "proj_test_1",
		"title": "Staged Multi-Coder Task",
		"task_program": {
			"id": "prog-orch-1",
			"stages": [{"id": "s1", "dependency_evidence": "none"}],
			"jobs": [{
				"id": "job-1",
				"stage_id": "s1",
				"agent_type": "coder",
				"title": "Backend",
				"meta_prompt": "Code",
				"deliverable": "api.go",
				"acceptance_criteria": ["ok"],
				"dependency_evidence": "none"
			}]
		}
	}`)
	if err != nil {
		t.Fatalf("propose_task with task_program failed: %v", err)
	}
	var propProgResp map[string]any
	if err := json.Unmarshal([]byte(propProgOut), &propProgResp); err != nil {
		t.Fatal(err)
	}
	progTask, _ := propProgResp["task"].(map[string]any)
	if progTask["task_program_id"] != "prog-orch-1" {
		t.Fatalf("expected task_program_id prog-orch-1, got %v", progTask["task_program_id"])
	}
	if progTask["task_program"] == nil {
		t.Fatal("expected non-nil task_program on task")
	}

	// 5d. Deploy task via deploy_task action
	progTaskID, _ := progTask["id"].(string)
	if progTaskID == "" {
		progTaskID = "task_test_3"
	}
	_, err = execTool(scope, "call-5d", fmt.Sprintf(`{
		"action": "deploy_task",
		"project_id": "proj_test_1",
		"task_id": %q
	}`, progTaskID))
	if err != nil {
		t.Fatalf("deploy_task failed: %v", err)
	}
	if len(mockLifecycle.deployedTasks) == 0 {
		t.Fatal("expected deployer invoked on deploy_task")
	}

	// 6. List tasks
	listTasksOut, err := execTool(scope, "call-6", `{"action":"list_tasks","project_id":"proj_test_1"}`)
	if err != nil {
		t.Fatalf("list_tasks failed: %v", err)
	}
	var listTasksResp map[string]any
	if err := json.Unmarshal([]byte(listTasksOut), &listTasksResp); err != nil {
		t.Fatal(err)
	}
	if listTasksResp["count"].(float64) < 1 {
		t.Fatalf("expected at least 1 task, got %v", listTasksResp["count"])
	}

	// 7. Update task
	updateTaskOut, err := execTool(scope, "call-7", fmt.Sprintf(`{
		"action": "update_task",
		"project_id": "proj_test_1",
		"task_id": %q,
		"status": "needs_review"
	}`, progTaskID))
	if err != nil {
		t.Fatalf("update_task failed: %v", err)
	}
	var updateTaskResp map[string]any
	if err := json.Unmarshal([]byte(updateTaskOut), &updateTaskResp); err != nil {
		t.Fatal(err)
	}
	taskObj, _ := updateTaskResp["task"].(map[string]any)
	if taskObj["status"] != "needs_review" {
		t.Fatalf("expected updated status needs_review, got %v", taskObj["status"])
	}

	// 7b. Refine task (Router refinement loop)
	refineTaskOut, err := execTool(scope, "call-7b", fmt.Sprintf(`{
		"action": "refine_task",
		"project_id": "proj_test_1",
		"task_id": %q,
		"feedback": "make sure it uses tailwind tokens only"
	}`, progTaskID))
	if err != nil {
		t.Fatalf("refine_task failed: %v", err)
	}
	var refineTaskResp map[string]any
	if err := json.Unmarshal([]byte(refineTaskOut), &refineTaskResp); err != nil {
		t.Fatal(err)
	}
	refinedObj, _ := refineTaskResp["task"].(map[string]any)
	if refinedObj["status"] != "pending_approval" {
		t.Fatalf("expected refined task status pending_approval, got %v", refinedObj["status"])
	}

	// 8. Delete project
	delOut, err := execTool(scope, "call-8", `{"action":"delete","id":"proj_test_1"}`)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	var delResp map[string]any
	if err := json.Unmarshal([]byte(delOut), &delResp); err != nil {
		t.Fatal(err)
	}
	if delResp["deleted"] != true {
		t.Fatalf("expected deleted=true, got %v", delResp["deleted"])
	}
}

func TestOrchestratorToolIsolationContract(t *testing.T) {
	// Purpose:
	// - Invariant: Primary swarm agent tool contract MUST explicitly disable manage_projects.
	//   Swarm Orchestrator agent contract MUST have manage_projects enabled, but omit raw media/environment tools.
	// - Boundary/authority: SwarmAgentToolContract / SwarmOrchestratorAgentToolContract in system_agent_registry.go.
	// - Threat/regression: Primary swarm agent getting bloated with raw project management tools,
	//   or orchestrator getting weighted down with raw byte manipulation tools.

	swarmTools := agentruntime.SwarmAgentToolContract()
	if swarmTools == nil || swarmTools.Tools == nil {
		t.Fatal("expected non-nil SwarmAgentToolContract")
	}
	if cfg, ok := swarmTools.Tools["manage_projects"]; ok && cfg.Enabled != nil && *cfg.Enabled {
		t.Fatalf("primary swarm agent must not have manage_projects enabled: %+v", cfg)
	}

	orchTools := agentruntime.SwarmOrchestratorAgentToolContract()
	if orchTools == nil || orchTools.Tools == nil {
		t.Fatal("expected non-nil SwarmOrchestratorAgentToolContract")
	}
	if cfg, ok := orchTools.Tools["manage_projects"]; !ok || cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("swarm orchestrator agent must have manage_projects enabled: %+v", cfg)
	}

	// Verify orchestrator excludes raw video/artifact manipulation and environment tools
	for _, excluded := range []string{"manage_video", "manage_artifact", "manage_environments", "manage_connections", "manage_actions", "manage_theme"} {
		if cfg, ok := orchTools.Tools[excluded]; ok && cfg.Enabled != nil && *cfg.Enabled {
			t.Fatalf("orchestrator must not have %s enabled: %+v", excluded, cfg)
		}
	}
}

func TestManageProjects_SingleTaskProgramCohortDeployment(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: An orchestrator can delegate a Task Program in a single task,
	//   supporting JSON string payloads, auto-synthesizing missing stages from jobs, auto-generating IDs,
	//   and auto-approving/deploying the task program cohort immediately.
	// - Regression prevented: Prevents regressions where single-task Task Programs fail to deploy or
	//   silently drop task_program definitions.
	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)

	_ = db.PutProject("account", &pebblestore.ProjectRecord{
		ID:        "proj_single_tp",
		AccountID: "account",
		Name:      "Single TP Project",
	})

	ctx := context.Background()
	execTool := func(s WorkspaceScope, callID, argsJSON string) (string, error) {
		return rt.ExecuteForWorkspaceScopeWithRuntime(ctx, s, Call{
			CallID:    callID,
			Name:      "manage_projects",
			Arguments: argsJSON,
		})
	}

	// 1. Propose task with auto_approve: true and JSON string task_program lacking explicit stages
	toolOut, err := execTool(scope, "call-single-tp", `{
		"action": "propose_task",
		"project_id": "proj_single_tp",
		"title": "Autonomous Single Task Program",
		"auto_approve": true,
		"task_program": "{\"jobs\": [{\"id\": \"single-coder-1\", \"title\": \"Implement core\", \"prompt\": \"Write code\", \"agent\": \"coder\", \"deliverable\": \"main.go\"}]}"
	}`)
	if err != nil {
		t.Fatalf("propose_task with string task_program failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(toolOut), &resp); err != nil {
		t.Fatal(err)
	}
	task, _ := resp["task"].(map[string]any)
	if task["status"] != "in_progress" {
		t.Fatalf("expected task status in_progress, got %v", task["status"])
	}
	if task["task_program_id"] == "" {
		t.Fatal("expected auto-generated task_program_id")
	}
	if task["task_program"] == nil {
		t.Fatal("expected parsed task_program")
	}
	if len(lifecycle.deployedTasks) != 1 {
		t.Fatalf("expected exactly 1 deploy call on auto_approve, got %d", len(lifecycle.deployedTasks))
	}
}

func TestManageProjects_ExplicitCoderTaskWithMediaKeywords(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Proposing a task with explicit agent="coder"
	//   that mentions media terms (PNG, image, media) must NOT route to image generation
	//   or media deliverables. It must create a coder task with code PR deliverable.
	// - Regression prevented: Prevents regressions where Orchestrator task proposals
	//   get corrupted by keyword matching into media generation tasks.

	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	rt := &Runtime{}
	rt.SetManageProjectStore(db)

	_ = db.PutProject("account", &pebblestore.ProjectRecord{
		ID:        "proj_media_kw",
		AccountID: "account",
		Name:      "Media KW Project",
	})

	ctx := context.Background()
	toolOut, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-media-kw",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_media_kw",
			"title": "Add profile PNG upload button and image selection from media",
			"agent": "coder"
		}`,
	})
	if err != nil {
		t.Fatalf("propose_task failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(toolOut), &resp); err != nil {
		t.Fatal(err)
	}
	task, _ := resp["task"].(map[string]any)
	if task["agent"] != "coder" {
		t.Fatalf("expected agent coder, got %v", task["agent"])
	}
	if task["outcome_type"] != "code_pr" {
		t.Fatalf("expected outcome_type code_pr, got %v", task["outcome_type"])
	}
	if task["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval status, got %v", task["status"])
	}
	delivs, _ := task["deliverables"].([]any)
	if len(delivs) == 0 {
		t.Fatal("expected deliverables")
	}
	firstDeliv := delivs[0].(map[string]any)
	if firstDeliv["kind"] == "image" || firstDeliv["kind"] == "video" || firstDeliv["kind"] == "audio" {
		t.Fatalf("coder task must not have media deliverable, got kind %v", firstDeliv["kind"])
	}
}

func TestManageProjects_RefinementStabilityPreservesAgent(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Refining a task must preserve explicit agent and outcome_type
	//   unless explicitly requested to change; it must not overwrite them with keyword heuristics.

	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	rt := &Runtime{}
	rt.SetManageProjectStore(db)

	_ = db.PutProject("account", &pebblestore.ProjectRecord{
		ID:        "proj_refine_stab",
		AccountID: "account",
		Name:      "Refine Stability Project",
	})
	_ = db.PutProjectTask("account", &pebblestore.ProjectTaskRecord{
		ID:          "task_refine_1",
		ProjectID:   "proj_refine_stab",
		Title:       "Implement authentication middleware",
		Agent:       "coder",
		OutcomeType: "code_pr",
		Status:      "pending_approval",
	})

	ctx := context.Background()
	toolOut, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-refine",
		Name:   "manage_projects",
		Arguments: `{
			"action": "refine_task",
			"project_id": "proj_refine_stab",
			"task_id": "task_refine_1",
			"feedback": "Make sure to also include a SVG test diagram in docs"
		}`,
	})
	if err != nil {
		t.Fatalf("refine_task failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(toolOut), &resp); err != nil {
		t.Fatal(err)
	}
	task, _ := resp["task"].(map[string]any)
	if task["agent"] != "coder" {
		t.Fatalf("expected agent coder preserved across refinement, got %v", task["agent"])
	}
	if task["outcome_type"] != "code_pr" {
		t.Fatalf("expected outcome_type code_pr preserved across refinement, got %v", task["outcome_type"])
	}
}

func TestManageProjects_FeatureSizeRouting(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: feature_size="small" routes to coder; feature_size="big" routes to plan agent.

	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	rt := &Runtime{}
	rt.SetManageProjectStore(db)

	_ = db.PutProject("account", &pebblestore.ProjectRecord{
		ID:        "proj_feat_size",
		AccountID: "account",
		Name:      "Feature Size Project",
	})

	ctx := context.Background()
	// Small feature
	outSmall, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-small",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_feat_size",
			"title": "Minor styling adjustment",
			"intent": "code",
			"feature_size": "small"
		}`,
	})
	if err != nil {
		t.Fatalf("small feature failed: %v", err)
	}
	var respSmall map[string]any
	_ = json.Unmarshal([]byte(outSmall), &respSmall)
	taskSmall := respSmall["task"].(map[string]any)
	if taskSmall["agent"] != "coder" {
		t.Fatalf("expected small feature to route to coder, got %v", taskSmall["agent"])
	}

	// Big feature
	outBig, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-big",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_feat_size",
			"title": "Architecture overhaul",
			"intent": "code",
			"feature_size": "big"
		}`,
	})
	if err != nil {
		t.Fatalf("big feature failed: %v", err)
	}
	var respBig map[string]any
	_ = json.Unmarshal([]byte(outBig), &respBig)
	taskBig := respBig["task"].(map[string]any)
	if taskBig["agent"] != "plan" {
		t.Fatalf("expected big feature to route to plan, got %v", taskBig["agent"])
	}
	if taskBig["tier"] != "complex" {
		t.Fatalf("expected big feature tier complex, got %v", taskBig["tier"])
	}
}

func TestManageProjects_DeployCompletedIdempotent(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: A completed task returns already_completed without re-deploying.
	//   An in_progress task with a dormant session forwards to canonical deployer to wake/reconcile the session,
	//   rather than falsely equating in_progress status with active deployment.
	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
			UserID:         "user_test_1",
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)

	_ = db.PutProject("account", &pebblestore.ProjectRecord{
		ID:        "proj_comp_idemp",
		AccountID: "account",
		Name:      "Completed Idempotency Project",
	})
	_ = db.PutProjectTask("account", &pebblestore.ProjectTaskRecord{
		ID:          "task_comp_1",
		ProjectID:   "proj_comp_idemp",
		Title:       "Completed task",
		Agent:       "coder",
		OutcomeType: "code_pr",
		Status:      "completed",
	})

	ctx := context.Background()
	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-comp-1",
		Name:   "manage_projects",
		Arguments: `{
			"action": "deploy_task",
			"project_id": "proj_comp_idemp",
			"task_id": "task_comp_1"
		}`,
	})
	if err != nil {
		t.Fatalf("deploy_task on completed failed: %v", err)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(out), &resp)
	if resp["status"] != "already_completed" {
		t.Fatalf("expected already_completed, got %v", resp["status"])
	}
	if len(lifecycle.deployedTasks) != 0 {
		t.Fatalf("deployer must not be invoked on completed task, got %d", len(lifecycle.deployedTasks))
	}
}

// -----------------------------------------------------------------------------
// Requirement-First Matrix Tests
// -----------------------------------------------------------------------------

func TestManageProjects_PrincipalForwarding(t *testing.T) {
	// Written test purpose:
	// - Invariant: manage_projects MUST forward the authentic user's full identity.Principal
	//   (Type="user", UserID, AccountScopeID) and Context to all lifecycle operations
	//   (Deploy, Approve, SubmitPlan).
	// - Authority: executeManageProjects in runtime_manage_projects.go.
	// - Threat/regression: "deploy task: user id is required" caused by dropping principal identity or context.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_fwd_999",
		AccountScopeID: "acct_fwd_123",
	}
	scope := WorkspaceScope{Principal: p}
	ctx := context.Background()

	_ = db.PutProject("acct_fwd_123", &pebblestore.ProjectRecord{
		ID:        "proj_fwd_1",
		AccountID: "acct_fwd_123",
		Name:      "Forwarding Project",
	})

	// 1. Propose task with auto_approve -> verifies Principal on Deploy
	outDeploy, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-fwd-deploy",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_fwd_1",
			"title": "Small Auto Task",
			"agent": "coder",
			"auto_approve": true
		}`,
	})
	if err != nil {
		t.Fatalf("propose_task with auto_approve failed: %v", err)
	}
	var respDeploy map[string]any
	_ = json.Unmarshal([]byte(outDeploy), &respDeploy)
	if respDeploy["status"] != "ok" {
		t.Fatalf("expected ok, got %v", respDeploy["status"])
	}

	lifecycle.mu.Lock()
	if lifecycle.lastPrincipal.Type != "user" || lifecycle.lastPrincipal.UserID != "user_fwd_999" || lifecycle.lastPrincipal.AccountScopeID != "acct_fwd_123" {
		t.Fatalf("deploy did not receive full principal: %+v", lifecycle.lastPrincipal)
	}
	if lifecycle.lastContext == nil {
		t.Fatal("deploy did not receive non-nil context")
	}
	lifecycle.mu.Unlock()

	// 2. Call approve_task -> verifies Principal on Approve
	_ = db.PutProjectTask("acct_fwd_123", &pebblestore.ProjectTaskRecord{
		ID:          "task_fwd_approve",
		ProjectID:   "proj_fwd_1",
		Title:       "Task to approve",
		Agent:       "coder",
		Status:      "pending_approval",
	})
	outApprove, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-fwd-approve",
		Name:   "manage_projects",
		Arguments: `{
			"action": "approve_task",
			"project_id": "proj_fwd_1",
			"task_id": "task_fwd_approve"
		}`,
	})
	if err != nil {
		t.Fatalf("approve_task failed: %v", err)
	}
	var respApprove map[string]any
	_ = json.Unmarshal([]byte(outApprove), &respApprove)
	if respApprove["status"] != "in_progress" {
		t.Fatalf("expected in_progress, got %v", respApprove["status"])
	}

	lifecycle.mu.Lock()
	if lifecycle.lastPrincipal.Type != "user" || lifecycle.lastPrincipal.UserID != "user_fwd_999" || lifecycle.lastPrincipal.AccountScopeID != "acct_fwd_123" {
		t.Fatalf("approve did not receive full principal: %+v", lifecycle.lastPrincipal)
	}
	lifecycle.mu.Unlock()

	// 3. Propose big feature with plan_document -> verifies Principal on SubmitProjectTaskPlan
	outPlan, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-fwd-plan",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_fwd_1",
			"title": "Big Feature with Structured Plan",
			"plan_document": {
				"id": "plan-fwd-01",
				"title": "Auth Architecture Plan",
				"info": {"goal": "Modernize authentication"},
				"checkpoints": [{"id": "cp-1", "title": "OAuth 2.1", "tasks": ["implement PKCE"]}]
			}
		}`,
	})
	if err != nil {
		t.Fatalf("propose_task with direct plan failed: %v", err)
	}
	var respPlan map[string]any
	_ = json.Unmarshal([]byte(outPlan), &respPlan)
	if respPlan["status"] != "ok" {
		t.Fatalf("expected ok, got %v", respPlan["status"])
	}

	lifecycle.mu.Lock()
	if len(lifecycle.submittedPlans) != 1 {
		t.Fatalf("expected 1 submitted plan, got %d", len(lifecycle.submittedPlans))
	}
	subPlan := lifecycle.submittedPlans[0]
	if subPlan.UserID != "user_fwd_999" || subPlan.AccountScopeID != "acct_fwd_123" {
		t.Fatalf("plan submission did not receive authentic identity: user=%q acct=%q", subPlan.UserID, subPlan.AccountScopeID)
	}
	lifecycle.mu.Unlock()
}

func TestManageProjects_MissingIdentityRejectionNoStateMutation(t *testing.T) {
	// Written test purpose:
	// - Invariant: If scope lacks valid authenticated user identity (empty UserID, empty AccountScopeID,
	//   or non-user Type), the tool MUST fail closed immediately and MUST NOT create or mutate
	//   any project or task state in the store.
	// - Authority: executeManageProjects in runtime_manage_projects.go.
	// - Threat/regression: Unauthenticated execution, data leakage, or corrupted orphan task records.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)
	ctx := context.Background()

	testCases := []struct {
		name      string
		principal identity.Principal
	}{
		{
			name: "missing UserID",
			principal: identity.Principal{
				Type:           "user",
				AccountScopeID: "acct_valid",
			},
		},
		{
			name: "missing AccountScopeID",
			principal: identity.Principal{
				Type:   "user",
				UserID: "user_valid",
			},
		},
		{
			name: "wrong type service",
			principal: identity.Principal{
				Type:           "service",
				UserID:         "user_valid",
				AccountScopeID: "acct_valid",
			},
		},
		{
			name:      "completely empty",
			principal: identity.Principal{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scope := WorkspaceScope{Principal: tc.principal}
			// Attempt to create a project
			_, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
				CallID: "call-fail-create",
				Name:   "manage_projects",
				Arguments: `{
					"action": "create",
					"name": "Should Never Be Created"
				}`,
			})
			if err == nil {
				t.Fatal("expected failure on unauthenticated principal")
			}
			if !strings.Contains(err.Error(), "authenticated user identity") {
				t.Fatalf("expected 'authenticated user identity' error, got: %v", err)
			}

			// Verify NO state was mutated in the store
			db.mu.Lock()
			projCount := len(db.projects)
			taskCount := len(db.tasks)
			db.mu.Unlock()
			if projCount != 0 || taskCount != 0 {
				t.Fatalf("state mutated despite unauthenticated principal! projs=%d tasks=%d", projCount, taskCount)
			}
		})
	}
}

func TestManageProjects_DirectPlanSubmission(t *testing.T) {
	// Written test purpose:
	// - Invariant: Orchestrator submitting a direct structured plan document creates the task in
	//   pending_approval with PlanBinding via the backend pending-review service, WITHOUT starting
	//   any implementation run or extra Plan agent run, even if auto_approve is requested.
	// - Authority: executeManageProjects (action="propose_task" with plan_document).
	// - Threat/regression: Implementing before approval or starting redundant Plan agent runs when plan is already authored.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)
	ctx := context.Background()

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_orch_1",
		AccountScopeID: "acct_orch_1",
	}
	scope := WorkspaceScope{Principal: p}

	_ = db.PutProject("acct_orch_1", &pebblestore.ProjectRecord{
		ID:        "proj_direct_plan",
		AccountID: "acct_orch_1",
		Name:      "Direct Plan Project",
	})

	// 1. Propose task with direct structured plan
	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-direct-plan-1",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_direct_plan",
			"title": "Direct Orchestrator Billing Plan",
			"plan_document": {
				"id": "plan-billing-direct",
				"title": "Billing System Architecture",
				"info": {"goal": "Directly authored billing flow"},
				"checkpoints": [
					{"id": "cp-1", "title": "Stripe Webhook", "tasks": ["handle charge.succeeded"]},
					{"id": "cp-2", "title": "Invoice Sync", "tasks": ["reconcile invoice items"]}
				]
			}
		}`,
	})
	if err != nil {
		t.Fatalf("propose_task with plan_document failed: %v", err)
	}

	var resp map[string]any
	_ = json.Unmarshal([]byte(out), &resp)
	taskMap, _ := resp["task"].(map[string]any)

	// Invariant: Status must be pending_approval
	if taskMap["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval status, got: %v", taskMap["status"])
	}
	// Invariant: Plan binding must be attached
	pb, _ := taskMap["plan_binding"].(map[string]any)
	if pb == nil || pb["plan_id"] != "plan-billing-direct" {
		t.Fatalf("expected plan_binding for plan-billing-direct, got: %#v", pb)
	}
	// Invariant: Action needed prompts card review
	actionNeeded, _ := taskMap["action_needed"].(string)
	if !strings.Contains(actionNeeded, "task card") && !strings.Contains(actionNeeded, "Approve") {
		t.Fatalf("expected card review action_needed, got: %q", actionNeeded)
	}

	// Invariant: No deployment was executed before approval
	lifecycle.mu.Lock()
	if len(lifecycle.deployedTasks) != 0 {
		t.Fatalf("direct plan task must NOT deploy before approval, got: %v", lifecycle.deployedTasks)
	}
	lifecycle.mu.Unlock()

	// 2. Propose with auto_approve: true + direct plan_document
	// Invariant: Auto-approve must NOT approve unseen structured plan!
	outAuto, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-direct-plan-auto",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_direct_plan",
			"title": "Direct Plan Auto Attempt",
			"auto_approve": true,
			"plan_document": {
				"id": "plan-auto-guard",
				"title": "Auto Guard Plan",
				"info": {"goal": "Verify auto_approve does not bypass review"},
				"checkpoints": [{"id": "cp-1", "title": "Check", "tasks": ["guard"]}]
			}
		}`,
	})
	if err != nil {
		t.Fatalf("propose_task with auto_approve and plan failed: %v", err)
	}
	var respAuto map[string]any
	_ = json.Unmarshal([]byte(outAuto), &respAuto)
	taskAuto, _ := respAuto["task"].(map[string]any)
	if taskAuto["status"] != "pending_approval" {
		t.Fatalf("auto_approve must NOT set in_progress on structured plan, status was: %v", taskAuto["status"])
	}
}

func TestManageProjects_DirectPlanInvalidDocumentGuards(t *testing.T) {
	// Written test purpose:
	// - Invariant: An invalid plan document (missing checkpoints, empty goal) must be rejected
	//   with validation error without submitting or mutating durable state.
	// - Authority: parseSessionPlanDocument in runtime_manage_projects.go.
	// - Threat/regression: Corrupted plan objects entering task card review.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)
	ctx := context.Background()

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_test_guard",
		AccountScopeID: "acct_guard",
	}
	scope := WorkspaceScope{Principal: p}

	_ = db.PutProject("acct_guard", &pebblestore.ProjectRecord{
		ID:        "proj_guard",
		AccountID: "acct_guard",
		Name:      "Guard Project",
	})

	// Plan without checkpoints fails validation
	_, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-bad-plan",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_guard",
			"title": "Bad Plan Task",
			"plan_document": {
				"id": "plan-empty-cps",
				"title": "Incomplete Plan",
				"info": {"goal": "Missing checkpoints"}
			}
		}`,
	})
	if err == nil {
		t.Fatal("expected failure on plan without checkpoints")
	}
	if !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("expected checkpoint validation error, got: %v", err)
	}

	lifecycle.mu.Lock()
	if len(lifecycle.submittedPlans) != 0 {
		t.Fatalf("invalid plan must not be submitted, got %d submissions", len(lifecycle.submittedPlans))
	}
	lifecycle.mu.Unlock()
}

func TestManageProjects_ApproveTaskGuardsAndStaleRevisions(t *testing.T) {
	// Written test purpose:
	// - Invariant: approve_task must validate that lifecycle service handles guarded acceptance,
	//   rejecting stale plan definitions, rejected tasks, and missing lifecycle service.
	// - Authority: executeManageProjects (action="approve_task").
	// - Threat/regression: Approving stale/rejected tasks or plans without proper lifecycle guards.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)
	ctx := context.Background()

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_app_guard",
		AccountScopeID: "acct_app_guard",
	}
	scope := WorkspaceScope{Principal: p}

	_ = db.PutProject("acct_app_guard", &pebblestore.ProjectRecord{
		ID:        "proj_app_guard",
		AccountID: "acct_app_guard",
		Name:      "Approve Guard Project",
	})

	// 1. Rejected task approval fails
	_ = db.PutProjectTask("acct_app_guard", &pebblestore.ProjectTaskRecord{
		ID:          "task_rejected_1",
		ProjectID:   "proj_app_guard",
		Title:       "Rejected Task",
		Status:      "rejected",
	})
	_, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-app-rej",
		Name:   "manage_projects",
		Arguments: `{
			"action": "approve_task",
			"project_id": "proj_app_guard",
			"task_id": "task_rejected_1"
		}`,
	})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected rejected task error, got: %v", err)
	}

	// 2. Stale plan definition revision fails
	_ = db.PutProjectTask("acct_app_guard", &pebblestore.ProjectTaskRecord{
		ID:          "task_stale_plan",
		ProjectID:   "proj_app_guard",
		Title:       "Stale Plan Task",
		Status:      "pending_approval",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			PlanID:             "plan-stale",
			DefinitionRevision: 2,
		},
	})
	_, err = rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-app-stale",
		Name:   "manage_projects",
		Arguments: `{
			"action": "approve_task",
			"project_id": "proj_app_guard",
			"task_id": "task_stale_plan"
		}`,
	})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale plan definition error, got: %v", err)
	}

	// 3. Normal task approval succeeds and transitions to in_progress with Swarm agent
	_ = db.PutProjectTask("acct_app_guard", &pebblestore.ProjectTaskRecord{
		ID:          "task_valid_plan",
		ProjectID:   "proj_app_guard",
		Title:       "Valid Plan Task",
		Status:      "pending_approval",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			PlanID:             "plan-valid",
			DefinitionRevision: 1,
		},
	})
	outOk, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-app-ok",
		Name:   "manage_projects",
		Arguments: `{
			"action": "approve_task",
			"project_id": "proj_app_guard",
			"task_id": "task_valid_plan"
		}`,
	})
	if err != nil {
		t.Fatalf("valid approve_task failed: %v", err)
	}
	var respOk map[string]any
	_ = json.Unmarshal([]byte(outOk), &respOk)
	if respOk["status"] != "in_progress" {
		t.Fatalf("expected status in_progress after approval, got: %v", respOk["status"])
	}
	taskOk, _ := respOk["task"].(map[string]any)
	if taskOk["agent"] != "swarm" {
		t.Fatalf("expected swarm agent executing approved plan, got: %v", taskOk["agent"])
	}
}

func TestManageProjects_AbsentDeployerFailsClosed(t *testing.T) {
	// Written test purpose:
	// - Invariant: If deployer or lifecycle service is not configured, any action requiring deployment
	//   (deploy_task, approve_task, or auto_approve on create_task) MUST fail closed with an explicit
	//   error and MUST NOT pre-mark the task as in_progress in the store or hide the absent deployer.
	// - Authority: executeManageProjects in runtime_manage_projects.go.
	// - Threat/regression: Tool falsely claiming deployment when no runner was invoked.

	db := newMockProjectStore()
	// Deliberately do NOT configure lifecycle service or deployer!
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	ctx := context.Background()

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_absent",
		AccountScopeID: "acct_absent",
	}
	scope := WorkspaceScope{Principal: p}

	_ = db.PutProject("acct_absent", &pebblestore.ProjectRecord{
		ID:        "proj_absent",
		AccountID: "acct_absent",
		Name:      "Absent Deployer Project",
	})
	_ = db.PutProjectTask("acct_absent", &pebblestore.ProjectTaskRecord{
		ID:        "task_absent_1",
		ProjectID: "proj_absent",
		Title:     "Pending Task",
		Status:    "pending_approval",
	})

	// 1. deploy_task fails closed
	_, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-dep-absent",
		Name:   "manage_projects",
		Arguments: `{
			"action": "deploy_task",
			"project_id": "proj_absent",
			"task_id": "task_absent_1"
		}`,
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected 'not configured' error on absent deployer, got: %v", err)
	}

	// 2. approve_task fails closed
	_, err = rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-app-absent",
		Name:   "manage_projects",
		Arguments: `{
			"action": "approve_task",
			"project_id": "proj_absent",
			"task_id": "task_absent_1"
		}`,
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected 'not configured' error on absent lifecycle, got: %v", err)
	}

	// 3. propose_task with auto_approve: true fails closed AND does NOT premark in_progress!
	_, err = rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-prop-absent-auto",
		Name:   "manage_projects",
		Arguments: `{
			"action": "propose_task",
			"project_id": "proj_absent",
			"title": "Auto Approve Without Deployer",
			"agent": "coder",
			"auto_approve": true
		}`,
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected 'not configured' error on auto_approve without deployer, got: %v", err)
	}

	// Invariant: The task record in DB must NOT be in_progress
	tasks, _ := db.ListProjectTasks("acct_absent", "proj_absent", 10)
	for _, tsk := range tasks {
		if tsk.Title == "Auto Approve Without Deployer" && tsk.Status == "in_progress" {
			t.Fatal("task was falsely pre-marked in_progress despite absent deployer failure!")
		}
	}
}

func TestManageProjects_TruthfulAuthoritativeOutputs(t *testing.T) {
	// Written test purpose:
	// - Invariant: Tool responses must expose authoritative durable links (session_id,
	//   worktree_branch, workspace_path, plan_binding, task_program_id, revision, last_error)
	//   freshly fetched from the store, and deploy_task must not skip in_progress tasks with dormant sessions.
	// - Authority: executeManageProjects in runtime_manage_projects.go.
	// - Threat/regression: Returning stale data or skipping dormant session recovery.

	db := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(db)
	rt := &Runtime{}
	rt.SetManageProjectStore(db)
	rt.SetProjectTaskLifecycleService(lifecycle)
	ctx := context.Background()

	p := identity.Principal{
		Type:           "user",
		UserID:         "user_truth",
		AccountScopeID: "acct_truth",
	}
	scope := WorkspaceScope{Principal: p}

	_ = db.PutProject("acct_truth", &pebblestore.ProjectRecord{
		ID:        "proj_truth",
		AccountID: "acct_truth",
		Name:      "Truthful Outputs Project",
	})
	_ = db.PutProjectTask("acct_truth", &pebblestore.ProjectTaskRecord{
		ID:             "task_truth_1",
		ProjectID:      "proj_truth",
		Title:          "Small Coder Task",
		Agent:          "coder",
		Status:         "pending_approval",
		WorkspacePath:  "/workspaces/engine",
		WorktreeBranch: "agent/task-truth-1",
	})

	// 1. Deploy task
	outDeploy, err := rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-deploy-truth",
		Name:   "manage_projects",
		Arguments: `{
			"action": "deploy_task",
			"project_id": "proj_truth",
			"task_id": "task_truth_1"
		}`,
	})
	if err != nil {
		t.Fatalf("deploy_task failed: %v", err)
	}
	var respDeploy map[string]any
	_ = json.Unmarshal([]byte(outDeploy), &respDeploy)

	// Invariant: Response exposes truthful links
	if respDeploy["session_id"] == "" || respDeploy["session_id"] == nil {
		t.Fatal("expected non-empty session_id in response")
	}
	if respDeploy["worktree_branch"] == "" || respDeploy["worktree_branch"] == nil {
		t.Fatal("expected non-empty worktree_branch in response")
	}

	// 2. Equate status with deployed: Deploying an in_progress task again MUST NOT silently skip!
	// It must invoke DeployProjectTask to awaken or reconcile dormant sessions.
	initialDeployCount := len(lifecycle.deployedTasks)
	_, err = rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "call-deploy-dormant",
		Name:   "manage_projects",
		Arguments: `{
			"action": "deploy_task",
			"project_id": "proj_truth",
			"task_id": "task_truth_1"
		}`,
	})
	if err != nil {
		t.Fatalf("re-deploy failed: %v", err)
	}
	if len(lifecycle.deployedTasks) <= initialDeployCount {
		t.Fatal("deploy_task falsely equated status with deployed and skipped dormant session deployer invocation!")
	}
}
