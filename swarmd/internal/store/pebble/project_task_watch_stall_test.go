package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func agentStallProgress(kind, command string, since, forMs int64) map[string]any {
	stall := map[string]any{"kind": kind, "command": command, "since": since, "for_ms": forMs}
	if kind == ProjectTaskAgentStallSleepPolling {
		stall["background_id"], stall["calls"] = "bg_1", 6
	}
	return map[string]any{"state": "running", "summary": "working", "task_state": "running", "agent_stall": stall}
}

// Purpose: a watch tells the Orchestrator when a watched task's agent
// stalls (sleep polling one background command, one long command, no
// activity): the `stalled` milestone, once per stall episode, with the row's
// agent_stall naming which, what command and for how long; when the agent
// moves again the episode is cleared without a wake, and a later stall is a
// new episode that wakes again. Threat (observed): agents slept in loops for
// 10+ minutes and nothing told the watching Orchestrator; or, the other way,
// a wake on every check while one stall lasts. Authority:
// prepareProjectTaskWatchWake (projectTaskWatchStallEpisode, LastStalls)
// through ApplyV3SessionMutation with an injected clock; the progress is the
// installed hook's (agent_stall), parsed by ProjectTaskWaitProgressFrom.
// Layer: a temporary real store, the narrowest that commits the wake.
func TestProjectTaskWatchStalledMilestoneOncePerEpisode(t *testing.T) {
	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}}})
	f.endRun("parent", "run-1")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "running" })
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	minute := time.Minute.Milliseconds()
	wakeAt := func(at int64) (V3SessionMutationResult, error) {
		in := ProjectTaskWatchWakeInput(f.onlyWatch("parent"), nil, nil)
		in.NowUnixMs = at
		return f.try(in)
	}
	endTurn := func(res V3SessionMutationResult, claim string) {
		t.Helper()
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: claim, RunIntent: &V3SessionRunIntent{RunID: res.RunIntent.RunID, Status: V3RunIntentRunning}})
		f.endRun("parent", res.RunIntent.RunID)
	}
	stalledRow := func(res V3SessionMutationResult, kind string) map[string]any {
		t.Helper()
		rows := milestoneRows(t, res.Message)
		if len(rows) != 1 || rows[0]["milestone"] != ProjectTaskMilestoneStalled {
			t.Fatalf("not a stalled milestone: %v", rows)
		}
		stall, _ := rows[0]["agent_stall"].(map[string]any)
		if stall["kind"] != kind || stall["reason"] == "" || stall["for"] == "" || stall["since"] == "" {
			t.Fatalf("agent_stall does not say which, what and how long: %v", rows[0])
		}
		if !strings.Contains(res.Message.Content, "agent_stall") {
			t.Fatalf("guidance does not point at agent_stall: %s", res.Message.Content)
		}
		return stall
	}

	// Working normally: nothing to report.
	progress.set("task", map[string]any{"state": "running", "summary": "working", "task_state": "running"})
	if _, err := wakeAt(t0); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("a moving agent woke the watch: %v", err)
	}
	// (a) Sleep polling for 11 minutes.
	pollSince := t0 - 11*minute
	progress.set("task", agentStallProgress(ProjectTaskAgentStallSleepPolling, "sleep 290", pollSince, 11*minute))
	first, err := wakeAt(t0)
	if err != nil {
		t.Fatal(err)
	}
	stall := stalledRow(first, ProjectTaskAgentStallSleepPolling)
	if stall["background_id"] != "bg_1" || stall["for"] != "11 min" || !strings.Contains(stall["reason"].(string), "bg_1") {
		t.Fatalf("sleep polling row: %v", stall)
	}
	if w := f.onlyWatch("parent"); w.LastStalls["task"] != ProjectTaskAgentStallSleepPolling+":"+jsonInt(pollSince) || w.Deliveries != 1 {
		t.Fatalf("episode not recorded: %+v", w)
	}
	endTurn(first, "claim-s1")
	// The same episode, longer: not again.
	progress.set("task", agentStallProgress(ProjectTaskAgentStallSleepPolling, "sleep 290", pollSince, 16*minute))
	if _, err := wakeAt(t0 + 5*minute); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("one episode woke twice: %v", err)
	}
	// The agent moves again: no wake for that.
	progress.set("task", map[string]any{"state": "running", "summary": "working", "task_state": "running"})
	if _, err := wakeAt(t0 + 6*minute); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("progress resuming woke the watch: %v", err)
	}
	// (b) A new episode: one long command.
	cmdSince := t0 + 7*minute
	progress.set("task", agentStallProgress(ProjectTaskAgentStallLongCommand, "go test ./...", cmdSince, 23*minute))
	second, err := wakeAt(t0 + 30*minute)
	if err != nil {
		t.Fatal(err)
	}
	if stall := stalledRow(second, ProjectTaskAgentStallLongCommand); stall["command"] != "go test ./..." || stall["for"] != "23 min" {
		t.Fatalf("long command row: %v", stall)
	}
	endTurn(second, "claim-s2")
	// (c) Another episode: no activity.
	idleSince := t0 + 31*minute
	progress.set("task", agentStallProgress(ProjectTaskAgentStallNoActivity, "", idleSince, 16*minute))
	third, err := wakeAt(t0 + 47*minute)
	if err != nil {
		t.Fatal(err)
	}
	stalledRow(third, ProjectTaskAgentStallNoActivity)
	endTurn(third, "claim-s3")
	// Moving again and reaching a stage: the stage is reported and the
	// ended episode is cleared from the watch.
	progress.set("task", workflowProgress("running", 1, "agent", 0, ""))
	fourth, err := wakeAt(t0 + 50*minute)
	if err != nil {
		t.Fatal(err)
	}
	if rows := milestoneRows(t, fourth.Message); len(rows) != 1 || rows[0]["milestone"] != "review started (round 0)" {
		t.Fatalf("stage after the stall: %v", rows)
	}
	if w := f.onlyWatch("parent"); len(w.LastStalls) != 0 || w.Deliveries != 4 {
		t.Fatalf("ended episode kept: %+v", w)
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Purpose: a watch on a linked machine reports an agent stall the same
// way: the machine's get_task progress carries agent_stall, the home's wake
// row names the machine, the `stalled` milestone and the stall, marked
// untrusted; a stall kind the home does not know is dropped (bounded remote
// data), so it never wakes. Threat: a linked task stalling with nobody told,
// or arbitrary remote text driving wakes. Authority: ProjectTaskWaitProgressFrom
// (ClipProjectTaskAgentStall), projectTaskWatchRemoteOutcome and
// prepareProjectTaskWatchWake, over a temporary real store.
func TestProjectTaskWatchLinkedWakeCarriesStalledMilestone(t *testing.T) {
	r := newTaskWaitFixture(t, "home", map[string]any{})
	r.apply(remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"}))
	r.endRun("home", "run-1")
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	observe := func(raw string) []V3ProjectTaskRemoteObservation {
		return []V3ProjectTaskRemoteObservation{{TaskID: "t1", Title: "One", Status: "running", AttemptID: "a1", SessionID: "rs1", Progress: ProjectTaskWaitProgressFrom(json.RawMessage(raw))}}
	}
	unknown := r.remoteWake(observe(`{"task_state":"running","agent_stall":{"kind":"anything","since":1,"for_ms":9}}`), nil)
	unknown.NowUnixMs = t0
	if _, err := r.try(unknown); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("an unknown remote stall kind woke the watch: %v", err)
	}
	in := r.remoteWake(observe(`{"task_state":"running","agent_stall":{"kind":"sleep_polling","command":"sleep 200","background_id":"bg_9","calls":5,"since":1790000000000,"for_ms":720000}}`), nil)
	in.NowUnixMs = t0
	res := r.apply(in)
	rows := milestoneRows(t, res.Message)
	if len(rows) != 1 || rows[0]["milestone"] != ProjectTaskMilestoneStalled || rows[0]["machine"] != "box" {
		t.Fatalf("linked stalled row: %v", rows)
	}
	stall, _ := rows[0]["agent_stall"].(map[string]any)
	if stall["kind"] != ProjectTaskAgentStallSleepPolling || stall["background_id"] != "bg_9" || stall["for"] != "12 min" {
		t.Fatalf("linked agent_stall: %v", stall)
	}
	if res.Message.Metadata["untrusted_content"] != true {
		t.Fatalf("linked milestone not marked untrusted: %v", res.Message.Metadata)
	}
	if w := r.onlyWatch("home"); w.LastStalls["t1"] != "sleep_polling:1790000000000" {
		t.Fatalf("linked episode not recorded: %+v", w)
	}
	// Swarm Control's wait (no conversation) names the same milestone.
	if got := ProjectTaskMilestone("running", observe(`{"agent_stall":{"kind":"long_command","since":5,"for_ms":9}}`)[0].Progress); got != ProjectTaskMilestoneStalled {
		t.Fatalf("control wait milestone: %q", got)
	}
	if got := ProjectTaskMilestone("running", observe(`{"agent_stall":{"kind":"anything","since":5,"for_ms":9}}`)[0].Progress); got != ProjectTaskMilestoneStarted {
		t.Fatalf("an unknown stall kind named a milestone: %q", got)
	}
}
