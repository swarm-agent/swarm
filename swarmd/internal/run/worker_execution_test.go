package run

import (
	"context"
	"errors"
	"os/exec"
	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
	"testing"
	"time"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: worker stop must retain stopping until the provider turn exits.
// Threat: StopSessionRun historically publishes cancelled before its goroutine
// returns. The shared worker service must not mistake a cancellation request for
// acknowledgement. Real session mutations and lifecycle state are the narrowest
// deterministic boundary; this is not a provider-backed execution benchmark.
func TestWorkerStopWaitsForExecutionAcknowledgement(t *testing.T) {
	runs, ss, _ := setupWorkerOrchestratorTestEnv(t)
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: runs, apply: ss.ApplySessionMutation}}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "stop", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.AdmitWorkerRun("account", store.WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", Input: map[string]any{"prompt": "review"}, IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if err = ss.Store().CreateSession(store.SessionSnapshot{ID: r.SessionID, AccountScopeID: "account", UserID: "owner", Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	intent := store.V3SessionRunIntent{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", RunID: r.ID, PlanID: r.ID, Status: sessions.RunIntentPendingExecutor}
	_, err = ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: "start", IdempotencyKey: "start", PayloadHash: "start", RequestHash: "start", EventType: "session.run_intent.recorded", RunIntent: &intent, NowUnixMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.beginSessionLifecycle(r.SessionID, r.ID, "test"); err != nil {
		t.Fatal(err)
	}
	cancelled := false
	runs.attachLifecycleCancel(r.SessionID, r.ID, func() { cancelled = true })
	stopped, err := execution.Stop(context.Background(), "account", "owner", w.ID, w.Revision, store.WorkerLifecycleStateArchived)
	if !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("stop before acknowledgement: %v", err)
	}
	_ = stopped
	current, _, err := ws.GetWorker("account", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled || current.LifecycleState != store.WorkerLifecycleStateStopping || current.StopTarget != store.WorkerLifecycleStateArchived {
		t.Fatalf("lost durable stop barrier: %+v cancelled=%v", current, cancelled)
	}
	receipt, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || receipt.Status != "admitted" {
		t.Fatalf("premature terminal receipt: %+v %v", receipt, err)
	}
	if _, _, err = runs.finishSessionLifecycle(r.SessionID, r.ID, context.Canceled); err != nil {
		t.Fatal(err)
	}
	final, err := execution.ReconcileStop("account", "owner", w.ID, current.Revision, store.WorkerLifecycleStateArchived)
	if err != nil || final.LifecycleState != store.WorkerLifecycleStateArchived {
		t.Fatalf("acknowledged stop: %+v %v", final, err)
	}
	receipt, _, err = ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || receipt.Status != "cancelled" {
		t.Fatalf("missing cancellation receipt: %+v %v", receipt, err)
	}
}

// Requirement: stopping undispatched durable work cancels it without creating
// an executor, and closed admission survives retries. WorkerExecutionService.Stop
// and WorkerStore.AdmitWorkerRun jointly own this boundary.
func TestWorkerStopCancelsUndispatchedReceipt(t *testing.T) {
	runs, ss, _ := setupWorkerOrchestratorTestEnv(t)
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: runs, apply: ss.ApplySessionMutation}}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "queue", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	req := store.WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", Input: map[string]any{"prompt": "review"}, IdempotencyKey: "queued"}
	r, err := ws.AdmitWorkerRun("account", req)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Stop(context.Background(), "account", "owner", w.ID, w.Revision, store.WorkerLifecycleStatePaused)
	if err != nil || w.LifecycleState != store.WorkerLifecycleStatePaused {
		t.Fatalf("stop: %+v %v", w, err)
	}
	replay, err := ws.AdmitWorkerRun("account", req)
	if err != nil || replay.ID != r.ID || replay.Status != "cancelled" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, found, err := ss.GetSession(r.SessionID); err != nil || found {
		t.Fatalf("stop created executor: %v %v", found, err)
	}
}

// Requirement: dispatch creates an isolated V3 execution session and canonical
// plan/intent, not just a worker receipt. Threat: wake failure loses accepted
// work or a retry duplicates execution. Real Git/Pebble and a bounded enqueue
// callback exercise the production preparation and retry boundary without LLMs.
func TestWorkerDispatchCreatesV3IntentAndRetriesWake(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		if out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	runs, ss, repository := setupWorkerOrchestratorTestEnv(t)
	db := repository.Underlying()
	if err := repository.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	entry, err := workspaces.AddForPrincipal(p, repo, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	agents := agent.NewService(store.NewAgentStore(db), nil)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), nil, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, catalog, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatalf("catalog: %v", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: "account", Swarm: store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	runs.workspace = workspaces
	runs.agents = agents
	runs.agentModelSettings = agentmodelsettings.NewService(settings)
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		return SessionDeployCanonicalization{SourceWorkspaceID: entry.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), workspaces, nil)
	wake := false
	calls := 0
	execution, err := NewWorkerExecutionService(runs, repository, trees, ss.ApplySessionMutation, func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		calls++
		if principal.AccountScopeID != "account" || intent.PlanID == "" || intent.CheckpointID != "cp-1" {
			t.Fatalf("wrong intent: %+v", intent)
		}
		return wake
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := ss.Store().WorkerStore().CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "dispatch", Instructions: "Review only", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": entry.WorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	req := store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Inspect the repository"}, IdempotencyKey: "dispatch-once"}
	r, err := execution.Dispatch(ctx, "account", "owner", req)
	if err == nil {
		t.Fatal("wake failure hidden")
	}
	snapshot, found, e := ss.GetSession(r.SessionID)
	if e != nil || !found {
		t.Fatalf("dispatch did not prepare V3 session: %v; dispatch=%v", e, err)
	}
	if !snapshot.WorktreeEnabled || snapshot.WorktreeRootPath == repo || snapshot.Metadata["worker_id"] != w.ID {
		t.Fatalf("wrong isolation/linkage: %+v", snapshot)
	}
	intent, found, e := ss.GetSessionRunIntent(r.SessionID, r.ID)
	if e != nil || !found || intent.Status != sessions.RunIntentPendingExecutor {
		t.Fatalf("missing retained intent: %+v %v; dispatch=%v", intent, e, err)
	}
	wake = true
	again, err := execution.Dispatch(ctx, "account", "owner", req)
	if err != nil || again.ID != r.ID || again.Status != "running" {
		t.Fatalf("retry failed: %+v %v", again, err)
	}
	if _, err = execution.Dispatch(ctx, "account", "owner", req); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("duplicate wake: %d", calls)
	}
}
