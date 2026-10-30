package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ControlTaskSnapshotNextAction is wait_tasks' next step for a Swarm Control
// caller.
const ControlTaskSnapshotNextAction = "Nothing is watched for a Swarm Control caller: it has no conversation turn to wake. These are the tasks as they stand now; read one again with get_task when the person asks."

// controlTaskSnapshot answers wait_tasks for a Swarm Control caller. A watch
// wakes the conversation that recorded it, and this caller has none, so
// nothing is watched (status not_watched, never watching): each listed task
// is read once with get_task, here or through the link (machine), and
// returned as it stands. cancel_watch has no watch to cancel and says so.
func (r *Runtime) controlTaskSnapshot(ctx context.Context, scope WorkspaceScope, machine string, args map[string]any) (string, error) {
	if !scope.ControlCaller {
		return "", errors.New("task snapshot is only for Swarm Control callers")
	}
	if asBool(args["cancel"]) {
		return "", errors.New("cancel_watch: nothing is watched for a Swarm Control caller, so there is no watch to cancel")
	}
	projectID := strings.TrimSpace(asString(args["project_id"]))
	if projectID == "" {
		return "", errors.New("project_id is required")
	}
	ids, err := projectTaskWatchIDs(args)
	if err != nil {
		return "", err
	}
	until, err := pebblestore.NormalizeProjectTaskWaitUntil(asString(args["until"]))
	if err != nil {
		return "", err
	}
	tasks := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		read := map[string]any{"action": "get_task", "project_id": projectID, "task_id": id}
		var answer string
		if machine != "" {
			read["machine"] = machine
			answer, err = r.executeFleetProjects(ctx, scope, read)
		} else {
			answer, err = r.executeManageProjectTasksContext(ctx, scope, "get_task", read)
		}
		if err != nil {
			return "", err
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			return "", err
		}
		view := decoded["task"]
		if machine != "" {
			view = decoded["data"]
		}
		tasks = append(tasks, map[string]any{"task_id": id, "task": view})
	}
	result := map[string]any{"tool": manageProjectsToolName, "action": "wait_tasks", "project_id": projectID, "status": "not_watched", "task_ids": ids, "until": until, "tasks": tasks, "next_action": ControlTaskSnapshotNextAction}
	if machine != "" {
		result["machine"], result["untrusted_content"] = machine, true
	}
	out, err := json.Marshal(result)
	return string(out), err
}
