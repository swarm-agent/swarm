package run

// Purpose. Requirement: a turn that run.Service.runTurn executes is cancelled
// only by its own stop (StopSessionRun, worker job control) or by the context
// its caller started it with, and both the error runTurn returns and the
// durable lifecycle record it leaves say which one. Threat: the incident's
// "executor context canceled" could not be attributed because every
// cancellation was flattened (the lifecycle said only "run interrupted"), and a
// stop aimed at one job's session must never end another job's turn.
// Production owners: runTurn (newRunContext, registerPendingAdmission,
// attachLifecycleCancel, the deferred namedRunCancellation and
// finishSessionLifecycle/classifyLifecycleFinish), StopSessionRun and
// WorkerExecutionService.cancelRun. Layer: a real Pebble store, session
// service, model/agent services and run.Service with only the model provider
// stubbed is the narrowest layer where the returned error and the persisted
// lifecycle both come from the production turn path. Scope limit: this covers
// run.Service turns only; the V3 executor in swarmd/internal/api, where the
// incident's follow-up ran, has its own cancel registry and classifier that
// these tests do not exercise. They do not reproduce the incident. The
// cross-job isolation check also passes on the code before this change
// (stops were already keyed by session and run) and is kept only as a
// regression guard.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const runTurnTestTimeout = 10 * time.Second

// blockingRunner is the only stub: a model provider that blocks each session's
// request until that session is released (a normal answer) or its context
// ends (the context's error, as a real provider returns it).
type blockingRunner struct {
	mu       sync.Mutex
	entered  map[string]chan struct{}
	released map[string]chan struct{}
}

func newBlockingRunner(sessionIDs ...string) *blockingRunner {
	r := &blockingRunner{entered: map[string]chan struct{}{}, released: map[string]chan struct{}{}}
	for _, id := range sessionIDs {
		r.entered[id] = make(chan struct{})
		r.released[id] = make(chan struct{})
	}
	return r
}

func (r *blockingRunner) ID() string { return "codex" }

func (r *blockingRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *blockingRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	entered, released := r.entered[req.SessionID], r.released[req.SessionID]
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	r.mu.Unlock()
	if released == nil {
		return provideriface.Response{}, errors.New("unexpected session " + req.SessionID)
	}
	select {
	case <-released:
		return provideriface.Response{Text: "follow-up done", StopReason: "stop"}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}

type runTurnFixture struct {
	svc      *Service
	sessions *sessionruntime.Service
	runner   *blockingRunner
	ids      map[string]string // name -> session ID
}

func newRunTurnFixture(t *testing.T, names ...string) runTurnFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "run-turn.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := agents.EnsureDefaultsForAccount("account-a"); err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	ids := map[string]string{}
	sessionIDs := make([]string, 0, len(names))
	for _, name := range names {
		created, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			UserID: "user-a", AccountScopeID: "account-a", Title: name, WorkspacePath: t.TempDir(), WorkspaceName: "workspace",
			Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "low"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = created.ID
		sessionIDs = append(sessionIDs, created.ID)
	}
	catalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(db))
	models := model.NewService(pebblestore.NewModelStore(db), events, catalog)
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	runner := newBlockingRunner(sessionIDs...)
	providers := registry.New()
	providers.RegisterRunner(runner)
	svc := NewService(sessions, models, providers, tool.NewRuntime(1), nil, agents, nil, events)
	settings := pebblestore.NewAgentModelSettingsStore(db)
	configured := pebblestore.AgentModelAssignment{Provider: "codex", Model: "gpt-5.4", Thinking: "low"}
	if _, err := settings.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account-a",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents:   pebblestore.SystemAgentModelAssignments{Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured},
	}); err != nil {
		t.Fatal(err)
	}
	svc.SetAgentModelSettingsService(agentmodelsettings.NewService(settings))
	return runTurnFixture{svc: svc, sessions: sessions, runner: runner, ids: ids}
}

type runTurnOutcome struct {
	result RunResult
	err    error
}

// start runs a real turn and returns once the stub provider holds it.
func (f runTurnFixture) start(t *testing.T, ctx context.Context, name, runID string) <-chan runTurnOutcome {
	t.Helper()
	sessionID := f.ids[name]
	done := make(chan runTurnOutcome, 1)
	go func() {
		principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a", SessionID: sessionID}
		result, err := f.svc.RunTurnWithOptions(ctx, sessionID, RunOptions{Prompt: "follow-up for " + name, RunID: runID, Principal: principal})
		done <- runTurnOutcome{result: result, err: err}
	}()
	select {
	case <-f.runner.entered[sessionID]:
	case outcome := <-done:
		t.Fatalf("turn for %s ended before reaching the provider: %v", name, outcome.err)
	case <-time.After(runTurnTestTimeout):
		t.Fatalf("turn for %s never reached the provider", name)
	}
	return done
}

