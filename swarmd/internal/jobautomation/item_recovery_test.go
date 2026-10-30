package jobautomation

import (
	"context"
	"strings"
	"testing"
)

// Purpose: a for_each item follows its job's newest end. When an item job
// that ended blocked later succeeds through a follow-up and its own workflow
// delivers, Runner.consume (the item-end receipt) must call
// Store.ReviveItems so the item returns to done and the items that failed
// only waiting on it (along the after chain) wait again; TriggerRunner's
// item queue then starts each exactly once with its deterministic key, even
// though the plan delivery already finished. Items whose job is still
// blocked, and their dependents, stay failed; a later unrelated end starts
// nothing again.
// Threat: the live regression where a day's plan output was never started
// because failed items never followed their jobs' later success, and the
// opposite harms: a still-failed job releasing its dependents, or a
// dependent started twice.
// Owners: Runner.consume, Store.ReviveItems/reviveItemsLocked,
// TriggerRunner.tickItems/itemDependency over the real Pebble store and
// signal feed. Only the worker start/running/outcome adapters and the merge
// deliverer are stubbed; this is the narrowest layer holding the item-end
// receipt, the job's own delivery and the item queue together.
func TestForEachItemFollowsItsJobsFollowupSuccess(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	deliverer := &recordingDeliverer{}
	h.runner.deliver = deliverer
	h.process()
	h.automation(Automation{Name: "content-post", Enabled: true, Scope: Scope{Folder: folder, Worker: "coder-1"}, Steps: []Step{{Type: StepDeliver, To: "merge"}}})
	plan := Delivery{ID: "plan", Account: testAccount, AutomationID: "planner", AutomationName: "planner", Folder: folder, State: StateSucceeded, FinishedAt: 1,
		Definition: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{MaxParallel: 3, AfterField: "reuses"}}},
		Steps: []StepRecord{{ID: "make", Type: StepAgent, State: StateSucceeded, Items: []ItemRecord{
			{Index: 0, ID: "main", State: "started", JobID: "worker/coder-1/main"},
			{Index: 1, ID: "cut", After: "main", State: "waiting for main"},
			{Index: 2, ID: "short", After: "cut", State: "waiting for cut"},
			{Index: 3, ID: "own", State: "started", JobID: "worker/coder-1/own"},
			{Index: 4, ID: "own-cut", After: "own", State: "waiting for own"},
		}}},
	}
	if err := h.store.SaveDelivery(plan); err != nil {
		t.Fatal(err)
	}
	source := "workflow/planner/delivery/plan/step/make"
	starts := map[string]int{}
	tr := TriggerRunner{Store: h.store, Account: func() string { return testAccount },
		Start: func(_ context.Context, _ Automation, _, key string) (string, error) {
			starts[key]++
			return "worker/maker/" + key[:12], nil
		},
		Running: func(_ context.Context, _ Automation, job string) (bool, error) {
			return strings.HasPrefix(job, "worker/maker/"), nil // started dependents keep running
		},
		ItemOutcome: func(_ context.Context, _ Automation, job string) (string, error) {
			return "worker job failed: blocked: speech budget exceeded", nil
		},
	}
	items := func() []ItemRecord {
		t.Helper()
		d, err := h.store.GetDelivery(testAccount, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.State != StateSucceeded {
			t.Fatalf("plan delivery state = %s", d.State)
		}
		return d.Steps[0].Items
	}
	want := func(states ...string) []ItemRecord {
		t.Helper()
		got := items()
		for i, state := range states {
			if got[i].State != state {
				t.Fatalf("item %d = %+v, want %s (all %+v)", i, got[i], state, got)
			}
		}
		return got
	}
	total := func() int {
		n := 0
		for _, c := range starts {
			n += c
		}
		return n
	}
	end := func(run, followup string, ok bool) {
		t.Helper()
		result := workerResult(folder, run)
		result.RequestSource = source
		result.Deliverables = []Deliverable{{Name: "branch", Kind: DeliverableBranch}}
		status := "succeeded"
		if !ok {
			result.Status, result.JobStatus, result.Error, status = StatusFailed, "blocked", "blocked: speech budget exceeded", "failed"
		} else if followup != "" {
			result.Git.Head = strings.Repeat("c", 40)
		}
		h.results.results[run] = result
		h.jobEnd(run, followup, status, source)
		h.process()
	}

	// Morning: both item jobs end blocked; every dependent fails with them.
	end("main", "", false)
	end("own", "", false)
	tr.Tick(context.Background())
	got := want("failed", "failed", "failed", "failed", "failed")
	if !strings.HasPrefix(got[1].Error, "waits for main, which did not finish: ") || !strings.HasPrefix(got[2].Error, "waits for cut, which did not finish: ") || total() != 0 {
		t.Fatalf("morning: %+v starts=%v", got, starts)
	}

	// A follow-up on main succeeds and its content-post delivery merges: main
	// is done and its chain waits again, though the plan delivery finished.
	end("main", "followup-1", true)
	if len(deliverer.delivered) != 1 {
		t.Fatalf("content-post merges = %d", len(deliverer.delivered))
	}
	want("done", "waiting for main", "waiting for cut", "failed", "failed")
	tr.Tick(context.Background())
	got = want("done", "started", "waiting for cut", "failed", "failed")
	if total() != 1 || got[1].JobID == "" || got[1].Error != "" {
		t.Fatalf("recovery: %+v starts=%v", got, starts)
	}

	// The still-blocked job keeps its dependent failed, even after another
	// blocked follow-up; a later unrelated end of main starts nothing again.
	end("own", "followup-1", false)
	end("main", "followup-2", true)
	tr.Tick(context.Background())
	tr.Tick(context.Background())
	got = want("done", "started", "waiting for cut", "failed", "failed")
	if total() != 1 || !strings.HasPrefix(got[4].Error, "waits for own, which did not finish: ") {
		t.Fatalf("after unrelated ends: %+v starts=%v", got, starts)
	}
	for key, n := range starts {
		if n != 1 {
			t.Fatalf("item key %s started %d times", key, n)
		}
	}

	// Another account's end of a job with the same id revives nothing here.
	if err := h.store.ReviveItems("other-account", source, "worker/coder-1/own"); err != nil {
		t.Fatal(err)
	}
	want("done", "started", "waiting for cut", "failed", "failed")
}

