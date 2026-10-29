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
		// Waiting for the person's approval or answer: only they can act.
		{"pending_approval", "", false, false, ProjectTaskCountNeedsAttention},
		{"needs_input", "", false, false, ProjectTaskCountNeedsAttention},
		{"needs_review", "stopped", false, false, ProjectTaskCountStopped},
		{"needs_review", "delivered files ready", false, false, ProjectTaskCountDone},
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

// Purpose: ProjectTaskNeedsAttention is the one decision whether the person
// must act on a task, and ProjectTaskBucket the count it agrees with: a task
// needs attention only when it is blocked, failed (not stopped), waiting for
// approval or an answer, or has work no workflow hands over; an archived
// task (the person put it away) and a workflow step's own task (a reviewer
// or fix run, whose work is handed over on the task it serves) never do,
// whatever their status or outcome, and count as done; their running,
// waiting or stopped states keep their buckets. Threat: a board of finished
// work (workflow runs carrying the job's commits, archived failures) called
// "needing attention", or a row flag that disagrees with the count. Owners:
// ProjectTaskBucket and ProjectTaskNeedsAttention (pure; the api tests prove
// list_tasks, get_task and the route use them, here and linked).
func TestProjectTaskNeedsAttentionIsTheCountsRule(t *testing.T) {
	workflowTask := ProjectTaskRecord{ID: "task_wf_1", AutomationID: "workflow:k"}
	for _, c := range []struct {
		name    string
		task    ProjectTaskRecord
		outcome string
		stale   bool
		idle    bool
		want    string
	}{
		{"commits nobody hands over", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "ready to integrate", false, false, ProjectTaskCountNeedsAttention},
		{"failed", ProjectTaskRecord{ID: "t", Status: "failed"}, "", false, false, ProjectTaskCountNeedsAttention},
		{"approval", ProjectTaskRecord{ID: "t", Status: "pending_approval"}, "", false, false, ProjectTaskCountNeedsAttention},
		{"pushed", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "pushed 0123456789ab", false, false, ProjectTaskCountDone},
		{"finder", ProjectTaskRecord{ID: "t", Agent: "finder", Status: "needs_review"}, "finished", false, false, ProjectTaskCountDone},
		{"stopped", ProjectTaskRecord{ID: "t", Status: ProjectTaskStatusStopped}, "stopped", false, false, ProjectTaskCountStopped},
		// A workflow's own task: its work belongs to the task it serves.
		{"workflow task with commits", withStatus(workflowTask, "needs_review"), "ready to integrate", false, false, ProjectTaskCountDone},
		{"workflow task failed", withStatus(workflowTask, "failed"), "", false, false, ProjectTaskCountDone},
		{"workflow task by id only", ProjectTaskRecord{ID: "task_wf_2", Status: "needs_review"}, "ready to integrate", false, false, ProjectTaskCountDone},
		{"workflow task old", withStatus(workflowTask, "needs_review"), "ready to integrate", false, true, ProjectTaskCountDone},
		{"workflow task running", withStatus(workflowTask, "in_progress"), "", false, false, ProjectTaskCountInProgress},
		{"workflow task stale", withStatus(workflowTask, "in_progress"), "", true, false, ProjectTaskCountStopped},
		// Archived: the person put it away.
		{"archived failure", ProjectTaskRecord{ID: "t", Status: "failed", Archived: true}, "", false, false, ProjectTaskCountDone},
		{"archived commits", ProjectTaskRecord{ID: "t", Status: "needs_review", Archived: true}, "ready to integrate", false, false, ProjectTaskCountDone},
		{"archived queued", ProjectTaskRecord{ID: "t", Status: "queued", Archived: true}, "", false, false, ProjectTaskCountWaiting},
	} {
		got := ProjectTaskBucket(c.task, c.outcome, c.stale, c.idle)
		if got != c.want {
			t.Fatalf("%s: bucket = %q, want %q", c.name, got, c.want)
		}
		if attention := ProjectTaskNeedsAttention(c.task, c.outcome, c.stale, c.idle); attention != (c.want == ProjectTaskCountNeedsAttention) {
			t.Fatalf("%s: needs attention = %v disagrees with bucket %q", c.name, attention, got)
		}
	}
}

