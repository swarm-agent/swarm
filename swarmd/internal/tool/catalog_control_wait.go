package tool

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Swarm Control's wait_tasks. A task watch wakes the conversation that
// recorded it; a Swarm Control caller (the person's own AI) has none. So its
// wait_tasks holds the call instead: until any listed task reaches its next
// milestone (pebblestore.ProjectTaskMilestone: started, needs you, review
// started, sent back, landed, done, blocked, failed, stopped) or wait_seconds
// passes. It wakes on the same sources a watch does (committed realtime
// outbox records here; the linked machine's activity feed there), re-reads
// the tasks only when woken, and returns them as get_task reads them.

// Swarm Control wait bounds, in seconds.
const (
	ControlTaskWaitDefaultSeconds = 60
	ControlTaskWaitMaxSeconds     = 110
)

// States of a Swarm Control wait_tasks answer.
const (
	// ControlTaskWaitMilestone: a listed task reached a new milestone.
	ControlTaskWaitMilestone = "milestone"
	// ControlTaskWaitSettled: every listed task had already settled when
	// the call came; nothing more will happen to wait for.
	ControlTaskWaitSettled = "settled"
	// ControlTaskWaitTimedOut: wait_seconds passed without a milestone.
	ControlTaskWaitTimedOut = "timed_out"
)

// ProjectTaskChanges is a follow of one project's tasks: C receives (at most
// one pending signal) when something about them may have changed. Pin adds
// task sessions whose events concern the follow.
type ProjectTaskChanges interface {
	C() <-chan struct{}
	Pin(sessionIDs ...string)
}

// ProjectTaskChangeFollower is implemented by the daemon's API server: it
// follows a project's tasks here (machine "") or on a linked machine until
// ctx ends.
type ProjectTaskChangeFollower interface {
	FollowProjectTaskChanges(ctx context.Context, p identity.Principal, machine, projectID string) (ProjectTaskChanges, error)
}

// controlTaskReading is one read of a listed task: its get_task view, and
// the status, progress and session the milestone is named from.
type controlTaskReading struct {
	view      any
	status    string
	progress  *pebblestore.V3ProjectTaskWaitProgress
	sessionID string
}

// controlWaitSeconds reads wait_seconds: default 60, at most 110.
func controlWaitSeconds(args map[string]any) (int, error) {
	raw, ok := args["wait_seconds"]
	if !ok || raw == nil {
		return ControlTaskWaitDefaultSeconds, nil
	}
	var seconds float64
	switch v := raw.(type) {
	case float64:
		seconds = v
	case int:
		seconds = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, errors.New("wait_seconds must be a number of seconds")
		}
		seconds = f
	default:
		return 0, errors.New("wait_seconds must be a number of seconds")
	}
	if seconds < 0 {
		return 0, errors.New("wait_seconds must not be negative")
	}
	return min(int(seconds), ControlTaskWaitMaxSeconds), nil
}

// readControlTask reads one listed task through get_task, here or through
// the link (machine), as the Orchestrator's get_task does.
func (r *Runtime) readControlTask(ctx context.Context, scope WorkspaceScope, machine, projectID, taskID string) (controlTaskReading, error) {
	read := map[string]any{"action": "get_task", "project_id": projectID, "task_id": taskID}
	if machine != "" {
		// One read of the machine's full record (detail full only skips the
		// home's compaction): its status and progress name the milestone,
		// and the view is that answer compacted as get_task compacts it.
		read["machine"], read["detail"] = machine, ProjectTaskDetailFull
		answer, err := r.executeFleetProjects(ctx, scope, read)
		if err != nil {
			return controlTaskReading{}, err
		}
		var decoded struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			return controlTaskReading{}, err
		}
		// The machine's record (untrusted): status, progress, attempts.
		var record struct {
			Task struct {
				pebblestore.ProjectTaskRecord
				Progress json.RawMessage `json:"progress"`
			} `json:"task"`
		}
		_ = json.Unmarshal(decoded.Data, &record)
		compact, err := CompactProjectTaskAnswer("get_task", "", decoded.Data)
		if err != nil {
			return controlTaskReading{}, err
		}
		var view any
		if err := json.Unmarshal(compact, &view); err != nil {
			return controlTaskReading{}, err
		}
		task := record.Task.ProjectTaskRecord
		task.EnsureTaskAttempts()
		out := controlTaskReading{view: view, status: task.Status}
		if len(record.Task.Progress) > 0 {
			out.progress = pebblestore.ProjectTaskWaitProgressFrom(record.Task.Progress)
		}
		if a := task.ActiveAttempt(); a != nil {
			out.sessionID = a.SessionID
		}
		return out, nil
	}
	answer, err := r.executeManageProjectTasksContext(ctx, scope, "get_task", read)
	if err != nil {
		return controlTaskReading{}, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		return controlTaskReading{}, err
	}
	out := controlTaskReading{view: decoded["task"]}
	if r.projects != nil {
		account := scope.Principal.AccountScopeID
		task, found, err := r.projects.GetProjectTask(account, projectID, taskID)
		if err != nil {
			return controlTaskReading{}, err
		}
		if found && task != nil {
			task.EnsureTaskAttempts()
			out.status = task.Status
			out.progress = pebblestore.ProjectTaskWaitProgressFrom(r.projectTaskProgress(account, *task))
			if a := task.ActiveAttempt(); a != nil {
				out.sessionID = a.SessionID
			}
			if out.sessionID == "" {
				out.sessionID = task.SessionID
			}
		}
	}
	return out, nil
}

