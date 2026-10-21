package jobautomation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement (the review loop a user composes from primitives alone,
// docs/automations.md "Example: a review loop"): a coder job succeeds; an
// agent step starts a reviewer job for that exact head; on block a reopen
// step sends its findings into the coder job's original session; the
// follow-up's end triggers the automation again (round 1) and a new reviewer
// job reviews the new head; on ok the run step delivers, getting the final
// head as SWARM_HEAD and as the imported ref.
// Threat: a loop that reviews a stale head, pushes before an ok, pushes the
// pre-fix head, or loses the round count.
// Owners: Runner (consume, runDelivery, runAgent, runReopen, runScript) over
// the real Pebble store and signal feed. Stubbed boundaries only: the model
// provider behind the coder and reviewer jobs (recordingAgents scripts each
// reviewer job's recorded verdict), the job's session that accepts the
// follow-up (sessionReopener) and the run step's host executor (recordingHost).
func TestReviewLoopAcceptanceBlockReopenThenOkDeliversTheFinalHead(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "deliver/push.sh", "echo \"$SWARM_HEAD $SWARM_JOB_REF round=$SWARM_ROUND\" >> \"$PWD/pushed\"\necho pushed \"$SWARM_HEAD\"\n")
	var imported []string
	h.runner.head = func(_ context.Context, _ string, r Result) (string, error) {
		imported = append(imported, r.Git.Head)
		return "refs/swarm/jobs/" + r.RunID, nil
	}
	findings := "1. high internal/upload/upload.go:42 the per-minute limit is never checked\n2. medium internal/upload/upload_test.go:10 the report says go test passed, but no go test ran"
	fixed := strings.Repeat("d", 40)
	h.agents.outcomes = []AgentOutcome{
		{Verdict: VerdictBlock, Findings: findings},
		// OK with nothing to fix: the fixing reviewer's checkout, as Wait
		// reads it, is still at the reviewed head with no fix commits.
		{Verdict: VerdictOK, Result: &Result{Git: &ResultGit{Branch: "agent/review-run-review-2", BaseCommit: fixed, Head: fixed}}},
	}
	h.process()
	// The exact automation of the docs' example.
	h.automation(Automation{Name: "review loop", Scope: Scope{Folder: folder, Worker: "coder-1"}, When: WhenSucceeded, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Fixes: FixesSecurity, Brief: "Review this job's exact head against its brief."},
		{ID: "fix", Type: StepReopen, From: "review", MaxRounds: 2, Message: "The reviewer blocked your work. Its findings:", If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "deliver/push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}})

	// 1. The coder job succeeds with its first head.
	first := workerResult(folder, "run-1")
	firstHead := first.Git.Head
	h.results.results["run-1"] = first
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	// 2. A reviewer job reviews that exact head.
	if len(h.agents.started) != 1 || h.agents.started[0].Worker != "reviewer" || h.agents.started[0].Result.Git.Head != firstHead || h.agents.started[0].Fixes != FixesSecurity {
		t.Fatalf("first review = %+v", h.agents.started)
	}
	round0 := h.deliveries("worker/coder-1/run-1")[0]
	if round0.Round != 0 || round0.Steps[0].Outcome["verdict"] != VerdictBlock || round0.Steps[2].State != StateSkipped {
		t.Fatalf("round 0 = %+v", round0)
	}
	// 3. Block: the findings go into the coder job's own session, verbatim.
	if len(reopener.sent) != 1 || reopener.sent[0].JobID != "worker/coder-1/run-1" ||
		reopener.sent[0].Message != "The reviewer blocked your work. Its findings:\n\n"+findings+"\n\n"+ReopenClosing {
		t.Fatalf("reopen = %+v", reopener.sent)
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed")); !os.IsNotExist(err) || len(imported) != 0 {
		t.Fatalf("pushed before an ok (imported %v)", imported)
	}

	// The coder fixes it in the same worktree: the follow-up ends with a new head.
	h.results.results["run-1"].Git.Head = fixed
	h.results.results["run-1"].Git.Commits = append(h.results.results["run-1"].Git.Commits, ResultCommit{SHA: fixed, Subject: "check the limit"})
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()

	// A new reviewer job reviews the new head, in round 1.
	if len(h.agents.started) != 2 || h.agents.started[1].Result.Git.Head != fixed || h.agents.started[1].Result.Followup != "followup-1" {
		t.Fatalf("second review = %+v", h.agents.started[1:])
	}
	var round1 Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-1" {
			round1 = d
		}
	}
	// 5. Ok: no reopen, and the run step delivers the final head.
	if round1.Round != 1 || round1.State != StateSucceeded || round1.Steps[1].State != StateSkipped || round1.Steps[2].State != StateSucceeded {
		t.Fatalf("round 1 = %+v", round1)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("an ok verdict reopened the job: %d follow-ups", len(reopener.sent))
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(folder, "pushed"))); got != fixed+" refs/swarm/jobs/run-1 round=1" {
		t.Fatalf("the run step got %q, want the final head %s", got, fixed)
	}
	if round1.Steps[2].Output != "pushed "+fixed || len(imported) != 1 || imported[0] != fixed {
		t.Fatalf("push output = %q, imported %v", round1.Steps[2].Output, imported)
	}
}

// Requirement: with no automation, a job's end and its follow-ups review,
// reopen and deliver nothing, even with every primitive wired.
// Threat: a built-in review chain or push appearing without the person
// setting one up.
// Owner: Runner.consume.
func TestNoAutomationNothingReviewsReopensOrDelivers(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	h.runner.verify = gitVerifier
	folder := t.TempDir()
	writeScript(t, folder, "deliver/push.sh", "touch \"$PWD/pushed\"\n")
	h.process()
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.results.results["run-1"].Role = "reviewer"
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()
	if len(h.agents.started) != 0 || len(reopener.sent) != 0 || len(h.host.ran) != 0 || len(h.deliveries("worker/coder-1/run-1")) != 0 {
		t.Fatalf("without automations: agents %d reopens %d runs %d", len(h.agents.started), len(reopener.sent), len(h.host.ran))
	}
	if sigs := h.signalsOf("automation"); len(sigs) != 0 {
		t.Fatalf("automation signals without automations: %+v", sigs)
	}
}
