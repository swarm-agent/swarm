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
	// ProjectTaskCountNeedsAttention: the person must act. The task's run is
	// blocked, failed (not stopped), waiting for approval or a question, or
	// it finished within the last 7 days with work to hand over (ready to
	// integrate) and no workflow handing it over. Never a pushed, delivered,
	// integrated, finished (nothing to integrate) or stopped task, an
	// archived one, a workflow's own task, or a workflow delivery that ended
	// unsuccessfully (blocked_infra, workflow_failed).
	ProjectTaskCountNeedsAttention = "needs_attention"
	// ProjectTaskCountBlockedInfra: its workflow delivery ended because a
	// step could not run (outcome "error …": checkout, environment, lease,
	// script missing or unusable), within the last 7 days. An infrastructure
	// problem, not the work's; nothing waits on the person.
	ProjectTaskCountBlockedInfra = "blocked_infra"
	// ProjectTaskCountWorkflowFailed: its workflow delivery ended blocked or
	// failed (a check or review ended it, including sent back until its
	// rounds ran out), or conflict (Swarm's rebase onto the target conflicted
	// and its resolver agents did not resolve it: state rebase_conflict),
	// within the last 7 days. Nothing waits on the person.
	ProjectTaskCountWorkflowFailed = "workflow_failed"
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
	return map[string]int{ProjectTaskCountDone: 0, ProjectTaskCountInProgress: 0, ProjectTaskCountWaiting: 0, ProjectTaskCountNeedsAttention: 0, ProjectTaskCountBlockedInfra: 0, ProjectTaskCountWorkflowFailed: 0, ProjectTaskCountIdle: 0, ProjectTaskCountStopped: 0}
}

// ProjectTaskBucketNeedsAttention reports whether a task in bucket needs
// someone to act before its work moves again: needs_attention (the person
// must act), blocked_infra (fix the machine, then rerun the failed steps) and
// workflow_failed (send the task back or archive it). Only needs_attention's
// needs_you state waits on the person.
func ProjectTaskBucketNeedsAttention(bucket string) bool {
	switch bucket {
	case ProjectTaskCountNeedsAttention, ProjectTaskCountBlockedInfra, ProjectTaskCountWorkflowFailed:
		return true
	}
	return false
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
	case "queued", "pending", ProjectTaskStatusLimitWaiting:
		// Queued for capacity, or waiting for its provider's limit to
		// reset: it continues by itself.
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
	switch word {
	case "error":
		// "error <step> (infrastructure: <kind>)": a step could not run.
		return ProjectTaskCountBlockedInfra
	case "blocked", "failed", "conflict":
		// A check or review ended the delivery, or Swarm's rebase
		// conflicted and its resolver did not resolve it.
		return ProjectTaskCountWorkflowFailed
	}
	return ProjectTaskCountNeedsAttention
}

// ProjectTaskBucket is the count bucket of task from its progress outcome,
// stale and idle flags: ProjectTaskCountBucket, except that an archived task
// (the person put it away) and a task a workflow step started (a reviewer
// run, whose work and outcome belong to the task it serves) never wait on
// the person: what would be needs_attention, blocked_infra, workflow_failed
// or idle counts as done. List counts use it on this machine and on linked
// machines.
func ProjectTaskBucket(task ProjectTaskRecord, outcome string, stale, idle bool) string {
	bucket := ProjectTaskCountBucket(task.Status, outcome, stale, idle)
	if (ProjectTaskBucketNeedsAttention(bucket) || bucket == ProjectTaskCountIdle) && (task.Archived || ProjectTaskWorkflowSpawned(task)) {
		return ProjectTaskCountDone
	}
	return bucket
}

