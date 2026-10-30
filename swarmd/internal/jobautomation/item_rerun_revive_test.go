package jobautomation

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// Purpose: the owner's rerun (Store.Rerun for POST
// /v3/job-deliveries/{id}/rerun, Store.RerunNewest for swarm_deliver_job
// automation=true) revives a finished plan delivery's for_each items even
// when no step failed or stopped, reports them, and returns nothing_to_rerun
// only when there was also nothing to revive. A failed item whose job's
// newest end succeeded and was delivered (itemJobReady, the readiness
// itemDependency uses) returns to done; the item that failed waiting on it
// waits again and the item queue starts it exactly once. The item job's
// receipt has the shape written before successful runs dropped an earlier
// run's error: status succeeded with the old "blocked:" text. That stale
// text must not keep the item failed.
// Threat: the live regression where such a rerun returned
// {"rerun":[],"unchanged":[{"reason":"nothing_to_rerun"}]} and revived
// nothing, leaving the plan's dependents unstarted; the opposite harms: an
// item revived while its job's workflow did not deliver, a dependent started
// twice, a repeated rerun reporting work again, or another account's rerun
// touching the delivery.
// Owners: Store.Rerun/RerunNewest/rerunLocked, reviveItemsLocked,
// TriggerRunner.itemJobReady/itemDependency/tickItems over the real Pebble
// store. Only the worker start/running adapters are stubbed; the store is
// the narrowest layer that holds receipts, deliveries and the item queue.
func TestOwnerRerunRevivesItemsWithoutAFailedStep(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	plan, _, err := h.store.CreateDelivery(Automation{ID: "planner", Account: testAccount, Name: "planner",
		Steps: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{MaxParallel: 3, AfterField: "reuses"}}}}, Result{JobID: "parent", Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	// recap's job was blocked, then a follow-up succeeded before the stale
	// error fix: its receipt still carries the old blocker. Its own workflow
	// delivered (merge succeeded).
	recap := workerResult(folder, "recap")
	recap.Error = "blocked: Narration can't be generated"
	// late's job succeeded too, but its workflow's merge failed: not ready.
	late := workerResult(folder, "late")
	for _, end := range []struct {
		result *Result
		merge  string
	}{{recap, StateSucceeded}, {late, StateFailed}} {
		if err := h.store.db.PutJSON(itemEndKey(testAccount, end.result.JobID), end.result); err != nil {
			t.Fatal(err)
		}
		own, _, err := h.store.CreateDelivery(Automation{ID: "content-post", Account: testAccount, Name: "content-post",
			Steps: []Step{{ID: "merge", Type: StepDeliver, To: "merge"}}}, *end.result)
		if err != nil {
			t.Fatal(err)
		}
		own.State, own.Steps[0].State = end.merge, end.merge
		if err := h.store.SaveDelivery(own); err != nil {
			t.Fatal(err)
		}
	}
	plan.State, plan.FinishedAt, plan.Steps[0].State = StateSucceeded, 1, StateSucceeded
	plan.Steps[0].Items = []ItemRecord{
		{Index: 0, ID: "window-recap", State: "failed", JobID: recap.JobID, Error: "worker job failed: blocked: Narration can't be generated"},
		{Index: 1, ID: "cut", After: "window-recap", State: "failed", Error: "waits for window-recap, which did not finish: worker job failed: blocked: Narration can't be generated"},
		{Index: 2, ID: "late", State: "failed", JobID: late.JobID, Error: "worker job failed: blocked"},
		{Index: 3, ID: "late-cut", After: "late", State: "failed", Error: "waits for late, which did not finish: worker job failed: blocked"},
	}
	if err := h.store.SaveDelivery(plan); err != nil {
		t.Fatal(err)
	}
	stored := func() Delivery {
		t.Helper()
		d, err := h.store.GetDelivery(testAccount, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	states := func(d Delivery, want ...string) {
		t.Helper()
		for i, state := range want {
			if got := d.Steps[0].Items[i]; got.State != state {
				t.Fatalf("item %d = %+v, want %s (all %+v)", i, got, state, d.Steps[0].Items)
			}
		}
	}

	// Another account's rerun of the delivery finds nothing and changes nothing.
	if _, err := h.store.Rerun("other-account", plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account rerun = %v", err)
	}
	states(stored(), "failed", "failed", "failed", "failed")

	out, err := h.store.Rerun(testAccount, plan.ID)
	if err != nil || !out.Rerun || out.Reason != "" {
		t.Fatalf("rerun = %+v %v", out, err)
	}
	wantRevived := []RevivedItem{
		{Step: "make", Index: 0, ID: "window-recap", JobID: recap.JobID, State: "done"},
		{Step: "make", Index: 1, ID: "cut", State: "waiting for window-recap"},
	}
	if !reflect.DeepEqual(out.Revived, wantRevived) {
		t.Fatalf("revived = %+v, want %+v", out.Revived, wantRevived)
	}
	d := stored()
	if d.State != StateRunning || d.ItemsReopened != StateSucceeded || d.Steps[0].State != StateSucceeded || d.Steps[0].Attempts != 0 || d.Reruns != 1 {
		t.Fatalf("rerun delivery = %s reopened %q step %+v reruns %d", d.State, d.ItemsReopened, d.Steps[0], d.Reruns)
	}
	states(d, "done", "waiting for window-recap", "failed", "failed")
	if out.Delivery.State != d.State || !reflect.DeepEqual(out.Delivery.Steps[0].Items, d.Steps[0].Items) {
		t.Fatalf("rerun returned %+v, stored %+v", out.Delivery.Steps[0].Items, d.Steps[0].Items)
	}

	// The item queue starts cut once, with its deterministic key; late-cut
	// stays failed because late's workflow did not deliver.
	starts := map[string]int{}
	tr := TriggerRunner{Store: h.store, Account: func() string { return testAccount },
		Start: func(_ context.Context, _ Automation, _, key string) (string, error) {
			starts[key]++
			return "worker/maker/" + key[:12], nil
		},
		Running: func(context.Context, Automation, string) (bool, error) { return true, nil },
	}
	tr.Tick(context.Background())
	tr.Tick(context.Background())
	d = stored()
	states(d, "done", "started", "failed", "failed")
	if len(starts) != 1 || d.Steps[0].Items[1].JobID == "" {
		t.Fatalf("starts = %v items %+v", starts, d.Steps[0].Items)
	}
	for key, n := range starts {
		if n != 1 {
			t.Fatalf("item key %s started %d times", key, n)
		}
	}

	// Rerunning again finds no step and no item: nothing_to_rerun, unchanged.
	again, err := h.store.RerunNewest(testAccount, "parent", "")
	if err != nil || len(again) != 1 || again[0].Rerun || again[0].Reason != ReasonNothingToRerun || len(again[0].Revived) != 0 {
		t.Fatalf("second rerun = %+v %v", again, err)
	}
	if after := stored(); after.Reruns != 1 || !reflect.DeepEqual(after.Steps[0].Items, d.Steps[0].Items) {
		t.Fatalf("second rerun changed the delivery: %+v", after)
	}
}

// Purpose: RerunNewest, swarm_deliver_job automation=true, applies the same
// revival to the job's newest delivery and reports it through its outcome
// even though the delivery has no failed step; when every revived item is
// done (no dependent), the delivery stays finished.
// Threat: the MCP rerun reporting nothing_to_rerun after reviving items.
// Owners: Store.RerunNewest/rerunLocked/reviveItemsLocked.
func TestRerunNewestReportsRevivedItems(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	plan, _, err := h.store.CreateDelivery(Automation{ID: "planner", Account: testAccount, Name: "planner",
		Steps: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{MaxParallel: 1}}}}, Result{JobID: "parent", Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	solo := workerResult(folder, "solo")
	if err := h.store.db.PutJSON(itemEndKey(testAccount, solo.JobID), solo); err != nil {
		t.Fatal(err)
	}
	plan.State, plan.FinishedAt, plan.Steps[0].State = StateSucceeded, 1, StateSucceeded
	plan.Steps[0].Items = []ItemRecord{{Index: 0, State: "failed", JobID: solo.JobID, Error: "worker job failed"}}
	if err := h.store.SaveDelivery(plan); err != nil {
		t.Fatal(err)
	}
	out, err := h.store.RerunNewest(testAccount, "parent", "")
	if err != nil || len(out) != 1 || !out[0].Rerun || out[0].Reason != "" {
		t.Fatalf("rerun = %+v %v", out, err)
	}
	if want := []RevivedItem{{Step: "make", Index: 0, JobID: solo.JobID, State: "done"}}; !reflect.DeepEqual(out[0].Revived, want) {
		t.Fatalf("revived = %+v", out[0].Revived)
	}
	if d := out[0].Delivery; d.State != StateSucceeded || d.ItemsReopened != "" || d.Steps[0].Items[0].State != "done" {
		t.Fatalf("delivery = %s reopened %q items %+v", d.State, d.ItemsReopened, d.Steps[0].Items)
	}
}
