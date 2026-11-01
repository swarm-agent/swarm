package api

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Agent stalls (pebblestore.ProjectTaskAgentStall): how a task's running
// run spends its time without moving, read from the run's own session
// events and the account's limits. The task progress names it as
// agent_stall (taskWaitProgressFor: the task route, get_task, the watch's
// progress hook, a linked machine's get_task); a task watch reports a new one
// as its `stalled` milestone. Three shapes, checked in this order:
//
//   - sleep_polling: the run's latest tool calls are nothing but sleep
//     commands and status reads of one background command, for longer than
//     agent_poll_stall_minutes (bash command=wait is a real wait, not a poll);
//   - long_command: one tool call has run longer than
//     agent_command_stall_minutes; a bash wait on a background command
//     counts from that command's start (agentStallWaitStart), so waiting on a
//     hung background command over and over is one long command;
//   - no_activity: no tool call or message (and none in flight) for
//     agent_idle_stall_minutes.
//
// Nothing here is a timer: the stall check (task_stall.go, once a minute)
// tells the task's project it changed once per new episode
// (noteTaskAgentStall), which reconciles local watches and reaches a linked
// home's follower through the activity feed.

const (
	// taskAgentStallEventScan bounds the session events read per check,
	// not counting session.tool.delta: each chunk of a command's streamed
	// output is one delta, and a build that prints a lot must not push its
	// own start out of the read. taskAgentStallEventRawScan bounds every
	// event read, deltas included; a call seen only through its deltas
	// (its start further back) is in flight with an unknown start.
	taskAgentStallEventScan    = 400
	taskAgentStallEventRawScan = 5000
	taskAgentStallEventPage    = 100
	// taskAgentStallMinPolls is the fewest poll calls a sleep_polling
	// stall has (one long sleep is a long_command).
	taskAgentStallMinPolls = 2
)

// agentStallCall is one tool call of the run, as the stall check reads it.
type agentStallCall struct {
	tool    string
	command string
	// backgroundID and action are a bash call on a background command.
	backgroundID string
	action       string
	// background is a bash call asked to run its command in the background
	// (background:true).
	background bool
	// resultBackgroundID and stillRunning are read from the call's end
	// event (providerManagedV3ToolBackgroundFields): the background command
	// it names (started, moved to the background, or read), and whether a
	// wait returned with that command still running.
	resultBackgroundID string
	stillRunning       bool
	start              int64
	ended              bool
}

// target is what a stall row names for the call.
func (c agentStallCall) target() string {
	if c.command != "" {
		return c.command
	}
	if c.backgroundID != "" {
		return c.tool + " background_id=" + c.backgroundID + " command=" + firstNonEmptyString(c.action, "status")
	}
	return c.tool
}

// poll reports whether the call only waits by polling: a sleep, or a status
// read of a background command.
func (c agentStallCall) poll() bool {
	if c.tool != "bash" {
		return false
	}
	if c.backgroundID != "" {
		return c.action == "" || c.action == "status"
	}
	return agentStallSleepCommand(c.command)
}

// agentStallSleepCommand reports whether a shell command starts with sleep
// (sleep N, sleep N && tail log, ...): waiting by the clock.
func agentStallSleepCommand(command string) bool {
	first := strings.FieldsFunc(command, func(r rune) bool { return r == ';' || r == '&' || r == '|' || r == '\n' })
	if len(first) == 0 {
		return false
	}
	fields := strings.Fields(first[0])
	return len(fields) >= 1 && fields[0] == "sleep"
}

// agentStallThresholds are the account's agent stall limits.
type agentStallThresholds struct {
	poll, command, idle time.Duration
}

func agentStallThresholdsFrom(l pebblestore.Limits) agentStallThresholds {
	pick := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * time.Minute
	}
	return agentStallThresholds{
		poll:    pick(l.AgentPollStallMinutes, pebblestore.DefaultAgentPollStallMinutes),
		command: pick(l.AgentCommandStallMinutes, pebblestore.DefaultAgentCommandStallMinutes),
		idle:    pick(l.AgentIdleStallMinutes, pebblestore.DefaultAgentIdleStallMinutes),
	}
}

