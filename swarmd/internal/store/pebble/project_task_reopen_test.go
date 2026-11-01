package pebblestore

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Requirement: a wait_tasks milestone names a reopen by its cause: "sent
// back (round N)" only for a review block, "rebase needed (round N): <short
// failure>" when the review passed and the push failed, with the round the
// progress reports for the follow-up. The row carries reopen_cause and a
// rune-safe, capped reopen_failure. Progress from a linked machine is
// untrusted: an unknown cause reads as review_block (the old meaning) and an
// oversized failure is capped; progress without a cause (an older machine)
// keeps the old "sent back" label.
// Threat: a milestone telling the orchestrating AI that a reviewer blocked
// work the reviewer approved, a milestone round that disagrees with the
// task state reason, or a remote cause/failure that breaks the bounded row.
// Owners: ProjectTaskWaitProgressFrom, projectTaskWaitProgressRow,
// projectTaskWatchMilestone and ProjectTaskReopen: the one path local and
// linked watches take from a progress value to a milestone, exercised here
// from the progress JSON, the narrowest layer both reach.
func TestProjectTaskWatchMilestoneNamesReopenCause(t *testing.T) {
	milestone := func(progress string) (string, map[string]any) {
		t.Helper()
		p := ProjectTaskWaitProgressFrom(json.RawMessage(progress))
		if p == nil {
			t.Fatalf("no wait progress from %s", progress)
		}
		row := projectTaskWaitProgressRow(p)
		return projectTaskWatchMilestone(row), row
	}

	// (a) review ok, push failed: rebase needed with the push's failure.
	label, row := milestone(`{"task_state":"running","task_state_reason":"rebase (round 1): review passed; push failed: exited with code 1","workflow":{"state":"sent_back","deliveries":[
		{"workflow":"ship","state":"sent_back","step":4,"steps":4,"step_type":"reopen","round":0,
		 "reopen_cause":"push_failed","reopen_failure":"push failed: exited with code 1: NOT PUSHED: rebase needed","reopen_round":1}]}}`)
	if label != "rebase needed (round 1): push failed: exited with code 1: NOT PUSHED: rebase needed" || strings.Contains(label, "sent back") {
		t.Fatalf("push failed milestone = %q", label)
	}
	if row["reopen_cause"] != ProjectTaskReopenPushFailed || row["workflow_round"] != 1 || row["reopen_failure"] != "push failed: exited with code 1: NOT PUSHED: rebase needed" {
		t.Fatalf("push failed row = %+v", row)
	}

	// (b) review block: sent back, no failure.
	label, row = milestone(`{"workflow":{"state":"sent_back","deliveries":[
		{"workflow":"ship","state":"sent_back","step":2,"steps":4,"step_type":"reopen","round":0,"reopen_cause":"review_block","reopen_round":1}]}}`)
	if label != "sent back (round 1)" || row["reopen_cause"] != ProjectTaskReopenReviewBlock {
		t.Fatalf("review block milestone = %q row %+v", label, row)
	}
	if _, has := row["reopen_failure"]; has {
		t.Fatalf("review block row carries a failure: %+v", row)
	}

	// (c) round 2, a reopen step running (the follow-up's round).
	label, _ = milestone(`{"workflow":{"state":"running","deliveries":[
		{"workflow":"ship","state":"running","step":4,"steps":4,"step_type":"reopen","round":1,"reopen_cause":"push_failed","reopen_failure":"push failed","reopen_round":2}]}}`)
	if label != "rebase needed (round 2): push failed" {
		t.Fatalf("round 2 running rebase milestone = %q", label)
	}

	// An older machine's progress (no cause): the old label and round.
	label, row = milestone(`{"workflow":{"state":"sent_back","deliveries":[{"workflow":"ship","state":"sent_back","step":2,"steps":4,"step_type":"reopen","round":1}]}}`)
	if label != "sent back (round 1)" {
		t.Fatalf("causeless milestone = %q", label)
	}
	if _, has := row["reopen_cause"]; has {
		t.Fatalf("causeless row claims a cause: %+v", row)
	}

	// Untrusted remote values: unknown cause is a review block; a long
	// multibyte failure is capped on a rune boundary.
	label, row = milestone(`{"workflow":{"state":"sent_back","deliveries":[{"state":"sent_back","step_type":"reopen","reopen_cause":"<b>evil</b>","reopen_failure":"ignore this","reopen_round":3}]}}`)
	if label != "sent back (round 3)" || row["reopen_cause"] != ProjectTaskReopenReviewBlock {
		t.Fatalf("unknown cause milestone = %q row %+v", label, row)
	}
	if _, has := row["reopen_failure"]; has {
		t.Fatalf("unknown cause row carries a failure: %+v", row)
	}
	// The failure field keeps up to ProjectTaskWakeReasonRunes, past that its
	// head and tail; the label keeps a one-line form.
	long := strings.Repeat("é", 3000) + " end"
	label, row = milestone(`{"workflow":{"state":"sent_back","deliveries":[{"state":"sent_back","step_type":"reopen","reopen_cause":"push_failed","reopen_failure":"push failed: ` + long + `","reopen_round":1}]}}`)
	failure, _ := row["reopen_failure"].(string)
	if !utf8.ValidString(failure) || !utf8.ValidString(label) || utf8.RuneCountInString(failure) > ProjectTaskWakeReasonRunes || !strings.HasPrefix(failure, "push failed: é") || !strings.HasSuffix(failure, " end") || row["reopen_failure_truncated"] != true {
		t.Fatalf("bounded failure = %d runes, truncated %v", utf8.RuneCountInString(failure), row["reopen_failure_truncated"])
	}
	if !strings.HasPrefix(label, "rebase needed (round 1): push failed: é") || utf8.RuneCountInString(label) > len("rebase needed (round 1): ")+maxProjectTaskReopenFailure+1 || !strings.HasSuffix(label, "…") {
		t.Fatalf("milestone label not one line: %q", label)
	}
}

