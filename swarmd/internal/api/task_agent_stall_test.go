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
	e.callResult(id, tool, args, start, end, nil)
}

// callResult is call whose end event also carries result fields, as
// providerManagedV3ToolBackgroundFields copies them from a bash result
// (background_id, still_running).
func (e *agentStallEvents) callResult(id, tool string, args map[string]any, start, end int64, result map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(args)
	e.add("session.tool.started", start, map[string]any{"call_id": id, "tool_name": tool, "arguments": string(raw)})
	if end > 0 {
		payload := map[string]any{"call_id": id, "tool_name": tool}
		for k, v := range result {
			payload[k] = v
		}
		e.add("session.tool.completed", end, payload)
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
	// A proper wait ends the poll streak: no stall from polling. The
	// background command it waits on has run 23 min since its start (t0),
	// which is a long command.
	e.callResult("w", "bash", map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}, t0+21*m, t0+22*m, map[string]any{"background_id": "bg_1", "still_running": true})
	if got := agentStallDetect(t, e, t0, t0+23*m); got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Since != t0 || got.BackgroundID != "bg_1" {
		t.Fatalf("a bash wait counted as polling, or its command not timed: %+v", got)
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
	// One bash wait in flight (capped at 10 min) under the threshold is no
	// stall, and not an idle run either.
	w := &agentStallEvents{t: t, runID: "run-1"}
	w.call("w", "bash", map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}, t0, 0)
	if got := agentStallDetect(t, w, t0, t0+16*m); got != nil {
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

// agentStallList pages e's events newest first as ListSessionEventsBefore
// does (before 0: from the newest), counting the events it hands out.
func agentStallList(e *agentStallEvents, read *int) func(uint64, int) ([]pebblestore.V3SessionEvent, error) {
	return func(before uint64, limit int) ([]pebblestore.V3SessionEvent, error) {
		var page []pebblestore.V3SessionEvent
		for _, ev := range e.events {
			if before != 0 && ev.Seq >= before {
				continue
			}
			if len(page) == limit {
				break
			}
			page = append(page, ev)
		}
		*read += len(page)
		return page, nil
	}
}

// Purpose: a long command that streams a lot of output keeps its start in
// the stall read, so it is reported as long_command past
// agent_command_stall_minutes and never as no_activity while it runs.
// Threat (review finding): each streamed chunk is one durable
// session.tool.delta; with deltas counted against the 400-event read, a
// build printing more than ~400 chunks pushed its own session.tool.started
// out of the read, so the run looked idle since its start (a false
// no_activity) and the real long command was never named. Authority:
// agentStallReadEvents (the read taskAgentStall does through
// ListSessionEventsBefore), agentStallCallsFromEvents and detectAgentStall.
// Layer: the read and decision over a paged fake of the session event list,
// the narrowest layer that holds the paging budget.
func TestAgentStallStreamedOutputKeepsTheLongCommandStart(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()
	stream := func(chunks int) *agentStallEvents {
		e := &agentStallEvents{t: t, runID: "run-1"}
		e.call("r", "read", map[string]any{"path": "main.go"}, t0, t0+500)
		e.call("b", "bash", map[string]any{"command": "go test -v -count=1 ./..."}, t0+m, 0)
		for i := 0; i < chunks; i++ {
			// Over 24 min after the start, oldest first.
			e.add("session.tool.delta", t0+m+int64(i+1)*24*m/int64(chunks), map[string]any{"call_id": "b", "tool_name": "bash", "output": "=== RUN TestX\n"})
		}
		return e
	}
	detect := func(e *agentStallEvents, now int64) (*pebblestore.ProjectTaskAgentStall, int) {
		t.Helper()
		read := 0
		events, err := agentStallReadEvents(agentStallList(e, &read), t0)
		if err != nil {
			t.Fatal(err)
		}
		calls, last := agentStallCallsFromEvents(events, "run-1", t0)
		return detectAgentStall(calls, last, now, agentStallThresholdsFrom(pebblestore.DefaultLimits())), read
	}

	e := stream(1200)
	// 17 min after its start, still under the 20 min command threshold and
	// past the 15 min idle one: busy, not idle.
	if got, _ := detect(e, t0+18*m); got != nil {
		t.Fatalf("a streaming command flagged before its threshold: %+v", got)
	}
	long, read := detect(e, t0+25*m)
	if long == nil || long.Kind != pebblestore.ProjectTaskAgentStallLongCommand || long.Since != t0+m || long.Command != "go test -v -count=1 ./..." || long.ForMs != 24*m {
		t.Fatalf("streaming long command not named from its start: %+v", long)
	}
	if read > taskAgentStallEventRawScan || read < 1200 {
		t.Fatalf("read %d events for 1200 deltas (raw cap %d)", read, taskAgentStallEventRawScan)
	}

	// More output than the raw read cap: the start is out of reach, so the
	// call is in flight with an unknown start: neither timed nor idle.
	huge := stream(taskAgentStallEventRawScan + 500)
	got, read := detect(huge, t0+25*m)
	if got != nil {
		t.Fatalf("a call seen only through its deltas flagged: %+v", got)
	}
	if read > taskAgentStallEventRawScan {
		t.Fatalf("read %d events past the raw cap %d", read, taskAgentStallEventRawScan)
	}
}

// Purpose: an agent that waits on one background command again and again
// (bash background_id command=wait, each bounded by its own cap) is one
// long command, timed from that command's start, and reported past
// agent_command_stall_minutes. Threat (review finding): waits were exempt
// from long_command, poll streaks stop at a wait, and every wait reset the
// idle timer, so a hung background command waited on in a loop went
// unreported for up to the 4 h background limit. The time must never be
// longer than real (a different command's waits are not joined) and stay
// stable while the waiting goes on (one episode). Authority:
// agentStallWaitStart and detectAgentStall. Layer: the pure decision over
// recorded event shapes.
func TestAgentStallRepeatedWaitsOnOneBackgroundCommandAreOneLongCommand(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()
	wait := map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}
	e := &agentStallEvents{t: t, runID: "run-1"}
	e.call("start", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000)
	e.callResult("w1", "bash", wait, t0+2000, t0+10*m+2000, map[string]any{"background_id": "bg_1", "still_running": true})
	e.call("w2", "mcp__swarm__bash", wait, t0+10*m+5000, 0)
	if got := agentStallDetect(t, e, t0, t0+16*m); got != nil {
		t.Fatalf("16 min of waiting flagged: %+v", got)
	}
	stall := agentStallDetect(t, e, t0, t0+21*m)
	if stall == nil || stall.Kind != pebblestore.ProjectTaskAgentStallLongCommand || stall.Since != t0 || stall.Command != "make e2e" || stall.BackgroundID != "bg_1" || stall.ForMs != 21*m {
		t.Fatalf("hung background command not named from its start: %+v", stall)
	}
	// Between two waits (the newest one ended at its bound, the command
	// still running) and on the next wait it is the same episode.
	e.add("session.tool.completed", t0+20*m+5000, map[string]any{"call_id": "w2", "tool_name": "bash", "background_id": "bg_1", "still_running": true})
	if gap := agentStallDetect(t, e, t0, t0+21*m); gap == nil || gap.Episode() != stall.Episode() {
		t.Fatalf("the gap between waits changed the episode: %+v", gap)
	}
	e.call("w3", "bash", map[string]any{"background_id": "bg_1", "wait_seconds": 600}, t0+21*m, 0)
	if later := agentStallDetect(t, e, t0, t0+30*m); later == nil || later.Episode() != stall.Episode() || later.ForMs != 30*m {
		t.Fatalf("a later wait changed the episode: %+v", later)
	}
	// Moving on ends it.
	e.add("session.tool.completed", t0+31*m, map[string]any{"call_id": "w3", "tool_name": "bash"})
	e.call("edit", "edit", map[string]any{"path": "a.go"}, t0+31*m+1000, t0+31*m+2000)
	if got := agentStallDetect(t, e, t0, t0+32*m); got != nil {
		t.Fatalf("a run that moved on still flagged: %+v", got)
	}

	// Waits on another background command are not joined: a wait on bg_2
	// after waits on bg_1 counts from its own start only.
	f := &agentStallEvents{t: t, runID: "run-1"}
	f.call("r", "read", map[string]any{"path": "a.go"}, t0, t0+500)
	f.call("a1", "bash", wait, t0+m, t0+11*m)
	f.call("a2", "bash", wait, t0+11*m, t0+21*m)
	f.call("b1", "bash", map[string]any{"background_id": "bg_2", "command": "wait"}, t0+21*m, 0)
	if got := agentStallDetect(t, f, t0, t0+30*m); got != nil {
		t.Fatalf("waits on two background commands joined: %+v", got)
	}
	// With no starting call in the read, the waits on bg_1 count from the
	// first of them (never longer than real).
	g := &agentStallEvents{t: t, runID: "run-1"}
	g.call("r", "read", map[string]any{"path": "a.go"}, t0, t0+500)
	g.call("a1", "bash", map[string]any{"background_id": "bg_1", "command": "status"}, t0+m, t0+m+500)
	g.call("a2", "bash", wait, t0+2*m, t0+12*m)
	g.call("a3", "bash", wait, t0+12*m, 0)
	if got := agentStallDetect(t, g, t0, t0+22*m); got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Since != t0+m || got.BackgroundID != "bg_1" {
		t.Fatalf("waits without their start not timed from the first: %+v", got)
	}
}

// Purpose: a long_command stall on a bash wait names the command the wait
// is on: the bash call whose result returned the same background_id (a
// background:true start, or a foreground command moved to the background),
// never another bash call that happens to come before the wait. Threat
// (review finding): agentStallWaitStart took the nearest earlier non-sleep
// bash call as the start, so a second background command (`make lint`,
// bg_2), a foreground `ls`, or a foreground build moved to the background
// with its own id was reported as the hung command. Without the exact start
// in the read, the stall names the wait's own target, timed from the waits
// (never longer than real). Authority: agentStallCallsFromEvents (the end
// event's background_id, copied by providerManagedV3ToolBackgroundFields)
// and agentStallWaitStart. Layer: the pure decision over recorded event
// shapes.
func TestAgentStallWaitNamesOnlyItsOwnBackgroundCommand(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()
	wait := map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}
	waitTarget := "bash background_id=bg_1 command=wait"

	// Two background commands, then a wait on the first.
	e := &agentStallEvents{t: t, runID: "run-1"}
	e.callResult("e2e", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000, map[string]any{"background_id": "bg_1"})
	e.callResult("lint", "bash", map[string]any{"command": "make lint", "background": true}, t0+m, t0+m+1000, map[string]any{"background_id": "bg_2"})
	e.call("w", "bash", wait, t0+2*m, 0)
	got := agentStallDetect(t, e, t0, t0+21*m)
	if got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Command != "make e2e" || got.BackgroundID != "bg_1" || got.Since != t0 {
		t.Fatalf("the wait on bg_1 named another command: %+v", got)
	}

	// Unrelated foreground calls between the start and the waits: an `ls`,
	// and a build that reached its limit and moved to the background as bg_3.
	f := &agentStallEvents{t: t, runID: "run-1"}
	f.callResult("e2e", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000, map[string]any{"background_id": "bg_1"})
	f.call("ls", "bash", map[string]any{"command": "ls"}, t0+m, t0+m+500)
	f.callResult("build", "bash", map[string]any{"command": "make build"}, t0+2*m, t0+7*m, map[string]any{"background_id": "bg_3"})
	f.callResult("w1", "bash", wait, t0+8*m, t0+18*m, map[string]any{"background_id": "bg_1", "still_running": true})
	f.call("w2", "bash", wait, t0+18*m, 0)
	got = agentStallDetect(t, f, t0, t0+21*m)
	if got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Command != "make e2e" || got.BackgroundID != "bg_1" || got.Since != t0 {
		t.Fatalf("an unrelated call between the start and the waits named: %+v", got)
	}
	// A wait on the moved build names the build, from its own start.
	f.add("session.tool.completed", t0+19*m, map[string]any{"call_id": "w2", "tool_name": "bash", "background_id": "bg_1", "still_running": false})
	f.call("w3", "bash", map[string]any{"background_id": "bg_3", "command": "wait"}, t0+19*m+1000, 0)
	got = agentStallDetect(t, f, t0, t0+23*m)
	if got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Command != "make build" || got.BackgroundID != "bg_3" || got.Since != t0+2*m {
		t.Fatalf("a wait on a moved foreground command not named from it: %+v", got)
	}

	// The start not known (its end event names no id) and a foreground `ls`
	// before the wait: the stall names the wait itself, from the wait's start.
	g := &agentStallEvents{t: t, runID: "run-1"}
	g.call("e2e", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000)
	g.call("ls", "bash", map[string]any{"command": "ls"}, t0+m, t0+m+500)
	g.call("w", "bash", wait, t0+2*m, 0)
	if got := agentStallDetect(t, g, t0, t0+21*m); got != nil {
		t.Fatalf("a wait timed past an unrelated call: %+v", got)
	}
	got = agentStallDetect(t, g, t0, t0+23*m)
	if got == nil || got.Kind != pebblestore.ProjectTaskAgentStallLongCommand || got.Command != waitTarget || got.Since != t0+2*m {
		t.Fatalf("the unrelated `ls` was named as the hung command: %+v", got)
	}
}

