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
}

func newMockProjectStore() *mockProjectStore {
	return &mockProjectStore{projects: make(map[string]*pebblestore.ProjectRecord)}
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

	// 5. Delete project
	delOut, err := execTool(scope, "call-5", `{"action":"delete","id":"proj_test_1"}`)
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
