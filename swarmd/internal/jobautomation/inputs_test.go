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
