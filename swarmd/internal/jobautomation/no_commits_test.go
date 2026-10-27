package jobautomation

import (
	"context"
	"encoding/json"
	"testing"
)

type roleAgents struct{ *recordingAgents }

func (a roleAgents) Role(context.Context, string, string) (string, error) { return "reviewer", nil }

// Purpose: Runner.skipEmptyReview must skip only positively empty reviews;
// Runner.runAgent must reject unknown heads instead of dispatching a reviewer.
// Committed work, role-based review detection, conditions and unrelated steps
// retain their contracts. The durable runner harness is the narrowest layer
// proving stored states and dispatches without provider execution.
func TestReviewNoCommits(t *testing.T) {
	for _, tc := range []struct {
		name, worker         string
		empty, unknown, skip bool
	}{
		{"empty", "reviewer", true, false, true},
		{"role", "worker-review", true, false, true},
		{"committed", "reviewer", false, false, false},
		{"unknown", "reviewer", false, true, false},
		{"non-reviewer", "finder", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.worker == "worker-review" {
				h.runner.agent = roleAgents{h.agents}
			}
			folder := t.TempDir()
			writeScript(t, folder, "notify.sh", "echo notified\n")
			h.process()
			h.automation(Automation{Name: "review", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
				{ID: "review", Type: StepAgent, Worker: tc.worker, Brief: "Review"},
				{ID: "verdict", Type: StepRun, Command: "notify.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
				{ID: "status", Type: StepRun, Command: "notify.sh", If: []Condition{{Step: "review", Status: StateSucceeded}}},
				{ID: "always", Type: StepRun, Command: "notify.sh"},
			}})
			result := workerResult(folder, "run-1")
			if tc.empty || tc.unknown {
				result.Git = nil
			}
			// JSON also proves the signal survives the persisted result contract.
			if tc.empty {
				if err := json.Unmarshal([]byte(`{"no_commits":true}`), result); err != nil {
					t.Fatal(err)
				}
			}
			h.results.results["run-1"] = result
			h.agents.outcomes = []AgentOutcome{{Status: StatusSucceeded, Verdict: VerdictOK}}
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()
			d := h.deliveries(result.JobID)[0]
			if tc.unknown {
				if d.State != StateFailed || d.Steps[0].State != StateFailed || d.Steps[0].Reason != "no head to review" || len(h.agents.started) != 0 {
					t.Fatalf("unknown head review = %+v; starts=%d", d, len(h.agents.started))
				}
				return
			}
			if d.State != StateSucceeded {
				t.Fatalf("delivery = %+v", d)
			}
			if tc.skip {
				if d.Steps[0].State != StateSkipped || d.Steps[0].Reason != "the job made no commits: nothing to review" || len(h.agents.started) != 0 || d.Steps[0].Attempts != 0 {
					t.Fatalf("review = %+v; starts=%d", d.Steps[0], len(h.agents.started))
				}
				if d.Steps[1].State != StateSkipped || d.Steps[2].State != StateSkipped || len(h.host.ran) != 1 {
					t.Fatalf("conditions = %+v; runs=%d", d.Steps, len(h.host.ran))
				}
			} else if d.Steps[0].State != StateSucceeded || len(h.agents.started) != 1 || len(h.host.ran) != 3 {
				t.Fatalf("unexpected skip: %+v", d.Steps)
			}
			if d.Steps[3].State != StateSucceeded {
				t.Fatal("unconditional run did not run")
			}
		})
	}
}
