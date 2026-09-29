package run

import (
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Next-schedule summary uses the same next-due calculation as
// TickWorker; only active workers with enabled interval/cron definitions have
// eligible slots. Threat: showing stale slots for paused/disabled workers or
// restarting from wall-clock instead of the interval's persisted anchor.
// Boundary: NextWorkerScheduledAt and TickWorker; pure scheduling layer suffices.
func TestNextWorkerScheduledAtEligibility(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	w := store.WorkerRecord{LifecycleState: store.WorkerLifecycleStateActive, Automations: []store.WorkerAutomationDefinition{
		{ActivationMode: "interval", Enabled: true, CreatedAt: now.Add(-20*time.Second).UnixMilli(), Schedule: &store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}},
		{ActivationMode: "interval", Enabled: false, CreatedAt: now.Add(-59*time.Second).UnixMilli(), Schedule: &store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}},
	}}
	got, err := NextWorkerScheduledAt(w, now)
	if err != nil || got != now.Add(40*time.Second).UnixMilli() {
		t.Fatalf("eligible interval: %d %v", got, err)
	}
	for _, state := range []store.WorkerLifecycleState{store.WorkerLifecycleStatePaused, store.WorkerLifecycleStateIdle, store.WorkerLifecycleStateArchived, store.WorkerLifecycleStateStopping, store.WorkerLifecycleStateDeleted} {
		w.LifecycleState = state
		got, err = NextWorkerScheduledAt(w, now)
		if err != nil || got != 0 {
			t.Fatalf("%s should not schedule: %d %v", state, got, err)
		}
	}
	w.LifecycleState = store.WorkerLifecycleStateActive
	w.Automations[0].Enabled = false
	got, err = NextWorkerScheduledAt(w, now)
	if err != nil || got != 0 {
		t.Fatalf("disabled automations should not schedule: %d %v", got, err)
	}
}
