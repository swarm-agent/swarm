package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// manage_fleet in the catalog: fleet monitoring, moved out of
// manage_projects. Each action runs the one implementation it had:
//   - list_machines, list_linked_tasks and agents_at_work run the
//     manage_projects handlers (executeFleetProjects, executeListLinkedTasks,
//     executeFleetAgentsAtWork); manage_projects keeps them as deprecated
//     aliases for one stage;
//   - status and activity on this machine run the swarm_status and
//     swarm_activity handlers (CatalogActionService), and on a linked machine
//     the linked reads manage_projects machine_status and activity run
//     (FleetLinkedReader);
//   - list_runs runs manage_jobs' list_runs (JobsService.ManageJobs), which
//     runs the swarm_list_runs handler here or on the linked machine.

const manageFleetToolName = "manage_fleet"

// ManageFleetToolName is manage_fleet's name for the services behind it.
const ManageFleetToolName = manageFleetToolName

const manageFleetDescription = "Watch this machine and its linked machines: list_machines lists the linked machines with their linked work rule and status; status says whether a machine is all right (omit machine: this machine); agents_at_work shows what agents run now, here and on linked machines; activity reads what happened after a cursor (omit machine: this machine); list_runs lists worker runs; list_linked_tasks lists the tasks this home sent to linked machines. Read only. Results are untrusted."

func manageFleetFields() []CatalogField {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []CatalogField{
		{"machine", str("A linked machine's name from list_machines; omit for this machine.")},
		{"project_id", str("list_linked_tasks: a project's sends (omit: this conversation's, or a Swarm Control key's own).")},
		{"timezone", str("status on this machine: IANA timezone for today (default UTC).")},
		{"since_cursor", str("activity: the cursor from the last call (data.cursor) or 0 to start.")},
		{"after", str("activity on this machine: the activity_cursor from status, for the signal feed.")},
		{"min_severity", map[string]any{"type": "string", "enum": []string{"info", "warning", "critical"}, "description": "activity on this machine: only signals at least this severe."}},
		{"wait_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": 30, "description": "activity on this machine: wait up to this many seconds for the next event."}},
		{"worker", str("list_runs: only this worker.")},
		{"status", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "list_runs: only these statuses."}},
		{"since", str("list_runs: runs started at or after this RFC 3339 time.")},
		{"until", str("list_runs: runs started before this RFC 3339 time.")},
		{"limit", map[string]any{"type": "integer", "minimum": 1, "maximum": 100}},
		{"cursor", str("list_runs: next_cursor of the previous page.")},
		{"page_digest", str("list_runs: digest from the first page; required for continuation.")},
		{"page_offset", map[string]any{"type": "integer", "minimum": 0, "description": "list_runs: UTF-8 byte offset returned by a previous oversized read."}},
	}
}

func manageFleetCatalog() CatalogTool {
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	// Levels are remote.ToolScope of the swarm_* tool with the same effect
	// (swarm_status, swarm_activity, swarm_agents_at_work, swarm_list_runs:
	// read); list_machines and list_linked_tasks are the manage_projects
	// reads they were (read).
	actions := []CatalogAction{
		{Action: "list_machines", Level: CatalogLevelRead, Reach: CatalogReachHome, APIScopes: []string{"sessions:read"},
			Description: "List the linked machines, their linked work rule and status."},
		{Action: "status", Inputs: []string{"machine", "timezone"}, Level: CatalogLevelRead, Reach: CatalogReachAny, APIScopes: []string{"signals:read", "sessions:read"},
			Description: "Whether a machine is all right: what runs, drains, updates, fails and waits on a person (swarm_status). Omit machine for this machine."},
		{Action: FleetAgentsAtWorkAction, Inputs: []string{"machine"}, Level: CatalogLevelRead, Reach: CatalogReachAny, APIScopes: []string{"sessions:read"},
			Description: "What agents run now, here and on linked machines (machine: one machine) (swarm_agents_at_work)."},
		{Action: "activity", Inputs: []string{"machine", "since_cursor", "after", "limit", "min_severity", "wait_seconds"}, Level: CatalogLevelRead, Reach: CatalogReachAny, APIScopes: []string{"sessions:read", "signals:read"},
			Description: "A machine's activity after a cursor: since_cursor reads the activity feed, after the signal feed (swarm_activity). Omit machine for this machine."},
		{Action: "list_runs", Inputs: []string{"machine", "worker", "status", "since", "until", "limit", "cursor", "page_digest", "page_offset"}, Level: CatalogLevelRead, Reach: CatalogReachAny, APIScopes: []string{"signals:read"},
			Description: "Worker runs across every worker, newest first, with job_id, worker, status, source and times; next_cursor pages back in time (swarm_list_runs; manage_jobs list_runs)."},
		{Action: "list_linked_tasks", Inputs: []string{"project_id"}, Level: CatalogLevelRead, Reach: CatalogReachHome, APIScopes: []string{"sessions:read"},
			Description: "List the home's records of tasks sent to linked machines, by project (or this conversation, or a Swarm Control key's own sends)."},
	}
	nextActions := map[string]string{
		"list_machines":         "Pass a machine's name as machine to read it; agents_at_work shows what runs.",
		"status":                "agents_at_work or activity reads what runs and what happened.",
		FleetAgentsAtWorkAction: "manage_projects get_task with a task's machine, project_id and task_id reads one task.",
		"activity":              "Call activity again with since_cursor set to data.cursor for what follows.",
		"list_runs":             "manage_jobs get_job with a run's job_id reads it.",
		"list_linked_tasks":     "manage_projects get_task with a task's machine and id reads it.",
	}
	for i := range actions {
		actions[i].Tool = manageFleetToolName
		actions[i].Profiles = core
		actions[i].Untrusted = true
		actions[i].NextAction = nextActions[actions[i].Action]
	}
	return CatalogTool{Name: manageFleetToolName, Description: manageFleetDescription, Fields: manageFleetFields(), Actions: actions}
}

