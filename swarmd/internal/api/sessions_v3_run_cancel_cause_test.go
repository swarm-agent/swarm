package api

// Purpose:
// - Requirement: a V3 run (the path a worker job follow-up runs on: a message
//   posted to the job's session, EnqueueRun, sessionV3Executor.run) is
//   cancelled only by its own stop (CancelRun), its run timeout or the
//   executor's context ending (daemon shutdown), and whatever ends it says by
//   what. A failed run's durable reason names that cause; the fixed text
//   "executor context canceled" remains only when no cause was recorded.
// - Threat/regression: a worker follow-up failed at once with "executor
//   context canceled" and nothing showed what cancelled it. The classifier
//   (sessionV3RuntimeErrorReason) flattened every context.Canceled to that
//   text, and the run context (context.WithCancel in run, attachCancel,
//   CancelRun) carried no cause, so a stop, a shutdown and a cancellation that
//   never touched the run's context all read the same. A stop aimed at one
//   session must also never end another session's run.
// - Boundary/authority: sessionV3Executor.run, attachCancel, CancelRun,
//   providerAssistantResponse (stream context, run timeout, progress sink),
//   the terminal classifier and the durable V3SessionRunIntent reason written
//   through applySessionV3PrimaryMutation.
// - Narrowest test layer: the real server, executor, Pebble store and
//   /v3/sessions/{id}/messages route with only the model provider stubbed;
//   the cause is assigned inside the executor and only its durable run intent
//   and the provider's context show it.
// - Scope limit: the session is an ordinary V3 session, not a
//   worker-execution- session, so the worker follow-up gate
//   (prepareWorkerJobFollowup) and the sandbox follow-up reservation, which
//   decide whether a follow-up starts and cancel nothing, are not exercised.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const runCancelTestTimeout = 5 * time.Second

// blockingProvider is the only stub: each session's request blocks until the
// session is released (a normal answer) or the request's context ends (the
// context's error, as a real provider returns it). It keeps the cause the
// request's context reports.
type blockingProvider struct {
	mu       sync.Mutex
	entered  map[string]chan struct{}
	released map[string]chan struct{}
	causes   map[string]error
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{entered: map[string]chan struct{}{}, released: map[string]chan struct{}{}, causes: map[string]error{}}
}

func (p *blockingProvider) channels(sessionID string) (chan struct{}, chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entered[sessionID] == nil {
		p.entered[sessionID] = make(chan struct{})
		p.released[sessionID] = make(chan struct{})
	}
	return p.entered[sessionID], p.released[sessionID]
}

func (p *blockingProvider) handle(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	entered, released := p.channels(req.SessionID)
	p.mu.Lock()
	select {
	case <-entered:
	default:
		close(entered)
	}
	p.mu.Unlock()
	select {
	case <-released:
		return provideriface.Response{Text: "follow-up done", StopReason: "stop"}, nil
	case <-ctx.Done():
		p.mu.Lock()
		p.causes[req.SessionID] = context.Cause(ctx)
		p.mu.Unlock()
		return provideriface.Response{}, ctx.Err()
	}
}

func (p *blockingProvider) cause(sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.causes[sessionID]
}

// sendFollowup posts a message to the session, as swarm_message_job does, and
// returns its run ID once the provider holds the run.
func sendFollowup(t *testing.T, f *confirmationFixture, p *blockingProvider, sessionID, requestID string) string {
	t.Helper()
	entered, _ := p.channels(sessionID)
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": "follow-up"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	select {
	case <-entered:
	case <-time.After(runCancelTestTimeout):
		t.Fatal("the follow-up never reached the provider")
	}
	return stableSessionsV3PrimaryRunID(sessionID, requestID)
}

func runReason(t *testing.T, f *confirmationFixture, sessionID, runID, status string) string {
	t.Helper()
	f.waitRun(sessionID, runID, status)
	intent, ok, err := f.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok {
		t.Fatalf("run intent: ok=%v err=%v", ok, err)
	}
	return intent.BlockedReason
}

func requireNamesCause(t *testing.T, text string, want ...string) {
	t.Helper()
	if text == "executor context canceled" {
		t.Fatalf("the run's reason is still the fixed %q", text)
	}
	for _, part := range want {
		if !strings.Contains(text, part) {
			t.Fatalf("%q does not name its cause (want %q)", text, part)
		}
	}
}

func newRunCancelFixture(t *testing.T, name string) (*confirmationFixture, *blockingProvider) {
	t.Helper()
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), name), nil)
	t.Cleanup(f.close)
	p := newBlockingProvider()
	f.runner.handler = p.handle
	return f, p
}

