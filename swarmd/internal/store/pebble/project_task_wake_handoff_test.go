package pebblestore

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// wakeHandoffFixture is a settled-wait fixture whose task's agent ran run-c
// to completion and recorded a task_progress done summary (session metadata
// lifecycle_summary) bound to boundRun.
func wakeHandoffFixture(t *testing.T, handoff, boundRun string) *taskWaitFixture {
	t.Helper()
	f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	f.apply(V3SessionMutationInput{SessionID: "child-task", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "pending-child", RunIntent: &V3SessionRunIntent{RunID: "run-c", Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: "child-task", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "running-child", RunIntent: &V3SessionRunIntent{RunID: "run-c", Status: V3RunIntentRunning}})
	f.apply(V3SessionMutationInput{SessionID: "child-task", Kind: V3SessionMutationUpdateMetadata, ClientRequestID: "task-progress-done", Session: &SessionSnapshot{ID: "child-task", Mode: "auto", Metadata: map[string]any{
		"project_id": "project", "task_id": "task",
		"lifecycle_signal": "needs_review", "lifecycle_summary": handoff, "lifecycle_summary_run_id": boundRun,
	}}})
	f.endRun("child-task", "run-c")
	return f
}

// Purpose: a wake row's summary is the agent's own handoff (its
// task_progress done summary for the followed run) when it recorded one, with
// summary_source "handoff"; the final message is only the fallback. Threat
// (review finding): the summary was the whole final message, whose kept end
// still opened with "Still exploring…" progress lines, so the woken AI
// reported from them. A handoff bound to another run must never be shown.
// Boundary: projectTaskRunHandoff through hydrateTaskAttemptOutcome
// (GetProjectTask) and projectTaskWatchLocalOutcome over a temporary real
// store, and projectTaskWatchRemoteOutcome for a linked machine's attempt
// handoff: the layers that bind the handoff to its run and build both rows.
func TestProjectTaskWakeSummaryIsTheAgentHandoff(t *testing.T) {
	handoff := "Changed project_task_wait.go; ran go test -run TestX: ok; commit abc123."
	progressLines := strings.Repeat("Still exploring the wait code…\n", 20) + "Done."

	f := wakeHandoffFixture(t, handoff, "run-c")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review"; t.Attempts[0].Summary = progressLines })
	row := outcomeRow(t, f.reconcile(), 0)
	if row["summary"] != handoff || row["summary_source"] != ProjectTaskSummaryHandoff {
		t.Fatalf("summary is not the agent's handoff: source %v, %q", row["summary_source"], row["summary"])
	}
	if strings.Contains(row["summary"].(string), "Still exploring") || row["result_truncated"] != nil {
		t.Fatalf("handoff summary carries progress lines or a cut flag: %v", row)
	}

	// A done summary recorded for an earlier run is not this run's handoff.
	stale := wakeHandoffFixture(t, "OLD RUN HANDOFF", "run-old")
	stale.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	staleRow := outcomeRow(t, stale.reconcile(), 0)
	if staleRow["summary_source"] == ProjectTaskSummaryHandoff || strings.Contains(staleRow["summary"].(string), "OLD RUN HANDOFF") {
		t.Fatalf("a handoff bound to another run was shown: %v", staleRow)
	}
	if got := projectTaskRunHandoff(map[string]any{"lifecycle_signal": "needs_review", "lifecycle_summary": handoff, "lifecycle_summary_run_id": "run-c"}, V3SessionRunState{RunID: "run-c", Active: true}); got != "" {
		t.Fatalf("an active run's handoff was read: %q", got)
	}

	// A linked machine's attempt handoff gives the same summary and source.
	r := newTaskWaitFixture(t, "home", map[string]any{})
	in := remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"})
	off := false
	in.TaskWait.Milestones = &off
	r.apply(in)
	r.endRun("home", "run-1")
	observed := []V3ProjectTaskRemoteObservation{{TaskID: "t1", Title: "One", Status: "needs_review", AttemptID: "a1", SessionID: "rs1", AttemptSummary: progressLines, AttemptHandoff: handoff}}
	remote := outcomeRow(t, taskWaitOutcome(t, r.apply(r.remoteWake(observed, nil)).Message), 0)
	if remote["summary"] != row["summary"] || remote["summary_source"] != row["summary_source"] {
		t.Fatalf("linked handoff differs: local %v/%v linked %v/%v", row["summary_source"], row["summary"], remote["summary_source"], remote["summary"])
	}
}

// Purpose: a review's blocking findings are only real BLOCKING findings of a
// review whose verdict is block. Threat (review finding): any line with the
// word, such as "No blocking issues" or "BLOCKING: none" on an ok review,
// woke the Orchestrator with blocking findings that do not exist.
// Boundary: ProjectTaskBlockingFindings, the one helper task progress and
// wake rows use for a step's blocking list.
func TestProjectTaskBlockingFindingsSkipsNegatedFormsAndPassingVerdicts(t *testing.T) {
	findings := strings.Join([]string{
		"1. BLOCKING (requirement not done): no progress field",
		"No blocking issues in the docs.",
		"None blocking.",
		"not blocking: rename X",
		"There are no further blocking findings",
		"Non-blocking: style nit",
		"BLOCKING: none",
		"Blocking findings: (none).",
		"Unblocking the queue is fine",
		"BLOCKING: the summary is the first lines; the rest is not blocking",
	}, "\n")
	want := []string{"1. BLOCKING (requirement not done): no progress field", "BLOCKING: the summary is the first lines; the rest is not blocking"}
	if got := ProjectTaskBlockingFindings(findings, "block"); !reflect.DeepEqual(got, want) {
		t.Fatalf("block review findings:\n got %q\nwant %q", got, want)
	}
	for _, verdict := range []string{"ok", "warn", ""} {
		if got := ProjectTaskBlockingFindings("No blocking issues\nBLOCKING: none\nBLOCKING: a real one", verdict); got != nil {
			t.Fatalf("verdict %q listed blocking findings: %q", verdict, got)
		}
	}
	// A block review with no BLOCKING line still names its block.
	if got := ProjectTaskBlockingFindings("The change breaks the build.\nMore detail.", "block"); !reflect.DeepEqual(got, []string{"The change breaks the build."}) {
		t.Fatalf("block review without BLOCKING lines: %q", got)
	}
}

// Purpose: the task state reason stays a one-line label (it is shown on
// every list row, outcome word and the per-turn prompt), and a wake row
// still carries the full failure it labels: failure for a failed run, with
// head and tail past ProjectTaskWakeReasonRunes. Threat: claiming the label
// keeps 2000 characters while it is cut, or losing the full reason.
// Boundary: ProjectTaskState and projectTaskWatchLocalOutcome over a
// temporary real store.
func TestProjectTaskStateReasonIsALabelAndTheWakeCarriesTheFullFailure(t *testing.T) {
	// Under ProjectTaskWakeReasonRunes, far over the label's cut.
	long := "HEAD-REASON " + strings.Repeat("middle detail ", 100) + " TAIL-REASON"
	_, reason := ProjectTaskState(ProjectTaskRecord{Status: "failed", LastError: long}, "", false, false)
	if !strings.HasPrefix(reason, "run failed: HEAD-REASON") || utf8.RuneCountInString(reason) > 200 || !strings.HasSuffix(reason, "…") {
		t.Fatalf("state reason is not a marked one-line label: %d runes %q", utf8.RuneCountInString(reason), reason)
	}
	f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.register("", "task")
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "failed"; t.LastError = long })
	row := outcomeRow(t, f.reconcile(), 0)
	failure, _ := row["failure"].(string)
	if failure != long || row["failure_truncated"] != nil {
		t.Fatalf("wake failure is not the whole %d-char reason: %d chars", len(long), len(failure))
	}
}
