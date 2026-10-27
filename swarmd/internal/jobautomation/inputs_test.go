package jobautomation

import (
	"context"
	"testing"
)

// Purpose: Normalize must reject invalid reusable parameters without replacing
// the stored template. RenderInputs is the narrow domain boundary preventing
// unknown references and unsafe substituted deliver paths from being saved.
func TestWorkflowInputsValidation(t *testing.T) {
	for _, tc := range []string{"missing", "undeclared", "unknown", "bad-name", "path"} {
		t.Run(tc, func(t *testing.T) {
			a := testAutomation(t.TempDir())
			a.Inputs = map[string]Input{"brand": {}}
			a.With = map[string]string{"brand": "swarm"}
			a.Steps = []Step{{Type: StepAgent, Worker: "coder", Brief: "{{inputs.brand}}"}}
			switch tc {
			case "missing":
				a.With = nil
			case "undeclared":
				a.With["other"] = "x"
			case "unknown":
				a.Steps[0].Brief = "{{inputs.other}}"
			case "bad-name":
				a.Inputs["Bad"] = Input{}
			case "path":
				a.Steps = []Step{{Type: StepDeliver, To: "folder", Path: "{{inputs.brand}}"}}
				a.With["brand"] = "../escape"
			}
			if err := a.Normalize(); !IsValidationError(err) {
				t.Fatalf("expected validation refusal, got %v", err)
			}
		})
	}
	a := testAutomation(t.TempDir())
	value := "swarm"
	a.Inputs = map[string]Input{"brand": {Default: &value}}
	a.Steps = []Step{{Type: StepAgent, Worker: "coder", Brief: "write {{inputs.brand}}"}}
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	if a.Steps[0].Brief != "write {{inputs.brand}}" {
		t.Fatal("template lost")
	}
	rendered, err := a.RenderInputs()
	if err != nil || rendered.Steps[0].Brief != "write swarm" {
		t.Fatal(rendered, err)
	}
}

