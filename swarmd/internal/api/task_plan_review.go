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
type taskPlanReviewConflict struct {
	err    error
	review tool.ProjectTaskPlanReview
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

// DecideTaskPlan carries out a person's decision on a task's plan, after a
// fresh read shows it still names the current version. It answers the task's
// review as read after the decision.
func (s *Server) DecideTaskPlan(ctx context.Context, p identity.Principal, d TaskPlanDecision) (tool.ProjectTaskPlanReview, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusForbidden, "only a person decides on a task's plan")
	}
	d.Machine, d.ProjectID, d.TaskID = strings.TrimSpace(d.Machine), strings.TrimSpace(d.ProjectID), strings.TrimSpace(d.TaskID)
	decision := strings.TrimSpace(d.Decision)
	if decision != "accept" && decision != "revise" {
		return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusBadRequest, `decision must be "accept" or "revise"`)
	}
	var guard tool.ProjectTaskApprovalGuards
	if b := d.PlanBinding; b != nil {
		guard = tool.ProjectTaskApprovalGuards{SessionID: strings.TrimSpace(b.SessionID), PlanID: strings.TrimSpace(b.PlanID), DefinitionRevision: b.DefinitionRevision}
	}
	current, err := s.ReadTaskPlanReview(ctx, p, d.Machine, d.ProjectID, d.TaskID)
	if err != nil {
		return tool.ProjectTaskPlanReview{}, err
	}
	if err := tool.ProjectTaskPlanReviewMatches(current, guard); err != nil {
		return current, &taskPlanReviewConflict{err: err, review: current}
	}
	feedback := strings.TrimSpace(d.Feedback)
	if decision == "revise" {
		if current.Decision != tool.ProjectTaskReviewAcceptPlan {
			return current, &taskPlanReviewConflict{err: errors.New("only a plan waiting for acceptance can be sent back for changes"), review: current}
		}
		if feedback == "" {
			return current, linkedTaskFail(http.StatusBadRequest, "say what to change in feedback")
		}
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
		// The task as it is now, so the person sees what changed.
		fresh, readErr := s.ReadTaskPlanReview(ctx, p, d.Machine, d.ProjectID, d.TaskID)
		if readErr != nil {
			fresh = current
		}
		return fresh, &taskPlanReviewConflict{err: fmt.Errorf("%s refused: %w", decision, err), review: fresh}
	}
	after, err := s.ReadTaskPlanReview(ctx, p, d.Machine, d.ProjectID, d.TaskID)
	if err != nil {
		// Decided; only the read after it failed.
		return current, nil
	}
	return after, nil
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
	review, err := s.DecideTaskPlan(r.Context(), principal, d)
	if err != nil {
		var conflict *taskPlanReviewConflict
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": conflict.Error(), "review": conflict.review})
			return
		}
		writeError(w, linkedTaskStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "decision": strings.TrimSpace(d.Decision), "review": review})
}
