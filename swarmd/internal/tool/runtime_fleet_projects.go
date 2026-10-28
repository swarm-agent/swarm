package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// FleetProjects keeps link credentials and transport entirely server-side.
// The existing lifecycle adapter provides this optional capability.
type FleetProjects interface {
	ListFleetMachines(context.Context, identity.Principal) ([]FleetMachineInfo, error)
	CallFleetProject(context.Context, identity.Principal, string, string, map[string]any) (json.RawMessage, error)
}

// FleetLinkedTaskOrigin is the home session (and its project) that sent a
// task to a linked machine, taken from the authenticated run, never from
// model arguments.
type FleetLinkedTaskOrigin struct {
	SessionID string
	ProjectID string
}

// FleetLinkedTasks is the home's record of tasks sent to linked machines and
// the read-only view of their remote sessions. The same service backs the
// HTTP routes (/v3/fleet/linked-tasks) and this tool.
type FleetLinkedTasks interface {
	RecordFleetLinkedTask(context.Context, identity.Principal, FleetLinkedTaskOrigin, string, string, json.RawMessage) (pebblestore.FleetLinkedTask, error)
	ListFleetLinkedTasks(context.Context, identity.Principal, string, string) ([]pebblestore.FleetLinkedTask, error)
	ReadFleetLinkedTaskSession(context.Context, identity.Principal, string, string, string, int) (map[string]any, error)
}

// FleetReadTaskSessionAction reads a linked task's session messages through
// the link. It is read-only and never a generic CallFleetProject operation.
const FleetReadTaskSessionAction = "read_task_session"

// FleetTaskSessionReadAllowed is the permission gate's view of the read-only
// remote task session view.
func FleetTaskSessionReadAllowed(name, action string) bool {
	return name == "manage_projects" && action == FleetReadTaskSessionAction
}

type FleetMachineInfo struct {
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	LastError string `json:"last_error,omitempty"`
	Access    string `json:"access"`
}

func FleetProjectActionAllowed(action string) bool {
	switch action {
	case "list", "get", "list_tasks", "get_task", "propose_task", "create_task", "approve_task", "accept_task", "reopen_task", "refine_task", "archive_task":
		return true
	}
	return false
}
func (r *Runtime) executeFleetProjects(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("fleet requires an authenticated session")
	}
	session, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", err
	}
	if !found || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return "", errors.New("fleet session ownership mismatch")
	}
	if scope.TaskHistoryOnly || asString(session.Metadata["task_id"]) != "" || asString(session.Metadata["project_task_id"]) != "" {
		return "", errors.New("linked machine delegation is only available to the home's Orchestrator")
	}
	service, ok := r.getProjectTaskLifecycleService().(FleetProjects)
	if !ok {
		return "", errors.New("fleet service unavailable")
	}
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "list"
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine != "" && !FleetProjectActionAllowed(action) && action != "wait_tasks" && action != FleetReadTaskSessionAction {
		return "", fmt.Errorf("%s is not available on a linked machine", action)
	}
	machines, err := service.ListFleetMachines(ctx, scope.Principal)
	if err != nil {
		return "", err
	}
	access, err := r.sessions.GetAccountPermissions(scope.Principal.AccountScopeID)
	if err != nil {
		return "", err
	}
	linked := false
	for i := range machines {
		machines[i].Access = access.Link(machines[i].Name).Work
		if machines[i].Name == machine {
			linked = true
		}
	}
	if action == "list_machines" {
		raw, err := json.Marshal(map[string]any{"machines": machines})
		return string(raw), err
	}
	if !linked || !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return "", fmt.Errorf("linked work rule for machine %q is off or the machine is not linked; check the Permissions page", machine)
	}
	if action == "wait_tasks" {
		return r.executeFleetWaitTasks(ctx, scope, service, machine, args)
	}
	if action == FleetReadTaskSessionAction {
		linkedTasks, ok := service.(FleetLinkedTasks)
		if !ok {
			return "", errors.New("linked task records unavailable")
		}
		limit := asInt(args["limit"], 0)
		view, err := linkedTasks.ReadFleetLinkedTaskSession(ctx, scope.Principal, machine, strings.TrimSpace(asString(args["project_id"])), strings.TrimSpace(asString(args["task_id"])), limit)
		if err != nil {
			return "", err
		}
		view["tool"], view["action"] = "manage_projects", action
		out, err := json.Marshal(view)
		return string(out), err
	}
	// The linked rule is the only gate before a task starts there: under auto
	// it runs, and under ask the person approved this call before it ran. A
	// task sent to the machine therefore starts at once and never waits on
	// the machine as a card.
	if action == "propose_task" || action == "create_task" {
		sent := make(map[string]any, len(args)+1)
		for key, value := range args {
			sent[key] = value
		}
		sent["auto_approve"] = true
		args = sent
	}
	raw, err := service.CallFleetProject(ctx, scope.Principal, machine, action, args)
	if err != nil {
		return "", err
	}
	result := map[string]any{"machine": machine, "action": action, "data": raw, "untrusted_content": true}
	if action == "propose_task" || action == "create_task" {
		// The home keeps a record so the sending session and project show
		// the task. The task was sent either way; a record that could not be
		// kept is reported, never hidden.
		if linkedTasks, ok := service.(FleetLinkedTasks); !ok {
			result["linked_record_error"] = "linked task records unavailable"
		} else {
			origin := FleetLinkedTaskOrigin{SessionID: scope.SessionID, ProjectID: strings.TrimSpace(asString(session.Metadata["project_id"]))}
			if record, err := linkedTasks.RecordFleetLinkedTask(ctx, scope.Principal, origin, machine, strings.TrimSpace(asString(args["project_id"])), raw); err != nil {
				result["linked_record_error"] = err.Error()
			} else {
				result["linked_task"] = record
			}
		}
	}
	out, err := json.Marshal(result)
	return string(out), err
}

