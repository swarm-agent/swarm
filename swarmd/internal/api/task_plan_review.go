package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Task plan review for a client: the person reads a waiting task's complete
// plan and accepts exactly that version, or asks for changes. One service
// serves this Swarm's own tasks and the tasks its sessions sent to linked
// machines (machine set), where the link reads and decides on the machine's
// own task routes (CallFleetProject get_task, approve_task, refine_task).
//
//	GET  /v3/task-plan-review?project_id=&task_id=[&machine=]
//	     the review (tool.ProjectTaskPlanReview)
//	POST /v3/task-plan-review/decision
//	     {"machine"?, "project_id", "task_id", "decision": "accept"|"revise",
//	      "plan_binding": {"session_id", "plan_id", "definition_revision"}, "feedback"?}
//
// A decision is checked against a fresh read of the task first: a plan that
// changed, was replaced or is gone is refused (409, with the current review),
// never accepted in place of the one the person read. Accepting names the
// exact plan binding; the machine's approve route checks it again, so a
// change between the read and the approval is refused there. A task that
// already started answers 409 with its current state: a repeated click never
// starts a second run. Only a user decides; the route reads the task with
// the caller's own account, and a linked task only when a session of the
// caller (or the account's Swarm Control caller) sent it.
//
// The answers say exactly what is known (TaskPlanDecisionResult):
//
//   - 200 ok: the decision was carried out. "decided" names the task and the
//     exact version decided on. review_current true: "review" is the task as
//     read after it. review_current false: that read failed
//     ("refresh_error"); no review is sent, never the one from before the
//     decision, and the client reads again before anything else.
//   - 409 ok false: refused because of the task's state (the plan changed,
//     the task started); "review" is the current one when review_current.
//   - 400/403/404: refused for that reason (linked work off, not found, not
//     sent from here); nothing was done.
//   - 502 outcome_unknown: the decision was sent but no answer says whether
//     it was carried out (the link timed out, the machine failed). It is
//     never retried; the client reads the task again.

const (
	taskPlanReviewPath         = "/v3/task-plan-review"
	taskPlanReviewDecisionPath = "/v3/task-plan-review/decision"
)

// TaskPlanDecision is a person's decision on a task's plan.
type TaskPlanDecision struct {
	Machine     string                           `json:"machine,omitempty"`
	ProjectID   string                           `json:"project_id"`
	TaskID      string                           `json:"task_id"`
	Decision    string                           `json:"decision"`
	PlanBinding *tool.ProjectTaskPlanBindingView `json:"plan_binding,omitempty"`
	Feedback    string                           `json:"feedback,omitempty"`
}

// taskPlanReviewConflict carries the current review with a refused decision.
// current is false when the task could not be read after the refusal: then
// no review is answered.
type taskPlanReviewConflict struct {
	err     error
	review  tool.ProjectTaskPlanReview
	current bool
}

func (e *taskPlanReviewConflict) Error() string { return e.err.Error() }

func (s *Server) registerTaskPlanReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc(taskPlanReviewPath, s.handleTaskPlanReview)
	mux.HandleFunc(taskPlanReviewDecisionPath, s.handleTaskPlanReview)
}

// ReadTaskPlanReview reads one task's plan review: this Swarm's task when
// machine is empty, else the linked machine's through the link.
func (s *Server) ReadTaskPlanReview(ctx context.Context, p identity.Principal, machine, projectID, taskID string) (tool.ProjectTaskPlanReview, error) {
	machine, projectID, taskID = strings.TrimSpace(machine), strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if !p.Valid() {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	if projectID == "" || taskID == "" {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusBadRequest, "project_id and task_id are required")
	}
	if machine == "" {
		return s.readHomeTaskPlanReview(p, projectID, taskID)
	}
	return s.readLinkedTaskPlanReview(ctx, p, machine, projectID, taskID)
}

