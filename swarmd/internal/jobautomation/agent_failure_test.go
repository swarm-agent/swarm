package jobautomation

import "testing"

// Purpose: Runner.runAgent must preserve declared failure reasons, skip dependent
// work, and emit failure signals, while a review verdict is not execution failure.
// The real delivery runner/store with scripted AgentRunner outcomes is the
// narrowest layer proving these postconditions without a provider dependency.
func TestAgentStepDeclaredFailureAndReviewVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason, verdict, state string
	}{
		{"blocked", "blocked", "fetch failed: SELF_SIGNED_CERT_IN_CHAIN", "", StateFailed},
		{"failed", "failed", "required input missing", "", StateFailed},
		{"review block", StatusSucceeded, "", VerdictBlock, StateSucceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			h.agents.outcomes = []AgentOutcome{{Status: tc.status, Error: tc.reason, Verdict: tc.verdict}, {Status: StatusSucceeded}}
			h.process()
			h.automation(Automation{Name: "agent outcome", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
				{ID: "work", Type: StepAgent, Worker: "reviewer", Brief: "Do the work"},
				{ID: "next", Type: StepAgent, Worker: "finder", Brief: "Use the result"},
			}})
			h.results.results["run-1"] = workerResult(folder, "run-1")
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()
			d := h.deliveries("worker/coder-1/run-1")[0]
			if d.State != tc.state || d.Steps[0].State != tc.state || d.Steps[0].Reason != tc.reason {
				t.Fatalf("delivery: %+v", d)
			}
			if tc.state == StateFailed {
				if d.Steps[1].State != StateSkipped || len(h.agents.started) != 1 || len(h.signalsOf("automation.step.failed")) != 1 {
					t.Fatalf("failure did not stop dependent work or signal: %+v", d)
				}
			} else if d.Steps[0].Outcome["verdict"] != VerdictBlock || d.Steps[1].State != StateSucceeded || len(h.signalsOf("automation.step.failed")) != 0 {
				t.Fatalf("review verdict treated as failure: %+v", d)
			}
		})
	}
}
