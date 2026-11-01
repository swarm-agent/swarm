package jobautomation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the daemon's own rebase (GitRebaser over real Git in a
// temporary repository) through the real Runner, Pebble store, signal feed
// and host executor. Only the agent jobs (recordingAgents: the model
// provider) are stubbed; a resolver's work is a directory the test writes,
// standing for the resolver task's worktree.

// testGit is the GitFunc the daemon routes, run directly: Git in dir with a
// minimal environment plus env.
func testGit(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1"}, env...)
	return cmd.Output()
}

// conflictRepo is a folder where main moved after the task branched. base
// has f.txt (a b c); the task's branch agent/task-1 changes f.txt's middle
// line (commit one, which also adds j.txt) and then j.txt (commit two);
// main's tip either changes another file (clean) or the same line
// (conflict).
type conflictRepo struct {
	*gitRepo
	base, one, two, tip string
}

func newConflictRepo(t *testing.T, folder string, conflict bool) *conflictRepo {
	t.Helper()
	repo := &conflictRepo{gitRepo: newGitRepo(t, folder)}
	repo.write("f.txt", "a\nb\nc\n")
	repo.base = repo.commitFiles("base: f")
	repo.git("checkout", "-q", "-b", "agent/task-1")
	repo.write("f.txt", "a\nJOB\nc\n")
	repo.write("j.txt", "job 1\n")
	repo.git("add", "-A")
	repo.git("-c", "user.name=Job Author", "-c", "user.email=job@example.invalid", "commit", "-q", "-m", "task: change b\n\nwith a body line")
	repo.one = repo.git("rev-parse", "HEAD")
	repo.write("j.txt", "job 2\n")
	repo.git("add", "-A")
	repo.git("-c", "user.name=Job Author", "-c", "user.email=job@example.invalid", "commit", "-q", "-m", "task: second")
	repo.two = repo.git("rev-parse", "HEAD")
	repo.git("checkout", "-q", "main")
	if conflict {
		repo.write("f.txt", "a\nDEV\nc\n")
	} else {
		repo.write("d.txt", "dev moved\n")
	}
	repo.tip = repo.commitFiles("dev: moved")
	return repo
}

// moveMain commits on main (the folder stays on main).
func (r *conflictRepo) moveMain(file, body string) string {
	r.write(file, body)
	r.tip = r.commitFiles("dev: " + file)
	return r.tip
}

func (h *harness) withRebaser(t *testing.T) {
	t.Helper()
	h.runner.verify = gitVerifier
	h.runner.rebaser = GitRebaser{Git: testGit, Scratch: t.TempDir()}
}

func (h *harness) startTask(folder string, repo *conflictRepo) {
	h.t.Helper()
	result := taskResult(folder, "task-1", "coder")
	result.Git.Head, result.Git.BaseCommit, result.Git.BaseBranch, result.Git.Branch = repo.two, repo.base, "main", "agent/task-1"
	result.Git.Commits = []ResultCommit{{SHA: repo.one}, {SHA: repo.two}}
	h.results.results["task-1"] = result
	h.taskEnd("task-1", "", "succeeded", "task")
}

