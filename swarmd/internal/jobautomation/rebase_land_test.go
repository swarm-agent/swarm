package jobautomation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergingDeliverer is the daemon's deliverer reduced to its Git: it applies
// the daemon's rule for a moved delivery head (BranchCarriesHead, the check
// the daemon's toBranch runs, over the same GitRebaser) and then really
// merges the work's branch into the folder's checked-out target, as task
// integration lands the branch.
type mergingDeliverer struct {
	repo    *conflictRepo
	landed  []string
	refused []string
}

func (m *mergingDeliverer) Deliver(ctx context.Context, req DeliverRequest) (DeliverOutcome, error) {
	if err := BranchCarriesHead(ctx, GitRebaser{Git: testGit}, req); err != nil {
		m.refused = append(m.refused, err.Error())
		return DeliverOutcome{}, err
	}
	m.repo.git("merge", "-q", "--no-ff", "-m", "land "+req.Result.Git.Branch, req.Result.Git.Branch)
	m.landed = append(m.landed, req.Head)
	return DeliverOutcome{Where: "merged " + req.Head, Delivered: []DeliveredItem{{Name: "branch", Kind: DeliverableBranch, Where: "merged"}}}, nil
}

func (m *mergingDeliverer) Locate(context.Context, string, ResultSpec, AgentOutcome) (Deliverable, error) {
	return Deliverable{}, nil
}

// Requirement: with no rebase step, a delivery whose target moved since the
// job's base is rebased automatically and its deliver step to merge lands the
// rebased head: the job's branch was moved to it by compare-and-swap, so the
// merge lands exactly the head the steps checked, and the target then holds
// the job's commits on top of the commit that moved it. When the rebase could
// not move the branch (it is checked out in another worktree, not the job's
// own), the automatic rebase is not applied (outcome skipped, its reason
// naming the branch and where it is checked out): the delivery keeps its head
// and the merge lands the branch as it is, the head the steps checked.
// Threat: every delivery whose target moved failing at its merge step ("an
// earlier step moved the delivery head"), the acceptance case; a merge
// landing a branch head the checks never saw; or a branch Swarm cannot move
// breaking the delivery.
// Owners: Runner.runDeliver (Head, RebasedFrom), BranchCarriesHead (the
// daemon's toBranch rule), Runner.finishRebase, GitRebaser over a real
// repository. The merge itself is a plain git merge of the branch, standing
// for task integration.
func TestDeliverMergeLandsTheRebasedHead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		branchOut bool
	}{{name: "branch moved"}, {name: "branch checked out elsewhere", branchOut: true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withRebaser(t)
			folder := t.TempDir()
			repo := newConflictRepo(t, folder, false)
			if tc.branchOut {
				repo.git("worktree", "add", "-q", t.TempDir()+"/wt", "agent/task-1")
			}
			deliverer := &mergingDeliverer{repo: repo}
			h.runner.deliver = deliverer
			h.process()
			h.automation(pushFlow(folder, "", Step{ID: "merge", Type: StepDeliver, To: DeliverMerge}))
			h.startTaskWithBranch(folder, repo)
			h.process()

			d := h.deliveries("task/proj-1/task-1")[0]
			if tc.branchOut {
				if len(d.Rebases) != 1 || d.Rebases[0].Outcome != RebaseSkipped || d.Head != repo.two || d.RebasedFrom != "" ||
					!strings.Contains(d.Rebases[0].Reason, "agent/task-1") || !strings.Contains(d.Rebases[0].Reason, "checked out") {
					t.Fatalf("rebase: head %s rebases %+v", d.Head, d.Rebases)
				}
				if d.State != StateSucceeded || len(deliverer.landed) != 1 || deliverer.landed[0] != repo.two || len(deliverer.refused) != 0 {
					t.Fatalf("delivery %s landed %v refused %v step %+v", d.State, deliverer.landed, deliverer.refused, d.Steps[0])
				}
				if repo.git("rev-parse", "agent/task-1") != repo.two || repo.git("rev-parse", "main^2") != repo.two {
					t.Fatalf("main = %s, want a merge of the unmoved branch %s", repo.git("log", "--oneline", "-3", "main"), repo.two)
				}
				if len(h.signalsOf("automation.delivery.rebase_skipped")) != 1 {
					t.Fatal("no rebase_skipped signal")
				}
				return
			}
			if len(d.Rebases) != 1 || d.Rebases[0].Outcome != RebaseRebased || d.Head != d.Rebases[0].To || d.RebasedFrom != repo.two {
				t.Fatalf("rebase: head %s rebases %+v", d.Head, d.Rebases)
			}
			if d.State != StateSucceeded || len(deliverer.landed) != 1 || deliverer.landed[0] != d.Head {
				t.Fatalf("delivery %s landed %v refused %v step %+v", d.State, deliverer.landed, deliverer.refused, d.Steps[0])
			}
			if repo.git("rev-parse", "main^2") != d.Head || repo.git("rev-parse", "main^1") != repo.tip {
				t.Fatalf("main = %s, want a merge of %s onto %s", repo.git("log", "--oneline", "-3", "main"), d.Head, repo.tip)
			}
		})
	}
}