// Purpose: a bash wait that returned because its background command ended
// (no still_running in its end event) ends that command's long_command:
// the run goes on to the idle check, so an agent that then hangs is
// no_activity from the wait's end. Threat (review finding): detectAgentStall
// kept reporting "`make e2e` running N min" after the command had exited,
// until the agent's next tool call, and hid the real hang (no new episode,
// no wake). Authority: agentStallWaitStart (still_running from the end
// event) and detectAgentStall. Layer: the pure decision over recorded event
// shapes.
func TestAgentStallWaitThatEndedWithItsCommandIsNotALongCommand(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	m := time.Minute.Milliseconds()
	wait := map[string]any{"background_id": "bg_1", "command": "wait", "wait_seconds": 600}
	e := &agentStallEvents{t: t, runID: "run-1"}
	e.callResult("e2e", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000, map[string]any{"background_id": "bg_1"})
	e.callResult("w1", "bash", wait, t0+m, t0+11*m, map[string]any{"background_id": "bg_1", "still_running": true})
	// The command finishes at 19 min; the wait returns its exit.
	e.callResult("w2", "bash", wait, t0+11*m, t0+19*m, map[string]any{"background_id": "bg_1", "still_running": false, "exit_code": 0})
	if got := agentStallDetect(t, e, t0, t0+20*m); got != nil {
		t.Fatalf("an ended command still reported: %+v", got)
	}
	if got := agentStallDetect(t, e, t0, t0+33*m); got != nil {
		t.Fatalf("14 min quiet after the wait flagged: %+v", got)
	}
	got := agentStallDetect(t, e, t0, t0+34*m)
	if got == nil || got.Kind != pebblestore.ProjectTaskAgentStallNoActivity || got.Since != t0+19*m || got.ForMs != 15*m {
		t.Fatalf("the hang after the command ended not no_activity: %+v", got)
	}
	// An end event without still_running (no result fields) is an ended wait too.
	f := &agentStallEvents{t: t, runID: "run-1"}
	f.call("e2e", "bash", map[string]any{"command": "make e2e", "background": true}, t0, t0+1000)
	f.call("w", "bash", wait, t0+m, t0+19*m)
	if got := agentStallDetect(t, f, t0, t0+21*m); got != nil {
		t.Fatalf("a wait ended without still_running reported: %+v", got)
	}
}
