package jobautomation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRebaseChecker is the RebaseChecker contract read with real Git in the
// folder (the daemon's jobAutomationAgents routes the same commands).
type gitRebaseChecker struct{}

func (gitRebaseChecker) TargetTip(_ context.Context, folder, branch string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	cmd.Dir = folder
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (gitRebaseChecker) ChangedFiles(_ context.Context, folder string, base, head HeadRef) ([]string, error) {
	cmd := exec.Command("git", "diff", "--no-renames", "--name-only", base.Commit, head.Commit, "--")
	cmd.Dir = folder
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

// rebaseRepo is a folder where dev (main) moved after the task branched:
// base B, the task's head H1 (a.txt) on agent/task-1, main's tip T (b.txt),
// and candidate rebase heads on top of T or B.
type rebaseRepo struct {
	*gitRepo
	base, taskHead, tip string
}

func newRebaseRepo(t *testing.T, folder string) *rebaseRepo {
	t.Helper()
	repo := &rebaseRepo{gitRepo: newGitRepo(t, folder)}
	repo.base = repo.git("rev-parse", "HEAD")
	repo.git("checkout", "-q", "-b", "agent/task-1")
	repo.write("a.txt", "task change\n")
	repo.taskHead = repo.commitFiles("task: a")
	repo.git("checkout", "-q", "main")
	repo.write("b.txt", "dev moved\n")
	repo.tip = repo.commitFiles("dev: b")
	return repo
}

// head commits files on a new branch started at from and returns its head;
// the folder is back on main afterwards.
func (r *rebaseRepo) head(name, from string, files map[string]string) string {
	r.git("checkout", "-q", "-b", name, from)
	for file, body := range files {
		r.write(file, body)
	}
	head := r.commitFiles(name)
	r.git("checkout", "-q", "main")
	return head
}

func rebaseFlow(folder string) Automation {
	return Automation{Name: "rebase then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "rebase", Type: StepAgent, Worker: "coder", Rebase: true, Brief: "Rebase the work onto dev."},
		{ID: "push", Type: StepRun, Command: "push.sh"},
	}}
}

// Requirement: a coder step with rebase: true is told the target branch tip
// it rebases onto, and its head moves the delivery head although it does
// not descend from it, only when the head descends from that tip, the job
// reported no uncommitted changes, and the files it changes against the tip
// are the delivery head's files against the old base, give or take only the
// files the handoff names on its "Rebase conflicts:" line; rebased_from is
// recorded and the later push gets the rebased head. Every other rebased
// head is refused (no retry), the head stays and the push does not run.
// Threat: a "rebase" that smuggles in new changes, drops work, leaves edits
// uncommitted, or is not onto the target, delivered without a review.
// Owners: Runner.runAgent (rebaseTarget, rebaseBrief) and Runner.rebaseHead
// over a real Git repository, the real Pebble store, signal feed and host
// executor. Stubbed: the coder job itself (recordingAgents, the provider).
// Layer: the runner is the narrowest layer that holds the step's recorded
// target tip, the head check and the delivery head together.
func TestRebaseStepMovesTheHeadOnlyUnderItsChecks(t *testing.T) {
	type rebaseCase struct {
		name     string
		files    map[string]string
		onBase   bool
		handoff  string
		dirty    []string
		accepted bool
		why      string
	}
	for _, tc := range []rebaseCase{
		{name: "clean rebase", files: map[string]string{"a.txt": "task change\n"}, handoff: "Rebased.\nRebase conflicts: none", accepted: true},
		{name: "conflict kept both sides", files: map[string]string{"a.txt": "task change\n", "b.txt": "dev moved\nand the task\n"}, handoff: "- Rebase conflicts: b.txt", accepted: true},
		{name: "unexplained file", files: map[string]string{"a.txt": "task change\n", "c.txt": "smuggled\n"}, handoff: "Rebase conflicts: none", why: "c.txt"},
		{name: "uncommitted changes", files: map[string]string{"a.txt": "task change\n"}, handoff: "Rebase conflicts: none", dirty: []string{"a.txt"}, why: "uncommitted"},
		{name: "not onto the target tip", files: map[string]string{"a.txt": "task change, rewritten\n"}, onBase: true, handoff: "Rebase conflicts: none", why: "does not descend from the target branch tip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			repo := newRebaseRepo(t, folder)
			from := repo.tip
			if tc.onBase {
				from = repo.base
			}
			rebased := repo.head("rebased", from, tc.files)
			h.runner.verify, h.runner.rebase = gitVerifier, gitRebaseChecker{}
			writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" > \"$PWD/pushed-head\"\n")
			h.agents.outcomes = []AgentOutcome{{Head: rebased, Handoff: tc.handoff,
				Result: &Result{Git: &ResultGit{Head: rebased, Worktree: folder}, Uncommitted: tc.dirty}}}
			h.process()
			h.automation(rebaseFlow(folder))
			result := taskResult(folder, "task-1", "coder")
			result.Git.Head, result.Git.BaseCommit, result.Git.BaseBranch, result.Git.Branch = repo.taskHead, repo.base, "main", "agent/task-1"
			h.results.results["task-1"] = result
			h.taskEnd("task-1", "", "succeeded", "task")
			h.process()

			if len(h.agents.started) != 1 || h.agents.started[0].Head != repo.taskHead ||
				!strings.Contains(h.agents.started[0].Brief, "onto main at "+repo.tip) || !strings.Contains(h.agents.started[0].Brief, RebaseConflictsPrefix) {
				t.Fatalf("rebase step started = %+v", h.agents.started)
			}
			d := h.deliveries("task/proj-1/task-1")[0]
			if d.Steps[0].Outcome["rebase_onto"] != repo.tip {
				t.Fatalf("rebase_onto = %v, want the tip %s", d.Steps[0].Outcome["rebase_onto"], repo.tip)
			}
			if tc.accepted {
				if d.State != StateSucceeded || d.Head != rebased || d.RebasedFrom != repo.taskHead || d.RebasedOnto != repo.tip {
					t.Fatalf("accepted rebase: state %s head %s rebased_from %s onto %s (step %+v)", d.State, d.Head, d.RebasedFrom, d.RebasedOnto, d.Steps[0])
				}
				if moved, _ := d.Steps[0].Outcome["moved_head"].(map[string]any); moved["from"] != repo.taskHead || moved["to"] != rebased || moved["rebased"] != true {
					t.Fatalf("moved_head = %+v", d.Steps[0].Outcome["moved_head"])
				}
				if got := strings.TrimSpace(readFile(t, filepath.Join(folder, "pushed-head"))); got != rebased {
					t.Fatalf("pushed %s, want the rebased head %s", got, rebased)
				}
				return
			}
			if d.State != StateFailed || d.Head != repo.taskHead || d.RebasedFrom != "" || d.Steps[0].State != StateFailed || d.Steps[0].Attempts != 1 ||
				!strings.Contains(d.Steps[0].Reason, "rebase not accepted") || !strings.Contains(d.Steps[0].Outcome["rebase_refused"].(string), tc.why) {
				t.Fatalf("refused rebase: state %s head %s rebased_from %q step %+v", d.State, d.Head, d.RebasedFrom, d.Steps[0])
			}
			if d.Steps[1].State != StateSkipped {
				t.Fatalf("push ran after a refused rebase: %+v", d.Steps[1])
			}
			if _, err := os.Stat(filepath.Join(folder, "pushed-head")); !os.IsNotExist(err) {
				t.Fatalf("a head was pushed after a refused rebase: %v", err)
			}
		})
	}
}