// fixingReviewer is a reviewer allowed to fix: it starts where it is told
// (the delivery head), commits one fix on top of it in the folder and
// answers OK, as the daemon reports a fixing reviewer's checkout.
type fixingReviewer struct {
	recordingAgents
	repo *conflictRepo
}

func (f *fixingReviewer) Wait(_ context.Context, _, jobID string) (AgentOutcome, error) {
	start := f.started[len(f.started)-1]
	f.repo.git("checkout", "-q", "--detach", start.Head)
	f.repo.write("fix.txt", "security fix\n")
	fix := f.repo.commitFiles("review: security fix")
	f.repo.git("checkout", "-q", "main")
	return AgentOutcome{JobID: jobID, Status: StatusSucceeded, Verdict: VerdictOK, Head: fix,
		Result: &Result{JobID: jobID, Git: &ResultGit{BaseCommit: start.Head, Head: fix, Commits: []ResultCommit{{SHA: fix, Subject: "review: security fix"}}}}}, nil
}

// Requirement: a reviewer allowed to fix (fixes: security) still runs after
// Swarm's automatic rebase moved the delivery head: it starts at the rebased
// head and its fix moves the head on top of it. Only a head an agent step
// moved (not one carried forward by Swarm's rebases) keeps refusing it.
// Threat: every existing workflow with a fixing reviewer failing as soon as
// the target moved ("an earlier agent step moved it", which never happened).
// Owners: Runner.runAgent (the fixes guard), headMovedOnlyByRebase,
// applyReviewerFixes over a real repository; the reviewer is stubbed.
func TestFixingReviewerRunsAfterAnAutomaticRebase(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	reviewer := &fixingReviewer{repo: repo}
	h.runner.agent, h.runner.lines = reviewer, gitLineCounter
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review it.", Fixes: FixesSecurity}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if len(d.Rebases) != 1 || d.Rebases[0].Outcome != RebaseRebased {
		t.Fatalf("rebases %+v", d.Rebases)
	}
	rebased := d.Rebases[0].To
	if d.State != StateSucceeded || len(reviewer.started) != 1 || reviewer.started[0].Head != rebased || reviewer.started[0].Fixes != FixesSecurity {
		t.Fatalf("delivery %s step %+v started %+v", d.State, d.Steps[0], reviewer.started)
	}
	if repo.git("rev-parse", d.Head+"^") != rebased || d.Steps[0].Outcome["fixes_applied"] == false {
		t.Fatalf("head %s is not the fix on the rebased head %s: %+v", d.Head, rebased, d.Steps[0].Outcome)
	}

	// The guard itself: the job's own head and Swarm's rebases pass; an
	// agent's own rebase or a head nobody recorded does not.
	own := strings.Repeat("a", 40)
	one, two := strings.Repeat("b", 40), strings.Repeat("c", 40)
	base := Delivery{Head: own, Result: Result{Git: &ResultGit{Head: own}}}
	chain := base
	chain.Head = two
	chain.Rebases = []RebaseRecord{{N: 1, Trigger: RebaseTriggerStart, Outcome: RebaseRebased, From: own, To: one},
		{N: 2, Trigger: RebaseTriggerExplicit, Outcome: RebaseRebased, From: one, To: two}}
	agent := chain
	agent.Rebases = []RebaseRecord{chain.Rebases[0], {N: 2, Trigger: RebaseTriggerAgent, Outcome: RebaseRebased, From: one, To: two}}
	moved := base
	moved.Head = two
	for name, tc := range map[string]struct {
		d    Delivery
		want bool
	}{"own head": {base, true}, "swarm's rebases": {chain, true}, "an agent's rebase": {agent, false}, "moved by an agent": {moved, false}} {
		if got := headMovedOnlyByRebase(tc.d); got != tc.want {
			t.Fatalf("%s: headMovedOnlyByRebase = %v", name, got)
		}
	}
}

