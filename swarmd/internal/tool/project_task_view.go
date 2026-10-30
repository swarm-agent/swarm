package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The Orchestrator's task tools answer with what it needs to decide its next
// step: where the task is, what it produced and what to do next. The stored
// record stays available on request (detail). Home and linked machines share
// this one shape, so a task answers the same way wherever it runs. HTTP
// routes and clients keep their full records; this is the agent's view only.

const (
	// projectTaskReportBytes bounds the report summary in the default view.
	projectTaskReportBytes = 1200
	// projectTaskViewBytes bounds the whole default view; the report gives
	// way first, so get_task with its wrapper stays within 2 KB.
	projectTaskViewBytes   = 1800
	projectTaskTitleRunes  = 256
	projectTaskErrorBytes  = 400
	projectTaskTextBytes   = 300
	projectTaskListMax     = 10
	projectTaskFilesMax    = 200
	projectTaskAttemptsMax = 50

	// projectTaskMoreGet tells the agent how to read the rest of a task.
	projectTaskMoreGet = "detail=plan|attempts|files|full"
	// projectTaskMoreRow tells the agent how to read the stored record after
	// a task mutation.
	projectTaskMoreRow = "get_task for progress; detail=full for the record"
)

// Task detail levels for get_task. "" is the compact default.
const (
	ProjectTaskDetailPlan     = "plan"
	ProjectTaskDetailAttempts = "attempts"
	ProjectTaskDetailFiles    = "files"
	ProjectTaskDetailFull     = "full"
)

// projectTaskCompactActions are the task actions whose answer is compact by
// default, on this machine and on linked machines.
func projectTaskCompactAction(action string) bool {
	switch action {
	case "get_task", "propose_task", "create_task", "approve_task", "accept_task", "reopen_task", "refine_task", "archive_task":
		return true
	}
	return false
}

// ProjectTaskDetail reads the optional detail argument. Unknown values are
// refused so a misspelt request never silently returns the compact view.
func ProjectTaskDetail(args map[string]any) (string, error) {
	raw, ok := args["detail"]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", errors.New("detail must be plan, attempts, files or full")
	}
	switch value := strings.ToLower(strings.TrimSpace(text)); value {
	case "", ProjectTaskDetailPlan, ProjectTaskDetailAttempts, ProjectTaskDetailFiles, ProjectTaskDetailFull:
		return value, nil
	}
	return "", fmt.Errorf("detail %q is not one of plan, attempts, files, full", text)
}

// projectTaskProgressRead is the progress a task carries when read through
// the task routes (or computed by the store's progress hook): its state,
// one-line summary, workflow deliveries and integration.
type projectTaskProgressRead struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
	Outcome string `json:"outcome"`
	Stale   bool   `json:"stale"`
	Idle    bool   `json:"idle"`
	// Live is the working run's live state (pebblestore.ProjectTaskLive).
	Live     *pebblestore.ProjectTaskLive `json:"live"`
	Workflow *struct {
		State      string `json:"state"`
		Deliveries []struct {
			ID       string `json:"id"`
			Workflow string `json:"workflow"`
			State    string `json:"state"`
			Step     int    `json:"step"`
			Steps    int    `json:"steps"`
			StepType string `json:"step_type"`
			Round    int    `json:"round"`
			SentBack *struct {
				Step   string `json:"step"`
				Round  int    `json:"round"`
				Reason string `json:"reason"`
			} `json:"sent_back"`
			// ReopenCause: review_block, push_failed or check_failed
			// (pebblestore.ProjectTaskReopen).
			ReopenCause       string `json:"reopen_cause"`
			FindingsStep      string `json:"findings_step"`
			Verdict           string `json:"verdict"`
			Findings          string `json:"findings"`
			FindingsTruncated bool   `json:"findings_truncated"`
			More              string `json:"more"`
			// Review is the delivery's latest review step with a verdict
			// (task progress deliveryReview).
			Review *struct {
				Step              string `json:"step"`
				Verdict           string `json:"verdict"`
				Round             int    `json:"round"`
				Findings          string `json:"findings"`
				FindingsTruncated bool   `json:"findings_truncated"`
			} `json:"review"`
		} `json:"deliveries"`
	} `json:"workflow"`
	Integration *struct {
		State   string `json:"state"`
		Branch  string `json:"branch"`
		Commits int    `json:"commits"`
		Commit  string `json:"commit"`
	} `json:"integration"`
}

// projectTaskRead is a task as the task routes return it: the stored record
// plus its computed progress.
type projectTaskRead struct {
	pebblestore.ProjectTaskRecord
	Progress *projectTaskProgressRead `json:"progress,omitempty"`
	// Worktree is the task's own committed range when no delivery
	// assessment is present (the store never persists one): on this machine
	// base..refs/heads/<WorktreeBranch>, or base..Integration.SourceHead for
	// a folder task delivered in_folder; for a linked machine, the task
	// route's top-level commits/files for that same range. nil otherwise.
	Worktree *projectTaskWorktree `json:"-"`
	// Messages are what was sent to the task and whether it arrived
	// (pebblestore.ProjectTaskMessages), newest last.
	Messages []pebblestore.ProjectTaskMessage `json:"-"`
}