func waitRunTurn(t *testing.T, done <-chan runTurnOutcome) runTurnOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(runTurnTestTimeout):
		t.Fatal("turn did not end")
		return runTurnOutcome{}
	}
}

func requireRunCancelledBy(t *testing.T, err error, by, reason string) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runTurn error %v no longer matches context.Canceled", err)
	}
	var named *RunCancelledError
	if !errors.As(err, &named) {
		t.Fatalf("runTurn error %q does not name its cause", err)
	}
	if named.By != by || named.Reason != reason {
		t.Fatalf("cause = (%q, %q), want (%q, %q)", named.By, named.Reason, by, reason)
	}
}

func (f runTurnFixture) requireLifecycle(t *testing.T, name, phase string, want ...string) {
	t.Helper()
	lifecycle, ok, err := f.sessions.GetLifecycle(f.ids[name])
	if err != nil || !ok {
		t.Fatalf("lifecycle for %s: found=%v err=%v", name, ok, err)
	}
	if lifecycle.Active || lifecycle.Phase != phase {
		t.Fatalf("lifecycle for %s = active %v phase %q, want ended %q", name, lifecycle.Active, lifecycle.Phase, phase)
	}
	for _, part := range want {
		if !strings.Contains(lifecycle.StopReason, part) {
			t.Fatalf("persisted stop reason %q for %s does not say %q", lifecycle.StopReason, name, part)
		}
	}
}

func TestRunTurnStopRequestNamesCauseAndSparesAnotherJobsTurn(t *testing.T) {
	f := newRunTurnFixture(t, "coder", "reviewer")
	coder := f.start(t, context.Background(), "coder", "run-followup")
	reviewer := f.start(t, context.Background(), "reviewer", "run-review")

	const reason = "review loop ended the reviewer job"
	if err := f.svc.StopSessionRun(f.ids["reviewer"], "run-review", reason); err != nil {
		t.Fatal(err)
	}
	stopped := waitRunTurn(t, reviewer)
	requireRunCancelledBy(t, stopped.err, RunCancelledByStopRequest, reason)
	f.requireLifecycle(t, "reviewer", lifecyclePhaseCancelled, RunCancelledByStopRequest, reason)

	// The other job's turn is still held by its provider and finishes normally.
	select {
	case outcome := <-coder:
		t.Fatalf("stopping another job's turn ended this one: %v", outcome.err)
	default:
	}
	close(f.runner.released[f.ids["coder"]])
	followup := waitRunTurn(t, coder)
	if followup.err != nil {
		t.Fatalf("follow-up failed after another job's stop: %v", followup.err)
	}
	lifecycle, ok, err := f.sessions.GetLifecycle(f.ids["coder"])
	if err != nil || !ok || lifecycle.Phase != lifecyclePhaseCompleted || lifecycle.StopReason != "" {
		t.Fatalf("follow-up lifecycle = %+v found=%v err=%v", lifecycle, ok, err)
	}
}

func TestRunTurnCallerCancellationIsNamedInErrorAndLifecycle(t *testing.T) {
	f := newRunTurnFixture(t, "coder")
	caller, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := f.start(t, caller, "coder", "run-followup")
	shutdown := errors.New("daemon shutting down")
	cancel(shutdown)
	outcome := waitRunTurn(t, done)
	requireRunCancelledBy(t, outcome.err, RunCancelledByCaller, shutdown.Error())
	f.requireLifecycle(t, "coder", lifecyclePhaseInterrupted, RunCancelledByCaller, shutdown.Error())
}

func TestRunTurnWorkerJobControlNamesCauseInErrorAndLifecycle(t *testing.T) {
	f := newRunTurnFixture(t, "coder")
	sessionID := f.ids["coder"]
	ws := f.sessions.Store().WorkerStore()
	worker, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: "wrun-coder", WorkerID: worker.ID, UserID: "user-a", Role: pebblestore.WorkerRoleCoder, SessionID: sessionID, Status: "running", RequestSource: "direct", CreatedAt: 1, StartedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	key := "run-turn-cancel-intent"
	if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: "user-a", AccountScopeID: "account-a", Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.recorded",
		RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, UserID: "user-a", AccountScopeID: "account-a", RunID: "run-followup", Status: sessionruntime.RunIntentPendingExecutor, RunSessionID: sessionID, ParentSessionID: sessionID}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	done := f.start(t, context.Background(), "coder", "run-followup")
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: f.svc, apply: f.sessions.ApplySessionMutation}}
	if err := execution.cancelRun(record, "worker stopped"); err != nil {
		t.Fatal(err)
	}
	outcome := waitRunTurn(t, done)
	requireRunCancelledBy(t, outcome.err, RunCancelledByWorkerControl, "worker stopped")
	f.requireLifecycle(t, "coder", lifecyclePhaseCancelled, RunCancelledByWorkerControl, "worker stopped")
}
