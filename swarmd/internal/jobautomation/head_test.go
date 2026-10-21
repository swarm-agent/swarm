package jobautomation

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a real Git repository for head checks: the folder the job's
// commits and a reviewer's commits on top of them live in.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T, dir string) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	r.commit("base")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + r.dir, "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepo) commit(msg string) string {
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// gitVerifier is the HeadVerifier contract checked with real Git in the
// folder (the daemon's VerifyJobHead also imports from the checkouts first;
// here every commit is already in the folder).
func gitVerifier(_ context.Context, folder string, ancestor, head HeadRef) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor.Commit, head.Commit)
	cmd.Dir = folder
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Requirement: the delivery's head starts as the triggering job's head; an
// agent step whose job committed on top of it (Git confirms its head
// descends from the current delivery head) moves it forward, and later run
// steps get the moved head as SWARM_HEAD and as the ref they import. A job
// head that does not descend fails the step, the head stays, and the later
// delivery step does not run.
// Threat: delivering a reviewer's unrelated or rewritten history, or pushing
// the pre-fix head after a reviewer fixed it.
// Owners: Runner.moveHead (HeadVerifier), stepEnv/headResult and
// Store.CreateDelivery, over a real Git repository.
func TestAgentCommitMovesTheDeliveryHeadAndANonDescendantFails(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	jobHead := repo.commit("coder: add the limit")
	fixHead := repo.commit("reviewer: escape the path")
	repo.git("checkout", "-q", "-b", "other", jobHead+"~1")
	sibling := repo.commit("unrelated history")
	h.runner.verify = gitVerifier
	var imported []string
	h.runner.head = func(_ context.Context, _ string, r Result) (string, error) {
		imported = append(imported, r.Git.Head)
		return "refs/swarm/jobs/" + r.RunID, nil
	}
	writeScript(t, folder, "deliver/push.sh", "echo \"$SWARM_HEAD\" > \"$PWD/pushed-head\"\n")
	h.agents.outcomes = []AgentOutcome{
		{Verdict: VerdictOK, Head: fixHead, Result: &Result{Git: &ResultGit{Head: fixHead, Worktree: filepath.Join(folder, "reviewer-checkout")}}},
		{Verdict: VerdictOK, Head: sibling},
	}
	h.process()
	h.automation(Automation{Name: "review then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review"},
		{ID: "push", Type: StepRun, Command: "deliver/push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}})
	result := workerResult(folder, "run-1")
	result.Git.Head, result.Git.Worktree = jobHead, filepath.Join(folder, "coder-checkout")
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || d.Head != fixHead || d.HeadWorktree != filepath.Join(folder, "reviewer-checkout") {
		t.Fatalf("delivery = state %s head %s worktree %s (want %s)", d.State, d.Head, d.HeadWorktree, fixHead)
	}
	if moved, _ := d.Steps[0].Outcome["moved_head"].(map[string]any); moved["from"] != jobHead || moved["to"] != fixHead {
		t.Fatalf("moved_head = %+v", d.Steps[0].Outcome["moved_head"])
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(folder, "pushed-head"))); got != fixHead {
		t.Fatalf("SWARM_HEAD = %s, want the moved head %s", got, fixHead)
	}
	if len(imported) != 1 || imported[0] != fixHead {
		t.Fatalf("imported heads = %v", imported)
	}
	if d.Result.Git.Head != jobHead {
		t.Fatal("the job's own result was rewritten")
	}

	// A head that does not descend from the delivery head fails the step.
	result2 := workerResult(folder, "run-2")
	result2.Git.Head = jobHead
	h.results.results["run-2"] = result2
	h.jobEnd("run-2", "", "succeeded", "direct")
	h.process()
	d2 := h.deliveries("worker/coder-1/run-2")[0]
	if d2.State != StateFailed || d2.Steps[0].State != StateFailed || !strings.Contains(d2.Steps[0].Reason, "does not descend") || d2.Head != jobHead {
		t.Fatalf("non-descendant = state %s step %+v head %s", d2.State, d2.Steps[0], d2.Head)
	}
	if d2.Steps[0].Attempts != 1 || d2.Steps[1].State != StateSkipped {
		t.Fatalf("after a non-descendant head: attempts %d, push %s", d2.Steps[0].Attempts, d2.Steps[1].State)
	}
	if len(imported) != 1 {
		t.Fatalf("a run step imported a head after a failed head check: %v", imported)
	}
}
