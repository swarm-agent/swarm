package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// manage_projects wait_tasks watches tasks in the background: it records a
// durable watch on this conversation and returns at once. The run goes on, the
// AI answers the person and ends its turn normally; when the watched tasks
// settle the daemon starts a new turn with their outcomes. watch_tasks is an
// alias; cancel_watch (or wait_tasks with cancel=true) ends watches.

// ProjectTaskWatchAction maps a manage_projects action to wait_tasks: ok for
// wait_tasks, watch_tasks and cancel_watch; cancel for cancel_watch.
func ProjectTaskWatchAction(action string) (cancel, ok bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait_tasks", "watch_tasks":
		return false, true
	case "cancel_watch":
		return true, true
	}
	return false, false
}

// normalizeProjectTaskWatchArgs rewrites watch_tasks and cancel_watch to the
// one wait_tasks implementation (cancel_watch sets cancel).
func normalizeProjectTaskWatchArgs(args map[string]any) map[string]any {
	cancel, ok := ProjectTaskWatchAction(asString(args["action"]))
	if !ok || asString(args["action"]) == "wait_tasks" {
		return args
	}
	out := make(map[string]any, len(args)+1)
	for key, value := range args {
		out[key] = value
	}
	out["action"] = "wait_tasks"
	if cancel {
		out["cancel"] = true
	}
	return out
}

// TaskWatchFollower is implemented by the daemon's API server: it follows a
// linked-machine watch from that machine once the watch is recorded.
type TaskWatchFollower interface {
	FollowTaskWatch(sessionID, watchID string)
}

// projectTaskWatchRequestKey is the registration's idempotency key: one per
// run and task selection, so a retried call is the same watch.
func projectTaskWatchRequestKey(runID, machine, projectID, until string, ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join([]string{machine, projectID, until, strings.Join(sorted, "\x00")}, "\x01")))
	return "task-watch:" + runID + ":" + hex.EncodeToString(sum[:12])
}

// projectTaskWatchIDs reads 1-16 task_ids.
func projectTaskWatchIDs(args map[string]any) ([]string, error) {
	var ids []string
	raw, err := json.Marshal(args["task_ids"])
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &ids); err != nil || len(ids) == 0 || len(ids) > 16 {
		return nil, errors.New("task_ids must contain 1-16 task IDs")
	}
	return ids, nil
}

// registerProjectTaskWatch commits the watch and answers with status
// watching. Tasks already watched by this conversation are not watched twice.
func (r *Runtime) registerProjectTaskWatch(scope WorkspaceScope, op *pebblestore.V3ProjectTaskWaitMutation, hash string, ids, titles []string) (map[string]any, error) {
	key := projectTaskWatchRequestKey(op.RunID, op.Machine, op.ProjectID, op.Until, ids)
	result := map[string]any{"tool": "manage_projects", "action": "wait_tasks", "project_id": op.ProjectID, "status": "watching", "task_ids": ids, "until": op.Until}
	if op.Machine != "" {
		result["machine"], result["untrusted_content"] = op.Machine, true
	}
	_, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID,
		Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.started",
		ClientRequestID: key, PayloadHash: hash, TaskWait: op,
	})
	switch {
	case errors.Is(err, pebblestore.ErrProjectTaskAlreadyWatched):
		result["already_watching"] = true
		result["notice"] = "Already watching " + strings.Join(titles, ", ") + ". Reply to the person now and end your turn; a new turn starts with the outcomes when they settle."
		return result, nil
	case err != nil:
		return nil, err
	}
	watchID, watched := op.RegisteredWatchID, op.RegisteredTitles
	if watchID == "" {
		// A retried call replays the committed registration.
		watchID, watched = pebblestore.ProjectTaskWatchID(scope.SessionID, op.RunID, op.Machine, op.ProjectID, op.Until, ids), titles
	} else if len(op.RegisteredTaskIDs) < len(ids) {
		covered := map[string]bool{}
		for _, id := range op.RegisteredTaskIDs {
			covered[id] = true
		}
		var already []string
		for _, id := range ids {
			if !covered[id] {
				already = append(already, id)
			}
		}
		result["task_ids"], result["already_watching_task_ids"] = op.RegisteredTaskIDs, already
	}
	result["watch_id"] = watchID
	result["notice"] = pebblestore.ProjectTaskWatchNotice(watched, op.Machine)
	if follower, ok := r.getProjectTaskLifecycleService().(TaskWatchFollower); ok && op.Machine != "" {
		follower.FollowTaskWatch(scope.SessionID, watchID)
	}
	return result, nil
}

// cancelProjectTaskWatches ends this conversation's watches on task_ids (all
// of its watches in the project, or on the machine, when task_ids is empty).
func (r *Runtime) cancelProjectTaskWatches(scope WorkspaceScope, runID, machine, projectID string, args map[string]any) (string, error) {
	var ids []string
	if args["task_ids"] != nil {
		var err error
		if ids, err = projectTaskWatchIDs(args); err != nil {
			return "", err
		}
	}
	op := &pebblestore.V3ProjectTaskWaitMutation{Cancel: true, CancelledBy: pebblestore.ProjectTaskWaitEndedByAgent, Machine: machine, ProjectID: projectID, TaskIDs: ids, WatchID: strings.TrimSpace(asString(args["watch_id"]))}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join([]string{runID, machine, projectID, op.WatchID, strings.Join(sorted, "\x00")}, "\x01")))
	key := "task-watch-cancel:" + hex.EncodeToString(sum[:16])
	_, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID,
		Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.cancelled",
		ClientRequestID: key, PayloadHash: key, TaskWait: op,
	})
	if err != nil {
		if errors.Is(err, pebblestore.ErrProjectTaskWatchNotFound) {
			return "", errors.New("no watch on this conversation matches; nothing was cancelled")
		}
		return "", err
	}
	cancelled := op.CancelledTaskIDs
	if len(cancelled) == 0 {
		cancelled = ids
	}
	result := map[string]any{"tool": "manage_projects", "action": "wait_tasks", "project_id": projectID, "status": "cancelled", "task_ids": cancelled}
	if machine != "" {
		result["machine"] = machine
	}
	out, err := json.Marshal(result)
	return string(out), err
}
