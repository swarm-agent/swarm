package jobautomation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gitLineCounter is the LineCounter contract checked with real Git in the
// folder (the daemon's JobHeadChangedLines also imports from the checkouts
// first; here every commit is already in the folder).
func gitLineCounter(_ context.Context, folder string, base, head HeadRef) (int, error) {
	cmd := exec.Command("git", "diff", "--numstat", base.Commit, head.Commit, "--")
	cmd.Dir = folder
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		removed, _ := strconv.Atoi(fields[1])
		total += added + removed
	}
	return total, nil
}

func (r *gitRepo) write(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", name)
}

// commitFiles commits the staged files (never empty) and returns the head.
func (r *gitRepo) commitFiles(msg string) string {
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// Requirement: an agent step with fixes "security" asks for a reviewer that
// may commit (AgentRequest.Fixes); its commits move the delivery head only
// when its verdict is OK, its head descends from the delivery head and the
// commits change at most 80 lines (added plus removed); a later run step gets
// the moved head. Over 80 lines the step fails with "reviewer fixes too
// large" and the head stays; a BLOCK verdict with commits keeps the head and
// the step's verdict. The reviewer's fix commits are recorded on the step.
// Threat: a "reviewer" rewriting the job under the guise of a fix, or fixes
// it also blocked being pushed; enforced in code, not only in the prompt.
// Owners: Runner.runAgent and Runner.applyReviewerFixes (HeadVerifier,
// LineCounter), stepEnv, over a real Git repository, the real Pebble store
// and signal feed. Stubbed: the reviewer job itself (recordingAgents: the
// model provider) and the run step's host executor (recordingHost).
func TestReviewerFixesMoveTheHeadOnlyWithOKAndWithinTheLimit(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	repo.write("upload.go", "package upload\n\nfunc Name(s string) string { return s }\n")
	jobHead := repo.commitFiles("coder: add the upload name")
	// A small security fix on top of the job's head: 4 changed lines.
	repo.write("upload.go", "package upload\n\nimport \"html\"\n\nfunc Name(s string) string { return html.EscapeString(s) }\n")
	smallFix := repo.commitFiles("reviewer: escape the upload name")
	// A large "fix" on top of the job's head: 81 added lines.
	repo.git("checkout", "-q", "-b", "large", jobHead)
	repo.write("big.go", "package upload\n"+strings.Repeat("// line\n", 80))
	largeFix := repo.commitFiles("reviewer: rewrite")
	// A fix the reviewer committed while blocking.
	repo.git("checkout", "-q", "-b", "blocked", jobHead)
	repo.write("upload.go", "package upload\n\nfunc Name(s string) string { return \"\" }\n")
	blockedFix := repo.commitFiles("reviewer: partial fix")

	h.runner.verify, h.runner.lines = gitVerifier, gitLineCounter
	var imported []string
	h.runner.head = func(_ context.Context, _ string, r Result) (string, error) {
		imported = append(imported, r.Git.Head)
		return "refs/swarm/jobs/" + r.RunID, nil
	}
	writeScript(t, folder, "deliver/push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed-heads\"\n")
	reviewerOutcome := func(verdict, head, subject string) AgentOutcome {
		return AgentOutcome{Verdict: verdict, Head: head, Findings: "1. FIXED high upload.go:5 the name was not escaped",
			Result: &Result{Git: &ResultGit{Branch: "agent/review-run", BaseCommit: jobHead, Head: head, Worktree: filepath.Join(folder, "reviewer-checkout"),
				Commits: []ResultCommit{{SHA: head, Subject: subject}}}}}
	}
	h.agents.outcomes = []AgentOutcome{
		reviewerOutcome(VerdictOK, smallFix, "reviewer: escape the upload name"),
		reviewerOutcome(VerdictOK, largeFix, "reviewer: rewrite"),
		reviewerOutcome(VerdictBlock, blockedFix, "reviewer: partial fix"),
	}
	h.process()
	h.automation(Automation{Name: "review then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "wkr_custom_reviewer", Brief: "review", Fixes: FixesSecurity},
		{ID: "push", Type: StepRun, Command: "deliver/push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}})
	end := func(runID string) Delivery {
		t.Helper()
		result := workerResult(folder, runID)
		result.Git.Head, result.Git.Worktree = jobHead, filepath.Join(folder, "coder-checkout")
		h.results.results[runID] = result
		h.jobEnd(runID, "", "succeeded", "direct")
		h.process()
		list := h.deliveries("worker/coder-1/" + runID)
		if len(list) != 1 {
			t.Fatalf("deliveries for %s = %d", runID, len(list))
		}
		return list[0]
	}

	// OK with a 4-line fix: the head moves and the run step gets it.
	d := end("run-ok")
	if len(h.agents.started) != 1 || h.agents.started[0].Fixes != FixesSecurity {
		t.Fatalf("the reviewer job was not asked to allow fixes: %+v", h.agents.started)
	}
	if d.State != StateSucceeded || d.Head != smallFix || d.Steps[0].State != StateSucceeded || d.Steps[1].State != StateSucceeded {
		t.Fatalf("ok delivery = state %s head %s steps %+v", d.State, d.Head, d.Steps)
	}
	out := d.Steps[0].Outcome
	if out["fixes_applied"] != true || fmt.Sprint(out["fix_lines"]) != "4" {
		t.Fatalf("ok outcome = %+v", out)
	}
	if moved, _ := out["moved_head"].(map[string]any); moved["from"] != jobHead || moved["to"] != smallFix {
		t.Fatalf("moved_head = %+v", out["moved_head"])
	}
	if commits, _ := out["fix_commits"].([]any); len(commits) != 1 || commits[0].(map[string]any)["sha"] != smallFix {
		t.Fatalf("fix_commits = %+v", out["fix_commits"])
	}
	pushed := strings.Fields(readFile(t, filepath.Join(folder, "pushed-heads")))
	if len(pushed) != 1 || pushed[0] != smallFix || len(imported) != 1 || imported[0] != smallFix {
		t.Fatalf("the run step got %v (imported %v), want the reviewer's head %s", pushed, imported, smallFix)
	}
	if d.Result.Git.Head != jobHead {
		t.Fatal("the job's own result was rewritten")
	}

	// OK with 81 changed lines: the step fails, the head stays, nothing is pushed.
	d = end("run-large")
	if d.State != StateFailed || d.Head != jobHead || d.Steps[0].State != StateFailed || d.Steps[0].Reason != ReasonFixesTooLarge {
		t.Fatalf("large delivery = state %s head %s step %+v", d.State, d.Head, d.Steps[0])
	}
	if d.Steps[1].State != StateSkipped || d.Steps[0].Attempts != 1 || d.Steps[0].Outcome["fixes_applied"] != false || fmt.Sprint(d.Steps[0].Outcome["fix_lines"]) != "81" {
		t.Fatalf("after too large fixes: push %s attempts %d outcome %+v", d.Steps[1].State, d.Steps[0].Attempts, d.Steps[0].Outcome)
	}

	// BLOCK with commits: the head stays, the verdict stands, nothing is pushed.
	d = end("run-block")
	if d.Head != jobHead || d.Steps[0].State != StateSucceeded || d.Steps[0].Outcome["verdict"] != VerdictBlock || d.Steps[0].Outcome["fixes_applied"] != false {
		t.Fatalf("block delivery = head %s step %+v", d.Head, d.Steps[0])
	}
	if _, moved := d.Steps[0].Outcome["moved_head"]; moved || d.Steps[1].State != StateSkipped {
		t.Fatalf("a BLOCK verdict moved the head or pushed: %+v / %s", d.Steps[0].Outcome, d.Steps[1].State)
	}
	if pushed := strings.Fields(readFile(t, filepath.Join(folder, "pushed-heads"))); len(pushed) != 1 || len(imported) != 1 {
		t.Fatalf("pushes after the refused fixes = %v (imported %v)", pushed, imported)
	}
}

// Requirement: fixes is opt-in per agent step, only "security", only for a
// reviewer worker; run and reopen steps cannot carry it.
// Threat: a coder or finder step, or a typo'd value, read as permission for
// a writable reviewer.
// Owner: NormalizeSteps (normalizeAgent, normalizeRun, normalizeReopen).
func TestFixesIsOnlyForAReviewerAgentStep(t *testing.T) {
	// Native reviewers and explicit reviewer worker IDs may opt into fixes.
	if err := NormalizeSteps([]Step{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review", Fixes: FixesSecurity}}); err != nil {
		t.Fatalf("native reviewer fixes = %v", err)
	}
	if err := NormalizeSteps([]Step{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review", Fixes: FixesSecurity, Checkout: CheckoutSame}}); err == nil {
		t.Fatal("fixes accepted in a borrowed checkout")
	}
	ok := []Step{{ID: "review", Type: StepAgent, Worker: "wkr_reviewer", Brief: "review", Fixes: " Security "}}
	if err := NormalizeSteps(ok); err != nil || ok[0].Fixes != FixesSecurity {
		t.Fatalf("reviewer fixes = %q %v", ok[0].Fixes, err)
	}
	// A worker id is checked against the account's workers by the API.
	if err := NormalizeSteps([]Step{{ID: "review", Type: StepAgent, Worker: "wkr_123", Brief: "review", Fixes: FixesSecurity}}); err != nil {
		t.Fatalf("worker id with fixes = %v", err)
	}
	for _, bad := range [][]Step{
		{{ID: "review", Type: StepAgent, Worker: "coder", Brief: "review", Fixes: FixesSecurity}},
		{{ID: "review", Type: StepAgent, Worker: "finder", Brief: "review", Fixes: FixesSecurity}},
		{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review", Fixes: "all"}},
		{{ID: "push", Type: StepRun, Command: "deliver/push.sh", Fixes: FixesSecurity}},
		{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review"}, {ID: "fix", Type: StepReopen, From: "review", Fixes: FixesSecurity}},
	} {
		err := NormalizeSteps(bad)
		if err == nil || !IsValidationError(err) {
			t.Fatalf("steps %+v = %v, want a refusal", bad, err)
		}
	}
	if err := NonReviewerFixesError("review", "wkr_1", "coder"); !IsValidationError(err) || !strings.Contains(err.Error(), "only for a reviewer worker") {
		t.Fatalf("NonReviewerFixesError = %v", err)
	}
}

// Requirement: with fixes, an OK verdict may mean "I fixed it", so it fails
// closed unless the reviewer's own checkout was read: a read error, a missing
// result or head, a head that moved without readable fix commits, or a
// branch not started at the delivery head fail the step with "reviewer fixes
// unreadable", keep the delivery head and skip the run step gated on OK. An
// OK verdict whose checkout was read with no fix commits (head still the
// reviewed head) succeeds and the run step gets the job's own head.
// Threat (review finding, fails open): an OK whose fix commits were lost
// (dropped result error, failed get_changes) pushing the coder's unfixed
// head while the reviewer's findings say FIXED.
// Owners: Runner.applyReviewerFixes (via runAgent), over a real Git
// repository, the real Pebble store and signal feed. Stubbed: the reviewer
// job (recordingAgents: the model provider) and the run step's host executor (recordingHost).
func TestReviewerOKWithUnreadableFixesFailsClosed(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	repo.write("upload.go", "package upload\n\nfunc Name(s string) string { return s }\n")
	jobHead := repo.commitFiles("coder: add the upload name")
	repo.write("upload.go", "package upload\n\nimport \"html\"\n\nfunc Name(s string) string { return html.EscapeString(s) }\n")
	fixHead := repo.commitFiles("reviewer: escape the upload name")
	other := strings.Repeat("c", 40) // not the delivery head

	h.runner.verify, h.runner.lines = gitVerifier, gitLineCounter
	var pushedRefs []string
	h.runner.head = func(_ context.Context, _ string, r Result) (string, error) {
		pushedRefs = append(pushedRefs, r.Git.Head)
		return "refs/swarm/jobs/" + r.RunID, nil
	}
	writeScript(t, folder, "deliver/push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed-heads\"\n")
	worktree := filepath.Join(folder, "reviewer-checkout")
	git := func(base, head string, commits ...ResultCommit) *Result {
		return &Result{Git: &ResultGit{Branch: "agent/review-run", BaseCommit: base, Head: head, Worktree: worktree, Commits: commits}}
	}
	fixCommit := ResultCommit{SHA: fixHead, Subject: "reviewer: escape the upload name"}
	findings := "1. FIXED high upload.go:5 the name was not escaped"
	cases := []struct {
		name    string
		outcome AgentOutcome
	}{
		{"result error", AgentOutcome{Verdict: VerdictOK, Findings: findings, ResultError: "the reviewer's checkout head could not be read"}},
		{"no result", AgentOutcome{Verdict: VerdictOK, Findings: findings}},
		{"no git", AgentOutcome{Verdict: VerdictOK, Findings: findings, Result: &Result{Report: "VERDICT: OK"}}},
		{"head moved without commits", AgentOutcome{Verdict: VerdictOK, Findings: findings, Result: git(jobHead, fixHead)}},
		{"branch not at the delivery head", AgentOutcome{Verdict: VerdictOK, Findings: findings, Result: git(other, fixHead, fixCommit)}},
		{"commits without a moved head", AgentOutcome{Verdict: VerdictOK, Findings: findings, Result: git(jobHead, jobHead, fixCommit)}},
	}
	for _, c := range cases {
		h.agents.outcomes = append(h.agents.outcomes, c.outcome)
	}
	// Last: OK, checkout read, nothing fixed.
	h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictOK, Result: git(jobHead, jobHead)})
	h.process()
	h.automation(Automation{Name: "review then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "wkr_custom_reviewer", Brief: "review", Fixes: FixesSecurity},
		{ID: "push", Type: StepRun, Command: "deliver/push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}})
	end := func(runID string) Delivery {
		t.Helper()
		result := workerResult(folder, runID)
		result.Git.Head = jobHead
		h.results.results[runID] = result
		h.jobEnd(runID, "", "succeeded", "direct")
		h.process()
		list := h.deliveries("worker/coder-1/" + runID)
		if len(list) != 1 {
			t.Fatalf("deliveries for %s = %d", runID, len(list))
		}
		return list[0]
	}
	for i, c := range cases {
		d := end("run-unreadable-" + strconv.Itoa(i))
		if d.State != StateFailed || d.Head != jobHead || d.Steps[0].State != StateFailed || d.Steps[0].Reason != ReasonFixesUnreadable {
			t.Fatalf("%s: delivery = state %s head %s step %+v", c.name, d.State, d.Head, d.Steps[0])
		}
		if d.Steps[1].State != StateSkipped || d.Steps[0].Outcome["fixes_applied"] != false {
			t.Fatalf("%s: push %s outcome %+v", c.name, d.Steps[1].State, d.Steps[0].Outcome)
		}
		if _, moved := d.Steps[0].Outcome["moved_head"]; moved {
			t.Fatalf("%s: the head was moved", c.name)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed-heads")); !os.IsNotExist(err) || len(pushedRefs) != 0 {
		t.Fatalf("an unreadable OK pushed something: %v %v", err, pushedRefs)
	}

	d := end("run-nothing-fixed")
	if d.State != StateSucceeded || d.Head != jobHead || d.Steps[0].Outcome["fixes_applied"] != false || fmt.Sprint(d.Steps[0].Outcome["fix_lines"]) != "0" {
		t.Fatalf("nothing fixed: state %s head %s outcome %+v", d.State, d.Head, d.Steps[0].Outcome)
	}
	if pushed := strings.Fields(readFile(t, filepath.Join(folder, "pushed-heads"))); len(pushed) != 1 || pushed[0] != jobHead {
		t.Fatalf("nothing fixed: pushed %v, want the job's head", pushed)
	}
}

// Requirement: a fixing reviewer branches at the reviewed job's own head, so
// when an earlier agent step already moved the delivery head the fixing step
// is refused before its job starts (its fixes could never descend from the
// moved head) and the head stays where the earlier step put it.
// Threat: a reviewer job spent (and its fixes thrown away) on a branch that
// can never be applied, or fixes applied on top of a head they never saw.
// Owner: Runner.runAgent. Stubbed as above.
func TestFixingReviewerAfterAMovedHeadIsNotStarted(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	repo.write("upload.go", "package upload\n")
	jobHead := repo.commitFiles("coder")
	repo.write("upload.go", "package upload\n\n// formatted\n")
	formatted := repo.commitFiles("formatter")
	h.runner.verify, h.runner.lines = gitVerifier, gitLineCounter
	h.agents.outcomes = []AgentOutcome{{Head: formatted, Result: &Result{Git: &ResultGit{Head: formatted, BaseCommit: jobHead,
		Commits: []ResultCommit{{SHA: formatted, Subject: "formatter"}}}}}}
	h.process()
	h.automation(Automation{Name: "format then review", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "format", Type: StepAgent, Worker: "coder", Brief: "format"},
		{ID: "review", Type: StepAgent, Worker: "wkr_custom_reviewer", Brief: "review", Fixes: FixesSecurity},
	}})
	result := workerResult(folder, "run-moved")
	result.Git.Head = jobHead
	h.results.results["run-moved"] = result
	h.jobEnd("run-moved", "", "succeeded", "direct")
	h.process()
	list := h.deliveries("worker/coder-1/run-moved")
	if len(list) != 1 {
		t.Fatalf("deliveries = %d", len(list))
	}
	d := list[0]
	if d.Steps[0].State != StateSucceeded || d.Head != formatted {
		t.Fatalf("format step = %+v head %s", d.Steps[0], d.Head)
	}
	if d.Steps[1].State != StateFailed || !strings.Contains(d.Steps[1].Reason, "an earlier agent step moved it") || len(h.agents.started) != 1 {
		t.Fatalf("fixing review after a moved head = %+v (started %d jobs)", d.Steps[1], len(h.agents.started))
	}
}
