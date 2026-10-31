package api

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// Stage 6 of the tool catalog: manage_fleet, manage_media,
// manage_custom_agents, manage_theme, manage_designs and manage_projects'
// get_task_changes and read_task_file. Their parity cases join
// catalogParityCases, so TestCatalogParityCoversEveryAction holds each action
// to a case and TestCatalogParityOrchestratorAndControl runs each through the
// Orchestrator's runtime and Swarm Control's tools/call.

// paritySessionBound: a home manage_media action a conversation runs on its
// own session's project (MediaLifecycleService) while a Swarm Control caller,
// which has no conversation, names the project (the swarm_* media handlers).
// Both surfaces must answer the same way: both an envelope of the same tool,
// action and machine, or both a refusal. With machine= both run the linked
// machine's own service and are held to parityExact.
const paritySessionBound catalogParityCompare = "session_bound"

func parityOn(machines []string, args ...any) []catalogParityCase {
	var out []catalogParityCase
	for _, machine := range machines {
		call := parityArgs(args...)
		if machine != "" {
			call["machine"] = machine
		}
		out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
	}
	return out
}

func parityEverywhere(args ...any) []catalogParityCase {
	return parityOn([]string{"", "box"}, args...)
}

func parityHome(args ...any) []catalogParityCase { return parityOn([]string{""}, args...) }

func manageFleetParityCases() map[string][]catalogParityCase {
	return map[string][]catalogParityCase{
		"list_machines":     parityHome("action", "list_machines"),
		"list_linked_tasks": parityHome("action", "list_linked_tasks", "project_id", "home-project"),
		"agents_at_work":    parityEverywhere("action", "agents_at_work"),
		"status":            parityEverywhere("action", "status"),
		"activity":          parityEverywhere("action", "activity", "since_cursor", "0"),
		"list_runs":         parityEverywhere("action", "list_runs"),
	}
}

func manageMediaParityCases() map[string][]catalogParityCase {
	sessionBound := func(args ...any) catalogParityCase {
		call := parityArgs(args...)
		return catalogParityCase{orchestrator: call, control: call, compare: paritySessionBound}
	}
	linked := func(args ...any) catalogParityCase {
		call := parityArgs(append(args, "machine", "box")...)
		return catalogParityCase{orchestrator: call, control: call, compare: parityExact}
	}
	notServed := func(action string) []catalogParityCase {
		call := parityArgs("action", action)
		return []catalogParityCase{{orchestrator: call, control: call, compare: parityNotServed}}
	}
	return map[string][]catalogParityCase{
		"models":     {sessionBound("action", "models"), linked("action", "models")},
		"generate":   {sessionBound("action", "generate", "kind", "image", "brief", "a red square", "project", "no-such-project"), linked("action", "generate", "kind", "image", "brief", "a red square", "project", "no-such-project")},
		"retry":      {sessionBound("action", "retry", "job_id", "media/no-such-project/no-such-task"), linked("action", "retry", "job_id", "media/no-such-project/no-such-task", "project", "no-such-project")},
		"get":        {sessionBound("action", "get", "job_id", "media/no-such-project/no-such-task"), linked("action", "get", "job_id", "media/no-such-project/no-such-task")},
		"save":       {sessionBound("action", "save", "job_id", "media/no-such-project/no-such-task", "folder", "media"), linked("action", "save", "job_id", "media/no-such-project/no-such-task", "folder", "media")},
		"read":       append(parityHome("action", "read", "project_id", "home-project", "task_id", "h1"), linked("action", "read", "project_id", "remote-project", "task_id", "t1")),
		"list":       notServed("list"),
		"transcribe": notServed("transcribe"),
	}
}

func manageCustomAgentsParityCases() map[string][]catalogParityCase {
	return map[string][]catalogParityCase{
		"list":    parityHome("action", "list"),
		"get":     parityHome("action", "get", "name", "no-such-agent"),
		"define":  {{orchestrator: parityArgs("action", "define", "name", "parity-a", "instructions", "Review.", "tools", "read_only"), control: parityArgs("action", "define", "name", "parity-b", "instructions", "Review.", "tools", "read_only"), compare: parityShape, twin: map[string]string{"parity-b": "parity-a"}}},
		"disable": parityHome("action", "disable", "name", "no-such-agent"),
	}
}

func manageThemeParityCases() map[string][]catalogParityCase {
	return map[string][]catalogParityCase{
		"get": append(parityHome("action", "get"), parityHome("action", "get", "theme_id", "no-such-theme")...),
	}
}

func manageDesignsParityCases() map[string][]catalogParityCase {
	return map[string][]catalogParityCase{
		"create": parityEverywhere("action", "create", "brief", "A landing page", "project", "no-such-project"),
		"edit":   parityEverywhere("action", "edit", "brief", "Make it blue", "edit_path", "designs/a.html", "project", "no-such-project"),
	}
}

// manageProjectsTaskWorkParityCases are get_task_changes and read_task_file,
// here (a task whose session is not on this machine) and on the linked
// machine.
func manageProjectsTaskWorkParityCases() map[string][]catalogParityCase {
	both := func(action string, more ...any) []catalogParityCase {
		local := parityArgs(append([]any{"action", action, "project_id", "home-project", "task_id", "h1"}, more...)...)
		remote := parityArgs(append([]any{"action", action, "machine", "box", "project_id", "remote-project", "task_id", "t1"}, more...)...)
		return []catalogParityCase{{orchestrator: local, control: local, compare: parityExact}, {orchestrator: remote, control: remote, compare: parityExact}}
	}
	return map[string][]catalogParityCase{
		"get_task_changes": both("get_task_changes"),
		"read_task_file":   both("read_task_file", "path", "."),
	}
}

// seedStage6 gives the home the custom agent service (behind the runtime's
// manage_custom_agents) and the UI settings behind the theme catalog, as the
// daemon wires them.
func (h *catalogParityHarness) seedStage6(t *testing.T) {
	t.Helper()
	db := h.home.s.sessions.Store().Underlying()
	if h.home.s.agents == nil {
		events, err := pebblestore.NewEventLog(db)
		if err != nil {
			t.Fatal(err)
		}
		h.home.s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	}
	if h.home.s.uiSettings == nil {
		h.home.s.uiSettings = uisettings.NewService(pebblestore.NewUISettingsStore(db))
	}
	h.runtime.SetCustomAgentLifecycleService(h.home.s)
}
