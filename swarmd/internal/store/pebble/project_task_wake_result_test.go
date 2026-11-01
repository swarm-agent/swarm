package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// wakeFinalMessage is an agent's final assistant message as a provider run
// records it: many progress lines first, the handoff (what was done, tests,
// commit) at the end.
func wakeFinalMessage() string {
	return strings.Repeat("Still exploring the wait code é…\n", 400) + "FINAL HANDOFF: changed project_task_wait.go; ran go test -run TestX: ok; commit abc123."
}

// wakeStepResultsProgress is task progress (as Server.taskWaitProgressFor
// computes it) whose shown delivery finished a run step, failed a gate run
// step and had a review step block with BLOCKING findings, and stands at a
// push_failed reopen whose failure is long.
func wakeStepResultsProgress(failure string) map[string]any {
	code := 1
	steps := []ProjectTaskStepResult{
		{ID: "lint", Type: "run", Status: "succeeded", DurationMs: 1200, OutputTail: "lint ok"},
		{ID: "gate", Type: "run", Status: "failed", DurationMs: 4100, ExitCode: &code, Error: "exited with code 1: FAIL TestWakeRow", OutputTail: "--- FAIL: TestWakeRow\nFAIL\nexit status 1"},
		{ID: "review", Type: "agent", Status: "succeeded", Verdict: "block", Blocking: []string{"BLOCKING: summary is the first lines"}},
	}
	return map[string]any{"state": "workflow", "summary": "review loop", "workflow": map[string]any{"state": "sent_back", "deliveries": []any{
		map[string]any{"id": "jdel-1", "workflow": "review loop", "state": "sent_back", "step": 4, "steps": 4, "step_type": "reopen", "round": 1,
			"reopen_cause": "push_failed", "reopen_failure": failure, "reopen_round": 2, "steps_result": steps},
	}}}
}