func TestV3FollowupStoppedOnPurposeNamesTheStop(t *testing.T) {
	f, p := newRunCancelFixture(t, "stop")
	sessionID := f.createSession("coder-job")
	runID := sendFollowup(t, f, p, sessionID, "followup-1")

	const reason = "review loop ended the job"
	if _, cancelled, err := f.server.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sessionID, RunID: runID}, reason); err != nil || !cancelled {
		t.Fatalf("CancelRun: cancelled=%v err=%v", cancelled, err)
	}
	if got := runReason(t, f, sessionID, runID, sessionruntime.RunIntentCancelled); got != reason {
		t.Fatalf("cancelled run reason = %q, want %q", got, reason)
	}
	cause := p.cause(sessionID)
	if cause == nil {
		t.Fatal("the provider's context was not cancelled")
	}
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("stop cause %v no longer matches context.Canceled", cause)
	}
	requireNamesCause(t, cause.Error(), "a stop request", reason)
}

func TestV3FollowupDaemonShutdownNamesTheShutdown(t *testing.T) {
	f, p := newRunCancelFixture(t, "shutdown")
	executorCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	f.server.v3SessionExecutor.ctx = executorCtx
	sessionID := f.createSession("coder-job")
	runID := sendFollowup(t, f, p, sessionID, "followup-1")

	shutdown()
	requireNamesCause(t, runReason(t, f, sessionID, runID, sessionruntime.RunIntentFailed), "the daemon shutting down")
}

func TestV3FollowupRunTimeoutNamesTheTimeout(t *testing.T) {
	f, p := newRunCancelFixture(t, "timeout")
	if _, _, _, err := f.server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model",
		Enabled: pebblestore.BoolPtr(true), Prompt: "Take orders.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
			"place_order": {Enabled: pebblestore.BoolPtr(true)}, "lookup_order": {Enabled: pebblestore.BoolPtr(true)},
		}},
		Limits: &pebblestore.AgentRunLimits{RunTimeoutMS: 100},
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := f.createSession("coder-job")
	runID := sendFollowup(t, f, p, sessionID, "followup-1")
	requireNamesCause(t, runReason(t, f, sessionID, runID, sessionruntime.RunIntentFailed), "its run timeout", "100ms")
}

func TestV3FollowupCancelledOutsideItsRunContextSaysSo(t *testing.T) {
	// The incident's shape: the run fails with context.Canceled although
	// nothing stopped it, timed out or shut the daemon down. The reason must
	// say that the run's own context was not cancelled instead of the fixed
	// text.
	f, _ := newRunCancelFixture(t, "foreign")
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{}, context.Canceled
	}
	sessionID := f.createSession("coder-job")
	body, _ := json.Marshal(map[string]any{"client_request_id": "followup-1", "role": "user", "content": "follow-up"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	runID := stableSessionsV3PrimaryRunID(sessionID, "followup-1")
	requireNamesCause(t, runReason(t, f, sessionID, runID, sessionruntime.RunIntentFailed), "a context other than the run's own", "not cancelled")
}

func TestV3TerminalClassifierReportsARecordedCancelCause(t *testing.T) {
	// The classifier reports a recorded cause, also when wrapped (as an
	// overflow compaction wraps run.Service's error), and keeps the fixed
	// text only for a cancellation that recorded none.
	stop := fmt.Errorf("v3 context overflow compact failed: %w", &runruntime.RunCancelledError{By: runruntime.RunCancelledByCaller, Reason: "run cancelled by a stop request: person pressed stop"})
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&sessionV3RunCancelledError{By: sessionV3CancelledByStop, Reason: "person pressed stop", Err: context.Canceled}, "run cancelled by a stop request: person pressed stop"},
		{stop, stop.Error()},
		{context.Canceled, "executor context canceled"},
		{errors.New("provider returned 500"), "provider returned 500"},
	} {
		got := sessionV3TerminalClassifier.Classify(TerminalClassifierInput{Err: tc.err})
		if got.Status != sessionruntime.RunIntentFailed || got.Reason != tc.want {
			t.Fatalf("Classify(%v) = %+v, want failed %q", tc.err, got, tc.want)
		}
	}
}

func TestV3StopOfAnotherJobLeavesTheFollowupRunning(t *testing.T) {
	// Regression guard, not a reproduction: the executor already keys stops
	// by session and run, so this also passes on the base code.
	f, p := newRunCancelFixture(t, "isolation")
	coder := f.createSession("coder-job")
	reviewer := f.createSession("reviewer-job")
	reviewRun := sendFollowup(t, f, p, reviewer, "review-1")
	followup := sendFollowup(t, f, p, coder, "followup-1")

	if n := f.server.v3SessionExecutor.CancelRunsForSession(reviewer, "review loop ended the reviewer job"); n != 1 {
		t.Fatalf("CancelRunsForSession cancelled %d runs, want 1", n)
	}
	f.waitRun(reviewer, reviewRun, sessionruntime.RunIntentCancelled)
	if cause := p.cause(coder); cause != nil {
		t.Fatalf("stopping the reviewer cancelled the follow-up: %v", cause)
	}
	_, release := p.channels(coder)
	close(release)
	f.waitRun(coder, followup, sessionruntime.RunIntentCompleted)
}
