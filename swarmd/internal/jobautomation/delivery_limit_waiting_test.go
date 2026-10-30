package jobautomation

import (
	"context"
	"testing"
	"time"
)

// statusOnWaitAgents scripts one worker-backed job across Wait calls, like
// limitWaitingAgents, and records the delivery's shown status (read from the
// store) while each Wait runs, that is while the step is running.
type statusOnWaitAgents struct {
	limitWaitingAgents
	read   func() DeliveryStatus
	during []DeliveryStatus
}

func (a *statusOnWaitAgents) Wait(ctx context.Context, account, jobID string) (AgentOutcome, error) {
	a.during = append(a.during, a.read())
	return a.limitWaitingAgents.Wait(ctx, account, jobID)
}

// Requirement: a delivery whose agent step waits for its worker run's
// provider limit to reset (the run reruns itself at retry_at) shows state
// waiting (Delivery.Status) with until = the reset and reason = the step's
// reason, while what is stored stays running and unfinished. The delivery
// resumes by itself: a reset that passed while the daemon was stopped (the
// machine powered off) runs the step at the next start, the delivery shows
// running while the step runs, and it then carries on to its end.
// Threat (seen live): a delivery parked on a weekly limit showed running for
// days; or a parked delivery lost or never resumed after a power cycle.
// Owners: Delivery.Status, Runner.dispatch / runDelivery / runStep over the
// real Pebble store, reopened as a daemon start does (harness.restart); the
// worker job is the scripted AgentRunner boundary.
func TestDeliveryShowsWaitingOnAProviderLimitAndResumesAfterAPowerCycle(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	clock := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
	now := func() time.Time { return clock }
	h.runner.now = now
	until := time.Date(2026, 10, 30, 16, 1, 0, 0, time.UTC)
	reason := "waiting for the provider's limit to reset (until 2026-10-30T16:01:00Z): You've hit your weekly limit"
	agents := &statusOnWaitAgents{limitWaitingAgents: limitWaitingAgents{script: []AgentOutcome{
		{Status: "waiting", Error: reason, LimitUntil: until.UnixMilli()},
		{Status: StatusSucceeded},
	}}}
	agents.read = func() DeliveryStatus { return h.deliveries("worker/coder-1/run-1")[0].Status() }
	h.runner.agent = agents
	writeScript(t, folder, "after.sh", "echo after\n")
	h.process()
	h.automation(Automation{Name: "content plan", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "write", Type: StepAgent, Worker: "author", Brief: "Write the plan"},
		{ID: "after", Type: StepRun, Command: "after.sh"},
	}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateRunning || Terminal(d.State) || d.Steps[0].State != StateWaiting {
		t.Fatalf("stored delivery = %s, step %+v; want running with its step waiting", d.State, d.Steps[0])
	}
	if got := d.Status(); got.State != StateWaiting || got.Until != until.UnixMilli() || got.Reason != reason {
		t.Fatalf("shown status = %+v, want waiting until %d with the run's reason", got, until.UnixMilli())
	}
	if len(agents.during) != 1 || agents.during[0].State != StateRunning {
		t.Fatalf("while the step ran the delivery showed %+v, want running", agents.during)
	}
	if list, err := h.store.ListDeliveries(testAccount, DeliveryFilter{State: StateWaiting}); err != nil || len(list) != 1 {
		t.Fatalf("state=waiting lists %d (%v), want the parked delivery", len(list), err)
	}
	if list, err := h.store.ListDeliveries(testAccount, DeliveryFilter{State: StateRunning}); err != nil || len(list) != 0 {
		t.Fatalf("state=running lists %d (%v), want none", len(list), err)
	}
	// The delivery stays unfinished: it is what the runner resumes.
	if unfinished, err := h.store.Unfinished(); err != nil || len(unfinished) != 1 || unfinished[0].ID != d.ID {
		t.Fatalf("unfinished = %v (%v), want the parked delivery", unfinished, err)
	}

	// The daemon stops before the reset and starts after it (the machine
	// was powered off over the reset): the step runs at the start.
	h.restart()
	clock = until.Add(9 * time.Hour)
	h.runner.now, h.runner.agent = now, agents
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if len(agents.started) != 1 || len(agents.waits) != 2 || agents.waits[1] != "worker/author-1/run-1" {
		t.Fatalf("after the start: started %d, waits %v; want the same job read again", len(agents.started), agents.waits)
	}
	if len(agents.during) != 2 || agents.during[1].State != StateRunning || agents.during[1].Until != 0 {
		t.Fatalf("while the step ran again the delivery showed %+v, want running", agents.during)
	}
	if d.State != StateSucceeded || d.Status().State != StateSucceeded || d.Steps[1].State != StateSucceeded {
		t.Fatalf("after the reset the delivery = %s (%+v), want it carried on to succeeded", d.State, d.Steps)
	}
}

// Requirement: Delivery.Status shows waiting only when no step works now:
// a step running, or a reopen step waiting for its job to settle (it asks
// again within its timeout, so it has no NextAttemptAt), keeps running; a
// retry backoff (pending with NextAttemptAt) is not a provider limit; the
// earliest of several resets is until; finished and pending deliveries
// show their stored state.
// Threat: a delivery doing work shown parked (a drain or power-off then
// interrupts it), or a parked one shown with the wrong reset.
// Owner: Delivery.Status, a pure function of the stored record.
func TestDeliveryStatusShowsWaitingOnlyWhenNothingRuns(t *testing.T) {
	limit := func(at int64, reason string) StepRecord {
		return StepRecord{State: StateWaiting, NextAttemptAt: at, Reason: reason}
	}
	for _, tc := range []struct {
		name   string
		d      Delivery
		want   string
		until  int64
		reason string
	}{
		{"limit waiting", Delivery{State: StateRunning, Steps: []StepRecord{{State: StateSucceeded}, limit(500, "r"), {State: StatePending}}}, StateWaiting, 500, "r"},
		{"one running, one limit waiting", Delivery{State: StateRunning, Steps: []StepRecord{limit(500, "r"), {State: StateRunning}}}, StateRunning, 0, ""},
		{"running before a limit wait", Delivery{State: StateRunning, Steps: []StepRecord{{State: StateRunning}, limit(500, "r")}}, StateRunning, 0, ""},
		{"reopen asking its job", Delivery{State: StateRunning, Steps: []StepRecord{limit(500, "r"), {State: StateWaiting, Reason: "waiting: the job's run is in flight"}}}, StateRunning, 0, ""},
		{"retry backoff", Delivery{State: StateRunning, Steps: []StepRecord{{State: StatePending, NextAttemptAt: 500}}}, StateRunning, 0, ""},
		{"earliest reset", Delivery{State: StateRunning, Steps: []StepRecord{limit(900, "late"), limit(500, "early")}}, StateWaiting, 500, "early"},
		{"items reopened", Delivery{State: StateRunning, ItemsReopened: StateSucceeded, Steps: []StepRecord{limit(500, "r")}}, StateRunning, 0, ""},
		{"finished", Delivery{State: StateFailed, Steps: []StepRecord{limit(500, "r")}}, StateFailed, 0, ""},
		{"pending", Delivery{State: StatePending, Steps: []StepRecord{{State: StatePending}}}, StatePending, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.Status()
			if got.State != tc.want || got.Until != tc.until || got.Reason != tc.reason {
				t.Fatalf("Status() = %+v, want %s until %d reason %q", got, tc.want, tc.until, tc.reason)
			}
		})
	}
}
