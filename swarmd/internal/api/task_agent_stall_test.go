package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// agentStallEvents builds a run's session events, newest first, as the
// tool invoker records them (session.tool.started with tool_name and
// arguments; an end event with the same call id).
type agentStallEvents struct {
	t      *testing.T
	runID  string
	events []pebblestore.V3SessionEvent
	seq    uint64
}

func (e *agentStallEvents) add(eventType string, at int64, payload map[string]any) {
	e.t.Helper()
	payload["run_id"] = e.runID
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	e.seq++
	// Newest first, as ListSessionEventsBefore returns them.
	e.events = append([]pebblestore.V3SessionEvent{{SessionID: "task-session", Seq: e.seq, EventType: eventType, TsUnixMs: at, Payload: raw}}, e.events...)
}

// call records one tool call starting at start and, when end > 0, ending.
func (e *agentStallEvents) call(id, tool string, args map[string]any, start, end int64) {
	e.t.Helper()
	raw, _ := json.Marshal(args)
	e.add("session.tool.started", start, map[string]any{"call_id": id, "tool_name": tool, "arguments": string(raw)})
	if end > 0 {
		e.add("session.tool.completed", end, map[string]any{"call_id": id, "tool_name": tool})
	}
}

func agentStallDetect(t *testing.T, e *agentStallEvents, startedAt, now int64) *pebblestore.ProjectTaskAgentStall {
	t.Helper()
	calls, last := agentStallCallsFromEvents(e.events, e.runID, startedAt)
	return detectAgentStall(calls, last, now, agentStallThresholdsFrom(pebblestore.DefaultLimits()))
}

// Purpose: the task progress names a running run's agent stall from its own
// session events, with the account's thresholds: (a) nothing but sleep and
// status polls of one background command past agent_poll_stall_minutes,
// (b) one call running past agent_command_stall_minutes, (c) no tool call or
// message past agent_idle_stall_minutes. The stall's start is stable while
// it lasts (one episode), and real work, a proper bash wait or a second
// background command is not a poll stall. Threat (observed): coder agents
// looped `sleep 200`/`sleep 290` plus a status check for 10+ minutes and
// nothing flagged it; or false alarms on an agent that is working.
// Authority: agentStallCallsFromEvents and detectAgentStall (read by
// taskAgentStall for taskWaitProgressFor and the stall check's
// noteTaskAgentStall). Layer: the pure decision over recorded event shapes,
// the narrowest layer that holds every threshold and boundary.
func TestAgentStallSleepPollingDetectedOncePerEpisode(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()
	e := &agentStallEvents{t: t, runID: "run-1"}
	e.call("c0", "bash", map[string]any{"command": "go test ./...", "background": true}, t0, t0+1000)
	// From t0+1m: sleep, status, sleep, status ... (3.5 min a round).
	at := t0 + m
	for i := 0; i < 4; i++ {
		e.call("s"+string(rune('a'+i)), "bash", map[string]any{"command": "sleep 200"}, at, at+200_000)
		e.call("p"+string(rune('a'+i)), "mcp__swarm__bash", map[string]any{"background_id": "bg_1", "command": "status"}, at+201_000, at+202_000)
		at += 210_000
	}
	if stall := agentStallDetect(t, e, t0, t0+10*m); stall != nil {
		t.Fatalf("polling for 9 min (under the 10 min default) flagged: %+v", stall)
	}
	stall := agentStallDetect(t, e, t0, t0+15*m)
	if stall == nil || stall.Kind != pebblestore.ProjectTaskAgentStallSleepPolling || stall.BackgroundID != "bg_1" || stall.Since != t0+m || stall.Calls != 8 || stall.ForMs != 14*m || stall.Command != "sleep 200" {
		t.Fatalf("sleep polling not named: %+v", stall)
	}
	// It goes on: the same episode (same start), only longer.
	e.call("s-more", "bash", map[string]any{"command": "sleep 290 && tail -5 build.log"}, at, 0)
	later := agentStallDetect(t, e, t0, t0+20*m)
	if later == nil || later.Episode() != stall.Episode() || later.ForMs != 19*m {
		t.Fatalf("one episode changed identity: %+v vs %+v", later, stall)
	}
	// A proper wait ends the poll streak: no stall from polling.
	e.call("w", "bash", map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}, t0+21*m, t0+22*m)
	if got := agentStallDetect(t, e, t0, t0+23*m); got != nil {
		t.Fatalf("a bash wait counted as polling: %+v", got)
	}
	// Status reads of two different background commands are not one poll loop.
	f := &agentStallEvents{t: t, runID: "run-1"}
	f.call("x1", "bash", map[string]any{"background_id": "bg_1"}, t0, t0+1000)
	f.call("x2", "bash", map[string]any{"command": "sleep 300"}, t0+m, t0+6*m)
	f.call("x3", "bash", map[string]any{"background_id": "bg_2", "command": "status"}, t0+6*m, t0+6*m+1000)
	if got := agentStallDetect(t, f, t0, t0+12*m); got != nil && got.Kind == pebblestore.ProjectTaskAgentStallSleepPolling && got.Since == t0 {
		t.Fatalf("polls of two commands joined into one streak: %+v", got)
	}
	// Another run's calls are not this run's.
	if calls, _ := agentStallCallsFromEvents(e.events, "run-2", t0); len(calls) != 0 {
		t.Fatalf("calls of another run read: %d", len(calls))
	}
}

