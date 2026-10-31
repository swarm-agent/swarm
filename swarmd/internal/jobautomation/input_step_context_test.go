package jobautomation

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: Normalize/RenderInputs and persisted delivery execution must treat
// placeholder-looking with values as literal data. This domain/persistence test
// is narrower than provider execution and proves earlier/later references in
// input values cannot alter validation, including for_each admission.
func TestInputStepContextIsLiteral(t *testing.T) {
	for _, each := range []bool{false, true} {
		a := testAutomation(t.TempDir())
		literal := "{{steps.review.output}} {{steps.later.handoff}}"
		a.Inputs = map[string]Input{"quote": {}}
		a.With = map[string]string{"quote": literal}
		a.Steps = []Step{
			{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review"},
			{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "{{inputs.quote}}"},
			{ID: "later", Type: StepAgent, Worker: "finder", Brief: "Check"},
		}
		if each {
			a.Steps[1].Worker = "worker-1"
			a.Steps[1].ForEach = &ForEach{File: "items.json"}
			a.Steps[1].Brief += " {{item.id}}"
		}
		if err := a.Normalize(); err != nil {
			t.Fatalf("literal input refused (each=%v): %v", each, err)
		}
		rendered, err := a.RenderInputs()
		if err != nil {
			t.Fatal(err)
		}
		// Preserve provenance through the same JSON boundary as durable deliveries.
		data, err := json.Marshal(Delivery{Definition: rendered.Steps, Steps: []StepRecord{{ID: "review", Output: "must not be inserted"}}})
		if err != nil {
			t.Fatal(err)
		}
		var d Delivery
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatal(err)
		}
		got := renderAgentBrief(d, d.Definition[1])
		want := literal
		if each {
			want += " {{item.id}}"
		}
		if got != want {
			t.Fatalf("literal data changed: got %q want %q", got, want)
		}
	}
	// Genuine future references and for_each references in the definition must
	// still be rejected, even if callers submit forged runtime provenance.
	for _, each := range []bool{false, true} {
		a := testAutomation(t.TempDir())
		a.Steps = []Step{{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "{{steps.review.output}}", BriefContext: &briefContext{Template: "innocent"}}, {ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review"}}
		if each {
			a.Steps[0].ForEach = &ForEach{File: "items.json"}
		}
		if err := a.Normalize(); err == nil {
			t.Fatal("definition reference escaped validation")
		}
	}
	// Step findings themselves are not a second input template.
	step := Step{BriefContext: &briefContext{Template: "{{steps.review.output}} {{inputs.quote}}", Inputs: map[string]string{"quote": "literal"}}}
	got := renderAgentBrief(Delivery{Steps: []StepRecord{{ID: "review", Output: "{{inputs.quote}}"}}}, step)
	if !strings.HasPrefix(got, "{{inputs.quote}} literal") {
		t.Fatalf("findings re-rendered: %q", got)
	}
}
