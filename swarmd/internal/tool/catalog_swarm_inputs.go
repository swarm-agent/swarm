package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// Inputs the swarm_* tools took that their catalog action now takes under the
// same name and meaning (docs/tool-catalog.md: a catalog action accepts every
// input of the tool it replaced). Each is mapped here onto the action's own
// input, or served by the one implementation the swarm_* tool used.

// TaskFilesScope is the scope sentence a brief gets for files: the path
// prefixes a task or job may change (swarm_run_task, swarm_give_job and
// manage_projects propose_task). Empty when no file is named.
func TaskFilesScope(raw any) string {
	var files []string
	switch list := raw.(type) {
	case []any:
		for _, item := range list {
			if path, _ := item.(string); strings.TrimSpace(path) != "" {
				files = append(files, strings.TrimSpace(path))
			}
		}
	case []string:
		for _, path := range list {
			if strings.TrimSpace(path) != "" {
				files = append(files, strings.TrimSpace(path))
			}
		}
	}
	if len(files) == 0 {
		return ""
	}
	return "\n\nScope: only change files under " + strings.Join(files, ", ") + ". Anything else is out of scope; say so instead of touching it."
}

// manageProjectsSwarmArgs maps the swarm_* names a manage_projects action
// takes onto its own inputs, before the call is routed (here or to a linked
// machine, which then sees only the action's own inputs):
//   - update: project_id is id (swarm_update_project_context,
//     swarm_set_project_workspaces, swarm_retry_project_context) and
//     project_md is project_context;
//   - wait_tasks: task_id is a one-task task_ids (swarm_wait_task);
//   - propose_task: brief is prompt, workspace is workspace_path (absolute)
//     or workspace_id, and files adds the brief's scope (swarm_run_task).
//
// An action's own input wins over its swarm_* name.
func manageProjectsSwarmArgs(args map[string]any) map[string]any {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	switch action {
	case "update", "wait_tasks", "propose_task", "create_task":
	default:
		return args
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	text := func(key string) string { return strings.TrimSpace(asString(out[key])) }
	switch action {
	case "update":
		if text("id") == "" && text("project_id") != "" {
			out["id"] = text("project_id")
		}
		delete(out, "project_id")
		if markdown, ok := out["project_md"]; ok {
			if _, own := out["project_context"]; !own {
				out["project_context"] = markdown
			}
			delete(out, "project_md")
		}
	case "wait_tasks":
		if _, own := out["task_ids"]; !own && text("task_id") != "" {
			out["task_ids"] = []any{text("task_id")}
		}
		delete(out, "task_id")
	case "propose_task", "create_task":
		if text("prompt") == "" && text("brief") != "" {
			out["prompt"] = text("brief")
		}
		delete(out, "brief")
		if workspace := text("workspace"); workspace != "" && text("workspace_path") == "" && text("workspace_id") == "" {
			if filepath.IsAbs(workspace) {
				out["workspace_path"] = filepath.Clean(workspace)
			} else {
				out["workspace_id"] = workspace
			}
		}
		delete(out, "workspace")
		if scope := TaskFilesScope(out["files"]); scope != "" && text("prompt") != "" {
			out["prompt"] = text("prompt") + scope
		}
		delete(out, "files")
	}
	return out
}

// projectUpdateEdits are the inputs an update changes a project with.
var projectUpdateEdits = []string{"name", "description", "workspaces", "project_context", "theme_id"}

// ProjectContextRetryCall reports an update that names a project and changes
// nothing: it writes the project's project.md again from its folders, the
// effect of swarm_retry_project_context (refused while an attempt runs).
// Only an explicit update counts: an omitted action is list, a read the
// permission gate lets run unapproved, and must never write.
func ProjectContextRetryCall(args map[string]any) bool {
	if strings.ToLower(strings.TrimSpace(asString(args["action"]))) != "update" {
		return false
	}
	return ProjectUpdateChangesNothing(args)
}

// ProjectUpdateChangesNothing reports update arguments that name a project
// (id) and no edit. The service checks it on a call already routed as
// update, whose arguments no longer carry the action.
func ProjectUpdateChangesNothing(args map[string]any) bool {
	if strings.TrimSpace(asString(args["id"])) == "" {
		return false
	}
	for _, key := range projectUpdateEdits {
		if _, ok := args[key]; ok {
			return false
		}
	}
	return true
}

// manageProjectsSwarmExtras adds to an answer what its swarm_* tool's
// options asked for, from the one implementation that tool used:
//   - get_task include_diff (swarm_task_result): the patch against the base,
//     cut at diff_chars, read as get_task_changes reads it (swarm_get_changes);
//   - list_tasks include_linked (swarm_list_tasks): the project's tasks sent
//     to linked machines, as list_linked_tasks lists them.
//
// A part that cannot be read is named in the answer (changes_error,
// linked_tasks_error) and the rest of the answer stands.
func (r *Runtime) manageProjectsSwarmExtras(ctx context.Context, scope WorkspaceScope, action string, args map[string]any, answer string) (string, error) {
	includeDiff := action == "get_task" && asBool(args["include_diff"])
	includeLinked := action == "list_tasks" && asBool(args["include_linked"]) && strings.TrimSpace(asString(args["machine"])) == ""
	if !includeDiff && !includeLinked {
		return answer, nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(answer), &object); err != nil {
		return answer, nil
	}
	// A linked answer carries the machine's answer in data.
	target := object
	if data, ok := object["data"].(map[string]any); ok && strings.TrimSpace(asString(args["machine"])) != "" {
		target = data
	}
	if includeDiff {
		read := map[string]any{"action": TaskChangesAction, "project_id": args["project_id"], "task_id": args["task_id"]}
		if machine := strings.TrimSpace(asString(args["machine"])); machine != "" {
			read["machine"] = machine
		}
		read["diff_chars"] = float64(asInt(args["diff_chars"], 20000))
		catalog := manageProjectsDeclared()
		declared, _ := catalog.Action(TaskChangesAction)
		changes, err := r.catalogServiceAnswer(ctx, scope, catalog, declared, read)
		var decoded map[string]any
		if err == nil {
			err = json.Unmarshal([]byte(changes), &decoded)
		}
		if err != nil {
			target["changes_error"] = truncateRunes(err.Error(), 300)
		} else {
			if data, ok := decoded["data"].(map[string]any); ok {
				decoded = data
			}
			for _, key := range []string{"diff", "diff_truncated", "diff_note"} {
				if value, ok := decoded[key]; ok {
					target[key] = value
				}
			}
		}
	}
	if includeLinked {
		listed, err := r.executeListLinkedTasks(ctx, scope, map[string]any{"action": "list_linked_tasks", "project_id": args["project_id"]})
		var decoded map[string]any
		if err == nil {
			err = json.Unmarshal([]byte(listed), &decoded)
		}
		if err != nil {
			target["linked_tasks_error"] = truncateRunes(err.Error(), 300)
		} else {
			target["linked_tasks"] = decoded["linked_tasks"]
		}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// truncateRunes cuts text to at most limit runes.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// ControlSwarmAnswers is implemented by the API server: it runs a swarm_*
// tool's own handler (swarm_get_project, swarm_wait_task)
// or its model check for a Swarm Control caller, under that caller's key.
// A catalog action takes those tools' Swarm Control inputs (held waits, a
// task's model) and answers their fields through it.
type ControlSwarmAnswers interface {
	ControlSwarmAnswer(ctx context.Context, p identity.Principal, who, swarmTool string, args map[string]any) (map[string]any, error)
	ControlModelPreference(ctx context.Context, p identity.Principal, who string, model map[string]any) (map[string]any, error)
}

// controlSwarmAnswers is the API server's ControlSwarmAnswers and the Swarm
// Control caller it runs for.
func (r *Runtime) controlSwarmAnswers(scope WorkspaceScope) (ControlSwarmAnswers, string, error) {
	who, err := catalogServiceCaller(scope, "manage_projects")
	if err != nil {
		return nil, "", err
	}
	service, ok := r.getWorkflowLifecycleService().(ControlSwarmAnswers)
	if !ok {
		return nil, "", errors.New("manage_projects: Swarm Control options are unavailable on this machine")
	}
	return service, who, nil
}

// manageProjectsControlBefore applies a Swarm Control caller's inputs that
// act before the action runs, here (not through a link, whose machine runs
// the call as a Swarm Control one itself):
//   - get wait_seconds (swarm_get_project): wait until project.md is written;
//     the answer's waited is kept for the reply;
//   - propose_task model (swarm_run_task): checked as that tool checks it and
//     given to the task as its provider, model, thinking and service tier.
func (r *Runtime) manageProjectsControlBefore(ctx context.Context, scope WorkspaceScope, action string, args map[string]any) (map[string]any, map[string]any, error) {
	if !scope.ControlCaller || strings.TrimSpace(asString(args["machine"])) != "" {
		return args, nil, nil
	}
	switch action {
	case "get":
		seconds := asInt(args["wait_seconds"], 0)
		if seconds <= 0 {
			return args, nil, nil
		}
		project := strings.TrimSpace(asString(args["project_id"]))
		if project == "" {
			project = strings.TrimSpace(asString(args["id"]))
		}
		service, who, err := r.controlSwarmAnswers(scope)
		if err != nil {
			return nil, nil, err
		}
		waited, err := service.ControlSwarmAnswer(ctx, scope.Principal, who, "swarm_get_project", map[string]any{"project_id": project, "wait_seconds": float64(seconds)})
		return args, waited, err
	case "propose_task", "create_task":
		model, ok := args["model"].(map[string]any)
		if !ok {
			return args, nil, nil
		}
		service, who, err := r.controlSwarmAnswers(scope)
		if err != nil {
			return nil, nil, err
		}
		preference, err := service.ControlModelPreference(ctx, scope.Principal, who, model)
		if err != nil {
			return nil, nil, err
		}
		out := make(map[string]any, len(args)+3)
		for key, value := range args {
			if key != "model" {
				out[key] = value
			}
		}
		for key, value := range preference {
			out[key] = value
		}
		return out, nil, nil
	}
	return args, nil, nil
}

// manageProjectsControlAfter adds to a Swarm Control caller's answer the
// fields its swarm_* tool answered, from that tool's own handler; the
// action's own fields win:
//   - get: what swarm_get_project answered while it waited (waited);
//   - wait_tasks for one task: swarm_wait_task's answer read at once
//     (task_id, session_id, settled), after the action's own wait;
//   - propose_task wait_seconds: swarm_wait_task's wait for the new task.
func (r *Runtime) manageProjectsControlAfter(ctx context.Context, scope WorkspaceScope, action string, args map[string]any, before map[string]any, answer string) (string, error) {
	if !scope.ControlCaller || strings.TrimSpace(asString(args["machine"])) != "" {
		return answer, nil
	}
	if _, wired := r.getWorkflowLifecycleService().(ControlSwarmAnswers); !wired && before == nil {
		// A runtime without the API server (no swarm_* handlers to run)
		// answers with the action's own fields. Inputs the caller gave
		// (waits, a model) still fail without it, in the before step.
		return answer, nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(answer), &object); err != nil {
		return answer, nil
	}
	extra := before
	project := strings.TrimSpace(asString(args["project_id"]))
	var err error
	switch action {
	case "wait_tasks":
		ids, _ := projectTaskWatchIDs(args)
		if len(ids) != 1 {
			return answer, nil
		}
		extra, err = r.controlSwarmAnswer(ctx, scope, "swarm_wait_task", map[string]any{"project_id": project, "task_id": ids[0], "wait_seconds": float64(0)})
	case "propose_task", "create_task":
		seconds := asInt(args["wait_seconds"], 0)
		taskID := strings.TrimSpace(asString(object["task_id"]))
		if seconds <= 0 || taskID == "" {
			return answer, nil
		}
		if project == "" {
			project = strings.TrimSpace(asString(object["project_id"]))
		}
		extra, err = r.controlSwarmAnswer(ctx, scope, "swarm_wait_task", map[string]any{"project_id": project, "task_id": taskID, "wait_seconds": float64(seconds)})
	}
	if err != nil {
		return "", err
	}
	if len(extra) == 0 {
		return answer, nil
	}
	for key, value := range extra {
		if _, own := object[key]; !own {
			object[key] = value
		}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (r *Runtime) controlSwarmAnswer(ctx context.Context, scope WorkspaceScope, swarmTool string, args map[string]any) (map[string]any, error) {
	service, who, err := r.controlSwarmAnswers(scope)
	if err != nil {
		return nil, err
	}
	return service.ControlSwarmAnswer(ctx, scope.Principal, who, swarmTool, args)
}
