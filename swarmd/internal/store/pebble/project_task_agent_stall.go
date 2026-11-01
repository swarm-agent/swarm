package pebblestore

import (
	"fmt"
	"strconv"
	"time"
)

// An agent stall is how a task's active run is spending its time without
// moving: polling a background command with sleep and status calls, one
// command running far longer than a step should, or no tool call or message
// at all. The task progress (taskWaitProgressFor) names it as agent_stall,
// read from the run's own session events and the account's limits; the task
// watch turns a new one into the `stalled` milestone (once per episode) and
// Swarm Control's wait_tasks names it as its milestone. A linked machine
// reports it in get_task's progress, so a linked watch reads the same field.
type ProjectTaskAgentStall struct {
	// Kind is sleep_polling, long_command or no_activity.
	Kind string `json:"kind"`
	// Command is the command (or tool) the stall is about: the polled
	// background command or the sleep, the long command, or the last call.
	Command string `json:"command,omitempty"`
	// BackgroundID is the background command a sleep_polling stall polls.
	BackgroundID string `json:"background_id,omitempty"`
	// Calls counts the poll calls of a sleep_polling stall.
	Calls int `json:"calls,omitempty"`
	// Since is when the stall began (Unix ms): the first poll of the
	// streak, the long command's start, or the run's last activity. It is
	// stable for one episode, so a new episode is a new Since.
	Since int64 `json:"since"`
	// ForMs is how long it has lasted when it was read.
	ForMs int64 `json:"for_ms"`
}

// Agent stall kinds.
const (
	ProjectTaskAgentStallSleepPolling = "sleep_polling"
	ProjectTaskAgentStallLongCommand  = "long_command"
	ProjectTaskAgentStallNoActivity   = "no_activity"
)

// ProjectTaskMilestoneStalled is the milestone a watch (and Swarm Control's
// wait) reports when a task's agent stalls; the row's agent_stall says how.
const ProjectTaskMilestoneStalled = "stalled"

// Episode identifies one stall episode: its kind and when it began.
func (a ProjectTaskAgentStall) Episode() string {
	return a.Kind + ":" + strconv.FormatInt(a.Since, 10)
}

// Reason is the stall in one line: which, what command, for how long.
func (a ProjectTaskAgentStall) Reason() string {
	for_ := projectTaskStallDuration(a.ForMs)
	switch a.Kind {
	case ProjectTaskAgentStallSleepPolling:
		target := a.Command
		if a.BackgroundID != "" {
			target = "background command " + a.BackgroundID
		}
		return fmt.Sprintf("only sleep and status polls of %s for %s (%d calls)", firstNonEmptyTrimmed(target, "a background command"), for_, a.Calls)
	case ProjectTaskAgentStallLongCommand:
		return fmt.Sprintf("one command running for %s: %s", for_, firstNonEmptyTrimmed(a.Command, "a tool call"))
	case ProjectTaskAgentStallNoActivity:
		reason := "no tool call or message for " + for_
		if a.Command != "" {
			reason += " (last: " + a.Command + ")"
		}
		return reason
	}
	return a.Kind + " for " + for_
}

func projectTaskStallDuration(ms int64) string {
	d := time.Duration(max(ms, 0)) * time.Millisecond
	if d < time.Minute {
		return strconv.Itoa(int(d/time.Second)) + " s"
	}
	return strconv.Itoa(int(d/time.Minute)) + " min"
}

// ClipProjectTaskAgentStall bounds an agent stall (it may come from a linked
// machine) for a wait outcome row. An unknown kind is dropped.
func ClipProjectTaskAgentStall(a *ProjectTaskAgentStall) *ProjectTaskAgentStall {
	if a == nil {
		return nil
	}
	switch a.Kind {
	case ProjectTaskAgentStallSleepPolling, ProjectTaskAgentStallLongCommand, ProjectTaskAgentStallNoActivity:
	default:
		return nil
	}
	return &ProjectTaskAgentStall{Kind: a.Kind, Command: clipProjectTaskWaitField(a.Command), BackgroundID: clipProjectTaskWaitField(a.BackgroundID), Calls: max(a.Calls, 0), Since: max(a.Since, 0), ForMs: max(a.ForMs, 0)}
}

// projectTaskAgentStallRow is an agent stall as a wake row shows it.
func projectTaskAgentStallRow(a *ProjectTaskAgentStall) map[string]any {
	row := map[string]any{"kind": a.Kind, "for_ms": a.ForMs, "for": projectTaskStallDuration(a.ForMs), "since_ms": a.Since, "reason": a.Reason()}
	if a.Since > 0 {
		row["since"] = time.UnixMilli(a.Since).UTC().Format(time.RFC3339)
	}
	if a.Command != "" {
		row["command"] = a.Command
	}
	if a.BackgroundID != "" {
		row["background_id"] = a.BackgroundID
	}
	if a.Calls > 0 {
		row["calls"] = a.Calls
	}
	return row
}

// projectTaskWatchStallEpisode is the stall episode a wake row shows ("" for
// none): the agent_stall's kind and start.
func projectTaskWatchStallEpisode(row map[string]any) string {
	stall, _ := row["agent_stall"].(map[string]any)
	if stall == nil {
		return ""
	}
	kind, _ := stall["kind"].(string)
	if kind == "" {
		return ""
	}
	return kind + ":" + fmt.Sprint(stall["since_ms"])
}