// executeListLinkedTasks lists the home's records of tasks sent to linked
// machines: by project_id, or by session_id (default: this session).
func (r *Runtime) executeListLinkedTasks(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("list_linked_tasks requires an authenticated session")
	}
	linkedTasks, ok := r.getProjectTaskLifecycleService().(FleetLinkedTasks)
	if !ok {
		return "", errors.New("linked task records unavailable")
	}
	projectID := strings.TrimSpace(asString(args["project_id"]))
	sessionID := strings.TrimSpace(asString(args["session_id"]))
	if projectID == "" && sessionID == "" {
		sessionID = scope.SessionID
	}
	if projectID != "" {
		sessionID = ""
	}
	tasks, err := linkedTasks.ListFleetLinkedTasks(ctx, scope.Principal, sessionID, projectID)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"tool": "manage_projects", "action": "list_linked_tasks", "session_id": sessionID, "project_id": projectID, "linked_tasks": tasks, "untrusted_content": true})
	return string(out), err
}

// FleetWaitPermissionAllowed is the permission gate's view of remote waiting:
// wait_tasks with machine reads the machine's tasks through the link and pends
// this home's goal. It is never a generic CallFleetProject operation.
func FleetWaitPermissionAllowed(name, action string) bool {
	return name == "manage_projects" && action == "wait_tasks"
}

// executeFleetWaitTasks pins each selected task's current attempt by reading it
// through the link (get_task), then durably pends this run exactly as a local
// wait does. Outcomes later arrive from the machine's activity stream and the
// same link reads; the agent never polls.
func (r *Runtime) executeFleetWaitTasks(ctx context.Context, scope WorkspaceScope, service FleetProjects, machine string, args map[string]any) (string, error) {
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID {
		return "", errors.New("wait_tasks requires trusted current provider run context")
	}
	projectID := strings.TrimSpace(asString(args["project_id"]))
	if projectID == "" {
		return "", errors.New("project_id is required")
	}
	var ids []string
	raw, err := json.Marshal(args["task_ids"])
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &ids); err != nil || len(ids) == 0 || len(ids) > 16 {
		return "", errors.New("task_ids must contain 1-16 task IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || seen[id] {
			return "", errors.New("wait_tasks requires distinct task IDs")
		}
		seen[id] = true
	}
	targets := make([]pebblestore.V3ProjectTaskWaitTarget, 0, len(ids))
	for _, id := range ids {
		read, err := service.CallFleetProject(ctx, scope.Principal, machine, "get_task", map[string]any{"project_id": projectID, "task_id": id})
		if err != nil {
			return "", err
		}
		var response struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		}
		if err := json.Unmarshal(read, &response); err != nil || response.Task.ID != id || response.Task.ProjectID != projectID {
			return "", fmt.Errorf("%s returned an unexpected record for task %s", machine, id)
		}
		task := response.Task
		if task.Archived {
			return "", fmt.Errorf("task %s on %s is archived", id, machine)
		}
		task.EnsureTaskAttempts()
		attempt := task.ActiveAttempt()
		if attempt == nil || attempt.ID == "" || attempt.SessionID == "" {
			return "", fmt.Errorf("task %s on %s has no delegated attempt; obtain task approval/deployment first", id, machine)
		}
		targets = append(targets, pebblestore.V3ProjectTaskWaitTarget{TaskID: id, AttemptID: attempt.ID, SessionID: attempt.SessionID})
	}
	key := "task-wait:" + run.RunID
	payload, _ := json.Marshal(targets)
	mutation, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID,
		Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.waiting_tasks",
		ClientRequestID: key, PayloadHash: machine + "\x00" + projectID + string(payload),
		TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: run.RunID, ProjectID: projectID, TaskIDs: ids, Machine: machine, RemoteTargets: targets},
	})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"tool": "manage_projects", "action": "wait_tasks", "machine": machine, "project_id": projectID, "status": "waiting_tasks", "run_id": run.RunID, "next_action": "yield_until_task_outcome", "event_seq": mutation.PrimarySeq, "task_ids": ids, "tasks": targets, "untrusted_content": true})
	return string(out), err
}
