package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose:
//   - Requirement: a caller correcting a running worker job through
//     swarm_message_job is accepted while the job's run is active (its first
//     run included): the message is queued for that run and the answer says
//     so (sent.delivery, sent.queued_for_run_id). It is refused only while an
//     earlier follow-up is queued to start, and the refusal names that
//     follow-up's run. A job stopped with swarm_stop_run takes a message that
//     continues it in its own session (the same branch and worktree); after
//     swarm_stop_job it takes none.
//   - Threat/regression: swarm_message_job refused a job on its first run with
//     "a follow-up is already running in this job" although none was sent,
//     and a stopped job answered "the job was cancelled".
//   - Boundary/authority: controlMCPMessageJob / controlMCPStopJob over the
//     daemon's own routes (internalRouteHandler): POST /v3/sessions/{id}/
//     messages -> acceptSessionsV3Message -> acceptSessionsV3QueuedNote /
//     workerJobQueuedNoteRunID -> WorkerExecutionService.AdmitQueuedMessage,
//     or prepareWorkerJobFollowup -> PrepareFollowup; POST /v3/workers/{id}/
//     runs/{run}/cancel -> CancelRun. Real Pebble store, real active Coder
//     worker (deployed through swarm_deploy_worker) and its owner's
//     membership; the model provider is never reached.
//   - Narrowest layer: the defect sat between the MCP tool, the session
//     message route and the worker gate, so only the routed MCP call proves
//     what a caller sees. The receipt settlement after a stop is proven in the
//     run package (worker_job_stop_test.go); here the stopped receipt is
//     recorded as that settlement writes it.

type jobCorrectionFixture struct {
	server   *Server
	sessions *sessionruntime.Service
	call     *controlMCPCall
	workerID string
}

func newJobCorrectionFixture(t *testing.T) *jobCorrectionFixture {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db := sessions.Store().Underlying()
	principal := testPrincipal()
	ids := pebblestore.NewIdentityStore(db)
	if _, err := ids.PutUser(principal.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(principal.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(principal.AccountUser); err != nil {
		t.Fatal(err)
	}
	// The catalog holds the models the seeded agent-model settings name, so
	// a follow-up's role model resolves.
	catalog := pebblestore.NewModelCatalogStore(db)
	for _, model := range []string{"action", "plan", "compact", "finder", "coder", "designer", "router"} {
		if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: "codex", Model: model, ThinkingOptions: []string{"high"}}); err != nil {
			t.Fatal(err)
		}
	}
	runner := server.runner.(*runruntime.Service)
	execution, err := runruntime.NewWorkerExecutionService(runner, sessions.Store(), &worktree.Service{}, sessions.ApplySessionMutation,
		func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "sessions:write", "automations:read", "automations:write"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	if _, err := controlMCPDeployWorker(call, map[string]any{"preset": "coder"}); err != nil {
		t.Fatal(err)
	}
	workers, err := sessions.Store().WorkerStore().ListWorkers(principal.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10})
	if err != nil || len(workers.Workers) != 1 || workers.Workers[0].LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("coder worker = %+v %v", workers, err)
	}
	return &jobCorrectionFixture{server: server, sessions: sessions, call: call, workerID: workers.Workers[0].ID}
}

// job records a worker job in its own worker-execution session, as admission
// does, with the receipt in the given state.
func (f *jobCorrectionFixture) job(t *testing.T, runID string, mutate func(*pebblestore.WorkerRunRecord)) (string, string) {
	t.Helper()
	p := testPrincipal()
	pref := pebblestore.ModelPreference{Provider: "codex", Model: "coder", Thinking: "high"}
	session, _, err := f.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "worker-execution-" + runID, WorkspacePath: t.TempDir(), UserID: p.UserID, AccountScopeID: p.AccountScopeID, Preference: &pref,
		Metadata: map[string]any{"worker_id": f.workerID, "worker_execution_run_id": runID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := pebblestore.WorkerRunRecord{ID: runID, WorkerID: f.workerID, UserID: p.UserID, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID,
		Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Add a rate limit to the upload route."}, CreatedAt: 1, StartedAt: 1}
	if mutate != nil {
		mutate(&run)
	}
	if _, err := f.sessions.Store().WorkerStore().RecordWorkerRun(p.AccountScopeID, run); err != nil {
		t.Fatal(err)
	}
	return controlMCPJobID(controlMCPJobWorker, f.workerID, runID), session.ID
}

// intent records a run intent state in the job's session, as the executor
// and the message route do.
func (f *jobCorrectionFixture) intent(t *testing.T, sessionID, runID, status string) {
	t.Helper()
	p := testPrincipal()
	key := "intent-" + runID + "-" + status
	if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, NowUnixMs: time.Now().UnixMilli(),
		RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, RunID: runID, Status: status, RunSessionID: sessionID}}); err != nil {
		t.Fatal(err)
	}
}

