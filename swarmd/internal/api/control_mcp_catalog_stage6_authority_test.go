package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/tool"
)

// agentToolCall runs one tool through the runtime as the run service does for
// a session's agent.
func (h *catalogParityHarness) agentToolCall(t *testing.T, agent, session, name string, args map[string]any) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), agent), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(args)
	return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: h.p}, tool.Call{Name: name, Arguments: string(raw)})
}

// Purpose:
//   - Requirement: an agent's manage_theme (the Swarm agent has it) is the
//     theme tool for every action, get included: get answers the theme
//     named by theme_id, theme or id, in the theme tool's answer
//     (status, action, theme, workspace), also in a delegated task's
//     session. The catalog's manage_theme get is Swarm Control's only.
//   - Threat/regression: the catalog's get taking over the agents' get,
//     dropping the theme and id aliases (silently answering the active
//     theme), changing the answer to an envelope, or refusing a task's
//     session.
//   - Boundary/authority: Runtime.ExecuteForWorkspaceScopeWithRuntime's
//     manage_theme dispatch to executeManageTheme (manageThemeGet), and
//     ExecuteControlCatalog for Swarm Control.
//   - Narrowest layer: the runtime entry point the run service calls
//     (run.Service.executeManageThemeTool) with the daemon's UI settings.
func TestAgentManageThemeGetIsTheThemeTool(t *testing.T) {
	h := newCatalogParityHarness(t)
	h.runtime.SetManageThemeServices(h.home.s.uiSettings, nil)
	h.createGoal(t, "swarm-task-session", map[string]any{"task_id": "h1", "project_id": "home-project"})
	inspect, err := h.agentToolCall(t, "swarm", "home-orch", "manage_theme", map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Default  string           `json:"default_theme_id"`
		Builtins []map[string]any `json:"builtin_themes"`
	}
	if err := json.Unmarshal([]byte(inspect), &listing); err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, theme := range listing.Builtins {
		if id, _ := theme["id"].(string); id != "" && id != listing.Default {
			want = id
			break
		}
	}
	if want == "" {
		t.Fatalf("no builtin theme besides the default: %s", inspect)
	}
	for _, name := range []string{"manage_theme", "manage-theme"} {
		for _, key := range []string{"theme_id", "theme", "id"} {
			for _, session := range []string{"home-orch", "swarm-task-session"} {
				out, err := h.agentToolCall(t, "swarm", session, name, map[string]any{"action": "get", key: want})
				if err != nil {
					t.Fatalf("%s get %s=%s in %s: %v", name, key, want, session, err)
				}
				var answer map[string]any
				if err := json.Unmarshal([]byte(out), &answer); err != nil {
					t.Fatal(err)
				}
				theme, _ := answer["theme"].(map[string]any)
				if answer["status"] != "ok" || answer["action"] != "get" || theme["id"] != want || answer["tool"] != nil {
					t.Fatalf("%s get %s=%s in %s: %s", name, key, want, session, out)
				}
				if _, ok := answer["workspace"]; !ok {
					t.Fatalf("%s get lost its workspace summary: %s", name, out)
				}
			}
		}
	}
	// The theme tool's get still requires a theme; it never falls back.
	if _, err := h.agentToolCall(t, "swarm", "home-orch", "manage_theme", map[string]any{"action": "get"}); err == nil || !strings.Contains(err.Error(), "requires theme_id") {
		t.Fatalf("theme tool get without a theme: %v", err)
	}
	// Swarm Control's catalog get answers the envelope.
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_theme", map[string]any{"action": "get", "theme_id": want})
	if refused != "" || tool.CheckEnvelope(out) != nil {
		t.Fatalf("Swarm Control manage_theme get: %q %s", refused, out)
	}
	if data, _ := parityDecode(t, out)["data"].(map[string]any); data["id"] != want {
		t.Fatalf("Swarm Control manage_theme get answered another theme: %s", out)
	}
}

