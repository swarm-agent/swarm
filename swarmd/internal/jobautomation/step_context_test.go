package jobautomation

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Purpose: fresh steps exchange bounded durable evidence, not transcripts.
// Runner.runAgent and NormalizeSteps own this boundary; the real-store runner
// harness is the narrowest layer proving dispatch context and recorded outcomes.
func TestFreshStepContext(t *testing.T) {
	h := newHarness(t)
	h.runner.verify = func(context.Context, string, HeadRef, HeadRef) (bool, error) { return true, nil }
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "close this\nand this", Handoff: "reviewed"}, {Head: strings.Repeat("c", 40), Result: &Result{Report: "changed; tested; open"}}, {Verdict: VerdictOK}}
	h.process()
	folder := t.TempDir()
	h.automation(Automation{Name: "fresh", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review"},
		{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "{{steps.review.output}} {{steps.review.verdict}} {{steps.review.head}} {{steps.review.handoff}}"},
		{ID: "again", Type: StepAgent, Worker: "reviewer", Since: "review", Brief: "{{steps.fix.handoff}}"},
	}})
	h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
	h.taskEnd("task-1", "", "succeeded", "task")
	h.process()
	d := h.deliveries("task/proj-1/task-1")[0]
	if len(h.agents.started) != 3 {
		t.Fatalf("started: %d, steps: %+v", len(h.agents.started), d.Steps)
	}
	if d.Steps[0].Output != "close this\nand this" || !strings.Contains(h.agents.started[1].Brief, "close this\nand this block") {
		t.Fatalf("findings lost: %+v", d.Steps)
	}
	req := h.agents.started[2]
	if req.Head != strings.Repeat("c", 40) || req.ReviewBase != d.Steps[0].Outcome["head"] || req.PreviousFindings != d.Steps[0].Output || !strings.HasPrefix(req.Brief, "changed; tested; open") || d.Steps[2].Outcome["base"] != req.ReviewBase {
		t.Fatalf("delta context: %+v, %+v", req, d.Steps[2])
	}
	// Missing/stale review evidence must fail closed, not silently do a full review.
	d.Steps[0].Outcome["review_unreadable"] = true
	if _, _, err := sinceContext(d, "review"); err == nil {
		t.Fatal("accepted unreadable review")
	}
}

// Purpose: NormalizeSteps rejects future context references before dispatch;
// rendering is single-pass and bounded at the workflow substitution boundary.
func TestFreshStepPlaceholderValidationAndBounds(t *testing.T) {
	for _, field := range []string{"output", "verdict", "head", "handoff"} {
		steps := []Step{{ID: "first", Type: StepAgent, Worker: "coder", Brief: "{{steps.later." + field + "}}"}, {ID: "later", Type: StepAgent, Worker: "reviewer", Brief: "review"}}
		if NormalizeSteps(steps) == nil {
			t.Fatalf("accepted future %s", field)
		}
	}
	d := Delivery{Steps: []StepRecord{{ID: "review", Output: strings.Repeat("x", 20000)}}}
	if got := agentBrief(d, "{{steps.review.output}}"); len(got) > maxFindingsBytes {
		t.Fatalf("unbounded: %d", len(got))
	}
}

// Purpose: durable per-job reservations bound fresh correction steps across
// retries/deliveries; Store.reserveFreshFix is the atomic admission authority.
func TestFreshFixRoundBound(t *testing.T) {
	h := newHarness(t)
	d := Delivery{ID: "delivery", Account: "account", AutomationID: "flow", JobID: "task/p/t"}
	for i := 0; i < MaxRounds; i++ {
		for retry := 0; retry < 2; retry++ {
			ok, err := h.runner.store.reserveFreshFix(d, fmt.Sprint(i))
			if err != nil || !ok {
				t.Fatalf("reserve %d: %v %v", i, ok, err)
			}
		}
	}
	d.ID = "next-delivery"
	if ok, err := h.runner.store.reserveFreshFix(d, "fix"); err != nil || ok {
		t.Fatalf("limit: %v %v", ok, err)
	}
	d.JobID = "task/p/other"
	if ok, err := h.runner.store.reserveFreshFix(d, "fix"); err != nil || !ok {
		t.Fatalf("other job: %v %v", ok, err)
	}
}
