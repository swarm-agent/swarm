package jobautomation

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/providerlimit"
)

// Requirement: a workflow fan-out (for_each) item whose worker job ends on
// the provider's session limit is not failed: it waits for the reset the
// provider states ("waiting for the provider's limit to reset (until …)"),
// the item that depends on it keeps waiting instead of failing, nothing
// starts before the reset (also after a restart), and after the reset a new
// job starts for the item (a new idempotency key). A failure that is not a
// provider limit, Swarm's own artifact quota among them, or a worker run that
// already gave up waiting ("worker job failed: the provider's limit was still
// in effect after 12 waits …"), still fails the item and its dependent.
// Threat: a limited item failed (workflow.item.failed) and failed every item
// waiting on it through itemDependency, leaving the plan stuck.
// Owners: TriggerRunner.tickItems (limitItem, itemPending, itemDependency)
// over the real Pebble delivery store; Start/Running/ItemOutcome are the
// worker boundary, scripted here.
func TestForEachItemWaitsForProviderLimitAndReruns(t *testing.T) {
	limit := "worker job failed: claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	for _, tc := range []struct {
		name, failure string
		limited       bool
	}{
		{"session limit", limit, true},
		{"swarm quota", "worker job failed: artifact v3 git: quota exceeded", false},
		// The worker run already gave up after its own bounded waits: the
		// item fails, it does not wait (and start up to 12 more waits).
		{"worker run gave up", "worker job failed: " + providerlimit.Exhausted(12, "You've hit your session limit · resets 4pm (UTC)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			d := Delivery{ID: "plan", Account: testAccount, AutomationID: "planner", Folder: folder, State: StateSucceeded,
				Definition: []Step{{ID: "make", Worker: "maker", ForEach: &ForEach{MaxParallel: 1, AfterField: "reuses"}}},
				Steps: []StepRecord{{ID: "make", Items: []ItemRecord{
					{Index: 0, ID: "wide", State: "started", JobID: "worker/maker/first"},
					{Index: 1, ID: "cut", After: "wide", State: "waiting for wide"},
				}}},
			}
			if err := h.store.SaveDelivery(d); err != nil {
				t.Fatal(err)
			}
			clock := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
			var keys, signals []string
			runner := TriggerRunner{Store: h.store, Account: func() string { return testAccount }, Now: func() time.Time { return clock },
				Start: func(_ context.Context, _ Automation, _ string, key string) (string, error) {
					keys = append(keys, key)
					return "worker/maker/rerun", nil
				},
				Running: func(_ context.Context, _ Automation, job string) (bool, error) {
					return job == "worker/maker/rerun", nil
				},
				ItemOutcome: func(_ context.Context, _ Automation, job string) (string, error) {
					if job == "worker/maker/first" {
						return tc.failure, nil
					}
					return "", nil
				},
				Signal: func(_ Automation, kind, _ string) { signals = append(signals, kind) },
			}
			items := func() []ItemRecord {
				t.Helper()
				got, err := h.store.GetDelivery(testAccount, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				return got.Steps[0].Items
			}
			runner.Tick(context.Background())
			got := items()
			if !tc.limited {
				if got[0].State != "failed" || got[0].Error != tc.failure || got[1].State != "failed" || len(keys) != 0 {
					t.Fatalf("a non-limit failure = %+v, keys %v", got, keys)
				}
				return
			}
			wantUntil := time.Date(2026, 10, 27, 16, 1, 0, 0, time.UTC)
			if got[0].State != itemLimitWaiting || got[0].RetryAt != wantUntil.UnixMilli() || got[0].LimitWaits != 1 ||
				!strings.HasPrefix(got[0].Error, "waiting for the provider's limit to reset (until 2026-10-27T16:01:00Z)") {
				t.Fatalf("a provider limit did not leave the item waiting: %+v", got[0])
			}
			if got[1].State != "waiting for wide" || got[1].Error != "" {
				t.Fatalf("the dependent of a waiting item failed: %+v", got[1])
			}
			if len(signals) != 1 || signals[0] != "workflow.item.waiting" || len(keys) != 0 {
				t.Fatalf("signals %v, starts %v", signals, keys)
			}

			// Before the reset nothing starts, also after a restart.
			h.restart()
			runner.Store = h.store
			clock = time.Date(2026, 10, 27, 15, 59, 0, 0, time.UTC)
			runner.Tick(context.Background())
			if got := items(); len(keys) != 0 || got[0].State != itemLimitWaiting || got[1].State != "waiting for wide" {
				t.Fatalf("started before the reset: %v %+v", keys, got)
			}

			// After the reset a new job starts for the item.
			clock = time.Date(2026, 10, 27, 16, 2, 0, 0, time.UTC)
			runner.Tick(context.Background())
			got = items()
			if len(keys) != 1 || !strings.HasSuffix(keys[0], ".limit1") || got[0].State != "started" || got[0].JobID != "worker/maker/rerun" || got[0].Error != "" {
				t.Fatalf("the item did not rerun after the reset: keys %v item %+v", keys, got[0])
			}
			if got[1].State != "waiting for wide" {
				t.Fatalf("dependent = %+v", got[1])
			}
		})
	}
}
