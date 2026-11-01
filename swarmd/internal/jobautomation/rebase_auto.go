package jobautomation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Swarm rebases a delivery's work onto its target branch by itself
// (docs/automations.md, "Rebasing"): before the first step, and again
// before any step that depends on the head (run, agent, deliver merge or
// publish) whenever the target moved since the last check, and after such
// a step failed while the target moved. Steps after a rebase see the
// rebased head; steps that passed on the old head and whose result
// depended on it run again. A conflict is given to a resolver agent; one
// it cannot resolve ends the delivery conflict.

const (
	// MaxDeliveryRebases bounds the automatic rebases that move one
	// delivery's head (a target that keeps moving): past it the steps run
	// on the head as it is.
	MaxDeliveryRebases = 5
	maxRebaseRecords   = 20
	// resolverTimeout bounds the wait for one resolver agent.
	resolverTimeout  = time.Hour
	maxResolverBrief = 18000
)

// Why a rebase ran (RebaseRecord.Trigger).
const (
	RebaseTriggerStart    = "start"
	RebaseTriggerStep     = "before_step"
	RebaseTriggerFailed   = "step_failed"
	RebaseTriggerExplicit = "rebase_step"
	RebaseTriggerAgent    = "agent_step"
	RebaseTriggerRequest  = "request"
)

// Resolver outcomes (RebaseConflict.Outcome).
const (
	ConflictResolved   = "resolved"
	ConflictUnresolved = "unresolved"
)

// ResolverWorker is the agent a conflict is given to: the coder that comes
// with Swarm (its model is the account's coder setting).
const ResolverWorker = "coder"