// projectTaskWorktree is the task branch's committed result: the head of
// refs/heads/<WorktreeBranch>, the commits after the task's base and the
// files they change.
type projectTaskWorktree struct {
	Head    string
	Commits int
	Files   []string
}

// ProjectTaskView is the default get_task answer.
type ProjectTaskView struct {
	// State is where the task's work is (pebblestore.ProjectTaskState):
	// running, waiting, needs_you, landed, done, not_integrated or stopped,
	// with StateReason its one-line why. It is what a reader reports.
	State       string `json:"state"`
	StateReason string `json:"state_reason,omitempty"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	Agent       string `json:"agent,omitempty"`
	// RunStatus is the stored record's status: the status of the task's
	// agent run (needs_review only means the run ended), not the task's
	// state.
	RunStatus   string                `json:"run_status"`
	Revision    int                   `json:"revision,omitempty"`
	UpdatedAt   int64                 `json:"updated_at,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	Progress    projectTaskProgress   `json:"progress"`
	Result      *projectTaskResult    `json:"result,omitempty"`
	Integration string                `json:"integration,omitempty"`
	LastError   string                `json:"last_error,omitempty"`
	PlanBinding *projectTaskPlanGuard `json:"plan_binding,omitempty"`
	// Messages are the last messages sent to the task (message_task, or a
	// follow-up), each with its delivery: queued, delivered (with the run
	// that received it) or reopened.
	Messages []pebblestore.ProjectTaskMessage `json:"messages,omitempty"`
	// NeedsAttention says the person must act on the task, with the same
	// decision as its list row and the needs_attention count
	// (pebblestore.ProjectTaskNeedsAttention).
	NeedsAttention bool `json:"needs_attention"`
	// PendingRequest is the permission or question the task's run waits on
	// (state needs_you): what is asked, and the permission_id
	// resolve_task_permission answers. Its text is untrusted.
	PendingRequest *pebblestore.ProjectTaskPendingRequest `json:"pending_request,omitempty"`
	// Review is the latest review of the task's work: its verdict (ok,
	// warn or block), round and findings summary. nil when no review has
	// recorded a verdict.
	Review *projectTaskReview `json:"review,omitempty"`
	// NextAction is the action to take next; omitted when there is none.
	NextAction string `json:"next_action,omitempty"`
	More       string `json:"more"`
}

// projectTaskReview is a task's latest review verdict as get_task shows it.
// The findings are the reviewer's text (untrusted), bounded to
// projectTaskTextBytes; FindingsTruncated and More (the call with the full
// text) say when they were cut.
type projectTaskReview struct {
	Verdict           string `json:"verdict"`
	Round             int    `json:"round,omitempty"`
	Step              string `json:"step,omitempty"`
	DeliveryID        string `json:"delivery_id,omitempty"`
	Findings          string `json:"findings,omitempty"`
	FindingsTruncated bool   `json:"findings_truncated,omitempty"`
	More              string `json:"more,omitempty"`
}

type projectTaskProgress struct {
	Stage    string               `json:"stage,omitempty"`
	Now      string               `json:"now,omitempty"`
	Next     string               `json:"next,omitempty"`
	Workflow *projectTaskWorkflow `json:"workflow,omitempty"`
}

// projectTaskWorkflow is the workflow/review loop around the task: whether it
// still runs, its last step and the outcome (sent_back, pushed/delivered,
// blocked with its reason).
type projectTaskWorkflow struct {
	Name     string `json:"name,omitempty"`
	Running  bool   `json:"running"`
	State    string `json:"state"`
	LastStep string `json:"last_step,omitempty"`
	Round    int    `json:"round,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// ReopenCause says why a reopen sent the work back: review_block
	// (findings to address) or push_failed (review passed; rebase and
	// retry), or check_failed.
	ReopenCause string `json:"reopen_cause,omitempty"`
	// DeliveryID is the shown delivery; manage_workflows action=deliveries
	// delivery_id=<it> reads its steps in full.
	DeliveryID string `json:"delivery_id,omitempty"`
	// FindingsStep, Verdict and Findings say why a review blocked or sent
	// the work back (task progress deliveryFindings), the findings capped to
	// projectTaskFindingsRunes; FindingsTruncated and More (the call with
	// the full text) are set when they were cut. The findings are the
	// reviewer's text: untrusted.
	FindingsStep      string `json:"findings_step,omitempty"`
	Verdict           string `json:"verdict,omitempty"`
	Findings          string `json:"findings,omitempty"`
	FindingsTruncated bool   `json:"findings_truncated,omitempty"`
	More              string `json:"more,omitempty"`
}

// projectTaskFindingsRunes bounds a workflow's findings in get_task; a
// linked machine's answer is bounded here as well.
const projectTaskFindingsRunes = 4000

// projectTaskFindingsMore is the call that returns a delivery's full
// findings.
func projectTaskFindingsMore(deliveryID string, remote bool) string {
	more := "manage_workflows action=deliveries delivery_id=" + deliveryID
	if remote {
		more += " with this task's machine"
	}
	return more
}

type projectTaskResult struct {
	Summary string `json:"summary,omitempty"`
	// ResultTruncated says Summary is shortened; ResultBytes is the full
	// report's size and ReadFull how to read all of it.
	ResultTruncated bool   `json:"result_truncated,omitempty"`
	ResultBytes     int    `json:"result_bytes,omitempty"`
	ReadFull        string `json:"read_full,omitempty"`
	// Next is set with ResultTruncated: the exact arguments of the
	// manage_projects call that reads the full report (the envelope's next).
	Next         map[string]any `json:"next,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	Head         string         `json:"head,omitempty"`
	Commits      int            `json:"commits"`
	Files        int            `json:"files"`
	Deliverables []string       `json:"deliverables,omitempty"`
}

