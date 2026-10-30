package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/remote"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// relayDeviceScopes are the API scopes of a relay device token whose ceiling
// allows everything (remote deviceTokenScopes): sessions:* and no swarm:*
// levels. A relay client's own levels come with each call
// (remote.ClientLevelsHeader).
var relayDeviceScopes = []string{"sessions:read", "automations:read", "agents:read", "signals:read", "secrets:read", "sessions:write", "automations:write", "settings:write", "agents:write", "usage:write"}

// relayCall runs one manage_projects call as the relay forwards it: the
// device token's scopes and the client's levels in the header.
func relayCall(t *testing.T, h *catalogParityHarness, clientLevels []string, args map[string]any) (string, string) {
	t.Helper()
	req := h.controlRequest(relayDeviceScopes, "")
	req.Header.Set(remote.ClientLevelsHeader, strings.Join(clientLevels, ","))
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer cancel()
	c := &controlMCPCall{request: req.WithContext(ctx), server: h.home.s}
	params, _ := json.Marshal(map[string]any{"name": "manage_projects", "arguments": args})
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("tools/call: %+v", rpcErr)
	}
	text, _ := result["content"].([]map[string]any)[0]["text"].(string)
	if result["isError"] == true {
		return "", text
	}
	return text, ""
}

// relayListed lists manage_projects' actions for a relay client.
func relayListed(t *testing.T, h *catalogParityHarness, clientLevels []string) map[string]bool {
	t.Helper()
	req := h.controlRequest(relayDeviceScopes, "")
	req.Header.Set(remote.ClientLevelsHeader, strings.Join(clientLevels, ","))
	c := &controlMCPCall{request: req, server: h.home.s}
	for _, candidate := range c.visibleTools() {
		if candidate.Name == "manage_projects" {
			out := map[string]bool{}
			for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
				out[action] = true
			}
			return out
		}
	}
	return nil
}

// Purpose: a relay client is held to its own levels per action. The relay
// forwards calls with its device token, which carries API scopes but no
// swarm:* levels, so without the forwarded client levels every action would
// pass. A write-only client is refused approve-level resolve_task_permission
// and manage-level delete_task and delete (nothing is deleted); a read-only
// client can call the read actions and is refused writes; forwarded levels
// only narrow a key that has levels of its own; an empty forwarded set
// allows nothing. Threat: privilege escalation through the relay. Boundary:
// controlMCPCall.catalogLevels / callCatalogTool / catalogTools and
// tool.CatalogLevelsAllow, with the relay's header (remote.ClientLevelsHeader).
func TestRelayClientLevelsBindEveryCatalogAction(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	writeOnly := []string{remote.ScopeRead, remote.ScopeWrite}
	for _, args := range []map[string]any{
		{"action": tool.ResolveTaskPermissionAction, "project_id": "home-project", "task_id": "h1", "permission_id": "p1", "decision": "approve"},
		{"action": "delete_task", "project_id": "home-project", "task_id": "d1", "expected_revision": float64(1)},
		{"action": "delete", "id": "del-a"},
	} {
		if _, refused := relayCall(t, h, writeOnly, args); !strings.Contains(refused, "this key is limited to swarm:read, swarm:write") {
			t.Fatalf("write-only relay client ran %v: %q", args["action"], refused)
		}
	}
	if _, found, _ := store.GetProjectTask(h.home.account, "home-project", "d1"); !found {
		t.Fatal("a refused delete_task deleted the task")
	}
	if _, found, _ := store.GetProject(h.home.account, "del-a"); !found {
		t.Fatal("a refused delete deleted the project")
	}
	listed := relayListed(t, h, writeOnly)
	if !listed["message_task"] || listed[tool.ResolveTaskPermissionAction] {
		t.Fatalf("write-only relay listing: %v", listed)
	}
	// Read-only: reads run, writes are refused.
	readOnly := []string{remote.ScopeRead}
	for _, args := range []map[string]any{
		{"action": "list_tasks", "project_id": "home-project"},
		{"action": "get_task", "project_id": "home-project", "task_id": "h1"},
		{"action": "list"},
	} {
		if out, errText := relayCall(t, h, readOnly, args); errText != "" || !strings.Contains(out, `"action":"`+args["action"].(string)+`"`) {
			t.Fatalf("read-only relay client %v: %s %q", args["action"], out, errText)
		}
	}
	if _, refused := relayCall(t, h, readOnly, map[string]any{"action": "stop_task", "project_id": "home-project", "task_id": "h1"}); !strings.Contains(refused, "needs swarm:write") {
		t.Fatalf("read-only relay client stop_task: %q", refused)
	}
	if listed := relayListed(t, h, readOnly); !listed["get_task"] || listed["message_task"] {
		t.Fatalf("read-only relay listing: %v", listed)
	}
	// No forwarded level: nothing.
	if _, refused := relayCall(t, h, nil, map[string]any{"action": "list"}); refused == "" {
		t.Fatal("a relay call with no client level ran")
	}
	if listed := relayListed(t, h, nil); listed != nil {
		t.Fatalf("a relay client with no level lists %v", listed)
	}
	// Forwarded levels narrow a key with levels; they never widen it.
	req := h.controlRequest([]string{"swarm:read", "sessions:read", "sessions:write"}, "")
	req.Header.Set(remote.ClientLevelsHeader, "swarm:read,swarm:write,swarm:manage")
	if levels, bound := (&controlMCPCall{request: req, server: h.home.s}).catalogLevels(); !bound || len(levels) != 1 || levels[0] != "swarm:read" {
		t.Fatalf("forwarded levels widened a key: %v %v", levels, bound)
	}
}

