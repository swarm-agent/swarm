package jobautomation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: "up to date -> no-op" also when the target already contains
// the delivery head (the head is an ancestor of the tip, not equal to it: the
// work landed). The automatic rebase records nothing and moves neither the
// delivery head nor the job's branch, steps run on the job's own head, and an
// on-demand rebase answers up_to_date ("already contains the work") with no
// record. A replay whose commits all change nothing on the tip (the work
// landed as other commits) is up to date too.
// Threat: the head and the job's branch silently moved to the target's tip
// (rebased_from recorded, gates and reviews re-run against the target).
// Owners: GitRebaser.Replay, Runner.rebaseDelivery, Runner.RebaseDelivery over
// a real repository.
func TestRebaseLeavesAHeadTheTargetAlreadyContains(t *testing.T) {
	h := newHarness(t)
	h.withRebaser(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	repo.git("merge", "-q", "--no-ff", "-m", "land task-1", "agent/task-1")
	tip := repo.git("rev-parse", "main")
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, "", Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()

	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || d.Head != repo.two || len(d.Rebases) != 0 || d.RebasedFrom != "" || d.TargetChecked != tip {
		t.Fatalf("delivery: state %s head %s rebases %+v rebased_from %q checked %q", d.State, d.Head, d.Rebases, d.RebasedFrom, d.TargetChecked)
	}
	if got := readLines(t, filepath.Join(folder, "pushed")); len(got) != 1 || got[0] != repo.two {
		t.Fatalf("pushed %v, want the job's own head", got)
	}
	out, err := h.runner.RebaseDelivery(context.Background(), testAccount, d.ID, "")
	if err != nil || out.Outcome != RebaseUpToDate || out.Record != nil || !strings.Contains(out.Reason, "already contains the work") {
		t.Fatalf("on-demand rebase: %+v %v", out, err)
	}
	stored, _ := h.store.GetDelivery(testAccount, d.ID)
	if stored.Head != repo.two || len(stored.Rebases) != 0 || stored.RebaseHold != nil || repo.git("rev-parse", "agent/task-1") != repo.two || repo.git("rev-parse", "main") != tip {
		t.Fatalf("after the on-demand rebase: head %s rebases %+v hold %+v", stored.Head, stored.Rebases, stored.RebaseHold)
	}

	// The tests' Git refuses what a sandboxed project's Git cannot reach (the
	// scratch message/blob/index files the rebase used to need).
	outside := filepath.Join(t.TempDir(), "workflow-rebase")
	if _, err := testGit(context.Background(), folder, nil, nil, "commit-tree", "HEAD^{tree}", "-F", filepath.Join(outside, "message")); err == nil {
		t.Fatal("a scratch file outside the folder was reachable")
	}
	if _, err := testGit(context.Background(), folder, []string{"GIT_INDEX_FILE=" + filepath.Join(outside, "index")}, nil, "write-tree"); err == nil {
		t.Fatal("an index outside the folder was reachable")
	}

	// The same changes landed as other commits (cherry-picked onto main).
	other := t.TempDir()
	cp := newConflictRepo(t, other, false)
	cp.git("cherry-pick", cp.one, cp.two)
	res, err := GitRebaser{Git: testGit}.Replay(context.Background(), other, cp.two, cp.git("rev-parse", "main"))
	if err != nil || res.Status != RebaseUpToDate || !res.InTarget || res.To != cp.two || len(res.Commits) != 2 || !res.Commits[0].InTarget {
		t.Fatalf("replay of work already in the target: %+v %v", res, err)
	}
}

// Requirement: an on-demand rebase and a rerun never overwrite each other. The
// rebase claims the delivery in the store (HoldForRebase); a rerun arriving
// while it works (here: at the moment it moves the job's branch) is refused
// with ReasonRebasing and changes nothing; the rebase's own saves are checked
// against its hold; once it is released the rerun is accepted and keeps the
// rebased head. A save under a hold that is gone writes nothing.
// Threat: a rerun answered rerun:true and then overwritten back to the
// terminal state (it never runs), or a rerun writing back the old head after
// the branch was moved.
// Owners: Store.HoldForRebase/SaveHeldDelivery/ReleaseRebaseHold, Store.Rerun
// (rerunLocked), Runner.RebaseDelivery, over a real repository and Pebble.
func TestRerunIsRefusedWhileARequestedRebaseHoldsTheDelivery(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newConflictRepo(t, folder, false)
	var deliveryID string
	var during *RerunOutcome
	h.runner.verify = gitVerifier
	h.runner.rebaser = GitRebaser{Git: func(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "refs/heads/agent/task-1" && during == nil && deliveryID != "" && strings.Contains(strings.Join(args, " "), "update-ref") {
				out, err := h.store.Rerun(testAccount, deliveryID)
				if err != nil {
					t.Errorf("rerun during the rebase: %v", err)
				}
				during = &out
			}
		}
		return testGit(ctx, dir, env, stdin, args...)
	}}
	writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
	h.process()
	h.automation(pushFlow(folder, RebaseOff, Step{ID: "push", Type: StepRun, Command: "push.sh"}))
	h.startTask(folder, repo)
	h.process()
	d := h.deliveries("task/proj-1/task-1")[0]
	if d.State != StateSucceeded || d.Head != repo.two {
		t.Fatalf("delivery: %s %s", d.State, d.Head)
	}
	// The push failed: a rerun has something to run again.
	d.State, d.Steps[0].State = StateFailed, StateFailed
	if err := h.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	deliveryID = d.ID

	out, err := h.runner.RebaseDelivery(context.Background(), testAccount, d.ID, "")
	if err != nil || out.Outcome != RebaseRebased {
		t.Fatalf("on-demand rebase: %+v %v", out, err)
	}
	if during == nil || during.Rerun || during.Reason != ReasonRebasing {
		t.Fatalf("a rerun while the rebase held the delivery: %+v", during)
	}
	stored, _ := h.store.GetDelivery(testAccount, d.ID)
	if stored.State != StateFailed || stored.Head != out.Record.To || stored.RebasedFrom != repo.two || stored.RebaseHold != nil || repo.git("rev-parse", "agent/task-1") != stored.Head {
		t.Fatalf("after the rebase: state %s head %s rebased_from %s hold %+v", stored.State, stored.Head, stored.RebasedFrom, stored.RebaseHold)
	}
	again, err := h.store.Rerun(testAccount, d.ID)
	if err != nil || !again.Rerun || again.Delivery.State != StatePending || again.Delivery.Head != stored.Head {
		t.Fatalf("rerun after the rebase: %+v %v", again, err)
	}

	// The store's hold itself: one at a time, refused on a pending delivery,
	// and a save under a lost hold writes nothing.
	if _, err := h.store.HoldForRebase(testAccount, d.ID, "t1", 1); !errors.Is(err, ErrDeliveryRunning) {
		t.Fatalf("hold on a pending delivery: %v", err)
	}
	finished := again.Delivery
	finished.State = StateFailed
	if err := h.store.SaveDelivery(finished); err != nil {
		t.Fatal(err)
	}
	held, err := h.store.HoldForRebase(testAccount, d.ID, "t1", h.runner.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.HoldForRebase(testAccount, d.ID, "t2", h.runner.now().UnixMilli()); !errors.Is(err, ErrDeliveryRunning) {
		t.Fatalf("second hold: %v", err)
	}
	stale := held
	stale.RebaseHold = &RebaseHold{Token: "someone-else", At: held.RebaseHold.At}
	stale.Head = repo.tip
	if err := h.store.SaveHeldDelivery(stale); !errors.Is(err, ErrRebaseHoldLost) {
		t.Fatalf("save under a lost hold: %v", err)
	}
	if _, err := h.store.ReleaseRebaseHold(stale); !errors.Is(err, ErrRebaseHoldLost) {
		t.Fatalf("release under a lost hold: %v", err)
	}
	if cur, _ := h.store.GetDelivery(testAccount, d.ID); cur.Head != held.Head || cur.RebaseHold == nil || cur.RebaseHold.Token != "t1" {
		t.Fatalf("a lost-hold write changed the delivery: %+v", cur)
	}
	if released, err := h.store.ReleaseRebaseHold(held); err != nil || released.RebaseHold != nil {
		t.Fatalf("release: %+v %v", released, err)
	}
}

// Requirement: a delivery that ends conflict says on the step it stopped at
// whether that step ran: "not run: <conflict>" for a step the rebase before
// it stopped (pending, never started), "<its failure>; <conflict>" for a
// step that ran and failed (a gate failing on a moved target), and the
// failure alone when it already names the conflict (a rebase step).
// Threat: a reason that starts "; the rebase ..." (an empty reason joined)
// or a step that never ran reported as failing.
// Owners: Runner.stopOnConflict. Layer: the function itself; the runner
// paths that reach it are covered by
// TestUnresolvedRebaseConflictEndsTheDeliveryConflict.
func TestStopOnConflictSaysWhetherTheStepRan(t *testing.T) {
	h := newHarness(t)
	conflict := &RebaseConflict{Commit: strings.Repeat("a", 40), Files: []string{"f.txt"}, Outcome: ConflictUnresolved}
	delivery := func(step StepRecord) Delivery {
		return Delivery{ID: "jdel-x", Account: testAccount, AutomationName: "land", JobID: "task/p/t", State: StateRunning, Conflict: conflict,
			Rebases: []RebaseRecord{{N: 1, Target: "main", Outcome: RebaseConflicted, Conflict: conflict}},
			Steps:   []StepRecord{step, {ID: "after", Type: StepRun, State: StatePending}}}
	}
	why := ConflictReason(delivery(StepRecord{}))
	for _, tc := range []struct {
		name string
		step StepRecord
		want string
	}{
		{"never ran", StepRecord{ID: "push", Type: StepRun, State: StatePending}, "not run: " + why},
		{"failed and waits for a retry", StepRecord{ID: "push", Type: StepRun, State: StatePending, Attempts: 1, StartedAt: 5, NextAttemptAt: 9,
			Reason: "rebase needed: main moved", Failures: []AttemptFailure{{Attempt: 1, Reason: "rebase needed: main moved"}}}, "rebase needed: main moved; " + why},
		{"a rebase step's own failure", StepRecord{ID: "rebase", Type: StepRebase, State: StateFailed, Attempts: 1, StartedAt: 5, Reason: why}, why},
	} {
		d := delivery(tc.step)
		h.runner.stopOnConflict(&d, 0)
		if d.Steps[0].Reason != tc.want || d.Steps[0].State != StateFailed || d.Steps[0].NextAttemptAt != 0 || d.State != StateConflict || d.Steps[1].State != StateSkipped {
			t.Fatalf("%s: step %+v delivery %s next step %s", tc.name, d.Steps[0], d.State, d.Steps[1].State)
		}
	}
}