// ProjectTaskNeedsAttention is the one decision whether someone must act on
// task before its work moves again: its list row's needs_attention and
// get_task's needs_attention read it, here and on linked machines. It is true
// for the needs_attention, blocked_infra and workflow_failed count buckets
// (ProjectTaskBucketNeedsAttention); only the needs_you state waits on the
// person.
func ProjectTaskNeedsAttention(task ProjectTaskRecord, outcome string, stale, idle bool) bool {
	return ProjectTaskBucketNeedsAttention(ProjectTaskBucket(task, outcome, stale, idle))
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
	// ProjectTaskStateWaiting: queued for capacity, or waiting for its
	// provider's usage limit to reset (it continues by itself at the reset).
	// Count bucket waiting.
	ProjectTaskStateWaiting = "waiting"
	// ProjectTaskStateNeedsYou: the person must act: a pending approval or
	// question, or a blocked or failed agent run. Never a workflow delivery
	// that ended unsuccessfully (blocked_infra, workflow_failed). Count
	// bucket needs_attention.
	ProjectTaskStateNeedsYou = "needs_you"
	// ProjectTaskStateBlockedInfra: the workflow delivery ended because a
	// step could not run (its checkout, the environment, the lease, a
	// missing or unusable script): an infrastructure problem, not the work.
	// Nothing waits on the person; fix the machine or folder, then rerun the
	// failed steps. Count bucket blocked_infra (idle once 7 days old, as
	// not_integrated).
	ProjectTaskStateBlockedInfra = "blocked_infra"
	// ProjectTaskStateWorkflowFailed: the workflow delivery ended blocked or
	// failed: a check or review ended it (including sent back until its
	// rounds ran out). Nothing waits on the person; send the task back with
	// the findings, or archive it. Count bucket workflow_failed (idle once 7
	// days old, as not_integrated).
	ProjectTaskStateWorkflowFailed = "workflow_failed"
	// ProjectTaskStateRebaseConflict: the workflow delivery ended conflict:
	// Swarm's rebase of the work onto its target branch conflicted and its
	// resolver agents did not resolve it. The work is intact on its branch;
	// nothing waits on the person: send the task back to rebase onto the
	// target, then the delivery is re-run (it rebases again). Count bucket
	// workflow_failed (idle once 7 days old, as not_integrated).
	ProjectTaskStateRebaseConflict = "rebase_conflict"
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

// What a blocked_infra or workflow_failed task's state reason says comes
// next. get_task also names the call (next_action); wake rows carry it in
// task_state_reason.
const (
	ProjectTaskBlockedInfraNext   = "an infrastructure problem, not the work; nothing for a coder to fix and nothing waits on the person; next: fix the machine or folder, then rerun the failed steps"
	ProjectTaskWorkflowFailedNext = "a check or review ended it; nothing waits on the person; next: send the task back with the findings, or archive it"
	ProjectTaskRebaseConflictNext = "Swarm's rebase onto the target conflicted and its resolver could not resolve it; the work is intact on its branch and nothing waits on the person; next: send the task back to rebase onto the target, then rerun the delivery (it rebases again)"
)

// ProjectTaskState is the task's state and a one-line reason, from the same
// inputs and the same bucket as ProjectTaskBucket (the list counts), so a
// state never disagrees with its count: landed and done count done, running
// in_progress, waiting waiting, needs_you needs_attention, blocked_infra
// blocked_infra, workflow_failed and rebase_conflict workflow_failed, stopped stopped,
// not_integrated needs_attention (or idle once 7 days old).
func ProjectTaskState(task ProjectTaskRecord, outcome string, stale, idle bool) (string, string) {
	state, reason := projectTaskState(task, outcome, stale, idle)
	// A stalled task keeps its state word (and so its count bucket); its
	// reason names the stall. Landed and stopped tasks are never stalled.
	if task.Stall.Applies(task) && state != ProjectTaskStateLanded && state != ProjectTaskStateStopped {
		return state, "stalled: " + clipProjectTaskStateReason(task.Stall.Reason)
	}
	return state, reason
}

func projectTaskState(task ProjectTaskRecord, outcome string, stale, idle bool) (string, string) {
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
		if task.Status == ProjectTaskStatusLimitWaiting {
			reason := strings.TrimSpace(task.LimitReason)
			if reason == "" {
				reason = "waiting for the provider's limit to reset"
			}
			return ProjectTaskStateWaiting, clipProjectTaskStateReason(reason) + "; continues by itself"
		}
		return ProjectTaskStateWaiting, "queued; starts when capacity frees"
	case ProjectTaskCountInProgress:
		switch word {
		case "sent_back":
			// A review block, or a passed review whose push failed (a
			// rebase), named apart by ProjectTaskReopen.
			if reopen, ok := ParseProjectTaskReopenOutcome(outcome); ok {
				return ProjectTaskStateRunning, reopen.StateReason()
			}
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
	case ProjectTaskCountBlockedInfra:
		// A workflow step's script never ran (its checkout, the
		// environment, the lease): an infrastructure problem.
		return ProjectTaskStateBlockedInfra, "workflow " + outcome + ": " + ProjectTaskBlockedInfraNext
	case ProjectTaskCountWorkflowFailed:
		if word == "conflict" {
			return ProjectTaskStateRebaseConflict, "workflow " + outcome + "; " + ProjectTaskRebaseConflictNext
		}
		return ProjectTaskStateWorkflowFailed, "workflow " + outcome + "; " + ProjectTaskWorkflowFailedNext
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
	case "error":
		// Only idle reaches here (blocked_infra above otherwise): not
		// attention any more; its work was never handed over.
		return ProjectTaskStateNotIntegrated, "workflow " + outcome + ": an infrastructure problem, not the work; idle over 7 days"
	case "blocked", "failed", "conflict":
		// Only idle reaches here (workflow_failed or rebase_conflict
		// above otherwise).
		return ProjectTaskStateNotIntegrated, "workflow " + outcome + "; idle over 7 days"
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

// ProjectTaskLive is what a working task's run is doing right now, read by
// the task progress from the machine's live records rather than the task
// record: waiting on a pending permission or question (state needs_you, with
// the request), or held before it can run by an update drain or execution
// admission (state waiting, reason "held: …"). It changes a task's state
// only while the record's status says its run works (ProjectTaskLiveApplies):
// a run status of running alone does not say the run is moving.
type ProjectTaskLive struct {
	State   string                     `json:"state"`
	Reason  string                     `json:"reason"`
	Request *ProjectTaskPendingRequest `json:"request,omitempty"`
}

// ProjectTaskPendingRequest is the oldest pending permission or question a
// task's run waits on: its id (what resolves it), the session it belongs to,
// the tool and a short summary of its arguments. Pending counts every
// pending request of the session when there is more than one.
type ProjectTaskPendingRequest struct {
	PermissionID string `json:"permission_id"`
	SessionID    string `json:"session_id"`
	Kind         string `json:"kind"` // permission or question
	Tool         string `json:"tool"`
	Summary      string `json:"summary"`
	RequestedAt  int64  `json:"requested_at,omitempty"`
	Pending      int    `json:"pending,omitempty"`
}

// Kinds of a pending request.
const (
	ProjectTaskRequestPermission = "permission"
	ProjectTaskRequestQuestion   = "question"
)

// ProjectTaskLiveApplies reports whether live decides task's state: a
// needs_you or waiting live state of a task that is not archived and whose
// run status says its agent works.
func ProjectTaskLiveApplies(task ProjectTaskRecord, live *ProjectTaskLive) bool {
	if live == nil || task.Archived || (live.State != ProjectTaskStateNeedsYou && live.State != ProjectTaskStateWaiting) {
		return false
	}
	switch task.Status {
	case "planning", "in_progress", "running", "starting", "generating", "swarming", "publishing":
		return true
	}
	return false
}

// ProjectTaskBucketLive is ProjectTaskBucket with the run's live state: a run
// waiting on a person counts needs_attention, a held run waiting.
func ProjectTaskBucketLive(task ProjectTaskRecord, outcome string, stale, idle bool, live *ProjectTaskLive) string {
	if ProjectTaskLiveApplies(task, live) {
		if live.State == ProjectTaskStateNeedsYou {
			return ProjectTaskCountNeedsAttention
		}
		return ProjectTaskCountWaiting
	}
	return ProjectTaskBucket(task, outcome, stale, idle)
}

// ProjectTaskNeedsAttentionLive is ProjectTaskNeedsAttention with the run's
// live state.
func ProjectTaskNeedsAttentionLive(task ProjectTaskRecord, outcome string, stale, idle bool, live *ProjectTaskLive) bool {
	return ProjectTaskBucketNeedsAttention(ProjectTaskBucketLive(task, outcome, stale, idle, live))
}

// ProjectTaskStateLive is ProjectTaskState with the run's live state, in the
// same bucket as ProjectTaskBucketLive. Every surface that has a task's
// progress reads its state through this.
func ProjectTaskStateLive(task ProjectTaskRecord, outcome string, stale, idle bool, live *ProjectTaskLive) (string, string) {
	if ProjectTaskLiveApplies(task, live) {
		return live.State, clipProjectTaskStateReason(live.Reason)
	}
	return ProjectTaskState(task, outcome, stale, idle)
}

// ProjectTaskProgressLive reads the live state from a task progress value
// (the progress hook's answer, or a linked machine's progress). nil when
// there is none.
func ProjectTaskProgressLive(progress any) *ProjectTaskLive {
	if progress == nil {
		return nil
	}
	raw, ok := progress.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(progress); err != nil {
			return nil
		}
	}
	var view struct {
		Live *ProjectTaskLive `json:"live"`
	}
	if json.Unmarshal(raw, &view) != nil || view.Live == nil || view.Live.State == "" {
		return nil
	}
	return view.Live
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

// ProjectTaskMediaDeliverable reports whether d is a media asset (image,
// video, audio/sound, or any deliverable carrying a media URL). The media
// pipeline keeps a media deliverable's Status in step with its generation, so
// that stored status is what surfaces show.
func ProjectTaskMediaDeliverable(d ProjectTaskDeliverable) bool {
	switch strings.ToLower(strings.TrimSpace(d.Kind)) {
	case "image", "video", "audio", "sound":
		return true
	}
	return strings.TrimSpace(d.MediaURL) != ""
}

// ProjectTaskDeliverableStatus is the status to show for d of a task in
// state. Every non-media deliverable (code PR, report, plan, artifact, …) is
// created with the placeholder "pending" that nothing keeps in step with the
// work, so while it carries that placeholder it reads from the task state:
// running: in progress; waiting: not started; landed: delivered; done: ready;
// not_integrated: on branch; needs_you: needs you; blocked_infra: blocked
// (infrastructure); workflow_failed: workflow failed; rebase_conflict: rebase
// conflict; stopped: stopped. A media
// deliverable, or one whose stored status was set explicitly (ready,
// accepted, …), keeps its stored status. This is the one rule every surface
// (get_task, the task routes, the Orchestrator's prompt block) uses.
func ProjectTaskDeliverableStatus(d ProjectTaskDeliverable, state string) string {
	status := strings.TrimSpace(d.Status)
	if ProjectTaskMediaDeliverable(d) || (status != "" && status != "pending") {
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
	case ProjectTaskStateBlockedInfra:
		return "blocked (infrastructure)"
	case ProjectTaskStateWorkflowFailed:
		return "workflow failed"
	case ProjectTaskStateRebaseConflict:
		return "rebase conflict"
	case ProjectTaskStateDone:
		return "ready"
	case ProjectTaskStateStopped:
		return "stopped"
	}
	return "unknown"
}

// ProjectTaskDeliverablesShown returns a copy of list with each Status as
// ProjectTaskDeliverableStatus shows it for a task in state. list itself is
// not changed; nil stays nil.
func ProjectTaskDeliverablesShown(list []ProjectTaskDeliverable, state string) []ProjectTaskDeliverable {
	if list == nil {
		return nil
	}
	out := make([]ProjectTaskDeliverable, len(list))
	for i, d := range list {
		d.Status = ProjectTaskDeliverableStatus(d, state)
		out[i] = d
	}
	return out
}

// ShowDeliverablesForState rewrites, in place, the deliverables of t and of
// its attempts with the statuses ProjectTaskDeliverableStatus shows for a
// task in state. Callers pass a response copy, never the stored record: each
// list is replaced by a new slice, so slices shared with the stored record
// are not written through.
func (t *ProjectTaskRecord) ShowDeliverablesForState(state string) {
	if t == nil {
		return
	}
	t.Deliverables = ProjectTaskDeliverablesShown(t.Deliverables, state)
	if len(t.Attempts) > 0 {
		t.Attempts = append([]ProjectTaskAttempt(nil), t.Attempts...)
		for i := range t.Attempts {
			t.Attempts[i].Deliverables = ProjectTaskDeliverablesShown(t.Attempts[i].Deliverables, state)
		}
	}
}