// Purpose: a wake row's summary is the agent's final result message, and a
// long one keeps its end (the handoff), says it was cut and how to read the
// whole message. Threat (observed): the summary was the run's first progress
// lines cut at 4000 bytes, so the woken AI reported from "Still exploring…"
// and missed what the agent did, which tests ran and the commit.
// Boundary: completedTaskEventSummary (the stored final message) and
// setProjectTaskWakeResult through projectTaskWatchLocalOutcome over a
// temporary real store; the narrowest layers that store and show it.
func TestProjectTaskWakeSummaryIsTheFinalMessageAndKeepsItsEnd(t *testing.T) {
	final := wakeFinalMessage()
	state := V3SessionRunState{SessionID: "child", AccountScopeID: "account", RunID: "run-9", Status: V3RunIntentCompleted, EventSeq: 7}
	payload, err := json.Marshal(v3SessionEventReplayPayload{
		RunIntent: &V3SessionRunIntent{SessionID: "child", RunID: "run-9", AccountScopeID: "account", Status: V3RunIntentCompleted},
		Message:   &MessageSnapshot{ID: "msg-final", Role: "assistant", Content: final, Metadata: map[string]any{"run_id": "run-9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := completedTaskEventSummary(state, V3SessionEvent{SessionID: "child", Seq: 7, EventType: "session.assistant.completed", Payload: payload})
	if !ok {
		t.Fatal("completed run summary not read")
	}
	if len(stored) > ProjectTaskWakeResultBytes || !utf8.ValidString(stored) || !strings.HasSuffix(stored, "commit abc123.") || !strings.HasPrefix(stored, "[…first ") {
		t.Fatalf("stored summary does not keep the end: %d bytes, %q…%q", len(stored), stored[:60], stored[len(stored)-40:])
	}

	f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review"; t.Attempts[0].Summary = stored })
	row := outcomeRow(t, f.reconcile(), 0)
	summary, _ := row["summary"].(string)
	if summary != stored || row["summary_source"] != ProjectTaskSummaryFinalMessage {
		t.Fatalf("row summary is not the final message: source %v, %q", row["summary_source"], summary)
	}
	if row["result_truncated"] != true || row["result_bytes"] != float64(len(final)) || row["read_full"] == nil {
		t.Fatalf("cut summary not flagged with the whole size and a read: %v", row)
	}
	// The kept part is the message's end (the handoff), not its first lines.
	if !strings.HasSuffix(summary, "FINAL HANDOFF: changed project_task_wait.go; ran go test -run TestX: ok; commit abc123.") || len(summary) >= len(final) {
		t.Fatalf("summary does not end with the handoff: %q", summary[len(summary)-120:])
	}

	// A short final message is whole and unflagged.
	short := map[string]any{}
	setProjectTaskWakeResult(short, ProjectTaskWakeResult{Summary: "Done: tests ok, commit abc.", Source: ProjectTaskSummaryFinalMessage, ReadFull: "read"})
	if short["summary"] != "Done: tests ok, commit abc." || short["result_truncated"] != nil || short["read_full"] != nil {
		t.Fatalf("short summary changed: %v", short)
	}
}

// Purpose: a failure reason in a wake row is never cut below 2000
// characters; a longer one keeps its head and tail with a marker and a read
// of the whole. Threat (observed): reopen_failure "push failed: exited with
// code 1: NOT PUSHED: rebase needed: dev moved and change…" cut mid-sentence,
// so the AI never saw why the push failed. Boundary: ClipProjectTaskReason,
// ProjectTaskReopen.Normalized, ProjectTaskWaitProgressFrom and
// setProjectTaskWakeResult through a local wake over a temporary real store.
func TestProjectTaskWakeFailureReasonKeepsHeadAndTail(t *testing.T) {
	observed := "push failed: exited with code 1: NOT PUSHED: rebase needed: dev moved and changed files this change touches (swarmd/internal/api/project_task_wait.go); rebase onto origin/dev and push again"
	if got := (ProjectTaskReopen{Cause: ProjectTaskReopenPushFailed, Failure: observed}.Normalized()).Failure; got != observed {
		t.Fatalf("a %d-char reason was cut: %q", len(observed), got)
	}
	long := "HEAD-REASON " + strings.Repeat("middle detail ", 600) + " TAIL-REASON"
	clipped, cut := ClipProjectTaskReason(long, ProjectTaskWakeReasonRunes)
	if !cut || utf8.RuneCountInString(clipped) > ProjectTaskWakeReasonRunes || !strings.HasPrefix(clipped, "HEAD-REASON") || !strings.HasSuffix(clipped, "TAIL-REASON") || !strings.Contains(clipped, "chars cut") {
		t.Fatalf("long reason not head+tail: cut=%v %d runes", cut, utf8.RuneCountInString(clipped))
	}
	// The milestone label stays one line.
	if label := (ProjectTaskReopen{Round: 2, Cause: ProjectTaskReopenPushFailed, Failure: long}).Milestone(); utf8.RuneCountInString(label) > 120 {
		t.Fatalf("milestone label not one line: %d runes", utf8.RuneCountInString(label))
	}

	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	progress.set("task", wakeStepResultsProgress("push failed: "+long))
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "failed"; t.LastError = long })
	row := outcomeRow(t, f.reconcile(), 0)
	for _, key := range []string{"failure", "reopen_failure"} {
		text, _ := row[key].(string)
		if !strings.Contains(text, "HEAD-REASON") || !strings.HasSuffix(text, "TAIL-REASON") || utf8.RuneCountInString(text) > ProjectTaskWakeReasonRunes {
			t.Fatalf("%s lost its head or tail: %d runes", key, utf8.RuneCountInString(text))
		}
	}
	if row["failure_truncated"] != true || row["failure_bytes"] != float64(len(long)) || row["reopen_failure_truncated"] != true || !strings.Contains(row["reopen_failure_more"].(string), "delivery_id=jdel-1") {
		t.Fatalf("cut reasons not flagged with a read: %v", row)
	}
	if row["summary_source"] != ProjectTaskSummaryLastError {
		t.Fatalf("failed task summary source: %v", row["summary_source"])
	}
}

// Purpose: a wake row says each finished workflow step's outcome: a run
// step, a failing run step (exit code, error, output tail) and a review step
// with its verdict and BLOCKING findings. Threat: the AI only learning
// workflow_state/step and reporting a gate or a review it never saw.
// Boundary: ProjectTaskWaitProgressFrom and projectTaskWaitProgressRow through
// a local wake over a temporary real store.
func TestProjectTaskWakeRowCarriesWorkflowStepResults(t *testing.T) {
	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	progress.set("task", wakeStepResultsProgress("push failed: exited with code 1"))
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "failed"; t.LastError = "workflow sent back" })
	row := outcomeRow(t, f.reconcile(), 0)
	raw, _ := json.Marshal(row["workflow_steps_result"])
	var steps []ProjectTaskStepResult
	if err := json.Unmarshal(raw, &steps); err != nil || len(steps) != 3 {
		t.Fatalf("workflow_steps_result: %s (%v)", raw, err)
	}
	if steps[0].ID != "lint" || steps[0].Status != "succeeded" || steps[0].DurationMs != 1200 || steps[0].OutputTail != "lint ok" {
		t.Fatalf("run step: %+v", steps[0])
	}
	if steps[1].Status != "failed" || steps[1].ExitCode == nil || *steps[1].ExitCode != 1 || !strings.Contains(steps[1].Error, "FAIL TestWakeRow") || !strings.HasSuffix(steps[1].OutputTail, "exit status 1") {
		t.Fatalf("failing run step: %+v", steps[1])
	}
	if steps[2].Type != "agent" || steps[2].Verdict != "block" || len(steps[2].Blocking) != 1 || !strings.HasPrefix(steps[2].Blocking[0], "BLOCKING:") {
		t.Fatalf("review step: %+v", steps[2])
	}
	if row["delivery_id"] != "jdel-1" {
		t.Fatalf("delivery_id: %v", row["delivery_id"])
	}
}

