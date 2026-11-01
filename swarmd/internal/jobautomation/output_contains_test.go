package jobautomation

import (
	"fmt"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a workflow has 1 to MaxSteps (40) steps, and the refusal names
// that bound; a condition's output_contains is checked when the workflow is
// saved: only with a status (never a verdict or alone), one line of literal
// text, non-blank, at most MaxOutputContainsBytes, and kept exactly as written
// (case-sensitive, never lowercased like status and verdict).
// Threat: a review loop that needs more than the old 20 steps refused; a
// condition that silently never matches (a verdict with output text, a
// multi-line or oversized needle) or one normalized into a different needle.
// Owner: NormalizeSteps (normalizeConditions), the definition-time boundary
// every surface (HTTP, Orchestrator, MCP, import) goes through.
func TestStepLimitAndOutputContainsAreValidated(t *testing.T) {
	if MaxSteps != 40 {
		t.Fatalf("MaxSteps = %d, want 40", MaxSteps)
	}
	agents := func(n int) []Step {
		steps := make([]Step, n)
		for i := range steps {
			steps[i] = Step{ID: fmt.Sprintf("review-%d", i+1), Type: StepAgent, Worker: "reviewer", Brief: "b"}
		}
		return steps
	}
	if err := NormalizeSteps(agents(MaxSteps)); err != nil {
		t.Fatalf("%d steps refused: %v", MaxSteps, err)
	}
	if err := NormalizeSteps(agents(21)); err != nil {
		t.Fatalf("21 steps (past the old bound) refused: %v", err)
	}
	err := NormalizeSteps(agents(MaxSteps + 1))
	if err == nil || !IsValidationError(err) || err.Error() != "an automation needs 1 to 40 steps" {
		t.Fatalf("%d steps: err = %v", MaxSteps+1, err)
	}

	run := Step{ID: "gate", Type: StepRun, Command: "gate.sh"}
	agent := Step{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b"}
	reopen := func(c Condition) []Step {
		return []Step{run, agent, {ID: "rebase-after-fix", Type: StepReopen, Message: "rebase", If: []Condition{c}}}
	}
	bad := map[string]Condition{
		"alone":        {Step: "gate", OutputContains: "rebase needed"},
		"with verdict": {Step: "review", Verdict: "block", OutputContains: "rebase needed"},
		"too long":     {Step: "gate", Status: "failed", OutputContains: strings.Repeat("x", MaxOutputContainsBytes+1)},
		"newline":      {Step: "gate", Status: "failed", OutputContains: "rebase\nneeded"},
		"carriage":     {Step: "gate", Status: "failed", OutputContains: "rebase\rneeded"},
		"nul":          {Step: "gate", Status: "failed", OutputContains: "rebase\x00"},
		"blank":        {Step: "gate", Status: "failed", OutputContains: "   "},
		"invalid utf8": {Step: "gate", Status: "failed", OutputContains: "\xff\xfe"},
	}
	for name, c := range bad {
		if err := NormalizeSteps(reopen(c)); err == nil || !IsValidationError(err) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	good := reopen(Condition{Step: "gate", Status: " FAILED ", OutputContains: "GATE FAILED: Rebase needed"})
	if err := NormalizeSteps(good); err != nil {
		t.Fatal(err)
	}
	if c := good[2].If[0]; c.Status != StateFailed || c.OutputContains != "GATE FAILED: Rebase needed" {
		t.Fatalf("normalized = %+v (output_contains must be kept as written)", c)
	}
	exact := reopen(Condition{Step: "gate", Status: "failed", OutputContains: strings.Repeat("x", MaxOutputContainsBytes)})
	if err := NormalizeSteps(exact); err != nil {
		t.Fatalf("output_contains at the bound refused: %v", err)
	}
}

// Requirement: a reopen step on {step, status: failed, output_contains} runs
// only when the failed step's reason, output or saved log tail holds that
// exact (case-sensitive) text, also when it is not the script's last line;
// otherwise it is skipped with a reason and nothing is sent. A reopen it
// sends is a rebase reopen (ReopenCause push_failed, IsRebaseReopen, counted
// in the job's rebase rounds), and its failure names the matched text.
// Threat (seen live): a gate re-run after a review fix failed "rebase
// needed"; the workflow could not tell that from a code failure, so it either
// had no step to send the work back or would send every failure back as a
// rebase.
// Owners: conditionsHold (StepEvidence, Store.StepLog), Runner.runReopen,
// ReopenCause, IsRebaseReopen, Store.ReopenRounds over the real Pebble store
// and host executor (the gate is a real script); only the job's session that
// accepts follow-ups is stubbed. The runner is the narrowest layer that runs
// or skips a step on an earlier step's output.
func TestOutputContainsReopenFiresOnlyOnItsText(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		wantSent bool
	}{
		// The rebase line is not the last line: it is only in the log tail.
		{name: "rebase needed in the log tail", script: "echo 'GATE FAILED: rebase needed: dev moved and changed files this job also changed: a.go'; echo 'gate done'; exit 1\n", wantSent: true},
		{name: "a code failure", script: "echo 'FAIL: TestUpload (0.01s)'; echo 'gate done'; exit 1\n"},
		{name: "another case", script: "echo 'GATE FAILED: Rebase Needed'; exit 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			session := &runCheckingSession{noRun: map[string]bool{}}
			h.runner.reopen = session
			folder := t.TempDir()
			writeScript(t, folder, "gate.sh", tc.script)
			h.process()
			saved := h.automation(Automation{Name: "gate then rebase", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
				{ID: "gate-fix", Type: StepRun, Command: "gate.sh", ContinueOnFailure: true},
				{ID: "rebase-after-fix", Type: StepReopen, Message: "The gate needs a rebase: {{steps.gate-fix.output}}", If: []Condition{{Step: "gate-fix", Status: StateFailed, OutputContains: "rebase needed"}}},
			}})
			const jobID = "worker/coder-1/run-1"
			h.endAt(folder, "", 0)
			d := h.deliveryFor(jobID, "")
			var reopen StepRecord
			for _, rec := range d.Steps {
				if rec.ID == "rebase-after-fix" {
					reopen = rec
				}
			}
			if !tc.wantSent {
				if reopen.State != StateSkipped || !strings.Contains(reopen.Reason, `do not contain "rebase needed"`) {
					t.Fatalf("reopen = %s %q, want skipped on its output_contains", reopen.State, reopen.Reason)
				}
				if len(session.sent) != 0 || d.State == StateSentBack || d.SentBack != nil {
					t.Fatalf("a failure without the text sent the work back: sent %d, delivery %s %+v", len(session.sent), d.State, d.SentBack)
				}
				if rounds, err := h.store.ReopenRounds(testAccount, jobID, saved.ID); err != nil || rounds.Total != 0 {
					t.Fatalf("rounds = %+v %v, want none", rounds, err)
				}
				return
			}
			if reopen.State != StateSucceeded || d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "rebase-after-fix" {
				t.Fatalf("reopen %s %q, delivery %s %+v: want sent_back by rebase-after-fix", reopen.State, reopen.Reason, d.State, d.SentBack)
			}
			if len(session.sent) != 1 || !strings.Contains(session.sent[0].Message, "The gate needs a rebase") {
				t.Fatalf("sent = %+v", session.sent)
			}
			cause, failure := ReopenCause(d, "rebase-after-fix")
			if cause != pebblestore.ProjectTaskReopenPushFailed || !strings.Contains(failure, "gate-fix failed") || !strings.Contains(failure, `output contains "rebase needed"`) {
				t.Fatalf("cause %q failure %q, want push_failed naming the matched text", cause, failure)
			}
			if !IsRebaseReopen(d, "rebase-after-fix") {
				t.Fatal("the reopen is not a rebase reopen")
			}
			if !strings.Contains(d.SentBack.Reason, `output contains "rebase needed"`) {
				t.Fatalf("sent back reason %q", d.SentBack.Reason)
			}
			rounds, err := h.store.ReopenRounds(testAccount, jobID, saved.ID)
			if err != nil || rounds.Total != 1 || rounds.Rebase != 1 {
				t.Fatalf("rounds = %+v %v, want one rebase round", rounds, err)
			}
		})
	}
}

