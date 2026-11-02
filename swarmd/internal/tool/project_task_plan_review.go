package tool

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Task plan review: the one view of a task's plan a person reads before they
// accept it to start, built from the task record its machine returned (the
// home's own task, or a linked machine's through the link). The client route
// (GET /v3/task-plan-review) answers it whole; get_task detail=plan carries
// the same decision and problem next to the plan_document it already shows,
// so the Orchestrator, Swarm Control and a client read one rule.
//
// The plan is the task's structured plan_document, unchanged, with markdown
// derived from it field by field (ProjectTaskPlanMarkdown). The router's
// plan_summary and full_plan_markdown are never shown in its place: a task
// bound to a plan whose document is missing or names another plan cannot be
// accepted until the current version is read.

// Plan review decisions: what the person can decide on the task now.
const (
	// ProjectTaskReviewAcceptPlan: the task waits for its bound plan to be
	// accepted at exactly plan_binding's definition_revision.
	ProjectTaskReviewAcceptPlan = "accept_plan"
	// ProjectTaskReviewStart: an ordinary task (no plan) waits to start.
	ProjectTaskReviewStart = "start"
	// ProjectTaskReviewReload: it waits, but its current plan could not be
	// read whole; nothing can be accepted until it is.
	ProjectTaskReviewReload = "reload"
	// ProjectTaskReviewNone: nothing waits on the person (already started,
	// finished, being revised).
	ProjectTaskReviewNone = "none"
)

// ProjectTaskPlanBindingView is the exact plan version a decision names.
// The binding's receipt stays on its machine.
type ProjectTaskPlanBindingView struct {
	SessionID          string `json:"session_id"`
	PlanID             string `json:"plan_id"`
	DefinitionRevision int    `json:"definition_revision"`
}