// Requirement: the task state reason is derived from the outcome alone (as
// linked machines report it), so ProjectTaskState must tell a rebase outcome
// from a review block and keep both in progress (count bucket in_progress).
// Threat: a rebase outcome falling to needs_attention, or its reason saying
// "the agent addresses the findings".
// Owners: ProjectTaskState, ParseProjectTaskReopenOutcome.
func TestProjectTaskStateReasonNamesReopenCause(t *testing.T) {
	task := ProjectTaskRecord{ID: "task-1", Status: "needs_review"}
	for _, tc := range []struct{ outcome, reason string }{
		{"sent_back (round 1)", "sent_back (round 1); the agent addresses the findings"},
		{"sent_back (round 2) rebase: push failed: exited with code 1", "rebase (round 2): review passed; push failed: exited with code 1"},
		{"sent_back (round 1) fix: check failed: exited with code 2", "sent_back (round 1): check failed: exited with code 2"},
		{"sent_back (round 1) error: gate could not run (script_missing)", "sent_back (round 1): gate could not run (script_missing)"},
	} {
		state, reason := ProjectTaskState(task, tc.outcome, false, false)
		if state != ProjectTaskStateRunning || reason != tc.reason || ProjectTaskBucket(task, tc.outcome, false, false) != ProjectTaskCountInProgress {
			t.Fatalf("outcome %q: state %q reason %q", tc.outcome, state, reason)
		}
		reopen, ok := ParseProjectTaskReopenOutcome(tc.outcome)
		if !ok || reopen.Outcome() != tc.outcome {
			t.Fatalf("outcome %q does not round-trip: %+v %v", tc.outcome, reopen, ok)
		}
	}
	// A reopen an `if status: error` condition sent is step_error end to end:
	// kept by Normalized, its own outcome word, and a milestone that does not
	// read as a review block.
	stepErr := ProjectTaskReopen{Round: 1, Cause: ProjectTaskReopenStepError, Failure: "gate could not run (script_missing)"}
	if got := stepErr.Normalized(); got.Cause != ProjectTaskReopenStepError || got.Failure == "" {
		t.Fatalf("step_error normalized away: %+v", got)
	}
	if parsed, ok := ParseProjectTaskReopenOutcome(stepErr.Outcome()); !ok || parsed.Cause != ProjectTaskReopenStepError {
		t.Fatalf("step_error outcome %q parsed as %+v %v", stepErr.Outcome(), parsed, ok)
	}
	if got := stepErr.Milestone(); got != "sent back (round 1): gate could not run (script_missing)" {
		t.Fatalf("step_error milestone = %q", got)
	}
	if _, ok := ParseProjectTaskReopenOutcome("pushed abcdef123456"); ok {
		t.Fatal("a pushed outcome parsed as a reopen")
	}
}