// Purpose (parity): a linked machine's wake row carries the same fields as a
// local one from the same task data (only progress is local and machine
// linked), and every key either row has is a declared wake row key. Threat:
// the remote path dropping the final message, the full failure or the step
// results, the agent stall (agent_stall, the `stalled` milestone's detail),
// or a new field added to one builder only. Boundary:
// projectTaskWatchLocalOutcome and projectTaskWatchRemoteOutcome over a
// temporary real store, both through prepareProjectTaskWatchWake.
func TestProjectTaskWakeRowLocalAndLinkedShapesMatch(t *testing.T) {
	final := wakeFinalMessage()
	stored, _ := ClipProjectTaskResultTail(final, ProjectTaskWakeResultBytes)
	long := "HEAD-REASON " + strings.Repeat("middle detail ", 600) + " TAIL-REASON"
	progress := wakeStepResultsProgress("push failed: " + long)
	progress["agent_stall"] = map[string]any{"kind": ProjectTaskAgentStallSleepPolling, "command": "sleep 290", "background_id": "bg_1", "calls": 4, "since": 1790000000000, "for_ms": 660000}

	f, stub := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	stub.set("task", progress)
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "failed"; t.LastError = long; t.Attempts[0].Summary = stored })
	local := outcomeRow(t, f.reconcile(), 0)

	r := newTaskWaitFixture(t, "home", map[string]any{})
	in := remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"})
	off := false
	in.TaskWait.Milestones = &off
	r.apply(in)
	r.endRun("home", "run-1")
	observed := []V3ProjectTaskRemoteObservation{{TaskID: "t1", Title: "One", Status: "failed", AttemptID: "a1", SessionID: "rs1", AttemptSummary: stored, LastError: long, Progress: ProjectTaskWaitProgressFrom(progress)}}
	remote := outcomeRow(t, taskWaitOutcome(t, r.apply(r.remoteWake(observed, nil)).Message), 0)

	keys := func(row map[string]any, drop string) []string {
		var out []string
		for k := range row {
			if k != drop {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	localKeys, remoteKeys := keys(local, "progress"), keys(remote, "machine")
	if strings.Join(localKeys, ",") != strings.Join(remoteKeys, ",") {
		t.Fatalf("wake row shapes drifted:\nlocal  %v\nlinked %v", localKeys, remoteKeys)
	}
	declared := map[string]bool{}
	for _, k := range ProjectTaskWakeRowKeys {
		declared[k] = true
	}
	for _, k := range append(keys(local, ""), keys(remote, "")...) {
		if !declared[k] {
			t.Fatalf("wake row key %q is not in ProjectTaskWakeRowKeys", k)
		}
	}
	if !reflect.DeepEqual(local["agent_stall"], remote["agent_stall"]) || local["agent_stall"] == nil {
		t.Fatalf("agent_stall differs: local %v linked %v", local["agent_stall"], remote["agent_stall"])
	}
	for _, key := range []string{"summary", "summary_source", "result_truncated", "result_bytes", "failure", "failure_truncated", "reopen_failure", "reopen_failure_truncated", "workflow_steps_result", "delivery_id", "agent_stall"} {
		if _, ok := remote[key]; !ok {
			t.Fatalf("linked wake row lacks %s: %v", key, remote)
		}
		if key == "summary" || key == "failure" || key == "reopen_failure" || key == "result_bytes" {
			if local[key] != remote[key] {
				t.Fatalf("%s differs: local %v linked %v", key, local[key], remote[key])
			}
		}
	}
	if args, _ := remote["read_full_args"].(map[string]any); args["action"] != "read_task_session" || args["attempt_id"] != "a1" || args["machine"] != "box" {
		t.Fatalf("linked read_full_args: %v", remote["read_full_args"])
	}
}
