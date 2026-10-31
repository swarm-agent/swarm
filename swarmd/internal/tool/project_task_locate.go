package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A task action names its task by project_id and task_id. When the call
// gives the task but not its project, the machine finds the project from the
// task id where it can, so the call is accepted without project_id:
//
//   - here, the account's project that holds the task (its project store);
//   - on a linked machine, the home's record of a task this conversation (or
//     this Swarm Control caller) sent there, which names the machine's project.
//
// A task id that names no task, or tasks in more than one project, leaves the
// call as it was: the action then refuses it with its own reason. The lookup
// reads only the caller's own account and its own sends; it never widens what
// the call may reach.

// projectTaskLocateMaxProjects bounds the projects searched for a task id.
const projectTaskLocateMaxProjects = 1000

// TaskProjectFromTaskID reports whether the action finds its project from its
// task id when project_id is omitted (catalog: it takes project_id and
// task_id or task_ids, and is not the task session's own report).
func TaskProjectFromTaskID(action CatalogAction) bool {
	if action.Tool != manageProjectsToolName || action.Action == "report_task" || action.Action == "inspect_files" || action.Action == "list_linked_tasks" {
		return false
	}
	return envelopeTakes(action, "project_id") && (envelopeTakes(action, "task_id") || envelopeTakes(action, "task_ids"))
}

// locateTaskProject returns args with project_id set from the task id when
// the action finds it there and the call omitted it. err is set only when the
// task id is ambiguous.
func (r *Runtime) locateTaskProject(ctx context.Context, scope WorkspaceScope, args map[string]any) (map[string]any, error) {
	if strings.TrimSpace(asString(args["project_id"])) != "" || strings.TrimSpace(asString(args["id"])) != "" {
		return args, nil
	}
	declared, ok := manageProjectsDeclared().Action(asString(args["action"]))
	if !ok || !TaskProjectFromTaskID(declared) {
		return args, nil
	}
	ids := taskIDsOfCall(args)
	if len(ids) == 0 {
		return args, nil
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	var found map[string][]string // project -> task ids found there
	var err error
	if machine != "" {
		found, err = r.linkedTaskProjects(ctx, scope, machine, ids)
	} else {
		found, err = r.localTaskProjects(scope, ids)
	}
	if err != nil || len(found) == 0 {
		// Nothing found (or the records are unavailable): the action
		// answers with its own reason.
		return args, nil
	}
	if len(found) > 1 {
		projects := make([]string, 0, len(found))
		for project := range found {
			projects = append(projects, project)
		}
		sort.Strings(projects)
		return args, fmt.Errorf("the task ids are in more than one project (%s); pass project_id", strings.Join(projects, ", "))
	}
	out := make(map[string]any, len(args)+1)
	for key, value := range args {
		out[key] = value
	}
	for project := range found {
		out["project_id"] = project
	}
	return out, nil
}

// taskIDsOfCall reads task_id, or task_ids, of a call.
func taskIDsOfCall(args map[string]any) []string {
	if id := strings.TrimSpace(asString(args["task_id"])); id != "" {
		return []string{id}
	}
	var ids []string
	switch list := args["task_ids"].(type) {
	case []any:
		for _, item := range list {
			if id := strings.TrimSpace(asString(item)); id != "" {
				ids = append(ids, id)
			}
		}
	case []string:
		for _, id := range list {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// localTaskProjects finds the account's projects holding the task ids.
func (r *Runtime) localTaskProjects(scope WorkspaceScope, ids []string) (map[string][]string, error) {
	account := strings.TrimSpace(scope.Principal.AccountScopeID)
	if r == nil || r.projects == nil || account == "" || !scope.Principal.Valid() {
		return nil, nil
	}
	projects, err := r.projects.ListProjects(account, projectTaskLocateMaxProjects)
	if err != nil {
		return nil, err
	}
	found := map[string][]string{}
	for _, id := range ids {
		for _, project := range projects {
			task, ok, err := r.projects.GetProjectTask(account, project.ID, id)
			if err != nil {
				return nil, err
			}
			if ok && task != nil {
				found[project.ID] = append(found[project.ID], id)
			}
		}
	}
	return found, nil
}

// linkedTaskProjects finds, in the home's records of the tasks this caller
// sent to machine, the machine's projects holding the task ids.
func (r *Runtime) linkedTaskProjects(ctx context.Context, scope WorkspaceScope, machine string, ids []string) (map[string][]string, error) {
	linked, ok := r.getProjectTaskLifecycleService().(FleetLinkedTasks)
	if !ok || !scope.Principal.Valid() {
		return nil, nil
	}
	var records []pebblestore.FleetLinkedTask
	switch {
	case scope.ControlCaller && strings.TrimSpace(scope.ControlSender) != "":
		bySender, ok := linked.(FleetLinkedTasksBySender)
		if !ok {
			return nil, nil
		}
		list, err := bySender.ListFleetLinkedTasksBySender(ctx, scope.Principal, scope.ControlSender)
		if err != nil {
			return nil, err
		}
		records = list
	case strings.TrimSpace(scope.SessionID) != "":
		list, err := linked.ListFleetLinkedTasks(ctx, scope.Principal, scope.SessionID, "")
		if err != nil {
			return nil, err
		}
		records = list
	default:
		return nil, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	found := map[string][]string{}
	for _, record := range records {
		if record.Machine == machine && want[record.TaskID] && strings.TrimSpace(record.ProjectID) != "" {
			found[record.ProjectID] = append(found[record.ProjectID], record.TaskID)
		}
	}
	return found, nil
}