// Requirement: rebase is an option of a coder or swarm agent step only.
// Threat: a reviewer, a run step or a fixing reviewer allowed to rewrite the
// delivery head. Owner: NormalizeSteps (validateRebase); the step bound
// stays MaxSteps.
func TestRebaseIsOnlyForACoderOrSwarmStep(t *testing.T) {
	for _, steps := range [][]Step{
		{{ID: "r", Type: StepAgent, Worker: "reviewer", Brief: "x", Rebase: true}},
		{{ID: "r", Type: StepAgent, Worker: "reviewer", Brief: "x", Fixes: FixesSecurity, Rebase: true}},
		{{ID: "r", Type: StepRun, Command: "push.sh", Rebase: true}},
		{{ID: "r", Type: StepAgent, Worker: "coder", Brief: "x", Produce: true, Rebase: true}},
		{{ID: "r", Type: StepAgent, Worker: "wkr-custom", Brief: "x", Rebase: true}},
	} {
		if err := NormalizeSteps(steps); err == nil || !strings.Contains(err.Error(), "rebase") {
			t.Fatalf("steps %+v: err %v, want a rebase refusal", steps, err)
		}
	}
	for _, worker := range []string{"coder", "swarm"} {
		if err := NormalizeSteps([]Step{{ID: "r", Type: StepAgent, Worker: worker, Brief: "x", Rebase: true}}); err != nil {
			t.Fatalf("%s rebase step refused: %v", worker, err)
		}
	}
	many := make([]Step, MaxSteps+1)
	for i := range many {
		many[i] = Step{Type: StepRun, Command: "push.sh"}
	}
	if err := NormalizeSteps(many); err == nil {
		t.Fatalf("%d steps accepted", len(many))
	}
}

