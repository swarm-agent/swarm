package api

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a send-back queued for a task whose run in flight is a provider
// limit continuation (<run>.limit<n>) is keyed by the follow-up the task's
// end names for that run (<run>), so the round it opens is the one that end
// closes. When the continuation continues the task's first run, whose end is
// the task's first end (no follow-up), nothing is queued: the step is
// answered busy and the session gets no message.
// Threat (review finding): the reopen was recorded under "<run>.limit<n>",
// which no task end carries; a blocked end of that run then matched no
// reopen, recorded no delivery and left the round open forever.
// Owners: Server.reopenTaskSent and taskRunEndFollowup (shared with
// signalTaskRunEnded), over the real POST /v3/sessions/{id}/messages route
// and the executor's queued-note authority. No provider is reached; this is
// the narrowest layer where the executor's run id and the end's naming meet.
func TestReopenQueuedForALimitContinuationIsKeyedByTheRunItContinues(t *testing.T) {
	setup := func(t *testing.T, running string) (jobautomation.QueueingReopener, pebblestore.ProjectTaskRecord, func() int) {
		t.Helper()
		f := newReopenFixture(t)
		if f.server.v3SessionExecutor == nil {
			f.server.v3SessionExecutor = newSessionV3Executor(f.server)
		}
		p := testPrincipal()
		sessionID := f.session.ID
		task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "task_limit", ProjectID: "proj-limit", Agent: "coder", Status: "in_progress"})
		record := func(runID, status string) {
			t.Helper()
			key := runID + "-" + status
			in := sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
				ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: status}}
			if _, err := f.server.applySessionV3PrimaryMutation(in); err != nil {
				t.Fatalf("record %s %s: %v", runID, status, err)
			}
		}
		record(running, pebblestore.V3RunIntentPendingExecutor)
		record(running, pebblestore.V3RunIntentRunning)
		executor := f.server.v3SessionExecutor
		executor.mu.Lock()
		executor.openQueuedNotesLocked(sessionV3ExecutorJob{SessionID: sessionID, RunID: running, queuedNotes: runruntime.NewQueuedNoteTracker(0)})
		executor.mu.Unlock()
		reopener, ok := f.server.JobAutomationReopener().(jobautomation.QueueingReopener)
		if !ok {
			t.Fatal("the daemon's reopener cannot queue a send-back")
		}
		userMessages := func() int {
			t.Helper()
			messages, err := f.server.sessions.ListMessages(sessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, m := range messages {
				if m.Role == "user" {
					n++
				}
			}
			return n
		}
		return reopener, task, userMessages
	}

	t.Run("a follow-up's continuation", func(t *testing.T) {
		const base = "run-person-followup"
		running := pebblestore.ProjectTaskLimitRerunID(base, 1)
		reopener, task, userMessages := setup(t, running)
		sent, err := reopener.ReopenOrQueue(context.Background(), jobautomation.ReopenRequest{Account: testPrincipal().AccountScopeID, JobID: "task/proj-limit/" + task.ID, Message: "fix the limit check", RequestID: "jdel_limit.fix"})
		if err != nil {
			t.Fatalf("send-back: %v", err)
		}
		if !sent.Queued || sent.Followup != base {
			t.Fatalf("sent = %+v, want queued and keyed by %s (the run %s continues)", sent, base, running)
		}
		// The key equals what the task's end names for the running run.
		if end := taskRunEndFollowup(task.ID, running); end != sent.Followup {
			t.Fatalf("end of %s names %q, reopen keyed %q", running, end, sent.Followup)
		}
		if n := userMessages(); n != 1 {
			t.Fatalf("user messages = %d, want the findings queued once", n)
		}
	})

	t.Run("the first run's continuation", func(t *testing.T) {
		reopener, task, userMessages := setup(t, pebblestore.ProjectTaskLimitRerunID(pebblestore.InitialTaskRunID("task_limit"), 2))
		_, err := reopener.ReopenOrQueue(context.Background(), jobautomation.ReopenRequest{Account: testPrincipal().AccountScopeID, JobID: "task/proj-limit/" + task.ID, Message: "fix it", RequestID: "jdel_first.fix"})
		var busy interface{ Busy() bool }
		if !errors.As(err, &busy) || !busy.Busy() {
			t.Fatalf("send-back during the first run's continuation: %v, want busy", err)
		}
		if n := userMessages(); n != 0 {
			t.Fatalf("user messages = %d, want nothing queued", n)
		}
	})
}
