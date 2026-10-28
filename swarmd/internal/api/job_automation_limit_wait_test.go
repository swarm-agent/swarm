package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: when an agent step's job is a custom direct-role worker's run
// that waits for its provider's limit to reset (status waiting, retry_at the
// reset, queue_reason what it waits for), jobAutomationAgents.Wait returns
// at once with that wait (LimitUntil, the reason, no verdict) instead of
// blocking inside the step's timeout; a run whose reset is already due is
// not returned as waiting (the worker scheduler starts its rerun), so Wait
// keeps reading until its context ends.
// Threat (review finding): Wait only returned on succeeded/failed/cancelled,
// so a step whose worker run waited hours for a reset timed out and failed.
// Owners: jobAutomationAgents.Wait over the daemon's real worker run route
// (handleWorkerRunByID) and worker store; no model provider is involved.
func TestAgentWaitReturnsAWorkerRunWaitingOnAProviderLimit(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	author, err := f.server.sessions.CreateWorker(ctx, p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Author", IdempotencyKey: "limit-wait-author"})
	if err != nil {
		t.Fatal(err)
	}
	reason := "waiting for the provider's limit to reset (until 2099-01-01T16:01:00Z): You've hit your session limit · resets 4pm (UTC)"
	record := func(runID string, retryAt int64) string {
		t.Helper()
		if _, err := f.server.sessions.Store().WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: runID, WorkerID: author.ID, UserID: p.UserID,
			Role: pebblestore.WorkerRoleCoder, Status: pebblestore.WorkerRunStatusWaiting, RequestSource: jobautomation.RequestSourceAutomation,
			Input: map[string]any{"prompt": "Write the plan"}, QueueReason: reason, RetryAt: retryAt, LimitWaits: 1, LimitRunID: runID, CreatedAt: 1, StartedAt: 1}); err != nil {
			t.Fatal(err)
		}
		return controlMCPJobID(controlMCPJobWorker, author.ID, runID)
	}

	until := time.Now().Add(3 * time.Hour).UnixMilli()
	job := record("run-limited", until)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := f.server.JobAutomationAgents().Wait(waitCtx, p.AccountScopeID, job)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if out.JobID != job || out.Status != pebblestore.WorkerRunStatusWaiting || out.LimitUntil != until || out.Error != reason || out.Verdict != "" || out.Result != nil {
		t.Fatalf("a run waiting on a provider limit = %+v", out)
	}

	due := record("run-due", time.Now().Add(-time.Second).UnixMilli())
	dueCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if out, err := f.server.JobAutomationAgents().Wait(dueCtx, p.AccountScopeID, due); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a run whose reset is due was returned: %+v %v", out, err)
	}
}
