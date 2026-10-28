package api

import (
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A task's outcome answers "what happened to its work" in one line, so a list
// of tasks says whether a change was pushed, sent back or stopped, not only
// that the agent's run ended (needs_review). It is read from the task's latest
// workflow delivery; without one, from the task's own integration.

const (
	// taskStaleAfterMillis is how long an in_progress or blocked task may go
	// without an active run before its progress says stale.
	taskStaleAfterMillis int64 = 24 * 60 * 60 * 1000
	// taskIdleAfterMillis is how long ago a finished task last changed
	// before its progress says idle: nobody is waiting on it any more.
	taskIdleAfterMillis     int64 = 7 * 24 * 60 * 60 * 1000
	maxTaskOutcome                = 120
	outcomeReadyToIntegrate       = "ready to integrate"
	// outcomeFinished: the agent finished with nothing to hand over (no
	// commits, nothing delivered or ready): progress state done.
	outcomeFinished        = "finished"
	outcomeIntegrated      = "integrated"
	outcomeWorkflowPending = "workflow pending"
	outcomeStopped         = "stopped"
)

// taskProgressNow is the clock task progress (and its stale flag) is read at.
var taskProgressNow = time.Now

// taskProgressOutcome is the task's outcome: from its latest delivery
// (running <step>, sent_back (round n), pushed <head12>, delivered <target>,
// blocked <reason>, failed <step>); else integrated, delivered, or once its
// agent finished: finished when its progress state is done (nothing to
// integrate, the same decision get_task's summary reads), else ready to
// integrate. "" while the task has not finished and no workflow ran for it.
func taskProgressOutcome(class, state string, latest []jobautomation.Delivery, workflow *taskProgressWorkflow, integrated bool, deliveredWhere string) string {
	if class == progressStopped {
		// The person's stop is the latest thing that happened to the work.
		return outcomeStopped
	}
	if len(latest) > 0 {
		d := latest[0]
		for _, candidate := range latest {
			if deliveryProgressState(candidate.State) == workflowRunning {
				d = candidate
				break
			}
		}
		step := currentStep(d)
		switch d.State {
		case jobautomation.StateSucceeded, jobautomation.StateSkipped:
			if out := deliveryHandover(d); out != "" {
				return clipOutcome(out)
			}
		case jobautomation.StateSentBack:
			round := d.Round
			if d.SentBack != nil && d.SentBack.Round > 0 {
				round = d.SentBack.Round
			}
			return "sent_back (round " + strconv.Itoa(round) + ")"
		case jobautomation.StateBlocked:
			reason := d.BlockedReason
			if reason == "" && step < len(d.Steps) {
				reason = firstNonEmptyString(d.Steps[step].Reason, stepLabel(d, step))
			}
			return clipOutcome("blocked " + reason)
		case jobautomation.StateFailed, jobautomation.StateStopped:
			return clipOutcome("failed " + firstNonEmptyString(stepLabel(d, step), "workflow"))
		default:
			return clipOutcome("running " + firstNonEmptyString(stepLabel(d, step), "workflow"))
		}
	} else if workflow != nil && workflow.State == workflowPending {
		return outcomeWorkflowPending
	}
	switch {
	case integrated:
		return outcomeIntegrated
	case deliveredWhere != "":
		return clipOutcome("delivered " + strings.TrimPrefix(deliveredWhere, "delivered to "))
	case class == "finished" && state == progressDone:
		return outcomeFinished
	case class == "finished":
		return outcomeReadyToIntegrate
	}
	return ""
}

// deliveryHandover is how a finished delivery handed the work over: its last
// deliver step (delivered <target>), or a run step after its last agent step
// (the owner's script, such as a push: pushed <head12>). "" when it handed
// nothing over.
func deliveryHandover(d jobautomation.Delivery) string {
	lastAgent := -1
	for i, step := range d.Steps {
		if step.Type == jobautomation.StepAgent && step.State == jobautomation.StateSucceeded {
			lastAgent = i
		}
	}
	out := ""
	for i, step := range d.Steps {
		if step.State != jobautomation.StateSucceeded {
			continue
		}
		switch {
		case step.Type == jobautomation.StepDeliver:
			to := deliverTo(d, i)
			where := strings.TrimSpace(deliverWhere(step))
			switch {
			case to == jobautomation.DeliverMerge && isProgressCommit(where):
				out = "delivered merge " + shortCommit(where)
			case where != "" && to != jobautomation.DeliverMerge:
				out = "delivered " + firstNonEmptyString(to, "result") + " " + where
			default:
				out = "delivered " + firstNonEmptyString(to, "result")
			}
		case step.Type == jobautomation.StepRun && lastAgent >= 0 && i > lastAgent:
			if isProgressCommit(d.Head) {
				out = "pushed " + shortCommit(d.Head)
			} else {
				out = "delivered " + step.ID
			}
		}
	}
	return out
}

// projectTaskOutcomeCounts counts tasks by what happened to their work
// (pebblestore.ProjectTaskCountBucket over each one's progress outcome), the
// counts a task list answers with next to its raw status counts.
func (s *Server) projectTaskOutcomeCounts(account string, tasks []pebblestore.ProjectTaskRecord, now int64) map[string]int {
	counts := pebblestore.NewProjectTaskOutcomeCounts()
	for _, task := range tasks {
		p := s.taskProgressFor(account, task, now)
		counts[pebblestore.ProjectTaskCountBucket(task.Status, p.Outcome, p.Stale, p.Idle)]++
	}
	return counts
}

func clipOutcome(text string) string {
	return oneLineProgress(text, maxTaskOutcome)
}

// taskProgressStale reports whether an in_progress or blocked task has had no
// active run for taskStaleAfterMillis: its run is not active and neither the
// task nor its session's last run changed since. lastRunAt is when the
// session's run state last changed (0: none recorded).
func taskProgressStale(task pebblestore.ProjectTaskRecord, runActive bool, lastRunAt, now int64) bool {
	if task.Archived || runActive || (taskStatusClass(task.Status) != progressWorking && task.Status != "blocked") {
		return false
	}
	last := max(task.UpdatedAt, lastRunAt)
	return last > 0 && now-last > taskStaleAfterMillis
}

// taskProgressIdle reports whether a finished (not working, waiting, failed
// or stopped) task has not changed for taskIdleAfterMillis: updatedAt is its
// progress's latest change.
func taskProgressIdle(task pebblestore.ProjectTaskRecord, updatedAt, now int64) bool {
	if taskStatusClass(task.Status) != "finished" {
		return false
	}
	return updatedAt > 0 && now-updatedAt > taskIdleAfterMillis
}

// markTaskProgressStale sets p.Stale from the task's session run state. A run
// state that cannot be read leaves the task not stale: stale is a claim.
func (s *Server) markTaskProgressStale(p *taskProgress, task pebblestore.ProjectTaskRecord, now int64) {
	if p == nil || (taskStatusClass(task.Status) != progressWorking && task.Status != "blocked") {
		return
	}
	active, lastRunAt := false, int64(0)
	if task.SessionID != "" {
		if s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return
		}
		state, found, err := s.sessions.Store().GetV3SessionRunState(task.SessionID)
		if err != nil {
			return
		}
		if found {
			active, lastRunAt = state.Active, max(state.UpdatedAt, state.CompletedAt, state.StartedAt)
		}
	}
	p.Stale = taskProgressStale(task, active, lastRunAt, now)
}
