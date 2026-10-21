package jobautomation

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: a reviewer allowed to fix (fixes "security") whose report
// could not be read (outcome verdict block, ReviewUnreadable) never has its
// fix commits applied, even a small fix that descends from the delivery
// head: the delivery head stays the coder's, fixes_applied is false, the
// fix commits are only listed, a later verdict-ok run step does not run, and
// the reopen step sends nothing, uses no round and blocks the delivery with
// "review unreadable: <reviewer job>".
// Threat: an unreadable report from a fixing reviewer being treated as an
// approval of its own commits (pushing reviewer code no verdict approved), or
// being sent to the coder as findings.
// Owners: Runner.runAgent, Runner.applyReviewerFixes (HeadVerifier,
// LineCounter over a real Git repository), Runner.runReopen and
// Runner.finish over the real Pebble store and signal feed. Stubbed: the
// reviewer job (recordingAgents: the model provider) and the session host.
func TestUnreadableFixingReviewNeverAppliesFixes(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	repo.write("upload.go", "package upload\n\nfunc Name(s string) string { return s }\n")
	jobHead := repo.commitFiles("coder: add the upload name")
	repo.write("upload.go", "package upload\n\nimport \"html\"\n\nfunc Name(s string) string { return html.EscapeString(s) }\n")
	smallFix := repo.commitFiles("reviewer: escape the upload name")
	h.runner.verify, h.runner.lines = gitVerifier, gitLineCounter
	writeScript(t, folder, "push.sh", "echo pushed > \"$PWD/pushed\"\n")

	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, ReviewUnreadable: true, Head: smallFix,
		Result: &Result{Git: &ResultGit{Branch: "agent/review-run", BaseCommit: jobHead, Head: smallFix, Worktree: filepath.Join(folder, "reviewer-checkout"),
			Commits: []ResultCommit{{SHA: smallFix, Subject: "reviewer: escape the upload name"}}}}}}
	h.process()
	loop := reviewLoop(folder, 2)
	loop.Steps[0].Fixes = FixesSecurity
	h.automation(loop)
	result := workerResult(folder, "run-1")
	result.Git.Head, result.Git.Worktree = jobHead, filepath.Join(folder, "coder-checkout")
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	list := h.deliveries("worker/coder-1/run-1")
	if len(list) != 1 {
		t.Fatalf("deliveries = %d", len(list))
	}
	d := list[0]
	if len(h.agents.started) != 1 || h.agents.started[0].Fixes != FixesSecurity {
		t.Fatalf("the reviewer job was not asked to allow fixes: %+v", h.agents.started)
	}
	review := d.Steps[0].Outcome
	if review["verdict"] != VerdictBlock || review["review_unreadable"] != true || review["fixes_applied"] != false {
		t.Fatalf("review outcome = %+v", review)
	}
	if _, moved := review["moved_head"]; moved {
		t.Fatalf("an unreadable review moved the head: %+v", review)
	}
	if _, ok := review["findings"]; ok {
		t.Fatalf("an unreadable review kept findings: %+v", review)
	}
	if commits, _ := review["fix_commits"].([]any); len(commits) != 1 || commits[0].(map[string]any)["sha"] != smallFix {
		t.Fatalf("fix_commits = %+v", review["fix_commits"])
	}
	if d.Head != jobHead {
		t.Fatalf("delivery head = %s, want the coder's %s (fix %s must not be applied)", d.Head, jobHead, smallFix)
	}
	reviewer, _ := review["job_id"].(string)
	want := ReasonReviewUnreadable + ": " + reviewer
	if reviewer == "" || d.State != StateBlocked || d.BlockedReason != want || d.Round != 0 {
		t.Fatalf("delivery = state %s reason %q round %d", d.State, d.BlockedReason, d.Round)
	}
	if fix := d.Steps[1]; fix.State != StateFailed || fix.Reason != want || fix.Outcome["followup"] != nil {
		t.Fatalf("reopen step = %+v", fix)
	}
	if d.Steps[2].State != StateSkipped {
		t.Fatalf("push after an unreadable fixing review = %s", d.Steps[2].State)
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed")); !os.IsNotExist(err) {
		t.Fatalf("push ran after an unreadable fixing review (stat err %v)", err)
	}
	if len(reopener.sent) != 0 {
		t.Fatalf("an unreadable fixing review sent %d follow-ups", len(reopener.sent))
	}
}