// ProjectTaskPlanReview is the review a client shows.
type ProjectTaskPlanReview struct {
	// Machine is the linked machine the task is on; empty for this Swarm.
	Machine   string `json:"machine,omitempty"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	// RunStatus is the task record's status (pending_approval, planning,
	// in_progress, ...), as its machine reported it.
	RunStatus    string                           `json:"run_status"`
	TaskRevision int                              `json:"task_revision,omitempty"`
	OriginID     string                           `json:"origin_session_id,omitempty"`
	PlanBound    bool                             `json:"plan_bound"`
	PlanBinding  *ProjectTaskPlanBindingView      `json:"plan_binding,omitempty"`
	PlanDocument *pebblestore.SessionPlanDocument `json:"plan_document,omitempty"`
	// PlanMarkdown is derived from PlanDocument only.
	PlanMarkdown string `json:"plan_markdown,omitempty"`
	Decision     string `json:"decision"`
	// Problem says why nothing can be accepted, when Decision is reload or none.
	Problem string `json:"problem,omitempty"`
	// LastError is the task's own last error, as its machine reported it.
	LastError string `json:"last_error,omitempty"`
	// UntrustedContent: the plan and texts are agent-written.
	UntrustedContent bool `json:"untrusted_content"`
}

// projectTaskPlanBound reports whether the task is a plan task: it has a
// plan binding, or is a plan outcome that must have one.
func projectTaskPlanBound(t pebblestore.ProjectTaskRecord) bool {
	return (t.PlanBinding != nil && strings.TrimSpace(t.PlanBinding.PlanID) != "") || t.Agent == "plan" || t.OutcomeType == "plan_spec"
}

// ProjectTaskPlanReviewDecision is the decision rule: what the person can
// decide on the task now, and why not when nothing.
func ProjectTaskPlanReviewDecision(t pebblestore.ProjectTaskRecord) (string, string) {
	if t.Archived {
		return ProjectTaskReviewNone, "the task is archived"
	}
	switch t.Status {
	case "pending_approval":
	case "planning":
		return ProjectTaskReviewNone, "its plan is being written or revised; a new version comes for review"
	default:
		return ProjectTaskReviewNone, fmt.Sprintf("the task is %s: nothing waits for acceptance", strings.TrimSpace(t.Status))
	}
	if !projectTaskPlanBound(t) {
		return ProjectTaskReviewStart, ""
	}
	b := t.PlanBinding
	switch {
	case b == nil || strings.TrimSpace(b.PlanID) == "":
		return ProjectTaskReviewReload, "the task is a plan task without a submitted plan; it cannot be accepted"
	case strings.TrimSpace(b.SessionID) == "" || b.DefinitionRevision <= 0:
		return ProjectTaskReviewReload, "the plan binding names no exact session and version; it cannot be accepted"
	case t.SessionID != "" && b.SessionID != t.SessionID:
		return ProjectTaskReviewReload, "the plan binding names another session than the task's; it cannot be accepted"
	case t.PlanDocument == nil:
		return ProjectTaskReviewReload, "the current version of the plan could not be read; read it again before deciding"
	case strings.TrimSpace(t.PlanDocument.ID) != "" && strings.TrimSpace(t.PlanDocument.ID) != b.PlanID:
		return ProjectTaskReviewReload, "the plan read is not the plan the task is bound to; read it again before deciding"
	}
	return ProjectTaskReviewAcceptPlan, ""
}

// BuildProjectTaskPlanReview is the review of one task record from machine
// ("" for this Swarm).
func BuildProjectTaskPlanReview(machine string, t pebblestore.ProjectTaskRecord) ProjectTaskPlanReview {
	out := ProjectTaskPlanReview{
		Machine: strings.TrimSpace(machine), ProjectID: t.ProjectID, TaskID: t.ID, Title: t.Title,
		RunStatus: t.Status, TaskRevision: t.Revision, OriginID: t.OriginSessionID,
		PlanBound: projectTaskPlanBound(t), LastError: clipBytes(t.LastError, projectTaskErrorBytes), UntrustedContent: true,
	}
	if b := t.PlanBinding; b != nil && strings.TrimSpace(b.PlanID) != "" {
		out.PlanBinding = &ProjectTaskPlanBindingView{SessionID: b.SessionID, PlanID: b.PlanID, DefinitionRevision: b.DefinitionRevision}
	}
	out.Decision, out.Problem = ProjectTaskPlanReviewDecision(t)
	if out.PlanBound && t.PlanDocument != nil && out.Decision != ProjectTaskReviewReload {
		out.PlanDocument = t.PlanDocument
		out.PlanMarkdown = ProjectTaskPlanMarkdown(t.PlanDocument)
	}
	return out
}

// ProjectTaskPlanReviewMatches reports whether a decision names the review's
// exact current plan version. An ordinary task's decision names none.
func ProjectTaskPlanReviewMatches(review ProjectTaskPlanReview, guard ProjectTaskApprovalGuards) error {
	switch review.Decision {
	case ProjectTaskReviewAcceptPlan:
		b := review.PlanBinding
		if strings.TrimSpace(guard.SessionID) == "" || strings.TrimSpace(guard.PlanID) == "" || guard.DefinitionRevision <= 0 {
			return fmt.Errorf("accepting a plan names its exact version: plan_binding session_id, plan_id and definition_revision (current %d)", b.DefinitionRevision)
		}
		if guard.SessionID != b.SessionID || guard.PlanID != b.PlanID || guard.DefinitionRevision != b.DefinitionRevision {
			return fmt.Errorf("the plan changed: the version decided on is not the current one (current definition_revision %d); read it again and decide on it", b.DefinitionRevision)
		}
		return nil
	case ProjectTaskReviewStart:
		if strings.TrimSpace(guard.PlanID) != "" || guard.DefinitionRevision > 0 {
			return fmt.Errorf("the task has no plan to accept")
		}
		return nil
	}
	if review.Problem != "" {
		return fmt.Errorf("nothing can be decided now: %s", review.Problem)
	}
	return fmt.Errorf("nothing can be decided now")
}

// ProjectTaskPlanMarkdown renders a plan document as markdown, every authored
// field in order, nothing added that the document does not say.
func ProjectTaskPlanMarkdown(doc *pebblestore.SessionPlanDocument) string {
	if doc == nil {
		return ""
	}
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	text := func(heading, value string) {
		if value = strings.TrimSpace(value); value != "" {
			line("\n## %s\n\n%s", heading, value)
		}
	}
	items := func(heading string, values []string) {
		kept := make([]string, 0, len(values))
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			return
		}
		line("\n## %s\n", heading)
		for _, v := range kept {
			line("- %s", v)
		}
	}
	title := strings.TrimSpace(doc.Title)
	if title == "" {
		title = "Plan"
	}
	line("# %s", title)
	text("Objective", doc.Info.Goal)
	text("Scope", doc.Info.Scope)
	text("Context", doc.Info.Context)
	items("Decisions", doc.Info.Decisions)
	items("Constraints", doc.Info.Constraints)
	items("Assumptions", doc.Info.Assumptions)
	items("Open questions", doc.Info.OpenQuestions)
	items("Relevant files", doc.Info.RelevantFiles)
	if len(doc.Requirements) > 0 {
		line("\n## Requirements\n")
		for _, r := range doc.Requirements {
			where := ""
			if cp := strings.TrimSpace(r.CheckpointID); cp != "" {
				where = " (checkpoint " + cp + ")"
			}
			line("- **%s** %s%s", strings.TrimSpace(r.ID), strings.TrimSpace(r.Text), where)
		}
	}
	items("Requirement changes", doc.RequirementChanges)
	if len(doc.Checkpoints) > 0 {
		line("\n## Checkpoints")
		for i, cp := range doc.Checkpoints {
			name := strings.TrimSpace(cp.Title)
			if name == "" {
				name = strings.TrimSpace(cp.ID)
			}
			line("\n### %d. %s", i+1, name)
			if id := strings.TrimSpace(cp.ID); id != "" {
				line("\nid: `%s`", id)
			}
			if o := strings.TrimSpace(cp.Objective); o != "" {
				line("\n%s", o)
			}
			tasks := cp.Tasks
			if len(tasks) == 0 {
				for _, st := range cp.Subtasks {
					tasks = append(tasks, st.Title)
				}
			}
			if len(tasks) > 0 {
				line("\nTasks:\n")
				for _, task := range tasks {
					if task = strings.TrimSpace(task); task != "" {
						line("- %s", task)
					}
				}
			}
			if len(cp.AcceptanceCriteria) > 0 {
				line("\nAcceptance criteria:\n")
				for _, c := range cp.AcceptanceCriteria {
					if c = strings.TrimSpace(c); c != "" {
						line("- %s", c)
					}
				}
			}
			if len(cp.Validation) > 0 {
				line("\nValidation:\n")
				for _, v := range cp.Validation {
					if v = strings.TrimSpace(v); v != "" {
						line("- %s", v)
					}
				}
			}
		}
	}
	items("Success criteria", doc.Info.SuccessCriteria)
	text("Validation", doc.Info.ValidationStrategy)
	return strings.TrimSpace(b.String()) + "\n"
}