// RebaseRecord is one rebase of a delivery head: what it replayed onto
// what, how it ended, and what it did to the job's branch and the steps.
type RebaseRecord struct {
	N       int    `json:"n"`
	Trigger string `json:"trigger"`
	Step    string `json:"step,omitempty"`
	// Job is the agent step's job whose own rebased head was accepted.
	Job    string `json:"job,omitempty"`
	Target string `json:"target"`
	Onto   string `json:"onto"`
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	// Outcome is rebased, resolving (a resolver works on a conflict),
	// conflict (not resolved) or error.
	Outcome   string          `json:"outcome"`
	Commits   []RebasedCommit `json:"commits,omitempty"`
	Added     []string        `json:"added,omitempty"`
	RangeDiff string          `json:"range_diff,omitempty"`
	Conflict  *RebaseConflict `json:"conflict,omitempty"`
	// State is the replay a conflict stopped (what a resolution continues).
	State  *ReplayState  `json:"state,omitempty"`
	Branch *BranchUpdate `json:"branch,omitempty"`
	// Rerun names the steps that run again on the rebased head.
	Rerun      []string `json:"rerun,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	At         int64    `json:"at"`
	FinishedAt int64    `json:"finished_at,omitempty"`
}

// LastRebase is the delivery's newest rebase record, nil when none.
func (d Delivery) LastRebase() *RebaseRecord {
	if len(d.Rebases) == 0 {
		return nil
	}
	rec := d.Rebases[len(d.Rebases)-1]
	return &rec
}

type rebaseOutcome struct {
	n                         int
	branch, onto              string
	moved, conflict, upToDate bool
	// skipped: a clean replay not applied (skipRebase).
	skipped bool
	// reason: why a clean replay was skipped.
	reason string
	// inTarget: up to date because the target already contains the work.
	inTarget bool
	// err is why the rebase could not run.
	err string
}

// autoRebasing reports whether the delivery rebases automatically: the
// daemon can, the workflow did not turn it off, and the job has commits on
// a branch with a known target branch.
func (r *Runner) autoRebasing(d Delivery) bool {
	return r.rebaser != nil && d.RebaseMode != RebaseOff && d.Result.Git != nil && strings.TrimSpace(d.Result.Git.BaseBranch) != "" && isCommitID(d.Head)
}

// headDependent reports whether a step's result depends on the delivery
// head: a run step (its script gets the head), an agent step (it starts at
// or reviews the head), a deliver step to merge or publish.
func headDependent(step Step) bool {
	switch step.Type {
	case StepRun, StepAgent:
		return true
	case StepDeliver:
		return step.To == DeliverMerge || step.To == DeliverPublish
	}
	return false
}

// pushLike reports whether a failed step may have failed because the target
// moved under it: a run step (a gate, a push) or a deliver merge/publish.
func pushLike(step Step) bool {
	return step.Type == StepRun || (step.Type == StepDeliver && (step.To == DeliverMerge || step.To == DeliverPublish))
}

func (d Delivery) rebaseMoves() int {
	n := 0
	for _, rec := range d.Rebases {
		if rec.Outcome == RebaseRebased && rec.Trigger != RebaseTriggerExplicit && rec.Trigger != RebaseTriggerRequest {
			n++
		}
	}
	return n
}

func (d *Delivery) nextRebase(rec RebaseRecord) int {
	rec.N = 1
	if n := len(d.Rebases); n > 0 {
		rec.N = d.Rebases[n-1].N + 1
	}
	d.Rebases = append(d.Rebases, rec)
	if extra := len(d.Rebases) - maxRebaseRecords; extra > 0 {
		d.Rebases = append([]RebaseRecord(nil), d.Rebases[extra:]...)
	}
	return len(d.Rebases) - 1
}

// rebaseDelivery is the one rebase of a delivery head: onto branch (\"\" the
// job's target branch) at its tip now. Unless explicit (a rebase step, a
// request), nothing runs when the tip is the one last checked or the
// delivery used its MaxDeliveryRebases. Up to date moves nothing; a clean
// replay moves the head (finishRebase); a conflict goes to the resolver
// (resolveConflict). A rebase that waited on its resolver when the daemon
// stopped continues.
func (r *Runner) rebaseDelivery(ctx context.Context, d *Delivery, trigger, stepID, branch string, explicit bool) rebaseOutcome {
	if r.rebaser == nil {
		return rebaseOutcome{err: "this daemon cannot rebase"}
	}
	g := d.Result.Git
	target := ""
	if g != nil {
		target = strings.TrimSpace(g.BaseBranch)
	}
	if branch == "" {
		branch = target
	}
	if g == nil || !isCommitID(d.Head) || branch == "" {
		return rebaseOutcome{err: "nothing to rebase: the job has no commits on a branch with a known target branch"}
	}
	if last := len(d.Rebases) - 1; last >= 0 && d.Rebases[last].Outcome == RebaseResolving {
		return r.resolveConflict(ctx, d, last)
	}
	tip, err := r.rebaser.TargetTip(ctx, d.Folder, branch)
	if err != nil {
		return rebaseOutcome{branch: branch, err: stepReason("read the tip of " + branch + ": " + err.Error())}
	}
	out := rebaseOutcome{branch: branch, onto: tip}
	if !explicit && tip == d.TargetChecked {
		return out
	}
	if !explicit && d.rebaseMoves() >= MaxDeliveryRebases {
		d.TargetChecked = tip
		r.signal(*d, "automation.delivery.rebase_limit", pebblestore.SignalSeverityWarning,
			fmt.Sprintf("Automation %s: %s moved again, but job %s was already rebased %d times in this delivery; its steps run on the head as it is", d.AutomationName, branch, d.JobID, MaxDeliveryRebases),
			stepID, map[string]string{"automation": d.AutomationName, "target": branch, "onto": tip, "head": d.Head}, strconv.Itoa(len(d.Rebases)))
		return out
	}
	if r.head != nil {
		if _, err := r.head(ctx, d.Folder, headResult(*d)); err != nil {
			out.err = stepReason("could not bring the delivery head into the folder: " + err.Error())
			return out
		}
	}
	res, err := r.rebaser.Replay(ctx, d.Folder, d.Head, tip)
	if err != nil {
		if ctx.Err() != nil {
			return out
		}
		out.err = stepReason(err.Error())
		r.signal(*d, "automation.delivery.rebase_error", pebblestore.SignalSeverityWarning,
			fmt.Sprintf("Automation %s could not rebase job %s onto %s: %s", d.AutomationName, d.JobID, branch, out.err), stepID,
			map[string]string{"automation": d.AutomationName, "target": branch, "onto": tip, "head": d.Head, "reason": out.err}, tip)
		return out
	}
	if res.Status == RebaseUpToDate {
		if branch == target {
			d.TargetChecked = tip
		}
		out.upToDate, out.inTarget = true, res.InTarget
		return out
	}
	idx := d.nextRebase(RebaseRecord{Trigger: trigger, Step: stepID, Target: branch, Onto: tip, From: d.Head, At: r.now().UnixMilli()})
	if res.Status == RebaseConflicted && res.Conflict != nil && res.State != nil {
		rec := &d.Rebases[idx]
		rec.Outcome, rec.Conflict, rec.State, rec.Commits = RebaseResolving, res.Conflict, res.State, res.Commits
		_ = r.rebaser.KeepRef(ctx, d.Folder, rebaseRefName("rebase-conflicts", d.ID, rec.N), res.Conflict.Checkout)
		r.save(*d)
		r.signal(*d, "automation.delivery.rebase_conflict", pebblestore.SignalSeverityInfo,
			fmt.Sprintf("Automation %s: rebasing job %s onto %s conflicted in %s; a resolver agent works on it", d.AutomationName, d.JobID, branch, strings.Join(limitFiles(res.Conflict.Files), ", ")),
			stepID, map[string]string{"automation": d.AutomationName, "target": branch, "onto": tip, "head": d.Head, "files": strings.Join(limitFiles(res.Conflict.Files), ", "),
				"commit": res.Conflict.Commit}, strconv.Itoa(rec.N))
		return r.resolveConflict(ctx, d, idx)
	}
	return r.finishRebase(ctx, d, idx, res)
}

// finishRebase moves the delivery head to a clean replay's new head (the
// old one kept as rebased_from), moves the job's own branch by
// compare-and-swap (a task's checked-out branch through its branch
// advancer, only when its worktree is clean and idle) and records it all.
// Whatever asked for the rebase (automatic, a rebase step, a request), a
// replay whose branch could not be moved while a merge or publish step is
// still to land that branch is not applied (skipRebase): landing it would be
// refused.
func (r *Runner) finishRebase(ctx context.Context, d *Delivery, idx int, res ReplayResult) rebaseOutcome {
	rec := &d.Rebases[idx]
	prev := d.Head
	rec.Outcome, rec.To, rec.Commits, rec.RangeDiff, rec.State, rec.FinishedAt = RebaseRebased, res.To, res.Commits, res.RangeDiff, nil, r.now().UnixMilli()
	if rec.Conflict != nil {
		rec.Conflict.Outcome, rec.Conflict.Pending = ConflictResolved, ""
	}
	if err := r.rebaser.KeepRef(ctx, d.Folder, rebaseRefName("rebased", d.ID, rec.N), res.To); err != nil {
		rec.Reason = "the rebased head could not be named under refs/swarm: " + truncate(err.Error(), 200)
	}
	if d.Result.Git != nil && rec.Target == d.Result.Git.BaseBranch {
		d.TargetChecked = res.Onto
	}
	if g := d.Result.Git; g != nil {
		update := r.moveJobBranch(ctx, d, rec.Step, prev, res.To)
		rec.Branch = &update
		if !update.Updated && d.landsBranchLater() {
			return r.skipRebase(d, idx, update)
		}
	}
	d.RebasedFrom, d.RebasedOnto = prev, res.Onto
	d.Head, d.HeadWorktree = res.To, ""
	r.save(*d)
	attrs := map[string]string{"automation": d.AutomationName, "trigger": rec.Trigger, "target": rec.Target, "onto": res.Onto, "from": prev, "to": res.To,
		"commits": strconv.Itoa(len(res.Commits)), "rebase": strconv.Itoa(rec.N)}
	if rec.Conflict != nil {
		attrs["resolved_files"] = strings.Join(limitFiles(rec.Conflict.Files), ", ")
		attrs["resolver_jobs"] = strings.Join(rec.Conflict.ResolverJobs, ", ")
	}
	if rec.Branch != nil {
		attrs["branch_updated"] = strconv.FormatBool(rec.Branch.Updated)
	}
	r.signal(*d, "automation.delivery.rebased", pebblestore.SignalSeverityInfo,
		fmt.Sprintf("Automation %s rebased job %s onto %s at %s: head %s is now %s", d.AutomationName, d.JobID, rec.Target, short(res.Onto), short(prev), short(res.To)),
		rec.Step, attrs, strconv.Itoa(rec.N))
	return rebaseOutcome{n: rec.N, branch: rec.Target, onto: res.Onto, moved: true}
}

// moveJobBranch moves the job's own branch from prev to the rebased head to:
// by compare-and-swap when no worktree has it checked out; else only in the
// job's own worktree: a task's through its branch advancer (clean, idle), a
// worker job's (made with git worktree add -b, so it keeps its branch checked
// out) by MoveCheckedOutBranch (clean, at prev, git reset --keep). A branch
// checked out anywhere else (the folder itself, another worktree) is left
// alone, the update saying where.
func (r *Runner) moveJobBranch(ctx context.Context, d *Delivery, stepID, prev, to string) BranchUpdate {
	g := d.Result.Git
	update := r.rebaser.UpdateBranch(ctx, d.Folder, g.Branch, prev, to)
	if update.Updated || update.CheckedOut == "" {
		return update
	}
	switch {
	case d.Result.Kind == KindTask:
		if r.branch == nil {
			return update
		}
		sync := r.branch.AdvanceTaskBranch(ctx, TaskBranchAdvance{Account: d.Account, JobID: d.JobID, DeliveryID: d.ID, Step: stepID, Branch: g.Branch,
			From: prev, To: HeadRef{Commit: to}, Rebased: true})
		sync.Step, sync.From, sync.At = stepID, prev, r.now().UnixMilli()
		if sync.Head == "" {
			sync.Head = to
		}
		sync.Reason = stepReason(sync.Reason)
		d.TaskBranch = &sync
		update.Updated, update.Reason = sync.Advanced, sync.Reason
	case sameDir(update.CheckedOut, g.Worktree) && !sameDir(g.Worktree, d.Folder):
		moved, why := r.rebaser.MoveCheckedOutBranch(ctx, g.Worktree, g.Branch, prev, to)
		update.Updated, update.Reason = moved, ""
		if !moved {
			update.Reason = stepReason("the branch " + g.Branch + " is checked out in the job's worktree " + g.Worktree + " and was not moved: " + why)
		}
	}
	return update
}

// sameDir reports whether a and b name the same directory (both set).
func sameDir(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// landsBranchLater reports whether a deliver step to merge or publish (which
// lands the job's own branch, BranchCarriesHead) has not succeeded yet.
func (d Delivery) landsBranchLater() bool {
	for _, rec := range d.Steps {
		if rec.State == StateSucceeded {
			continue
		}
		if step, ok := d.step(rec.ID); ok && step.Type == StepDeliver && (step.To == DeliverMerge || step.To == DeliverPublish) {
			return true
		}
	}
	return false
}

// skipRebase undoes the move of a clean replay (automatic, a rebase step's
// or a request's) whose job branch could not be moved while a merge or
// publish is still to land that branch: landing it would be refused (the
// branch does not carry the rebased head), so the delivery keeps its head and
// its steps run on it as before. The rebased head stays named under
// refs/swarm; the record (outcome skipped) says why, and the tip counts as
// checked until the target moves again or the delivery is rerun.
func (r *Runner) skipRebase(d *Delivery, idx int, update BranchUpdate) rebaseOutcome {
	rec := &d.Rebases[idx]
	why := update.Reason
	if why == "" {
		why = "the branch " + update.Branch + " was not moved"
	}
	rec.Outcome = RebaseSkipped
	rec.Reason = stepReason("not applied: the job's branch " + update.Branch + " could not be moved to the rebased head (" + why +
		"), and a deliver step to merge or publish lands that branch; the delivery keeps its head " + short(rec.From))
	r.save(*d)
	r.signal(*d, "automation.delivery.rebase_skipped", pebblestore.SignalSeverityWarning,
		fmt.Sprintf("Automation %s did not rebase job %s onto %s: %s", d.AutomationName, d.JobID, rec.Target, rec.Reason),
		rec.Step, map[string]string{"automation": d.AutomationName, "trigger": rec.Trigger, "target": rec.Target, "onto": rec.Onto, "head": rec.From,
			"rebased": rec.To, "branch": update.Branch, "reason": rec.Reason, "rebase": strconv.Itoa(rec.N)}, strconv.Itoa(rec.N))
	return rebaseOutcome{n: rec.N, branch: rec.Target, onto: rec.Onto, skipped: true, reason: rec.Reason}
}

// carryRebasedHead moves the job's branch to the delivery head right before a
// merge or publish lands that branch, when the head is one Swarm's own rebase
// made and the branch was left at an earlier head of this delivery (the
// job's own, or one a previous rebase made): the rebase could not move it then
// (a dirty worktree, a busy task) or a daemon before this one left it. A rerun
// once the worktree is clean or the task idle so lands the head the steps
// checked. A branch at any other commit is left alone, and so is a head an
// agent step moved. It reports whether it moved the branch (from where);
// when it could not, the newest rebase record's branch says why and the
// deliverer's own check (BranchCarriesHead) refuses the step.
func (r *Runner) carryRebasedHead(ctx context.Context, d *Delivery, stepID string) (string, bool) {
	g := d.Result.Git
	if r.rebaser == nil || g == nil || strings.TrimSpace(g.Branch) == "" || !isCommitID(d.Head) || d.Head == g.Head {
		return "", false
	}
	idx := -1
	known := map[string]bool{g.Head: true}
	for i, rec := range d.Rebases {
		if rec.Outcome != RebaseRebased || rec.Trigger == RebaseTriggerAgent {
			continue
		}
		known[rec.From] = true
		if rec.To == d.Head {
			idx = i
		} else {
			known[rec.To] = true
		}
	}
	if idx < 0 {
		return "", false
	}
	tip, err := r.rebaser.TargetTip(ctx, d.Folder, g.Branch)
	if err != nil || tip == d.Head || !known[tip] {
		return "", false
	}
	update := r.moveJobBranch(ctx, d, stepID, tip, d.Head)
	d.Rebases[idx].Branch = &update
	r.save(*d)
	if !update.Updated {
		return "", false
	}
	rec := d.Rebases[idx]
	r.signal(*d, "automation.delivery.branch_moved", pebblestore.SignalSeverityInfo,
		fmt.Sprintf("Automation %s moved job %s's branch %s to its rebased head %s (from %s) before %s", d.AutomationName, d.JobID, g.Branch, short(d.Head), short(tip), stepID),
		stepID, map[string]string{"automation": d.AutomationName, "trigger": rec.Trigger, "target": rec.Target, "onto": rec.Onto, "from": rec.From, "to": rec.To,
			"rebase": strconv.Itoa(rec.N), "branch": g.Branch, "branch_updated": "true", "branch_from": tip}, strconv.Itoa(rec.N)+".branch")
	return tip, true
}

// resolveConflict gives the conflict of rebase idx to resolver agents, one
// round at a time (at most the delivery's rebase rounds): each works in a
// checkout of the kept conflicted state; Swarm takes exactly the conflicted
// files from it and continues the replay. A round whose work is refused
// (a conflict marker left) or whose job failed uses a round; a later
// commit that conflicts too continues in the next round.
func (r *Runner) resolveConflict(ctx context.Context, d *Delivery, idx int) rebaseOutcome {
	rounds := d.RebaseRounds
	if rounds <= 0 {
		rounds = DefaultRebaseRounds
	}
	for {
		rec := &d.Rebases[idx]
		c := rec.Conflict
		if c == nil || rec.State == nil {
			return r.unresolved(ctx, d, idx, "the conflicted replay was not recorded")
		}
		if c.Pending == "" {
			if rec.Trigger == RebaseTriggerRequest {
				return r.unresolved(ctx, d, idx, "a requested rebase starts no resolver: rerun the delivery to rebase with one")
			}
			if r.agent == nil {
				return r.unresolved(ctx, d, idx, "no agent can resolve a conflict on this daemon")
			}
			if c.Rounds >= rounds {
				return r.unresolved(ctx, d, idx, fmt.Sprintf("%d of %d resolver rounds used", c.Rounds, rounds))
			}
			key := fmt.Sprintf("%s.rebase%d.resolve%d", d.ID, rec.N, c.Rounds+1)
			if d.Reruns > 0 {
				key += ".rerun" + strconv.Itoa(d.Reruns)
			}
			jobID, err := r.agent.Start(ctx, AgentRequest{Account: d.Account, Folder: d.Folder, Worker: ResolverWorker, Brief: resolverBrief(*d, *rec),
				Result: d.Result, Head: c.Checkout, IdempotencyKey: key, Checkout: CheckoutNew})
			if err != nil {
				if ctx.Err() != nil {
					return rebaseOutcome{}
				}
				return r.unresolved(ctx, d, idx, stepReason("could not start the resolver agent: "+err.Error()))
			}
			c.Pending, c.Rounds = jobID, c.Rounds+1
			c.ResolverJobs = append(c.ResolverJobs, jobID)
			r.save(*d)
			r.signal(*d, "automation.delivery.resolver_started", pebblestore.SignalSeverityInfo,
				fmt.Sprintf("Automation %s: resolver job %s works on the rebase conflict of job %s (%s), round %d of %d", d.AutomationName, jobID, d.JobID, strings.Join(limitFiles(c.Files), ", "), c.Rounds, rounds),
				rec.Step, map[string]string{"automation": d.AutomationName, "resolver_job": jobID, "files": strings.Join(limitFiles(c.Files), ", "), "round": strconv.Itoa(c.Rounds)}, jobID)
		}
		waitCtx, cancel := context.WithTimeout(ctx, resolverTimeout)
		outcome, err := r.agent.Wait(waitCtx, d.Account, c.Pending)
		cancel()
		if ctx.Err() != nil {
			return rebaseOutcome{} // resumed with the same job on the next run
		}
		job := c.Pending
		c.Pending = ""
		if err != nil {
			c.Reason = stepReason("waiting for the resolver job " + job + ": " + err.Error())
			r.save(*d)
			continue
		}
		if NormalizeStatus(outcome.Status) != StatusSucceeded {
			c.Reason = stepReason("the resolver job " + job + " ended " + outcome.Status + ": " + truncate(outcome.Error, 300))
			r.save(*d)
			continue
		}
		dir := outcome.Worktree
		if dir == "" && outcome.Result != nil && outcome.Result.Git != nil {
			dir = outcome.Result.Git.Worktree
		}
		res, err := r.rebaser.Resume(ctx, d.Folder, *rec.State, *c, dir)
		var refused *ResolveRefusedError
		if errors.As(err, &refused) {
			c.Reason = stepReason("the resolver job " + job + "'s work was not taken: " + refused.Reason)
			r.save(*d)
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return rebaseOutcome{}
			}
			return r.unresolved(ctx, d, idx, stepReason("the replay could not continue after the resolver job "+job+": "+err.Error()))
		}
		switch res.Status {
		case RebaseConflicted:
			if res.Conflict == nil || res.State == nil {
				return r.unresolved(ctx, d, idx, "the replay stopped without a recorded conflict")
			}
			next := res.Conflict
			next.ResolverJobs, next.Rounds = c.ResolverJobs, c.Rounds
			rec.Conflict, rec.State, rec.Commits = next, res.State, res.Commits
			_ = r.rebaser.KeepRef(ctx, d.Folder, rebaseRefName("rebase-conflicts", d.ID, rec.N), next.Checkout)
			r.save(*d)
		case RebaseRebased:
			return r.finishRebase(ctx, d, idx, res)
		default:
			return r.unresolved(ctx, d, idx, "the replay ended "+res.Status)
		}
	}
}

// unresolved ends rebase idx as a conflict nobody resolved: the record keeps
// the files, the stopped commit, the target's commits, the resolver jobs
// and the conflicted state (refs/swarm/rebase-conflicts/...).
func (r *Runner) unresolved(ctx context.Context, d *Delivery, idx int, why string) rebaseOutcome {
	rec := &d.Rebases[idx]
	rec.Outcome, rec.Reason, rec.FinishedAt = RebaseConflicted, why, r.now().UnixMilli()
	if rec.Conflict == nil {
		rec.Conflict = &RebaseConflict{}
	}
	rec.Conflict.Outcome, rec.Conflict.Pending = ConflictUnresolved, ""
	c := *rec.Conflict
	if rec.Trigger != RebaseTriggerRequest {
		// A requested rebase leaves the delivery's own outcome alone.
		d.Conflict = &c
	}
	r.save(*d)
	attrs := map[string]string{"automation": d.AutomationName, "target": rec.Target, "onto": rec.Onto, "head": rec.From, "files": strings.Join(limitFiles(c.Files), ", "),
		"commit": c.Commit, "reason": why, "rebase": strconv.Itoa(rec.N)}
	if len(c.ResolverJobs) > 0 {
		attrs["resolver_jobs"] = strings.Join(c.ResolverJobs, ", ")
	}
	r.signal(*d, "automation.delivery.rebase_unresolved", pebblestore.SignalSeverityWarning,
		fmt.Sprintf("Automation %s: the rebase of job %s onto %s conflicted in %s and was not resolved: %s", d.AutomationName, d.JobID, rec.Target, strings.Join(limitFiles(c.Files), ", "), why),
		rec.Step, attrs, strconv.Itoa(rec.N))
	return rebaseOutcome{n: rec.N, branch: rec.Target, onto: rec.Onto, conflict: true}
}

// ConflictReason is the one line a delivery that ended conflict says.
func ConflictReason(d Delivery) string {
	c := d.Conflict
	if c == nil {
		return ""
	}
	target := ""
	if last := d.LastRebase(); last != nil {
		target = " onto " + last.Target
	}
	why := "the rebase" + target + " conflicted in " + strings.Join(limitFiles(c.Files), ", ")
	if c.Commit != "" {
		why += " replaying " + short(c.Commit)
		if c.Subject != "" {
			why += " (" + truncate(c.Subject, 120) + ")"
		}
	}
	if len(c.ResolverJobs) > 0 {
		why += "; resolver jobs " + strings.Join(c.ResolverJobs, ", ") + " did not resolve it"
	} else {
		why += "; no resolver ran"
	}
	if last := d.LastRebase(); last != nil && last.Reason != "" {
		why += " (" + last.Reason + ")"
	}
	return why
}

// ConflictNext is what to do about a delivery that ended conflict.
const ConflictNext = "resolve the conflict in the job (message it to rebase onto the target), then rerun the delivery (manage_jobs rerun_failed_steps), which rebases again"

// stopOnConflict ends the delivery conflict at step i: the step does not
// run (or, after it failed, keeps its failure), the rest are skipped.
func (r *Runner) stopOnConflict(d *Delivery, i int) {
	rec := &d.Steps[i]
	// A step that never ran (the automatic rebase before it conflicted)
	// says "not run"; one that ran and failed (a gate or push failing on a
	// moved target, a rebase step) keeps its own reason and adds the
	// conflict, unless it already says it.
	why := ConflictReason(*d)
	reason := "not run: " + why
	ran := rec.StartedAt > 0 || rec.Attempts > 0 || len(rec.Failures) > 0
	if ran && strings.TrimSpace(rec.Reason) != "" {
		reason = rec.Reason + "; " + why
		if strings.Contains(rec.Reason, why) {
			reason = rec.Reason
		}
	}
	rec.State, rec.Reason, rec.Next, rec.NextAttemptAt, rec.FinishedAt = StateFailed, stepReason(reason), ConflictNext, 0, r.now().UnixMilli()
	if rec.Outcome == nil {
		rec.Outcome = map[string]any{}
	}
	if last := d.LastRebase(); last != nil {
		rec.Outcome["rebase"] = last.N
	}
	r.skipRest(d, i, "not run: the rebase conflicted")
	r.finish(d, StateConflict)
}

// headMovedOnlyByRebase reports whether the delivery head is the job's own
// head, or that head carried forward only by Swarm's own rebases (automatic,
// a rebase step, on request): each rebase record's from is the head before it
// and its to the head after. A head an agent step moved (its commits, or its
// own rebase: trigger agent_step) breaks the chain.
func headMovedOnlyByRebase(d Delivery) bool {
	if d.Result.Git == nil || d.Head == "" || d.Result.Git.Head == "" {
		return false
	}
	cur := d.Result.Git.Head
	for _, rec := range d.Rebases {
		if rec.Outcome == RebaseRebased && rec.Trigger != RebaseTriggerAgent && rec.From == cur && isCommitID(rec.To) {
			cur = rec.To
		}
	}
	return cur == d.Head
}

// rerunAfterRebase makes the steps before at whose result depended on the
// head run again on the rebased head (rebase n): run steps that ran, review
// steps that recorded a verdict (not a reviewer that fixed: its fixes are
// in the head), and, after the first of them, skipped steps (their
// conditions are read again). includeAt also resets step at-1 (the step
// that failed). It returns the first step reset, -1 for none.
func rerunAfterRebase(d *Delivery, at, n int, includeAt bool) int {
	first := -1
	names := []string{}
	for j := 0; j < at && j < len(d.Steps); j++ {
		rec := &d.Steps[j]
		step, _ := d.step(rec.ID)
		again := false
		switch {
		case includeAt && j == at-1:
			again = true
		case rec.Type == StepRun && (rec.State == StateSucceeded || rec.State == StateFailed || rec.State == StateError):
			again = true
		case rec.Type == StepAgent && rec.State == StateSucceeded && step.Fixes == "" && step.ForEach == nil:
			_, verdict := rec.Outcome["verdict"]
			_, moved := rec.Outcome["moved_head"]
			again = verdict && !moved
		case rec.State == StateSkipped && first >= 0:
			again = true
		}
		if !again {
			continue
		}
		if first < 0 {
			first = j
		}
		resetStep(rec)
		rec.RebaseRerun = n
		rec.Note = fmt.Sprintf("runs again on the rebased head %s (rebase %d): it ran on the old head", short(d.Head), n)
		names = append(names, rec.ID)
	}
	for i := range d.Rebases {
		if d.Rebases[i].N == n {
			d.Rebases[i].Rerun = append(d.Rebases[i].Rerun, names...)
		}
	}
	return first
}

// runRebaseStep is a rebase step: the daemon's rebase onto the step's
// branch (default the job's target) at that point of the workflow.
func (r *Runner) runRebaseStep(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	out := r.rebaseDelivery(ctx, d, RebaseTriggerExplicit, step.ID, step.Onto, true)
	if ctx.Err() != nil {
		return stepFailure{}
	}
	switch {
	case out.conflict:
		record.Outcome = map[string]any{"rebase": out.n, "onto": out.onto, "target": out.branch, "conflict": d.Conflict}
		return stepFailure{reason: stepReason(ConflictReason(*d)), next: ConflictNext, refused: true}
	case out.err != "":
		return stepFailure{reason: stepReason("rebase: " + out.err), next: "rerun"}
	case out.skipped:
		record.Output = "rebase onto " + out.branch + " at " + short(out.onto) + " " + out.reason
		record.Outcome = map[string]any{"rebase": out.n, "onto": out.onto, "target": out.branch, "skipped": true, "reason": out.reason}
	case out.upToDate:
		record.Output = "up to date with " + out.branch + " at " + short(out.onto)
		record.Outcome = map[string]any{"up_to_date": true, "onto": out.onto, "target": out.branch}
		if out.inTarget {
			record.Output = out.branch + " at " + short(out.onto) + " already contains the work: nothing to rebase"
			record.Outcome["in_target"] = true
		}
	case out.moved:
		last := d.LastRebase()
		record.Output = fmt.Sprintf("rebased onto %s at %s: head %s is now %s", out.branch, short(out.onto), short(last.From), short(last.To))
		record.Outcome = map[string]any{"rebase": out.n, "onto": out.onto, "target": out.branch, "rebased": true,
			"moved_head": map[string]any{"from": last.From, "to": last.To, "rebased": true}}
	}
	return stepFailure{}
}

// acceptRebasedHead accepts an agent step's head that does not descend from
// the delivery head because the agent rebased it: it must descend from the
// target tip (the one its step was told, the one last checked or read
// now), and each of the delivery head's commits must map by patch id to a
// distinct commit of it (or be in the target already). Its other commits
// are the agent's own (Added). The old head is kept as rebased_from.
func (r *Runner) acceptRebasedHead(ctx context.Context, d *Delivery, outcome AgentOutcome, worktree string, out map[string]any) (bool, string) {
	g := d.Result.Git
	if r.rebaser == nil || g == nil || strings.TrimSpace(g.BaseBranch) == "" || !isCommitID(outcome.Head) || !isCommitID(d.Head) {
		return false, ""
	}
	candidates := []string{}
	add := func(c string) {
		if !isCommitID(c) {
			return
		}
		for _, have := range candidates {
			if have == c {
				return
			}
		}
		candidates = append(candidates, c)
	}
	if onto, _ := out["rebase_onto"].(string); onto != "" {
		add(onto)
	}
	add(d.TargetChecked)
	if last := d.LastRebase(); last != nil {
		add(last.Onto)
	}
	if tip, err := r.rebaser.TargetTip(ctx, d.Folder, g.BaseBranch); err == nil {
		add(tip)
	}
	why := ""
	for _, onto := range candidates {
		m, err := r.rebaser.MapCommits(ctx, d.Folder, d.Head, onto, outcome.Head)
		if err != nil {
			why = err.Error()
			continue
		}
		if len(m.Unmatched) > 0 {
			why = "the delivery head's commits " + strings.Join(limitFiles(shortAll(m.Unmatched)), ", ") + " are not in it (no commit with the same patch id)"
			continue
		}
		from := d.Head
		idx := d.nextRebase(RebaseRecord{Trigger: RebaseTriggerAgent, Job: outcome.JobID, Target: g.BaseBranch, Onto: onto, From: from, To: outcome.Head,
			Outcome: RebaseRebased, Commits: m.Commits, Added: m.Added, At: r.now().UnixMilli(), FinishedAt: r.now().UnixMilli()})
		out["moved_head"] = map[string]any{"from": from, "to": outcome.Head, "rebased": true, "onto": onto}
		out["rebased_from"], out["rebased_onto"], out["rebase"] = from, onto, d.Rebases[idx].N
		d.RebasedFrom, d.RebasedOnto = from, onto
		d.Head, d.HeadWorktree = outcome.Head, worktree
		d.TargetChecked = onto
		r.signal(*d, "automation.delivery.rebased", pebblestore.SignalSeverityInfo,
			fmt.Sprintf("Automation %s accepted job %s's rebased head %s onto %s at %s (it was %s)", d.AutomationName, outcome.JobID, short(outcome.Head), g.BaseBranch, short(onto), short(from)),
			"", map[string]string{"automation": d.AutomationName, "trigger": RebaseTriggerAgent, "job": outcome.JobID, "target": g.BaseBranch, "onto": onto, "from": from, "to": outcome.Head,
				"rebase": strconv.Itoa(d.Rebases[idx].N)}, strconv.Itoa(d.Rebases[idx].N))
		return true, ""
	}
	return false, why
}

func shortAll(list []string) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = short(c)
	}
	return out
}

// resolverBrief is what a resolver agent is told: the conflicted files, the
// commit the replay stopped at, the target's commits and both sides'
// changes, and that it edits only those files and runs no Git.
func resolverBrief(d Delivery, rec RebaseRecord) string {
	c := rec.Conflict
	var b strings.Builder
	fmt.Fprintf(&b, "Resolve a rebase conflict so Swarm can finish rebasing job %s onto %s.\n\n", d.JobID, rec.Target)
	fmt.Fprintf(&b, "Swarm replays the job's commits onto %s at %s. Replaying the job's commit %s", rec.Target, rec.Onto, c.Commit)
	if c.Subject != "" {
		fmt.Fprintf(&b, " (%q)", truncate(c.Subject, 200))
	}
	b.WriteString(" conflicted in these files:\n")
	for _, f := range c.Files {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\nYour worktree is a checkout of that conflicted state: those files hold conflict markers. Between <<<<<<< and ======= is the target branch's side (with the job's earlier commits already replayed); between ======= and >>>>>>> is the job's commit.\n\n")
	b.WriteString("Edit only those files: keep the intent of both sides and remove every conflict marker. Do not run git (no add, commit, rebase, merge, checkout, stash or reset) and change no other file: Swarm takes exactly these files from your worktree, refuses any conflict marker left in them, stages them and continues the rebase itself.\n")
	if len(c.TargetCommits) > 0 {
		b.WriteString("\nThe target's commits that changed these files since the job's base:\n")
		for _, t := range c.TargetCommits {
			fmt.Fprintf(&b, "- %s %s\n", short(t.SHA), t.Subject)
		}
	}
	if c.Reason != "" {
		b.WriteString("\nThe previous attempt was not accepted: " + c.Reason + "\n")
	}
	if c.TargetDiff != "" {
		b.WriteString("\nThe target's change to these files since the job's base:\n```diff\n" + c.TargetDiff + "\n```\n")
	}
	if c.JobDiff != "" {
		b.WriteString("\nThe job's commit's change to these files:\n```diff\n" + c.JobDiff + "\n```\n")
	}
	b.WriteString("\nEnd with a short report naming each file and how you resolved it.")
	out := b.String()
	if len(out) > maxResolverBrief {
		out = clip(out, maxResolverBrief)
	}
	return out
}

func (r *Runner) save(d Delivery) {
	save := r.store.SaveDelivery
	if d.RebaseHold != nil {
		save = r.store.SaveHeldDelivery // an on-demand rebase's own writes
	}
	if err := save(d); err != nil {
		log.Printf("swarmd job automations: save delivery %s: %v", d.ID, err)
	}
}

// ---- on request ----------------------------------------------------------------

// RebaseRequestOutcome is what an on-demand rebase of a delivery did.
type RebaseRequestOutcome struct {
	Delivery Delivery
	// Outcome is up_to_date, rebased, skipped, conflict or error; Record the
	// rebase's record (nil when up to date).
	Outcome string
	Target  string
	Onto    string
	Record  *RebaseRecord
	Reason  string
}

// ErrDeliveryRunning refuses an on-demand rebase of a delivery that is
// running: it rebases itself before its next step.
var ErrDeliveryRunning = errors.New("the delivery is running: it rebases itself before its next step that depends on the head")

// RebaseDelivery rebases a finished delivery's head onto branch (\"\" the
// job's target branch) on request (manage_jobs rebase, POST
// /v3/job-deliveries/{id}/rebase), with the same function the runner uses.
// A clean replay moves the head and the job's branch; a conflict is
// recorded and reported, with no resolver (a rerun of the delivery
// rebases again and gives a conflict to the resolver). It does not re-run
// any step.
func (r *Runner) RebaseDelivery(ctx context.Context, account, id, branch string) (RebaseRequestOutcome, error) {
	if r.rebaser == nil {
		return RebaseRequestOutcome{}, &RefusedError{Reason: "this daemon cannot rebase"}
	}
	branch = strings.TrimSpace(branch)
	if branch != "" && !ValidBranchName(branch) {
		return RebaseRequestOutcome{}, &RefusedError{Reason: "onto must be a branch name"}
	}
	r.mu.Lock()
	if r.active[id] {
		r.mu.Unlock()
		return RebaseRequestOutcome{}, ErrDeliveryRunning
	}
	r.active[id] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
	}()
	// The store holds the delivery for this rebase: a rerun is refused
	// until it is released, and every save of it is checked against the
	// hold, so neither overwrites the other.
	d, err := r.store.HoldForRebase(account, id, randomToken(), r.now().UnixMilli())
	if err != nil {
		return RebaseRequestOutcome{Delivery: d}, err
	}
	// No resolver on request: a conflict is recorded as it stands, and the
	// delivery's own state is left alone.
	out := r.rebaseDelivery(ctx, &d, RebaseTriggerRequest, "", branch, true)
	result := RebaseRequestOutcome{Target: out.branch, Onto: out.onto, Reason: out.err}
	switch {
	case out.err != "":
		result.Outcome = RebaseError
	case out.upToDate:
		result.Outcome = RebaseUpToDate
		if out.inTarget {
			result.Reason = out.branch + " already contains the work: nothing was moved"
		}
	case out.conflict:
		result.Outcome = RebaseConflicted
	case out.skipped:
		result.Outcome, result.Reason = RebaseSkipped, out.reason
	case out.moved:
		result.Outcome = RebaseRebased
	default:
		result.Outcome = RebaseUpToDate
	}
	if out.n > 0 {
		result.Record = d.LastRebase()
	}
	released, err := r.store.ReleaseRebaseHold(d)
	if err != nil {
		return RebaseRequestOutcome{Delivery: d}, err
	}
	result.Delivery = released
	return result, nil
}

func randomToken() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// RebaseEnabled reports whether this daemon can rebase deliveries.
func (r *Runner) RebaseEnabled() bool { return r != nil && r.rebaser != nil }
