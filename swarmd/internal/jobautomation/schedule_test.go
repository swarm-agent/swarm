package jobautomation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Purpose: Normalize is the shared save boundary. These domain tests prevent
// ambiguous sources, invalid clocks, and accidental empty job-end workflows.
func TestScheduleValidation(t *testing.T) {
	for _, tc := range []struct {
		name, cron, zone             string
		event, noTrigger, steps, bad bool
	}{
		{name: "trigger only", cron: "0 9 * JAN MON", zone: "UTC"},
		{name: "bad cron", cron: "daily", bad: true},
		{name: "bad zone", cron: "* * * * *", zone: "Not/AZone", bad: true},
		{name: "local zone", cron: "* * * * *", zone: "Local", bad: true},
		{name: "both", cron: "* * * * *", event: true, bad: true},
		{name: "empty ordinary", noTrigger: true, bad: true},
		{name: "ordinary", noTrigger: true, steps: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAutomation(t.TempDir())
			if !tc.steps {
				a.Steps = nil
			}
			if !tc.noTrigger {
				a.On = &Trigger{Schedule: &TriggerSchedule{Cron: tc.cron, Timezone: tc.zone}, Start: TriggerStart{Worker: "worker-1", Brief: "plan"}}
			}
			if tc.event {
				a.On.Event = TriggerEvent{Source: "webhook", ID: "id"}
			}
			if err := a.Normalize(); (err != nil) != tc.bad {
				t.Fatalf("Normalize: %v", err)
			}
		})
	}
	var trigger Trigger
	if err := json.Unmarshal([]byte(`{"event":{},"schedule":{"cron":"* * * * *"}}`), &trigger); err == nil {
		t.Fatal("accepted two sources")
	}
}

// Purpose: TriggerRunner's schedule path must render local dates, serialize
// canonical starts, persist skip/failure receipts and never replay downtime.
// Real Pebble and a fixed clock isolate these invariants from provider latency.
func TestScheduleSlotsAndRestart(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 1, 2, 4, 59, 0, 0, time.UTC)
	h.store.now = func() time.Time { return now }
	a := testAutomation(t.TempDir())
	a.Steps = nil
	a.Inputs = map[string]Input{"brand": {}}
	a.With = map[string]string{"brand": "Brand"}
	a.On = &Trigger{Schedule: &TriggerSchedule{Cron: "* * * * *", Timezone: "America/New_York"}, Start: TriggerStart{Worker: "worker-1", Brief: "{{inputs.brand}} {{schedule.date}} {{schedule.time}}"}}
	a = h.automation(a)
	starts := 0
	running := true
	r := &TriggerRunner{Store: h.store, Account: func() string { return testAccount }, Start: func(_ context.Context, _ Automation, brief, key string) (string, error) {
		starts++
		if brief != "Brand 2026-01-02 00:00" || key == "" {
			t.Fatalf("brief=%q key=%q", brief, key)
		}
		return "job-1", nil
	}, Running: func(context.Context, Automation, string) (bool, error) { return running, nil }}
	ctx := context.Background()
	r.Tick(ctx)
	now = now.Add(time.Minute)
	r.Tick(ctx)
	r.Tick(ctx)
	if starts != 1 {
		t.Fatalf("starts=%d", starts)
	}
	now = now.Add(time.Minute)
	r.Tick(ctx)
	events, err := h.store.TriggerEvents(a)
	if err != nil || len(events) != 2 || events[0].State != "started" || events[1].State != "skipped" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	h.restart()
	now = now.Add(3 * time.Hour)
	h.store.now = func() time.Time { return now }
	r.Store = h.store
	r.scheduleSeen = nil
	running = false
	r.Tick(ctx)
	r.Tick(ctx)
	events, err = h.store.TriggerEvents(a)
	if err != nil || starts != 1 || len(events) != 3 || events[2].State != "skipped" {
		t.Fatalf("starts=%d events=%+v err=%v", starts, events, err)
	}
	r.Start = func(context.Context, Automation, string, string) (string, error) {
		starts++
		return "", errors.New("refused")
	}
	now = now.Add(time.Minute)
	r.Tick(ctx)
	r.Tick(ctx)
	events, err = h.store.TriggerEvents(a)
	if err != nil || starts != 2 || len(events) != 4 || events[3].State != "failed" || events[3].JobID != "" {
		t.Fatalf("failure receipt: %+v err=%v starts=%d", events, err, starts)
	}
	// Inject the durable boundary left by a crash between dispatch and receipt.
	rendered, err := a.RenderInputs()
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.store.triggerState(rendered)
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveJob = "schedule-dispatch-pending"
	if err := h.db.PutJSON(triggerKey(a), state); err != nil {
		t.Fatal(err)
	}
	r.Start = func(_ context.Context, _ Automation, brief, key string) (string, error) {
		starts++
		if key == "" || brief == "" {
			t.Fatal("missing recovery payload")
		}
		return "recovered-job", nil
	}
	running = true
	h.restart()
	h.store.now = func() time.Time { return now }
	r.Store = h.store
	r.scheduleSeen = nil
	r.Tick(ctx)
	now = now.Add(time.Minute)
	r.Tick(ctx)
	events, err = h.store.TriggerEvents(a)
	if err != nil || starts != 3 || len(events) != 5 || events[3].JobID != "recovered-job" || events[4].State != "skipped" {
		t.Fatalf("recovery: %+v starts=%d err=%v", events, starts, err)
	}
}