// agentStallCallsFromEvents reads a run's tool calls (newest first) and its
// last activity (a tool call starting or ending, a message, reasoning) from
// its session's events, newest first. Events of an earlier run, or from
// before startedAt, end the read. A call whose output deltas are read but
// whose start is not (the read ended first) is still in flight: it comes
// first, with start 0 (unknown), so it is neither timed nor taken for
// inactivity.
func agentStallCallsFromEvents(events []pebblestore.V3SessionEvent, runID string, startedAt int64) ([]agentStallCall, int64) {
	type payload struct {
		RunID     string `json:"run_id"`
		ToolName  string `json:"tool_name"`
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
		Instance  string `json:"tool_instance_id"`
		// End events of a bash call on a background command.
		BackgroundID string `json:"background_id"`
		StillRunning bool   `json:"still_running"`
	}
	ended := map[string]payload{}
	// streaming is the in-flight calls seen only through deltas so far.
	streaming := map[string]string{}
	var calls []agentStallCall
	lastActivity := startedAt
	for _, ev := range events {
		if startedAt > 0 && ev.TsUnixMs < startedAt {
			break
		}
		activity := ev.EventType == "session.message.appended" || strings.HasPrefix(ev.EventType, "session.assistant.") || strings.HasPrefix(ev.EventType, "session.reasoning.")
		if strings.HasPrefix(ev.EventType, "session.tool.") {
			var p payload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if runID != "" && p.RunID != "" && p.RunID != runID {
				break
			}
			call := firstNonEmptyString(p.Instance, p.CallID)
			switch ev.EventType {
			case "session.tool.delta":
				if _, done := ended[call]; call != "" && !done {
					streaming[call] = p.ToolName
				}
			case "session.tool.started":
				activity = true
				delete(streaming, call)
				end, done := ended[call]
				c := agentStallCallOf(p.ToolName, p.Arguments, ev.TsUnixMs, done)
				c.resultBackgroundID = strings.TrimSpace(end.BackgroundID)
				c.stillRunning = done && end.StillRunning
				calls = append(calls, c)
			default:
				activity = true
				if _, done := ended[call]; !done {
					ended[call] = p
				}
			}
		}
		if activity && ev.TsUnixMs > lastActivity {
			lastActivity = ev.TsUnixMs
		}
	}
	for _, name := range streaming {
		calls = append([]agentStallCall{agentStallCallOf(name, "", 0, false)}, calls...)
		break
	}
	return calls, lastActivity
}

// agentStallReadEvents reads a run's session events newest first through
// list (ListSessionEventsBefore): up to taskAgentStallEventScan events
// besides tool output deltas, and at most taskAgentStallEventRawScan in all.
// The read stops at the run's start.
func agentStallReadEvents(list func(beforeSeq uint64, limit int) ([]pebblestore.V3SessionEvent, error), startedAt int64) ([]pebblestore.V3SessionEvent, error) {
	var events []pebblestore.V3SessionEvent
	before, counted := uint64(0), 0
	for counted < taskAgentStallEventScan && len(events) < taskAgentStallEventRawScan {
		page, err := list(before, taskAgentStallEventPage)
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		for _, ev := range page {
			if ev.EventType != "session.tool.delta" {
				counted++
			}
		}
		if len(page) < taskAgentStallEventPage {
			break
		}
		before = page[len(page)-1].Seq
		if startedAt > 0 && page[len(page)-1].TsUnixMs < startedAt {
			break
		}
	}
	return events, nil
}

