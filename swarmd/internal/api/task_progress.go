package api

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Task progress (docs/task-progress.md): one answer per task to "where is it
// and what happened", computed from the records Swarm already keeps (the
// task and its attempts, its job's workflow deliveries, the evaluation of
// its job end, its integration). It is a view; nothing here decides.

// Progress states.
const (
	progressWaiting       = "waiting"
	progressWorking       = "working"
	progressWorkflow      = "workflow"
	progressNotIntegrated = "not_integrated"
	progressIntegrated    = "integrated"
	progressDelivered     = "delivered"
	progressDone          = "done"
	progressFailed        = "failed"
	// progressStopped: the person stopped the task's run; a message resumes
	// it. Not a failure.
	progressStopped = "stopped"
)

// Workflow states (workflow.state and each delivery's state).
const (
	workflowNone    = "none"
	workflowPending = "pending"
	workflowRunning = "running"
	workflowDone    = "done"
	workflowFailed  = "failed"
	workflowBlocked = "blocked"
	// workflowSentBack: a reopen step sent the work back (review findings,
	// a failed push to rebase); nothing was handed over.
	workflowSentBack = "sent_back"
)

const (
	maxProgressHistory = 50
	maxProgressSummary = 160
)

type taskProgress struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
	// Outcome is what happened to the task's work, from its latest workflow
	// delivery (taskProgressOutcome); "" while nothing has happened yet.
	Outcome string `json:"outcome,omitempty"`
	// Stale marks an in_progress or blocked task with no active run for
	// taskStaleAfterMillis (taskProgressStale).
	Stale bool `json:"stale,omitempty"`
	// Idle marks a finished task that has not changed for
	// taskIdleAfterMillis (taskProgressIdle): list counts put one with
	// work still to hand over under idle, not needs_attention.
	Idle bool `json:"idle,omitempty"`
	// NeedsAttention says the person must act on the task
	// (pebblestore.ProjectTaskNeedsAttention, the rule list counts use):
	// blocked, failed, waiting for approval or a question, or work no
	// workflow hands over and nobody integrated.
	NeedsAttention bool `json:"needs_attention"`
	// TaskState is the task's state (pebblestore.ProjectTaskState, the word
	// list_tasks, get_task and the Orchestrator's prompt show) with its
	// one-line reason; the record's status is only its run's status.
	TaskState       string `json:"task_state,omitempty"`
	TaskStateReason string `json:"task_state_reason,omitempty"`
	// Live is what the task's working run is doing now when its record
	// cannot say (task_progress_live.go): waiting on a pending permission
	// or question (with the request), or held by an update drain or
	// execution admission. TaskState reads it (ProjectTaskStateLive).
	Live        *pebblestore.ProjectTaskLive `json:"live,omitempty"`
	Workflow    *taskProgressWorkflow        `json:"workflow,omitempty"`
	Integration *taskProgressIntegration     `json:"integration,omitempty"`
	History     []taskProgressEvent          `json:"history"`
	UpdatedAt   int64                        `json:"updated_at"`
}

type taskProgressWorkflow struct {
	State      string                 `json:"state"`
	Deliveries []taskProgressDelivery `json:"deliveries"`
}

type taskProgressDelivery struct {
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	State    string `json:"state"`
	Step     int    `json:"step"`
	Steps    int    `json:"steps"`
	StepType string `json:"step_type"`
	Round    int    `json:"round"`
	// SentBack is the reopen step, round and reason of a sent_back delivery.
	SentBack *jobautomation.SentBack `json:"sent_back,omitempty"`
	// ReopenCause, ReopenFailure and ReopenRound describe the reopen step
	// the delivery stands at (sent_back, or a reopen step running):
	// review_block (findings to address), push_failed (the review passed,
	// the push failed: rebase) or check_failed, the failed step's short
	// reason and the follow-up's round (deliveryReopen).
	ReopenCause   string `json:"reopen_cause,omitempty"`
	ReopenFailure string `json:"reopen_failure,omitempty"`
	ReopenRound   int    `json:"reopen_round,omitempty"`
}