// Purpose (parity): one agent stall reads the same on every surface: the
// task route's progress (taskProgress.AgentStall), the wake row's progress
// (ProjectTaskWaitProgressFrom, fed by the local hook and a linked
// machine's get_task alike), get_task's compact view (native and Swarm
// Control, tool.CompactProjectTaskAnswer) and Swarm Control's wait milestone
// (ProjectTaskMilestone). Threat: the API, the wake and MCP drifting apart, so
// a watcher or an outside AI cannot see what the Orchestrator sees.
// Layer: pure functions over the route's own JSON, the narrowest layer that
// holds all surfaces together.
func TestAgentStallMatchesAcrossRouteWakeAndGetTask(t *testing.T) {
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Build", Status: "running", Agent: "coder"}
	p := computeTaskProgress(task, nil, nil, 30000)
	p.AgentStall = &pebblestore.ProjectTaskAgentStall{Kind: pebblestore.ProjectTaskAgentStallSleepPolling, Command: "sleep 290", BackgroundID: "bg_1", Calls: 6, Since: 1790000000000, ForMs: 720000}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	wake := pebblestore.ProjectTaskWaitProgressFrom(json.RawMessage(raw))
	if wake == nil || !reflect.DeepEqual(wake.AgentStall, p.AgentStall) {
		t.Fatalf("wake progress drifted from the route: %+v", wake)
	}
	if got := pebblestore.ProjectTaskMilestone(task.Status, wake); got != pebblestore.ProjectTaskMilestoneStalled {
		t.Fatalf("control wait milestone: %q", got)
	}
	route, err := json.Marshal(map[string]any{"task": newTaskWithProgress(&task, &p)})
	if err != nil {
		t.Fatal(err)
	}
	compact, err := tool.CompactProjectTaskAnswer("get_task", "", route)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Task struct {
			Progress struct {
				AgentStall *pebblestore.ProjectTaskAgentStall `json:"agent_stall"`
			} `json:"progress"`
		} `json:"task"`
	}
	if err := json.Unmarshal(compact, &view); err != nil || !reflect.DeepEqual(view.Task.Progress.AgentStall, p.AgentStall) {
		t.Fatalf("get_task drifted from the route: %s (%v)", compact, err)
	}
}

func TestAgentStallLongCommandAndNoActivity(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()

	// (b) One command still running past the 20 min default.
	e := &agentStallEvents{t: t, runID: "run-1"}
	e.call("r", "read", map[string]any{"path": "main.go"}, t0, t0+500)
	e.call("b", "bash", map[string]any{"command": "go test -count=1 ./...\necho done"}, t0+m, 0)
	e.add("session.tool.delta", t0+15*m, map[string]any{"call_id": "b", "tool_name": "bash"})
	if got := agentStallDetect(t, e, t0, t0+20*m); got != nil {
		t.Fatalf("19 min command flagged: %+v", got)
	}
	long := agentStallDetect(t, e, t0, t0+25*m)
	if long == nil || long.Kind != pebblestore.ProjectTaskAgentStallLongCommand || long.Command != "go test -count=1 ./..." || long.Since != t0+m || long.ForMs != 24*m {
		t.Fatalf("long command not named: %+v", long)
	}
	// A bash wait in flight is bounded by its own cap: never a long command.
	w := &agentStallEvents{t: t, runID: "run-1"}
	w.call("w", "bash", map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}, t0, 0)
	if got := agentStallDetect(t, w, t0, t0+40*m); got != nil {
		t.Fatalf("a bash wait flagged: %+v", got)
	}

	// (c) Nothing in flight and no tool call or message past 15 min.
	q := &agentStallEvents{t: t, runID: "run-1"}
	q.call("e", "edit", map[string]any{"path": "a.go"}, t0, t0+2000)
	if got := agentStallDetect(t, q, t0, t0+14*m); got != nil {
		t.Fatalf("14 min quiet flagged: %+v", got)
	}
	idle := agentStallDetect(t, q, t0, t0+20*m)
	if idle == nil || idle.Kind != pebblestore.ProjectTaskAgentStallNoActivity || idle.Since != t0+2000 || idle.Command != "edit" {
		t.Fatalf("no activity not named: %+v", idle)
	}
	// A message is activity: the quiet episode ends, and a later one has a
	// new start (a new episode).
	q.add("session.assistant.completed", t0+19*m, map[string]any{})
	if got := agentStallDetect(t, q, t0, t0+20*m); got != nil {
		t.Fatalf("a run that just spoke flagged: %+v", got)
	}
	again := agentStallDetect(t, q, t0, t0+40*m)
	if again == nil || again.Episode() == idle.Episode() || again.Since != t0+19*m {
		t.Fatalf("a new quiet episode kept the old identity: %+v", again)
	}
	// Thresholds come from the account's limits.
	limits := pebblestore.DefaultLimits()
	limits.AgentIdleStallMinutes = 60
	calls, last := agentStallCallsFromEvents(q.events, "run-1", t0)
	if got := detectAgentStall(calls, last, t0+40*m, agentStallThresholdsFrom(limits)); got != nil {
		t.Fatalf("configured idle threshold ignored: %+v", got)
	}
}
