package jobautomation

import (
	"strings"
	"testing"
)

// Requirement: a reviewer step reviews the delivery's newest work. When the
// triggering job committed nothing but an earlier produce step's job
// committed the result, the reviewer starts with that job's head and job;
// when nothing before it committed (a produce step whose job left no
// commit), it is still refused "no head to review" and no job starts.
// Threat: reviewed work never reviewed (the produce, review, deliver loop of
// a research task failed every time), or a reviewer started with no commit,
// reviewing nothing and passing it.
// Owners: Runner.runAgent's head selection, latestWorkJob/latestWorkHead over
// the real store and signal feed; the agents and the deliverer are the
// stubbed daemon boundary. The runner decides the head a reviewer gets.
func TestReviewerReviewsTheProducedHeadOrIsRefused(t *testing.T) {
	flow := func(folder string) Automation {
		return Automation{Name: "audit", Scope: Scope{Folder: folder, Role: "finder"}, Enabled: true,
			Result: &ResultSpec{Kind: "file", Path: "reports/audit.md", Describe: "An audit."},
			Steps: []Step{
				{ID: "write", Type: StepAgent, Worker: "coder", Brief: "Write the report.", Produce: true},
				{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Check the report."},
			}}
	}
	for _, tc := range []struct {
		name, head string
	}{{"committed", strings.Repeat("c", 40)}, {"nothing committed", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.runner.deliver = &recordingDeliverer{produced: Deliverable{Kind: DeliverableFile, Source: SourceGit, Head: tc.head, SHA256: strings.Repeat("d", 64), Size: 10}}
			h.agents.outcomes = []AgentOutcome{{Head: tc.head}, {Verdict: VerdictOK}}
			h.process()
			folder := t.TempDir()
			h.automation(flow(folder))
			result := taskResult(folder, "task-1", "finder")
			result.Git = nil
			h.results.results["task-1"] = result
			h.taskEnd("task-1", "", "succeeded", "task")
			h.process()
			d := h.deliveries("task/proj-1/task-1")[0]
			if tc.head == "" {
				if len(h.agents.started) != 1 || d.State != StateFailed || d.Steps[1].Reason != "no head to review" {
					t.Fatalf("review with nothing committed: started %d, delivery %s, review %+v", len(h.agents.started), d.State, d.Steps[1])
				}
				return
			}
			if len(h.agents.started) != 2 || d.State != StateSucceeded {
				t.Fatalf("review of the produced head: started %d, delivery %s %+v", len(h.agents.started), d.State, d.Steps)
			}
			review := h.agents.started[1]
			if review.Head != tc.head || review.ReviewJob == "" || review.ReviewJob != d.Steps[0].Outcome["job_id"] {
				t.Fatalf("reviewer started at head %q reviewing %q, want the produced head %s of %v", review.Head, review.ReviewJob, tc.head, d.Steps[0].Outcome["job_id"])
			}
		})
	}
}