func (s *Server) readHomeTaskPlanReview(p identity.Principal, projectID, taskID string) (tool.ProjectTaskPlanReview, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusServiceUnavailable, "project store unavailable")
	}
	db := s.sessions.Store()
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusInternalServerError, "read task: %v", err)
	}
	if !found || task == nil || (task.AccountID != "" && task.AccountID != p.AccountScopeID) || (task.ProjectID != "" && task.ProjectID != projectID) {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusNotFound, "task %s not found", taskID)
	}
	record := *task
	record.PlanDocument = nil
	// The document is the bound plan's current one, read here; a stored copy
	// is never shown in its place.
	hydrateTaskPlanDocument(&record, db)
	return tool.BuildProjectTaskPlanReview("", record), nil
}

// linkedTaskSentBy reports whether the account's record of the linked task
// was sent by the principal: by one of their sessions, or by the account's
// Swarm Control caller.
func (s *Server) linkedTaskSentBy(p identity.Principal, machine, projectID, taskID string) error {
	record, found, err := s.fleet.GetLinkedTask(p.AccountScopeID, machine, projectID, taskID)
	if err != nil {
		return linkedTaskFail(http.StatusInternalServerError, "read linked task record: %v", err)
	}
	if !found {
		return linkedTaskFail(http.StatusNotFound, "no task %s sent to %s from here", taskID, machine)
	}
	if record.HomeSessionID != "" {
		session, ok, err := s.sessions.GetSession(record.HomeSessionID)
		if err != nil {
			return linkedTaskFail(http.StatusInternalServerError, "read session: %v", err)
		}
		if !ok || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
			return linkedTaskFail(http.StatusNotFound, "no task %s sent to %s from here", taskID, machine)
		}
	}
	return nil
}

