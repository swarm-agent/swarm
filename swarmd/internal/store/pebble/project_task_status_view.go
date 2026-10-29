package pebblestore

import "strings"

// A deliberate stop of a task's run (stop_task, or the person's own stop in
// the task's session) is not a failure: the task's status is stopped, the
// stop is not a result its job ended with, and a message resumes it.
const (
	ProjectTaskStatusStopped = "stopped"
	// ProjectTaskStopReason is the reason stop_task records on the run it
	// cancels.
	ProjectTaskStopReason = "user stopped task"
)

// ProjectTaskRunStoppedByPerson reports whether a run with this terminal
// status and reason was stopped by the person (stop_task or the session's
// stop), not cancelled by quota, shutdown or deletion.
func ProjectTaskRunStoppedByPerson(status, reason string) bool {
	return status == V3RunIntentCancelled && (reason == ProjectTaskStopReason || reason == V3RunStoppedByUser)
}

// Task count buckets: what a list's counts say about its tasks, from each
// task's status and outcome (task progress), not its raw status. needs_review
// only means the agent's run ended; the outcome says whether the work was
// pushed, sent back or blocked.
const (
	// ProjectTaskCountDone: pushed, delivered or integrated (or completed,
	// rejected, superseded): nobody waits on it.
	ProjectTaskCountDone = "done"
	// ProjectTaskCountInProgress: an agent or a workflow (running, sent
	// back, not evaluated yet) still works on it.
	ProjectTaskCountInProgress = "in_progress"
	// ProjectTaskCountWaiting: queued for capacity; nobody has to act.
	ProjectTaskCountWaiting = "waiting"
	// ProjectTaskCountNeedsAttention: the person must act. The task is
	// blocked, failed (not stopped), waiting for approval or a question, or
	// finished within the last 7 days with work to hand over (ready to
	// integrate, a workflow that blocked or failed) and no workflow handing
	// it over. Never a pushed, delivered, integrated, finished (nothing to
	// integrate) or stopped task, an archived one, or a workflow's own task.
	ProjectTaskCountNeedsAttention = "needs_attention"
	// ProjectTaskCountIdle: finished over 7 days ago (progress idle) with
	// work never handed over: nobody is waiting on it, so it is not called
	// attention.
	ProjectTaskCountIdle = "idle"
	// ProjectTaskCountStopped: stopped by the person, or stale (no active run
	// for a day).
	ProjectTaskCountStopped = "stopped"
)

// NewProjectTaskOutcomeCounts is a zero count for every bucket, so a reader
// sees every bucket and never infers one from a missing key.
func NewProjectTaskOutcomeCounts() map[string]int {
	return map[string]int{ProjectTaskCountDone: 0, ProjectTaskCountInProgress: 0, ProjectTaskCountWaiting: 0, ProjectTaskCountNeedsAttention: 0, ProjectTaskCountIdle: 0, ProjectTaskCountStopped: 0}
}

// ProjectTaskCountBucket is the bucket of one task from its status and its
// progress outcome, stale and idle flags. It is the one rule list counts use
// on this machine and on linked machines.
func ProjectTaskCountBucket(status, outcome string, stale, idle bool) string {
	if status == ProjectTaskStatusStopped || stale {
		return ProjectTaskCountStopped
	}
	switch status {
	case "pending_approval", "needs_input":
		// Waiting for the person's approval or answer.
		return ProjectTaskCountNeedsAttention
	case "queued", "pending":
		return ProjectTaskCountWaiting
	case "planning", "in_progress", "running", "starting", "generating", "swarming", "publishing":
		return ProjectTaskCountInProgress
	case "completed", "rejected", "superseded":
		return ProjectTaskCountDone
	case "failed", "blocked", "error", "cancelled", "interrupted", "expired":
		return ProjectTaskCountNeedsAttention
	}
	// The run ended (needs_review and the like): the outcome says where the
	// work is.
	word, _, _ := strings.Cut(strings.TrimSpace(outcome), " ")
	switch word {
	case "pushed", "delivered", "integrated", "finished":
		// "finished": nothing to integrate or deliver.
		return ProjectTaskCountDone
	case "running", "sent_back", "workflow":
		// "workflow pending": its workflows were not evaluated yet.
		return ProjectTaskCountInProgress
	case "stopped":
		return ProjectTaskCountStopped
	}
	if idle {
		return ProjectTaskCountIdle
	}
	return ProjectTaskCountNeedsAttention
}

// ProjectTaskBucket is the count bucket of task from its progress outcome,
// stale and idle flags: ProjectTaskCountBucket, except that an archived task
// (the person put it away) and a task a workflow step started (a reviewer
// run, whose work and outcome belong to the task it serves) never wait on
// the person: what would be needs_attention or idle counts as done. List
// counts use it on this machine and on linked machines.
func ProjectTaskBucket(task ProjectTaskRecord, outcome string, stale, idle bool) string {
	bucket := ProjectTaskCountBucket(task.Status, outcome, stale, idle)
	if (bucket == ProjectTaskCountNeedsAttention || bucket == ProjectTaskCountIdle) && (task.Archived || ProjectTaskWorkflowSpawned(task)) {
		return ProjectTaskCountDone
	}
	return bucket
}

// ProjectTaskNeedsAttention is the one decision whether the person must act
// on task: its list row's needs_attention, get_task's needs_attention and
// the needs_attention count all read it, here and on linked machines.
func ProjectTaskNeedsAttention(task ProjectTaskRecord, outcome string, stale, idle bool) bool {
	return ProjectTaskBucket(task, outcome, stale, idle) == ProjectTaskCountNeedsAttention
}