// Requirement: a reopen's cause reflects the condition it ran on. A failed
// step's condition whose output_contains names a rebase is push_failed (the
// rebase cause), even after a review that blocked; the same failed-step
// condition without it stays check_failed, and a failed step outside the
// condition's step changes nothing.
// Threat: a rebase reopen labelled check_failed (an agent told to fix code
// that is fine, and its review rounds used up), or every check failure
// relabelled a rebase.
// Owner: ReopenCause and RebaseCondition, pure over a delivery record; the
// narrowest layer that classifies a reopen for every progress surface.
func TestOutputContainsReopenCauseIsRebase(t *testing.T) {
	delivery := func(c Condition) Delivery {
		return Delivery{
			Definition: []Step{
				{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b"},
				{ID: "gate-fix", Type: StepRun, Command: "gate.sh", ContinueOnFailure: true},
				{ID: "rebase-after-fix", Type: StepReopen, Message: "m", If: []Condition{c}},
			},
			Steps: []StepRecord{
				{ID: "review", Type: StepAgent, State: StateSucceeded, Outcome: map[string]any{"verdict": VerdictBlock}},
				{ID: "gate-fix", Type: StepRun, State: StateFailed, Reason: "exited with code 1: gate done"},
				{ID: "rebase-after-fix", Type: StepReopen, State: StateSucceeded},
			},
		}
	}
	d := delivery(Condition{Step: "gate-fix", Status: StateFailed, OutputContains: "rebase needed"})
	if cause, failure := ReopenCause(d, "rebase-after-fix"); cause != pebblestore.ProjectTaskReopenPushFailed || failure != `gate-fix failed: exited with code 1: gate done (output contains "rebase needed")` {
		t.Fatalf("rebase condition: %q %q", cause, failure)
	}
	if !IsRebaseReopen(d, "rebase-after-fix") {
		t.Fatal("rebase condition: not a rebase reopen")
	}
	d = delivery(Condition{Step: "gate-fix", Status: StateFailed})
	if cause, _ := ReopenCause(d, "rebase-after-fix"); cause != pebblestore.ProjectTaskReopenCheckFailed || IsRebaseReopen(d, "rebase-after-fix") {
		t.Fatalf("plain failed condition: cause %q, want check_failed and no rebase reopen", cause)
	}
	d = delivery(Condition{Step: "gate-fix", Status: StateFailed, OutputContains: "FAIL: TestUpload"})
	if cause, _ := ReopenCause(d, "rebase-after-fix"); cause != pebblestore.ProjectTaskReopenCheckFailed {
		t.Fatalf("output_contains without a rebase: cause %q, want check_failed", cause)
	}
	if RebaseCondition(Condition{Step: "gate-fix", Status: StateSucceeded, OutputContains: "rebase needed"}) {
		t.Fatal("a succeeded-step condition is a rebase condition")
	}
}
