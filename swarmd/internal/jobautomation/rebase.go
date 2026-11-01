package jobautomation

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// RebaseChecker is the Git a rebase step's checks need (Runner.rebaseHead).
// The daemon's agent runner implements it with routed Git in the folder.
type RebaseChecker interface {
	// TargetTip is the commit branch names in the folder now.
	TargetTip(ctx context.Context, folder, branch string) (string, error)
	// ChangedFiles lists the files the tree at head changes against base.
	ChangedFiles(ctx context.Context, folder string, base, head HeadRef) ([]string, error)
}

// TaskBranchAdvance asks to bring a project task's own branch and worktree
// to a head an agent step moved the delivery to.
type TaskBranchAdvance struct {
	Account    string
	JobID      string
	DeliveryID string
	Step       string
	// Branch is the delivery's branch: the task's branch must be it.
	Branch string
	// From is the delivery head before the move, To the head it moved to.
	From string
	To   HeadRef
	// Rebased is a move to a rebased head (To does not descend from From):
	// the branch moves only while it is at From, with a clean, idle
	// worktree (git reset --keep), never by a merge.
	Rebased bool
}

// TaskBranchSync is what became of a TaskBranchAdvance: Advanced when the
// task's branch and worktree now stand at Head, else Reason says why not
// (not a fast-forward, a dirty worktree, a running task, another branch).
type TaskBranchSync struct {
	Step     string `json:"step,omitempty"`
	From     string `json:"from,omitempty"`
	Head     string `json:"head"`
	Advanced bool   `json:"advanced"`
	Reason   string `json:"reason,omitempty"`
	At       int64  `json:"at,omitempty"`
}

// TaskBranchAdvancer fast-forwards a task's branch and worktree (the
// daemon's agent runner, with routed Git) and records the outcome on the
// task. It never fails the step: a branch it cannot move is reported.
type TaskBranchAdvancer interface {
	AdvanceTaskBranch(ctx context.Context, req TaskBranchAdvance) TaskBranchSync
}

// RebaseConflictsPrefix starts the handoff line on which a rebase step's
// agent names the files its rebase conflicted on ("none" for none).
const RebaseConflictsPrefix = "Rebase conflicts:"

// rebaseBrief is what a rebase step's agent is told on top of its brief:
// what to rebase onto, and how its head is accepted.
func rebaseBrief(branch, onto string) string {
	return "\n\nRebase: your worktree starts at the delivery head. Rebase it onto " + branch + " at " + onto +
		" (git rebase " + onto + "), resolve any conflicts and finish the rebase, leaving no uncommitted changes. " +
		"In your handoff, add one line naming the files the rebase conflicted on: \"" + RebaseConflictsPrefix + " <path>, <path>\" or \"" + RebaseConflictsPrefix + " none\". " +
		"Your head moves the delivery head only if it descends from " + onto + " and changes the same files as before, plus only the files you name."
}

// RebaseConflicts reads the files a rebase step's handoff names on its
// RebaseConflictsPrefix line (the last such line); none when there is none.
func RebaseConflicts(handoff string) []string {
	files := []string{}
	for _, line := range strings.Split(handoff, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*> "))
		if len(line) < len(RebaseConflictsPrefix) || !strings.EqualFold(line[:len(RebaseConflictsPrefix)], RebaseConflictsPrefix) {
			continue
		}
		files = []string{}
		rest := strings.TrimSpace(line[len(RebaseConflictsPrefix):])
		if strings.EqualFold(strings.Trim(rest, ". "), "none") {
			continue
		}
		for _, part := range strings.Split(rest, ",") {
			if name := strings.Trim(strings.TrimSpace(part), "`'\""); name != "" {
				files = append(files, name)
			}
		}
	}
	return files
}