// agentStallCallOf reads a tool call's name and arguments.
func agentStallCallOf(name, arguments string, start int64, ended bool) agentStallCall {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:] // a provider's namespaced name (mcp__swarm__bash)
	}
	c := agentStallCall{tool: name, start: start, ended: ended}
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) == nil {
		str := func(key string) string { v, _ := args[key].(string); return strings.TrimSpace(v) }
		c.backgroundID = str("background_id")
		if c.backgroundID != "" {
			c.action = strings.ToLower(str("command"))
			if c.action == "" && args["wait_seconds"] != nil {
				c.action = "wait" // as the bash tool reads it
			}
		} else {
			c.background, _ = args["background"].(bool)
			line, _, _ := strings.Cut(str("command"), "\n")
			c.command = fleetAgentsClip(strings.Join(strings.Fields(line), " "), 200)
		}
	}
	return c
}

// detectAgentStall decides whether a run stalls at now from its calls
// (newest first) and last activity. nil when it does not.
func detectAgentStall(calls []agentStallCall, lastActivity, now int64, th agentStallThresholds) *pebblestore.ProjectTaskAgentStall {
	// (a) Only sleeps and status reads of one background command.
	polls, oldest, bg, sleep := 0, int64(0), "", ""
	for _, c := range calls {
		if !c.poll() {
			break
		}
		if c.backgroundID != "" {
			if bg != "" && bg != c.backgroundID {
				break
			}
			bg = c.backgroundID
		} else if sleep == "" {
			sleep = c.command
		}
		polls++
		oldest = c.start
	}
	if polls >= taskAgentStallMinPolls && oldest > 0 && now-oldest >= th.poll.Milliseconds() {
		return &pebblestore.ProjectTaskAgentStall{Kind: pebblestore.ProjectTaskAgentStallSleepPolling, Command: sleep, BackgroundID: bg, Calls: polls, Since: oldest, ForMs: now - oldest}
	}
	// (b) One call running too long. A bash wait counts from its background
	// command's start: one hung command waited on again and again is one
	// long command.
	if len(calls) > 0 {
		c := calls[0]
		if since, command, ok := agentStallWaitStart(calls); ok {
			if now-since >= th.command.Milliseconds() {
				return &pebblestore.ProjectTaskAgentStall{Kind: pebblestore.ProjectTaskAgentStallLongCommand, Command: firstNonEmptyString(command, c.target()), BackgroundID: c.backgroundID, Since: since, ForMs: now - since}
			}
			if !c.ended {
				return nil
			}
		} else if !c.ended {
			if c.start > 0 && now-c.start >= th.command.Milliseconds() {
				return &pebblestore.ProjectTaskAgentStall{Kind: pebblestore.ProjectTaskAgentStallLongCommand, Command: c.target(), BackgroundID: c.backgroundID, Since: c.start, ForMs: now - c.start}
			}
			return nil
		}
	}
	// (c) Nothing at all.
	if lastActivity > 0 && now-lastActivity >= th.idle.Milliseconds() {
		stall := &pebblestore.ProjectTaskAgentStall{Kind: pebblestore.ProjectTaskAgentStallNoActivity, Since: lastActivity, ForMs: now - lastActivity}
		if len(calls) > 0 {
			stall.Command = calls[0].target()
		}
		return stall
	}
	return nil
}

// agentStallWaitStart reads, when the run's newest call is a bash wait on a
// background command that is still going (in flight, or ended with the
// command still_running), since when that command has been waited on, and
// the command:
//
//   - the bash call whose result names the same background_id started it
//     (background:true, or a foreground command moved to the background):
//     its start and its command;
//   - else, back over the unbroken run of calls on the same background_id
//     (waits, status reads) and sleeps, the bash call right before them
//     when it asked for background:true and its result names no other id
//     (an end event without the id);
//   - else the first call of that run, and no command (the stall names the
//     wait's own target).
//
// The command started at or before that point, so the time is never longer
// than it really is, and it stays the same while the waiting goes on (one
// episode). A wait that ended without still_running ended with its command:
// no wait stall (the run goes on to the idle check).
func agentStallWaitStart(calls []agentStallCall) (int64, string, bool) {
	c := calls[0]
	if c.tool != "bash" || c.backgroundID == "" || c.action != "wait" || c.start <= 0 || (c.ended && !c.stillRunning) {
		return 0, "", false
	}
	since, joined := c.start, true
	for _, p := range calls[1:] {
		if p.start <= 0 {
			break
		}
		if p.tool == "bash" && p.backgroundID == "" && p.resultBackgroundID == c.backgroundID {
			return p.start, p.command, true
		}
		if !joined {
			continue // only the exact start can still be found
		}
		if p.tool == "bash" && (p.backgroundID == c.backgroundID || (p.backgroundID == "" && agentStallSleepCommand(p.command))) {
			since = p.start
			continue
		}
		if p.tool == "bash" && p.backgroundID == "" && p.background && p.resultBackgroundID == "" {
			return p.start, p.command, true
		}
		joined = false
	}
	return since, "", true
}

