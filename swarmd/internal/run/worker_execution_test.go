package run

import (
	"context"
	"errors"
	"fmt"
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
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("later-checkpoint-%t", later), func(t *testing.T) {
			testWorkerStopAcknowledgement(t, later)
		})
	}
}

func testWorkerStopAcknowledgement(t *testing.T, later bool) {
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
	if err = ss.Store().CreateSession(store.SessionSnapshot{ID: r.SessionID, AccountScopeID: "account", UserID: "owner", Mode: "auto", Metadata: map[string]any{"worker_id": w.ID, "worker_execution_run_id": r.ID}}); err != nil {
		t.Fatal(err)
	}
	executionID := r.ID
	if later {
		executionID += "-checkpoint-2"
	}
	intent := store.V3SessionRunIntent{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", RunID: executionID, PlanID: r.ID, Status: sessions.RunIntentPendingExecutor}
	_, err = ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: "start", IdempotencyKey: "start", PayloadHash: "start", RequestHash: "start", EventType: "session.run_intent.recorded", RunIntent: &intent, NowUnixMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.beginSessionLifecycle(r.SessionID, executionID, "test"); err != nil {
		t.Fatal(err)
	}
	cancelled := false
	runs.attachLifecycleCancel(r.SessionID, executionID, func() { cancelled = true })
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
	if err != nil || receipt.Status != "admitted" || !receipt.CancelRequested {
		t.Fatalf("premature terminal receipt: %+v %v", receipt, err)
	}
	if _, _, err = runs.finishSessionLifecycle(r.SessionID, executionID, context.Canceled); err != nil {
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
func setupWorkerExecutionFixture(t *testing.T, enqueue func(identity.Principal, store.V3SessionRunIntent) bool) (*Service, *sessions.Service, *WorkerExecutionService, string) {
	t.Helper()
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
	proj := &store.ProjectRecord{
		ID:        "proj_fixture",
		AccountID: "account",
		Name:      "Fixture Project",
		Workspaces: []store.ProjectWorkspaceRef{
			{WorkspaceID: entry.WorkspaceID, Path: repo},
		},
	}
	if err := repository.PutProject("account", proj); err != nil {
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
	runs.model = models
	runs.workspace = workspaces
	runs.agents = agents
	runs.agentModelSettings = agentmodelsettings.NewService(settings)
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		return SessionDeployCanonicalization{SourceWorkspaceID: entry.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), workspaces, nil)
	execution, err := NewWorkerExecutionService(runs, repository, trees, ss.ApplySessionMutation, enqueue)
	if err != nil {
		t.Fatal(err)
	}
	runs.SetWorkerExecutionService(execution)
	return runs, ss, execution, entry.WorkspaceID
}

func TestWorkerDispatchCreatesV3IntentAndRetriesWake(t *testing.T) {
	wake := false
	calls := 0
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		calls++
		if principal.AccountScopeID != "account" || intent.PlanID == "" || intent.CheckpointID != "cp-1" {
			t.Fatalf("wrong intent: %+v", intent)
		}
		return wake
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := ss.Store().WorkerStore().CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "dispatch", Instructions: "Review only", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
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
	if !snapshot.WorktreeEnabled || snapshot.WorktreeRootPath == snapshot.WorkspacePath || snapshot.Metadata["worker_id"] != w.ID {
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

// Requirement: a user-approved idle test uses real preparation without opening
// schedules; interrupted session preparation is retryable and stoppable. These
// tests inject only the mutation failure, not execution or provider telemetry.
func TestWorkerIdleTestAndPreparationRecovery(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "idle test", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.ConfigureBindings("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID}, false)
	if err != nil || w.LifecycleState != store.WorkerLifecycleStateIdle {
		t.Fatalf("idle binding: %+v %v", w, err)
	}
	req := store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "test_run", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "idle-test"}
	apply := execution.host.apply
	execution.host.apply = func(in sessions.SessionMutationInput) (sessions.SessionMutationResult, error) {
		if in.Kind != sessions.SessionMutationCreateSession {
			return sessions.SessionMutationResult{}, errors.New("injected preparation failure")
		}
		return apply(in)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err == nil {
		t.Fatal("preparation failure hidden")
	}
	if _, found, err := ss.GetSession(r.SessionID); err != nil || !found {
		t.Fatalf("missing partial session: %v", err)
	}
	execution.host.apply = apply
	if err = execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || recovered.Status != "running" {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	current, _, err := ws.GetWorker("account", w.ID)
	if err != nil || current.LifecycleState != store.WorkerLifecycleStateIdle || current.Revision != w.Revision {
		t.Fatalf("test enabled worker: %+v %v", current, err)
	}
	req.RequestSource, req.IdempotencyKey = "direct", "idle-direct"
	if _, err = execution.Dispatch(context.Background(), "account", "owner", req); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("idle direct admitted: %v", err)
	}
	cancelled, err := execution.CancelRun(context.Background(), "account", w.ID, r.ID)
	if err != nil || cancelled.Status != "cancelled" || !cancelled.CancelRequested {
		t.Fatalf("cancel idle test: %+v %v", cancelled, err)
	}
	snapshot, _, _ := ss.GetSession(r.SessionID)
	if err = execution.host.runs.validateWorkerExecution(snapshot); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("cancelled run regained execution: %v", err)
	}
}

// Purpose: Stop/ReconcileStop must safely settle partial preparation without
// stranding stopping state, while rejecting a stale revision after Dispatch
// initializes the worker model. The real store/execution fixture is the narrowest
// layer proving cancellation and no subsequent intent or provider execution.
func TestWorkerStopDuringPreparation(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "preparation", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	apply := execution.host.apply
	execution.host.apply = func(in sessions.SessionMutationInput) (sessions.SessionMutationResult, error) {
		if in.Kind != sessions.SessionMutationCreateSession {
			return sessions.SessionMutationResult{}, errors.New("injected preparation failure")
		}
		return apply(in)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "prepare-stop"})
	if err == nil {
		t.Fatal("preparation failure hidden")
	}
	execution.host.apply = apply
	// Dispatch may commit initial model policy; the old UI revision must fail CAS.
	current, found, err := ws.GetWorker("account", w.ID)
	if err != nil || !found {
		t.Fatalf("worker after preparation: %v %v", found, err)
	}
	if current.Revision != w.Revision {
		if _, err := execution.Stop(context.Background(), "account", "owner", w.ID, w.Revision, store.WorkerLifecycleStatePaused); !errors.Is(err, store.ErrWorkerConflict) {
			t.Fatalf("stale stop accepted: %v", err)
		}
		unchanged, _, err := ws.GetWorker("account", w.ID)
		if err != nil || unchanged.Revision != current.Revision || unchanged.LifecycleState != current.LifecycleState {
			t.Fatalf("stale stop mutated worker: %+v %v", unchanged, err)
		}
	}
	w, err = execution.Stop(context.Background(), "account", "owner", w.ID, current.Revision, store.WorkerLifecycleStatePaused)
	if err != nil || w.LifecycleState != store.WorkerLifecycleStatePaused {
		t.Fatalf("stop preparation: %+v %v", w, err)
	}
	receipt, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || receipt.Status != "cancelled" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if err = execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ss.GetSessionRunIntent(r.SessionID, r.ID); err != nil || found {
		t.Fatalf("stopped preparation executed: %v %v", found, err)
	}
}

// Requirement: interval/cron admission deduplicates each slot, preserves the
// attached plan, and skips offline slots. TickWorker plus real V3 preparation
// is the narrowest boundary; no provider is invoked in this scheduler test.
func TestWorkerScheduleDeduplicationAndRestartFence(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	plan := store.SessionPlanDocument{Title: "Scheduled review", Info: store.SessionPlanInfo{Goal: "Review"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Review", Tasks: []string{"Review"}, AcceptanceCriteria: []string{"Reviewed"}, Status: "pending"}}}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "scheduler", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []store.WorkerAutomationDefinition{{Name: "interval", ActivationMode: "interval", Enabled: true, Schedule: &store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, PlanDocument: plan}, {Name: "cron", ActivationMode: "cron", Enabled: true, Schedule: &store.AutomationV2Schedule{Kind: "cron", Cron: "* * * * *", Timezone: "UTC"}, PlanDocument: plan}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	after := time.UnixMilli(w.UpdatedAt).Add(time.Second)
	execution.MarkWorkerSweep(after)
	now := after.Add(time.Minute)
	for i := 0; i < 2; i++ {
		if err = execution.TickWorker(context.Background(), "account", w.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	receipts, _, err := ws.ListWorkerRuns("account", w.ID, 10, "")
	if err != nil || len(receipts) != 2 {
		t.Fatalf("slot deduplication: %d %v", len(receipts), err)
	}
	for _, r := range receipts {
		if r.RequestSource != "schedule" || r.AutomationRevision != 1 || r.Status != "running" {
			t.Fatalf("wrong scheduled receipt: %+v", r)
		}
		accepted, found, err := ss.GetActivePlan(r.SessionID)
		if err != nil || !found || accepted.Document.Info.Goal != "Review" {
			t.Fatalf("lost plan: %+v %v", accepted, err)
		}
	}
	restarted := &WorkerExecutionService{host: execution.host}
	if err = restarted.TickWorker(context.Background(), "account", w.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	receipts, _, err = ws.ListWorkerRuns("account", w.ID, 10, "")
	if err != nil || len(receipts) != 2 {
		t.Fatalf("restart replayed slots: %d %v", len(receipts), err)
	}
}

// Requirement: a worker completes only after every attached checkpoint, including
// a later executor run, has completed. A first-turn completion is not a worker
// success. Canonical plan mutations plus V3 intents prove the receipt mapping.
func TestWorkerMultiCheckpointCompletion(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	doc := store.SessionPlanDocument{Title: "Two steps", Info: store.SessionPlanInfo{Goal: "Review then report"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Review", Tasks: []string{"Review"}, AcceptanceCriteria: []string{"Reviewed"}, Status: "pending"}, {ID: "cp-2", Order: 2, Title: "Report", Tasks: []string{"Report"}, AcceptanceCriteria: []string{"Reported"}, Status: "pending"}}}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "two steps", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []store.WorkerAutomationDefinition{{Name: "review", ActivationMode: "manual", Enabled: true, PlanDocument: doc}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, RequestSource: "test_run", Input: map[string]any{"prompt": "Extra input must not replace the plan"}, IdempotencyKey: "multi"})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := ss.GetActivePlan(r.SessionID)
	if err != nil || len(plan.Document.Checkpoints) != 2 {
		t.Fatalf("attached plan replaced: %+v %v", plan, err)
	}
	if _, err = runs.executePlanManageToolWithMutation(r.SessionID, fmt.Sprintf(`{"action":"complete_checkpoint","checkpoint_id":"cp-1","run_id":%q,"attempt_id":%q,"run_session_id":%q,"parent_session_id":%q,"report":"Reviewed","result":"done"}`, r.ID, plan.Document.Checkpoints[0].AttemptID, r.SessionID, r.SessionID), "", ss.ApplySessionMutation); err != nil {
		t.Fatal(err)
	}
	first, _, err := ss.GetSessionRunIntent(r.SessionID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	first.Status = sessions.RunIntentCompleted
	recordIntent := func(intent store.V3SessionRunIntent, key string) {
		t.Helper()
		_, err := ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	recordIntent(first, "first-completed")
	if err = execution.observeRun(r); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("premature success: %v", err)
	}
	nextID := r.ID + "-next"
	lifecycle := sessions.NewPlanLifecycleService(ss)
	lifecycle.SetApplySessionMutation(ss.ApplySessionMutation)
	next, err := lifecycle.StartCheckpoint(sessions.PlanLifecycleExecutionInput{SessionID: r.SessionID, PlanID: r.ID, CheckpointID: "cp-2", RunID: nextID, RunSessionID: r.SessionID, ParentSessionID: r.SessionID, StartedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	second := store.V3SessionRunIntent{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", PlanID: r.ID, CheckpointID: "cp-2", AttemptID: next.AttemptID, RunID: nextID, Status: sessions.RunIntentPendingExecutor}
	recordIntent(second, "second-pending")
	if _, err = runs.executePlanManageToolWithMutation(r.SessionID, fmt.Sprintf(`{"action":"complete_checkpoint","checkpoint_id":"cp-2","run_id":%q,"attempt_id":%q,"run_session_id":%q,"parent_session_id":%q,"report":"Reported","result":"done"}`, nextID, next.AttemptID, r.SessionID, r.SessionID), "", ss.ApplySessionMutation); err != nil {
		t.Fatal(err)
	}
	second.Status = sessions.RunIntentCompleted
	recordIntent(second, "second-completed")
	if err = execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || receipt.Status != "succeeded" || receipt.CompletedAt == 0 {
		t.Fatalf("completion: %+v %v", receipt, err)
	}
}

// Requirement: a worker run starts whether the source workspace is on a branch
// or on a detached HEAD (mid-rebase, bisect, CI-style checkout). On a branch the
// run forks from it so it can be integrated; on a detached HEAD it forks from
// the HEAD commit as before. Threat: forking only in current-branch mode made
// every run on a detached HEAD fail to start. startPlan owns the base choice;
// the real worktree service on a real repository is the narrowest proof.
func TestWorkerRunStartsFromDetachedHead(t *testing.T) {
	_, ss, execution, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	canonical, err := execution.host.runs.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", canonical.SourceWorkspacePath, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach: %v %s", err, out)
	}
	ws := ss.Store().WorkerStore()
	doc := store.SessionPlanDocument{Title: "One step", Info: store.SessionPlanInfo{Goal: "Review"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Review", Tasks: []string{"Review"}, AcceptanceCriteria: []string{"Reviewed"}, Status: "pending"}}}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "detached", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []store.WorkerAutomationDefinition{{Name: "review", ActivationMode: "manual", Enabled: true, PlanDocument: doc}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": canonical.SourceWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, RequestSource: "test_run", IdempotencyKey: "detached"})
	if err != nil {
		t.Fatalf("run on a detached HEAD did not start: %v", err)
	}
	snapshot, ok, err := ss.GetSession(r.SessionID)
	if err != nil || !ok || snapshot.WorktreeRootPath == "" {
		t.Fatalf("run has no worktree lane: %+v %v", snapshot, err)
	}
}