// projectTaskPlanGuard is what approve_task/refine_task need for a task
// waiting on a bound plan.
type projectTaskPlanGuard struct {
	SessionID          string `json:"session_id,omitempty"`
	PlanID             string `json:"plan_id,omitempty"`
	DefinitionRevision int    `json:"definition_revision,omitempty"`
}

// ProjectTaskRow is the answer to a task mutation: the task's identity and
// run status and what to do next. State is set when the run status alone
// decides it (pebblestore.ProjectTaskState without an outcome): a task whose
// run ended needs its outcome, which get_task reads.
type ProjectTaskRow struct {
	State       string                `json:"state,omitempty"`
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Agent       string                `json:"agent,omitempty"`
	RunStatus   string                `json:"run_status"`
	Revision    int                   `json:"revision,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	LastError   string                `json:"last_error,omitempty"`
	PlanBinding *projectTaskPlanGuard `json:"plan_binding,omitempty"`
}

func clipBytes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func clipRunes(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return string(r[:limit])
}

func projectTaskPlanGuardFor(t *pebblestore.ProjectTaskRecord) *projectTaskPlanGuard {
	if t.PlanBinding == nil || t.Status != "pending_approval" {
		return nil
	}
	return &projectTaskPlanGuard{SessionID: t.PlanBinding.SessionID, PlanID: t.PlanBinding.PlanID, DefinitionRevision: t.PlanBinding.DefinitionRevision}
}

// projectTaskStatusClass sorts a status the way task progress does.
func projectTaskStatusClass(status string) string {
	switch status {
	case pebblestore.ProjectTaskStatusStopped:
		return "stopped"
	case "pending_approval":
		return "approval"
	case "queued", "pending", pebblestore.ProjectTaskStatusLimitWaiting:
		// Queued for capacity, or its run stopped on the provider's usage
		// limit and it continues by itself at the reset: not finished.
		return "waiting"
	case "planning", "in_progress", "running", "starting", "generating", "swarming", "publishing":
		return "working"
	case "failed", "blocked", "error", "cancelled", "interrupted", "expired", "rejected":
		return "failed"
	}
	return "finished"
}

// projectTaskNext is the action the Orchestrator can take next and what
// comes next for the task. remote tasks are on a linked machine, where
// integration is the machine's own.
func projectTaskNext(t *pebblestore.ProjectTaskRecord, progress *projectTaskProgressRead, integration string, commits int, remote bool) (string, string) {
	if t.Archived {
		return "", "archived"
	}
	switch projectTaskStatusClass(t.Status) {
	case "approval":
		return "approve_task", "starts when approved"
	case "waiting":
		if t.Status == pebblestore.ProjectTaskStatusLimitWaiting {
			return "wait_tasks", "continues by itself after the provider's limit resets (stop_task cancels)"
		}
		return "wait_tasks", "starts when capacity frees"
	case "working":
		return "wait_tasks", "agent finishes, then its result is ready to review"
	case "stopped":
		return "message_task", "stopped by the person; message_task resumes it"
	case "failed":
		if remote {
			return "read_task_session", "read why it stopped, then reopen_task with feedback"
		}
		return "reopen_task", "send it back with feedback, or archive_task"
	}
	if progress != nil && progress.Workflow != nil {
		switch progress.Workflow.State {
		case "running", "pending":
			return "wait_tasks", "workflow finishes its steps"
		case "sent_back":
			for _, d := range progress.Workflow.Deliveries {
				switch d.ReopenCause {
				case pebblestore.ProjectTaskReopenPushFailed:
					return "wait_tasks", "review passed; agent rebases after the failed push, then the workflow retries"
				case pebblestore.ProjectTaskReopenCheckFailed:
					return "wait_tasks", "agent fixes the failed step"
				}
			}
			return "wait_tasks", "agent addresses the sent-back findings"
		case "failed", "blocked":
			if remote {
				return "read_task_session", "workflow stopped; read why, then reopen_task"
			}
			return "reopen_task", "workflow stopped; fix the reason, then reopen_task"
		}
	}
	switch {
	case integration == "integrated" || integration == "delivered":
		return "", "done"
	case commits > 0 && remote:
		return "read_task_session", "review the result; integrating its commits is done on that machine"
	case commits > 0:
		return "integrate_task", "integrate its commits into the target branch"
	}
	return "", "done; use the result"
}

// compactProjectTask is the default get_task view of a task.
// ProjectTaskWorkflow is a task's progress (the task route's "progress") as
// get_task shows it in progress.workflow: the deciding delivery's id, round
// and state, and why a review blocked or sent it back with the findings
// bounded (findings_truncated, more). nil when the task has no workflow.
// Swarm Control's task tools read it here, so they show the same fields
// under the same bounds as get_task.
func ProjectTaskWorkflow(progress any) any {
	raw, err := json.Marshal(progress)
	if err != nil {
		return nil
	}
	var p projectTaskProgressRead
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	if wf := compactProjectTask(projectTaskRead{Progress: &p}, false).Progress.Workflow; wf != nil {
		return wf
	}
	return nil
}

func compactProjectTask(read projectTaskRead, remote bool) ProjectTaskView {
	t := &read.ProjectTaskRecord
	t.EnsureTaskAttempts()
	view := ProjectTaskView{ID: t.ID, Title: clipRunes(t.Title, projectTaskTitleRunes), Agent: t.Agent, RunStatus: t.Status, Revision: t.Revision, UpdatedAt: t.UpdatedAt, SessionID: t.SessionID, PlanBinding: projectTaskPlanGuardFor(t), More: projectTaskMoreGet}
	if text := firstNonEmptyText(t.LastError, actionNeededForFailure(t)); text != "" {
		view.LastError = clipBytes(text, projectTaskErrorBytes)
	}
	if n := len(t.PipelineStages); n > 0 {
		i := t.CurrentStageIndex
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		view.Progress.Stage = fmt.Sprintf("%s (%d of %d)", clipBytes(t.PipelineStages[i], 80), i+1, n)
	}
	p := read.Progress
	view.Progress.Now = t.Status
	if p != nil && strings.TrimSpace(p.Summary) != "" {
		view.Progress.Now = clipBytes(p.Summary, projectTaskTextBytes)
	}
	if p != nil && p.Workflow != nil && p.Workflow.State != "" && p.Workflow.State != "none" {
		wf := &projectTaskWorkflow{State: p.Workflow.State, Running: p.Workflow.State == "running" || p.Workflow.State == "pending"}
		if len(p.Workflow.Deliveries) > 0 {
			// The running delivery, else the one that decides the workflow's
			// state (the one sent back, failed or blocked), else the newest.
			d, picked := p.Workflow.Deliveries[0], false
			for _, candidate := range p.Workflow.Deliveries {
				if candidate.State == "running" {
					d, picked = candidate, true
					break
				}
			}
			for _, candidate := range p.Workflow.Deliveries {
				if !picked && candidate.State == p.Workflow.State {
					d, picked = candidate, true
				}
			}
			wf.Name, wf.Round = clipBytes(d.Workflow, 80), d.Round
			if d.State != wf.State {
				wf.Outcome = d.State
			}
			if d.Steps > 0 {
				wf.LastStep = fmt.Sprintf("%d of %d", d.Step, d.Steps)
				if d.StepType != "" {
					wf.LastStep += " (" + d.StepType + ")"
				}
			}
			if d.SentBack != nil {
				wf.Reason = clipBytes(d.SentBack.Reason, projectTaskTextBytes)
			}
			if d.ReopenCause != "" {
				wf.ReopenCause = pebblestore.ProjectTaskReopen{Cause: d.ReopenCause}.Normalized().Cause
			}
			wf.DeliveryID = clipBytes(d.ID, 160)
			wf.FindingsStep, wf.Verdict = clipBytes(d.FindingsStep, 80), clipBytes(d.Verdict, 16)
			if findings := strings.ToValidUTF8(strings.TrimSpace(d.Findings), string(utf8.RuneError)); findings != "" {
				cut := clipRunes(findings, projectTaskFindingsRunes)
				wf.Findings, wf.FindingsTruncated = cut, d.FindingsTruncated || cut != findings
				if wf.FindingsTruncated && wf.DeliveryID != "" {
					wf.More = projectTaskFindingsMore(wf.DeliveryID, remote)
				}
			}
		}
		if wf.Reason == "" && (wf.State == "blocked" || wf.State == "failed") {
			if _, reason, ok := strings.Cut(p.Summary, ": "); ok {
				wf.Reason = clipBytes(reason, projectTaskTextBytes)
			}
		}
		view.Progress.Workflow = wf
	}

	result := &projectTaskResult{Branch: t.WorktreeBranch, Commits: max(t.UnintegratedCommits, 0)}
	report, reportAttempt := "", -1
	if a := t.ActiveAttempt(); a != nil && strings.TrimSpace(a.Summary) != "" {
		report = strings.TrimSpace(a.Summary)
		for i := range t.Attempts {
			if t.Attempts[i].ID == a.ID {
				reportAttempt = i
			}
		}
	} else {
		for i := len(t.Attempts) - 1; i >= 0; i-- {
			if strings.TrimSpace(t.Attempts[i].Summary) != "" {
				report, reportAttempt = strings.TrimSpace(t.Attempts[i].Summary), i
				break
			}
		}
	}
	result.Summary = clipBytes(report, projectTaskReportBytes)
	if da := t.DeliveryAssessment; da != nil {
		result.Head = da.SourceOID
		result.Files = len(da.Files)
		result.Commits = max(result.Commits, da.CandidateCommits)
		if result.Branch == "" {
			result.Branch = da.SourceBranch
		}
	}
	if w := read.Worktree; w != nil && t.DeliveryAssessment == nil {
		result.Head, result.Files = w.Head, len(w.Files)
		result.Commits = max(result.Commits, w.Commits)
	}
	integration := ""
	if in := t.Integration; in != nil {
		integration = in.State
		if result.Head == "" {
			result.Head = in.SourceHead
		}
		if result.Branch == "" {
			result.Branch = in.SourceBranch
		}
	}
	if t.IsIntegrated {
		integration = "integrated"
	}
	if p != nil && p.Integration != nil {
		integration = p.Integration.State
		if read.Worktree == nil {
			// Without the task's own range, progress's count is the best
			// known; with it, the range is the task's commit count (an
			// in_folder task has no unintegrated commits but did commit).
			result.Commits = p.Integration.Commits
		}
		if result.Branch == "" {
			result.Branch = p.Integration.Branch
		}
		if result.Head == "" && p.Integration.Commit != "" {
			result.Head = p.Integration.Commit
		}
	}
	if p != nil && p.State == "delivered" {
		integration = "delivered"
	}
	outcome, stale, idle := "", false, false
	var live *pebblestore.ProjectTaskLive
	if p != nil {
		outcome, stale, idle, live = p.Outcome, p.Stale, p.Idle, p.Live
	}
	view.NeedsAttention = pebblestore.ProjectTaskNeedsAttentionLive(*t, outcome, stale, idle, live)
	view.State, view.StateReason = pebblestore.ProjectTaskStateLive(*t, outcome, stale, idle, live)
	if pebblestore.ProjectTaskLiveApplies(*t, live) && live.Request != nil {
		request := *live.Request
		request.Summary = clipBytes(request.Summary, projectTaskTextBytes)
		view.PendingRequest = &request
	}
	view.StateReason = clipBytes(view.StateReason, projectTaskTextBytes)
	// Each deliverable reads "title (status)", its status shown from the task
	// state (pebblestore.ProjectTaskDeliverableStatus), never its creation
	// placeholder.
	for i, d := range t.Deliverables {
		if i >= projectTaskListMax {
			break
		}
		entry := clipBytes(firstNonEmptyText(d.Title, d.ID), 80)
		if status := pebblestore.ProjectTaskDeliverableStatus(d, view.State); status != "" {
			entry += " (" + clipBytes(status, 32) + ")"
		}
		result.Deliverables = append(result.Deliverables, entry)
	}
	if result.Summary != "" || result.Branch != "" || result.Head != "" || result.Commits > 0 || result.Files > 0 || len(result.Deliverables) > 0 {
		view.Result = result
	}
	view.Integration = integration
	if n := len(read.Messages); n > 0 {
		messages := read.Messages[max(n-pebblestore.ProjectTaskMessagesShown, 0):]
		view.Messages = make([]pebblestore.ProjectTaskMessage, 0, len(messages))
		for _, m := range messages {
			// A linked machine's answer is bounded here as well.
			if r := []rune(m.Text); len(r) > pebblestore.ProjectTaskMessageTextRunes {
				m.Text = string(r[:pebblestore.ProjectTaskMessageTextRunes])
			}
			m.State, m.RunID = clipBytes(m.State, 16), clipBytes(m.RunID, 160)
			view.Messages = append(view.Messages, m)
		}
	}
	view.NextAction, view.Progress.Next = projectTaskNext(t, p, integration, result.Commits, remote)
	if view.PendingRequest != nil {
		// The run waits on the person: they decide, the Orchestrator asks.
		view.NextAction, view.Progress.Next = ResolveTaskPermissionAction, "tell the person what is asked; answer only on their word or under a rule that allows it"
	} else if pebblestore.ProjectTaskLiveApplies(*t, live) && live.State == pebblestore.ProjectTaskStateWaiting {
		view.Progress.Next = "its run starts when the hold ends"
	}
	// The stage stops at the work's end, and it is read from the state
	// only: "Landed" only for a landed task. A finished task is past its
	// implementation steps whatever its workflow or integration says.
	pastImplementation := integration == "delivered" || integration == "integrated" || (view.Progress.Workflow != nil && view.Progress.Workflow.State == "done")
	switch view.State {
	case pebblestore.ProjectTaskStateLanded:
		view.Progress.Stage = "Landed"
	case pebblestore.ProjectTaskStateDone:
		view.Progress.Stage = "Done"
	case pebblestore.ProjectTaskStateNotIntegrated:
		view.Progress.Stage = "Finished, not handed over"
	case pebblestore.ProjectTaskStateNeedsYou:
		if pastImplementation {
			view.Progress.Stage = "Needs you"
		}
	}
	view.Review = projectTaskLatestReview(p, remote)
	cut := projectTaskReportCut{report: report, attempt: reportAttempt, projectID: t.ProjectID, taskID: t.ID, remote: remote}
	markProjectTaskResultCut(&view, cut)
	fitProjectTaskView(&view)
	markProjectTaskResultCut(&view, cut)
	return view
}

// projectTaskLatestReview is the task's latest review verdict: the shown
// delivery's review when it has one, else the newest delivery's that does.
// nil when no review recorded a verdict.
func projectTaskLatestReview(p *projectTaskProgressRead, remote bool) *projectTaskReview {
	if p == nil || p.Workflow == nil {
		return nil
	}
	for _, d := range p.Workflow.Deliveries {
		r := d.Review
		if r == nil || strings.TrimSpace(r.Verdict) == "" {
			continue
		}
		review := &projectTaskReview{Verdict: clipBytes(r.Verdict, 16), Round: max(r.Round, 0), Step: clipBytes(r.Step, 80), DeliveryID: clipBytes(d.ID, 160)}
		if findings := strings.ToValidUTF8(strings.TrimSpace(r.Findings), string(utf8.RuneError)); findings != "" {
			review.Findings = clipBytes(findings, projectTaskTextBytes)
			review.FindingsTruncated = r.FindingsTruncated || review.Findings != findings
			if review.FindingsTruncated && review.DeliveryID != "" {
				review.More = projectTaskFindingsMore(review.DeliveryID, remote)
			}
		}
		return review
	}
	return nil
}

// projectTaskReportCut is the task's report and where it is kept, for the
// call that reads it in full.
type projectTaskReportCut struct {
	report string
	// attempt is the report's attempt index in the record (-1: unknown).
	attempt           int
	projectID, taskID string
	remote            bool
}

// markProjectTaskResultCut says when the view shows less than the task's
// report, how long the report is and how to read it all, in prose
// (read_full) and as the exact call (next): here, get_task detail=full at
// the report's attempt, whose summary is the full report; on a linked
// machine, read_task_session, paged within a message by each cut message's
// next.
func markProjectTaskResultCut(view *ProjectTaskView, cut projectTaskReportCut) {
	if view.Result == nil || cut.report == "" || view.Result.Summary == cut.report {
		return
	}
	view.Result.ResultTruncated, view.Result.ResultBytes = true, len(cut.report)
	if cut.remote {
		view.Result.ReadFull = "call next"
		view.Result.Next = map[string]any{"action": FleetReadTaskSessionAction, "project_id": cut.projectID, "task_id": cut.taskID}
		return
	}
	view.Result.ReadFull = "call next"
	next := map[string]any{"action": "get_task", "project_id": cut.projectID, "task_id": cut.taskID, "detail": ProjectTaskDetailFull}
	if cut.attempt >= 0 {
		next["cursor"], next["limit"] = cut.attempt, 1
	}
	view.Result.Next = next
}

// fitProjectTaskView shortens the report until the view fits its budget.
// A workflow's delivery and findings (bounded on their own) come on top of
// the budget, so they never cut the report.
func fitProjectTaskView(view *ProjectTaskView) {
	budget := projectTaskViewBytes
	if view.Review != nil {
		if raw, err := json.Marshal(view.Review); err == nil {
			budget += len(raw)
		}
	}
	if wf := view.Progress.Workflow; wf != nil {
		extra := projectTaskWorkflow{DeliveryID: wf.DeliveryID, FindingsStep: wf.FindingsStep, Verdict: wf.Verdict, Findings: wf.Findings, FindingsTruncated: wf.FindingsTruncated, More: wf.More}
		if raw, err := json.Marshal(extra); err == nil {
			budget += len(raw)
		}
	}
	for range 4 {
		raw, err := json.Marshal(view)
		if err != nil || len(raw) <= budget || view.Result == nil || view.Result.Summary == "" {
			return
		}
		// JSON escaping can make the encoded text longer than the text.
		keep := len(view.Result.Summary) - (len(raw) - budget) - 16
		if keep < 80 {
			view.Result.Summary = ""
			return
		}
		view.Result.Summary = clipBytes(view.Result.Summary, keep)
	}
}

func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func actionNeededForFailure(t *pebblestore.ProjectTaskRecord) string {
	if projectTaskStatusClass(t.Status) == "failed" {
		return t.ActionNeeded
	}
	return ""
}

// compactProjectTaskRow is the answer row of a task mutation.
func compactProjectTaskRow(t pebblestore.ProjectTaskRecord) ProjectTaskRow {
	row := ProjectTaskRow{ID: t.ID, Title: clipRunes(t.Title, projectTaskTitleRunes), Agent: t.Agent, RunStatus: t.Status, Revision: t.Revision, SessionID: t.SessionID, PlanBinding: projectTaskPlanGuardFor(&t)}
	if t.Archived || projectTaskStatusClass(t.Status) != "finished" {
		row.State, _ = pebblestore.ProjectTaskState(t, "", false, false)
	}
	if t.LastError != "" {
		row.LastError = clipBytes(t.LastError, projectTaskErrorBytes)
	}
	return row
}

// projectTaskRowNext is the next action after a task mutation.
func projectTaskRowNext(t pebblestore.ProjectTaskRecord) string {
	if t.Archived {
		return ""
	}
	switch projectTaskStatusClass(t.Status) {
	case "approval":
		return "approve_task"
	case "waiting", "working":
		return "wait_tasks"
	case "stopped":
		return "message_task"
	case "failed":
		return "get_task"
	}
	return "get_task"
}

// setProjectTaskRow puts the compact mutation answer for t into out.
func setProjectTaskRow(out map[string]any, t pebblestore.ProjectTaskRecord) {
	out["task"], out["more"] = compactProjectTaskRow(t), projectTaskMoreRow
	if next := projectTaskRowNext(t); next != "" {
		out["next_action"] = next
	}
	if model := projectTaskModel(t.Provider, t.Model, t.Thinking); model != "" {
		out["model"] = model
	}
}

// projectTaskRecordFrom reads the task a mutation answer carries.
func projectTaskRecordFrom(value any) (pebblestore.ProjectTaskRecord, bool) {
	switch t := value.(type) {
	case pebblestore.ProjectTaskRecord:
		return t, t.ID != ""
	case *pebblestore.ProjectTaskRecord:
		if t != nil && t.ID != "" {
			return *t, true
		}
	}
	return pebblestore.ProjectTaskRecord{}, false
}

// compactProjectTaskMutation is the default answer to propose_task,
// create_task, approve_task, accept_task and refine_task on this machine:
// the answer's identity and status keys plus the compact task row.
func compactProjectTaskMutation(response map[string]any) map[string]any {
	task, ok := projectTaskRecordFrom(response["task"])
	if !ok {
		return response
	}
	out := map[string]any{}
	for _, key := range []string{"tool", "action", "status", "project_id", "task_id", "task_status"} {
		if value, ok := response[key]; ok {
			out[key] = value
		}
	}
	setProjectTaskRow(out, task)
	return out
}

// projectTaskModel is the resolved provider/model/thinking in one field.
func projectTaskModel(provider, model, thinking string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		model = provider + "/" + model
	}
	if thinking = strings.TrimSpace(thinking); thinking != "" {
		model += " (thinking " + thinking + ")"
	}
	return model
}

// projectTaskAttemptRow is one attempt in get_task detail=attempts.
type projectTaskAttemptRow struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Role      string `json:"role,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	Summary   string `json:"summary,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func projectTaskAttemptRows(attempts []pebblestore.ProjectTaskAttempt) []projectTaskAttemptRow {
	rows := make([]projectTaskAttemptRow, 0, len(attempts))
	for _, a := range attempts {
		rows = append(rows, projectTaskAttemptRow{ID: a.ID, Status: a.Status, Role: a.Role, SessionID: a.SessionID, CreatedAt: a.CreatedAt, Summary: clipBytes(a.Summary, projectTaskTextBytes), LastError: clipBytes(a.LastError, projectTaskTextBytes)})
	}
	return rows
}

// projectTaskPlanDetail is get_task detail=plan: the task's plan fields.
func projectTaskPlanDetail(t pebblestore.ProjectTaskRecord) map[string]any {
	out := map[string]any{"pipeline_stages": t.PipelineStages, "current_stage_index": t.CurrentStageIndex}
	for key, value := range map[string]string{"plan_summary": t.PlanSummary, "full_plan_markdown": t.FullPlanMarkdown, "tier": t.Tier, "outcome_type": t.OutcomeType, "task_program_id": t.TaskProgramID} {
		if value != "" {
			out[key] = value
		}
	}
	if t.PlanBinding != nil {
		out["plan_binding"] = t.PlanBinding
	}
	if t.PlanDocument != nil {
		out["plan_document"] = t.PlanDocument
	}
	return out
}

func clipList(list []string, limit int) []string {
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, clipBytes(item, projectTaskTextBytes))
	}
	return out
}

