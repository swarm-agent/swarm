package pebblestore

import (
	"encoding/json"
	"strings"
)

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

// Task states: where a task's work is, in one word. A task record's Status
// is its run status (needs_review only means the agent's run ended); the
// state reads it together with the progress outcome. Every surface that
// shows a task (list_tasks, get_task, linked machines, the Orchestrator's
// prompt, progress.task_state on the task routes) shows this state. See
// docs/task-progress.md.
const (
	// ProjectTaskStateRunning: an agent or a workflow (running, sent back,
	// not evaluated yet) still works on it. Count bucket in_progress.
	ProjectTaskStateRunning = "running"
	// ProjectTaskStateWaiting: queued for capacity. Count bucket waiting.
	ProjectTaskStateWaiting = "waiting"
	// ProjectTaskStateNeedsYou: the person must act: approval, a question,
	// a blocked or failed run or workflow (including sent back until its
	// rounds ran out). Count bucket needs_attention.
	ProjectTaskStateNeedsYou = "needs_you"
	// ProjectTaskStateLanded: the work was handed over: pushed, delivered or
	// integrated. Count bucket done.
	ProjectTaskStateLanded = "landed"
	// ProjectTaskStateDone: finished with nothing to hand over (finished,
	// completed, rejected, superseded), archived, or a workflow step's own
	// task. Count bucket done.
	ProjectTaskStateDone = "done"
	// ProjectTaskStateNotIntegrated: finished with work on its branch that
	// was never handed over. Count bucket needs_attention within 7 days of
	// its last change, idle after.
	ProjectTaskStateNotIntegrated = "not_integrated"
	// ProjectTaskStateStopped: stopped by the person, or stale (no active
	// run for a day). Count bucket stopped.
	ProjectTaskStateStopped = "stopped"
)

// ProjectTaskState is the task's state and a one-line reason, from the same
// inputs and the same bucket as ProjectTaskBucket (the list counts), so a
// state never disagrees with its count: landed and done count done, running
// in_progress, waiting waiting, needs_you needs_attention, stopped stopped,
// not_integrated needs_attention (or idle once 7 days old).
func ProjectTaskState(task ProjectTaskRecord, outcome string, stale, idle bool) (string, string) {
	outcome = strings.TrimSpace(outcome)
	word, _, _ := strings.Cut(outcome, " ")
	bucket := ProjectTaskBucket(task, outcome, stale, idle)
	switch bucket {
	case ProjectTaskCountStopped:
		if stale && task.Status != ProjectTaskStatusStopped {
			return ProjectTaskStateStopped, "no active run for over a day (run status " + task.Status + ")"
		}
		return ProjectTaskStateStopped, "stopped by the person; a message resumes it"
	case ProjectTaskCountWaiting:
		return ProjectTaskStateWaiting, "queued; starts when capacity frees"
	case ProjectTaskCountInProgress:
		switch word {
		case "sent_back":
			return ProjectTaskStateRunning, outcome + "; the agent addresses the findings"
		case "running":
			return ProjectTaskStateRunning, "workflow " + outcome
		case "workflow":
			return ProjectTaskStateRunning, outcome
		}
		return ProjectTaskStateRunning, "agent run " + task.Status
	case ProjectTaskCountDone:
		switch word {
		case "pushed", "delivered", "integrated":
			return ProjectTaskStateLanded, outcome
		}
		switch {
		case task.Archived:
			return ProjectTaskStateDone, "archived"
		case ProjectTaskWorkflowSpawned(task):
			return ProjectTaskStateDone, "workflow step; its work belongs to the task it serves"
		case word == "finished":
			return ProjectTaskStateDone, "finished; nothing to integrate"
		}
		return ProjectTaskStateDone, task.Status
	}
	// needs_attention or idle.
	switch task.Status {
	case "pending_approval":
		return ProjectTaskStateNeedsYou, "waiting for approval"
	case "needs_input":
		return ProjectTaskStateNeedsYou, "waiting for an answer"
	case "failed", "blocked", "error", "cancelled", "interrupted", "expired":
		reason := "run " + task.Status
		if text := strings.TrimSpace(task.LastError); text != "" {
			reason += ": " + clipProjectTaskStateReason(text)
		}
		return ProjectTaskStateNeedsYou, reason
	}
	switch word {
	case "blocked", "failed":
		reason := "workflow " + outcome
		if idle {
			// Not attention any more; its work was never handed over.
			return ProjectTaskStateNotIntegrated, reason + "; idle over 7 days"
		}
		return ProjectTaskStateNeedsYou, reason
	case "ready":
		reason := "work not handed over"
		if branch := strings.TrimSpace(task.WorktreeBranch); branch != "" {
			reason = "commits on branch " + branch + " not handed over"
		}
		if idle {
			reason += "; idle over 7 days"
		}
		return ProjectTaskStateNotIntegrated, reason
	}
	if idle {
		return ProjectTaskStateNotIntegrated, "run ended, nothing handed over; idle over 7 days"
	}
	return ProjectTaskStateNeedsYou, "run ended; outcome unknown"
}

func clipProjectTaskStateReason(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 160 {
		text = string(r[:160]) + "…"
	}
	return text
}

// ProjectTaskProgressFlags reads the outcome, stale and idle flags from a
// task progress value (the progress hook's answer, ProjectTaskProgress).
// Empty when there is none.
func ProjectTaskProgressFlags(progress any) (outcome string, stale, idle bool) {
	if progress == nil {
		return "", false, false
	}
	raw, err := json.Marshal(progress)
	if err != nil {
		return "", false, false
	}
	var flags struct {
		Outcome string `json:"outcome"`
		Stale   bool   `json:"stale"`
		Idle    bool   `json:"idle"`
	}
	if json.Unmarshal(raw, &flags) != nil {
		return "", false, false
	}
	return flags.Outcome, flags.Stale, flags.Idle
}

// ProjectTaskCodeDeliverable reports whether d is the code deliverable a
// coder task is created with (deliv_code). Its Status is the placeholder set
// when the task is created ("pending") and is not kept in step with the work:
// the task state is authoritative.
func ProjectTaskCodeDeliverable(d ProjectTaskDeliverable) bool {
	switch d.Kind {
	case "pr", "code_pr", "code_diff":
		return true
	}
	return d.ID == "deliv_code"
}

// ProjectTaskDeliverableStatus is the status to show for d of a task in
// state: a code deliverable still carrying its creation placeholder reads
// from the task state (landed: delivered; not_integrated: on branch;
// running: in progress); any other deliverable keeps its stored status.
func ProjectTaskDeliverableStatus(d ProjectTaskDeliverable, state string) string {
	status := strings.TrimSpace(d.Status)
	if !ProjectTaskCodeDeliverable(d) || (status != "" && status != "pending") {
		return status
	}
	switch state {
	case ProjectTaskStateLanded:
		return "delivered"
	case ProjectTaskStateNotIntegrated:
		return "on branch"
	case ProjectTaskStateRunning:
		return "in progress"
	case ProjectTaskStateWaiting:
		return "not started"
	case ProjectTaskStateNeedsYou:
		return "needs you"
	case ProjectTaskStateDone:
		return "done"
	case ProjectTaskStateStopped:
		return "stopped"
	}
	return "unknown"
}
