package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: the rules list counts and the stopped status rest on. A run is the
// person's stop only when cancelled with stop_task's or the session stop's
// reason; quota, shutdown or other cancellations and failed runs are not. A
// task counts by its outcome once its run ended: pushed/delivered/integrated
// are done, running/sent_back/workflow pending in progress, blocked, failed
// and ready to integrate need attention; finished (nothing to integrate) is
// done; a finished task idle for over 7 days with work never handed over is
// idle, not attention, while idle never hides a failed/blocked status or a
// stop; a stopped or stale task is stopped whatever its outcome; a failed
// status needs attention even after an earlier push. Threat: needs_review
// counted as waiting for review, hundreds of long-finished tasks called
// "needing attention", a stop counted as a failure, or a quota cancellation
// hidden as a stop. Owners:
// ProjectTaskRunStoppedByPerson and ProjectTaskCountBucket (pure; the api
// tests prove the wiring).
func TestProjectTaskStoppedAndCountRules(t *testing.T) {
	for _, c := range []struct {
		status, reason string
		want           bool
	}{
		{V3RunIntentCancelled, ProjectTaskStopReason, true},
		{V3RunIntentCancelled, V3RunStoppedByUser, true},
		{V3RunIntentCancelled, "daily spending limit reached", false},
		{V3RunIntentCancelled, "", false},
		{V3RunIntentFailed, ProjectTaskStopReason, false},
		{V3RunIntentInterrupted, V3RunStoppedByUser, false},
	} {
		if got := ProjectTaskRunStoppedByPerson(c.status, c.reason); got != c.want {
			t.Fatalf("stopped by person(%q, %q) = %v, want %v", c.status, c.reason, got, c.want)
		}
	}
	for _, c := range []struct {
		status, outcome string
		stale, idle     bool
		want            string
	}{
		{"needs_review", "pushed 0123456789ab", false, false, ProjectTaskCountDone},
		{"needs_review", "delivered folder", false, false, ProjectTaskCountDone},
		{"needs_review", "integrated", false, false, ProjectTaskCountDone},
		{"completed", "", false, false, ProjectTaskCountDone},
		{"needs_review", "sent_back (round 2)", false, false, ProjectTaskCountInProgress},
		{"needs_review", "running review", false, false, ProjectTaskCountInProgress},
		{"needs_review", "workflow pending", false, false, ProjectTaskCountInProgress},
		{"in_progress", "", false, false, ProjectTaskCountInProgress},
		{"queued", "", false, false, ProjectTaskCountWaiting},
		{"pending_approval", "", false, false, ProjectTaskCountWaiting},
		{"needs_review", "blocked rounds_exhausted: step send-back", false, false, ProjectTaskCountNeedsAttention},
		{"needs_review", "failed push", false, false, ProjectTaskCountNeedsAttention},
		{"needs_review", "ready to integrate", false, false, ProjectTaskCountNeedsAttention},
		{"needs_review", "", false, false, ProjectTaskCountNeedsAttention},
		{"failed", "pushed 0123456789ab", false, false, ProjectTaskCountNeedsAttention},
		{"blocked", "", false, false, ProjectTaskCountNeedsAttention},
		{ProjectTaskStatusStopped, "stopped", false, false, ProjectTaskCountStopped},
		{ProjectTaskStatusStopped, "pushed 0123456789ab", false, false, ProjectTaskCountStopped},
		{"in_progress", "", true, false, ProjectTaskCountStopped},
		{"blocked", "", true, false, ProjectTaskCountStopped},
		// A finished task with nothing to integrate is done, recent or old.
		{"needs_review", "finished", false, false, ProjectTaskCountDone},
		{"needs_review", "finished", false, true, ProjectTaskCountDone},
		// Work never handed over, finished over 7 days ago: idle, not
		// attention; a handed-over or running one keeps its bucket.
		{"needs_review", "ready to integrate", false, true, ProjectTaskCountIdle},
		{"needs_review", "blocked rounds_exhausted: step send-back", false, true, ProjectTaskCountIdle},
		{"needs_review", "", false, true, ProjectTaskCountIdle},
		{"needs_review", "pushed 0123456789ab", false, true, ProjectTaskCountDone},
		{"needs_review", "sent_back (round 2)", false, true, ProjectTaskCountInProgress},
		// Idle never hides a failed or blocked status, or a stop.
		{"failed", "ready to integrate", false, true, ProjectTaskCountNeedsAttention},
		{"blocked", "", false, true, ProjectTaskCountNeedsAttention},
		{ProjectTaskStatusStopped, "stopped", false, true, ProjectTaskCountStopped},
	} {
		if got := ProjectTaskCountBucket(c.status, c.outcome, c.stale, c.idle); got != c.want {
			t.Fatalf("bucket(%q, %q, stale=%v, idle=%v) = %q, want %q", c.status, c.outcome, c.stale, c.idle, got, c.want)
		}
	}
	counts := NewProjectTaskOutcomeCounts()
	for _, bucket := range []string{ProjectTaskCountDone, ProjectTaskCountInProgress, ProjectTaskCountWaiting, ProjectTaskCountNeedsAttention, ProjectTaskCountIdle, ProjectTaskCountStopped} {
		if n, ok := counts[bucket]; !ok || n != 0 {
			t.Fatalf("zero counts miss %s: %v", bucket, counts)
		}
	}
	if len(counts) != 6 {
		t.Fatalf("zero counts = %v", counts)
	}
}

// Purpose: a task's messages are bounded: follow-ups that reopened it are
// listed as reopened with their run, only the newest three are kept
// (oldest first) and each is cut to 80 characters with its whitespace
// collapsed; the first attempt (the task's own request) is not a message.
// Threat: get_task growing with every message, or the original request
// reported as a message. Owner: SessionStore.ProjectTaskMessages over a real
// temporary store (the queued/delivered notes are proven by the api tests).
func TestProjectTaskMessagesAreTheNewestThreeCut(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "messages"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSessionStore(db)
	long := strings.Repeat("word ", 40)
	task := &ProjectTaskRecord{ID: "t", Attempts: []ProjectTaskAttempt{
		{ID: "initial", Request: "the task itself", CreatedAt: 1},
		{ID: "a2", Request: "first follow-up", RunID: "run-2", SessionID: "s2", CreatedAt: 2},
		{ID: "a3", Request: "second\n\nfollow-up", RunID: "run-3", SessionID: "s3", CreatedAt: 3},
		{ID: "a4", Request: "third", RunID: "run-4", SessionID: "s4", CreatedAt: 4},
		{ID: "a5", Request: long, RunID: "run-5", SessionID: "s5", CreatedAt: 5},
	}}
	messages, err := store.ProjectTaskMessages(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != ProjectTaskMessagesShown {
		t.Fatalf("messages = %+v, want the newest %d", messages, ProjectTaskMessagesShown)
	}
	if messages[0].Text != "second follow-up" || messages[0].RunID != "run-3" || messages[1].Text != "third" || messages[2].RunID != "run-5" {
		t.Fatalf("messages = %+v", messages)
	}
	for _, m := range messages {
		if m.State != ProjectTaskMessageReopened || len([]rune(m.Text)) > ProjectTaskMessageTextRunes {
			t.Fatalf("message %+v", m)
		}
	}
	if want := strings.TrimSpace(long)[:ProjectTaskMessageTextRunes]; messages[2].Text != want {
		t.Fatalf("long message = %q, want %q", messages[2].Text, want)
	}
}