func (f *jobCorrectionFixture) userMessages(t *testing.T, sessionID string) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := f.sessions.ListMessages(sessionID, 0, 200)
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

func controlMCPRefusal(err error) string {
	var refused controlMCPToolError
	if errors.As(err, &refused) {
		return refused.message
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestMessageJobOnItsFirstActiveRunIsQueuedForThatRun(t *testing.T) {
	f := newJobCorrectionFixture(t)
	f.server.v3SessionExecutor = newSessionV3Executor(f.server)
	for _, tc := range []struct {
		name    string
		started bool // the executor runs the job's own run (else it is recorded, not started yet)
	}{{"running", true}, {"recorded", false}} {
		t.Run(tc.name, func(t *testing.T) {
			runID := "run-first-" + tc.name
			jobID, sessionID := f.job(t, runID, nil)
			f.intent(t, sessionID, runID, sessionruntime.RunIntentPendingExecutor)
			if tc.started {
				f.intent(t, sessionID, runID, sessionruntime.RunIntentRunning)
				f.server.v3SessionExecutor.mu.Lock()
				f.server.v3SessionExecutor.openQueuedNotesLocked(sessionV3ExecutorJob{SessionID: sessionID, RunID: runID, queuedNotes: runruntime.NewQueuedNoteTracker(0)})
				f.server.v3SessionExecutor.mu.Unlock()
			}

			out, err := controlMCPMessageJob(f.call, map[string]any{"job_id": jobID, "message": "also cap uploads at 10 MB", "client_request_id": "fix-" + tc.name})
			if err != nil {
				t.Fatalf("message to a job on its first run was refused: %v", controlMCPRefusal(err))
			}
			sent := controlMCPMap(controlMCPMap(out)["sent"])
			delivery, _ := sent["delivery"].(string)
			if sent["accepted"] != true || sent["queued"] != true || sent["queued_for_run_id"] != runID || !strings.HasPrefix(delivery, "queued:") || !strings.Contains(delivery, runID) {
				t.Fatalf("sent = %v, want queued for %s with its delivery", sent, runID)
			}
			// The message is stored once, as a note for the job's own run, and
			// starts no run of its own: the job's run stays the active one.
			messages := f.userMessages(t, sessionID)
			if len(messages) != 1 || messages[0].Content != "also cap uploads at 10 MB" || !runruntime.IsQueuedNote(messages[0]) || messages[0].Metadata[runruntime.QueuedNoteRunMetadataKey] != runID {
				t.Fatalf("session messages = %+v", messages)
			}
			if active, ok, err := f.sessions.GetSessionActiveRunIntent(sessionID); err != nil || !ok || active.RunID != runID {
				t.Fatalf("active run = %+v %v %v, want the job's own run", active, ok, err)
			}
		})
	}
}

func TestMessageJobRefusesOnlyARealQueuedFollowupAndNamesIt(t *testing.T) {
	f := newJobCorrectionFixture(t)
	// The job finished its own run; a follow-up was sent and waits for worker
	// capacity: its intent is recorded, the executor has not started it.
	jobID, sessionID := f.job(t, "run-settled", func(r *pebblestore.WorkerRunRecord) {
		r.Status, r.CompletedAt = "succeeded", 2
	})
	f.intent(t, sessionID, "run-settled", sessionruntime.RunIntentPendingExecutor)
	f.intent(t, sessionID, "run-settled", sessionruntime.RunIntentCompleted)
	f.intent(t, sessionID, "followup-queued", sessionruntime.RunIntentPendingExecutor)

	_, err := controlMCPMessageJob(f.call, map[string]any{"job_id": jobID, "message": "and add a test", "client_request_id": "second"})
	message := controlMCPRefusal(err)
	if err == nil || !strings.Contains(message, "the follow-up was not sent") || !strings.Contains(message, controlMCPFollowupBusy) || !strings.Contains(message, "followup-queued") {
		t.Fatalf("refusal = %q, want it to name the queued follow-up", message)
	}
	if got := f.userMessages(t, sessionID); len(got) != 0 {
		t.Fatalf("a refused message reached the session: %+v", got)
	}
}

func TestStoppedJobContinuesOnItsSessionAndStopJobClosesIt(t *testing.T) {
	f := newJobCorrectionFixture(t)
	// The receipt as observeRun settles a job whose own run swarm_stop_run
	// cancelled (WorkerExecutionService.observeRun, run package).
	jobID, sessionID := f.job(t, "run-stopped", func(r *pebblestore.WorkerRunRecord) {
		r.Status, r.CompletedAt = "failed", 2
		r.Error = pebblestore.WorkerRunStoppedPrefix + "the caller stopped the run; the job stays open on its branch: a message continues it"
	})
	f.intent(t, sessionID, "run-stopped", sessionruntime.RunIntentPendingExecutor)
	f.intent(t, sessionID, "run-stopped", sessionruntime.RunIntentCancelled)

	state, _, err := f.call.jobState(controlMCPJobWorker, f.workerID, "run-stopped")
	if err != nil || state["status"] != "stopped" {
		t.Fatalf("stopped job state = %v %v", state["status"], err)
	}
	out, err := controlMCPMessageJob(f.call, map[string]any{"job_id": jobID, "message": "use the shared limiter instead", "client_request_id": "continue"})
	if err != nil {
		t.Fatalf("message to a stopped job was refused: %v", controlMCPRefusal(err))
	}
	sent := controlMCPMap(controlMCPMap(out)["sent"])
	followup, _ := sent["run_id"].(string)
	if followup == "" || sent["queued"] == true || !strings.HasPrefix(sent["delivery"].(string), "started:") {
		t.Fatalf("sent = %v, want a started follow-up", sent)
	}
	// The follow-up runs in the job's own session (its branch and worktree),
	// on the same receipt: no new job.
	intent, ok, err := f.sessions.GetSessionRunIntent(sessionID, followup)
	if err != nil || !ok || intent.SessionID != sessionID {
		t.Fatalf("follow-up intent = %+v %v %v", intent, ok, err)
	}
	runs, _, err := f.sessions.Store().WorkerStore().ListWorkerRuns(testPrincipal().AccountScopeID, f.workerID, 10, "")
	if err != nil || len(runs) != 1 || runs[0].ID != "run-stopped" {
		t.Fatalf("worker jobs = %+v %v (a message must not start a new job)", runs, err)
	}

	// swarm_stop_job still cancels: on a stopped job it closes it for good.
	stoppedJob, sessionTwo := f.job(t, "run-stopped-2", func(r *pebblestore.WorkerRunRecord) {
		r.Status, r.CompletedAt = "failed", 2
		r.Error = pebblestore.WorkerRunStoppedPrefix + "the caller stopped the run"
	})
	f.intent(t, sessionTwo, "run-stopped-2", sessionruntime.RunIntentPendingExecutor)
	f.intent(t, sessionTwo, "run-stopped-2", sessionruntime.RunIntentCancelled)
	stop, err := controlMCPStopJob(f.call, map[string]any{"job_id": stoppedJob})
	if err != nil || controlMCPMap(stop)["status"] != "cancelled" {
		t.Fatalf("stop_job on a stopped job = %v %v", stop, controlMCPRefusal(err))
	}
	if state, _, err := f.call.jobState(controlMCPJobWorker, f.workerID, "run-stopped-2"); err != nil || state["status"] != "cancelled" {
		t.Fatalf("cancelled job state = %v %v", state["status"], err)
	}
	_, err = controlMCPMessageJob(f.call, map[string]any{"job_id": stoppedJob, "message": "continue", "client_request_id": "after-cancel"})
	if message := controlMCPRefusal(err); err == nil || !strings.Contains(message, "the job was cancelled") {
		t.Fatalf("message after stop_job = %q, want refused as cancelled", message)
	}
	if got := f.userMessages(t, sessionTwo); len(got) != 0 {
		t.Fatalf("a cancelled job took a message: %+v", got)
	}
}