// Purpose: each catalog action needs the API scopes of the route behind it,
// since the call runs in-process and the route's own check never runs:
// folder environment update and rebuild need sessions:write (a
// projects:write-only key is refused), linked activity and agents at work
// need sessions:read (a projects:read-only key is refused), and project and
// task actions keep projects:* or sessions:*. Every action declares its
// scopes. Threat: a key reaching routes it was never granted through the
// catalog tool. Boundary: CatalogAction.APIScopes and
// controlMCPCall.catalogActionAllowed / callCatalogTool.
func TestCatalogActionsNeedTheirRoutesScopes(t *testing.T) {
	for _, catalog := range tool.Catalog() {
		for _, action := range catalog.Actions {
			if len(action.APIScopes) == 0 {
				t.Fatalf("%s %s declares no API scopes", catalog.Name, action.Action)
			}
		}
	}
	h := newCatalogParityHarness(t)
	projectsWrite := []string{"swarm:read", "swarm:write", "projects:read", "projects:write"}
	for _, action := range []string{tool.FolderEnvironmentUpdate, tool.FolderEnvironmentRebuild} {
		_, refused := h.control(t, projectsWrite, map[string]any{"action": action, "workspace_path": "/no-such-folder", "environment": map[string]any{"base_image": "debian:12"}})
		if !strings.Contains(refused, "needs sessions:write") {
			t.Fatalf("projects:write key %s: %q", action, refused)
		}
	}
	projectsRead := []string{"swarm:read", "projects:read"}
	for _, args := range []map[string]any{
		{"action": tool.FleetMachineActivityAction, "machine": "box", "since_cursor": "0"},
		{"action": tool.FleetAgentsAtWorkAction},
		{"action": tool.FolderEnvironmentList},
	} {
		if _, refused := h.control(t, projectsRead, args); !strings.Contains(refused, "needs sessions:read") {
			t.Fatalf("projects:read key %v: %q", args["action"], refused)
		}
	}
	// The project routes' scopes still serve: projects:read reads tasks.
	if out, errText := h.control(t, projectsRead, map[string]any{"action": "list_tasks", "project_id": "home-project"}); errText != "" || !strings.Contains(out, `"action":"list_tasks"`) {
		t.Fatalf("projects:read list_tasks: %s %q", out, errText)
	}
	// The listing agrees: no environment or fleet reads for projects:read.
	c := &controlMCPCall{request: requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp?tools=all", nil), h.home.user, h.home.account), &pebblestore.ScopedTokenRecord{ID: "k", Scopes: projectsRead}), server: h.home.s}
	for _, candidate := range c.visibleTools() {
		if candidate.Name != "manage_projects" {
			continue
		}
		for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
			if action == tool.FleetMachineActivityAction || action == tool.FleetAgentsAtWorkAction || action == tool.FolderEnvironmentList {
				t.Fatalf("projects:read key lists %s", action)
			}
		}
	}
}
