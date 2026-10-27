package jobautomation

import (
	"context"
	"strings"
	"testing"
)

// Purpose: itemDependency must judge the latest review delivery, not a skipped
// merge from a send-back round. TriggerRunner.tickItems and Store.RerunNewest
// must keep B undispatched until A finishes, propagate rounds_exhausted, and
// restore the durable pending-item index for legacy dependency failures without
// repeating successful work. The real runner/store is the narrowest layer for
// this lifecycle; only reviewer, session reopen, and delivery adapters are fake.
func TestForEachDependencyReviewRounds(t *testing.T) {
	for _, mode := range []string{"success", "exhausted", "retry"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			h.withReopener(&sessionReopener{})
			deliverer := &recordingDeliverer{}
			h.runner.deliver = deliverer
			verdict := VerdictOK
			if mode == "exhausted" {
				verdict = VerdictBlock
			}
			h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "missing thumbnail"}, {Verdict: verdict, Findings: "still missing thumbnail"}}
			h.process()
			a := reviewLoop(folder, 1)
			a.Steps[2] = Step{ID: "merge", Type: StepDeliver, To: "merge", If: []Condition{{Step: "review", Verdict: VerdictOK}}}
			h.automation(a)
			result := workerResult(folder, "wide")
			result.Deliverables = []Deliverable{{Name: "branch", Kind: DeliverableBranch}}
			result.RequestSource = "workflow/planner/delivery/plan/step/make"
			h.results.results["wide"] = result
			parent, _, err := h.store.CreateDelivery(Automation{ID: "planner", Account: testAccount, Name: "planner", Steps: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{MaxParallel: 1}}}}, Result{JobID: "parent", Folder: folder})
			if err != nil {
				t.Fatal(err)
			}
			parent.State = StateSucceeded
			parent.Steps[0].State = StateSucceeded
			parent.Steps[0].Items = []ItemRecord{{ID: "wide", State: "done", JobID: result.JobID}, {ID: "cut", After: "wide", State: "waiting for wide"}}
			save := func(d Delivery) {
				t.Helper()
				if err := h.store.SaveDelivery(d); err != nil {
					t.Fatal(err)
				}
			}
			save(parent)
			starts := 0
			tr := TriggerRunner{Store: h.store, Account: func() string { return testAccount }, Start: func(context.Context, Automation, string, string) (string, error) {
				starts++
				return "worker/maker/cut", nil
			}, Running: func(context.Context, Automation, string) (bool, error) { return true, nil }}
			check := func(want string) ItemRecord {
				t.Helper()
				d, err := h.store.GetDelivery(testAccount, parent.ID)
				if err != nil {
					t.Fatal(err)
				}
				item := d.Steps[0].Items[1]
				if item.State != want {
					t.Fatalf("item = %+v, want %s", item, want)
				}
				return item
			}
			h.jobEnd("wide", "", "succeeded", result.RequestSource)
			h.process()
			tr.Tick(context.Background())
			check("waiting for wide")
			if starts != 0 || len(deliverer.delivered) != 0 {
				t.Fatal("dispatched before successful review")
			}
			if mode == "retry" {
				parent.Steps[0].Items[1].State = "failed"
				parent.Steps[0].Items[1].Error = "waits for wide, which did not finish: workflow review loop deliver step merge skipped"
				// Seed a legacy queue-runner failure, bypassing SaveDelivery's
				// protection against overwriting independently advanced items.
				if err := h.store.putDeliveryLocked(parent); err != nil {
					t.Fatal(err)
				}
				out, err := h.store.RerunNewest(testAccount, "parent", "")
				if err != nil || len(out) != 1 || out[0].Rerun {
					t.Fatalf("premature retry: %+v %v", out, err)
				}
				check("failed")
			}
			result.Git.Head = strings.Repeat("c", 40)
			h.jobEnd("wide", "followup-1", "succeeded", result.RequestSource)
			h.process()
			if mode == "retry" {
				out, err := h.store.RerunNewest("other-account", "parent", "")
				if err != nil || len(out) != 0 {
					t.Fatalf("cross-account retry: %+v %v", out, err)
				}
				check("failed")
				out, err = h.store.RerunNewest(testAccount, "parent", "")
				if err != nil || len(out) != 1 || !out[0].Rerun || out[0].Delivery.Steps[0].State != StateSucceeded || out[0].Delivery.Steps[0].Items[0].State != "done" {
					t.Fatalf("retry: %+v %v", out, err)
				}
				check("waiting for wide")
				out, err = h.store.RerunNewest(testAccount, "parent", "")
				if err != nil || out[0].Rerun {
					t.Fatalf("duplicate retry: %+v %v", out, err)
				}
			}
			tr.Tick(context.Background())
			if mode == "exhausted" {
				item := check("failed")
				if !strings.Contains(item.Error, ReasonRoundsExhausted) || starts != 0 || len(deliverer.delivered) != 0 {
					t.Fatalf("exhaustion: %+v starts=%d", item, starts)
				}
			} else {
				check("started")
				if starts != 1 || len(deliverer.delivered) != 1 {
					t.Fatalf("starts=%d merges=%d", starts, len(deliverer.delivered))
				}
			}
		})
	}
}