// deliveryReopen is the reopen the delivery stands at, if any: the reopen
// step that sent it back (sent_back), or the reopen step running now (its
// follow-up is the next round). ok is false when it stands at no reopen.
func deliveryReopen(d jobautomation.Delivery, step int) (pebblestore.ProjectTaskReopen, bool) {
	stepID, round := "", 0
	switch {
	case d.State == jobautomation.StateSentBack && d.SentBack != nil:
		stepID, round = d.SentBack.Step, d.SentBack.Round
		if round <= 0 {
			round = d.Round
		}
	case d.State == jobautomation.StateSentBack:
		if step >= 0 && step < len(d.Steps) {
			stepID = d.Steps[step].ID
		}
		round = d.Round
	case !jobautomation.Terminal(d.State) && step >= 0 && step < len(d.Steps) && d.Steps[step].Type == jobautomation.StepReopen:
		stepID, round = d.Steps[step].ID, d.Round+1
	default:
		return pebblestore.ProjectTaskReopen{}, false
	}
	cause, failure := jobautomation.ReopenCause(d, stepID)
	return pebblestore.ProjectTaskReopen{Round: round, Cause: cause, Failure: failure}.Normalized(), true
}

type taskProgressIntegration struct {
	State   string `json:"state"`
	Branch  string `json:"branch"`
	Target  string `json:"target"`
	Commits int    `json:"commits"`
	Commit  string `json:"commit"`
	At      int64  `json:"at"`
}

type taskProgressEvent struct {
	At   int64  `json:"at"`
	What string `json:"what"`
}

// taskStatusClass sorts a task status into waiting, working, stopped, failed
// or finished.
func taskStatusClass(status string) string {
	switch status {
	case pebblestore.ProjectTaskStatusStopped:
		return progressStopped
	case "pending_approval", "queued", "pending", pebblestore.ProjectTaskStatusLimitWaiting:
		return progressWaiting
	case "planning", "in_progress", "running", "starting", "generating", "swarming", "publishing":
		return progressWorking
	case "failed", "blocked", "error", "cancelled", "interrupted", "expired", "rejected":
		return progressFailed
	}
	return "finished"
}

func mediaTaskAgent(agent string) bool {
	return agent == "image" || agent == "video" || agent == "sound" || agent == "audio"
}

// taskProgressJobID is the workflow job id of a task's job.
func taskProgressJobID(task pebblestore.ProjectTaskRecord) string {
	switch {
	case task.WorkerID != "" && task.WorkerRunID != "":
		return controlMCPJobID(controlMCPJobWorker, task.WorkerID, task.WorkerRunID)
	case task.Design != nil:
		return controlMCPJobID(controlMCPJobDesign, task.ProjectID, task.ID)
	case mediaTaskAgent(task.Agent):
		return controlMCPJobID(controlMCPJobMedia, task.ProjectID, task.ID)
	}
	return taskRunJobID(task.ProjectID, task.ID)
}

// latestDeliveries keeps each workflow's newest delivery (its latest round),
// newest first; list is newest first (Store.JobDeliveries).
func latestDeliveries(list []jobautomation.Delivery) []jobautomation.Delivery {
	seen := map[string]bool{}
	out := []jobautomation.Delivery{}
	for _, d := range list {
		if seen[d.AutomationID] {
			continue
		}
		seen[d.AutomationID] = true
		out = append(out, d)
	}
	return out
}

func deliveryProgressState(state string) string {
	switch state {
	case jobautomation.StateSucceeded, jobautomation.StateSkipped:
		return workflowDone
	case jobautomation.StateFailed, jobautomation.StateStopped:
		return workflowFailed
	case jobautomation.StateBlocked:
		return workflowBlocked
	case jobautomation.StateSentBack:
		return workflowSentBack
	}
	return workflowRunning
}