// Purpose: Store.Receive and TriggerRunner must interpolate instance inputs
// before event fields when constructing a start, retaining the durable template.
// A real temporary store and recording start callback isolate this boundary.
func TestWorkflowInputsTriggerRuntime(t *testing.T) {
	_, s := openTestStore(t)
	a := testAutomation(t.TempDir())
	a.Inputs = map[string]Input{"brand": {}}
	a.With = map[string]string{"brand": "swarm"}
	a.On = &Trigger{Event: TriggerEvent{Source: "webhook", ID: "id"}, Start: TriggerStart{Worker: "worker-1", Brief: "{{inputs.brand}} {{event.id}}"}}
	a, _, err := s.Put(testAccount, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Receive(a, []byte(`{"id":"item"}`)); err != nil {
		t.Fatal(err)
	}
	// TriggerEvents must fingerprint the rendered definition just like Receive
	// and Tick, without exposing payloads or losing queued/started receipts.
	checkEvents := func(state, job string) {
		t.Helper()
		events, err := s.TriggerEvents(a)
		if err != nil || len(events) != 1 {
			t.Fatalf("events: %+v, error: %v", events, err)
		}
		if events[0].ID != "item" || events[0].State != state || events[0].JobID != job || events[0].Item != nil {
			t.Fatalf("unexpected receipt: %+v", events[0])
		}
	}
	checkEvents("queued", "")
	got := ""
	r := TriggerRunner{Store: s, Account: func() string { return testAccount }, Start: func(_ context.Context, _ Automation, brief, key string) (string, error) {
		got = brief
		return "worker/w/r", nil
	}}
	r.Tick(context.Background())
	checkEvents("started", "worker/w/r")
	invalid := a
	invalid.With = nil
	if _, err := s.TriggerEvents(invalid); !IsValidationError(err) {
		t.Fatalf("invalid input binding accepted: %v", err)
	}
	checkEvents("started", "worker/w/r")
	if got != "swarm item" {
		t.Fatalf("brief %q", got)
	}
	saved, err := s.Get(testAccount, a.ID)
	if err != nil || saved.On.Start.Brief != "{{inputs.brand}} {{event.id}}" {
		t.Fatal(saved, err)
	}
}

// Purpose: CreateDelivery and Runner must pass the substituted brief to the
// agent boundary, not the template. Real durable delivery execution is narrower
// than provider testing and proves this orchestration contract directly.
func TestWorkflowInputsStepRuntime(t *testing.T) {
	h := newHarness(t)
	a := testAutomation(t.TempDir())
	a.Inputs = map[string]Input{"brand": {}}
	a.With = map[string]string{"brand": "swarm"}
	a.Steps = []Step{{Type: StepAgent, Worker: "finder", Brief: "write {{inputs.brand}}"}}
	a, _, err := h.store.Put(testAccount, a)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := h.store.CreateDelivery(a, Result{V: ResultVersion, JobID: "worker/w/r", Folder: a.Scope.Folder, Status: StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	h.runner.runDelivery(context.Background(), testAccount, d.ID)
	if len(h.agents.started) != 1 {
		t.Fatalf("agent requests: %+v", h.agents)
	}
	if h.agents.started[0].Brief != "write swarm" {
		t.Fatal(h.agents.started[0].Brief)
	}
}

// Purpose: Normalize and Matches must validate/render all scope bindings while
// preserving portable templates. This domain test isolates malformed references
// and ensures job/schedule filters are not bypassed after substitution.
func TestWorkflowInputsScopeFields(t *testing.T) {
	a := testAutomation(t.TempDir())
	a.Inputs = map[string]Input{"worker": {}, "job": {}, "schedule": {}}
	a.With = map[string]string{"worker": "maker", "job": "worker/maker/run", "schedule": "daily"}
	a.Scope.Worker, a.Scope.Job, a.Scope.Schedule = "{{inputs.worker}}", "{{inputs.job}}", "{{inputs.schedule}}"
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	if a.Scope.Worker != "{{inputs.worker}}" || a.Scope.Job != "{{inputs.job}}" || a.Scope.Schedule != "{{inputs.schedule}}" {
		t.Fatal("lost templates")
	}
	r := &Result{Folder: a.Scope.Folder, JobID: a.With["job"], ScheduleID: "daily", Worker: &ResultWorker{ID: "maker"}, Status: StatusSucceeded}
	if !a.Matches(r) {
		t.Fatal("did not match rendered scope")
	}
	r.ScheduleID = "other"
	if a.Matches(r) {
		t.Fatal("matched wrong schedule")
	}
	for _, key := range []string{"job", "schedule"} {
		old := a.With[key]
		a.With[key] = "bad reference!"
		if err := a.Normalize(); !IsValidationError(err) {
			t.Fatal(err)
		}
		a.With[key] = old
	}
}

// Purpose: Normalize must reject invalid rendered clock fields before saving;
// RenderInputs must preserve templates and produce the clock used by scheduling.
func TestWorkflowInputsScheduleValidation(t *testing.T) {
	for _, tc := range []struct {
		cron, zone string
		bad        bool
	}{{"0 9 * * *", "UTC", false}, {"bad", "UTC", true}, {"0 9 * * *", "Not/AZone", true}} {
		a := testAutomation(t.TempDir())
		a.Inputs = map[string]Input{"cron": {}, "zone": {}}
		a.With = map[string]string{"cron": tc.cron, "zone": tc.zone}
		a.On = &Trigger{Schedule: &TriggerSchedule{Cron: "{{inputs.cron}}", Timezone: "{{inputs.zone}}"}, Start: TriggerStart{Worker: "maker", Brief: "work"}}
		err := a.Normalize()
		if tc.bad {
			if !IsValidationError(err) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if a.On.Schedule.Cron != "{{inputs.cron}}" || a.On.Schedule.Timezone != "{{inputs.zone}}" {
			t.Fatal("lost schedule templates")
		}
		rendered, err := a.RenderInputs()
		if err != nil || rendered.On.Schedule.Cron != tc.cron || rendered.On.Schedule.Timezone != tc.zone {
			t.Fatal(rendered, err)
		}
	}
}
