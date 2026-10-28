package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	State    string `json:"state"`
	Summary  string `json:"summary"`
	Workflow *struct {
		State      string `json:"state"`
		Deliveries []struct {
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
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Agent       string                `json:"agent,omitempty"`
	Status      string                `json:"status"`
	Revision    int                   `json:"revision,omitempty"`
	UpdatedAt   int64                 `json:"updated_at,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	Progress    projectTaskProgress   `json:"progress"`
	Result      *projectTaskResult    `json:"result,omitempty"`
	Integration string                `json:"integration,omitempty"`
	LastError   string                `json:"last_error,omitempty"`
	PlanBinding *projectTaskPlanGuard `json:"plan_binding,omitempty"`
	NextAction  string                `json:"next_action"`
	More        string                `json:"more"`
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
}

type projectTaskResult struct {
	Summary      string   `json:"summary,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Head         string   `json:"head,omitempty"`
	Commits      int      `json:"commits"`
	Files        int      `json:"files"`
	Deliverables []string `json:"deliverables,omitempty"`
}

// projectTaskPlanGuard is what approve_task/refine_task need for a task
// waiting on a bound plan.
type projectTaskPlanGuard struct {
	SessionID          string `json:"session_id,omitempty"`
	PlanID             string `json:"plan_id,omitempty"`
	DefinitionRevision int    `json:"definition_revision,omitempty"`
}

// ProjectTaskRow is the answer to a task mutation: the task's identity and
// status and what to do next.
type ProjectTaskRow struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Agent       string                `json:"agent,omitempty"`
	Status      string                `json:"status"`
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
	case "pending_approval":
		return "approval"
	case "queued", "pending":
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
		return "none", "archived"
	}
	switch projectTaskStatusClass(t.Status) {
	case "approval":
		return "approve_task", "starts when approved"
	case "waiting":
		return "wait_tasks", "starts when capacity frees"
	case "working":
		return "wait_tasks", "agent finishes, then its result is ready to review"
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
		return "none", "done"
	case commits > 0 && remote:
		return "read_task_session", "review the result; integrating its commits is done on that machine"
	case commits > 0:
		return "integrate_task", "integrate its commits into the target branch"
	}
	return "none", "done; use the result"
}

// compactProjectTask is the default get_task view of a task.
func compactProjectTask(read projectTaskRead, remote bool) ProjectTaskView {
	t := &read.ProjectTaskRecord
	t.EnsureTaskAttempts()
	view := ProjectTaskView{ID: t.ID, Title: clipRunes(t.Title, projectTaskTitleRunes), Agent: t.Agent, Status: t.Status, Revision: t.Revision, UpdatedAt: t.UpdatedAt, SessionID: t.SessionID, PlanBinding: projectTaskPlanGuardFor(t), More: projectTaskMoreGet}
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
			d := p.Workflow.Deliveries[0]
			for _, candidate := range p.Workflow.Deliveries {
				if candidate.State == "running" {
					d = candidate
					break
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
		}
		if wf.Reason == "" && (wf.State == "blocked" || wf.State == "failed") {
			if _, reason, ok := strings.Cut(p.Summary, ": "); ok {
				wf.Reason = clipBytes(reason, projectTaskTextBytes)
			}
		}
		view.Progress.Workflow = wf
	}

	result := &projectTaskResult{Branch: t.WorktreeBranch, Commits: max(t.UnintegratedCommits, 0)}
	if a := t.ActiveAttempt(); a != nil && strings.TrimSpace(a.Summary) != "" {
		result.Summary = clipBytes(a.Summary, projectTaskReportBytes)
	} else {
		for i := len(t.Attempts) - 1; i >= 0; i-- {
			if strings.TrimSpace(t.Attempts[i].Summary) != "" {
				result.Summary = clipBytes(t.Attempts[i].Summary, projectTaskReportBytes)
				break
			}
		}
	}
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
	for i, d := range t.Deliverables {
		if i >= projectTaskListMax {
			break
		}
		result.Deliverables = append(result.Deliverables, clipBytes(firstNonEmptyText(d.Title, d.ID), 80))
	}
	if result.Summary != "" || result.Branch != "" || result.Head != "" || result.Commits > 0 || result.Files > 0 || len(result.Deliverables) > 0 {
		view.Result = result
	}
	view.Integration = integration
	view.NextAction, view.Progress.Next = projectTaskNext(t, p, integration, result.Commits, remote)
	fitProjectTaskView(&view)
	return view
}

// fitProjectTaskView shortens the report until the view fits its budget.
func fitProjectTaskView(view *ProjectTaskView) {
	for range 4 {
		raw, err := json.Marshal(view)
		if err != nil || len(raw) <= projectTaskViewBytes || view.Result == nil || view.Result.Summary == "" {
			return
		}
		// JSON escaping can make the encoded text longer than the text.
		keep := len(view.Result.Summary) - (len(raw) - projectTaskViewBytes) - 16
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
	row := ProjectTaskRow{ID: t.ID, Title: clipRunes(t.Title, projectTaskTitleRunes), Agent: t.Agent, Status: t.Status, Revision: t.Revision, SessionID: t.SessionID, PlanBinding: projectTaskPlanGuardFor(&t)}
	if t.LastError != "" {
		row.LastError = clipBytes(t.LastError, projectTaskErrorBytes)
	}
	return row
}

// projectTaskRowNext is the next action after a task mutation.
func projectTaskRowNext(t pebblestore.ProjectTaskRecord) string {
	if t.Archived {
		return "none"
	}
	switch projectTaskStatusClass(t.Status) {
	case "approval":
		return "approve_task"
	case "waiting", "working":
		return "wait_tasks"
	case "failed":
		return "get_task"
	}
	return "get_task"
}

// setProjectTaskRow puts the compact mutation answer for t into out.
func setProjectTaskRow(out map[string]any, t pebblestore.ProjectTaskRecord) {
	out["task"], out["next_action"], out["more"] = compactProjectTaskRow(t), projectTaskRowNext(t), projectTaskMoreRow
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
func projectTaskFilesDetail(t pebblestore.ProjectTaskRecord, worktree *projectTaskWorktree) map[string]any {
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
		deliverables = append(deliverables, map[string]any{"id": d.ID, "title": d.Title, "kind": d.Kind, "status": d.Status})
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
		Commits json.RawMessage `json:"commits"`
		Files   json.RawMessage `json:"files"`
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
		out["task"] = compactProjectTask(*answer.Task, true)
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
			files := projectTaskFilesDetail(task, answer.Task.Worktree)
			if len(answer.Commits) > 0 && string(answer.Commits) != "null" {
				files["commit_list"] = answer.Commits
			}
			out["files"] = files
		}
		return json.Marshal(out)
	}
	task := answer.Task.ProjectTaskRecord
	out["task"] = compactProjectTaskRow(task)
	out["next_action"], out["more"] = projectTaskRowNext(task), projectTaskMoreRow
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
