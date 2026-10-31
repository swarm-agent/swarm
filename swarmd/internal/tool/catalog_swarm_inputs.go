package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
func ProjectContextRetryCall(args map[string]any) bool {
	if action := strings.ToLower(strings.TrimSpace(asString(args["action"]))); action != "" && action != "update" {
		return false
	}
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