// projectTaskFilesDetail is get_task detail=files: what changed and what
// was delivered.
//
// A non-media deliverable's status is shown from the task state
// (pebblestore.ProjectTaskDeliverableStatus): its stored status is the
// placeholder set when the task was created.
func projectTaskFilesDetail(t pebblestore.ProjectTaskRecord, worktree *projectTaskWorktree, state string) map[string]any {
	out := map[string]any{}
	if worktree != nil && t.DeliveryAssessment == nil {
		out["head"], out["commits"], out["files"] = worktree.Head, worktree.Commits, clipList(worktree.Files, projectTaskFilesMax)
	}
	if t.WorktreeBranch != "" {
		out["branch"] = t.WorktreeBranch
	}
	if t.BaseCommit != "" {
		out["base_commit"] = t.BaseCommit
	}
	if da := t.DeliveryAssessment; da != nil {
		out["head"], out["files"], out["delivery_state"] = da.SourceOID, clipList(da.Files, projectTaskFilesMax), da.State
		if da.SourceDirty {
			out["uncommitted_files"] = clipList(da.SourceDirtyFiles, projectTaskFilesMax)
		}
	}
	if t.DiffSummary != "" {
		out["diff_summary"] = clipBytes(t.DiffSummary, 4000)
	}
	if len(t.WhatDidDo) > 0 {
		out["what_did_do"] = clipList(t.WhatDidDo, 50)
	}
	if len(t.WhatNotDone) > 0 {
		out["what_not_done"] = clipList(t.WhatNotDone, 50)
	}
	deliverables := []map[string]any{}
	for i, d := range t.Deliverables {
		if i >= 50 {
			break
		}
		deliverables = append(deliverables, map[string]any{"id": d.ID, "title": d.Title, "kind": d.Kind, "status": pebblestore.ProjectTaskDeliverableStatus(d, state)})
	}
	if len(deliverables) > 0 {
		out["deliverables"] = deliverables
	}
	return out
}

