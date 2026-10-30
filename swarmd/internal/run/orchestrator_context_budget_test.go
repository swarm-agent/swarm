package run

import (
	"encoding/json"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// orchestratorShownDefinitions returns exactly the tool definitions production
// shows the Orchestrator: the same resolver (resolveAgentToolStates) and schema
// view (AgentShownToolDefinitions) over the compiled built-in definitions.
func orchestratorShownDefinitions(t *testing.T) []tool.Definition {
	t.Helper()
	definitions := append((*tool.Runtime)(nil).Definitions(), staticMediaInspectToolDefinition())
	known := map[string]struct{}{"ask_user": {}, "exit_plan_mode": {}, "plan_manage": {}, "task": {}, "task_progress": {}}
	for _, definition := range definitions {
		known[canonicalToolName(definition.Name)] = struct{}{}
	}
	profile := agentruntime.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	resolved, err := resolveAgentToolStates(profile, known)
	if err != nil {
		t.Fatal(err)
	}
	var shown []tool.Definition
	for _, definition := range AgentShownToolDefinitions(profile, definitions) {
		if resolved.Tools[canonicalToolName(definition.Name)].Enabled {
			shown = append(shown, definition)
		}
	}
	return shown
}

// Purpose: removed tools, dead actions and Coder-only fields must not reach
// the Orchestrator, while the agents that use them keep them. Threat: a
// duplicate (manage_automation) or another agent's tooling (manage-worktree,
// committed_source) returns to its context, or the trim leaks into the shared
// schemas other agents rely on.
func TestOrchestratorIsShownNoDuplicateDeadOrOtherAgentTooling(t *testing.T) {
	shown := orchestratorShownDefinitions(t)
	byName := map[string]tool.Definition{}
	for _, definition := range shown {
		byName[canonicalToolName(definition.Name)] = definition
	}
	for _, removed := range []string{"manage_automation", "manage_theme", "manage_worktree"} {
		if _, ok := byName[removed]; ok {
			t.Fatalf("Orchestrator is shown %s", removed)
		}
	}
	for _, kept := range []string{"manage_projects", "manage_workers", "manage_sessions", "manage_workspace", "bash", "manage_media"} {
		if _, ok := byName[kept]; !ok {
			t.Fatalf("Orchestrator lost %s", kept)
		}
	}
	encode := func(definition tool.Definition) string {
		raw, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if text := encode(byName["bash"]); strings.Contains(text, "committed_source") {
		t.Fatal("Orchestrator bash still carries committed_source")
	}
	sessions := encode(byName["manage_sessions"])
	for _, banned := range []string{"deploy", "proposals", "recovery", "commits", "review_worktrees", "git_status"} {
		if strings.Contains(sessions, banned) {
			t.Fatalf("Orchestrator manage-sessions still shows %q: %s", banned, sessions)
		}
	}
	workspace := encode(byName["manage_workspace"])
	for _, banned := range []string{"adopt_worktree", "set_session", "reclaim_worktree", "copy_worktree", "discover_worktrees", "cancel_worktree_recovery", "action='help'", "theme_id"} {
		if strings.Contains(workspace, banned) {
			t.Fatalf("Orchestrator manage_workspace still shows %q", banned)
		}
	}
	projects := encode(byName["manage_projects"])
	for _, banned := range []string{"approve_task", "accept_task", "deploy_task", "refine_task", "edit_requirements", "reconcile_tasks", "report_task", "aspect_ratio", "soundtrack", "variant_count", "theme_id", "icon", "synthesize_context", "list_media", "plan_document", "task_program", "coder_assignments"} {
		if strings.Contains(projects, banned) {
			t.Fatalf("Orchestrator manage_projects still shows %q", banned)
		}
	}
	for _, required := range []string{"propose_task", "wait_tasks", "get_task", "list_tasks", "list_machines", "list_linked_tasks", "read_task_session", "integrate_task"} {
		if !strings.Contains(projects, required) {
			t.Fatalf("Orchestrator manage_projects lacks %q", required)
		}
	}
	if description := byName["manage_projects"].Description; strings.HasPrefix(description, "integrate_task") || !strings.Contains(description[:min(len(description), 200)], "propose_task") {
		t.Fatalf("manage_projects description must open with delegation: %q", description)
	}

	// Other agents keep the full shared schemas.
	other := AgentShownToolDefinitions(pebblestore.AgentProfile{Name: agentruntime.CoderAgentID}, (*tool.Runtime)(nil).Definitions())
	for _, definition := range other {
		switch definition.Name {
		case "bash":
			if !strings.Contains(encode(definition), "committed_source") {
				t.Fatal("Coder bash lost committed_source")
			}
		case "manage_workspace":
			if !strings.Contains(encode(definition), "adopt_worktree") {
				t.Fatal("Swarm manage_workspace lost its worktree actions")
			}
		}
	}
	names := map[string]bool{}
	for _, definition := range other {
		names[canonicalToolName(definition.Name)] = true
	}
	for _, kept := range []string{"manage_automation", "manage_theme", "manage_worktree"} {
		if !names[kept] {
			t.Fatalf("%s definition was removed for every agent", kept)
		}
	}
	coder := agentruntime.SwarmAgentToolContract()
	if cfg := coder.Tools["manage_worktree"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatal("Swarm agent lost manage_worktree")
	}
}