// Purpose: Runner.consume owns cross-workflow matching. Real persisted
// deliveries prove trigger-only jobs reach worker-scoped handlers while
// triggers with steps never create a second ordinary delivery.
func TestTriggerOnlyMatching(t *testing.T) {
	for _, ownSteps := range []bool{false, true} {
		t.Run(map[bool]string{false: "trigger only", true: "own steps"}[ownSteps], func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			a := testAutomation(folder)
			a.Name = "starter"
			a.On = &Trigger{Event: TriggerEvent{Source: "webhook", ID: "id"}, Start: TriggerStart{Worker: "coder-1", Brief: "work"}}
			if !ownSteps {
				a.Steps = nil
			}
			a = h.automation(a)
			b := testAutomation(folder)
			b.Name = "handler"
			b.Scope.Worker = "coder-1"
			b = h.automation(b)
			h.process()
			result := workerResult(folder, "run-1")
			result.TriggerID = a.ID
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "workflow_trigger")
			h.process()
			ds := h.deliveries(result.JobID)
			expected := b.ID
			if ownSteps {
				expected = a.ID
			}
			if len(ds) != 1 || ds[0].AutomationID != expected {
				t.Fatalf("deliveries=%+v", ds)
			}
		})
	}
}

// Purpose: consume must not poison the account cursor when the owning trigger
// is deleted. The real store/runner proves both that completion and later work
// still create worker-scoped deliveries.
func TestDeletedTriggerContinuesProcessing(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	a := testAutomation(folder)
	a.On = &Trigger{Event: TriggerEvent{Source: "webhook", ID: "id"}, Start: TriggerStart{Worker: "coder-1", Brief: "work"}}
	a = h.automation(a)
	b := testAutomation(folder)
	b.Name = "handler"
	b.Scope.Worker = "coder-1"
	b = h.automation(b)
	h.process()
	if err := h.store.Delete(testAccount, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-deleted", "run-later"} {
		result := workerResult(folder, id)
		if id == "run-deleted" {
			result.TriggerID = a.ID
		}
		h.results.results[id] = result
		h.jobEnd(id, "", "succeeded", "workflow_trigger")
		h.process()
		ds := h.deliveries(result.JobID)
		if len(ds) != 1 || ds[0].AutomationID != b.ID {
			t.Fatalf("%s: %+v", id, ds)
		}
	}
}

// Purpose: Trigger.UnmarshalJSON is nested inside strict HTTP/import decoders;
// reject misspellings at every nested source/start boundary without mutating
// the destination, rather than silently selecting UTC or a default interval.
func TestTriggerRejectsUnknownFields(t *testing.T) {
	for _, data := range []string{
		`{"schedule":{"cron":"* * * * *","timzone":"UTC"}}`,
		`{"event":{"source":"pull","evry_seconds":60}}`,
		`{"schedule":{"cron":"* * * * *"},"strat":{}}`,
		`{"schedule":{"cron":"* * * * *"},"start":{"wroker":"worker"}}`,
	} {
		value := Trigger{Start: TriggerStart{Worker: "unchanged"}}
		if err := json.Unmarshal([]byte(data), &value); err == nil {
			t.Fatalf("accepted %s", data)
		}
		if value.Start.Worker != "unchanged" {
			t.Fatal("mutated rejected definition")
		}
	}
}

// Purpose: a crash reservation must survive restart;
// finishScheduleDispatch must reuse the exact start identity, keep failed
// recovery pending, and suppress new starts while the recovered job runs.
func TestSchedulePendingRecovery(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	h.store.now = func() time.Time { return now }
	a := testAutomation(t.TempDir())
	a.On = &Trigger{Schedule: &TriggerSchedule{Cron: "* * * * *"}, Start: TriggerStart{Worker: "worker-1", Brief: "original"}}
	a = h.automation(a)
	state, err := h.store.triggerState(a)
	if err != nil {
		t.Fatal(err)
	}
	id := now.Format(time.RFC3339)
	state.ActiveJob = "schedule-dispatch-pending"
	state.Pending = &scheduleDispatch{Automation: a, Brief: "original", Key: "reserved-key", ReceiptID: id}
	state.Events = []TriggerReceipt{{ID: id, State: "skipped"}}
	state.NextPoll = now.Add(time.Minute).UnixMilli()
	if err := h.db.PutJSON(triggerKey(a), state); err != nil {
		t.Fatal(err)
	}
	h.restart()
	now = now.Add(time.Hour)
	h.store.now = func() time.Time { return now }
	attempts := 0
	r := &TriggerRunner{Store: h.store, Account: func() string { return testAccount }, Start: func(_ context.Context, original Automation, brief, key string) (string, error) {
		attempts++
		if brief != "original" || key != "reserved-key" || original.On.Start.Worker != "worker-1" {
			t.Fatalf("changed recovery payload: %+v %q %q", original, brief, key)
		}
		if attempts == 1 {
			return "", errors.New("temporarily unavailable")
		}
		return "existing-job", nil
	}, Running: func(_ context.Context, _ Automation, job string) (bool, error) {
		if job != "existing-job" {
			t.Fatalf("wrong job %s", job)
		}
		return true, nil
	}}
	r.Tick(context.Background())
	state, err = h.store.triggerState(a)
	if err != nil || state.Pending == nil || state.ActiveJob != "schedule-dispatch-pending" {
		t.Fatalf("lost reservation: %+v %v", state, err)
	}
	r.Tick(context.Background())
	state, err = h.store.triggerState(a)
	if err != nil || state.Pending != nil || state.ActiveJob != "existing-job" || state.Events[0].JobID != "existing-job" {
		t.Fatalf("recovery: %+v %v", state, err)
	}
	now = now.Add(time.Minute)
	r.Tick(context.Background())
	if attempts != 2 {
		t.Fatalf("new dispatch overlapped: %d", attempts)
	}
}