// Requirement: a worker job's branch stays checked out in the job's own
// worktree (the daemon makes it with git worktree add -b). An automatic
// rebase moves that branch in that worktree (clean, at the old head: git
// reset --keep) so a deliver step to merge lands the rebased head; while the
// worktree has uncommitted changes the branch is not moved, the rebase is not
// applied (outcome skipped, the reason naming the worktree) and the merge
// lands the branch as it is, so the delivery succeeds either way.
// Threat: every worker delivery that merges or publishes failing after a
// rebase (the branch left at the old head, BranchCarriesHead refusing it, a
// second rebase doing nothing); or a worktree's uncommitted work lost by
// moving its branch.
// Owners: Runner.finishRebase, Runner.moveJobBranch,
// GitRebaser.MoveCheckedOutBranch, Runner.skipRebase, BranchCarriesHead,
// over a real repository and worktree.
func TestWorkerJobCheckedOutBranchRebasesAndMerges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty bool
	}{{name: "clean worktree"}, {name: "dirty worktree", dirty: true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withRebaser(t)
			folder := t.TempDir()
			repo := newConflictRepo(t, folder, false)
			worktree := t.TempDir() + "/wt"
			repo.git("worktree", "add", "-q", worktree, "agent/task-1")
			if tc.dirty {
				if err := os.WriteFile(filepath.Join(worktree, "j.txt"), []byte("uncommitted\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			deliverer := &mergingDeliverer{repo: repo}
			h.runner.deliver = deliverer
			h.process()
			h.automation(pushFlow(folder, "", Step{ID: "merge", Type: StepDeliver, To: DeliverMerge}))
			result := workerResult(folder, "run-1")
			result.Git = &ResultGit{Branch: "agent/task-1", BaseBranch: "main", BaseCommit: repo.base, Head: repo.two, Worktree: worktree,
				Commits: []ResultCommit{{SHA: repo.one}, {SHA: repo.two}}, Files: []string{"f.txt", "j.txt"}}
			result.Deliverables = []Deliverable{{Name: "branch", Kind: DeliverableBranch}}
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()

			d := h.deliveries("worker/coder-1/run-1")[0]
			if len(d.Rebases) != 1 || d.Rebases[0].Branch == nil || d.Rebases[0].Branch.CheckedOut == "" {
				t.Fatalf("rebases %+v", d.Rebases)
			}
			if d.State != StateSucceeded || len(deliverer.landed) != 1 || len(deliverer.refused) != 0 {
				t.Fatalf("delivery %s landed %v refused %v step %+v", d.State, deliverer.landed, deliverer.refused, d.Steps[0])
			}
			if tc.dirty {
				if d.Rebases[0].Outcome != RebaseSkipped || d.Head != repo.two || !strings.Contains(d.Rebases[0].Reason, "uncommitted changes") {
					t.Fatalf("head %s rebase %+v", d.Head, d.Rebases[0])
				}
				if repo.git("rev-parse", "agent/task-1") != repo.two || readFile(t, filepath.Join(worktree, "j.txt")) != "uncommitted\n" {
					t.Fatal("the dirty worktree's branch or changes were touched")
				}
				if deliverer.landed[0] != repo.two || repo.git("rev-parse", "main^2") != repo.two {
					t.Fatalf("main = %s, want a merge of %s", repo.git("log", "--oneline", "-3", "main"), repo.two)
				}
				return
			}
			rebased := d.Rebases[0].To
			if d.Rebases[0].Outcome != RebaseRebased || !d.Rebases[0].Branch.Updated || d.Head != rebased || d.RebasedFrom != repo.two {
				t.Fatalf("head %s rebase %+v branch %+v", d.Head, d.Rebases[0], d.Rebases[0].Branch)
			}
			if repo.git("rev-parse", "agent/task-1") != rebased || repo.git("-C", worktree, "rev-parse", "HEAD") != rebased ||
				repo.git("-C", worktree, "symbolic-ref", "--short", "HEAD") != "agent/task-1" || repo.git("-C", worktree, "status", "--porcelain") != "" {
				t.Fatal("the job's worktree and branch are not at the rebased head")
			}
			if deliverer.landed[0] != rebased || repo.git("rev-parse", "main^2") != rebased || repo.git("rev-parse", "main^1") != repo.tip {
				t.Fatalf("main = %s, want a merge of %s onto %s", repo.git("log", "--oneline", "-3", "main"), rebased, repo.tip)
			}
		})
	}
}

// startTaskWithBranch is startTask whose result lists its branch as the
// deliverable (what merge and publish hand over), as the daemon lists it
// for a task with commits.
func (h *harness) startTaskWithBranch(folder string, repo *conflictRepo) {
	h.t.Helper()
	result := taskResult(folder, "task-1", "coder")
	result.Git.Head, result.Git.BaseCommit, result.Git.BaseBranch, result.Git.Branch = repo.two, repo.base, "main", "agent/task-1"
	result.Git.Commits = []ResultCommit{{SHA: repo.one}, {SHA: repo.two}}
	result.Deliverables = []Deliverable{{Name: "branch", Kind: DeliverableBranch}}
	h.results.results["task-1"] = result
	h.taskEnd("task-1", "", "succeeded", "task")
}