func withStatus(task ProjectTaskRecord, status string) ProjectTaskRecord {
	task.Status = status
	return task
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

// Purpose: ProjectTaskState is the one word every surface shows for a task
// (list_tasks, get_task, linked machines, the Orchestrator's prompt), read
// from the same inputs as the list counts: a run that ended (needs_review)
// with a pushed outcome is landed with its SHA, a running or sent-back
// workflow is running, a branch never handed over is not_integrated (needs
// attention within 7 days, idle after), finished with nothing to integrate is
// done, a stop is stopped and a failed run needs the person. Threat: the run
// status needs_review shown as the task's status, so pushed work reads as
// waiting for review, or a state that disagrees with its count bucket (a
// landed task counted needs_attention). Owners: ProjectTaskState over
// ProjectTaskBucket / ProjectTaskNeedsAttention (pure; the tool and run tests
// prove the surfaces use it).
func TestProjectTaskStateIsTheBucketsWord(t *testing.T) {
	agreeing := map[string][]string{
		ProjectTaskStateRunning:       {ProjectTaskCountInProgress},
		ProjectTaskStateWaiting:       {ProjectTaskCountWaiting},
		ProjectTaskStateNeedsYou:      {ProjectTaskCountNeedsAttention},
		ProjectTaskStateLanded:        {ProjectTaskCountDone},
		ProjectTaskStateDone:          {ProjectTaskCountDone},
		ProjectTaskStateNotIntegrated: {ProjectTaskCountNeedsAttention, ProjectTaskCountIdle},
		ProjectTaskStateStopped:       {ProjectTaskCountStopped},
	}
	branchTask := ProjectTaskRecord{ID: "t", Status: "needs_review", WorktreeBranch: "agent/fix-parser"}
	for _, c := range []struct {
		name          string
		task          ProjectTaskRecord
		outcome       string
		stale, idle   bool
		want          string
		reasonHas     string
		wantAttention bool
	}{
		{"pushed after the run ended", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "pushed 5711ae410abc", false, false, ProjectTaskStateLanded, "pushed 5711ae410abc", false},
		{"pushed and old", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "pushed 5711ae410abc", false, true, ProjectTaskStateLanded, "5711ae410abc", false},
		{"delivered", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "delivered merge 0123456789ab", false, false, ProjectTaskStateLanded, "delivered merge", false},
		{"integrated", ProjectTaskRecord{ID: "t", Status: "completed"}, "integrated", false, false, ProjectTaskStateLanded, "integrated", false},
		{"workflow running", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "running review", false, false, ProjectTaskStateRunning, "running review", false},
		{"agent running", ProjectTaskRecord{ID: "t", Status: "in_progress"}, "", false, false, ProjectTaskStateRunning, "in_progress", false},
		{"sent back", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "sent_back (round 2)", false, false, ProjectTaskStateRunning, "sent_back (round 2)", false},
		{"queued", ProjectTaskRecord{ID: "t", Status: "queued"}, "", false, false, ProjectTaskStateWaiting, "queued", false},
		{"ready to integrate, recent", branchTask, "ready to integrate", false, false, ProjectTaskStateNotIntegrated, "agent/fix-parser", true},
		{"ready to integrate, over 7 days", branchTask, "ready to integrate", false, true, ProjectTaskStateNotIntegrated, "agent/fix-parser", false},
		{"finished, nothing to integrate", ProjectTaskRecord{ID: "t", Agent: "finder", Status: "needs_review"}, "finished", false, false, ProjectTaskStateDone, "nothing to integrate", false},
		{"completed", ProjectTaskRecord{ID: "t", Status: "completed"}, "", false, false, ProjectTaskStateDone, "completed", false},
		{"stopped", ProjectTaskRecord{ID: "t", Status: ProjectTaskStatusStopped}, "stopped", false, false, ProjectTaskStateStopped, "stopped by the person", false},
		{"stale", ProjectTaskRecord{ID: "t", Status: "in_progress"}, "", true, false, ProjectTaskStateStopped, "no active run", false},
		{"failed", ProjectTaskRecord{ID: "t", Status: "failed", LastError: "provider error"}, "", false, false, ProjectTaskStateNeedsYou, "provider error", true},
		{"failed after a push", ProjectTaskRecord{ID: "t", Status: "failed"}, "pushed 5711ae410abc", false, false, ProjectTaskStateNeedsYou, "run failed", true},
		{"sent back until exhausted", ProjectTaskRecord{ID: "t", Status: "needs_review"}, "blocked rounds_exhausted: step send-back", false, false, ProjectTaskStateNeedsYou, "rounds_exhausted", true},
		{"approval", ProjectTaskRecord{ID: "t", Status: "pending_approval"}, "", false, false, ProjectTaskStateNeedsYou, "approval", true},
		{"question", ProjectTaskRecord{ID: "t", Status: "needs_input"}, "", false, false, ProjectTaskStateNeedsYou, "answer", true},
		{"archived failure", ProjectTaskRecord{ID: "t", Status: "failed", Archived: true}, "", false, false, ProjectTaskStateDone, "archived", false},
		{"workflow step task", ProjectTaskRecord{ID: "t", Status: "needs_review", AutomationID: "workflow:k"}, "ready to integrate", false, false, ProjectTaskStateDone, "workflow step", false},
	} {
		state, reason := ProjectTaskState(c.task, c.outcome, c.stale, c.idle)
		if state != c.want || !strings.Contains(reason, c.reasonHas) {
			t.Fatalf("%s: state = %q (%q), want %q with %q", c.name, state, reason, c.want, c.reasonHas)
		}
		bucket := ProjectTaskBucket(c.task, c.outcome, c.stale, c.idle)
		ok := false
		for _, b := range agreeing[state] {
			ok = ok || b == bucket
		}
		if !ok {
			t.Fatalf("%s: state %q disagrees with count bucket %q", c.name, state, bucket)
		}
		if attention := ProjectTaskNeedsAttention(c.task, c.outcome, c.stale, c.idle); attention != c.wantAttention {
			t.Fatalf("%s: needs attention = %v, want %v", c.name, attention, c.wantAttention)
		}
	}
}

// Purpose: a code deliverable's stored status is the placeholder set when the
// task was created ("pending") and never kept in step; what a surface shows
// for it is derived from the task state, so finished work never reads
// pending, while other deliverables (media assets) keep their stored status.
// Threat: the Orchestrator told every turn that pushed work is pending.
// Owner: ProjectTaskDeliverableStatus (pure).
func TestProjectTaskCodeDeliverableStatusFollowsTheState(t *testing.T) {
	code := ProjectTaskDeliverable{ID: "deliv_code", Kind: "pr", Status: "pending"}
	for state, want := range map[string]string{ProjectTaskStateLanded: "delivered", ProjectTaskStateNotIntegrated: "on branch", ProjectTaskStateRunning: "in progress"} {
		if got := ProjectTaskDeliverableStatus(code, state); got != want {
			t.Fatalf("code deliverable in %s = %q, want %q", state, got, want)
		}
	}
	if got := ProjectTaskDeliverableStatus(ProjectTaskDeliverable{ID: "img_1", Kind: "image", Status: "pending"}, ProjectTaskStateLanded); got != "pending" {
		t.Fatalf("asset deliverable status = %q, want its stored status", got)
	}
	if got := ProjectTaskDeliverableStatus(ProjectTaskDeliverable{ID: "deliv_code", Kind: "pr", Status: "accepted"}, ProjectTaskStateRunning); got != "accepted" {
		t.Fatalf("explicit code deliverable status = %q, want accepted", got)
	}
	outcome, stale, idle := ProjectTaskProgressFlags(map[string]any{"outcome": "pushed abc123", "stale": true, "idle": true})
	if outcome != "pushed abc123" || !stale || !idle {
		t.Fatalf("progress flags = %q %v %v", outcome, stale, idle)
	}
	if outcome, stale, idle := ProjectTaskProgressFlags(nil); outcome != "" || stale || idle {
		t.Fatalf("nil progress flags = %q %v %v", outcome, stale, idle)
	}
}
