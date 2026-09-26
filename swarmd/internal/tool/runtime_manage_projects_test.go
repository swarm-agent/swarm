package tool

import (
	"context"
	"encoding/json"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type mockProjectStore struct {
	projects map[string]*pebblestore.ProjectRecord
	tasks    map[string]*pebblestore.ProjectTaskRecord
}

func newMockProjectStore() *mockProjectStore {
	return &mockProjectStore{
		projects: make(map[string]*pebblestore.ProjectRecord),
		tasks:    make(map[string]*pebblestore.ProjectTaskRecord),
	}
}

func (m *mockProjectStore) PutProject(accountScopeID string, proj *pebblestore.ProjectRecord) error {
	if proj.ID == "" {
		proj.ID = "proj_test_1"
	}
	proj.AccountID = accountScopeID
	m.projects[proj.ID] = proj
	return nil
}

func (m *mockProjectStore) GetProject(accountScopeID, id string) (*pebblestore.ProjectRecord, bool, error) {
	p, ok := m.projects[id]
	if !ok || p.AccountID != accountScopeID {
		return nil, false, nil
	}
	return p, true, nil
}

func (m *mockProjectStore) ListProjects(accountScopeID string, limit int) ([]pebblestore.ProjectRecord, error) {
	var list []pebblestore.ProjectRecord
	for _, p := range m.projects {
		if p.AccountID == accountScopeID {
			list = append(list, *p)
		}
	}
	return list, nil
}

func (m *mockProjectStore) DeleteProject(accountScopeID, id string) error {
	delete(m.projects, id)
	return nil
}

func (m *mockProjectStore) UpdateProject(accountScopeID, id string, mutate func(*pebblestore.ProjectRecord) error) (*pebblestore.ProjectRecord, error) {
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
	if task.ID == "" {
		task.ID = "task_test_1"
	}
	task.AccountID = accountScopeID
	m.tasks[task.ID] = task
	return nil
}

func (m *mockProjectStore) GetProjectTask(accountScopeID, projectID, taskID string) (*pebblestore.ProjectTaskRecord, bool, error) {
	t, ok := m.tasks[taskID]
	if !ok || t.AccountID != accountScopeID || t.ProjectID != projectID {
		return nil, false, nil
	}
	return t, true, nil
}

func (m *mockProjectStore) ListProjectTasks(accountScopeID, projectID string, limit int) ([]pebblestore.ProjectTaskRecord, error) {
	var list []pebblestore.ProjectTaskRecord
	for _, t := range m.tasks {
		if t.AccountID == accountScopeID && t.ProjectID == projectID {
			list = append(list, *t)
		}
	}
	return list, nil
}

func (m *mockProjectStore) UpdateProjectTask(accountScopeID, projectID, taskID string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error) {
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
	delete(m.tasks, taskID)
	return nil
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
	rt.SetManageProjectStore(mockStore)
	ctx := context.Background()

	scope := WorkspaceScope{
		Principal: identity.Principal{
			Type:           "user",
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
	var deployedProjectID, deployedTaskID string
	rt.SetProjectTaskDeployer(func(acct, pID, tID string) error {
		deployedProjectID = pID
		deployedTaskID = tID
		return nil
	})
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
		progTaskID = "task_test_1"
	}
	_, err = execTool(scope, "call-5d", `{
		"action": "deploy_task",
		"project_id": "proj_test_1",
		"task_id": "task_test_1"
	}`)
	if err != nil {
		t.Fatalf("deploy_task failed: %v", err)
	}
	if deployedTaskID != "task_test_1" || deployedProjectID != "proj_test_1" {
		t.Fatalf("expected deployer invoked with proj_test_1 and task_test_1, got %q, %q", deployedProjectID, deployedTaskID)
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
	if listTasksResp["count"].(float64) != 1 {
		t.Fatalf("expected 1 task, got %v", listTasksResp["count"])
	}

	// 7. Update task
	updateTaskOut, err := execTool(scope, "call-7", `{
		"action": "update_task",
		"project_id": "proj_test_1",
		"task_id": "task_test_1",
		"status": "needs_review"
	}`)
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
	refineTaskOut, err := execTool(scope, "call-7b", `{
		"action": "refine_task",
		"project_id": "proj_test_1",
		"task_id": "task_test_1",
		"feedback": "make sure it uses tailwind tokens only"
	}`)
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
			AccountScopeID: "account",
		},
	}
	db := newMockProjectStore()
	rt := &Runtime{}
	rt.SetManageProjectStore(db)

	var deployedTaskID, deployedProjectID string
	rt.SetProjectTaskDeployer(func(acct, pID, tID string) error {
		deployedProjectID = pID
		deployedTaskID = tID
		return nil
	})

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
	if deployedTaskID == "" || deployedProjectID != "proj_single_tp" {
		t.Fatalf("expected deployer invoked immediately on auto_approve, got %q, %q", deployedProjectID, deployedTaskID)
	}
}