// deliveryStepsRecorded reports whether a succeeded delivery's steps have all
// recorded a result. A step still pending, waiting or running means its
// result (a push, a deliver) is not recorded yet, whatever the delivery's own
// state says. Deliveries in any other state are not judged here.
func deliveryStepsRecorded(d jobautomation.Delivery) bool {
	if d.State != jobautomation.StateSucceeded {
		return true
	}
	for _, step := range d.Steps {
		switch step.State {
		case jobautomation.StatePending, jobautomation.StateWaiting, jobautomation.StateRunning:
			return false
		}
	}
	return true
}

// currentStep is the delivery's step to show (0-based): the one running or
// waiting, else the one that failed or blocked it, else the last that ran.
func currentStep(d jobautomation.Delivery) int {
	if d.State == jobautomation.StateSentBack && d.SentBack != nil {
		// A sent-back delivery stands at the reopen step that sent it.
		for i, step := range d.Steps {
			if step.ID == d.SentBack.Step {
				return i
			}
		}
	}
	last := -1
	for i, step := range d.Steps {
		switch step.State {
		case jobautomation.StateRunning, jobautomation.StateWaiting:
			return i
		case jobautomation.StateFailed, jobautomation.StateStopped:
			// A step allowed to fail (continue_on_failure) is not what
			// the delivery stands at.
			if d.State != jobautomation.StateSucceeded && d.State != jobautomation.StateSentBack {
				return i
			}
		}
		if step.State != jobautomation.StatePending {
			last = i
		}
	}
	if last < 0 {
		return 0
	}
	if !jobautomation.Terminal(d.State) && last+1 < len(d.Steps) {
		return last + 1 // the next step to run
	}
	return last
}

// stepLabel names a step for a person: the agent, where it delivers, or what
// it does.
func stepLabel(d jobautomation.Delivery, i int) string {
	if i < 0 || i >= len(d.Steps) {
		return ""
	}
	var def jobautomation.Step
	if i < len(d.Definition) {
		def = d.Definition[i]
	}
	switch d.Steps[i].Type {
	case jobautomation.StepAgent:
		if def.Worker != "" {
			return def.Worker
		}
		return "agent"
	case jobautomation.StepReopen:
		return "send back"
	case jobautomation.StepDeliver:
		if def.To != "" {
			return "deliver to " + def.To
		}
		return "deliver"
	case jobautomation.StepRun:
		return "run " + d.Steps[i].ID
	}
	return d.Steps[i].ID
}

func deliverTo(d jobautomation.Delivery, i int) string {
	if to, _ := d.Steps[i].Outcome["to"].(string); to != "" {
		return to
	}
	if i < len(d.Definition) {
		return d.Definition[i].To
	}
	return ""
}