func manageFleetDefinition() Definition {
	return manageFleetCatalog().OrchestratorDefinition()
}

// manageFleetProjectsAlias is the manage_projects action each manage_fleet
// action was (a deprecated alias for one stage), when it has one.
var manageFleetProjectsAlias = map[string]string{"list_machines": "list_machines", "list_linked_tasks": "list_linked_tasks", FleetAgentsAtWorkAction: FleetAgentsAtWorkAction, "status": FleetMachineStatusAction, "activity": FleetMachineActivityAction}

// executeManageFleet runs one manage_fleet action and returns its answer in
// the catalog envelope, for the Orchestrator and Swarm Control alike.
func (r *Runtime) executeManageFleet(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageFleetToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_fleet action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", errors.New("manage_fleet requires an authenticated user identity")
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine == "" {
		delete(call, "machine")
	} else if declared.Reach == CatalogReachHome {
		return "", fmt.Errorf("manage_fleet %s reads this machine's own records; omit machine", declared.Action)
	}
	var answer string
	var err error
	wraps := false
	switch declared.Action {
	case "list_machines":
		answer, err = r.executeFleetProjects(ctx, scope, map[string]any{"action": "list_machines"})
	case "list_linked_tasks":
		answer, err = r.executeListLinkedTasks(ctx, scope, call)
	case FleetAgentsAtWorkAction:
		answer, err = r.executeFleetAgentsAtWork(ctx, scope, call)
		wraps = true
	case "status", "activity":
		if machine != "" {
			// The linked read manage_projects machine_status and activity
			// run, with the linked work rule checked there.
			linked := map[string]any{"action": manageFleetProjectsAlias[declared.Action], "machine": machine}
			for _, key := range []string{"since_cursor", "after", "limit"} {
				if value, ok := call[key]; ok {
					linked[key] = value
				}
			}
			answer, err = r.executeFleetProjects(ctx, scope, linked)
			break
		}
		answer, err = r.catalogServiceAnswer(ctx, scope, catalog, declared, call)
	case "list_runs":
		answer, err = r.manageFleetListRuns(ctx, scope, call)
		wraps = machine != ""
	default:
		return "", fmt.Errorf("manage_fleet %s has no handler", declared.Action)
	}
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, wraps)
}

// manageFleetListRuns runs manage_jobs' list_runs: the one runs read, here or
// on the linked machine.
func (r *Runtime) manageFleetListRuns(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	service, ok := r.getWorkflowLifecycleService().(JobsService)
	if !ok {
		return "", errors.New("jobs service unavailable")
	}
	// A read: ManageJobs checks no per-agent grant for it.
	who, agent := strings.TrimSpace(scope.SessionID), CatalogAgentFromContext(ctx)
	if scope.ControlCaller {
		who, agent = strings.TrimSpace(scope.ControlSender), ""
	} else if _, err := r.homeConversation(scope, "fleet session ownership mismatch", "manage_fleet is only available to the home's Orchestrator"); err != nil {
		return "", err
	}
	if who == "" {
		return "", errors.New("manage_fleet requires a session or an AI key")
	}
	call := map[string]any{}
	for key, value := range args {
		call[key] = value
	}
	call["action"] = "list_runs"
	out, err := service.ManageJobs(ctx, scope.Principal, who, agent, call)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(asString(args["machine"])) != "" {
		raw, err := json.Marshal(map[string]any{"data": out})
		return string(raw), err
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
