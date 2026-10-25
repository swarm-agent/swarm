package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: custom definitions must share one allowlist and account authority.
// ManageCustomAgents and handleCustomAgents own the boundary; real route/MCP
// dispatch with Pebble is the narrowest layer proving persistence and rejection.
func TestCustomAgentsSharedBoundary(t *testing.T) {
	c, workers := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"swarm:write", "swarm:read", "automations:read", "automations:write"}})
	account := testPrincipal().AccountScopeID
	args := map[string]any{"name": "social-maker", "instructions": "Make social media", "tools": []string{"read", "list", "find", "search", "write", "edit", "bash", "media_inspect", "manage_media", "git_status", "git_diff", "git_add", "git_commit", "task_progress"}}
	calls := []func() error{
		func() error { _, err := c.dispatch(http.MethodPut, "/v3/agents/social-maker", nil, args); return err },
		func() error { _, err := controlMCPDefineAgent(c, args); return err },
		func() error {
			args["action"] = "define"
			runtime := tool.NewRuntime(1)
			runtime.SetCustomAgentLifecycleService(c.server)
			data, _ := json.Marshal(args)
			_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{Principal: testPrincipal()}, tool.Call{Name: "manage_custom_agents", Arguments: string(data)})
			return err
		},
	}
	var contract *store.AgentToolContract
	for _, call := range calls {
		if err := call(); err != nil {
			t.Fatal(err)
		}
		p, ok, err := c.server.agents.GetProfileForAccount(account, "social-maker")
		if err != nil || !ok {
			t.Fatal(err)
		}
		if contract != nil && !reflect.DeepEqual(contract, p.ToolContract) {
			t.Fatal("different contract")
		}
		contract = p.ToolContract
	}
	deployed, err := controlMCPDeployWorker(c, map[string]any{"name": "Social worker", "role": "coder", "instructions": "Make social media", "agent": "social-maker"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(deployed)["worker"])["id"].(string)
	worker, found, err := workers.GetWorker(account, id)
	if err != nil || !found || worker.Agent != "social-maker" || worker.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("worker not active: %+v %v", worker, err)
	}
	for _, forbidden := range []string{"manage_workers", "unknown-tool"} {
		args["tools"] = []string{forbidden}
		for _, call := range calls {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "tool \""+forbidden+"\" is not allowed; allowed tools:") {
				t.Fatalf("unexpected refusal: %v", err)
			}
		}
		p, _, _ := c.server.agents.GetProfileForAccount(account, "social-maker")
		if !reflect.DeepEqual(contract, p.ToolContract) {
			t.Fatal("rejection changed profile")
		}
	}
	args["name"] = "coder"
	args["tools"] = []string{"read"}
	if _, err := c.server.ManageCustomAgents(account, args); err == nil {
		t.Fatal("built-in accepted")
	}
}
