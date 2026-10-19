package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// reopenFixture is a real V3 session owned by a recorded worker job, read
// and written through the daemon's own routes (internalRouteHandler), as a
// reopen step does in the daemon.
type reopenFixture struct {
	server   *Server
	session  pebblestore.SessionSnapshot
	workerID string
}

func newReopenFixture(t *testing.T) reopenFixture {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	ids := pebblestore.NewIdentityStore(sessions.Store().Underlying())
	if _, err := ids.PutUser(p.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(p.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(p.AccountUser); err != nil {
		t.Fatal(err)
	}
	session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "reopen-job-session", "Coder job", t.TempDir(),
		pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	worker, err := sessions.CreateWorker(context.Background(), p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "reopen-coder"})
	if err != nil {
		t.Fatal(err)
	}
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "sessions:write", "automations:read"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }
	return reopenFixture{server: server, session: session, workerID: worker.ID}
}

func (f reopenFixture) job(t *testing.T, runID, status, sessionID string) string {
	t.Helper()
	p := testPrincipal()
	if _, err := f.server.sessions.Store().WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{
		ID: runID, WorkerID: f.workerID, UserID: p.UserID, SessionID: sessionID, Status: status, RequestSource: "direct",
		Input: map[string]any{"prompt": "Add a rate limit to the upload route."}, CreatedAt: 1, StartedAt: 1, CompletedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	return controlMCPJobID(controlMCPJobWorker, f.workerID, runID)
}

func (f reopenFixture) userMessages(t *testing.T, sessionID string) []string {
	t.Helper()
	messages, err := f.server.sessions.ListMessages(sessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

// Requirement: a reopen step's follow-up goes into the triggering job's own
// session through swarm_message_job's path (controlMCPMessageJob): the same
// session, never a new job; it is accepted with the follow-up's run id, and
// the same request id never sends twice. A cancelled job is refused with the
// reason and nothing is sent; another account's automation cannot reopen the
// owner's job.
// Threat: a fix applied in a fresh job and worktree instead of the reviewed
// one, a duplicate follow-up after a restart, or a cancelled job revived.
// Owners: jobAutomationReopener.Reopen over the real session routes and
// worker run records; the model provider is never reached by this check.
func TestReopenSendsTheFollowupIntoTheJobsOwnSession(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	jobID := f.job(t, "run-reopen-1", "succeeded", f.session.ID)
	reopener := f.server.JobAutomationReopener()
	text := "The reviewer blocked your work.\n\n1. high api/upload.go:40 the limit is never checked\n\n" + jobautomation.ReopenClosing
	before := len(f.userMessages(t, f.session.ID))

	followup, err := reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: text, RequestID: "jdel_reopen.fix"})
	if err != nil {
		t.Fatal(err)
	}
	if followup == "" {
		t.Fatal("no follow-up run id")
	}
	got := f.userMessages(t, f.session.ID)
	if len(got) != before+1 || got[len(got)-1] != text {
		t.Fatalf("session user messages = %q", got)
	}
	runs, _, err := f.server.sessions.Store().WorkerStore().ListWorkerRuns(p.AccountScopeID, f.workerID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("worker jobs after reopen = %d %v (a reopen must not start a new job)", len(runs), err)
	}

	// The same request again (a restart) sends nothing new.
	again, err := reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: text, RequestID: "jdel_reopen.fix"})
	if err == nil && again != followup {
		t.Fatalf("replayed follow-up = %q, want %q", again, followup)
	}
	if n := len(f.userMessages(t, f.session.ID)); n != before+1 {
		t.Fatalf("a replayed reopen sent again: %d user messages", n)
	}

	// Another account's automation cannot reopen the owner's job.
	if _, err := reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: "other-account", JobID: jobID, Message: text, RequestID: "jdel_other.fix"}); err == nil {
		t.Fatal("another account reopened the job")
	}
}

// Requirement: a job whose run or follow-up is still in flight is answered
// "busy" (an error whose Busy reports true, naming the state), not refused,
// so the reopen step waits for it; nothing reaches its session meanwhile.
// Threat (forge run): the step treated a busy job as a refusal, the
// delivery failed and the findings were never sent.
// Owner: jobAutomationReopener.Reopen over the real worker run records and
// session routes.
func TestReopenAnswersBusyForARunningJob(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	before := len(f.userMessages(t, f.session.ID))
	// A worker run that is admitted or running is the job in_progress.
	for i, status := range []string{"running", "admitted"} {
		jobID := f.job(t, "run-reopen-busy-"+status, status, f.session.ID)
		_, err := f.server.JobAutomationReopener().Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: "fix it",
			RequestID: "jdel_busy.fix" + string(rune('a'+i))})
		var busy interface{ Busy() bool }
		if !errors.As(err, &busy) || !busy.Busy() || !strings.Contains(err.Error(), "in_progress") {
			t.Fatalf("%s job: err = %v, want busy naming the state", status, err)
		}
		var refused *jobautomation.RefusedError
		if errors.As(err, &refused) {
			t.Fatalf("%s job was refused, not answered busy: %v", status, err)
		}
	}
	if n := len(f.userMessages(t, f.session.ID)); n != before {
		t.Fatalf("a running job got a follow-up: %d user messages", n)
	}
}

// Requirement: a cancelled job is never reopened: the step fails with the
// reason and no message reaches its session.
// Owner: jobAutomationReopener.Reopen.
func TestReopenRefusesACancelledJob(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	jobID := f.job(t, "run-reopen-2", "cancelled", f.session.ID)
	before := len(f.userMessages(t, f.session.ID))
	_, err := f.server.JobAutomationReopener().Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: "fix it", RequestID: "jdel_cancelled.fix"})
	var refused *jobautomation.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.userMessages(t, f.session.ID)); n != before {
		t.Fatalf("a cancelled job got a follow-up: %d user messages", n)
	}
}
