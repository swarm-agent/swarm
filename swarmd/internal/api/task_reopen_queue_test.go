package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a workflow's send-back (reopen step) to a task that is busy with a
// run (here a follow-up the person sent after the reviewed run ended) is
// queued for that run through the session message path, exactly as a
// person's message is: it is accepted as a queued note for the running run,
// starts no second run, replays on a repeated request id, and, when the run
// ends without having read it, starts the task's next turn carrying it. A
// task that must not be reopened (stopped by the person, archived, deleted)
// is refused with that reason and nothing reaches its session.
// Threat (seen live): the send-back waited for the busy task to settle, timed
// out, failed the delivery and the reviewer's findings never reached the
// coder; or, the other way, a refusal hidden behind a queued message.
// Owners: jobAutomationReopener.ReopenOrQueue -> Server.reopenTaskSent over
// the real POST /v3/sessions/{id}/messages route, acceptSessionsV3QueuedNote,
// sessionV3Executor.closeQueuedNotes and the project task store. Run intents
// are recorded through the canonical session mutation and the executor's
// note queue is opened as an executing run opens it; no provider is reached.
// This service layer is the narrowest one where the task record, the route
// and the queued-note authority meet.
func TestReopenQueuesFindingsForABusyTaskAndRefusesAStoppedOrDeletedOne(t *testing.T) {
	f := newReopenFixture(t)
	if f.server.v3SessionExecutor == nil {
		f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	}
	p := testPrincipal()
	db := f.server.sessions.Store()
	sessionID := f.session.ID
	task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "task_busy", ProjectID: "proj-busy", Agent: "coder", Status: "in_progress"})
	jobID := "task/proj-busy/" + task.ID
	record := func(runID, status string) {
		t.Helper()
		key := runID + "-" + status
		in := sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: status}}
		if _, err := f.server.applySessionV3PrimaryMutation(in); err != nil {
			t.Fatalf("record %s %s: %v", runID, status, err)
		}
	}
	// The reviewed run ended; the person's follow-up is running and takes
	// queued messages, as an executing run does.
	reviewed := pebblestore.InitialTaskRunID(task.ID)
	for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning, pebblestore.V3RunIntentCompleted} {
		record(reviewed, status)
	}
	const current = "run-person-followup"
	record(current, pebblestore.V3RunIntentPendingExecutor)
	record(current, pebblestore.V3RunIntentRunning)
	job := sessionV3ExecutorJob{SessionID: sessionID, RunID: current, queuedNotes: runruntime.NewQueuedNoteTracker(0)}
	executor := f.server.v3SessionExecutor
	executor.mu.Lock()
	executor.openQueuedNotesLocked(job)
	executor.mu.Unlock()

	// The runner queues only through a QueueingReopener: the daemon's must be one.
	reopener, ok := f.server.JobAutomationReopener().(jobautomation.QueueingReopener)
	if !ok {
		t.Fatal("the daemon's reopener cannot queue a send-back for a busy job")
	}
	findings := "The reviewer blocked your work.\n\n1. high api/upload.go:40 the limit is never checked\n\n" + jobautomation.ReopenClosing
	req := jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: findings, RequestID: "jdel_busy.fix"}
	sent, err := reopener.ReopenOrQueue(context.Background(), req)
	if err != nil {
		t.Fatalf("send-back to a busy task: %v, want it queued", err)
	}
	if !sent.Queued || sent.Followup != current {
		t.Fatalf("sent = %+v, want queued for the running run %s", sent, current)
	}
	notes := func() []pebblestore.MessageSnapshot {
		t.Helper()
		messages, err := f.server.sessions.ListMessages(sessionID, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.MessageSnapshot
		for _, m := range messages {
			if m.Role == "user" {
				out = append(out, m)
			}
		}
		return out
	}
	got := notes()
	if len(got) != 1 || got[0].Content != findings || !runruntime.IsQueuedNote(got[0]) || got[0].Metadata[runruntime.QueuedNoteRunMetadataKey] != current {
		t.Fatalf("session user messages = %+v, want the findings queued once for %s", got, current)
	}
	note := got[0]
	if active, ok, err := f.server.sessions.GetSessionActiveRunIntent(sessionID); err != nil || !ok || active.RunID != current {
		t.Fatalf("active run = %+v %v %v, want only the person's run (no second run)", active, ok, err)
	}
	// The same request again (a restart before the delivery was saved)
	// replays: nothing is sent twice.
	again, err := reopener.ReopenOrQueue(context.Background(), req)
	if err != nil || again != sent {
		t.Fatalf("replayed send-back = %+v %v, want %+v", again, err, sent)
	}
	if n := len(notes()); n != 1 {
		t.Fatalf("a replayed send-back was stored again: %d user messages", n)
	}

	// The run ends without having read the note: the findings start the
	// task's next turn, delivered after the current run.
	record(current, pebblestore.V3RunIntentCompleted)
	executor.closeQueuedNotes(job)
	intents, err := db.ListRunIntents(sessionID, 100)
	if err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, intent := range intents {
		if intent.SourceMessageID == note.ID && intent.RunID != current && intent.RunID != reviewed {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("run intents after the run ended = %+v, want a next turn carrying the queued findings %s", intents, note.ID)
	}

	// Real refusals fail with their reason and send nothing.
	before := len(notes())
	refusal := func(name, want string, change func()) {
		t.Helper()
		change()
		_, err := reopener.ReopenOrQueue(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: "fix it", RequestID: "jdel_refused." + name})
		var refused *jobautomation.RefusedError
		if !errors.As(err, &refused) || !strings.Contains(refused.Reason, want) {
			t.Fatalf("%s task: err = %v, want refused naming %q", name, err, want)
		}
		var busy interface{ Busy() bool }
		if errors.As(err, &busy) && busy.Busy() {
			t.Fatalf("%s task answered busy: %v", name, err)
		}
		if n := len(notes()); n != before {
			t.Fatalf("%s task got a message: %d user messages, want %d", name, n, before)
		}
	}
	update := func(edit func(*pebblestore.ProjectTaskRecord)) func() {
		return func() {
			if _, err := db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(r *pebblestore.ProjectTaskRecord) error {
				edit(r)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	refusal("stopped", "stopped by the person", update(func(r *pebblestore.ProjectTaskRecord) { r.Status = pebblestore.ProjectTaskStatusStopped }))
	refusal("archived", "archived", update(func(r *pebblestore.ProjectTaskRecord) { r.Status, r.Archived = "needs_review", true }))
	refusal("deleted", "deleted", func() {
		if err := db.DeleteProjectTask(p.AccountScopeID, task.ProjectID, task.ID); err != nil {
			t.Fatal(err)
		}
	})
}
