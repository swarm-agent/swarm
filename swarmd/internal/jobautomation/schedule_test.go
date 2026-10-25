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
	now = now.Add(time.Minute)
	r.Tick(ctx)
	events, err = h.store.TriggerEvents(a)
	if err != nil || starts != 2 || len(events) != 5 || events[4].State != "failed" {
		t.Fatalf("uncertain dispatch overlapped: %+v starts=%d err=%v", events, starts, err)
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
