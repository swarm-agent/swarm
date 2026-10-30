package jobautomation

import (
	"context"
	"strings"
	"testing"
	"time"
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

// Requirement: runStep persists bounded inspection attempts even when a
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
		{"exhausted", 99, false, maxInspectionAttempts, StateFailed},
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
			for i := 0; i < maxInspectionAttempts+2; i++ {
				h.process()
			}
			d := h.deliveries("worker/coder-1/run-1")[0]
			if d.State != tc.state || d.Steps[0].Attempts != tc.attempts || f.calls != tc.attempts {
				t.Fatalf("delivery %+v calls %d", d, f.calls)
			}
			if tc.failures > maxInspectionAttempts && !strings.Contains(d.Steps[0].Reason, "inspection_timeout") {
				t.Fatalf("lost reason: %+v", d.Steps[0])
			}
			if wantFailures := min(tc.failures, tc.attempts); len(d.Steps[0].Failures) != wantFailures && !tc.refused {
				t.Fatalf("failures %+v, want %d", d.Steps[0].Failures, wantFailures)
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
	d := Delivery{ID: "third-cancelled", Account: testAccount, Steps: []StepRecord{{ID: "merge", Type: StepDeliver, Attempts: maxInspectionAttempts - 1}}, Result: Result{Deliverables: []Deliverable{{Name: "branch", Kind: DeliverableBranch}}}}
	step := Step{ID: "merge", Type: StepDeliver, To: DeliverMerge, TimeoutSeconds: 600}
	if h.runner.runStep(ctx, &d, 0, step) {
		t.Fatal("cancelled inspection succeeded")
	}
	saved, err := h.runner.store.GetDelivery(testAccount, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Steps[0].State != StateRunning || saved.Steps[0].Attempts != maxInspectionAttempts || saved.Steps[0].FinishedAt != 0 {
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

// slowSpellDeliverer times out Git inspection until the runner's clock
// passes the end of a slow spell, then delivers.
type slowSpellDeliverer struct {
	recordingDeliverer
	now      func() time.Time
	spellEnd time.Time
	calls    int
}

func (f *slowSpellDeliverer) Deliver(ctx context.Context, req DeliverRequest) (DeliverOutcome, error) {
	f.calls++
	if f.now().Before(f.spellEnd) {
		return DeliverOutcome{}, &TransientInspectionError{Reason: "Git inspection exceeded its time budget (inspection_timeout)"}
	}
	return f.recordingDeliverer.Deliver(ctx, req)
}

// Requirement: a reviewed job's merge step outlasts a ~40-minute spell in
// which Git inspection keeps exceeding its time budget (a busy machine, a job
// with large media), and the delivery record keeps every failed attempt's
// reason, also across a manual rerun. Threat (the reported regression): three
// attempts 15s/30s apart failed the step within a minute, leaving reviewed
// work unmerged until someone reran it, with the earlier reasons lost.
// Runner.runStep with the real store, the production defaultBackoff and a
// fixed fake clock is the narrowest boundary that owns retry scheduling and
// the durable step record; the delivery boundary is fake (no live Git).
func TestDeliverInspectionOutlastsSlowSpell(t *testing.T) {
	h := newHarness(t)
	clock := time.Date(2026, 10, 28, 7, 40, 0, 0, time.UTC)
	start := clock
	h.runner.now = func() time.Time { return clock }
	h.runner.backoff = defaultBackoff
	f := &slowSpellDeliverer{now: h.runner.now, spellEnd: start.Add(40 * time.Minute)}
	h.runner.deliver = f
	d := Delivery{ID: "slow-spell", Account: testAccount, Steps: []StepRecord{{ID: "merge", Type: StepDeliver}}, Result: Result{Deliverables: []Deliverable{{Name: "branch", Kind: DeliverableBranch}}}}
	step := Step{ID: "merge", Type: StepDeliver, To: DeliverMerge, TimeoutSeconds: 600}
	for i := 0; ; i++ {
		if i > maxInspectionAttempts {
			t.Fatalf("runaway retries: %+v", d.Steps[0])
		}
		if h.runner.runStep(context.Background(), &d, 0, step) {
			break
		}
		rec := d.Steps[0]
		if rec.State != StatePending {
			t.Fatalf("step gave up during the slow spell after %d attempts (%s): %+v", rec.Attempts, clock.Sub(start), rec)
		}
		clock = time.UnixMilli(rec.NextAttemptAt).UTC()
	}
	saved, err := h.runner.store.GetDelivery(testAccount, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := saved.Steps[0]
	if rec.State != StateSucceeded || len(f.delivered) != 1 || rec.Attempts <= 3 || rec.Attempts != f.calls {
		t.Fatalf("not delivered after the spell: %+v calls %d delivered %d", rec, f.calls, len(f.delivered))
	}
	if len(rec.Failures) != rec.Attempts-1 {
		t.Fatalf("failures %+v, want %d", rec.Failures, rec.Attempts-1)
	}
	for i, failure := range rec.Failures {
		if failure.Attempt != i+1 || failure.At <= 0 || !strings.Contains(failure.Reason, "inspection_timeout") {
			t.Fatalf("failure %d: %+v", i, failure)
		}
	}

	// A spell longer than the whole window still fails, bounded, and a
	// rerun keeps the earlier reasons beside its own.
	f2 := &slowSpellDeliverer{now: h.runner.now, spellEnd: clock.Add(24 * time.Hour)}
	h.runner.deliver = f2
	long := Delivery{ID: "long-spell", Account: testAccount, State: StateRunning, Steps: []StepRecord{{ID: "merge", Type: StepDeliver}}, Result: Result{Deliverables: []Deliverable{{Name: "branch", Kind: DeliverableBranch}}}}
	for !h.runner.runStep(context.Background(), &long, 0, step) && long.Steps[0].State == StatePending {
		clock = time.UnixMilli(long.Steps[0].NextAttemptAt).UTC()
	}
	if long.Steps[0].State != StateFailed || f2.calls != maxInspectionAttempts || len(long.Steps[0].Failures) != maxInspectionAttempts {
		t.Fatalf("long spell: %+v calls %d", long.Steps[0], f2.calls)
	}
	long.State = StateFailed
	if err := h.runner.store.SaveDelivery(long); err != nil {
		t.Fatal(err)
	}
	out, err := h.runner.store.Rerun(testAccount, long.ID)
	if err != nil || !out.Rerun {
		t.Fatalf("rerun %v %v", out.Rerun, err)
	}
	rerun := out.Delivery
	if rerun.Steps[0].State != StatePending || rerun.Steps[0].Attempts != 0 || len(rerun.Steps[0].Failures) != maxInspectionAttempts {
		t.Fatalf("rerun lost failures: %+v", rerun.Steps[0])
	}
	if !h.runner.runStep(context.Background(), &rerun, 0, step) && len(rerun.Steps[0].Failures) != maxInspectionAttempts+1 {
		t.Fatalf("rerun attempt not recorded: %+v", rerun.Steps[0].Failures)
	}
	last := rerun.Steps[0].Failures[len(rerun.Steps[0].Failures)-1]
	if last.Rerun != 1 || last.Attempt != 1 {
		t.Fatalf("rerun failure %+v", last)
	}
}

// Requirement: the failure history is bounded so a step rerun many times
// cannot grow its delivery record without limit; the newest reasons stay.
func TestStepFailuresBounded(t *testing.T) {
	var rec StepRecord
	for i := 1; i <= maxStepFailures+5; i++ {
		rec.Attempts = i
		rec.recordFailure(0, int64(i), "reason")
	}
	if len(rec.Failures) != maxStepFailures || rec.Failures[0].Attempt != 6 || rec.Failures[maxStepFailures-1].Attempt != maxStepFailures+5 {
		t.Fatalf("failures %+v", rec.Failures)
	}
}