func deliverWhere(step jobautomation.StepRecord) string {
	where, _ := step.Outcome["where"].(string)
	if where == "" {
		where = step.Output
	}
	return where
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func oneLineProgress(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// computeTaskProgress is a task's progress from its record, its job's
// deliveries (newest first, as Store.JobDeliveries lists them), the
// evaluation of its latest job end (nil: none recorded) and now (Unix ms).
// It reads nothing else.
func computeTaskProgress(task pebblestore.ProjectTaskRecord, deliveries []jobautomation.Delivery, eval *jobautomation.JobEvaluation, now int64) taskProgress {
	class := taskStatusClass(task.Status)
	ended := class == "finished" || class == progressFailed || class == progressStopped
	latest := latestDeliveries(deliveries)

	// Workflow: the deliveries that applied (latest round per workflow),
	// or none / pending from the evaluation of the job end.
	var workflow *taskProgressWorkflow
	running := []jobautomation.Delivery{}
	var failedDelivery, sentBackDelivery *jobautomation.Delivery
	if len(latest) > 0 || (ended && eval != nil) {
		workflow = &taskProgressWorkflow{Deliveries: []taskProgressDelivery{}}
		anyFailed, anyBlocked, anySentBack := false, false, false
		for i, d := range latest {
			step := currentStep(d)
			state := deliveryProgressState(d.State)
			if state == workflowDone && !deliveryStepsRecorded(d) {
				// The runner marked the delivery succeeded but a step (the
				// push or deliver at its end) has not recorded its result
				// yet: the work is not handed over until it has.
				state = workflowRunning
			}
			view := taskProgressDelivery{ID: d.ID, Workflow: d.AutomationName, State: state, Steps: len(d.Steps), Round: d.Round, SentBack: d.SentBack}
			if len(d.Steps) > 0 {
				view.Step, view.StepType = step+1, d.Steps[step].Type
			}
			if reopen, ok := deliveryReopen(d, step); ok {
				view.ReopenCause, view.ReopenFailure, view.ReopenRound = reopen.Cause, reopen.Failure, reopen.Round
			}
			workflow.Deliveries = append(workflow.Deliveries, view)
			switch state {
			case workflowRunning:
				running = append(running, d)
			case workflowFailed:
				anyFailed = true
				if failedDelivery == nil {
					failedDelivery = &latest[i]
				}
			case workflowBlocked:
				anyBlocked = true
				if failedDelivery == nil {
					failedDelivery = &latest[i]
				}
			case workflowSentBack:
				anySentBack = true
				if sentBackDelivery == nil {
					sentBackDelivery = &latest[i]
				}
			}
		}
		switch {
		case len(running) > 0:
			workflow.State = workflowRunning
		case eval != nil && !eval.Evaluated():
			workflow.State = workflowPending
		case anyFailed:
			workflow.State = workflowFailed
		case anyBlocked:
			workflow.State = workflowBlocked
		case anySentBack:
			workflow.State = workflowSentBack
		case len(latest) > 0:
			workflow.State = workflowDone
		default:
			workflow.State = workflowNone
		}
	}

	// Integration: the task's record, then what its workflows merged,
	// published or delivered.
	branch := firstNonEmptyString(task.WorktreeBranch)
	target := task.BaseBranch
	commits := max(task.UnintegratedCommits, 0)
	if task.DeliveryAssessment != nil && task.DeliveryAssessment.CandidateCommits > commits {
		commits = task.DeliveryAssessment.CandidateCommits
	}
	integrated, integratedCommit, integratedAt := false, "", int64(0)
	if in := task.Integration; in != nil {
		branch = firstNonEmptyString(branch, in.SourceBranch)
		target = firstNonEmptyString(in.TargetBranch, target)
		switch in.State {
		case "integrated", "already_integrated", "recovered", "equivalent":
			integrated, integratedCommit = true, firstNonEmptyString(in.ResultingTargetHead, in.RecoveredHead, in.SourceHead)
		case "in_folder":
			// Worked in the folder itself: its commits are on the
			// folder's branch already.
			if in.SourceHead != "" && in.SourceHead != in.PreviousTargetHead {
				integrated, integratedCommit = true, in.SourceHead
			}
		}
	}
	if task.IsIntegrated {
		integrated = true
	}
	if integrated {
		integratedAt = task.UpdatedAt
	}
	deliveredWhere := ""
	if task.Integration != nil && task.Integration.State == "pull_request" && task.Integration.PullRequestURL != "" {
		deliveredWhere = "pull request " + task.Integration.PullRequestURL
	}
	for _, d := range latest {
		// A run step after the delivery's last agent step (its review) is the
		// owner's script handing the work over (push, deploy); one before it
		// is a check.
		lastAgent := -1
		for i, step := range d.Steps {
			if step.Type == jobautomation.StepAgent && step.State == jobautomation.StateSucceeded {
				lastAgent = i
			}
		}
		for i, step := range d.Steps {
			if step.Type == jobautomation.StepRun && step.State == jobautomation.StateSucceeded && d.State == jobautomation.StateSucceeded && i > lastAgent && lastAgent >= 0 && deliveredWhere == "" {
				deliveredWhere = step.ID
				if out := strings.TrimSpace(step.Output); out != "" {
					deliveredWhere += ": " + out
				}
				continue
			}
			if step.Type != jobautomation.StepDeliver || step.State != jobautomation.StateSucceeded {
				continue
			}
			switch to := deliverTo(d, i); to {
			case jobautomation.DeliverMerge:
				integrated = true
				if c := deliverWhere(step); isProgressCommit(c) {
					integratedCommit = c
				}
				integratedAt = max(integratedAt, step.FinishedAt)
			case jobautomation.DeliverPublish:
				deliveredWhere = "pull request " + deliverWhere(step)
			case jobautomation.DeliverFolder, jobautomation.DeliverOutbox:
				if deliveredWhere == "" {
					deliveredWhere = "delivered to " + to
					if w := deliverWhere(step); w != "" && to == jobautomation.DeliverFolder {
						deliveredWhere = "delivered to " + w
					}
				}
			}
		}
	}
	if !integrated && commits == 0 && len(latest) > 0 && latest[0].Result.Git != nil {
		commits = len(latest[0].Result.Git.Commits)
		branch = firstNonEmptyString(branch, latest[0].Result.Git.Branch)
		target = firstNonEmptyString(target, latest[0].Result.Git.BaseBranch)
	}
	if integrated {
		commits = 0
	}
	mediaReady := false
	if mediaTaskAgent(task.Agent) || task.Design != nil {
		for _, d := range task.Deliverables {
			if d.Status == "ready" || d.Status == "accepted" {
				mediaReady = true
			}
		}
	}
	var integration *taskProgressIntegration
	if branch != "" || task.Integration != nil || commits > 0 || integrated {
		integration = &taskProgressIntegration{Branch: branch, Target: target, Commits: commits}
		switch {
		case integrated:
			integration.State, integration.Commit, integration.At = progressIntegrated, integratedCommit, integratedAt
		case commits > 0:
			integration.State = progressNotIntegrated
		default:
			integration.State = "none"
		}
	}

	// State and summary.
	out := taskProgress{}
	switch {
	case class == progressWaiting:
		out.State = progressWaiting
		if task.Status == "pending_approval" {
			out.Summary = "waiting for approval (Start tasks)"
		} else if task.Status == pebblestore.ProjectTaskStatusLimitWaiting {
			out.Summary = firstNonEmptyString(task.LimitReason, "waiting for the provider's limit to reset")
		} else {
			out.Summary = "queued"
		}
	case class == progressWorking:
		out.State = progressWorking
		who := firstNonEmptyString(task.Agent, "agent")
		switch {
		case task.Status == "planning":
			out.Summary = who + " planning"
		case len(task.Attempts) > 1:
			out.Summary = who + " working on a follow-up"
		default:
			out.Summary = who + " working"
		}
	case class == progressStopped:
		out.State = progressStopped
		out.Summary = "stopped by the person; message_task resumes it"
	case len(running) > 0:
		out.State = progressWorkflow
		d := running[0]
		step := currentStep(d)
		out.Summary = d.AutomationName
		if len(d.Steps) > 0 {
			out.Summary += fmt.Sprintf(" · step %d of %d: %s", step+1, len(d.Steps), stepLabel(d, step))
		}
		out.Summary += " · round " + strconv.Itoa(d.Round)
		if len(running) > 1 {
			out.Summary += fmt.Sprintf(" (+%d more)", len(running)-1)
		}
	case integrated:
		out.State = progressIntegrated
		out.Summary = "merged into " + firstNonEmptyString(target, "its target")
		if integratedCommit != "" {
			out.Summary += " at " + shortCommit(integratedCommit)
		}
	case class == progressFailed:
		out.State = progressFailed
		out.Summary = "failed"
		if task.Status != "failed" {
			out.Summary = task.Status
		}
		if reason := firstNonEmptyString(task.LastError, task.ActionNeeded); reason != "" {
			out.Summary += ": " + reason
		}
	case failedDelivery != nil && workflow != nil && (workflow.State == workflowFailed || workflow.State == workflowBlocked):
		out.State = progressFailed
		d := *failedDelivery
		out.Summary = "workflow " + d.AutomationName + " " + d.State
		reason := d.BlockedReason
		if reason == "" {
			if step := currentStep(d); step < len(d.Steps) {
				reason = firstNonEmptyString(d.Steps[step].Reason, stepLabel(d, step)+" "+d.Steps[step].State)
			}
		}
		if reason != "" {
			out.Summary += ": " + reason
		}
	case sentBackDelivery != nil && workflow != nil && workflow.State == workflowSentBack:
		// The work went back to its agent; the follow-up's end starts a
		// fresh delivery. Nothing was handed over.
		out.State = progressWorkflow
		out.Summary = sentBackSummary(*sentBackDelivery)
	case deliveredWhere != "" || mediaReady:
		out.State = progressDelivered
		out.Summary = firstNonEmptyString(deliveredWhere, "files ready")
	case commits > 0:
		out.State = progressNotIntegrated
		// The state first, then whether a workflow applied: a long branch
		// name may be cut where a client shows one line.
		out.Summary = "not integrated"
		if workflow != nil {
			switch workflow.State {
			case workflowNone:
				out.Summary += " · no workflow"
			case workflowPending:
				out.Summary += " · workflows not evaluated yet"
			case workflowDone:
				out.Summary += " · workflows done"
			}
		}
		out.Summary += " · " + plural(commits, "commit") + " on " + firstNonEmptyString(branch, "its branch")
	default:
		out.State = progressDone
		out.Summary = "finished, nothing to integrate"
	}
	out.Summary = oneLineProgress(out.Summary, maxProgressSummary)
	out.Outcome = taskProgressOutcome(class, out.State, latest, workflow, integrated, deliveredWhere)
	out.Workflow = workflow
	out.Integration = integration
	out.History = taskProgressHistory(task, deliveries, eval, ended, commits, branch, integrated, integratedCommit, target)
	out.UpdatedAt = task.UpdatedAt
	for _, e := range out.History {
		out.UpdatedAt = max(out.UpdatedAt, e.At)
	}
	if out.UpdatedAt == 0 {
		out.UpdatedAt = now
	}
	out.Idle = taskProgressIdle(task, out.UpdatedAt, now)
	return out
}

// sentBackSummary says a delivery sent its work back: the workflow, the
// reopen step, its round and why it ran.
func sentBackSummary(d jobautomation.Delivery) string {
	what := "workflow " + d.AutomationName + " sent back"
	sb := d.SentBack
	if sb == nil {
		return what
	}
	what += " by " + sb.Step + " · round " + strconv.Itoa(sb.Round)
	if sb.Reason != "" {
		what += ": " + sb.Reason
	}
	return what
}

func isProgressCommit(s string) bool {
	return controlMCPCommitID.MatchString(strings.TrimSpace(s))
}

// taskProgressHistory is what happened, oldest first, bounded to the most
// recent maxProgressHistory entries.
func taskProgressHistory(task pebblestore.ProjectTaskRecord, deliveries []jobautomation.Delivery, eval *jobautomation.JobEvaluation, ended bool, commits int, branch string, integrated bool, commit, target string) []taskProgressEvent {
	h := []taskProgressEvent{}
	add := func(at int64, what string) {
		if at > 0 && what != "" {
			h = append(h, taskProgressEvent{At: at, What: oneLineProgress(what, 200)})
		}
	}
	created := "created"
	if task.Status == "pending_approval" {
		created = "created · waiting for approval (Start tasks)"
	}
	add(task.CreatedAt, created)
	for i, a := range task.Attempts {
		what := "started"
		if i > 0 {
			what = "follow-up started"
		}
		add(a.CreatedAt, what)
	}
	if task.LimitAt > 0 {
		add(task.LimitAt, "run stopped on the provider's usage limit; the task continues after the reset")
	}
	// Job ends: each follow-up's end the deliveries were made for, and the
	// latest end from the evaluation (or, without one, the task itself).
	ends := map[string]bool{}
	sorted := append([]jobautomation.Delivery(nil), deliveries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt < sorted[j].CreatedAt })
	latestFollowup, latestEnd := "", int64(0)
	if eval != nil {
		latestFollowup, latestEnd = eval.Followup, eval.EndedAt
	}
	endWhat := func(status string, n int, br string) string {
		if status == jobautomation.StatusFailed || status == jobautomation.StatusCancelled {
			return status
		}
		if n > 0 {
			return "finished · " + plural(n, "commit") + " on " + firstNonEmptyString(br, "its branch")
		}
		return "finished"
	}
	for _, d := range sorted {
		if ends[d.Followup] || (eval != nil && d.Followup == latestFollowup) {
			continue
		}
		ends[d.Followup] = true
		n, br := 0, ""
		if d.Result.Git != nil {
			n, br = len(d.Result.Git.Commits), d.Result.Git.Branch
		}
		add(d.Result.FinishedAt, endWhat(d.Result.Status, n, br))
	}
	if ended {
		at := latestEnd
		if at == 0 && len(sorted) == 0 {
			at = task.UpdatedAt
		}
		status := jobautomation.StatusSucceeded
		if taskStatusClass(task.Status) == progressFailed {
			status = jobautomation.StatusFailed
		}
		what := endWhat(status, commits, branch)
		if status == jobautomation.StatusFailed && task.LastError != "" {
			what = "failed: " + task.LastError
		}
		if taskStatusClass(task.Status) == progressStopped {
			what = "stopped by the person"
		}
		add(at, what)
	}
	merged := false
	for _, d := range sorted {
		add(d.CreatedAt, "workflow "+d.AutomationName+" started")
		for i, step := range d.Steps {
			label := stepLabel(d, i)
			if step.StartedAt > 0 && step.State != jobautomation.StateSkipped {
				add(step.StartedAt, label+" started")
			}
			if step.FinishedAt == 0 {
				continue
			}
			switch step.State {
			case jobautomation.StateSucceeded:
				switch step.Type {
				case jobautomation.StepReopen:
					add(step.FinishedAt, "sent back · round "+strconv.Itoa(d.Round))
				case jobautomation.StepDeliver:
					to, where := deliverTo(d, i), deliverWhere(step)
					switch to {
					case jobautomation.DeliverMerge:
						merged = true
						what := "merged"
						if isProgressCommit(where) {
							what += " at " + shortCommit(where)
						}
						add(step.FinishedAt, what)
					case jobautomation.DeliverPublish:
						add(step.FinishedAt, "pull request "+where)
					default:
						add(step.FinishedAt, "delivered to "+firstNonEmptyString(where, to))
					}
				default:
					what := label + " finished"
					if verdict, _ := step.Outcome["verdict"].(string); verdict != "" {
						what += " · " + verdict
					}
					add(step.FinishedAt, what)
				}
			case jobautomation.StateFailed, jobautomation.StateStopped:
				add(step.FinishedAt, label+" "+step.State+": "+step.Reason)
			}
		}
		if d.FinishedAt > 0 {
			what := "workflow " + d.AutomationName + " " + d.State
			if d.State == jobautomation.StateSentBack {
				what = sentBackSummary(d)
			}
			if d.BlockedReason != "" {
				what += ": " + d.BlockedReason
			}
			add(d.FinishedAt, what)
		}
	}
	if integrated && !merged {
		what := "merged into " + firstNonEmptyString(target, "its target")
		if commit != "" {
			what += " at " + shortCommit(commit)
		}
		add(task.UpdatedAt, what)
	}
	sort.SliceStable(h, func(i, j int) bool { return h[i].At < h[j].At })
	if len(h) > maxProgressHistory {
		h = h[len(h)-maxProgressHistory:]
	}
	return h
}

// taskProgressFor reads a task's deliveries and evaluation and computes its
// progress. A waiting task has neither, so nothing is read for it.
func (s *Server) taskProgressFor(account string, task pebblestore.ProjectTaskRecord, now int64) *taskProgress {
	deliveries, eval := s.taskProgressRecords(account, task)
	p := computeTaskProgress(task, deliveries, eval, now)
	s.markTaskProgressStale(&p, task, now)
	s.markTaskProgressLive(&p, account, task)
	markTaskProgressAttention(&p, task)
	return &p
}

// markTaskProgressAttention sets p.NeedsAttention from its final outcome,
// stale and idle flags, with the rule list counts use.
func markTaskProgressAttention(p *taskProgress, task pebblestore.ProjectTaskRecord) {
	if p != nil {
		p.NeedsAttention = pebblestore.ProjectTaskNeedsAttentionLive(task, p.Outcome, p.Stale, p.Idle, p.Live)
		p.TaskState, p.TaskStateReason = pebblestore.ProjectTaskStateLive(task, p.Outcome, p.Stale, p.Idle, p.Live)
	}
}

// taskProgressRecords reads a task's job deliveries and evaluation.
func (s *Server) taskProgressRecords(account string, task pebblestore.ProjectTaskRecord) ([]jobautomation.Delivery, *jobautomation.JobEvaluation) {
	var deliveries []jobautomation.Delivery
	var eval *jobautomation.JobEvaluation
	// A task waiting for its provider's limit ran already: its deliveries
	// (a reviewer's send-back round, say) still apply.
	if store, err := s.jobAutomationStore(); err == nil && (taskStatusClass(task.Status) != progressWaiting || task.Status == pebblestore.ProjectTaskStatusLimitWaiting) {
		jobID := taskProgressJobID(task)
		if list, err := store.JobDeliveries(account, jobID); err == nil {
			deliveries = list
		} else {
			log.Printf("swarmd task progress: read deliveries of %s: %v", jobID, err)
		}
		if e, found, err := store.JobEvaluation(account, jobID); err == nil && found {
			eval = &e
		} else if err != nil {
			log.Printf("swarmd task progress: read evaluation of %s: %v", jobID, err)
		}
	}
	return deliveries, eval
}

// taskWithProgress is a task as a client reads it, with its progress.
type taskWithProgress struct {
	Task     *pebblestore.ProjectTaskRecord
	Progress *taskProgress
}

// newTaskWithProgress is task (a client copy) with its progress, its
// deliverables shown from the task state (showTaskDeliverables).
func newTaskWithProgress(task *pebblestore.ProjectTaskRecord, progress *taskProgress) taskWithProgress {
	showTaskDeliverables(task, progress)
	return taskWithProgress{Task: task, Progress: progress}
}

// showTaskDeliverables shows each deliverable of task, a client copy, with
// the status pebblestore.ProjectTaskDeliverableStatus gives for the task
// state in progress: a non-media deliverable's stored "pending" is a
// creation placeholder. Without a task state the stored statuses stay.
func showTaskDeliverables(task *pebblestore.ProjectTaskRecord, progress *taskProgress) {
	if task == nil || progress == nil || progress.TaskState == "" {
		return
	}
	task.ShowDeliverablesForState(progress.TaskState)
}

func (t taskWithProgress) MarshalJSON() ([]byte, error) {
	task, err := json.Marshal(t.Task)
	if err != nil || t.Progress == nil {
		return task, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(task, &fields); err != nil {
		return nil, err
	}
	if fields["progress"], err = json.Marshal(t.Progress); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// installTaskProgress publishes project.updated for a task's project on
// every change to its job's deliveries or evaluation, and gives wait_tasks
// each task's progress.
func (s *Server) installTaskProgress() {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return
	}
	db := s.sessions.Store()
	if store, err := s.jobAutomationStore(); err == nil {
		store.SetNotify(func(account, projectID, jobID string) {
			if projectID == "" {
				return
			}
			taskID := ""
			if parts := strings.Split(jobID, "/"); len(parts) == 3 && parts[1] == projectID {
				taskID = parts[2]
			}
			if err := db.NotifyProjectTaskChanged(account, projectID, taskID); err != nil {
				log.Printf("swarmd task progress: publish project.updated for %s: %v", jobID, err)
			}
		})
	}
	if underlying := db.Underlying(); underlying != nil {
		underlying.SetProjectTaskProgress(func(account string, task pebblestore.ProjectTaskRecord) any {
			return s.taskWaitProgressFor(account, task, taskProgressNow().UnixMilli())
		})
		// The startup reconciliation ran before the hook existed and left
		// settled waits on finished tasks undecided; decide them now.
		s.reconcileProjectTaskWaits("", "")
	}
}