// rebaseTarget is the tip of the delivery's target branch a rebase step is
// told to rebase onto, read when the step starts.
func (r *Runner) rebaseTarget(ctx context.Context, d *Delivery) (branch, onto string, failure stepFailure) {
	if r.rebase == nil {
		return "", "", stepFailure{reason: "rebase steps are not available on this daemon", next: "rerun", refused: true}
	}
	g := d.Result.Git
	if g == nil || strings.TrimSpace(g.BaseBranch) == "" || d.Head == "" {
		return "", "", stepFailure{reason: "nothing to rebase: the job has no commits on a branch with a known target branch", next: "fix the automation", refused: true}
	}
	tip, err := r.rebase.TargetTip(ctx, d.Folder, g.BaseBranch)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", stepFailure{}
		}
		return "", "", stepFailure{reason: stepReason("read the tip of " + g.BaseBranch + ": " + err.Error()), next: "rerun"}
	}
	if !isCommitID(tip) {
		return "", "", stepFailure{reason: "the tip of " + g.BaseBranch + " is not a commit id", next: "rerun", refused: true}
	}
	return g.BaseBranch, tip, stepFailure{}
}

// rebaseHead moves the delivery head to a rebase step's head. A head that
// descends from the delivery head is an ordinary move (moveHead). A head
// that does not (the rebase rewrote history) moves it only when all hold:
//
//   - it descends from the target branch tip the step was told to rebase
//     onto (rebase_onto, read when the step started);
//   - the job reported no uncommitted changes;
//   - the files it changes against that tip are the files the old head
//     changed against the old base, give or take only the files the
//     handoff names on its "Rebase conflicts:" line.
//
// It then records rebased_from on the delivery. Any check that fails
// refuses the step and keeps the head.
func (r *Runner) rebaseHead(ctx context.Context, d *Delivery, outcome AgentOutcome, out map[string]any) stepFailure {
	if outcome.Head == "" || d.Head == "" || outcome.Head == d.Head {
		return stepFailure{}
	}
	if NormalizeStatus(outcome.Status) != StatusSucceeded {
		return stepFailure{} // the job's own failure is reported by the caller
	}
	if r.verify == nil {
		return stepFailure{reason: "the job " + outcome.JobID + " committed, but this daemon cannot verify its head against the delivery head", next: "rerun", refused: true}
	}
	worktree := ""
	if outcome.Result != nil && outcome.Result.Git != nil {
		worktree = outcome.Result.Git.Worktree
	}
	newHead := HeadRef{Commit: outcome.Head, Worktree: worktree}
	descends, err := r.verify(ctx, d.Folder, HeadRef{Commit: d.Head, Worktree: d.HeadWorktree}, newHead)
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: stepReason("could not verify the job's head against the delivery head: " + err.Error()), next: "rerun"}
	}
	if descends {
		return r.moveHead(ctx, d, outcome, out)
	}
	refuse := func(why string) stepFailure {
		out["rebase_refused"] = truncate(why, 1000)
		return stepFailure{reason: stepReason("rebase not accepted: " + why + "; the delivery head is not moved"),
			next: "rebase by hand (message the task), then rerun", refused: true}
	}
	onto, _ := out["rebase_onto"].(string)
	if !isCommitID(onto) {
		return refuse("the step recorded no target branch tip to check the rebase against")
	}
	if r.rebase == nil {
		return refuse("this daemon cannot read the files a rebase changes")
	}
	ok, err := r.verify(ctx, d.Folder, HeadRef{Commit: onto}, newHead)
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: stepReason("could not verify the rebased head against the target tip: " + err.Error()), next: "rerun"}
	}
	if !ok {
		return refuse("head " + outcome.Head + " does not descend from the target branch tip " + onto)
	}
	switch {
	case outcome.ResultError != "":
		return refuse("the job's result could not be read: " + truncate(outcome.ResultError, 200))
	case outcome.Result == nil:
		return refuse("the job reported no result, so its uncommitted changes are unknown")
	case len(outcome.Result.Uncommitted) > 0:
		return refuse("the job left uncommitted changes: " + strings.Join(limitFiles(outcome.Result.Uncommitted), ", "))
	}
	oldBase := d.RebasedOnto
	if oldBase == "" && d.Result.Git != nil {
		oldBase = d.Result.Git.BaseCommit
	}
	if !isCommitID(oldBase) {
		return refuse("the delivery has no base commit to compare the rebase with")
	}
	oldFiles, err := r.rebase.ChangedFiles(ctx, d.Folder, HeadRef{Commit: oldBase}, HeadRef{Commit: d.Head, Worktree: d.HeadWorktree})
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: stepReason("list the files the delivery head changes: " + err.Error()), next: "rerun"}
	}
	newFiles, err := r.rebase.ChangedFiles(ctx, d.Folder, HeadRef{Commit: onto}, newHead)
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		return stepFailure{reason: stepReason("list the files the rebased head changes: " + err.Error()), next: "rerun"}
	}
	handoff, _ := out["handoff"].(string)
	if strings.TrimSpace(outcome.Handoff) != "" {
		handoff = outcome.Handoff
	}
	conflicts := RebaseConflicts(handoff)
	if unexplained := unexplainedFiles(oldFiles, newFiles, conflicts); len(unexplained) > 0 {
		return refuse("the rebased head changes other files than the delivery head did, beyond the files its handoff names as conflicted (\"" +
			RebaseConflictsPrefix + " ...\"): " + strings.Join(limitFiles(unexplained), ", "))
	}
	out["moved_head"] = map[string]any{"from": d.Head, "to": outcome.Head, "rebased": true}
	out["rebased_from"], out["rebased_onto"], out["rebase_conflicts"] = d.Head, onto, conflicts
	d.RebasedFrom, d.RebasedOnto = d.Head, onto
	d.Head, d.HeadWorktree = outcome.Head, worktree
	return stepFailure{}
}