func (s *Server) readLinkedTaskPlanReview(ctx context.Context, p identity.Principal, machine, projectID, taskID string) (tool.ProjectTaskPlanReview, error) {
	if s == nil || s.fleet == nil || s.fleetTokens == nil || s.sessions == nil {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	if err := s.linkedTaskSentBy(p, machine, projectID, taskID); err != nil {
		return tool.ProjectTaskPlanReview{}, err
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusInternalServerError, "read permissions: %v", err)
	}
	if !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusForbidden, "linked work on %s is off (Permissions: linked machines); its task cannot be read or decided here", machine)
	}
	// The read also refreshes the home's record of the task.
	raw, err := s.CallFleetProject(ctx, p, machine, "get_task", map[string]any{"project_id": projectID, "task_id": taskID})
	if err != nil {
		var refused *fleet.Error
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusNotFound, "task %s not found on %s", taskID, machine)
		}
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusBadGateway, "%v", err)
	}
	var answer struct {
		Task *pebblestore.ProjectTaskRecord `json:"task"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.Task == nil || answer.Task.ID != taskID || (answer.Task.ProjectID != "" && answer.Task.ProjectID != projectID) {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusBadGateway, "%s returned an unexpected record for task %s", machine, taskID)
	}
	if answer.Task.ProjectID == "" {
		answer.Task.ProjectID = projectID
	}
	return tool.BuildProjectTaskPlanReview(machine, *answer.Task), nil
}

// TaskPlanDecided names the task and the exact plan version a carried-out
// (or possibly carried-out) decision was on. PlanBinding is nil for an
// ordinary task started without a plan.
type TaskPlanDecided struct {
	Machine     string                           `json:"machine,omitempty"`
	ProjectID   string                           `json:"project_id"`
	TaskID      string                           `json:"task_id"`
	PlanBinding *tool.ProjectTaskPlanBindingView `json:"plan_binding,omitempty"`
}

// TaskPlanDecisionResult is a carried-out decision. Review is the task read
// after it, or nil with RefreshError when that read failed: the decision
// stands, and its state is unknown until read again.
type TaskPlanDecisionResult struct {
	Decision     string
	Decided      TaskPlanDecided
	Review       *tool.ProjectTaskPlanReview
	RefreshError string
}

// taskPlanDecisionUnknown: the decision was sent and no answer says whether
// it was carried out.
type taskPlanDecisionUnknown struct {
	err     error
	decided TaskPlanDecided
}

func (e *taskPlanDecisionUnknown) Error() string { return e.err.Error() }

// taskPlanDecisionFailure sorts a refused or failed approve/refine call:
// a refusal because of the task's state is a conflict (409, with the current
// review); a permission, not-found or request refusal keeps its status; a
// linked call with no definite answer (no reply, a timeout, the machine's
// own failure) is outcome-unknown. This Swarm's own services answer
// definitely: their refusals happen before anything is changed.
func taskPlanDecisionFailure(machine string, err error) (status int, unknown bool) {
	if machine == "" {
		var typed *linkedTaskError
		if errors.As(err, &typed) {
			return typed.status, false
		}
		switch msg := err.Error(); {
		case strings.Contains(msg, "forbidden"):
			return http.StatusForbidden, false
		case strings.Contains(msg, "not found"):
			return http.StatusNotFound, false
		}
		return http.StatusConflict, false
	}
	var refused *fleet.Error
	if !errors.As(err, &refused) {
		return http.StatusBadGateway, true
	}
	switch {
	case refused.Status == http.StatusForbidden:
		return http.StatusForbidden, false
	case refused.Status == http.StatusNotFound:
		return http.StatusNotFound, false
	case refused.Status == http.StatusUnauthorized:
		// The machine refused the link's own session: nothing ran there.
		return http.StatusBadGateway, false
	case refused.Status >= 400 && refused.Status < 500:
		// The machine's approve/refine refuses a stale binding or a task
		// that is not waiting with 400 or 409.
		return http.StatusConflict, false
	}
	return http.StatusBadGateway, true
}

// DecideTaskPlan carries out a person's decision on a task's plan, after a
// fresh read shows it still names the current version. It answers what was
// decided and the task as read after it.
func (s *Server) DecideTaskPlan(ctx context.Context, p identity.Principal, d TaskPlanDecision) (TaskPlanDecisionResult, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" {
		return TaskPlanDecisionResult{}, linkedTaskFail(http.StatusForbidden, "only a person decides on a task's plan")
	}
	d.Machine, d.ProjectID, d.TaskID = strings.TrimSpace(d.Machine), strings.TrimSpace(d.ProjectID), strings.TrimSpace(d.TaskID)
	decision := strings.TrimSpace(d.Decision)
	if decision != "accept" && decision != "revise" {
		return TaskPlanDecisionResult{}, linkedTaskFail(http.StatusBadRequest, `decision must be "accept" or "revise"`)
	}
	var guard tool.ProjectTaskApprovalGuards
	if b := d.PlanBinding; b != nil {
		guard = tool.ProjectTaskApprovalGuards{SessionID: strings.TrimSpace(b.SessionID), PlanID: strings.TrimSpace(b.PlanID), DefinitionRevision: b.DefinitionRevision}
	}
	current, err := s.ReadTaskPlanReview(ctx, p, d.Machine, d.ProjectID, d.TaskID)
	if err != nil {
		return TaskPlanDecisionResult{}, err
	}
	if err := tool.ProjectTaskPlanReviewMatches(current, guard); err != nil {
		return TaskPlanDecisionResult{}, &taskPlanReviewConflict{err: err, review: current, current: true}
	}
	feedback := strings.TrimSpace(d.Feedback)
	if decision == "revise" {
		if current.Decision != tool.ProjectTaskReviewAcceptPlan {
			return TaskPlanDecisionResult{}, &taskPlanReviewConflict{err: errors.New("only a plan waiting for acceptance can be sent back for changes"), review: current, current: true}
		}
		if feedback == "" {
			return TaskPlanDecisionResult{}, linkedTaskFail(http.StatusBadRequest, "say what to change in feedback")
		}
	}
	decided := TaskPlanDecided{Machine: d.Machine, ProjectID: d.ProjectID, TaskID: d.TaskID}
	if current.Decision == tool.ProjectTaskReviewAcceptPlan {
		decided.PlanBinding = &tool.ProjectTaskPlanBindingView{SessionID: guard.SessionID, PlanID: guard.PlanID, DefinitionRevision: guard.DefinitionRevision}
	}
	if d.Machine == "" {
		switch decision {
		case "accept":
			guards := []tool.ProjectTaskApprovalGuards{}
			if current.Decision == tool.ProjectTaskReviewAcceptPlan {
				guards = append(guards, guard)
			}
			_, err = s.ApproveProjectTask(ctx, p, d.ProjectID, d.TaskID, guards...)
		case "revise":
			_, err = s.RefineBoundProjectTask(ctx, p, d.ProjectID, d.TaskID, guard, feedback)
		}
	} else {
		args := map[string]any{"project_id": d.ProjectID, "task_id": d.TaskID}
		if current.Decision == tool.ProjectTaskReviewAcceptPlan {
			args["session_id"], args["plan_id"], args["definition_revision"] = guard.SessionID, guard.PlanID, guard.DefinitionRevision
		}
		action := "approve_task"
		if decision == "revise" {
			action = "refine_task"
			args["feedback"] = feedback
		}
		_, err = s.CallFleetProject(ctx, p, d.Machine, action, args)
	}
	if err != nil {
		status, unknown := taskPlanDecisionFailure(d.Machine, err)
		if unknown {
			return TaskPlanDecisionResult{}, &taskPlanDecisionUnknown{err: fmt.Errorf("the %s was sent but its outcome is unknown: %w; read the task again before deciding (it is not retried)", decision, err), decided: decided}
		}
		if status != http.StatusConflict {
			return TaskPlanDecisionResult{}, linkedTaskFail(status, "%s refused: %v", decision, err)
		}
		// The task as it is now, so the person sees what changed. When that
		// read fails too, no review is sent: the one from before is not
		// current.
		conflict := &taskPlanReviewConflict{err: fmt.Errorf("%s refused: %w", decision, err)}
		if fresh, readErr := s.ReadTaskPlanReview(ctx, p, d.Machine, d.ProjectID, d.TaskID); readErr == nil {
			conflict.review = fresh
			conflict.current = true
		}
		return TaskPlanDecisionResult{}, conflict
	}
	result := TaskPlanDecisionResult{Decision: decision, Decided: decided}
	refresh := s.ReadTaskPlanReview
	if s.taskPlanRefresh != nil {
		refresh = s.taskPlanRefresh
	}
	after, err := refresh(ctx, p, d.Machine, d.ProjectID, d.TaskID)
	if err != nil {
		// Decided; only the read after it failed. The state is unknown,
		// never the review from before the decision.
		result.RefreshError = err.Error()
		return result, nil
	}
	result.Review = &after
	return result, nil
}

func (s *Server) handleTaskPlanReview(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read or decide task plans"))
		return
	}
	if r.URL.Path == taskPlanReviewPath {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		q := r.URL.Query()
		review, err := s.ReadTaskPlanReview(r.Context(), principal, q.Get("machine"), q.Get("project_id"), q.Get("task_id"))
		if err != nil {
			writeError(w, linkedTaskStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"review": review})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
		return
	}
	var d TaskPlanDecision
	if !readProjectTaskJSON(w, r, &d) {
		return
	}
	result, err := s.DecideTaskPlan(r.Context(), principal, d)
	if err != nil {
		var conflict *taskPlanReviewConflict
		if errors.As(err, &conflict) {
			out := map[string]any{"ok": false, "error": conflict.Error(), "review_current": conflict.current}
			if conflict.current {
				out["review"] = conflict.review
			}
			writeJSON(w, http.StatusConflict, out)
			return
		}
		var unknown *taskPlanDecisionUnknown
		if errors.As(err, &unknown) {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "outcome_unknown": true, "error": unknown.Error(), "decision": strings.TrimSpace(d.Decision), "decided": unknown.decided})
			return
		}
		writeError(w, linkedTaskStatus(err), err)
		return
	}
	out := map[string]any{"ok": true, "decision": result.Decision, "decided": result.Decided, "review_current": result.Review != nil}
	if result.Review != nil {
		out["review"] = result.Review
	} else {
		out["refresh_error"] = result.RefreshError
	}
	writeJSON(w, http.StatusOK, out)
}