// Purpose:
//   - Requirement: the account-wide and linked catalog paths (manage_media
//     read and any call with machine, manage_fleet, manage_designs,
//     manage_projects' task work reads) are the home Orchestrator's only. A
//     worker's job session (worker_id) and any other agent are refused at the
//     tool and again in Server.ManageCatalogAction, and nothing runs: no
//     media task appears on the linked machine and no file is read.
//   - Threat/regression: work running on its own reading any project's
//     media in the account, or starting paid media work on a linked machine
//     under a project it names, beyond what was accepted for it.
//   - Boundary/authority: tool.catalogOrchestratorOnly (catalogServiceAnswer,
//     executeManageFleet) and Server.ManageCatalogAction's conversation
//     branch, through the runtime entry point and the service directly.
//   - Narrowest layer: the two real boundaries over in-process daemons.
func TestStage6RefusesWorkersAndOtherAgents(t *testing.T) {
	h := newCatalogParityHarness(t)
	h.createGoal(t, "worker-job-session", map[string]any{"worker_id": "w1", "job_id": "j1"})
	remoteTasks := func() int {
		tasks, err := h.remote.s.sessions.Store().ListProjectTasks(h.remote.account, "remote-project", 1000)
		if err != nil {
			t.Fatal(err)
		}
		return len(tasks)
	}
	before := remoteTasks()
	read := map[string]any{"action": "read", "project_id": "home-project", "task_id": "h1"}
	generate := map[string]any{"action": "generate", "machine": "box", "kind": "image", "brief": "a red square", "project": "remote-project"}
	calls := []struct {
		agent, session, name string
		args                 map[string]any
	}{
		{agentruntime.SwarmOrchestratorAgentID, "worker-job-session", "manage_media", read},
		{agentruntime.SwarmOrchestratorAgentID, "worker-job-session", "manage_media", generate},
		{agentruntime.SwarmOrchestratorAgentID, "worker-job-session", "manage_designs", map[string]any{"action": "create", "machine": "box", "brief": "A page", "project": "remote-project"}},
		{"designer", "home-orch", "manage_media", read},
		{"designer", "home-orch", "manage_media", generate},
		{"my-custom-agent", "home-orch", "manage_fleet", map[string]any{"action": "status"}},
		{"swarm", "home-orch", "manage_projects", map[string]any{"action": "read_task_file", "project_id": "home-project", "task_id": "h1"}},
	}
	for _, c := range calls {
		out, err := h.agentToolCall(t, c.agent, c.session, c.name, c.args)
		if err == nil || !strings.Contains(err.Error(), "Orchestrator's") {
			t.Fatalf("%s in %s ran %s %v: %v %s", c.agent, c.session, c.name, c.args, err, out)
		}
	}
	// The service itself refuses a worker's session even with the
	// Orchestrator in ctx, and another agent's call.
	for _, c := range []struct{ agent, session, want string }{
		{agentruntime.SwarmOrchestratorAgentID, "worker-job-session", "worker's session"},
		{"designer", "home-orch", "another agent"},
	} {
		ctx := tool.WithCatalogAgent(context.Background(), c.agent)
		for _, args := range []map[string]any{read, generate} {
			if out, err := h.home.s.ManageCatalogAction(ctx, h.p, c.session, tool.ManageMediaToolName, args); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ManageCatalogAction %s in %s %v: %v %v", c.agent, c.session, args, err, out)
			}
		}
	}
	if after := remoteTasks(); after != before {
		t.Fatalf("a refused call made %d media tasks on the linked machine", after-before)
	}
	// The Orchestrator's own conversation still reaches the service (the
	// home task has no media file, so it is the handler's own refusal).
	if _, err := h.agentToolCall(t, agentruntime.SwarmOrchestratorAgentID, "home-orch", "manage_media", read); err == nil || strings.Contains(err.Error(), "Orchestrator's") {
		t.Fatalf("the Orchestrator's read was refused as another agent's: %v", err)
	}
}

// Purpose:
//   - Requirement: the Orchestrator has manage_designs, generated from the
//     catalog declaration, in its tool definitions; manage_theme get stays
//     Swarm Control's only because manage_theme is the agents' theme tool.
//   - Threat/regression: a catalog tool the brief requires for the
//     Orchestrator missing from its definitions.
//   - Boundary/authority: Runtime.Definitions and the catalog profiles.
//   - Narrowest layer: the definitions themselves.
func TestOrchestratorHasManageDesigns(t *testing.T) {
	h := newCatalogParityHarness(t)
	found := false
	for _, definition := range h.runtime.Definitions() {
		if definition.Name == "manage_designs" {
			found = true
			enum, _ := definition.Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
			if strings.Join(enum, ",") != "create,edit" {
				t.Fatalf("manage_designs actions: %v", enum)
			}
		}
	}
	if !found {
		t.Fatal("the Orchestrator's definitions have no manage_designs")
	}
	designs, _ := tool.CatalogToolNamed("manage_designs")
	theme, _ := tool.CatalogToolNamed("manage_theme")
	for _, action := range designs.Actions {
		if !action.Listed(tool.CatalogProfileOrchestrator) {
			t.Fatalf("manage_designs %s has no Orchestrator profile", action.Action)
		}
	}
	if get, _ := theme.Action("get"); get.Listed(tool.CatalogProfileOrchestrator) {
		t.Fatal("manage_theme get is in the Orchestrator's profile beside the agents' theme tool of the same name")
	}
}
