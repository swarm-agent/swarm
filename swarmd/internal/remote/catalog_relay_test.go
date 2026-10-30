package remote

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the relay holds a client to its own levels on catalog tools. A
// catalog tool's actions have different levels, so the relay cannot gate it
// by name (manage_projects would default to write): it forwards the call
// with the client's effective levels (ClientLevelsHeader, its scopes within
// the device ceiling) for Swarm Control to check per action, and lists the
// catalog tool as Swarm Control filtered it. swarm_* tools keep the name
// gate. Threat: a write client reaching approve or manage actions, or a
// read client shut out of read actions, through the relay. Boundary:
// Service.handle and filterTools with a recording Swarm Control handler.
func TestRelayForwardsClientLevelsForCatalogTools(t *testing.T) {
	var got []string
	s := &Service{control: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(ClientLevelsHeader))
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	})}
	cfg := Config{AllowWrite: true, Token: "device"}
	call := func(name string, scopes ...string) string {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"action": "list"}}})
		return string(s.handle(cfg, frame{Client: &frameClient{Scopes: scopes}, Body: body}))
	}
	// A read-only client reaches manage_projects (its read actions are
	// checked there) with its levels.
	if out := call("manage_projects", ScopeRead); strings.Contains(out, "does not allow") || len(got) != 1 || got[0] != ScopeRead {
		t.Fatalf("read client: %s %v", out, got)
	}
	// A client asking for approve and manage gets only what the ceiling
	// allows forwarded.
	if out := call("manage_projects", ScopeRead, ScopeWrite, ScopeApprove, ScopeManage); strings.Contains(out, "does not allow") || got[1] != ScopeRead+","+ScopeWrite {
		t.Fatalf("ceiling not applied: %s %v", out, got)
	}
	// swarm_* tools keep the name gate.
	if out := call("swarm_stop_run", ScopeRead); !strings.Contains(out, "does not allow") || len(got) != 2 {
		t.Fatalf("name gate: %s %v", out, got)
	}
	listed := string(filterTools([]byte(`{"result":{"tools":[{"name":"manage_projects"},{"name":"swarm_stop_run"}]}}`), []string{ScopeRead}))
	if !strings.Contains(listed, "manage_projects") || strings.Contains(listed, "swarm_stop_run") {
		t.Fatalf("relay listing: %s", listed)
	}
	// manage_workflows is a catalog tool too: forwarded with the client's
	// levels, listed for Swarm Control to filter per action.
	if out := call("manage_workflows", ScopeRead); strings.Contains(out, "does not allow") || len(got) != 3 || got[2] != ScopeRead {
		t.Fatalf("manage_workflows read client: %s %v", out, got)
	}
	if listed := string(filterTools([]byte(`{"result":{"tools":[{"name":"manage_workflows"}]}}`), []string{ScopeRead})); !strings.Contains(listed, "manage_workflows") {
		t.Fatalf("relay listing: %s", listed)
	}
}

// Purpose: each manage_workflows action needs the level of the swarm_* tool
// with the same effect (ToolScope), so moving workflows into the catalog
// neither widens nor narrows what a key may do; stop_all stays manage. help
// has no swarm_* tool and only reads. Threat: a write key turning the
// machine-wide stop switch, or a level silently changed in the move.
// Boundary: tool.Catalog's manage_workflows declarations against ToolScope.
func TestManageWorkflowsLevelsAreTheSwarmToolsLevels(t *testing.T) {
	swarmTool := map[string]string{
		"list": "swarm_list_workflows", "get": "swarm_list_workflows", "deliveries": "swarm_list_workflows",
		"history": "swarm_workflow_history", "propose": "swarm_set_workflow", "enable": "swarm_set_workflow", "disable": "swarm_set_workflow",
		"delete": "swarm_delete_workflow", "export": "swarm_export_workflow", "import": "swarm_import_workflow",
		"restore": "swarm_restore_workflow", "stop_all": "swarm_stop_workflows",
	}
	workflows, ok := tool.CatalogToolNamed("manage_workflows")
	if !ok {
		t.Fatal("manage_workflows is not a catalog tool")
	}
	for _, action := range workflows.Actions {
		name, ok := swarmTool[action.Action]
		if !ok {
			if action.Action != "help" || action.Level != tool.CatalogLevelRead {
				t.Fatalf("%s has no swarm_* tool to take its level from (level %s)", action.Action, action.Level)
			}
			continue
		}
		if action.Level.Scope() != ToolScope(name) {
			t.Fatalf("manage_workflows %s needs %s; %s needs %s", action.Action, action.Level.Scope(), name, ToolScope(name))
		}
	}
	if stop, _ := workflows.Action("stop_all"); stop.Level != tool.CatalogLevelManage {
		t.Fatalf("stop_all is %s, not manage", stop.Level)
	}
}