// Purpose: the owner's rerun of a finished plan delivery (Store.RerunNewest,
// swarm_deliver_job automation=true) applies the same recovery to items whose
// job had already succeeded before the automatic path existed, and is
// idempotent: a second rerun changes nothing. A failed item whose job never
// succeeded stays failed.
// Threat: deliveries stuck from before the fix having no supported recovery,
// or a rerun reviving an item whose job is still failed.
// Owners: Store.RerunNewest/rerunLocked/reviveItemsLocked over the real store.
func TestForEachOwnerRerunRevivesItemsOfSucceededJobs(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	parent, _, err := h.store.CreateDelivery(Automation{ID: "planner", Account: testAccount, Name: "planner",
		Steps: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{MaxParallel: 3, AfterField: "reuses"}}}}, Result{JobID: "parent", Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	ok := workerResult(folder, "main")
	blocked := workerResult(folder, "own")
	blocked.Status, blocked.JobStatus, blocked.Error = StatusFailed, "blocked", "blocked: speech budget exceeded"
	for _, r := range []*Result{ok, blocked} {
		if err := h.store.db.PutJSON(itemEndKey(testAccount, r.JobID), r); err != nil {
			t.Fatal(err)
		}
	}
	parent.State, parent.Steps[0].State = StateSucceeded, StateSucceeded
	parent.Steps[0].Items = []ItemRecord{
		{Index: 0, ID: "main", State: "failed", JobID: ok.JobID, Error: "worker job failed: blocked"},
		{Index: 1, ID: "cut", After: "main", State: "failed", Error: "waits for main, which did not finish: worker job failed: blocked"},
		{Index: 2, ID: "own", State: "failed", JobID: blocked.JobID, Error: "worker job failed: blocked"},
		{Index: 3, ID: "own-cut", After: "own", State: "failed", Error: "waits for own, which did not finish: worker job failed: blocked"},
	}
	if err := h.store.SaveDelivery(parent); err != nil {
		t.Fatal(err)
	}
	out, err := h.store.RerunNewest(testAccount, "parent", "")
	if err != nil || len(out) != 1 || !out[0].Rerun || out[0].Delivery.State != StateSucceeded {
		t.Fatalf("rerun: %+v %v", out, err)
	}
	got := out[0].Delivery.Steps[0].Items
	for i, want := range []string{"done", "waiting for main", "failed", "failed"} {
		if got[i].State != want {
			t.Fatalf("item %d = %+v, want %s", i, got[i], want)
		}
	}
	if _, found, err := h.store.db.GetBytes(keyPendingItemsPrefix + hexKey(testAccount) + "/" + parent.ID); err != nil || !found {
		t.Fatal("revived items are not queued for dispatch", err)
	}
	if out, err := h.store.RerunNewest(testAccount, "parent", ""); err != nil || out[0].Rerun {
		t.Fatalf("second rerun: %+v %v", out, err)
	}
}
