package jobautomation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: queueItems' graph validation must reject unknown/ambiguous/cyclic
// references before dispatch. This pure validation layer tests the exact helper
// used before the queue's first durable write, including configurable field names.
func TestForEachDependencyValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []map[string]any
		want  string
	}{
		{"unknown", []map[string]any{{"id": "cut", "reuses": "missing"}}, "unknown dependency"},
		{"duplicate", []map[string]any{{"id": "a"}, {"id": "a"}}, "duplicate id"},
		{"cycle", []map[string]any{{"id": "a", "reuses": "b"}, {"id": "b", "reuses": "a"}}, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matrixDependencies(tc.items, ForEach{AfterField: "reuses"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
	got, err := matrixDependencies([]map[string]any{{"key": "wide"}, {"key": "cut", "reuses": "wide"}, {}}, ForEach{IDField: "key", AfterField: "reuses"})
	if err != nil || got[1].After != "wide" || got[0].ID != "wide" || got[2].After != "" {
		t.Fatal(got, err)
	}
}

// Purpose: TriggerRunner.tickItems must not dispatch dependent work before
// Runner.consume's receipt AND every delivery's merge have succeeded. The real
// Pebble queue and runner boundaries prove restart persistence, fail-closed
// missing receipts, failure propagation, and waiting items not taking slots;
// recording dispatch is a deterministic service test, not a live-agent benchmark.
func TestForEachDependencyDispatch(t *testing.T) {
	for _, outcome := range []string{"success", "job failed", "delivery blocked", "merge skipped"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			h.process()
			a := h.automation(Automation{Name: "merge-maker", Enabled: true, Scope: Scope{Folder: folder, Worker: "coder-1"}, Steps: []Step{{Type: StepDeliver, To: "merge"}}})
			d := Delivery{ID: "plan", Account: testAccount, AutomationID: "planner", Folder: folder, State: StateSucceeded,
				Definition: []Step{{ID: "make", Worker: "maker", ForEach: &ForEach{MaxParallel: 1, AfterField: "reuses"}}},
				Steps: []StepRecord{{ID: "make", Items: []ItemRecord{
					{Index: 0, ID: "wide", State: "started", JobID: "worker/coder-1/wide"},
					{Index: 1, ID: "cut", After: "wide", State: "waiting for wide"},
					{Index: 2, ID: "other", State: "queued"},
				}}},
			}
			if err := h.store.SaveDelivery(d); err != nil {
				t.Fatal(err)
			}
			starts := 0
			running := true
			runner := TriggerRunner{Store: h.store, Account: func() string { return testAccount },
				Start: func(context.Context, Automation, string, string) (string, error) {
					starts++
					return "worker/maker/new", nil
				},
				Running: func(_ context.Context, _ Automation, job string) (bool, error) {
					return job == "worker/coder-1/wide" && running, nil
				},
			}
			runner.Tick(context.Background())
			if starts != 0 {
				t.Fatal("exceeded parallelism")
			}
			running = false
			runner.Tick(context.Background())
			if starts != 1 {
				t.Fatal("waiting item consumed slot", starts)
			}
			check := func(want string) ItemRecord {
				t.Helper()
				got, err := h.store.GetDelivery(testAccount, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				item := got.Steps[0].Items[1]
				if item.State != want {
					t.Fatalf("dependent = %+v; want %s", item, want)
				}
				if want != "started" && item.JobID != "" {
					t.Fatal("dependent started early", item)
				}
				return item
			}
			check("waiting for wide")
			result := workerResult(folder, "wide")
			result.RequestSource = "workflow/planner/delivery/plan/step/make"
			delivery, _, err := h.store.CreateDelivery(a, *result)
			if err != nil {
				t.Fatal(err)
			}
			// Even an already visible delivery cannot bypass the consumption receipt.
			delivery.State = StateSucceeded
			delivery.Steps[0].State = StateSucceeded
			if err := h.store.SaveDelivery(delivery); err != nil {
				t.Fatal(err)
			}
			runner.Tick(context.Background())
			check("waiting for wide")
			delivery.State = StateRunning
			if err := h.store.SaveDelivery(delivery); err != nil {
				t.Fatal(err)
			}
			h.results.results["wide"] = result
			h.jobEnd("wide", "", "succeeded", result.RequestSource)
			if err := h.runner.consume(context.Background(), testAccount, pebblestore.Signal{Kind: "worker.run.succeeded", At: a.CreatedAt + 1, Refs: map[string]string{"worker_id": "coder-1", "run_id": "wide"}}); err != nil {
				t.Fatal(err)
			}
			runner.Tick(context.Background())
			check("waiting for wide")
			// A second delivery must also finish; success of the first is insufficient.
			a2 := a
			a2.ID, a2.Name = "second-workflow", "second-merge"
			second, _, err := h.store.CreateDelivery(a2, *result)
			if err != nil {
				t.Fatal(err)
			}
			delivery.State = StateSucceeded
			if err := h.store.SaveDelivery(delivery); err != nil {
				t.Fatal(err)
			}
			runner.Tick(context.Background())
			check("waiting for wide")
			second.State, second.Steps[0].State = StateSucceeded, StateSucceeded
			if err := h.store.SaveDelivery(second); err != nil {
				t.Fatal(err)
			}
			h.restart()
			runner.Store = h.store
			switch outcome {
			case "success":
				delivery.State = StateSucceeded
			case "job failed":
				result.Status, result.Error = StatusFailed, "blocked: source unavailable"
				if err := h.store.db.PutJSON(itemEndKey(testAccount, result.JobID), result); err != nil {
					t.Fatal(err)
				}
			case "delivery blocked":
				delivery.State, delivery.BlockedReason = StateBlocked, "review blocked"
			case "merge skipped":
				delivery.State, delivery.Steps[0].State = StateSucceeded, StateSkipped
			}
			if err := h.store.SaveDelivery(delivery); err != nil {
				t.Fatal(err)
			}
			runner.Tick(context.Background())
			if outcome == "success" {
				check("started")
				if starts != 2 {
					t.Fatal(starts)
				}
			} else {
				item := check("failed")
				if !strings.HasPrefix(item.Error, "waits for wide, which did not finish: ") || starts != 1 {
					t.Fatal(item, starts)
				}
			}
		})
	}
}

// Purpose: Runner.queueItems must reject an unknown dependency from the actual
// committed plan without writing any partial delivery or pending-item index.
// A local Git fixture and real store exercise the queue-time admission boundary.
func TestForEachUnknownDependencySavesNothing(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	if err := os.WriteFile(filepath.Join(folder, "plan.json"), []byte(`[{"id":"wide"},{"id":"cut","reuses":"missing"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git("add", "plan.json")
	d := Delivery{ID: "unknown", Account: testAccount, Folder: folder, Head: repo.commit("plan"), Steps: []StepRecord{{ID: "make"}}}
	if err := h.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	before, _, _ := h.store.db.GetBytes(deliveryKey(testAccount, d.ID))
	failure := h.runner.queueItems(context.Background(), &d, &d.Steps[0], Step{ForEach: &ForEach{File: "plan.json", AfterField: "reuses"}})
	if !failure.refused || !strings.Contains(failure.reason, `unknown dependency id "missing"`) || d.Steps[0].Items != nil {
		t.Fatal(failure, d.Steps)
	}
	after, _, err := h.store.db.GetBytes(deliveryKey(testAccount, d.ID))
	if err != nil || string(before) != string(after) {
		t.Fatal("partial delivery", err)
	}
	if _, found, err := h.store.db.GetBytes(keyPendingItemsPrefix + hexKey(testAccount) + "/" + d.ID); err != nil || found {
		t.Fatal("partial queue", err)
	}
}

// Purpose: Runner.consume must durably distinguish no matching workflows from
// an unprocessed job end. The real consumer/store boundary proves zero deliveries
// permits a successful item only after its receipt, scoped to its account.
func TestForEachDependencyNoWorkflows(t *testing.T) {
	h := newHarness(t)
	result := workerResult(t.TempDir(), "wide")
	result.RequestSource = "workflow/planner/delivery/plan/step/make"
	h.results.results["wide"] = result
	r := TriggerRunner{Store: h.store}
	items := []ItemRecord{{ID: "wide", State: "done", JobID: result.JobID}}
	if ready, _ := r.itemDependency(testAccount, items, "wide"); ready {
		t.Fatal("missing receipt accepted")
	}
	sig := pebblestore.Signal{Kind: "worker.run.succeeded", Refs: map[string]string{"worker_id": "coder-1", "run_id": "wide"}, Attrs: map[string]string{"source": result.RequestSource}}
	if err := h.runner.consume(context.Background(), testAccount, sig); err != nil {
		t.Fatal(err)
	}
	if ready, reason := r.itemDependency(testAccount, items, "wide"); !ready || reason != "" {
		t.Fatal(ready, reason)
	}
	if ready, _ := r.itemDependency("other-account", items, "wide"); ready {
		t.Fatal("cross-account receipt accepted")
	}
}