// unexplainedFiles is the files in exactly one of before and after that are
// not among conflicts, sorted.
func unexplainedFiles(before, after, conflicts []string) []string {
	in := func(list []string) map[string]bool {
		set := map[string]bool{}
		for _, name := range list {
			set[strings.TrimSpace(name)] = true
		}
		return set
	}
	a, b, c := in(before), in(after), in(conflicts)
	out := []string{}
	for name := range a {
		if !b[name] && !c[name] {
			out = append(out, name)
		}
	}
	for name := range b {
		if !a[name] && !c[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func limitFiles(files []string) []string {
	if len(files) > 10 {
		return append(append([]string(nil), files[:10]...), fmt.Sprintf("and %d more", len(files)-10))
	}
	return files
}

// syncTaskBranch brings the triggering task's own branch and worktree to
// the head an agent step just moved the delivery to (from is the head
// before), so a later follow-up of the task starts from the delivered work.
// Only a project task whose branch is the delivery's branch is moved, and
// only by a fast-forward of a clean worktree (TaskBranchAdvancer); why it
// was not moved is recorded on the delivery (and by the advancer on the
// task) and signalled. It never fails the step.
func (r *Runner) syncTaskBranch(ctx context.Context, d *Delivery, stepID, from string, out map[string]any) {
	if r.branch == nil || d.Result.Kind != KindTask || d.Result.Git == nil || strings.TrimSpace(d.Result.Git.Branch) == "" || from == d.Head {
		return
	}
	sync := r.branch.AdvanceTaskBranch(ctx, TaskBranchAdvance{Account: d.Account, JobID: d.JobID, DeliveryID: d.ID, Step: stepID, Branch: d.Result.Git.Branch,
		From: from, To: HeadRef{Commit: d.Head, Worktree: d.HeadWorktree}, Rebased: from != "" && d.RebasedFrom == from})
	sync.Step, sync.From, sync.At = stepID, from, r.now().UnixMilli()
	if sync.Head == "" {
		sync.Head = d.Head
	}
	sync.Reason = stepReason(sync.Reason)
	d.TaskBranch = &sync
	out["task_branch"] = map[string]any{"head": sync.Head, "advanced": sync.Advanced, "reason": sync.Reason}
	if sync.Advanced {
		return
	}
	r.signal(*d, "automation.task_branch.not_advanced", pebblestore.SignalSeverityWarning,
		fmt.Sprintf("Automation %s moved the delivery head of %s to %s, but the task's branch %s was not moved: %s", d.AutomationName, d.JobID, d.Head, d.Result.Git.Branch, sync.Reason),
		stepID, map[string]string{"automation": d.AutomationName, "step": stepID, "head": d.Head, "branch": d.Result.Git.Branch, "reason": sync.Reason}, "")
}

// HandoverRerunStep is the failed run step a same-head hand-over of the job
// re-runs the delivery from: the delivery failed on a run step, or a reopen
// step sent the work back because a run step failed or could not run
// (ReopenCause check_failed, push_failed or step_error: its `if status:
// failed` or `if status: error` conditions). ok is false for any other delivery (a
// review block, a failed agent step, one still running).
func HandoverRerunStep(d Delivery) (stepID string, ok bool) {
	at := handoverRerunIndex(d)
	if at < 0 {
		return "", false
	}
	return d.Steps[at].ID, true
}

func handoverRerunIndex(d Delivery) int {
	index := func(id string) int {
		for i := range d.Steps {
			if d.Steps[i].ID == id {
				return i
			}
		}
		return -1
	}
	switch d.State {
	case StateFailed, StateError:
		for i, step := range d.Steps {
			if step.State == StateFailed || step.State == StateError {
				if step.Type == StepRun {
					return i
				}
				return -1
			}
		}
	case StateSentBack:
		if d.SentBack == nil {
			return -1
		}
		cause, _ := ReopenCause(d, d.SentBack.Step)
		switch cause {
		case pebblestore.ProjectTaskReopenCheckFailed, pebblestore.ProjectTaskReopenPushFailed, pebblestore.ProjectTaskReopenStepError:
		default:
			return -1
		}
		step, _ := d.step(d.SentBack.Step)
		for _, c := range step.If {
			if c.Status != StateFailed && c.Status != StateError {
				continue
			}
			if at := index(c.Step); at >= 0 && d.Steps[at].Type == StepRun && d.Steps[at].State == c.Status {
				return at
			}
		}
	}
	return -1
}

// rerunOnHandover re-runs the job's newest delivery of the automation from
// its failed run step when the job handed its work over again with no new
// commit (a task's follow-up, from message_task or reopen_task, ended at
// the head the delivery already covers). That is the documented rerun
// (POST /v3/job-deliveries/{id}/rerun), done because a gate that failed for
// a reason outside the code (a broken gate script, a flaky test, a provider
// outage) has nothing to fix in a new commit. It reports whether it re-ran.
func (r *Runner) rerunOnHandover(existing Delivery, result Result) bool {
	if result.Followup == "" || result.Kind != KindTask || NormalizeStatus(result.Status) != StatusSucceeded {
		return false
	}
	stepID, ok := HandoverRerunStep(existing)
	if !ok || existing.HandoverRerun == result.Followup {
		return false
	}
	// Only the automation's newest delivery for the job re-runs: an older
	// one at the same head was superseded.
	list, err := r.store.JobDeliveries(existing.Account, existing.JobID)
	if err != nil {
		log.Printf("swarmd job automations: read deliveries of job %s: %v", existing.JobID, err)
		return false
	}
	for _, p := range list {
		if p.AutomationID == existing.AutomationID {
			if p.ID != existing.ID {
				return false
			}
			break
		}
	}
	outcome, err := r.store.RerunAfterHandover(existing.Account, existing.ID, result.Followup)
	if err != nil {
		log.Printf("swarmd job automations: re-run delivery %s after job %s handed over at the same head: %v", existing.ID, result.JobID, err)
		return false
	}
	if !outcome.Rerun {
		return false
	}
	d := outcome.Delivery
	if r.emit != nil {
		r.emit(pebblestore.Signal{Kind: "automation.delivery.rerun", Severity: pebblestore.SignalSeverityInfo, Account: d.Account,
			Summary: truncate(fmt.Sprintf("Automation %s: job %s handed its work over again with no new commit; delivery %s re-runs from step %s, which failed (the rerun of POST /v3/job-deliveries/%s/rerun)",
				d.AutomationName, d.JobID, d.ID, stepID, d.ID), 400),
			DedupKey: "automation.delivery.rerun:" + d.ID + ":" + result.Followup,
			Refs:     map[string]string{"job_id": d.JobID, "delivery_id": d.ID, "step_id": stepID},
			Attrs:    map[string]string{"automation": d.AutomationName, "reason": "handover_same_head", "step": stepID, "followup": result.Followup, "head": d.Head}})
	}
	r.Wake()
	return true
}