// controlTaskWait answers wait_tasks for a Swarm Control caller: it holds
// the call until a listed task reaches a new milestone or wait_seconds
// passes, then returns the tasks as get_task reads them. Nothing is
// recorded; cancel_watch has no watch to cancel and says so.
func (r *Runtime) controlTaskWait(ctx context.Context, scope WorkspaceScope, machine string, args map[string]any) (string, error) {
	if !scope.ControlCaller {
		return "", errors.New("a held wait is only for Swarm Control callers")
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
	seconds, err := controlWaitSeconds(args)
	if err != nil {
		return "", err
	}
	follower, ok := r.getProjectTaskLifecycleService().(ProjectTaskChangeFollower)
	if !ok {
		return "", errors.New("wait_tasks: task changes cannot be followed on this machine")
	}
	started := time.Now()
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	// Follow first, then read: a change between the two still wakes.
	changes, err := follower.FollowProjectTaskChanges(waitCtx, scope.Principal, machine, projectID)
	if err != nil {
		return "", err
	}
	readAll := func() ([]controlTaskReading, error) {
		out := make([]controlTaskReading, 0, len(ids))
		for _, id := range ids {
			// A read outlives the wait's deadline: the answer is always read.
			reading, err := r.readControlTask(context.WithoutCancel(ctx), scope, machine, projectID, id)
			if err != nil {
				return nil, err
			}
			if reading.sessionID != "" {
				changes.Pin(reading.sessionID)
			}
			out = append(out, reading)
		}
		return out, nil
	}
	baseline, err := readAll()
	if err != nil {
		return "", err
	}
	milestones := func(readings []controlTaskReading) []string {
		out := make([]string, len(readings))
		for i, reading := range readings {
			out[i] = pebblestore.ProjectTaskMilestone(reading.status, reading.progress)
		}
		return out
	}
	before := milestones(baseline)
	state, current, reached := "", baseline, map[string]string{}
	settled := true
	for _, reading := range baseline {
		settled = settled && pebblestore.ProjectTaskWaitSettled(reading.status, until, reading.progress)
	}
	if settled {
		state = ControlTaskWaitSettled
	}
	for state == "" {
		select {
		case <-changes.C():
			if current, err = readAll(); err != nil {
				return "", err
			}
			for i, milestone := range milestones(current) {
				if milestone != "" && milestone != before[i] {
					reached[ids[i]] = milestone
				}
			}
			if len(reached) > 0 {
				state = ControlTaskWaitMilestone
			}
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if current, err = readAll(); err != nil {
				return "", err
			}
			state = ControlTaskWaitTimedOut
		}
	}
	tasks := make([]map[string]any, 0, len(ids))
	after := milestones(current)
	for i, id := range ids {
		row := map[string]any{"task_id": id, "task": current[i].view}
		if after[i] != "" {
			row["milestone"] = after[i]
		}
		if milestone, ok := reached[id]; ok {
			row["reached"] = milestone
		}
		tasks = append(tasks, row)
	}
	sort.SliceStable(tasks, func(a, b int) bool { return tasks[a]["reached"] != nil && tasks[b]["reached"] == nil })
	result := map[string]any{"project_id": projectID, "status": state, "task_ids": ids, "until": until, "tasks": tasks, "waited_seconds": int(time.Since(started).Seconds()), "next_action": controlTaskWaitNextAction(state)}
	if machine != "" {
		result["machine"], result["untrusted_content"] = machine, true
	}
	out, err := json.Marshal(result)
	return string(out), err
}

// controlTaskWaitNextAction is the next step of a held wait.
func controlTaskWaitNextAction(state string) string {
	switch state {
	case ControlTaskWaitMilestone:
		return "A listed task reached a milestone (reached); tell the person, and call wait_tasks again to wait for the next one."
	case ControlTaskWaitSettled:
		return "Every listed task has settled; read one with get_task, or wait on other tasks."
	}
	return "No listed task reached a new milestone within wait_seconds; call wait_tasks again to keep waiting."
}