// linkedTaskRange is the task's own committed range as a linked machine's
// task route reports it: for a folder task delivered in_folder, its top-level
// commits and files list base..Integration.SourceHead. nil when the answer
// carries neither list, the task has a delivery assessment, or the lists are
// not the route's shape.
func linkedTaskRange(t pebblestore.ProjectTaskRecord, commits, files json.RawMessage) *projectTaskWorktree {
	if t.DeliveryAssessment != nil {
		return nil
	}
	present := func(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }
	if !present(commits) && !present(files) {
		return nil
	}
	w := &projectTaskWorktree{}
	if t.Integration != nil {
		w.Head = t.Integration.SourceHead
	}
	if present(commits) {
		var list []json.RawMessage
		if json.Unmarshal(commits, &list) != nil {
			return nil
		}
		w.Commits = len(list)
	}
	if present(files) && json.Unmarshal(files, &w.Files) != nil {
		return nil
	}
	return w
}

// CompactProjectTaskAnswer compacts a linked machine's answer to a task
// action (the task routes' JSON) into the same view this machine's tools
// return. detail "full" returns the answer unchanged; other details add their
// section to the compact view. Answers without a task keep only their top
// level text and numbers.
func CompactProjectTaskAnswer(action, detail string, raw json.RawMessage) (json.RawMessage, error) {
	if detail == ProjectTaskDetailFull || !projectTaskCompactAction(action) {
		return raw, nil
	}
	var answer struct {
		Status       string           `json:"status"`
		Task         *projectTaskRead `json:"task"`
		ModelPreview *struct {
			ResolvedModel *struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Thinking string `json:"thinking"`
			} `json:"resolved_model"`
			ResolutionError string `json:"resolution_error"`
		} `json:"model_preview"`
		Commits  json.RawMessage                  `json:"commits"`
		Files    json.RawMessage                  `json:"files"`
		Messages []pebblestore.ProjectTaskMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, errors.New("linked machine returned an invalid task answer")
	}
	if answer.Task == nil || answer.Task.ID == "" {
		var top map[string]any
		if err := json.Unmarshal(raw, &top); err != nil {
			return nil, errors.New("linked machine returned an invalid task answer")
		}
		out := map[string]any{}
		for key, value := range top {
			switch v := value.(type) {
			case string:
				out[key] = clipBytes(v, projectTaskErrorBytes)
			case float64, bool:
				out[key] = v
			}
		}
		return json.Marshal(out)
	}
	out := map[string]any{}
	if answer.Status != "" {
		out["status"] = answer.Status
	}
	if action == "get_task" {
		answer.Task.Worktree = linkedTaskRange(answer.Task.ProjectTaskRecord, answer.Commits, answer.Files)
		answer.Task.Messages = answer.Messages
		view := compactProjectTask(*answer.Task, true)
		out["task"] = view
		task := answer.Task.ProjectTaskRecord
		switch detail {
		case ProjectTaskDetailPlan:
			out["plan"] = projectTaskPlanDetail(task)
		case ProjectTaskDetailAttempts:
			attempts := task.Attempts
			if len(attempts) > projectTaskAttemptsMax {
				attempts = attempts[len(attempts)-projectTaskAttemptsMax:]
			}
			out["attempts"] = projectTaskAttemptRows(attempts)
		case ProjectTaskDetailFiles:
			files := projectTaskFilesDetail(task, answer.Task.Worktree, view.State)
			if len(answer.Commits) > 0 && string(answer.Commits) != "null" {
				files["commit_list"] = answer.Commits
			}
			out["files"] = files
		}
		return json.Marshal(out)
	}
	task := answer.Task.ProjectTaskRecord
	out["task"] = compactProjectTaskRow(task)
	out["more"] = projectTaskMoreRow
	if next := projectTaskRowNext(task); next != "" {
		out["next_action"] = next
	}
	if mp := answer.ModelPreview; mp != nil {
		if mp.ResolvedModel != nil {
			if model := projectTaskModel(mp.ResolvedModel.Provider, mp.ResolvedModel.Model, mp.ResolvedModel.Thinking); model != "" {
				out["model"] = model
			}
		}
		if mp.ResolutionError != "" {
			out["model_error"] = clipBytes(mp.ResolutionError, projectTaskErrorBytes)
		}
	}
	return json.Marshal(out)
}
