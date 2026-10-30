package pebblestore

// A wait that has no conversation to wake (Swarm Control's wait_tasks) holds
// the call until a watched task reaches its next milestone. It reads the
// same task status and progress a task watch reads, and names the stage
// with the watch's own rules, so the two never disagree about what a
// milestone is.

// Run milestones a conversation-less wait returns at, besides the watch's
// own (review started, sent back, rebase needed, landed, blocked, failed).
const (
	ProjectTaskMilestoneStarted  = "started"
	ProjectTaskMilestoneNeedsYou = "needs you"
	ProjectTaskMilestoneDone     = "done"
	ProjectTaskMilestoneFailed   = "failed"
	ProjectTaskMilestoneStopped  = "stopped"
)

// ProjectTaskMilestone names the stage a task has reached from its run
// status and its wait progress (ProjectTaskWaitProgressFrom): a watch
// milestone first (projectTaskWatchMilestone: review started, sent back,
// rebase needed, landed, blocked, failed), then the run's: needs you (a
// pending permission or question, or a run that waits on the person),
// done, failed, stopped or started. Empty when the task is at no
// milestone yet (proposed, queued).
func ProjectTaskMilestone(status string, progress *V3ProjectTaskWaitProgress) string {
	row := projectTaskWaitProgressRow(progress)
	row["status"] = status
	if milestone := projectTaskWatchMilestone(row); milestone != "" {
		return milestone
	}
	if projectTaskWaitNeedsYou(progress) || (progress != nil && progress.Live != nil && progress.Live.State == ProjectTaskStateNeedsYou) {
		return ProjectTaskMilestoneNeedsYou
	}
	switch status {
	case "needs_input", "pending_approval":
		return ProjectTaskMilestoneNeedsYou
	case "needs_review", "completed":
		return ProjectTaskMilestoneDone
	case "failed":
		return ProjectTaskMilestoneFailed
	case "cancelled", "superseded", "rejected", ProjectTaskStatusStopped:
		return ProjectTaskMilestoneStopped
	case "planning", "in_progress", "running", "starting", "generating", "swarming", "publishing":
		return ProjectTaskMilestoneStarted
	}
	if progress != nil && progress.TaskState == ProjectTaskStateStopped {
		return ProjectTaskMilestoneStopped
	}
	return ""
}

// ProjectTaskWaitSettled reports whether a task's work has settled under
// until (settled or finished), by the rule a task watch fires on.
func ProjectTaskWaitSettled(status, until string, progress *V3ProjectTaskWaitProgress) bool {
	ready, _ := projectTaskWaitReadiness(status, &V3ProjectTaskWait{Until: until}, progress)
	return ready
}
