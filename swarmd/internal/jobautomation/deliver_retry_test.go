package jobautomation

import (
	"context"
	"strings"
	"testing"
)

type inspectionDeliverer struct {
	recordingDeliverer
	failures int
	calls    int
	refused  bool
}

func (f *inspectionDeliverer) Deliver(ctx context.Context, req DeliverRequest) (DeliverOutcome, error) {
	f.calls++
	if f.refused {
		return DeliverOutcome{}, &RefusedError{Reason: "lane_branch_mismatch"}
	}
	if f.calls <= f.failures {
		return DeliverOutcome{}, &TransientInspectionError{Reason: "Git inspection exceeded its time budget (inspection_timeout)"}
	}
	return f.recordingDeliverer.Deliver(ctx, req)
}

// Requirement: runStep persists three bounded inspection attempts even when a
// workflow has no configured retries. Threat: transient load loses reviewed
// work, or real mismatches are retried. The runner/store layer is the narrowest
// proof of scheduling and durable attempt counts; the delivery boundary is fake,
// not a claim of live Git integration.
func TestDeliverInspectionRetries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int
		refused  bool
		attempts int
		state    string
	}{
		{"third succeeds", 2, false, 3, StateSucceeded},
		{"exhausted", 9, false, 3, StateFailed},
		{"mismatch", 0, true, 1, StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			f := &inspectionDeliverer{failures: tc.failures, refused: tc.refused}
			h.runner.deliver = f
			h.process()
			folder := t.TempDir()
			h.automation(Automation{Name: "merge", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "merge", Type: StepDeliver, To: DeliverMerge}}})
			result := workerResult(folder, "run-1")
			result.Deliverables = []Deliverable{{Name: "branch", Kind: DeliverableBranch}}
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "direct")
			for i := 0; i < 5; i++ {
				h.process()
			}
			d := h.deliveries("worker/coder-1/run-1")[0]
			if d.State != tc.state || d.Steps[0].Attempts != tc.attempts || f.calls != tc.attempts {
				t.Fatalf("delivery %+v calls %d", d, f.calls)
			}
			if tc.failures > 3 && !strings.Contains(d.Steps[0].Reason, "inspection_timeout") {
				t.Fatalf("lost reason: %+v", d.Steps[0])
			}
			want := 0
			if tc.state == StateSucceeded {
				want = 1
			}
			if len(f.delivered) != want {
				t.Fatalf("delivered %d, want %d", len(f.delivered), want)
			}
		})
	}
}

type cancelledInspectionDeliverer struct {
	recordingDeliverer
	cancel context.CancelFunc
}

func (f *cancelledInspectionDeliverer) Deliver(context.Context, DeliverRequest) (DeliverOutcome, error) {
	f.cancel()
	return DeliverOutcome{}, &TransientInspectionError{Reason: "Assessment cancelled (inspection_cancelled)"}
}

// Requirement: shutdown during an assessment records a retry rather than a
// terminal failure or an unbounded running attempt. Runner.runStep with the
// real store is the narrow persistence boundary; no shutdown daemon is needed.
func TestDeliverInspectionCancellationPersistsRetry(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.runner.deliver = &cancelledInspectionDeliverer{cancel: cancel}
	h.runner.backoff = defaultBackoff
	d := Delivery{ID: "cancelled-delivery", Account: testAccount, Steps: []StepRecord{{ID: "merge", Type: StepDeliver}}, Result: Result{Deliverables: []Deliverable{{Name: "branch", Kind: DeliverableBranch}}}}
	if h.runner.runStep(ctx, &d, 0, Step{ID: "merge", Type: StepDeliver, To: DeliverMerge, TimeoutSeconds: 600}) {
		t.Fatal("cancelled inspection succeeded")
	}
	rec := d.Steps[0]
	if rec.State != StatePending || rec.Attempts != 1 || rec.NextAttemptAt <= rec.FinishedAt || !strings.Contains(rec.Reason, "inspection_cancelled") {
		t.Fatalf("record %+v", rec)
	}
}

// Requirement: shutdown on the last inspection attempt must remain resumable.
// Threat: the retry ceiling turns daemon cancellation into permanent failure.
// runStep and the real store prove the durable state and successful resume at
// the narrow runner boundary, without claiming live daemon/Git qualification.
func TestDeliverInspectionThirdAttemptShutdownResumes(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.runner.deliver = &cancelledInspectionDeliverer{cancel: cancel}
	d := Delivery{ID: "third-cancelled", Account: testAccount, Steps: []StepRecord{{ID: "merge", Type: StepDeliver, Attempts: 2}}, Result: Result{Deliverables: []Deliverable{{Name: "branch", Kind: DeliverableBranch}}}}
	step := Step{ID: "merge", Type: StepDeliver, To: DeliverMerge, TimeoutSeconds: 600}
	if h.runner.runStep(ctx, &d, 0, step) {
		t.Fatal("cancelled inspection succeeded")
	}
	saved, err := h.runner.store.GetDelivery(testAccount, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Steps[0].State != StateRunning || saved.Steps[0].Attempts != 3 || saved.Steps[0].FinishedAt != 0 {
		t.Fatalf("not resumable: %+v", saved.Steps[0])
	}
	h.runner.deliver = &recordingDeliverer{}
	if !h.runner.runStep(context.Background(), &saved, 0, step) {
		t.Fatalf("resume failed: %+v", saved.Steps[0])
	}
	saved, err = h.runner.store.GetDelivery(testAccount, d.ID)
	if err != nil || saved.Steps[0].State != StateSucceeded {
		t.Fatalf("resume not persisted: %+v %v", saved, err)
	}
}