// taskAgentStall reads how the task's running run stalls at now (nil when
// it does not, or when it is not running: queued, held and waiting runs
// have their own states).
func (s *Server) taskAgentStall(account string, task pebblestore.ProjectTaskRecord, now int64) *pebblestore.ProjectTaskAgentStall {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || taskStatusClass(task.Status) != progressWorking || task.Archived {
		return nil
	}
	sessionID := taskLiveSessionID(task)
	if sessionID == "" {
		return nil
	}
	db := s.sessions.Store()
	state, found, err := db.GetV3SessionRunState(sessionID)
	if err != nil || !found || !state.Active || state.Status != pebblestore.V3RunIntentRunning {
		return nil
	}
	limits, err := db.GetLimits(account)
	if err != nil {
		log.Printf("swarmd agent stall: read limits: %v", err)
		return nil
	}
	events, err := agentStallReadEvents(func(before uint64, limit int) ([]pebblestore.V3SessionEvent, error) {
		return s.sessions.ListSessionEventsBefore(sessionID, before, limit)
	}, state.StartedAt)
	if err != nil {
		log.Printf("swarmd agent stall: read events of %s: %v", sessionID, err)
		return nil
	}
	calls, lastActivity := agentStallCallsFromEvents(events, state.RunID, state.StartedAt)
	return detectAgentStall(calls, lastActivity, now, agentStallThresholdsFrom(limits))
}

// markTaskAgentStall sets p.AgentStall. A run waiting on a person or held
// (p.Live) is not an agent stall.
func (s *Server) markTaskAgentStall(p *taskProgress, account string, task pebblestore.ProjectTaskRecord, now int64) {
	if p == nil || p.Live != nil {
		return
	}
	p.AgentStall = s.taskAgentStall(account, task, now)
}

// noteTaskAgentStall tells the task's project it changed when its run
// enters a new agent stall episode: local watches reconcile (the `stalled`
// milestone) and a linked home's follower reads the task again. Once per
// episode per daemon process; an ended episode is forgotten.
func (s *Server) noteTaskAgentStall(account string, task pebblestore.ProjectTaskRecord, now time.Time) {
	if s == nil || s.sessions == nil {
		return
	}
	episode := ""
	if stall := s.taskAgentStall(account, task, now.UnixMilli()); stall != nil {
		episode = stall.Episode()
	}
	s.taskStallGitMu.Lock()
	if s.taskAgentStallNoted == nil || len(s.taskAgentStallNoted) > 10_000 {
		s.taskAgentStallNoted = map[string]string{}
	}
	previous := s.taskAgentStallNoted[task.ID]
	if episode == "" {
		delete(s.taskAgentStallNoted, task.ID)
	} else {
		s.taskAgentStallNoted[task.ID] = episode
	}
	s.taskStallGitMu.Unlock()
	if episode == "" || episode == previous {
		return
	}
	if err := s.sessions.Store().NotifyProjectTaskChanged(account, task.ProjectID, task.ID); err != nil {
		log.Printf("swarmd agent stall: notify %s: %v", task.ID, err)
		s.reconcileProjectTaskWaits(account, task.ProjectID)
	}
}