// Requirement: RebaseConflicts reads the files named on the handoff's last
// "Rebase conflicts:" line; "none" and no line name none.
func TestRebaseConflictsReadsTheHandoffLine(t *testing.T) {
	for handoff, want := range map[string]string{
		"":                       "",
		"Rebase conflicts: none": "",
		"done\n* rebase conflicts: `a.go`, b/c.go":     "a.go|b/c.go",
		"Rebase conflicts: x\nRebase conflicts: none.": "",
	} {
		if got := strings.Join(RebaseConflicts(handoff), "|"); got != want {
			t.Fatalf("RebaseConflicts(%q) = %q, want %q", handoff, got, want)
		}
	}
}

type recordingAdvancer struct {
	reqs   []TaskBranchAdvance
	answer TaskBranchSync
}

func (a *recordingAdvancer) AdvanceTaskBranch(_ context.Context, req TaskBranchAdvance) TaskBranchSync {
	a.reqs = append(a.reqs, req)
	out := a.answer
	out.Head = req.To.Commit
	return out
}

// Requirement: when a fresh agent step's commits move the delivery head and
// the triggering job is a project task, the task's own branch is asked to
// fast-forward to the new head (from the old head, on the delivery's
// branch); the outcome is recorded on the delivery, and a branch that was
// not moved is signalled with its reason without failing the step. A worker
// job's delivery and a step that moved nothing ask nothing.
// Threat: a later follow-up of the task starting from a stale head, so the
// person cherry-picks the fix by hand; or a lagging branch nobody is told
// about. Owners: Runner.runAgent and Runner.syncTaskBranch over the real
// store and signal feed; the advancer (the daemon's Git, tested in api) and
// the fix job are stubbed.
func TestFixStepAsksToFastForwardTheTaskBranch(t *testing.T) {
	fixHead := strings.Repeat("c", 40)
	for _, tc := range []struct {
		name   string
		kind   string
		head   string
		answer TaskBranchSync
		asked  bool
	}{
		{name: "task advanced", kind: KindTask, head: fixHead, answer: TaskBranchSync{Advanced: true}, asked: true},
		{name: "task dirty", kind: KindTask, head: fixHead, answer: TaskBranchSync{Reason: "the task's worktree has uncommitted changes"}, asked: true},
		{name: "worker job", kind: KindWorker, head: fixHead},
		{name: "nothing committed", kind: KindTask, head: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			folder := t.TempDir()
			advancer := &recordingAdvancer{answer: tc.answer}
			h.runner.branch = advancer
			h.runner.verify = func(context.Context, string, HeadRef, HeadRef) (bool, error) { return true, nil }
			h.agents.outcomes = []AgentOutcome{{Head: tc.head}}
			h.process()
			h.automation(Automation{Name: "fix", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "Fix it."}}})
			jobID := "task/proj-1/task-1"
			if tc.kind == KindTask {
				h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
				h.taskEnd("task-1", "", "succeeded", "task")
			} else {
				jobID = "worker/coder-1/run-1"
				h.results.results["run-1"] = workerResult(folder, "run-1")
				h.jobEnd("run-1", "", "succeeded", "direct")
			}
			h.process()
			d := h.deliveries(jobID)[0]
			if d.State != StateSucceeded {
				t.Fatalf("delivery = %s %+v", d.State, d.Steps)
			}
			if !tc.asked {
				if len(advancer.reqs) != 0 || d.TaskBranch != nil {
					t.Fatalf("asked to move a branch: %+v %+v", advancer.reqs, d.TaskBranch)
				}
				return
			}
			old := strings.Repeat("b", 40)
			if len(advancer.reqs) != 1 || advancer.reqs[0].From != old || advancer.reqs[0].To.Commit != fixHead || advancer.reqs[0].Branch != "agent/task-1" ||
				advancer.reqs[0].JobID != jobID || advancer.reqs[0].Step != "fix" || advancer.reqs[0].DeliveryID != d.ID {
				t.Fatalf("advance requests = %+v", advancer.reqs)
			}
			if d.Head != fixHead || d.TaskBranch == nil || d.TaskBranch.Head != fixHead || d.TaskBranch.Advanced != tc.answer.Advanced || d.TaskBranch.Reason != tc.answer.Reason || d.TaskBranch.From != old {
				t.Fatalf("task branch = %+v (head %s)", d.TaskBranch, d.Head)
			}
			lagging := h.signalsOf("automation.task_branch.not_advanced")
			if tc.answer.Advanced != (len(lagging) == 0) || (len(lagging) == 1 && lagging[0].Attrs["reason"] != tc.answer.Reason) {
				t.Fatalf("not_advanced signals = %+v", lagging)
			}
		})
	}
}

