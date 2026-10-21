package jobautomation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: a step's `if` runs it only when every condition on an earlier
// step's outcome holds; a verdict condition matches the verdict of the review
// the agent step's job recorded, and a missing verdict never matches. A step
// whose condition does not hold is skipped (not failed), the steps after it
// still run, and the delivery does not fail because of the skip.
// Threat: a delivery (a push) running after a reviewer said block, a review
// loop that never reaches its delivery step because a skipped step stopped
// the rest, or an absent verdict being read as approval.
// Owners: NormalizeSteps (normalizeConditions), conditionsHold and
// Runner.runDelivery over the real store and signal feed; only the agent
// job's end is scripted (recordingAgents), as the provider boundary.
func TestVerdictConditionRunsOrSkipsAStepAndSkipsDoNotStopLaterSteps(t *testing.T) {
	cases := []struct {
		name          string
		outcome       AgentOutcome
		wantPush      bool
		wantFixState  string
		wantPushState string
	}{
		{name: "ok runs the push and skips the block branch", outcome: AgentOutcome{Verdict: VerdictOK}, wantPush: true, wantFixState: StateSkipped, wantPushState: StateSucceeded},
		{name: "block skips the push and runs the block branch", outcome: AgentOutcome{Verdict: VerdictBlock, Findings: "1. high a.go:3 missing check"}, wantFixState: StateSucceeded, wantPushState: StateSkipped},
		{name: "no verdict matches neither", outcome: AgentOutcome{}, wantFixState: StateSkipped, wantPushState: StateSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			writeScript(t, folder, "on-block.sh", "echo blocked >> \"$PWD/block.log\"\n")
			writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
			writeScript(t, folder, "notify.sh", "echo notified >> \"$PWD/notified\"\n")
			h.agents.outcomes = []AgentOutcome{tc.outcome}
			h.process()
			h.automation(Automation{Name: "review then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
				{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review it"},
				{ID: "on-block", Type: StepRun, Command: "on-block.sh", If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
				{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "review", Verdict: "OK"}, {Step: "review", Status: StateSucceeded}}},
				{ID: "notify", Type: StepRun, Command: "notify.sh"},
			}})
			h.results.results["run-1"] = workerResult(folder, "run-1")
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()

			d := h.deliveries("worker/coder-1/run-1")[0]
			states := map[string]StepRecord{}
			for _, s := range d.Steps {
				states[s.ID] = s
			}
			if states["on-block"].State != tc.wantFixState || states["push"].State != tc.wantPushState {
				t.Fatalf("on-block %s push %s (reasons %q / %q)", states["on-block"].State, states["push"].State, states["on-block"].Reason, states["push"].Reason)
			}
			if states["push"].State == StateSkipped && !strings.HasPrefix(states["push"].Reason, "condition not met") {
				t.Fatalf("skip reason = %q", states["push"].Reason)
			}
			// A skipped step never stops the steps after it.
			if states["notify"].State != StateSucceeded {
				t.Fatalf("notify after a skipped step = %s", states["notify"].State)
			}
			if d.State != StateSucceeded {
				t.Fatalf("delivery = %s, want succeeded (skips are not failures)", d.State)
			}
			_, err := os.Stat(filepath.Join(folder, "pushed"))
			if pushed := err == nil; pushed != tc.wantPush {
				t.Fatalf("pushed = %v, want %v", pushed, tc.wantPush)
			}
			if tc.outcome.Verdict != "" {
				if got := states["review"].Outcome["verdict"]; got != tc.outcome.Verdict {
					t.Fatalf("recorded verdict = %v", got)
				}
			} else if _, ok := states["review"].Outcome["verdict"]; ok {
				t.Fatal("a job without a recorded review got a verdict")
			}
			if tc.outcome.Findings != "" && states["review"].Outcome["findings"] != tc.outcome.Findings {
				t.Fatalf("findings = %v", states["review"].Outcome["findings"])
			}
		})
	}
}

// Requirement: conditions stay small and are validated when the automation
// is set: each names an earlier step and has exactly one of status
// (succeeded|failed) or verdict (ok|block); a verdict needs an agent step.
// Threat: a condition on a later or unknown step that silently never holds.
// Owner: NormalizeSteps.
func TestConditionsAreValidated(t *testing.T) {
	agent := Step{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b"}
	run := Step{ID: "check", Type: StepRun, Command: "check.sh"}
	bad := map[string][]Step{
		"unknown step":          {agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "nope", Status: "succeeded"}}}},
		"later step":            {{ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "review", Verdict: "ok"}}}, agent},
		"both status, verdict":  {agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "review", Status: "failed", Verdict: "ok"}}}},
		"neither":               {agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "review"}}}},
		"bad status":            {agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "review", Status: "skipped"}}}},
		"bad verdict":           {agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "review", Verdict: "approve"}}}},
		"verdict of a run step": {run, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "check", Verdict: "ok"}}}},
	}
	for name, steps := range bad {
		if err := NormalizeSteps(steps); err == nil || !IsValidationError(err) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	good := []Step{run, agent, {ID: "x", Type: StepRun, Command: "x.sh", If: []Condition{{Step: "check", Status: "FAILED"}, {Step: "review", Verdict: " Block "}}}}
	if err := NormalizeSteps(good); err != nil {
		t.Fatal(err)
	}
	if c := good[2].If; c[0].Status != StateFailed || c[1].Verdict != VerdictBlock {
		t.Fatalf("normalized = %+v", c)
	}
}

// Requirement: FindingsOf returns a reviewer report's FINDINGS section
// verbatim (what a reopen step sends) and nothing for None.
// Owner: FindingsOf.
func TestFindingsOfReadsTheFindingsSectionVerbatim(t *testing.T) {
	report := "VERDICT: BLOCK\nSUMMARY: two gaps\nFINDINGS: 1. high api/x.go:12 the limit is not checked\n2. medium api/x_test.go:40 the test asserts only the status\nTESTS RUN: go test ./api (exit 0)"
	if got := FindingsOf(report); got != "1. high api/x.go:12 the limit is not checked\n2. medium api/x_test.go:40 the test asserts only the status" {
		t.Fatalf("findings = %q", got)
	}
	if got := FindingsOf("VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none"); got != "" {
		t.Fatalf("None = %q", got)
	}
}
