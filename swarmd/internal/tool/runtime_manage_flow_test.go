package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/flow"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"testing"
)

func TestManageFlowDefinitionAndInspect(t *testing.T) {
	rt, _ := newManageFlowTestRuntime(t)
	definitions := rt.Definitions()
	found := false
	for _, definition := range definitions {
		if definition.Name == "manage-flow" {
			found = true
			if definition.Description == "" {
				t.Fatal("manage-flow description is empty")
			}
		}
	}
	if !found {
		t.Fatal("manage-flow definition not found")
	}

	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), manageFlowTestScope(t.TempDir()), Call{Name: "manage-flow", Arguments: `{"action":"inspect"}`})
	if err != nil {
		t.Fatalf("inspect: %v output=%s", err, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if payload["action"] != "inspect" {
		t.Fatalf("action = %v", payload["action"])
	}
	if payload["instructions"] == "" {
		t.Fatal("instructions missing")
	}
	availableAgents, ok := payload["available_agents"].(map[string]any)
	if !ok {
		t.Fatalf("available_agents missing or wrong type: %T", payload["available_agents"])
	}
	if availableAgents["configured"] != true {
		t.Fatalf("available_agents configured = %v", availableAgents["configured"])
	}
	if int(availableAgents["count"].(float64)) == 0 {
		t.Fatalf("available_agents count = %v", availableAgents["count"])
	}
	agents, ok := availableAgents["agents"].([]any)
	if !ok || len(agents) == 0 {
		t.Fatalf("available_agents agents missing or empty: %T", availableAgents["agents"])
	}
	firstAgent, ok := agents[0].(map[string]any)
	if !ok {
		t.Fatalf("first available agent type = %T", agents[0])
	}
	if _, ok := firstAgent["flow_agent"].(map[string]any); !ok {
		t.Fatalf("available agent flow_agent missing or wrong type: %T", firstAgent["flow_agent"])
	}
}

func TestManageFlowPreviewAndConfirmCreate(t *testing.T) {
	rt, flows := newManageFlowTestRuntime(t)
	workspacePath := t.TempDir()
	args := `{"action":"create","flow_id":"daily-agents","content":{"name":"Daily AGENTS.md refresh","target":{"kind":"self"},"agent":{"profile_name":"memory","profile_mode":"background"},"workspace":{"workspace_path":"` + filepath.ToSlash(workspacePath) + `"},"schedule":{"cadence":"daily","time":"09:00","timezone":"UTC"},"catch_up_policy":{"mode":"once"},"intent":{"prompt":"Check git diffs daily and update AGENTS.md when durable agent guidance changes."}}}`
	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), manageFlowTestScope(workspacePath), Call{Name: "manage-flow", Arguments: args})
	if err != nil {
		t.Fatalf("preview create: %v output=%s", err, out)
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview["status"] != "proposed_create" || preview["applied"] != false {
		t.Fatalf("preview status/applied = %v/%v", preview["status"], preview["applied"])
	}
	if _, ok, err := flows.GetDefinitionForAccount("account-1", "daily-agents"); err != nil || ok {
		t.Fatalf("preview should not persist ok=%v err=%v", ok, err)
	}

	var approved map[string]any
	rawApproved, _ := json.Marshal(preview["approved_arguments"])
	if err := json.Unmarshal(rawApproved, &approved); err != nil {
		t.Fatalf("decode approved args: %v", err)
	}
	confirmed, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), manageFlowTestScope(workspacePath), Call{Name: "manage_flow", Arguments: string(rawApproved)})
	if err != nil {
		t.Fatalf("confirm create: %v output=%s approved=%v", err, confirmed, approved)
	}
	if _, ok, err := flows.GetDefinitionForAccount("account-1", "daily-agents"); err != nil || !ok {
		t.Fatalf("confirmed definition ok=%v err=%v", ok, err)
	}
	if _, ok, err := flows.GetAcceptedAssignmentForAccount("account-1", "daily-agents"); err != nil || !ok {
		t.Fatalf("confirmed accepted assignment ok=%v err=%v", ok, err)
	}
}

func TestManageFlowConfirmDelete(t *testing.T) {
	rt, flows := newManageFlowTestRuntime(t)
	assignment := flow.Assignment{FlowID: "flow-delete", Revision: 1, Name: "Delete me", Enabled: true, Target: flow.TargetSelection{Kind: "self"}, Agent: flow.AgentSelection{ProfileName: "memory", ProfileMode: "background"}, Workspace: flow.WorkspaceContext{WorkspacePath: t.TempDir()}, Schedule: flow.ScheduleSpec{Cadence: flow.CadenceOnDemand}, CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce}, Intent: flow.PromptIntent{Prompt: "Run once."}}
	assignment.Agent = flow.NormalizeAgentSelection(assignment.Agent)
	if _, err := flows.PutDefinition(pebblestore.FlowDefinitionRecord{AccountScopeID: "account-1", UserID: "user-1", FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment}); err != nil {
		t.Fatalf("seed definition: %v", err)
	}
	out, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), manageFlowTestScope(assignment.Workspace.WorkspacePath), Call{Name: "manage-flow", Arguments: `{"action":"delete","flow_id":"flow-delete","confirm":true}`})
	if err != nil {
		t.Fatalf("delete: %v output=%s", err, out)
	}
	if _, ok, err := flows.GetDefinitionForAccount("account-1", "flow-delete"); err != nil || ok {
		t.Fatalf("definition after delete ok=%v err=%v", ok, err)
	}
}

func manageFlowTestScope(path string) WorkspaceScope {
	return WorkspaceScope{PrimaryPath: path, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}}
}

func newManageFlowTestRuntime(t *testing.T) (*Runtime, *pebblestore.FlowStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "flow.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	flows := pebblestore.NewFlowStore(store)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), nil)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}
	rt := NewRuntime(1)
	rt.SetManageAgentService(agents)
	rt.SetManageFlowServices(flows, fakeManageFlowWorkspace{path: t.TempDir()})
	return rt, flows
}

type fakeManageFlowWorkspace struct{ path string }

func (f fakeManageFlowWorkspace) CurrentBinding() (workspaceruntime.Resolution, bool, error) {
	return workspaceruntime.Resolution{ResolvedPath: f.path, WorkspacePath: f.path, WorkspaceName: "test"}, true, nil
}

func (f fakeManageFlowWorkspace) ScopeForPath(path string) (workspaceruntime.Scope, error) {
	return workspaceruntime.Scope{ResolvedPath: path, WorkspacePath: path, WorkspaceName: "test", Matched: true}, nil
}

func (f fakeManageFlowWorkspace) ListKnown(limit int) ([]workspaceruntime.Entry, error) {
	return nil, nil
}
