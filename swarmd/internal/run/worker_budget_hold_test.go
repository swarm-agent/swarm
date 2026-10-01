package run

import (
	"context"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: exhausted workers must not dispatch schedule slots or restart queued
// work; reconciliation must cancel the durable admitted receipt without changing
// manual lifecycle. TickWorker/ReconcileWorker and real WorkerStore admission
// own the boundary; the existing hermetic executor fixture is the narrowest
// integration layer (no provider calls or live workers).
func TestWorkerBudgetHoldSkipsScheduleAndCancelsQueued(t *testing.T) {
	runs, ss, _ := setupWorkerOrchestratorTestEnv(t)
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: runs, apply: ss.ApplySessionMutation}}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "budget", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil { t.Fatal(err) }
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil { t.Fatal(err) }
	r, err := ws.AdmitWorkerRun("account", store.WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", Input: map[string]any{"prompt": "review"}, IdempotencyKey: "queued"})
	if err != nil { t.Fatal(err) }
	if err := ss.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: "account", Enabled: true, DailyTokensLimit: 1}); err != nil { t.Fatal(err) }
	if err := ss.Store().PutDailyUsageAccumulator(store.DailyUsageAccumulator{AccountScopeID: "account", Date: time.Now().UTC().Format("2006-01-02"), TotalTokens: 1}); err != nil { t.Fatal(err) }
	execution.MarkWorkerSweep(time.Now().Add(-time.Minute))
	for i := 0; i < 3; i++ {
		if err := execution.TickWorker(context.Background(), "account", w.ID, time.Now()); err != nil { t.Fatalf("held tick: %v", err) }
	}
	if err := execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil { t.Fatal(err) }
	got, found, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || !found || !got.CancelRequested { t.Fatalf("queued hold did not cancel: %+v %v", got, err) }
	current, _, err := ws.GetWorker("account", w.ID)
	if err != nil || current.LifecycleState != store.WorkerLifecycleStateActive || current.Revision != w.Revision { t.Fatalf("hold changed lifecycle: %+v %v", current, err) }
	if _, err := ws.AdmitWorkerRun("account", store.WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", Input: map[string]any{"prompt": "review"}, IdempotencyKey: "new"}); !errors.Is(err, store.ErrWorkerBudget) { t.Fatalf("new admission: %v", err) }
}