func pushFlow(folder string, mode string, steps ...Step) Automation {
	return Automation{Name: "land", Scope: Scope{Folder: folder}, Enabled: true, Rebase: mode, Steps: steps}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// Requirement: a delivery whose head already descends from its target's tip
// is not rebased: no record, the head, the job's branch and the target stay.
// Threat: a no-op rebase that rewrites commits (new ids) or moves a ref.
// Owners: Runner.rebaseDelivery and GitRebaser.Replay over a real repository.
func TestDaemonRebaseUpToDateIsANoOp(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	// main is reset to base, so the task's head descends from it.
	repo.git("update-ref", "refs/heads/main", repo.base, repo.tip)
	repo.git("reset", "-q", "--hard", repo.base)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || d.Head != repo.two || len(d.Rebases) != 0 || d.RebasedFrom != "" || d.TargetChecked != repo.base {
		t.Fatalf("delivery: state %s head %s rebases %+v rebased_from %q checked %q", d.State, d.Head, d.Rebases, d.RebasedFrom, d.TargetChecked)
	}
	if got := repo.git("rev-parse", "agent/task-1"); got != repo.two {
		t.Fatalf("the job's branch moved to %s", got)
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != repo.two {
		t.Fatalf("pushed %v, want the job's own head", got)
	}
}

// Requirement: with no workflow configuration, a delivery whose target moved
// is rebased before its first step: the job's commits are replayed onto the
// target's tip with their messages and authors, each new commit carries the
// same patch, the delivery head moves (the old one kept as rebased_from),
// the job's branch moves by compare-and-swap, the target branch is never
// written, and the record keeps the old->new mapping and a range-diff. The
// push gets the rebased head.
// Threat: work landing on a stale base (a gate failing "rebase needed"), a
// rebase that changes a commit, rewrites the target, or moves a branch that
// moved meanwhile.
// Owners: Runner.runDelivery/rebaseDelivery/finishRebase, GitRebaser
// (Replay, verifyReplay, UpdateBranch) over a real repository.
func TestDaemonRebaseReplaysCleanlyAndMovesTheBranch(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || d.Head == repo.two || d.RebasedFrom != repo.two || d.RebasedOnto != repo.tip || d.TargetChecked != repo.tip {
		t.Fatalf("delivery: state %s head %s rebased_from %s onto %s checked %s", d.State, d.Head, d.RebasedFrom, d.RebasedOnto, d.TargetChecked)
	}
	if len(d.Rebases) != 1 {
		t.Fatalf("rebases = %+v", d.Rebases)
	}
	rec := d.Rebases[0]
	if rec.Outcome != RebaseRebased || rec.Trigger != RebaseTriggerStart || rec.From != repo.two || rec.To != d.Head || rec.Onto != repo.tip || rec.Target != "main" ||
		len(rec.Commits) != 2 || rec.Commits[0].From != repo.one || rec.Commits[1].From != repo.two || rec.Commits[1].To != d.Head || !strings.Contains(rec.RangeDiff, "task: second") {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Branch == nil || !rec.Branch.Updated || repo.git("rev-parse", "agent/task-1") != d.Head {
		t.Fatalf("branch update %+v; agent/task-1 at %s", rec.Branch, repo.git("rev-parse", "agent/task-1"))
	}
	if got := repo.git("rev-parse", "main"); got != repo.tip {
		t.Fatalf("the target moved to %s", got)
	}
	if repo.git("merge-base", "--is-ancestor", repo.tip, d.Head) != "" {
		t.Fatal("unreachable")
	}
	for _, c := range rec.Commits {
		for _, format := range []string{"%an <%ae> %ad", "%B"} {
			if a, b := repo.git("log", "-1", "--format="+format, c.From), repo.git("log", "-1", "--format="+format, c.To); a != b {
				t.Fatalf("commit %s -> %s: %s differs: %q vs %q", c.From, c.To, format, a, b)
			}
		}
		if a, b := repo.git("diff", c.From+"^", c.From), repo.git("diff", c.To+"^", c.To); a != b {
			t.Fatalf("commit %s -> %s changes something else:\n%s\n---\n%s", c.From, c.To, a, b)
		}
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != d.Head {
		t.Fatalf("pushed %v, want the rebased head %s", got, d.Head)
	}
	if len(h.signalsOf("automation.delivery.rebased")) != 1 {
		t.Fatal("no rebased signal")
	}

	// A branch that moved meanwhile is never overwritten.
	g := GitRebaser{Git: testGit, Scratch: t.TempDir()}
	update := g.UpdateBranch(context.Background(), folder, "agent/task-1", repo.two, repo.tip)
	if update.Updated || repo.git("rev-parse", "agent/task-1") != d.Head || !strings.Contains(update.Reason, "not moved") {
		t.Fatalf("stale compare-and-swap: %+v", update)
	}
}

// Requirement: a target that moves between steps is noticed before the next
// step that depends on the head: the head is rebased again and the run steps
// that passed on the old head run again on the new one (recorded as such),
// then the next step runs on the rebased head.
// Threat: a push of a head the checks never saw, or checks that passed on a
// stale head standing for the rebased one.
// Owners: Runner.runDelivery (rebase before a head-dependent step,
// rerunAfterRebase) over a real repository.
func TestTargetMovedBetweenStepsRebasesAndRerunsTheCheck(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	moved := false
	h.host.before = func(req RunRequest) error {
		if req.Command == "check.sh" && !moved {
			moved = true
			repo.moveMain("e.txt", "another landing\n") // lands while the check runs
		}
		return nil
	}
	writeScript(t, folder, "check.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/checked\"\n")
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "check", Type: StepRun, Command: "check.sh"}, Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || len(d.Rebases) != 2 || d.TargetChecked != repo.tip {
		t.Fatalf("delivery: state %s rebases %+v checked %s tip %s", d.State, d.Rebases, d.TargetChecked, repo.tip)
	}
	second := d.Rebases[1]
	if second.Trigger != RebaseTriggerStep || second.Step != "push" || second.Onto != repo.tip || second.To != d.Head || len(second.Rerun) != 1 || second.Rerun[0] != "check" {
		t.Fatalf("second rebase = %+v", second)
	}
	checked := readLines(t, filepath.Join(folder, "checked"))
	if len(checked) != 2 || checked[0] != d.Rebases[0].To || checked[1] != d.Head {
		t.Fatalf("checked %v, want the first rebased head then the second %s", checked, d.Head)
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != d.Head {
		t.Fatalf("pushed %v, want %s", got, d.Head)
	}
	if d.Steps[0].RebaseRerun != 2 || !strings.Contains(d.Steps[0].Note, "rebased head") {
		t.Fatalf("check step record = %+v", d.Steps[0])
	}
	if repo.git("merge-base", "--is-ancestor", repo.tip, d.Head) != "" {
		t.Fatal("unreachable")
	}
}

// Requirement: a push that fails because the target moved under it re-enters
// the rebase by itself (no reopen step): the head is rebased and the push
// runs again on the rebased head.
// Threat: a delivery that dies on "rebase needed" although Swarm could
// rebase the work itself.
// Owners: Runner.runDelivery (rebase after a failed push-like step).
func TestPushFailingOnAMovedTargetRebasesAndRetries(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	pushes := 0
	h.host.before = func(req RunRequest) error {
		if req.Command == "push.sh" {
			pushes++
			if pushes == 1 {
				repo.moveMain("e.txt", "landed during the push\n")
				if err := os.WriteFile(filepath.Join(folder, "fail-once"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return nil
	}
	writeScript(t, folder, "push.sh", "if [ -f \"$PWD/fail-once\" ]; then rm \"$PWD/fail-once\"; echo 'rebase needed: main moved'; exit 1; fi\necho \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || pushes != 2 || len(d.Rebases) != 2 || d.Rebases[1].Trigger != RebaseTriggerFailed || d.Rebases[1].Onto != repo.tip {
		t.Fatalf("delivery: state %s pushes %d rebases %+v", d.State, pushes, d.Rebases)
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != d.Head || d.Head != d.Rebases[1].To {
		t.Fatalf("pushed %v, head %s", got, d.Head)
	}
	if d.Steps[0].State != StateSucceeded || d.Steps[0].RebaseRerun != 2 || len(d.Steps[0].Failures) != 1 {
		t.Fatalf("push record = %+v", d.Steps[0])
	}
}

// Requirement: rebase: off turns the automatic rebase off: the delivery runs
// on the job's own head and records no rebase.
// Threat: an opt-out that is ignored.
// Owners: Automation.normalizeRebase, Store.CreateDelivery (RebaseMode),
// Runner.autoRebasing.
func TestRebaseOffLeavesTheHead(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, RebaseOff, Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || d.Head != repo.two || len(d.Rebases) != 0 || d.RebaseMode != RebaseOff {
		t.Fatalf("delivery: state %s head %s rebases %+v mode %q", d.State, d.Head, d.Rebases, d.RebaseMode)
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != repo.two {
		t.Fatalf("pushed %v", got)
	}
	if repo.git("rev-parse", "agent/task-1") != repo.two {
		t.Fatal("the job's branch moved")
	}
}

// resolverDir writes a resolver's checkout: the conflicted files as the
// resolver left them.
func resolverDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Requirement: a conflict goes to a resolver agent (the coder) started at the
// kept conflicted state, with a brief naming the files, the stopped commit
// and both sides' diffs. Swarm takes exactly the conflicted files from its
// checkout, refuses a conflict marker left in them (that round is used),
// stages them and continues the replay; the head moves and the resolved
// commit is recorded as such.
// Threat: a person or the Orchestrator needed for an ordinary conflict; a
// resolution with conflict markers delivered; a resolver smuggling changes
// to other files.
// Owners: Runner.resolveConflict, GitRebaser.Replay/Resume over a real
// repository; the resolver job is stubbed (recordingAgents).
func TestRebaseConflictGoesToTheResolverAndContinues(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, true)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	marked := resolverDir(t, map[string]string{"f.txt": "a\n<<<<<<< ours\nDEV\n=======\nJOB\n>>>>>>> theirs\nc\n", "other.txt": "smuggled\n"})
	resolved := resolverDir(t, map[string]string{"f.txt": "a\nDEV and JOB\nc\n", "other.txt": "smuggled\n"})
	h.agents.outcomes = []AgentOutcome{{Worktree: marked}, {Worktree: resolved}}
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || len(h.agents.started) != 2 || len(d.Rebases) != 1 {
		t.Fatalf("delivery: state %s started %d rebases %+v steps %+v", d.State, len(h.agents.started), d.Rebases, d.Steps)
	}
	start := h.agents.started[0]
	rec := d.Rebases[0]
	c := rec.Conflict
	if start.Worker != ResolverWorker || start.Checkout != CheckoutNew || c == nil || start.Head != c.Checkout || h.agents.started[1].Head != c.Checkout {
		t.Fatalf("resolver start = %+v", start)
	}
	if !strings.Contains(start.Brief, "f.txt") || !strings.Contains(start.Brief, repo.one) || !strings.Contains(start.Brief, "DEV") || !strings.Contains(start.Brief, "JOB") {
		t.Fatalf("resolver brief = %s", start.Brief)
	}
	if marker := repo.git("show", start.Head+":f.txt"); !strings.Contains(marker, "<<<<<<<") || !strings.Contains(marker, ">>>>>>>") {
		t.Fatalf("the resolver's checkout has no conflict markers: %q", marker)
	}
	if !strings.Contains(h.agents.started[1].Brief, "conflict marker") {
		t.Fatalf("the second round is not told why the first was refused: %s", h.agents.started[1].Brief)
	}
	if rec.Outcome != RebaseRebased || c.Outcome != ConflictResolved || c.Rounds != 2 || len(c.ResolverJobs) != 2 || len(c.Files) != 1 || c.Files[0] != "f.txt" ||
		c.Commit != repo.one || len(c.TargetCommits) != 1 || c.TargetCommits[0].SHA != repo.tip {
		t.Fatalf("record = %+v conflict %+v", rec, c)
	}
	if len(rec.Commits) != 2 || !rec.Commits[0].Resolved || rec.Commits[1].Resolved {
		t.Fatalf("commits = %+v", rec.Commits)
	}
	if got := repo.git("show", d.Head+":f.txt"); got != "a\nDEV and JOB\nc" {
		t.Fatalf("f.txt = %q", got)
	}
	if got := repo.git("show", d.Head+":j.txt"); got != "job 2" {
		t.Fatalf("j.txt = %q", got)
	}
	if out, err := testGit(context.Background(), folder, nil, "cat-file", "-e", d.Head+":other.txt"); err == nil {
		t.Fatalf("a file the resolver changed outside the conflict was taken: %s", out)
	}
	if repo.git("rev-parse", "main") != repo.tip || repo.git("rev-parse", "agent/task-1") != d.Head {
		t.Fatal("refs: the target moved or the job's branch was not moved")
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != d.Head {
		t.Fatalf("pushed %v", got)
	}
}

// Requirement: a conflict the resolver rounds do not resolve ends the
// delivery conflict (not failed, not blocked): the head and the job's
// branch stay, nothing after runs, and the delivery records the files, the
// stopped commit, the target's commits and the resolver jobs. A rerun of
// the delivery rebases again.
// Threat: a half-resolved head delivered, or an unresolvable conflict
// reported as the work failing a check.
// Owners: Runner.resolveConflict/unresolved/stopOnConflict, Store.Rerun.
func TestUnresolvedRebaseConflictEndsTheDeliveryConflict(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, true)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	marked := resolverDir(t, map[string]string{"f.txt": "a\n<<<<<<< ours\nDEV\n=======\nJOB\n>>>>>>> theirs\nc\n"})
	h.agents.outcomes = []AgentOutcome{{Worktree: marked}, {Status: "failed", Error: "gave up"}}
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateConflict || d.Head != repo.two || d.Conflict == nil || d.Conflict.Outcome != ConflictUnresolved || len(d.Conflict.ResolverJobs) != 2 ||
		d.Conflict.Files[0] != "f.txt" || d.Conflict.Commit != repo.one || len(d.Conflict.TargetCommits) != 1 {
		t.Fatalf("delivery: state %s head %s conflict %+v", d.State, d.Head, d.Conflict)
	}
	if d.Steps[0].State != StateFailed || !strings.Contains(d.Steps[0].Reason, "conflicted in f.txt") || d.Steps[0].Next != ConflictNext {
		t.Fatalf("push record = %+v", d.Steps[0])
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed")); !os.IsNotExist(err) {
		t.Fatal("pushed after an unresolved conflict")
	}
	if repo.git("rev-parse", "agent/task-1") != repo.two || repo.git("rev-parse", "main") != repo.tip {
		t.Fatal("a ref moved")
	}
	finished := h.signalsOf("automation.delivery.finished")
	if len(finished) == 0 || finished[len(finished)-1].Attrs["state"] != StateConflict || finished[len(finished)-1].Attrs["files"] != "f.txt" {
		t.Fatalf("finished signal = %+v", finished)
	}
	// The conflicted state stays reachable for whoever resolves it.
	if out, err := testGit(context.Background(), folder, nil, "cat-file", "-e", d.Conflict.Checkout+"^{commit}"); err != nil {
		t.Fatalf("the conflicted state is gone: %s %v", out, err)
	}

	// A rerun rebases again (the target tip is checked anew).
	outcome, err := h.store.Rerun(testAccount, d.ID)
	if err != nil || !outcome.Rerun || outcome.Delivery.TargetChecked != "" || outcome.Delivery.Conflict != nil {
		t.Fatalf("rerun: %+v %v", outcome, err)
	}
}

// Requirement: an agent step (a fix step) that returns a head rebased onto
// the target moves the delivery head instead of being thrown away, when the
// head descends from the target tip and every commit of the delivery head
// maps by patch id to one of its commits; rebased_from keeps the old head.
// A head that drops one of the delivery head's changes is still refused.
// Threat: rebased work thrown away ("does not descend from the delivery
// head"), or a "rebase" that silently drops work moving the head.
// Owners: Runner.moveHead/acceptRebasedHead, GitRebaser.MapCommits.
func TestFixStepRebasedHeadIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		drop     bool
		accepted bool
	}{{name: "rebased with all commits", accepted: true}, {name: "rebased without a commit", drop: true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withRebaser(t)
			folder := t.TempDir()
			repo := newConflictRepo(t, folder, false)
			// The fix step's agent rebased its own copy (and added a fix).
			if tc.drop {
				// Only the first of the job's two commits survives.
				repo.git("checkout", "-q", "-b", "fix", repo.tip)
				repo.git("-c", "user.name=T", "-c", "user.email=t@example.invalid", "cherry-pick", repo.one)
			} else {
				repo.git("checkout", "-q", "-b", "fix", repo.two)
				repo.git("-c", "user.name=T", "-c", "user.email=t@example.invalid", "rebase", "-q", repo.tip)
			}
			repo.write("fix.txt", "fixed\n")
			fixHead := repo.commitFiles("fix: the check")
			repo.git("checkout", "-q", "main")
			writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
			h.agents.outcomes = []AgentOutcome{{Head: fixHead, Result: &Result{Git: &ResultGit{Head: fixHead, Worktree: folder}}}}
			h.process()
			h.automation(pushFlow(folder, RebaseOff, Step{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "Fix the check."}, Step{ID: "push", Type: StepRun, Command: "push.sh"}))
			h.startTask(folder, repo)
			h.process()

			d := h.deliveries("task/proj-1/task-1")[0]
			if !tc.accepted {
				if d.State != StateFailed || d.Head != repo.two || !strings.Contains(d.Steps[0].Reason, "not accepted as a rebase either") {
					t.Fatalf("delivery: state %s head %s step %+v", d.State, d.Head, d.Steps[0])
				}
				return
			}
			if d.State != StateSucceeded || d.Head != fixHead || d.RebasedFrom != repo.two || d.RebasedOnto != repo.tip || len(d.Rebases) != 1 {
				t.Fatalf("delivery: state %s head %s rebased_from %s onto %s rebases %+v step %+v", d.State, d.Head, d.RebasedFrom, d.RebasedOnto, d.Rebases, d.Steps[0])
			}
			rec := d.Rebases[0]
			if rec.Trigger != RebaseTriggerAgent || len(rec.Commits) != 2 || rec.Commits[0].To == "" || rec.Commits[1].To == "" || len(rec.Added) != 1 || rec.Added[0] != fixHead {
				t.Fatalf("record = %+v", rec)
			}
			if moved, _ := d.Steps[0].Outcome["moved_head"].(map[string]any); moved["rebased"] != true || moved["from"] != repo.two {
				t.Fatalf("moved_head = %+v", d.Steps[0].Outcome["moved_head"])
			}
			if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != fixHead {
				t.Fatalf("pushed %v", got)
			}
		})
	}
}

// Requirement: an explicit rebase step rebases at its point of the workflow
// onto its onto branch (even with rebase: off), and steps after it see the
// rebased head.
// Owners: Runner.runRebaseStep over a real repository.
func TestRebaseStepRebasesOntoItsBranch(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, RebaseOff, Step{ID: "rebase", Type: StepRebase, Onto: "main"}, Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || len(d.Rebases) != 1 || d.Rebases[0].Trigger != RebaseTriggerExplicit || d.Head != d.Rebases[0].To || d.RebasedFrom != repo.two {
		t.Fatalf("delivery: state %s head %s rebases %+v", d.State, d.Head, d.Rebases)
	}
	if d.Steps[0].Outcome["rebased"] != true || !strings.Contains(d.Steps[0].Output, "rebased onto main") {
		t.Fatalf("rebase step = %+v", d.Steps[0])
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != d.Head {
		t.Fatalf("pushed %v", got)
	}
}

// Requirement: the rebase setting is auto (stored as "") or off, rebase_rounds
// 0..MaxRebaseRounds; a rebase step has only onto (a branch name), and onto
// belongs to a rebase step only.
// Threat: a misspelt opt-out silently ignored; a rebase step that runs a
// worker or a command.
// Owners: Automation.normalizeRebase, Step.normalizeRebaseStep, NormalizeSteps.
func TestRebaseSettingAndStepValidation(t *testing.T) {
	base := func() Automation {
		return Automation{Name: "x", Scope: Scope{Folder: "/srv/repo"}, Steps: []Step{{ID: "push", Type: StepRun, Command: "push.sh"}}}
	}
	for _, mode := range []string{"", "auto", " AUTO ", "off"} {
		a := base()
		a.Rebase = mode
		if err := a.Normalize(); err != nil {
			t.Fatalf("rebase %q refused: %v", mode, err)
		}
		if want := map[bool]string{true: RebaseOff, false: ""}[strings.EqualFold(strings.TrimSpace(mode), "off")]; a.Rebase != want {
			t.Fatalf("rebase %q stored as %q", mode, a.Rebase)
		}
	}
	for _, bad := range []func(*Automation){
		func(a *Automation) { a.Rebase = "manual" },
		func(a *Automation) { a.RebaseRounds = MaxRebaseRounds + 1 },
		func(a *Automation) { a.RebaseRounds = -1 },
	} {
		a := base()
		bad(&a)
		if err := a.Normalize(); err == nil || !strings.Contains(err.Error(), "rebase") {
			t.Fatalf("%+v accepted: %v", a, err)
		}
	}
	for _, steps := range [][]Step{
		{{ID: "r", Type: StepRebase, Worker: "coder"}},
		{{ID: "r", Type: StepRebase, Command: "push.sh"}},
		{{ID: "r", Type: StepRebase, Onto: "-x"}},
		{{ID: "r", Type: StepRebase, Onto: "a..b"}},
		{{ID: "r", Type: StepRun, Command: "push.sh", Onto: "dev"}},
	} {
		if err := NormalizeSteps(steps); err == nil {
			t.Fatalf("steps %+v accepted", steps)
		}
	}
	steps := []Step{{ID: "r", Type: StepRebase}, {ID: "r2", Type: StepRebase, Onto: " dev ", If: []Condition{{Step: "r", Status: StateSucceeded}}}}
	if err := NormalizeSteps(steps); err != nil || steps[1].Onto != "dev" {
		t.Fatalf("rebase steps refused: %v (%+v)", err, steps)
	}
}
