package api

import (
	"strings"
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

// parityControlOnly: a catalog action with no Orchestrator profile whose name
// an agent's own tool holds (manage_theme get: the run canonicalizes
// manage_theme to the agents' theme tool, TestAgentManageThemeGetIsTheThemeTool).
// Swarm Control must answer an envelope; the Orchestrator's same-named call is
// another tool and is not compared.
const parityControlOnly catalogParityCompare = "control_only"

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
		"get": {
			{orchestrator: parityArgs("action", "get"), control: parityArgs("action", "get"), compare: parityControlOnly},
			{orchestrator: parityArgs("action", "get", "theme_id", "no-such-theme"), control: parityArgs("action", "get", "theme_id", "no-such-theme"), compare: parityControlOnly},
		},
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

// Purpose:
//   - Requirement: under linked work ask, a conversation's media generation
//     or design on the linked machine (work that costs money there) waits
//     for the person, and these tools have no approval prompt for it yet,
//     so it is refused at home with the reason and never reaches the
//     machine; reads still run, a Swarm Control caller (whose person
//     approves in their own chat) reaches the machine, and off refuses
//     every linked action on both surfaces.
//   - Threat/regression: the permission gate's linked rule names only the
//     older catalog tools, so without this the Orchestrator could start
//     paid work on an ask machine with no one asked.
//   - Boundary/authority: Server.ManageCatalogAction (ask) and
//     CallFleetWork (off), via the Orchestrator's runtime and
//     controlMCPCall.callTool over the linked parity harness.
//   - Narrowest layer: the two real entry points over in-process daemons.
func TestStage6LinkedChangesUnderAskAndOff(t *testing.T) {
	h := newCatalogParityHarness(t)
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	design := map[string]any{"action": "create", "machine": "box", "brief": "A landing page", "project": "no-such-project"}
	media := map[string]any{"action": "generate", "machine": "box", "kind": "image", "brief": "a red square", "project": "no-such-project"}
	for name, args := range map[string]map[string]any{"manage_designs": design, "manage_media": media} {
		if _, err := h.orchestratorTool(t, name, args); err == nil || !strings.Contains(err.Error(), "linked.box.work is ask") {
			t.Fatalf("Orchestrator %s on an ask machine: %v", name, err)
		}
		// Swarm Control reaches the machine, which refuses the unknown
		// project: the call was not held at home.
		if _, refused := h.controlTool(t, catalogParityFullKey, name, args); !strings.Contains(refused, "linked machine box refused") || !strings.Contains(refused, "no-such-project") {
			t.Fatalf("Swarm Control %s on an ask machine did not reach it: %q", name, refused)
		}
	}
	if _, err := h.orchestratorTool(t, "manage_projects", map[string]any{"action": "read_task_file", "machine": "box", "project_id": "remote-project", "task_id": "t1"}); err == nil || strings.Contains(err.Error(), "is ask") {
		t.Fatalf("a read on an ask machine was held, or answered without its machine's refusal: %v", err)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	read := map[string]any{"action": "models", "machine": "box"}
	if _, err := h.orchestratorTool(t, "manage_media", read); err == nil || !strings.Contains(err.Error(), "linked.box.work is off") {
		t.Fatalf("Orchestrator read on an off machine: %v", err)
	}
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_media", read); !strings.Contains(refused, "linked.box.work is off") {
		t.Fatalf("Swarm Control read on an off machine: %q", refused)
	}
}

// catalogStage6OrchestratorCall reports a stage 6 call only the home
// Orchestrator makes (catalogOrchestratorOnly, ManageCatalogAction): every
// manage_fleet, manage_designs, manage_theme and manage_custom_agents call,
// manage_media's account-wide or linked ones, and manage_projects' task work
// reads. The parity harness makes them as the Orchestrator, as the run does.
func catalogStage6OrchestratorCall(name string, args map[string]any) bool {
	switch name {
	case "manage_fleet", "manage_designs", "manage_theme", "manage_custom_agents", "manage_media":
		return true
	case "manage_projects":
		return args["action"] == "get_task_changes" || args["action"] == "read_task_file"
	}
	return false
}