// Requirement: a task whose newest delivery failed on a run step (a gate),
// or was sent back because a run step failed, and that hands its work over
// again with no new commit (a message_task or reopen follow-up ending at
// the head the delivery covers) re-runs that delivery from the failed run
// step: the documented rerun, signalled as automation.delivery.rerun, with
// no new delivery. It happens once per follow-up. A delivery that failed on
// an agent step and a worker job's hand-over are left as before
// (duplicate_head, no rerun).
// Threat: a gate broken by something outside the code (the gate script, a
// flaky test, a provider outage) leaving the work stuck until a person
// calls POST /v3/job-deliveries/{id}/rerun by hand; or a hand-over
// re-running work it should not (an agent's failure, a replayed signal).
// Owners: Runner.consume (rerunOnHandover), Store.CreateDelivery,
// Store.RerunAfterHandover and HandoverRerunStep over the real store,
// signal feed and host executor; the task result is stubbed.
func TestSameHeadHandoverRerunsTheDeliveryFromTheFailedGate(t *testing.T) {
	gate := "test -f \"$PWD/gate-ok\" || { echo \"the gate script is broken\"; exit 1; }\necho gate passed\n"
	setup := func(t *testing.T, steps []Step) (*harness, string) {
		h := newHarness(t)
		h.withReopener(&sessionReopener{})
		folder := t.TempDir()
		writeScript(t, folder, "gate.sh", gate)
		writeScript(t, folder, "push.sh", "echo pushed >> \"$PWD/pushes\"\n")
		h.process()
		h.automation(Automation{Name: "gate then push", Scope: Scope{Folder: folder}, Enabled: true, Steps: steps})
		return h, folder
	}
	fixGate := func(t *testing.T, folder string) {
		if err := os.WriteFile(filepath.Join(folder, "gate-ok"), []byte("ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gateThenPush := []Step{
		{ID: "gate", Type: StepRun, Command: "gate.sh"},
		{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "gate", Status: StateSucceeded}}},
	}
	const jobID = "task/proj-1/task-1"

	t.Run("failed gate", func(t *testing.T) {
		h, folder := setup(t, gateThenPush)
		h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
		h.taskEnd("task-1", "", "succeeded", "task")
		h.process()
		first := h.deliveries(jobID)[0]
		if first.State != StateFailed || first.Steps[0].State != StateFailed || first.Steps[1].State != StateSkipped {
			t.Fatalf("first delivery = %s %+v", first.State, first.Steps)
		}
		fixGate(t, folder)
		h.taskEnd("task-1", "followup-1", "succeeded", "task")
		h.process()
		list := h.deliveries(jobID)
		if len(list) != 1 {
			t.Fatalf("a same-head hand-over created a delivery: %+v", list)
		}
		d := list[0]
		if d.ID != first.ID || d.State != StateSucceeded || d.Reruns != 1 || d.HandoverRerun != "followup-1" || d.Steps[0].State != StateSucceeded || d.Steps[1].State != StateSucceeded {
			t.Fatalf("re-run delivery = %s reruns %d handover %q steps %+v", d.State, d.Reruns, d.HandoverRerun, d.Steps)
		}
		if len(d.Steps[0].Failures) != 1 {
			t.Fatalf("the gate's earlier failure was lost: %+v", d.Steps[0].Failures)
		}
		reruns := h.signalsOf("automation.delivery.rerun")
		if len(reruns) != 1 || reruns[0].Refs["delivery_id"] != d.ID || reruns[0].Attrs["step"] != "gate" || !strings.Contains(reruns[0].Summary, "re-runs from step gate") {
			t.Fatalf("rerun signals = %+v", reruns)
		}
		if len(h.signalsOf("automation.delivery.skipped")) != 0 {
			t.Fatal("a re-run hand-over was also reported as skipped")
		}
		// The same follow-up never re-runs the delivery twice.
		again, err := h.store.RerunAfterHandover(testAccount, d.ID, "followup-1")
		if err != nil || again.Rerun || again.Reason != ReasonNothingToRerun {
			t.Fatalf("second rerun for the same follow-up = %+v, %v", again, err)
		}
		if got := readFile(t, filepath.Join(folder, "pushes")); strings.Count(got, "pushed") != 1 {
			t.Fatalf("pushes = %q", got)
		}
	})

	t.Run("sent back by the gate", func(t *testing.T) {
		h, folder := setup(t, []Step{
			{ID: "gate", Type: StepRun, Command: "gate.sh", ContinueOnFailure: true},
			{ID: "back", Type: StepReopen, Message: "The gate failed: {{steps.gate.output}}", If: []Condition{{Step: "gate", Status: StateFailed}}},
			{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "gate", Status: StateSucceeded}}},
		})
		h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
		h.taskEnd("task-1", "", "succeeded", "task")
		h.process()
		first := h.deliveries(jobID)[0]
		if first.State != StateSentBack || first.Steps[1].Outcome["followup"] != "followup-1" {
			t.Fatalf("first delivery = %s %+v", first.State, first.Steps)
		}
		fixGate(t, folder)
		h.taskEnd("task-1", "followup-1", "succeeded", "task")
		h.process()
		list := h.deliveries(jobID)
		if len(list) != 1 || list[0].State != StateSucceeded || list[0].Steps[0].State != StateSucceeded || list[0].Steps[1].State != StateSkipped ||
			list[0].Steps[2].State != StateSucceeded || list[0].SentBack != nil {
			t.Fatalf("after the hand-over = %+v", list)
		}
	})

	t.Run("hand-over at the head a fix step moved to", func(t *testing.T) {
		h, folder := setup(t, append([]Step{{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "Fix it."}}, gateThenPush...))
		fixHead := strings.Repeat("c", 40)
		h.runner.verify = func(context.Context, string, HeadRef, HeadRef) (bool, error) { return true, nil }
		h.agents.outcomes = []AgentOutcome{{Head: fixHead}}
		result := taskResult(folder, "task-1", "coder")
		h.results.results["task-1"] = result
		h.taskEnd("task-1", "", "succeeded", "task")
		h.process()
		first := h.deliveries(jobID)[0]
		if first.State != StateFailed || first.Head != fixHead {
			t.Fatalf("first delivery = %s head %s", first.State, first.Head)
		}
		fixGate(t, folder)
		followup := *result
		git := *result.Git
		git.Head = fixHead
		followup.Git = &git
		h.results.results["task-1"] = &followup
		h.taskEnd("task-1", "followup-1", "succeeded", "task")
		h.process()
		list := h.deliveries(jobID)
		if len(list) != 1 || list[0].State != StateSucceeded || list[0].Head != fixHead || len(h.agents.started) != 1 || list[0].Steps[0].State != StateSucceeded {
			t.Fatalf("after the hand-over = %+v (agents started %d)", list, len(h.agents.started))
		}
	})

	t.Run("failed agent step is not re-run", func(t *testing.T) {
		h, folder := setup(t, []Step{{ID: "fix", Type: StepAgent, Worker: "coder", Brief: "Fix it."}})
		h.agents.status = "failed"
		h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
		h.taskEnd("task-1", "", "succeeded", "task")
		h.process()
		h.taskEnd("task-1", "followup-1", "succeeded", "task")
		h.process()
		list := h.deliveries(jobID)
		if len(list) != 1 || list[0].State != StateFailed || list[0].Reruns != 0 || len(h.agents.started) != 1 {
			t.Fatalf("after the hand-over = %+v", list)
		}
		if len(h.signalsOf("automation.delivery.rerun")) != 0 || len(h.signalsOf("automation.delivery.skipped")) != 1 {
			t.Fatal("a failed agent step was re-run on a hand-over")
		}
	})

	t.Run("worker job is not re-run", func(t *testing.T) {
		h, folder := setup(t, gateThenPush)
		h.results.results["run-1"] = workerResult(folder, "run-1")
		h.jobEnd("run-1", "", "succeeded", "direct")
		h.process()
		fixGate(t, folder)
		h.jobEnd("run-1", "followup-1", "succeeded", "direct")
		h.process()
		list := h.deliveries("worker/coder-1/run-1")
		if len(list) != 1 || list[0].State != StateFailed || list[0].Reruns != 0 {
			t.Fatalf("after the hand-over = %+v", list)
		}
	})
}
